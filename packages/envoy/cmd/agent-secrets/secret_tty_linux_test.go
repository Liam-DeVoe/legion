// packages/envoy/cmd/agent-secrets/secret_tty_linux_test.go
//go:build linux && (amd64 || arm64)

// The value prompt's reader (readHidden) at a real pseudo-terminal: what a person types or pastes
// at it, Ctrl-D, and what it leaves for the shell once it returns. Linux-only: the pseudo-terminal
// pair is opened with Linux's /dev/ptmx ioctls.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openPTY opens a pseudo-terminal: the controller, the side a terminal emulator writes what a
// person types or pastes to, and the terminal a program reads, as a blocking descriptor.
func openPTY(t *testing.T) (controller *os.File, terminal int) {
	t.Helper()
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { ptmx.Close() })
	raw, err := ptmx.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var number uint32
	var ioctlErr error
	if err := raw.Control(func(fd uintptr) {
		if ioctlErr = unix.IoctlSetPointerInt(int(fd), unix.TIOCSPTLCK, 0); ioctlErr == nil {
			number, ioctlErr = unix.IoctlGetUint32(int(fd), unix.TIOCGPTN)
		}
	}); err != nil || ioctlErr != nil {
		t.Fatalf("unlock the pseudo-terminal: %v %v", err, ioctlErr)
	}
	fd, err := unix.Open(fmt.Sprintf("/dev/pts/%d", number), unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open /dev/pts/%d: %v", number, err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	return ptmx, fd
}

// lflag is the terminal's local mode flags.
func lflag(t *testing.T, fd int) uint32 {
	t.Helper()
	tio, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	return tio.Lflag
}

// testBinary is the running test binary's absolute path, for a helper started in another
// directory, where os.Args[0] as `go test -c` and a relative path run it would name nothing.
func testBinary(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// promptRead is what readHidden returned.
type promptRead struct {
	line []byte
	err  error
}

// typeAtPrompt starts readHidden at a fresh terminal and, once it has turned echo off, writes typed
// to the controller as a terminal emulator does for a person's keys or a paste. It answers the
// pseudo-terminal and the reader's answer, failing t if the reader has not answered within two
// seconds.
func typeAtPrompt(t *testing.T, typed string) (*os.File, int, promptRead) {
	t.Helper()
	return typeAtPromptInWrites(t, 0, typed)
}

// typeAtPromptInWrites is typeAtPrompt with what the person types or pastes reaching the terminal
// in several writes, gap apart, as a paste over a slow link does.
func typeAtPromptInWrites(t *testing.T, gap time.Duration, writes ...string) (*os.File, int, promptRead) {
	t.Helper()
	controller, terminal := openPTY(t)
	answer := make(chan promptRead, 1)
	go func() {
		line, err := readHidden(terminal, time.Time{}, nil, nil)
		answer <- promptRead{line, err}
	}()
	awaitEchoOff(t, terminal)
	for i, w := range writes {
		if i > 0 {
			time.Sleep(gap)
		}
		if _, err := controller.Write([]byte(w)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case got := <-answer:
		return controller, terminal, got
	case <-time.After(2 * time.Second):
		t.Fatalf("the reader did not return within 2s of %q being typed", writes)
		return nil, 0, promptRead{}
	}
}

// awaitEchoOff waits until the prompt has turned the terminal's echo off, failing t after ten
// seconds: a prompt in a process of its own may take a while to start on a loaded machine.
func awaitEchoOff(t *testing.T, terminal int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); lflag(t, terminal)&unix.ECHO != 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the reader never turned echo off")
		}
	}
}

// TestPromptEndsAtTheValueDeadline: a prompt handed a deadline (a create's value read) that gets
// nothing gives up at the deadline, with errValueTimedOut and no line, and gives the terminal back
// with echo on, as any other end of the prompt does.
func TestPromptEndsAtTheValueDeadline(t *testing.T) {
	_, terminal := openPTY(t) // openPTY's cleanup holds the controller open for the whole test
	deadline := time.Now().Add(500 * time.Millisecond)
	answer := make(chan promptRead, 1)
	go func() {
		line, err := readHidden(terminal, deadline, nil, nil)
		answer <- promptRead{line, err}
	}()
	awaitEchoOff(t, terminal)
	select {
	case got := <-answer:
		if got.line != nil || !errors.Is(got.err, errValueTimedOut) {
			t.Fatalf("readHidden = %q, %v; want no line and errValueTimedOut", got.line, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the prompt was still reading 10s on, past its 500ms deadline")
	}
	if lflag(t, terminal)&unix.ECHO == 0 {
		t.Fatal("the prompt left echo off at its deadline")
	}
}

// TestPromptAtTheValueDeadlineMidEntryReadsTheEntryToItsEnd: a deadline that passes once part of the
// entry has been typed refuses the entry, but the prompt keeps the terminal: it says so at once
// (onDiscard, with errValueTimedOut), reads the rest of the entry to the line's end, and only then
// answers errValueTimedOut, so what is typed after the deadline is neither echoed nor left for the
// shell. The rest is typed once the prompt has either said so or answered, whichever comes first,
// so a prompt that gives the entry up at the deadline shows the rest reaching the shell. The
// pseudo-terminal is not this process's controlling terminal, so no key of it sends a signal: the
// reason the notice reads names no interrupt key, and no open paste.
func TestPromptAtTheValueDeadlineMidEntryReadsTheEntryToItsEnd(t *testing.T) {
	controller, terminal := openPTY(t)
	deadline := time.Now().Add(2 * time.Second)
	noticed := make(chan error, 1)
	answer := make(chan promptRead, 1)
	go func() {
		line, err := readHidden(terminal, deadline, nil, func(reason error) { noticed <- reason })
		answer <- promptRead{line, err}
	}()
	awaitEchoOff(t, terminal)
	if _, err := controller.Write([]byte("fakehead-")); err != nil {
		t.Fatal(err)
	}
	var early *promptRead
	select {
	case reason := <-noticed:
		var notice valueDeadlineNotice
		if !errors.Is(reason, errValueTimedOut) || !errors.As(reason, &notice) || notice.interrupt != 0 || notice.inPaste {
			t.Fatalf("the prompt refused the entry for %#v; want a valueDeadlineNotice naming no interrupt key and no open paste, on a terminal that is not the controlling one", reason)
		}
	case got := <-answer:
		early = &got
	case <-time.After(10 * time.Second):
		t.Fatal("the prompt neither refused the entry nor answered 10s on, past its 2s deadline")
	}
	if _, err := controller.Write([]byte("FAKETAIL\r")); err != nil {
		t.Fatal(err)
	}
	got := early
	if got == nil {
		select {
		case read := <-answer:
			got = &read
		case <-time.After(10 * time.Second):
			t.Fatal("the prompt had not answered 10s after the entry's line ended")
		}
	}
	if got.line != nil || !errors.Is(got.err, errValueTimedOut) {
		t.Fatalf("readHidden = %q, %v; want no line and errValueTimedOut", got.line, got.err)
	}
	if n := unread(t, terminal); n != 0 || bytes.Contains(shown(t, controller), []byte("FAKETAIL")) {
		t.Fatalf("the rest of the entry reached the shell: %d bytes left unread, or it was echoed", n)
	}
}

// stillPromptClock replaces the prompt's clock (promptNow) for the rest of t with one that stands
// at start until the test moves it, through the function it answers: the test, not the machine's
// load, then decides at which of the reader's steps the value deadline passes. Once the reader
// runs, only its goroutine (a readTerminal or pollWatch the test wraps) may move the clock.
func stillPromptClock(t *testing.T, start time.Time) func(time.Time) {
	t.Helper()
	restore := promptNow
	now := start
	promptNow = func() time.Time { return now }
	t.Cleanup(func() { promptNow = restore })
	return func(at time.Time) { now = at }
}

// awaitQueued waits until the terminal fd holds at least n bytes unread, so a write to its
// controller has reached the reader's side before the reader polls for it; it reports through
// t.Error, for it runs on the reader's goroutine.
func awaitQueued(t *testing.T, fd, n int) {
	for budget := time.Now().Add(promptJobWait); ; {
		queued, err := unix.IoctlGetInt(fd, unix.TIOCINQ)
		if err != nil {
			t.Error(err)
			return
		}
		if queued >= n {
			return
		}
		if time.Now().After(budget) {
			t.Errorf("the terminal held %d of %d bytes written to it %s on", queued, n, promptJobWait)
			return
		}
		_, _ = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 50)
	}
}

// pasteHeadThenRest runs readHidden at a fresh terminal with a value deadline of two seconds on a
// clock that stands still (stillPromptClock), and types the head of a bracketed paste at the
// prompt's first wait, which it lets begin only once the terminal holds the head: a machine that
// delivers the head late delays that wait, rather than leave it to time out before the head comes.
// atWait runs at each of the reader's waits once it has read the whole head, on the reader's
// goroutine, with the wait's bound, the deadline, the clock's mover and a write to the terminal
// that returns once the bytes are queued there; it answers whether it took the wait in hand, and
// if so what the wait returns. The prompt's notice sets *noticed, where noticed is not nil.
// pasteHeadThenRest answers what readHidden returned, and the reason of the notice it gave, nil
// for none, once it has checked that nothing of the paste reached the shell.
func pasteHeadThenRest(t *testing.T, atWait func(timeout int, deadline time.Time, move func(time.Time), write func(string)) (bool, int, error), noticed *bool) (promptRead, error) {
	t.Helper()
	controller, terminal := openPTY(t)
	start := time.Now()
	deadline := start.Add(2 * time.Second)
	move := stillPromptClock(t, start)
	real := readTerminal
	t.Cleanup(func() { readTerminal = real })
	const head = "\x1b[200~pastehead"
	write := func(text string) {
		if _, err := controller.Write([]byte(text)); err != nil {
			t.Error(err)
		}
	}
	// Only the reader's goroutine touches these until it answers.
	headSent, read := false, 0
	var notice error
	readTerminal = func(fd, wake int, buf []byte, timeout int) (int, error) {
		switch {
		case !headSent:
			headSent = true
			write(head)
			awaitQueued(t, fd, len(head))
		case read == len(head):
			if took, n, err := atWait(timeout, deadline, move, func(text string) {
				write(text)
				awaitQueued(t, fd, len(text))
			}); took {
				return n, err
			}
		}
		n, err := real(fd, wake, buf, timeout)
		if n > 0 && read < len(head) {
			read += n
		}
		return n, err
	}
	answer := make(chan promptRead, 1)
	go func() {
		line, err := readHidden(terminal, deadline, nil, func(reason error) {
			notice = reason
			if noticed != nil {
				*noticed = true
			}
		})
		answer <- promptRead{line, err}
	}()
	var got promptRead
	select {
	case got = <-answer:
	case <-time.After(promptJobWait):
		t.Fatalf("the prompt had not answered %s on", promptJobWait)
	}
	if n := unread(t, terminal); n != 0 || bytes.Contains(shown(t, controller), []byte("PASTE_")) {
		t.Fatalf("the paste's rest reached the shell: %d bytes left unread, or it was echoed", n)
	}
	return got, notice
}

// TestPromptAtTheValueDeadlineAnInterruptedWaitStillEndsThere: a wait for input that ends with
// nothing read is tried again, and the retry still ends at the value deadline, so the deadline is
// acted on there as it is when nothing ends the wait early: a paste open at the deadline is refused
// at once (onDiscard, naming the open paste), and its rest, which comes at the prompt's next wait,
// is read through its closing mark, so none of it reaches the shell, and is refused with the
// entry, errValueTimedOut. Each way promptTerminal.read tries a wait again has its case: a signal
// interrupts the poll (EINTR), the signal watcher has an event the reader takes first, and the
// input poll reported is gone by the time it is read. The first wait after the paste's head, a
// wait that begins before the deadline, ends that way as the deadline passes: a retry that waited
// that wait's whole bound again read the rest as if it had come in time, and refused the entry as
// more than one line, its deadline never acted on. The rest goes at the first wait after that one
// that can block (a bound other than zero). The clock stands still until the ended wait moves it
// to the deadline, so no step of the machine's own decides which wait the deadline falls in.
func TestPromptAtTheValueDeadlineAnInterruptedWaitStillEndsThere(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		err  error
	}{
		{"poll interrupted by a signal (EINTR)", 0, unix.EINTR},
		{"the signal watcher's event", -2, nil},
		{"input gone before the read", -1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const rest = "\recho PASTE_$((2+3))\r\x1b[201~"
			ended, beganInTime, restSent, noticeFirst := false, false, false, false
			var noticed bool
			got, notice := pasteHeadThenRest(t, func(timeout int, deadline time.Time, move func(time.Time), write func(string)) (bool, int, error) {
				switch {
				case !ended:
					ended, beganInTime = true, promptNow().Before(deadline)
					move(deadline)
					return true, tc.n, tc.err
				case !restSent && timeout != 0:
					restSent, noticeFirst = true, noticed
					write(rest)
				}
				return false, 0, nil
			}, &noticed)
			if !ended || !beganInTime {
				t.Fatalf("readHidden = %q, %v; no wait that began before the deadline ended with nothing read (ended: %t, begun in time: %t), so this run says nothing of a retry", got.line, got.err, ended, beganInTime)
			}
			if got.line != nil || !errors.Is(got.err, errValueTimedOut) {
				t.Fatalf("readHidden = %q, %v; want no line and errValueTimedOut, for a paste open when the deadline passed", got.line, got.err)
			}
			var said valueDeadlineNotice
			if !restSent || !noticeFirst || !errors.As(notice, &said) || !said.inPaste {
				t.Fatalf("the prompt's notice %#v (the paste's rest sent: %t, after the notice: %t); want a valueDeadlineNotice naming the open paste, given at the deadline, before the rest came", notice, restSent, noticeFirst)
			}
		})
	}
}

// TestPromptRefusesInputReadPastTheValueDeadlineAsLate: input a read returns once the value
// deadline has passed came late, whatever it holds. The paste's rest is queued at the terminal as
// the deadline passes, while the prompt waits after the paste's head, and the read that returns it
// closes the paste and the line at once: the entry is refused as late (errValueTimedOut), not as
// the more than one line its bytes hold, and none of it reaches the shell. The clock stands still
// until the wait moves it to the deadline, once the rest is queued, so that read ends past the
// deadline however slowly this test runs.
func TestPromptRefusesInputReadPastTheValueDeadlineAsLate(t *testing.T) {
	const rest = "\recho PASTE_$((2+3))\r\x1b[201~"
	queued := false
	got, _ := pasteHeadThenRest(t, func(_ int, deadline time.Time, move func(time.Time), write func(string)) (bool, int, error) {
		if !queued {
			queued = true
			write(rest)
			move(deadline)
		}
		return false, 0, nil
	}, nil)
	if !queued {
		t.Fatalf("readHidden = %q, %v before the paste's rest was queued", got.line, got.err)
	}
	if got.line != nil || !errors.Is(got.err, errValueTimedOut) {
		t.Fatalf("readHidden = %q, %v; want no line and errValueTimedOut, for a paste whose rest was read past the deadline", got.line, got.err)
	}
}

// TestPromptRefusesInputItsDrainReadsPastTheValueDeadlineAsLate: the drain after the line makes
// the read loop's check (drainAfterTheLine): input it reads once the value deadline has passed came
// late, so the entry is refused as late (errValueTimedOut), not as the more than one line a second
// line makes it, and the drain reads that line, so none of it reaches the shell. The line is typed
// in time; the second line is queued at the drain's first wait, and the clock, which stands still
// until then, moves to the deadline there, so the drain's read ends past it however slowly this
// test runs.
func TestPromptRefusesInputItsDrainReadsPastTheValueDeadlineAsLate(t *testing.T) {
	controller, terminal := openPTY(t)
	start := time.Now()
	deadline := start.Add(2 * time.Second)
	move := stillPromptClock(t, start)
	real := readTerminal
	t.Cleanup(func() { readTerminal = real })
	write := func(fd int, text string) {
		if _, err := controller.Write([]byte(text)); err != nil {
			t.Error(err)
		}
		awaitQueued(t, fd, len(text))
	}
	// Only the reader's goroutine touches these until it answers.
	typed, lineRead, moreSent := false, false, false
	readTerminal = func(fd, wake int, buf []byte, timeout int) (int, error) {
		switch {
		case !typed:
			typed = true
			write(fd, "value\r")
		case lineRead && !moreSent && timeout > 0:
			moreSent = true
			write(fd, "more\r")
			move(deadline)
		}
		n, err := real(fd, wake, buf, timeout)
		if n > 0 && bytes.ContainsAny(buf[:n], "\r\n") {
			lineRead = true
		}
		return n, err
	}
	answer := make(chan promptRead, 1)
	go func() {
		line, err := readHidden(terminal, deadline, nil, func(error) {})
		answer <- promptRead{line, err}
	}()
	var got promptRead
	select {
	case got = <-answer:
	case <-time.After(promptJobWait):
		t.Fatalf("the prompt had not answered %s on", promptJobWait)
	}
	if !moreSent {
		t.Fatalf("readHidden = %q, %v before the drain read anything", got.line, got.err)
	}
	if got.line != nil || !errors.Is(got.err, errValueTimedOut) {
		t.Fatalf("readHidden = %q, %v; want no line and errValueTimedOut, for a second line the drain read past the deadline", got.line, got.err)
	}
	if n := unread(t, terminal); n != 0 || bytes.Contains(shown(t, controller), []byte("more")) {
		t.Fatalf("the second line reached the shell: %d bytes left unread, or it was echoed", n)
	}
}

// TestPromptWatchWaitEndsAtItsDeadlineThoughSignalsKeepInterruptingIt: the watcher's wait for an
// event (promptWatch.ready, which the wait for the foreground repeats every 50 ms) keeps the value
// deadline across the signals that interrupt it, by the read's own rule (waitBound): each attempt
// counts the time left again, none asks to wait past the deadline, and one interrupted once the
// deadline has passed ends the wait with no event, where waitForeground's check of the deadline
// ends the prompt. Every attempt here is interrupted (EINTR), and the clock moves 30 ms at each, as
// a stream of signals would have it; an attempt that the rule would never reach ends the wait. The
// wait must have been interrupted on both sides of the deadline, so a wait that polled something
// else, or gave up at its first interruption, does not pass.
func TestPromptWatchWaitEndsAtItsDeadlineThoughSignalsKeepInterruptingIt(t *testing.T) {
	start := time.Now()
	deadline := start.Add(100 * time.Millisecond)
	move := stillPromptClock(t, start)
	restore := pollWatch
	t.Cleanup(func() { pollWatch = restore })
	const enough = 100
	now := start
	var asked []int
	pastDeadline := 0
	pollWatch = func(_ []unix.PollFd, timeout int) (int, error) {
		asked = append(asked, timeout)
		if timeout < 0 || now.Add(time.Duration(timeout)*time.Millisecond).After(deadline.Add(time.Millisecond)) {
			pastDeadline++
		}
		if len(asked) == enough {
			return 0, nil
		}
		now = now.Add(30 * time.Millisecond)
		move(now)
		return 0, unix.EINTR
	}
	ready, err := (&promptWatch{fd: -1}).ready(50, deadline)
	if len(asked) < 2 || now.Before(deadline) {
		t.Fatalf("ready = %t, %v after %d interrupted attempts, the clock %s past its start; want attempts on both sides of the %s deadline", ready, err, len(asked), now.Sub(start), deadline.Sub(start))
	}
	if ready || err != nil || pastDeadline > 0 || len(asked) == enough {
		t.Fatalf("ready = %t, %v after %d attempts, asking %v ms, %d of them to wait past the deadline; want no event and no error, and no attempt past the deadline", ready, err, len(asked), asked, pastDeadline)
	}
}

// TestWaitBoundEndsAWaitAtItsMomentNeverBefore: waitBound cuts a wait's bound to the time left
// before its moment, rounded up past the next whole millisecond, so a poll given that bound ends at
// the moment or after it, never before, however little time is left: a wait that ended before the
// moment would find the deadline not yet passed, and wait again. It keeps a shorter bound, and a
// bound of -1 (none) where the moment is the zero time, and answers 0 once the moment has passed.
// The clock stands still, so the time left is exactly what each case sets.
func TestWaitBoundEndsAWaitAtItsMomentNeverBefore(t *testing.T) {
	start := time.Now()
	stillPromptClock(t, start)
	for _, left := range []time.Duration{time.Nanosecond, 400 * time.Microsecond, time.Millisecond, 1500 * time.Microsecond, 2 * time.Second} {
		bound := waitBound(-1, start.Add(left))
		if time.Duration(bound)*time.Millisecond < left || bound > int(left.Milliseconds())+1 {
			t.Errorf("waitBound(-1, %s from now) = %d ms; want a bound that reaches the moment, at most a millisecond past it", left, bound)
		}
	}
	for _, tc := range []struct {
		wait  int
		until time.Time
		want  int
	}{
		{-1, time.Time{}, -1},
		{50, time.Time{}, 50},
		{50, start.Add(time.Second), 50},
		{50, start, 0},
		{-1, start.Add(-time.Second), 0},
	} {
		if got := waitBound(tc.wait, tc.until); got != tc.want {
			t.Errorf("waitBound(%d, %s from now) = %d; want %d", tc.wait, tc.until.Sub(start), got, tc.want)
		}
	}
}

// TestGCPSecretCreateStoresNoValueThatEndsPastItsWindow: a value typed at the prompt inside the
// create's window, whose drain then runs past it - a bracketed paste that opens after the line and
// closes only once the window is over - is not stored: the prompt reads that paste through its
// closing mark, and the create then refuses the value as one that did not come in time, never
// calling AddSecretVersion, deletes the empty secret it made, asks the broker to reread the name,
// and exits 1 naming the window and the command to run again.
func TestGCPSecretCreateStoresNoValueThatEndsPastItsWindow(t *testing.T) {
	controller, terminal := openPTY(t)
	const window = 2 * time.Second
	restoreWindow := valueReadWindow
	valueReadWindow = window
	t.Cleanup(func() { valueReadWindow = restoreWindow })
	fake := newFakeSecretManager()
	startFakeSecretManager(t, fake)
	useGCPIdentity(t, "authorized_user", "ada@example.com")
	broker := startSecretBrokerWith(t, gcpTestSettings(), gcpServedBy(fake, gcpTestSettings()))
	useTerminal(t)
	stdinTerminal = func(io.Reader) (int, bool) { return terminal, true }
	readHidden = readHiddenAtTerminal
	real := readTerminal
	t.Cleanup(func() { readTerminal = real })
	// draining closes at the reader's first wait after the read that ended the line: the drain of
	// what follows the line. The terminal turns the typed carriage return into a line feed.
	draining := make(chan struct{})
	lineEnded, drainSeen := false, false
	readTerminal = func(fd, wake int, buf []byte, timeout int) (int, error) {
		if lineEnded && !drainSeen {
			drainSeen = true
			close(draining)
		}
		n, err := real(fd, wake, buf, timeout)
		if n > 0 && bytes.ContainsAny(buf[:n], "\r\n") {
			lineEnded = true
		}
		return n, err
	}
	type result struct {
		stderr string
		code   int
	}
	done := make(chan result, 1)
	go func() {
		_, stderr, code := runSecret("create", "NEW_TEST", "--owner", "me", "--tier", "agent")
		done <- result{stderr, code}
	}()
	awaitEchoOff(t, terminal)
	// The create asked the store for the secret before the prompt turned echo off, so its window
	// is over by then.
	windowEnds := time.Now().Add(window)
	if _, err := controller.Write([]byte("typed-value\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-draining:
	case <-time.After(10 * time.Second):
		t.Fatal("the prompt never drained what follows the line")
	}
	if _, err := controller.Write([]byte("\x1b[200~")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(windowEnds) + 300*time.Millisecond)
	if _, err := controller.Write([]byte("\x1b[201~")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.code != 1 || !strings.Contains(got.stderr, "no value came in time, within 2s") || !strings.Contains(got.stderr, "agent-secrets secret create NEW_TEST") {
			t.Fatalf("exit %d, stderr %q; want 1 naming the window and the command to run again", got.code, got.stderr)
		}
		if !regexp.MustCompile(`Enter the value by \d\d:\d\d:\d\d \S+, 2s after the create, or nothing is stored\.\nValue for NEW_TEST: `).MatchString(got.stderr) {
			t.Fatalf("stderr %q; want the prompt to name the time its value must come by, then its label", got.stderr)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the create had not ended 20s after the paste closed")
	}
	if calls := fake.called(); slices.Contains(calls, "AddSecretVersion") {
		t.Fatalf("Secret Manager calls %v; want no AddSecretVersion for a value that ended past its window", calls)
	}
	requireNothingLeft(t, fake, broker, "NEW_TEST")
}

// unread is how many bytes the terminal holds unread, which the shell would read once the form
// exits: with canonical mode turned off, so a last line without a newline counts too.
func unread(t *testing.T, fd int) int {
	t.Helper()
	tio, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	tio.Lflag &^= unix.ICANON
	tio.Cc[unix.VMIN], tio.Cc[unix.VTIME] = 0, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, tio); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCINQ)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// shown is what the terminal wrote back to the controller, which the person would see: an echo.
func shown(t *testing.T, controller *os.File) []byte {
	t.Helper()
	if err := controller.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	buf := make([]byte, 4096)
	for {
		n, err := controller.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			return out.Bytes()
		}
	}
}

// TestPromptReadsOneTypedLineWithEchoOff: Enter ends the value, which is never echoed; the reader
// asks the terminal to bracket pastes while it reads and to stop once it is done, and leaves it as
// it was, echo on.
func TestPromptReadsOneTypedLineWithEchoOff(t *testing.T) {
	controller, terminal, got := typeAtPrompt(t, "typed value\r")
	if got.err != nil || string(got.line) != "typed value" {
		t.Fatalf("readHidden = %q, %v; want %q", got.line, got.err, "typed value")
	}
	echoed := shown(t, controller)
	if bytes.Contains(echoed, []byte("typed")) {
		t.Fatalf("the terminal showed %q: the value was echoed", echoed)
	}
	on, off := bytes.Index(echoed, []byte("\x1b[?2004h")), bytes.LastIndex(echoed, []byte("\x1b[?2004l"))
	if on < 0 || off < on {
		t.Fatalf("the terminal was sent %q, want bracketed paste turned on and then off", echoed)
	}
	if flags := lflag(t, terminal); flags&unix.ECHO == 0 || flags&unix.ICANON == 0 {
		t.Fatalf("lflag after the read = %#x, want echo and canonical mode back on", flags)
	}
}

// TestPromptClearsIEXTENWhileItReads: the prompt's mode clears IEXTEN, under which macOS's terminal
// acts on Ctrl-V and Ctrl-O whatever ICANON and ISIG say (bsd/kern/tty.c, ttyinput), so a pasted
// Ctrl-V would vanish from the value and Ctrl-O would toggle output discard. The restore puts it
// back. On Linux n_tty lowercases input under IUCLC only while IEXTEN is set, even outside
// canonical mode, so this checks the mode itself.
func TestPromptClearsIEXTENWhileItReads(t *testing.T) {
	controller, terminal := openPTY(t)
	if lflag(t, terminal)&unix.IEXTEN == 0 {
		t.Fatal("a fresh pseudo-terminal has IEXTEN off; the test needs it on")
	}
	answer := make(chan promptRead, 1)
	go func() {
		line, err := readHidden(terminal, time.Time{}, nil, nil)
		answer <- promptRead{line, err}
	}()
	awaitEchoOff(t, terminal)
	if flags := lflag(t, terminal); flags&unix.IEXTEN != 0 {
		t.Fatalf("lflag while the prompt reads = %#x, want IEXTEN off", flags)
	}
	if _, err := controller.Write([]byte("value\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-answer:
		if got.err != nil || string(got.line) != "value" {
			t.Fatalf("readHidden = %q, %v; want %q", got.line, got.err, "value")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the reader did not return within 2s")
	}
	if lflag(t, terminal)&unix.IEXTEN == 0 {
		t.Fatal("IEXTEN is off after the prompt returned")
	}
}

// TestPromptCtrlDEndsTheValue: Ctrl-D ends the read, with nothing typed (an empty value) and after
// typing (the value typed).
func TestPromptCtrlDEndsTheValue(t *testing.T) {
	for _, tc := range []struct{ typed, want string }{{"\x04", ""}, {"typed\x04", "typed"}} {
		_, terminal, got := typeAtPrompt(t, tc.typed)
		if got.err != nil || string(got.line) != tc.want {
			t.Fatalf("after %q: readHidden = %q, %v; want %q", tc.typed, got.line, got.err, tc.want)
		}
		if flags := lflag(t, terminal); flags&unix.ECHO == 0 {
			t.Fatalf("after %q: lflag = %#x, want echo back on", tc.typed, flags)
		}
	}
}

// TestPromptRefusesAPasteOfMoreThanOneLineAndLeavesNothingForTheShell: a paste of a value of more
// than one line, as a terminal sends it (each line ended by a carriage return, the last one with
// or without), is refused rather than cut at its first line, and none of it is left for the shell
// to read once the form exits, nor echoed.
func TestPromptRefusesAPasteOfMoreThanOneLineAndLeavesNothingForTheShell(t *testing.T) {
	for _, paste := range []string{
		"-----BEGIN KEY-----\rline two\recho line three",
		"-----BEGIN KEY-----\nline two\nline three\n",
	} {
		controller, terminal, got := typeAtPrompt(t, paste)
		if !errors.Is(got.err, errMoreThanOneLine) {
			t.Fatalf("paste %q: readHidden = %q, %v; want errMoreThanOneLine", paste, got.line, got.err)
		}
		if n := unread(t, terminal); n != 0 {
			t.Fatalf("paste %q: the terminal holds %d bytes unread for the shell, want none", paste, n)
		}
		if echoed := shown(t, controller); bytes.Contains(echoed, []byte("line")) {
			t.Fatalf("paste %q: the terminal showed %q", paste, echoed)
		}
	}
}

// TestPromptReadsABracketedPasteThroughItsEnd: a bracketed paste of more than one line is read
// through its closing mark however long a gap splits it, as one lost packet over ssh can, so it is
// refused whole and nothing of it is left for the shell or echoed.
func TestPromptReadsABracketedPasteThroughItsEnd(t *testing.T) {
	controller, terminal, got := typeAtPromptInWrites(t, 300*time.Millisecond, "\x1b[200~line1\n", "line2\x1b[201~")
	if !errors.Is(got.err, errMoreThanOneLine) {
		t.Fatalf("readHidden = %q, %v; want errMoreThanOneLine", got.line, got.err)
	}
	if n := unread(t, terminal); n != 0 {
		t.Fatalf("the terminal holds %d bytes unread for the shell, want none", n)
	}
	if echoed := shown(t, controller); bytes.Contains(echoed, []byte("line")) {
		t.Fatalf("the terminal showed %q", echoed)
	}
}

// TestPromptAcceptsABracketedPasteOfOneLine: a bracketed paste of one line is the value, never its
// marks: ending in a line ending, which is dropped, or followed by Enter, with its marks whole or
// split across reads.
func TestPromptAcceptsABracketedPasteOfOneLine(t *testing.T) {
	for _, writes := range [][]string{
		{"\x1b[200~value\n\x1b[201~"},
		{"\x1b[200~value\r\n\x1b[201~"},
		{"\x1b[200~value\x1b[201~", "\r"},
		{"\x1b[20", "0~value\n\x1b[2", "01~"},
	} {
		_, terminal, got := typeAtPromptInWrites(t, 50*time.Millisecond, writes...)
		if got.err != nil || string(got.line) != "value" {
			t.Fatalf("%q: readHidden = %q, %v; want %q", writes, got.line, got.err, "value")
		}
		if n := unread(t, terminal); n != 0 {
			t.Fatalf("%q: the terminal holds %d bytes unread, want none", writes, n)
		}
	}
}

// TestPromptWordEraseAndControlBytes: the terminal's configured word erase removes the word it
// would in canonical mode, and another control byte cannot become an invisible byte in a secret.
func TestPromptWordEraseAndControlBytes(t *testing.T) {
	_, terminal, got := typeAtPrompt(t, "abc def\x17ghi\r")
	if got.err != nil || string(got.line) != "abc ghi" {
		t.Fatalf("word erase: readHidden = %q, %v; want %q", got.line, got.err, "abc ghi")
	}
	if n := unread(t, terminal); n != 0 {
		t.Fatalf("word erase: the terminal holds %d bytes unread, want none", n)
	}
	_, _, got = typeAtPrompt(t, "value\x01\r")
	if got.err == nil || !strings.Contains(got.err.Error(), "the control byte 0x01 cannot be typed at the prompt") {
		t.Fatalf("control byte: readHidden = %q, %v; want refusal naming 0x01", got.line, got.err)
	}
}

// TestPromptNamesATabAPasteEndsWith: a key pasted at a real terminal with a Tab after it, as a copy
// out of a web page's table often carries, is refused, neither stored nor trimmed, with a refusal
// that names the Tab, says it is at the end of the value, and says that a paste often adds one
// there; the terminal is left with nothing unread.
func TestPromptNamesATabAPasteEndsWith(t *testing.T) {
	_, terminal, got := typeAtPromptInWrites(t, 50*time.Millisecond, "\x1b[200~sk-live-key\t\x1b[201~", "\r")
	var refused promptControlByteError
	if got.line != nil || !errors.As(got.err, &refused) || refused.c != '\t' || !refused.textBefore || refused.textAfter {
		t.Fatalf("readHidden = %q, %v; want no line and a refusal of the Tab at the value's end", got.line, got.err)
	}
	if text := got.err.Error(); !strings.Contains(text, "0x09") || !strings.Contains(text, "Tab") || !strings.Contains(text, "end of the value") || !strings.Contains(text, "paste") {
		t.Fatalf("refusal %q; want it to name 0x09, the Tab, the value's end and the paste", text)
	}
	if n := unread(t, terminal); n != 0 {
		t.Fatalf("the terminal holds %d bytes unread, want none", n)
	}
}

// TestPromptRefusesFlowControlBytes: Ctrl-S and Ctrl-Q reach the reader as control bytes rather
// than stopping and starting the terminal's output, so a paste holding one is refused, never stored
// without it, and never leaves the prompt waiting with its output suspended.
func TestPromptRefusesFlowControlBytes(t *testing.T) {
	for _, c := range []byte{0x13, 0x11} {
		_, _, got := typeAtPrompt(t, "val"+string(c)+"ue\r")
		want := fmt.Sprintf("the control byte 0x%02x cannot be typed at the prompt", c)
		if got.line != nil || got.err == nil || !strings.Contains(got.err.Error(), want) {
			t.Fatalf("control 0x%02x: readHidden = %q, %v; want the refusal naming it", c, got.line, got.err)
		}
	}
}

// TestPromptRefusesAPasteCutShortByAHangUp: when the terminal hangs up inside a bracketed paste, as
// when its window or ssh session closes mid-paste, the reader answers an error rather than what it
// has read, so nothing is stored. The hang-up lands while the reader is between reads, where its
// next read of the hung-up terminal answers end of input.
func TestPromptRefusesAPasteCutShortByAHangUp(t *testing.T) {
	for _, tc := range []struct {
		paste string
		want  error
	}{
		{"\x1b[200~-----BEGIN KEY-----\nline2", errMoreThanOneLine},
		{"\x1b[200~value\n", errPasteCutShort},
		{"\x1b[200~val", errPasteCutShort},
		{"partial-secr", errValueCutShort},
	} {
		t.Run(fmt.Sprintf("%q", tc.paste), func(t *testing.T) {
			controller, terminal := openPTY(t)
			real := readTerminal
			t.Cleanup(func() { readTerminal = real })
			consumed, hungUp := make(chan struct{}), make(chan struct{})
			var once sync.Once
			read := 0
			readTerminal = func(fd, wake int, buf []byte, timeout int) (int, error) {
				n, err := real(fd, wake, buf, timeout)
				if n > 0 {
					read += n
				}
				if read >= len(tc.paste) {
					once.Do(func() { close(consumed) })
					<-hungUp
				}
				return n, err
			}
			answer := make(chan promptRead, 1)
			go func() {
				line, err := readHidden(terminal, time.Time{}, nil, nil)
				answer <- promptRead{line, err}
			}()
			awaitEchoOff(t, terminal)
			if _, err := controller.Write([]byte(tc.paste)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-consumed:
			case <-time.After(2 * time.Second):
				t.Fatal("the reader did not read the paste within 2s")
			}
			if err := controller.Close(); err != nil {
				t.Fatal(err)
			}
			close(hungUp)
			select {
			case got := <-answer:
				if !errors.Is(got.err, tc.want) || got.line != nil {
					t.Fatalf("readHidden = %q, %v; want no value and %v", got.line, got.err, tc.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the reader did not return within 2s of the hang-up")
			}
		})
	}
}

// TestPromptSignalHelper is the process the signal tests start: the value prompt's reader on its
// standard input, its controlling terminal. It names only the reader's error, never its value.
func TestPromptSignalHelper(t *testing.T) {
	if os.Getenv("AGENT_SECRETS_PROMPT_HELPER") == "" {
		t.Skip("subprocess helper")
	}
	_, err := readHidden(0, time.Time{}, nil, nil)
	fmt.Printf("RETURNED %v\n", err)
}

// TestPromptCtrlCEndsTheProcessBySIGINT: Ctrl-C at the prompt ends the process by SIGINT, echo back
// on, so a calling shell stops its list rather than running the next command. The prompt runs in a
// process of its own, the session leader whose controlling terminal sends the Ctrl-C, started by
// `env --default-signal`, so it does not inherit a SIGINT this test process ignores (as a job run in
// the background of a script does), at which the prompt rightly goes on reading.
func TestPromptCtrlCEndsTheProcessBySIGINT(t *testing.T) {
	reset := []string{"env", "--default-signal=INT,TERM"}
	helper := shellWord(os.Args[0]) + " -test.run=^TestPromptSignalHelper$"
	for _, tc := range []struct {
		name    string
		command *exec.Cmd
	}{
		{"the prompt alone", exec.Command(reset[0], reset[1], os.Args[0], "-test.run=^TestPromptSignalHelper$")},
		{"a bash list", exec.Command(reset[0], reset[1], "bash", "-c", helper+"; echo SECOND")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controller, terminal := openPTY(t)
			fd, err := unix.Dup(terminal)
			if err != nil {
				t.Fatal(err)
			}
			tty := os.NewFile(uintptr(fd), "terminal")
			cmd := tc.command
			cmd.Env = append(os.Environ(), "AGENT_SECRETS_PROMPT_HELPER=1")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			err = cmd.Start()
			tty.Close()
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			awaitEchoOff(t, terminal)
			if _, err := controller.Write([]byte{0x03}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("the process did not end within 10s of Ctrl-C; the terminal showed %q", shown(t, controller))
			}
			status := cmd.ProcessState.Sys().(syscall.WaitStatus)
			out := shown(t, controller)
			if tc.name == "the prompt alone" && (!status.Signaled() || status.Signal() != syscall.SIGINT) {
				t.Fatalf("wait status %v (%s); want ended by SIGINT. The terminal showed %q", status, cmd.ProcessState, out)
			}
			if bytes.Contains(out, []byte("SECOND")) || bytes.Contains(out, []byte("RETURNED")) {
				t.Fatalf("the terminal showed %q (%s): the list went on past the Ctrl-C", out, cmd.ProcessState)
			}
			if lflag(t, terminal)&unix.ECHO == 0 {
				t.Fatal("echo is off after the process ended")
			}
		})
	}
}

// TestPromptQuitEndsTheProcessBySIGQUITWithNoCore: Ctrl-\ at the prompt restores echo and ends the
// process by SIGQUIT, which a shell reads as 131, with no goroutine dump from Go's runtime and no
// core, which would hold what was typed. The prompt runs with its core limit raised to the hard
// limit, in a directory of its own, so a core the kernel would write shows in its wait status and
// in that directory.
func TestPromptQuitEndsTheProcessBySIGQUITWithNoCore(t *testing.T) {
	controller, terminal := openPTY(t)
	fd, err := unix.Dup(terminal)
	if err != nil {
		t.Fatal(err)
	}
	tty := os.NewFile(uintptr(fd), "terminal")
	dir := t.TempDir()
	cmd := exec.Command("bash", "-c",
		`ulimit -c "$(ulimit -Hc)" && exec env --default-signal=QUIT "$0" -test.run='^TestPromptSignalHelper$'`,
		testBinary(t))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "AGENT_SECRETS_PROMPT_HELPER=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		tty.Close()
		t.Fatal(err)
	}
	tty.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	awaitEchoOff(t, terminal)
	if _, err := controller.Write([]byte{0x1c}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the process did not quit within 10s of Ctrl-\\")
	}
	status := cmd.ProcessState.Sys().(syscall.WaitStatus)
	out := shown(t, controller)
	if !status.Signaled() || status.Signal() != syscall.SIGQUIT {
		t.Fatalf("wait status %v (%s); want ended by SIGQUIT. The terminal showed %q", status, cmd.ProcessState, out)
	}
	if status.CoreDump() {
		t.Fatal("the kernel dumped core")
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range left {
		t.Errorf("the prompt left %s in its directory", entry.Name())
	}
	if bytes.Contains(out, []byte("goroutine")) || bytes.Contains(out, []byte("RETURNED")) {
		t.Fatalf("the terminal showed %q: want no goroutine dump and no return from the read", out)
	}
	if lflag(t, terminal)&unix.ECHO == 0 {
		t.Fatalf("echo is off after Ctrl-\\; the terminal showed %q", out)
	}
}
