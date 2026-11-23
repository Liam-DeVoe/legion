// Package cmdtest builds a command's binary and runs it as a process a test drives, for the tests
// of more than one command. It is an ordinary package rather than a _test.go file because every
// command is package main, and none can import another's test helpers.
package cmdtest

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/logtest"
)

// Build builds the main package in the test's working directory, its own command's, into a binary
// named name that is removed when the test ends.
func Build(t testing.TB, name string) string {
	t.Helper()
	return BuildCommand(t, Command{Name: name})
}

// BuildStamped is Build with the binary stamped with commit as the image build stamps it:
// -ldflags "-X main.buildCommit=<commit>", the variable cmd/dispatch reports on /healthz and
// registers its process under. A test that needs processes serving different builds builds one
// binary per commit.
func BuildStamped(t testing.TB, name, commit string) string {
	t.Helper()
	return BuildCommand(t, Command{Name: name, Flags: []string{"-ldflags", "-X main.buildCommit=" + commit}})
}

// Command is a binary to build: Name is what the binary is called under the test's own
// directory, Dir the directory go build runs in (the test's own when empty), Target what it
// builds ("." when empty), Flags the flags before the target, and Env the whole environment of
// the go command (the process's when nil).
type Command struct {
	Name   string
	Dir    string
	Target string
	Flags  []string
	Env    []string
}

// built is the binary each test has already built, by builtKey. go build relinks the whole
// binary into every -o path it is given - about a second apiece on a warm build cache, minutes on
// a loaded machine - and a row that starts five processes of one command asked for five of them
// (INFRA-985), so a test builds each command once and hands every process of that test the same
// binary. The key holds the test itself, so no binary outlives the TempDir it was written into,
// and the entry goes when the test ends.
var built sync.Map

// builtKey is one test's build of one command: the test, and everything that decides what go
// build writes - the command's name, directory, target, flags and environment, and the overlay.
type builtKey struct {
	t    testing.TB
	args string
}

// builtBinary is one memoised build: once makes it, path is what it wrote, empty where it failed.
type builtBinary struct {
	once sync.Once
	path string
}

// BuildCommand builds c and returns the binary's path. The binary is removed when the test ends,
// and the overlay of the build (Command.overlay: c's own -overlay flag, else ENVOY_TEST_GO_OVERLAY,
// else GOFLAGS's) is passed to go build as its last flag, so a probe can build it from changed
// sources without editing the tree; Command.Source reads a file as that build reads it. A test that
// asks for the same build again is handed the one it already built (built). Where the build failed,
// it fails the test, naming the build that failed: the one this call ran, after runBuild's line with
// the build's error, or an earlier call's in the same test, which it does not run again.
func BuildCommand(t testing.TB, c Command) string {
	t.Helper()
	overlay, err := c.overlay()
	if err != nil {
		t.Fatalf("build %s: %v", c.Name, err)
	}
	// The count of the flags stands between them and the environment, so a value that moves from
	// one of the two to the other is another key rather than the same one.
	parts := append([]string{c.Name, c.Dir, c.Target, overlay, strconv.Itoa(len(c.Flags))}, c.Flags...)
	key := builtKey{t: t, args: strings.Join(append(parts, c.Env...), "\x00")}
	entry, loaded := built.LoadOrStore(key, &builtBinary{})
	if !loaded {
		t.Cleanup(func() { built.Delete(key) })
	}
	binary := entry.(*builtBinary)
	ran := false
	binary.once.Do(func() { ran, binary.path = true, runBuild(t, c, overlay) })
	if binary.path == "" {
		which := "an earlier build of it in this test"
		if ran {
			which = "the build of it in this call"
		}
		t.Fatalf("build %s: %s failed", c.Name, which)
	}
	return binary.path
}

// runBuild is one go build of c, into a binary named c.Name under a directory of this test's
// own, with overlay, c's (Command.overlay), as its last flag: the go command takes the last value of
// a flag, and the command line's over GOFLAGS's, so the build reads that overlay and no other, none
// where it is "". It returns the empty path where the build failed, having failed the test. It builds
// with -buildvcs=false, so the binary records no commit whether the checkout is clean, as CI's is, or
// changed, as a developer's often is: internal/buildversion names a connection's build from that
// record, and a test that reads a connection's name or a log line's name field would otherwise
// pass on one checkout and fail on the other. A test that needs a stamped build passes its
// -ldflags in Command.Flags, as BuildStamped does.
func runBuild(t testing.TB, c Command, overlay string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), c.Name)
	args := append([]string{"build", "-buildvcs=false", "-o", binary}, c.Flags...)
	args = append(args, "-overlay="+overlay)
	target := c.Target
	if target == "" {
		target = "."
	}
	args = append(args, target)
	cmd := exec.Command("go", args...)
	cmd.Dir, cmd.Env = c.Dir, c.Env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("build %s: %v\n%s", c.Name, err, out)
		return ""
	}
	return binary
}

// overlay is the overlay file of BuildCommand's go build of c, "" for none: the value of the last
// -overlay of c.Flags, as the flag package reads one (-overlay or --overlay, with its value after "="
// or in the next argument); else ENVOY_TEST_GO_OVERLAY of this process; else the value of the last
// -overlay of GOFLAGS in the build's environment (c.Env, or this process's where c.Env is nil), as
// the go command splits GOFLAGS into flags (at white space: a space, a tab, a line end or a carriage
// return; a flag that starts with a single or a double quote taken whole, to the same quote). It is the
// one reading of the overlay of c's build: runBuild passes it to the build as its last flag, so the
// build reads it whatever GOFLAGS says, and Source reads each file through it. A relative path is read
// from c.Dir, as the go command reads it. TestSourceIsTheFileThatTheBuildReads holds each form above by a
// leg.
func (c Command) overlay() (string, error) {
	if overlay, set, err := lastFlag(c.Flags, "overlay"); err != nil || set {
		return overlay, err
	}
	if overlay := os.Getenv("ENVOY_TEST_GO_OVERLAY"); overlay != "" {
		return overlay, nil
	}
	goflags := os.Getenv("GOFLAGS")
	if c.Env != nil {
		goflags = ""
		for _, kv := range c.Env {
			if value, ok := strings.CutPrefix(kv, "GOFLAGS="); ok {
				goflags = value
			}
		}
	}
	flags, err := goflagsFlags(goflags)
	if err != nil {
		return "", err
	}
	overlay, _, err := lastFlag(flags, "overlay")
	return overlay, err
}

// lastFlag is the value of the last flag named name among args, as the flag package reads one: -name
// or --name, with its value after "=" or in the next argument; and whether one names it.
func lastFlag(args []string, name string) (string, bool, error) {
	value, set := "", false
	for i := 0; i < len(args); i++ {
		flag, ok := strings.CutPrefix(args[i], "-")
		if !ok {
			continue
		}
		named, given, hasValue := strings.Cut(strings.TrimPrefix(flag, "-"), "=")
		if named != name {
			continue
		}
		if !hasValue {
			if i+1 == len(args) {
				return "", false, fmt.Errorf("the flag %s has no value", args[i])
			}
			i++
			given = args[i]
		}
		value, set = given, true
	}
	return value, set, nil
}

// goflagsFlags is the flags of GOFLAGS, goflags, as the go command splits them (cmd/internal/quoted): at
// white space, a space, a tab, a line end or a carriage return, with a flag that starts with a single or
// a double quote taken whole, to the same quote, the white space within it too.
func goflagsFlags(goflags string) ([]string, error) {
	var flags []string
	for s := goflags; ; {
		s = strings.TrimLeft(s, " \t\n\r")
		if s == "" {
			return flags, nil
		}
		if quote := s[0]; quote == '"' || quote == '\'' {
			end := strings.IndexByte(s[1:], quote)
			if end < 0 {
				return nil, fmt.Errorf("GOFLAGS %q: an unterminated %c string", goflags, quote)
			}
			flags, s = append(flags, s[1:1+end]), s[2+end:]
			continue
		}
		end := strings.IndexAny(s, " \t\n\r")
		if end < 0 {
			end = len(s)
		}
		flags, s = append(flags, s[:end]), s[end:]
	}
}

// Source is the content of the file at path as BuildCommand's go build of c reads it, with the overlay
// of that build (Command.overlay) as go applies one: where the overlay names path, the file that it
// names in its place, or no file where it names none (""); where it names a directory above path, no
// file; and else the file on disk. A relative path, the overlay's own path and each path in it are
// read from c.Dir, the directory that go build runs in, or the test's working directory where c.Dir
// is empty, as go reads them. So a test that reads the code that it builds reads the code of the
// binary, overlay and all, for each Command.
func (c Command) Source(path string) ([]byte, error) {
	dir, err := filepath.Abs(c.Dir)
	if err != nil {
		return nil, err
	}
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
		return filepath.Join(dir, p)
	}
	file := abs(path)
	overlay, err := c.overlay()
	if err != nil {
		return nil, fmt.Errorf("the overlay of the build: %w", err)
	}
	if overlay == "" {
		return os.ReadFile(file)
	}
	data, err := os.ReadFile(abs(overlay))
	if err != nil {
		return nil, fmt.Errorf("the overlay of the build: %w", err)
	}
	var o struct{ Replace map[string]string }
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("the overlay of the build, %s: %w", overlay, err)
	}
	for from, to := range o.Replace {
		switch {
		case abs(from) != file:
		case to == "":
			return nil, fmt.Errorf("the overlay of the build, %s, takes %s out of it", overlay, file)
		default:
			return os.ReadFile(abs(to))
		}
	}
	for from := range o.Replace {
		if strings.HasPrefix(file, abs(from)+string(filepath.Separator)) {
			return nil, fmt.Errorf("the overlay of the build, %s, replaces %s, a directory above %s", overlay, abs(from), file)
		}
	}
	return os.ReadFile(file)
}

// Process is a binary a test started, with its combined output.
type Process struct {
	Cmd    *exec.Cmd
	Output *LockedBuffer
	// Exited is closed once the process has exited, Cmd.ProcessState is set and the afterExit
	// StartThen was given has returned.
	Exited <-chan struct{}
	// name is what failures call the process.
	name string
}

// Start starts cmd, which failures call name, with its standard output and error captured in
// Output, and kills it when the test ends if it is still running. It closes every file in
// cmd.ExtraFiles once cmd.Start returns, whether the process started or not (CloseExtraFiles).
func Start(t testing.TB, name string, cmd *exec.Cmd) *Process {
	t.Helper()
	return StartThen(t, name, cmd, nil)
}

// StartThen is Start with afterExit, which runs once cmd.Wait has returned and before Exited is
// closed, in the goroutine that waits on the process. So whoever sees Exited closed sees what
// afterExit did. afterExit never runs for a process that did not start, and nil runs nothing.
func StartThen(t testing.TB, name string, cmd *exec.Cmd, afterExit func()) *Process {
	t.Helper()
	exited := make(chan struct{})
	process := &Process{Cmd: cmd, Output: &LockedBuffer{}, Exited: exited, name: name}
	cmd.Stdout, cmd.Stderr = process.Output, process.Output
	err := cmd.Start()
	CloseExtraFiles(cmd)
	if err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	go func() {
		_ = cmd.Wait()
		if afterExit != nil {
			afterExit()
		}
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	return process
}

// CloseExtraFiles closes the test's copy of every file in cmd.ExtraFiles. Call it once cmd.Start
// returns, whether the process started or not: a started process holds its own copies from then
// on, so a socket a HeldPort passed it is released when it exits.
func CloseExtraFiles(cmd *exec.Cmd) {
	for _, file := range cmd.ExtraFiles {
		_ = file.Close()
	}
}

// Terminate sends the process SIGTERM, as a deploy stops it, and returns when it was sent.
func (p *Process) Terminate(t testing.TB) time.Time {
	t.Helper()
	signalled := time.Now()
	if err := p.Cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM %s: %v", p.name, err)
	}
	return signalled
}

// WaitExit waits up to 30 s for the process to exit, and fails the test with its output when it is
// still running; after names what it was waiting on.
func (p *Process) WaitExit(t testing.TB, after string) {
	t.Helper()
	select {
	case <-p.Exited:
	case <-time.After(30 * time.Second):
		t.Fatalf("%s was still running 30s after %s:\n%s", p.name, after, p.Output.String())
	}
}

// WaitForOutput waits up to 30 s for each of lines to appear in the process's output, and fails the
// test when the process exits first or a line never appears.
func (p *Process) WaitForOutput(t testing.TB, lines ...string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for _, line := range lines {
		for !strings.Contains(p.Output.String(), line) {
			select {
			case <-p.Exited:
				// Exited closes only once the output is whole, so this read is final.
				if strings.Contains(p.Output.String(), line) {
					continue
				}
				t.Fatalf("%s exited before it logged %q:\n%s", p.name, line, p.Output.String())
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never logged %q:\n%s", p.name, line, p.Output.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// HeldPort is a loopback TCP port the kernel chose, held by a listening socket from the moment
// HoldPort returns until the process the test passes it to serves on it, so no other program can
// take the port in between.
type HeldPort struct {
	// Port is the port the kernel chose.
	Port int
	// Addr is the address the socket listens on, 127.0.0.1:Port.
	Addr string
	// file holds the listening socket until Start has handed it to the process.
	file *os.File
}

// HoldPort opens a listening socket on a loopback port the kernel chooses, for the test to hand to
// the process it starts with Pass. The test's copy closes once Start has started that process, or
// when the test ends.
func HoldPort(t testing.TB) *HeldPort {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold a loopback port: %v", err)
	}
	// The file is a descriptor of its own for the socket, so the socket stays listening once the
	// listener is closed.
	file, err := listener.(*net.TCPListener).File()
	_ = listener.Close()
	if err != nil {
		t.Fatalf("hold a loopback port: take the listening socket's descriptor: %v", err)
	}
	// A File closed twice closes its descriptor once, so this is safe after Start has closed it.
	t.Cleanup(func() { _ = file.Close() })
	addr := listener.Addr().(*net.TCPAddr)
	return &HeldPort{Port: addr.Port, Addr: addr.String(), file: file}
}

// Pass hands the held socket to cmd: it adds the socket to cmd.ExtraFiles and sets envName to the
// descriptor number the process inherits it as. It appends to cmd.Env, or to this process's
// environment when cmd.Env is nil, so call it after cmd.Env is assigned. Start then closes the
// test's copy.
func (h *HeldPort) Pass(cmd *exec.Cmd, envName string) {
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	// ExtraFiles entry i is descriptor 3+i in the process.
	cmd.Env = append(cmd.Env, envName+"="+strconv.Itoa(3+len(cmd.ExtraFiles)))
	cmd.ExtraFiles = append(cmd.ExtraFiles, h.file)
}

// SocketListens reports whether /proc/net/tcp lists a listening socket of inode, on any address:
// the inode names a socket whoever holds a descriptor of it, where its port is any program's to
// bind once the socket is closed. A line's fourth field is its state, 0A for LISTEN, and its
// tenth the socket's inode.
func SocketListens(t testing.TB, inode string) bool {
	t.Helper()
	table, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		t.Fatalf("read the listening sockets: %v", err)
	}
	for line := range strings.SplitSeq(string(table), "\n") {
		if fields := strings.Fields(line); len(fields) > 9 && fields[3] == "0A" && fields[9] == inode {
			return true
		}
	}
	return false
}

// LockedBuffer is a buffer a process's output, or a logger, writes from more than one goroutine
// while a test reads it. Its body is logtest.Buffer's.
type LockedBuffer = logtest.Buffer
