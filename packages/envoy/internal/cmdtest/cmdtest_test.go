package cmdtest

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sjawhar/envoy/listenfd"
)

const (
	// heldPortChildEnv makes this test binary run heldPortChild instead of its tests.
	heldPortChildEnv = "CMDTEST_HELD_PORT_CHILD"
	// heldPortFD is the variable Pass names the child's inherited socket in.
	heldPortFD = "CMDTEST_LISTEN_FD"
)

func TestMain(m *testing.M) {
	if os.Getenv(heldPortChildEnv) == "1" {
		os.Exit(heldPortChild())
	}
	os.Exit(m.Run())
}

// heldPortChild is the process TestHeldPortStaysHeldUntilItsProcessServes starts. It waits for a
// line on standard input, then serves one connection on the socket it inherited, answering its own
// pid, and exits once the test closes that connection, so the test is the one to close first.
func heldPortChild() int {
	fail := func(format string, args ...any) int {
		fmt.Fprintf(os.Stderr, "held-port child: "+format+"\n", args...)
		return 1
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		return fail("wait for the line to serve: %v", err)
	}
	socket, err := listenfd.Inspect(heldPortFD, os.Getenv(heldPortFD))
	if err != nil {
		return fail("%v", err)
	}
	listener, err := listenfd.Adopt(socket)
	if err != nil {
		return fail("%v", err)
	}
	defer listener.Close()
	conn, err := listener.Accept()
	if err != nil {
		return fail("accept: %v", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "%d\n", os.Getpid()); err != nil {
		return fail("answer: %v", err)
	}
	if _, err := io.Copy(io.Discard, conn); err != nil {
		return fail("wait for the test to close the connection: %v", err)
	}
	return 0
}

// A held port stays bound from HoldPort until the process it was passed to serves on it, with only
// that process holding it once Start returns: another program's bind in the meantime is refused,
// the process serves on the socket, and nothing holds the socket once it exits.
func TestHeldPortStaysHeldUntilItsProcessServes(t *testing.T) {
	held := HoldPort(t)
	// The socket's inode names it whoever holds a descriptor of it, which a port does not: once the
	// socket is closed, another program may bind the port and listen there.
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(held.file.Fd()), &stat); err != nil {
		t.Fatalf("read the held socket's inode: %v", err)
	}
	inode := strconv.FormatUint(stat.Ino, 10)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), heldPortChildEnv+"=1")
	held.Pass(cmd, heldPortFD)
	serve, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	child := Start(t, "the held-port child", cmd)

	// The child is waiting for its line and has not served, so only its inherited copy can hold the
	// port.
	competitor, err := net.Listen("tcp", held.Addr)
	if err == nil {
		_ = competitor.Close()
		t.Fatalf("another listener bound %s after Start, before the child served; want address already in use", held.Addr)
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("another listener's bind of %s before the child served: %v; want address already in use", held.Addr, err)
	}
	if !SocketListens(t, inode) {
		t.Fatalf("/proc/net/tcp lists no listening socket of inode %s while the child holds it, so it cannot say when nothing does", inode)
	}

	if _, err := io.WriteString(serve, "serve\n"); err != nil {
		t.Fatalf("tell the child to serve: %v\n%s", err, child.Output.String())
	}
	_ = serve.Close()
	conn, err := net.Dial("tcp", held.Addr)
	if err != nil {
		t.Fatalf("dial %s: %v\n%s", held.Addr, err, child.Output.String())
	}
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	answer, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		_ = conn.Close()
		t.Fatalf("read the answer on %s: %v\n%s", held.Addr, err, child.Output.String())
	}
	if got, want := strings.TrimSpace(answer), strconv.Itoa(cmd.Process.Pid); got != want {
		_ = conn.Close()
		t.Fatalf("%s answered pid %q; want the child's %s", held.Addr, got, want)
	}
	_ = conn.Close()
	child.WaitExit(t, "the test closed its connection")
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("the child exited %d:\n%s", code, child.Output.String())
	}

	// The test's cleanup has not run yet, so no listening socket of the inode now shows Start closed
	// the test's copy, and the child released its own on exit.
	if SocketListens(t, inode) {
		t.Fatalf("a socket of inode %s still listens after the child exited: the test's copy or the child's was left open", inode)
	}
}

// StartThen runs afterExit once the process has exited and before it closes Exited, so a test
// that waits on Exited sees what afterExit did. afterExit sleeps before it records anything, so
// an Exited closed before afterExit returned would show here as nothing recorded.
func TestStartThenRunsAfterExitBeforeExited(t *testing.T) {
	// -test.run=^$ makes this test binary run no test and exit 0.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	var runs int
	var exitedFirst bool
	child := StartThen(t, "the exiting child", cmd, func() {
		exitedFirst = cmd.ProcessState != nil
		time.Sleep(200 * time.Millisecond)
		runs++
	})
	child.WaitExit(t, "its start")
	if runs != 1 {
		t.Fatalf("afterExit had run %d times when Exited closed; want 1", runs)
	}
	if !exitedFirst {
		t.Fatalf("afterExit ran before the process's exit was recorded (Cmd.ProcessState was nil)")
	}
}

// Source reads a file as BuildCommand's go build reads it, through the overlay that the build reads
// (Command.overlay), and the build reads that overlay and no other. The test builds a command whose
// file word.go declares the word that it prints, from the files on disk, and through an overlay given
// each way that one reaches the build: by ENVOY_TEST_GO_OVERLAY naming a file in a folder of its own
// (conf) whose paths are relative to the build's folder, and naming one whose paths are absolute; by
// the Command's own Flags, beside ENVOY_TEST_GO_OVERLAY, as -overlay with its value in the next
// argument and as --overlay=, and naming two, of which the last counts; by GOFLAGS of the Command's own
// Env, and of the test's environment, as -overlay= and in quotes around a path that holds a space; and
// by ENVOY_TEST_GO_OVERLAY beside GOFLAGS, in a build of its own. Each overlay puts a word.go of its own
// in word.go's place, takes not-go.go, a file that is not Go, out of the build, and puts a file in the
// place of the directory gone, under which gone/word.go is on disk. Each binary prints its leg's word,
// and Source reads that word in word.go; with no overlay Source reads not-go.go and gone/word.go, and
// under each overlay it gives no file for either. Then, with not-go.go gone from the disk, two builds
// read no overlay and print the disk's word: a Command whose Env holds no GOFLAGS, while the test's
// GOFLAGS names an overlay; and a Command built while only go's own environment file (GOENV) names an
// overlay, through GOFLAGS, which go env reads, so that a build handed no -overlay= would read it.
func TestSourceIsTheFileThatTheBuildReads(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/sourceprobe\n\ngo 1.22\n")
	write("main.go", "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Print(word) }\n")
	write("word.go", "package main\n\nconst word = \"disk\"\n")
	write("gone/word.go", "package gone\n\nconst word = \"gone\"\n")
	// overlay writes conf/<word>.json, an overlay that puts <word>/word.go, which declares word, in
	// word.go's place, takes not-go.go out and puts word.go in the place of the directory gone, each
	// path relative to the build's folder, or absolute where abs holds; it is the overlay's path,
	// written the same way.
	overlay := func(word string, abs bool) string {
		t.Helper()
		at := func(path string) string {
			if abs {
				return filepath.Join(dir, path)
			}
			return path
		}
		write(filepath.Join(word, "word.go"), fmt.Sprintf("package main\n\nconst word = %q\n", word))
		replace, err := json.Marshal(map[string]map[string]string{"Replace": {
			at("word.go"): at(filepath.Join(word, "word.go")), at("not-go.go"): "", at("gone"): at("word.go"),
		}})
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join("conf", word+".json"), string(replace))
		return at(filepath.Join("conf", word+".json"))
	}
	// leg is one build: what it is, ENVOY_TEST_GO_OVERLAY and GOFLAGS of the test's environment for
	// it, the Command, and the word that its binary prints.
	type leg struct {
		what, overlay, goflags string
		c                      Command
		word                   string
	}
	// buildLeg builds l's Command and fails the test unless its binary prints l's word and Source
	// reads that word in word.go.
	buildLeg := func(l leg) {
		t.Helper()
		t.Setenv("ENVOY_TEST_GO_OVERLAY", l.overlay)
		t.Setenv("GOFLAGS", l.goflags)
		out, err := exec.Command(BuildCommand(t, l.c)).Output()
		if err != nil {
			t.Errorf("%s: the built command: %v", l.what, err)
			return
		}
		src, err := l.c.Source("word.go")
		_, quoted, _ := strings.Cut(string(src), "const word = ")
		read, _ := strconv.Unquote(strings.TrimSpace(quoted))
		if string(out) != l.word || err != nil || read != l.word {
			t.Errorf("%s: the build prints %q and Source reads %q in word.go (%v), want both %q", l.what, out, read, err, l.word)
		}
	}
	goflags := os.Getenv("GOFLAGS")
	c := Command{Name: "sourceprobe", Dir: dir}
	buildLeg(leg{"no overlay", "", goflags, c, "disk"})
	write("not-go.go", "this is not Go\n")
	gone := []string{"not-go.go", filepath.Join("gone", "word.go")}
	for _, path := range gone {
		if _, err := c.Source(path); err != nil {
			t.Errorf("with no overlay, Source(%s): %v, where the file is on disk", path, err)
		}
	}
	envOverlay, goflagsOverlay := overlay("env", false), overlay("goflags", false)
	for _, l := range []leg{
		{"ENVOY_TEST_GO_OVERLAY naming conf/env.json, whose paths are relative to the build's folder", envOverlay, goflags, c, "env"},
		{"ENVOY_TEST_GO_OVERLAY naming an overlay, and each path in it, by an absolute path", overlay("abs", true), goflags, c, "abs"},
		{"the Command's Flags naming an overlay, beside ENVOY_TEST_GO_OVERLAY", envOverlay, goflags,
			Command{Name: "sourceprobe", Dir: dir, Flags: []string{"-overlay", overlay("flags", false)}}, "flags"},
		{"the Command's Flags naming an overlay by --overlay=, beside ENVOY_TEST_GO_OVERLAY", envOverlay, goflags,
			Command{Name: "sourceprobe-dashes", Dir: dir, Flags: []string{"--overlay=" + overlay("dashes", false)}}, "dashes"},
		{"the Command's Flags naming two overlays, of which the last counts", "", goflags,
			Command{Name: "sourceprobe-last", Dir: dir, Flags: []string{"-overlay=" + envOverlay, "-overlay", overlay("last", false)}}, "last"},
		{"GOFLAGS of the Command's Env naming an overlay", "", goflags,
			Command{Name: "sourceprobe", Dir: dir, Env: append(os.Environ(), "GOFLAGS="+goflags+" -overlay="+overlay("goenv", false))}, "goenv"},
		{"GOFLAGS of the test's environment naming an overlay", "", goflags + " -overlay=" + goflagsOverlay, c, "goflags"},
		{"GOFLAGS of the test's environment naming, in quotes, an overlay whose path holds a space", "",
			goflags + " '-overlay=" + overlay("in quotes", false) + "'", Command{Name: "sourceprobe-quoted", Dir: dir}, "in quotes"},
		{"ENVOY_TEST_GO_OVERLAY beside GOFLAGS of the test's environment", envOverlay, goflags + " -overlay=" + goflagsOverlay,
			Command{Name: "sourceprobe-both", Dir: dir}, "env"},
	} {
		buildLeg(l)
		for _, path := range gone {
			if src, err := l.c.Source(path); err == nil {
				t.Errorf("%s: Source reads %s as %q, which the overlay takes out of the build", l.what, path, src)
			}
		}
	}
	// The builds that read no overlay: not-go.go, which only an overlay takes out, leaves the disk first.
	if err := os.Remove(filepath.Join(dir, "not-go.go")); err != nil {
		t.Fatal(err)
	}
	var noGoflags []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GOFLAGS=") {
			noGoflags = append(noGoflags, kv)
		}
	}
	buildLeg(leg{"the Command's Env holding no GOFLAGS, beside GOFLAGS of the test's environment naming an overlay", "",
		goflags + " -overlay=" + goflagsOverlay, Command{Name: "sourceprobe-noflags", Dir: dir, Env: noGoflags}, "disk"})
	// An overlay that only go's own environment file names, through GOFLAGS, with GOFLAGS of the
	// environment empty: go reads it (go env GOFLAGS), and the build, handed -overlay= last, must not.
	goenvOverlay := filepath.Join(dir, overlay("goenvfile", false))
	write("go-env-file", "GOFLAGS=-overlay="+goenvOverlay+"\n")
	t.Setenv("GOENV", filepath.Join(dir, "go-env-file"))
	t.Setenv("GOFLAGS", "")
	goenv := exec.Command("go", "env", "GOFLAGS")
	goenv.Dir = dir
	if out, err := goenv.Output(); err != nil || strings.TrimSpace(string(out)) != "-overlay="+goenvOverlay {
		t.Fatalf("go env GOFLAGS, with GOENV naming a file that sets it: %q (%v), want the file's overlay", out, err)
	}
	buildLeg(leg{"an overlay that only go's environment file (GOENV) names", "", "", Command{Name: "sourceprobe-goenv", Dir: dir}, "disk"})
}
