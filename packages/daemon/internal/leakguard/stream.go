package leakguard

import (
	"context"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sjawhar/legion/daemon/internal/procgroup"
)

// readOutput runs c under ctx and hands read the child's standard output as the child writes it,
// so that no copy of the whole output is held: a push's added paths and diff are read this way
// (Guard.gitWith). It answers the child's exit status, and fails as run does: a child that did not
// start, one the end of ctx stopped (a *stoppedError), one a signal ended; and, after those, an
// error read answered. An error of read closes the pipe, which ends the child's writes.
//
// The standard output is a pipe this function owns, its write end handed to the child as a file,
// so exec copies none of it and Wait, run in a goroutine of its own, returns when the child exits
// (and its standard error is copied, up to procgroup.WaitDelay after that). read reads the pipe in
// this goroutine until one of three ends: the pipe's end, once the child and everything that
// inherited the pipe have closed it; ctx's end, when the pipe's read deadline is set; and the
// child's exit, procgroup.WaitDelay after Wait returns (pipeReader.endAfterExit). The last is the
// wait exec gives a child's output when it copies the output itself: a process the child started
// that outlives it and holds the pipe open is not waited for, in or out of the child's process
// group, with or without a deadline.
func (g *Guard) readOutput(ctx context.Context, c child, read func(io.Reader) error) (int, error) {
	cmd, killed := g.command(ctx, c)
	cmd.Stderr = c.stderr
	out, in, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	defer out.Close()
	cmd.Stdout = in
	started := time.Now()
	err = cmd.Start()
	// The child holds its own copy of the write end; with this one closed, the reader sees the
	// pipe's end once the child and everything that inherited it have closed theirs.
	_ = in.Close()
	if err != nil {
		return 0, err
	}
	waited := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(waited)
	}()
	reader := &pipeReader{ctx: ctx, file: out}
	stop := context.AfterFunc(ctx, func() { _ = out.SetReadDeadline(time.Now()) })
	finished := make(chan struct{})
	go reader.endAfterExit(waited, finished)
	readErr := read(reader)
	close(finished)
	stop()
	// A reader that stopped early leaves the child writing into a pipe nobody reads: closed, its
	// next write fails and it ends, rather than holding up the wait below.
	_ = out.Close()
	<-waited
	switch {
	// A read the context's end stopped is a stop like the group's: the child may have exited
	// before the context ended, and exec then runs no Cancel, so killed alone would not see it.
	case killed(), readErr != nil && ctx.Err() != nil:
		return 0, stopped(ctx, c, started)
	case readErr != nil:
		return 0, readErr
	}
	if err := procgroup.Err(waitErr); err != nil {
		return 0, err
	}
	return cmd.ProcessState.ExitCode(), nil
}

// pipeReader is a child's standard output as readOutput hands it to its reader: the pipe, read as
// it is written, until the pipe's end, ctx's end, or the child's exit and procgroup.WaitDelay after
// it (endAfterExit), whichever comes first.
//
// Once the child has exited and that wait has passed, nothing the child wrote can still be on its
// way: what it wrote is in the pipe's buffer, and only a process that outlived it can write more.
// So from then on each read takes what the buffer holds without waiting (drain), and an empty
// buffer is the end of the output. A reader still scanning what the child wrote reads all of it;
// what it does not wait for is a process that holds the pipe open after the child.
type pipeReader struct {
	ctx    context.Context
	file   *os.File
	exited atomic.Bool
}

func (r *pipeReader) Read(p []byte) (int, error) {
	if !r.exited.Load() {
		n, err := r.file.Read(p)
		if !errors.Is(err, os.ErrDeadlineExceeded) || r.ctx.Err() != nil || !r.exited.Load() {
			return n, err
		}
	}
	return r.drain(p)
}

// drain is one read of what the pipe's buffer holds now, without waiting: io.EOF where it holds
// nothing, and os.ErrDeadlineExceeded once ctx has ended, which is what a read ctx's deadline
// stopped answers. The deadline endAfterExit set is cleared first, since a read past its deadline
// reads nothing at all; one set again after the clear is cleared again.
func (r *pipeReader) drain(p []byte) (int, error) {
	raw, err := r.file.SyscallConn()
	if err != nil {
		return 0, err
	}
	for {
		if r.ctx.Err() != nil {
			return 0, os.ErrDeadlineExceeded
		}
		if err := r.file.SetReadDeadline(time.Time{}); err != nil {
			return 0, err
		}
		var n int
		var readErr error
		err := raw.Read(func(fd uintptr) bool {
			n, readErr = syscall.Read(int(fd), p)
			return true
		})
		switch {
		case errors.Is(err, os.ErrDeadlineExceeded), errors.Is(readErr, syscall.EINTR):
			continue
		case err != nil:
			return 0, err
		case errors.Is(readErr, syscall.EAGAIN), readErr == nil && n == 0:
			return 0, io.EOF
		case readErr != nil:
			return 0, readErr
		}
		return n, nil
	}
}

// endAfterExit ends the read procgroup.WaitDelay after waited is closed, which readOutput closes
// once Wait has returned: it marks the reader as past the child's exit and then sets the pipe's
// read deadline to now, which ends a read waiting on a process that outlived the child. The mark
// comes first, so a read the deadline wakes already reads it. It does nothing where finished is
// closed first, the reader having stopped on its own.
func (r *pipeReader) endAfterExit(waited, finished <-chan struct{}) {
	select {
	case <-finished:
		return
	case <-waited:
	}
	timer := time.NewTimer(procgroup.WaitDelay)
	defer timer.Stop()
	select {
	case <-finished:
	case <-timer.C:
		r.exited.Store(true)
		_ = r.file.SetReadDeadline(time.Now())
	}
}
