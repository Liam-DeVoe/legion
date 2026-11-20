// Package testproc reads the processes a daemon test asks about from /proc. Where the test knows a
// process, its own files answer: Children and Descendants read the tree below it from the
// children files of its threads, and Ended reads its state, so neither reads any other process
// of the machine, which holds tens of thousands on a loaded developer box. Where no known process
// leads to the one asked about, as for an orphan whose parent has gone, Matching walks every
// process, held to its caller's context.
//
// A process has ended when /proc has no entry for it or its state is Z (a zombie) or X (dead):
// never by whether something reaped it, since where a test's runner is PID 1 of its namespace (a
// `go test` in a container with no init) nothing but that runner reaps an orphan, and an orphan
// that has exited stays a zombie.
package testproc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// root is the /proc these functions read: a var so this package's tests can plant a tree.
var root = "/proc"

// Children answers the children of pid: the union of /proc/<pid>/task/<tid>/children over every
// thread of pid, since each of those files lists only the children that one thread forked
// (proc(5)), and a Go program forks from whichever of its threads runs exec.Command. An error
// wrapping fs.ErrNotExist means pid has no /proc entry, so it has been reaped.
func Children(pid int) ([]int, error) {
	task := filepath.Join(root, strconv.Itoa(pid), "task")
	threads, err := os.ReadDir(task)
	if err != nil {
		return nil, err
	}
	var children []int
	read := 0
	for _, thread := range threads {
		path := filepath.Join(task, thread.Name(), "children")
		listed, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue // the thread ended after the listing of task
		}
		if err != nil {
			return nil, err
		}
		read++
		for _, field := range strings.Fields(string(listed)) {
			child, err := strconv.Atoi(field)
			if err != nil {
				return nil, fmt.Errorf("%s lists %q, which is no pid", path, field)
			}
			children = append(children, child)
		}
	}
	if read == 0 {
		// No thread had a children file: either pid ended after the listing of task, or the
		// kernel has no children files at all (CONFIG_PROC_CHILDREN), which must not read as a
		// process with no child.
		if _, err := os.Stat(filepath.Join(root, strconv.Itoa(pid))); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("no thread of pid %d has a children file under %s, so its children cannot be read", pid, task)
	}
	return children, nil
}

// Descendants answers every process below pid, breadth first, read with Children. A process that
// ended between its parent's read and its own is passed over, as are its children, which the
// kernel has handed to another parent by then.
func Descendants(pid int) ([]int, error) {
	var found []int
	seen := map[int]bool{pid: true}
	queue := []int{pid}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		children, err := Children(parent)
		if parent != pid && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return found, err
		}
		for _, child := range children {
			if !seen[child] {
				seen[child] = true
				found = append(found, child)
				queue = append(queue, child)
			}
		}
	}
	return found, nil
}

// Ended answers whether pid has ended, by its own stat: no entry, or state Z or X.
func Ended(pid int) (bool, error) {
	path := filepath.Join(root, strconv.Itoa(pid), "stat")
	stat, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	// The command name, in parentheses, may hold spaces and parentheses of its own, so the state
	// is the first field after the last ')'.
	fields := bytes.Fields(stat[bytes.LastIndexByte(stat, ')')+1:])
	if len(fields) == 0 {
		return false, fmt.Errorf("%s holds %q, with no state", path, stat)
	}
	state := string(fields[0])
	return state == "Z" || state == "X", nil
}

// Process is one process Matching read: its pid and the words of its command line.
type Process struct {
	PID  int
	Argv []string
}

// CommandLine is p's command line, its words joined by spaces.
func (p Process) CommandLine() string { return strings.Join(p.Argv, " ") }

// Matching walks every process of /proc, reading each one's command line, and answers those match
// holds for. It is for the question no known process's files answer, as which processes an
// orphaned command left running. A read of a command line waits on that process's memory map,
// which a process stopped in the kernel can hold for as long as it stays stopped, and Go cannot
// interrupt a read once it blocks: so the walk runs on a goroutine of its own, and when ctx, the
// caller's bound, ends first, Matching answers an error naming the walk and wrapping ctx's cause,
// so that a context made with a cause names the bound it ran out of. The goroutine is left behind,
// blocked in its read, with a buffered channel it can still answer into once the read returns.
func Matching(ctx context.Context, match func(Process) bool) ([]Process, error) {
	type walked struct {
		found []Process
		err   error
	}
	proc := root
	done := make(chan walked, 1)
	go func() {
		found, err := matching(proc, match)
		done <- walked{found, err}
	}()
	select {
	case w := <-done:
		return w.found, w.err
	case <-ctx.Done():
		return nil, fmt.Errorf("the walk of every process of %s had not ended: %w", proc, context.Cause(ctx))
	}
}

// matching is Matching's walk of proc, with no bound of its own. A process that ended after the
// listing of proc, or whose command line this process may not read, is passed over.
func matching(proc string, match func(Process) bool) ([]Process, error) {
	entries, err := os.ReadDir(proc)
	if err != nil {
		return nil, err
	}
	var found []Process
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join(proc, entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		process := Process{PID: pid, Argv: strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")}
		if match(process) {
			found = append(found, process)
		}
	}
	return found, nil
}
