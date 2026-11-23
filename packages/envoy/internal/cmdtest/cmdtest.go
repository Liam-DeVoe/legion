// Package cmdtest builds a command's binary and runs it as a process a test drives, for the tests
// of more than one command. It is an ordinary package rather than a _test.go file because every
// command is package main, and none can import another's test helpers.
package cmdtest

import (
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
// named name, removed when the test ends, or once the tests have run under Main (BuildCommand).
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

// Command is a binary to build: Name is what the binary is called in its directory, Dir the
// directory go build runs in (the test's own when empty), Target what it builds ("." when empty),
// Flags the flags before the target, and Env the whole environment of the go command (the
// process's when nil).
type Command struct {
	Name   string
	Dir    string
	Target string
	Flags  []string
	Env    []string
}

// built is the binary each test has already built, by builtKey, in a test process whose TestMain
// does not run Main or Run. go build relinks the whole binary into every -o path it is given -
// about a second apiece on a warm build cache, minutes on a loaded machine - and a row that starts
// five processes of one command asked for five of them (INFRA-985), so a test builds each command
// once and hands every process of that test the same binary. The key holds the test itself, so no
// binary outlives the TempDir it was written into, and the entry goes when the test ends.
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

// process is the build of each command for a whole test process, while its TestMain runs Main or
// Run: a package whose tests each run the same command (cmd/cipher's ran 100 builds of
// agent-secrets, 247.5 s of a 605 s run) links it once. Each build has a subdirectory of its own in
// dir, so the directory of a binary holds that binary alone (a test may put it on PATH); dir is
// made before the tests run, so a test that moves TMPDIR cannot place it inside a directory of its
// own that goes when it ends, and it is removed when they have run.
var process struct {
	mu     sync.Mutex
	on     bool
	dir    string
	builds map[string]*processBinary
}

// processBinary is one build of the process: mu is held while it is made, so a test that asks for
// it meanwhile waits for that build instead of making another; path is empty until a build
// succeeds, so a test that asks after a failed one builds again, with its own output.
type processBinary struct {
	mu   sync.Mutex
	path string
}

// processDirPrefix begins the name of each process's directory of builds, followed by its pid and
// a dash: Run removes those of this user's processes that no longer run, which go test's alarm
// leaves behind, since the alarm ends the process before Run can remove its own.
const processDirPrefix = "cmdtest-"

// Main runs the package's tests with one build of each command for the whole process
// (BuildCommand), removes those builds once they have run, and returns the exit code. A package
// whose tests build the same command in many tests calls it from TestMain:
// os.Exit(cmdtest.Main(m)).
func Main(m *testing.M) int { return Run(m.Run) }

// Run is Main for a TestMain that also runs another helper's: it runs run, the package's tests,
// with one build of each command for the whole process, and removes those builds afterwards:
// os.Exit(cmdtest.Run(func() int { return testnats.Main(m) })). Before the tests, it removes the
// directories of builds that this user's test processes left behind and that no longer run.
func Run(run func() int) int {
	removeBuildsOfEndedProcesses()
	dir, err := os.MkdirTemp("", processDirPrefix+strconv.Itoa(os.Getpid())+"-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmdtest: make the directory of the process's builds: %v\n", err)
		return 1
	}
	process.mu.Lock()
	process.on, process.dir, process.builds = true, dir, map[string]*processBinary{}
	process.mu.Unlock()
	code := run()
	process.mu.Lock()
	process.on, process.dir, process.builds = false, "", nil
	process.mu.Unlock()
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "cmdtest: remove the process's builds: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

// BuildCommand builds c and returns the binary's path. ENVOY_TEST_GO_OVERLAY, when set, is passed
// to go build -overlay, so a probe can build it from changed sources without editing the tree. In a
// test process whose TestMain runs Main or Run, every test that asks for the same build is handed
// the one binary the process built (process), removed once the tests have run; otherwise a test
// that asks again is handed the one it already built (built), removed when the test ends.
func BuildCommand(t testing.TB, c Command) string {
	t.Helper()
	overlay := os.Getenv("ENVOY_TEST_GO_OVERLAY")
	// The count of the flags stands between them and the environment, so a value that moves from
	// one of the two to the other is another key rather than the same one.
	parts := append([]string{c.Name, c.Dir, c.Target, overlay, strconv.Itoa(len(c.Flags))}, c.Flags...)
	args := strings.Join(append(parts, c.Env...), "\x00")
	if path, ok := processBuild(t, c, overlay, args); ok {
		return path
	}
	key := builtKey{t: t, args: args}
	entry, loaded := built.LoadOrStore(key, &builtBinary{})
	if !loaded {
		t.Cleanup(func() { built.Delete(key) })
	}
	binary := entry.(*builtBinary)
	binary.once.Do(func() { binary.path = runBuild(t, c, overlay, t.TempDir()) })
	if binary.path == "" {
		t.Fatalf("build %s: an earlier build of it in this test failed", c.Name)
	}
	return binary.path
}

// processBuild answers the process's build of args (process), making it where no test has, and
// reports whether the process keeps builds at all. A build that fails fails t.
func processBuild(t testing.TB, c Command, overlay, args string) (string, bool) {
	t.Helper()
	process.mu.Lock()
	if !process.on {
		process.mu.Unlock()
		return "", false
	}
	binary := process.builds[args]
	if binary == nil {
		binary = &processBinary{}
		process.builds[args] = binary
	}
	dir := process.dir
	process.mu.Unlock()
	binary.mu.Lock()
	defer binary.mu.Unlock()
	if binary.path != "" {
		return binary.path, true
	}
	sub, err := os.MkdirTemp(dir, "build-")
	if err != nil {
		t.Fatalf("build %s: make its directory: %v", c.Name, err)
	}
	if binary.path = runBuild(t, c, overlay, sub); binary.path == "" {
		t.FailNow()
	}
	return binary.path, true
}

// runBuild is one go build of c, into a binary named c.Name in dir. It returns the empty path where
// the build failed, having failed the test. It builds with -buildvcs=false, so the binary records
// no commit whether the checkout is clean, as CI's is, or changed, as a developer's often is:
// internal/buildversion names a connection's build from that record, and a test that reads a
// connection's name or a log line's name field would otherwise pass on one checkout and fail on the
// other. A test that needs a stamped build passes its -ldflags in Command.Flags, as BuildStamped
// does.
func runBuild(t testing.TB, c Command, overlay, dir string) string {
	t.Helper()
	binary := filepath.Join(dir, c.Name)
	args := []string{"build", "-buildvcs=false", "-o", binary}
	if overlay != "" {
		args = append(args, "-overlay", overlay)
	}
	args = append(args, c.Flags...)
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
