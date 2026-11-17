package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// logLine is one line of the listener's log: its own JSON records, and the text lines the bus
// writes through the default logger (with a time to the second).
type logLine struct {
	at      int64
	msg     string
	outcome string
	entries int
	offset  int64
}

func parseLogLine(line []byte) (logLine, bool) {
	if len(line) > 0 && line[0] == '{' {
		var record struct {
			Time    string `json:"time"`
			Msg     string `json:"msg"`
			Outcome string `json:"outcome"`
			Entries int    `json:"entries"`
		}
		if json.Unmarshal(line, &record) != nil {
			return logLine{}, false
		}
		at, err := time.Parse(time.RFC3339Nano, record.Time)
		if err != nil {
			return logLine{}, false
		}
		return logLine{at: at.UnixNano(), msg: record.Msg, outcome: record.Outcome, entries: record.Entries}, true
	}
	const layout = "2006/01/02 15:04:05"
	if len(line) < len(layout)+1 {
		return logLine{}, false
	}
	at, err := time.ParseInLocation(layout, string(line[:len(layout)]), time.Local)
	if err != nil {
		return logLine{}, false
	}
	return logLine{at: at.UnixNano(), msg: strings.TrimSpace(string(line[len(layout):]))}, true
}

// readLog reads the log lines from offset on, each with the offset just past it.
func readLog(path string, offset int64) ([]logLine, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	var lines []logLine
	reader := bufio.NewReaderSize(file, 1<<20)
	position := offset
	for {
		raw, err := reader.ReadBytes('\n')
		if len(raw) > 0 && raw[len(raw)-1] == '\n' {
			position += int64(len(raw))
			if line, ok := parseLogLine(bytes.TrimSpace(raw)); ok {
				line.offset = position
				lines = append(lines, line)
			}
		}
		if err == io.EOF {
			return lines, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

var signals = map[string]syscall.Signal{"STOP": syscall.SIGSTOP, "CONT": syscall.SIGCONT, "KILL": syscall.SIGKILL, "TERM": syscall.SIGTERM}

// matches is a repeatable -match flag: each value is text a line's message contains, optionally
// followed by @outcome, the line's outcome attribute.
type matches []string

func (m *matches) String() string        { return strings.Join(*m, " or ") }
func (m *matches) Set(text string) error { *m = append(*m, text); return nil }

// any reports whether line is one of the lines m names.
func (m matches) any(line logLine) bool {
	for _, match := range m {
		text, outcome, hasOutcome := strings.Cut(match, "@")
		if strings.Contains(line.msg, text) && (!hasOutcome || line.outcome == outcome) {
			return true
		}
	}
	return false
}

// runAwait waits for the first log line from offset on that one of the -match values names and
// whose time is after after-ns, signals pid at once when asked to, and prints the line's time, the
// signal's time (0 without one) and the offset past the line. With -front it first ends the held
// front that serves pid's port (endFront), so the front never forwards to the port pid releases.
// With -cont-after it then continues the process it stopped that long after the stop, so a script
// blocked on that process's work (refillrig recreate filling a stopped NATS) still gets it back.
func runAwait(args []string) error {
	flags := flag.NewFlagSet("await", flag.ContinueOnError)
	path := flags.String("log", "", "listener log")
	offset := flags.Int64("offset", 0, "byte offset to read from")
	var match matches
	flags.Var(&match, "match", "text the line's message contains, optionally followed by @<outcome attribute>; repeatable, any one matches")
	after := flags.Int64("after-ns", 0, "skip lines logged at or before this Unix time in nanoseconds")
	timeout := flags.Duration("timeout", time.Minute, "how long to wait")
	signalName := flags.String("signal", "", "signal to send pid when the line appears: STOP, CONT, KILL or TERM")
	pid := flags.Int("pid", 0, "process to signal")
	contAfter := flags.Duration("cont-after", 0, "with -signal STOP, send pid SIGCONT this long after the stop")
	front := flags.Int("front", 0, "with -signal KILL or TERM, the leader of a held front's process group (scripts/e2e/lib/held-front.sh), killed and waited for until it has exited, before pid is signalled")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(match) == 0 {
		return errors.New("no -match")
	}
	var sig syscall.Signal
	if *signalName != "" {
		var ok bool
		if sig, ok = signals[*signalName]; !ok || *pid <= 0 {
			return fmt.Errorf("signal %q to pid %d: want STOP, CONT, KILL or TERM and a pid", *signalName, *pid)
		}
	}
	if *contAfter > 0 && sig != syscall.SIGSTOP {
		return errors.New("-cont-after needs -signal STOP")
	}
	if *front != 0 && (*front <= 1 || (sig != syscall.SIGKILL && sig != syscall.SIGTERM)) {
		return fmt.Errorf("-front %d needs a process group leader's pid and -signal KILL or TERM", *front)
	}
	file, err := os.Open(*path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(*offset, io.SeekStart); err != nil {
		return err
	}
	deadline := time.Now().Add(*timeout)
	position := *offset
	var pending []byte
	buffer := make([]byte, 1<<16)
	for {
		n, readErr := file.Read(buffer)
		pending = append(pending, buffer[:n]...)
		for {
			end := bytes.IndexByte(pending, '\n')
			if end < 0 {
				break
			}
			raw := pending[:end+1]
			pending = pending[end+1:]
			position += int64(len(raw))
			line, ok := parseLogLine(bytes.TrimSpace(raw))
			if !ok || !match.any(line) || line.at <= *after {
				continue
			}
			var signalled int64
			if *front != 0 {
				if err := endFront(*front, frontGrace, procState); err != nil {
					return err
				}
			}
			if sig != 0 {
				if err := syscall.Kill(*pid, sig); err != nil {
					return fmt.Errorf("signal %d: %w", *pid, err)
				}
				signalled = time.Now().UnixNano()
			}
			fmt.Printf("%d %d %d\n", line.at, signalled, position)
			if *contAfter > 0 {
				time.Sleep(*contAfter)
				if err := syscall.Kill(*pid, syscall.SIGCONT); err != nil {
					return fmt.Errorf("continue %d: %w", *pid, err)
				}
			}
			return nil
		}
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if n == 0 {
			if time.Now().After(deadline) {
				return fmt.Errorf("no line naming %s in %s within %s", match.String(), *path, *timeout)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
}

// frontGrace bounds the wait for a held front to stop and then to exit: SIGSTOP and SIGKILL take
// effect the next time a process runs, so only a process that the kernel holds takes longer.
const frontGrace = 5 * time.Second

// endFront ends the held front whose process group pgid leads, before the server behind it is
// signalled (lib/held-front.sh: a front lives only while its server does). The front is the
// leader and the process it forks for each connection. endFront stops the whole group with
// SIGSTOP and waits until the leader is stopped: a stopped leader is in no fork, so every child it
// forked is then listed, and it forks no more. It lists those children, sends SIGKILL to the
// group, and waits until the leader and each child have exited. Once endFront returns, no process
// of the front accepts on the kept port or connects to the port the server is about to release.
// grace bounds both waits (frontGrace), and state reads a process's state (procState; a test
// gives its own).
func endFront(pgid int, grace time.Duration, state func(int) byte) error {
	if runtime.GOOS != "linux" {
		return errors.New("-front reads /proc, which only Linux has")
	}
	if group, err := syscall.Getpgid(pgid); err == nil && group != pgid {
		return fmt.Errorf("-front %d leads no process group (it is in group %d)", pgid, group)
	}
	deadline := time.Now().Add(grace)
	wait := func(pid int, what string, done func(byte) bool) error {
		for !done(state(pid)) {
			if time.Now().After(deadline) {
				return fmt.Errorf("the front's process %d did not %s within %s", pid, what, grace)
			}
			time.Sleep(100 * time.Microsecond)
		}
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGSTOP); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop the front's process group %d: %w", pgid, err)
	}
	pids := []int{pgid}
	err := wait(pgid, "stop", func(s byte) bool { return s == 'T' || gone(s) })
	if err == nil && !gone(state(pgid)) {
		var listed []int
		listed, err = children("/proc", pgid)
		pids = append(pids, listed...)
	}
	// The group gets SIGKILL even when the stop wait or the list failed: a stopped process acts on
	// no other signal that ends it, so a group left stopped would outlast a caller's SIGTERM, and
	// the caller's wait for it (e2e-refill.sh's stop_front) would never return.
	if killErr := syscall.Kill(-pgid, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
		return errors.Join(err, fmt.Errorf("kill the front's process group %d: %w", pgid, killErr))
	}
	if err != nil {
		return err
	}
	for _, pid := range pids {
		if err := wait(pid, "exit", gone); err != nil {
			return err
		}
	}
	return nil
}

// children lists the processes that pid forked and that are still its children, from the
// children file of each of its threads under proc (/proc). A process has one such file for each
// thread, so finding none (a kernel built without CONFIG_PROC_CHILDREN) is an error, as is a file
// that cannot be read: an empty list would read as a front with no connection.
func children(proc string, pid int) ([]int, error) {
	pattern := filepath.Join(proc, strconv.Itoa(pid), "task", "*", "children")
	files, err := filepath.Glob(pattern)
	if err == nil && len(files) == 0 {
		err = fs.ErrNotExist
	}
	if err != nil {
		return nil, fmt.Errorf("list the children of the front's leader %d (%s): %w", pid, pattern, err)
	}
	var pids []int
	for _, file := range files {
		text, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("list the children of the front's leader %d: %w", pid, err)
		}
		for _, field := range strings.Fields(string(text)) {
			child, err := strconv.Atoi(field)
			if err != nil {
				return nil, fmt.Errorf("list the children of the front's leader %d: %s: %w", pid, file, err)
			}
			pids = append(pids, child)
		}
	}
	return pids, nil
}

// procState returns the state letter /proc gives pid (R, S, T, Z, X and so on), 0 once /proc lists
// it no more, and '?' when its stat cannot be read for another reason.
func procState(pid int) byte {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return 0
	}
	// The state follows the command name, which /proc writes in parentheses and which can itself
	// hold spaces and parentheses.
	end := bytes.LastIndexByte(stat, ')')
	if err != nil || end < 0 || end+2 >= len(stat) {
		return '?'
	}
	return stat[end+2]
}

// gone reports whether a process in state has exited: /proc lists it no more, or lists it as a
// zombie, whose descriptors the kernel has closed, or as dead.
func gone(state byte) bool {
	return state == 0 || state == 'Z' || state == 'X'
}
