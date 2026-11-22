package specgraph

import (
	"bufio"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sjawhar/envoy/internal/cmdtest"
)

// sweepReader is the reader that the sweeps judge, in a process of its own: the main package
// internal/sweepreader, which holds only this package's code and the standard library, and no
// _test.go file. Test code runs in the test process, in this package, so it can name and change
// what an in-process reader reads (the package's variables, the storage that a regexp or a
// replacer hands out, the exported values of the libraries that it calls); the reader's process
// shares no memory with the test process, so no test code, before the first test, in a writer, a
// cleanup or a goroutine, names or changes a value that the reader reads there. Only the operating
// system's access of a parent to its child reaches that memory (ptrace, /proc/<pid>/mem), and no
// test uses it. So each state that a sweep judges, and the states of a base that it judges by, come
// from that process alone (readerProcess). Its code and its settings are not out of reach: the test
// process builds the binary (newSweepReader) and starts a process of it for each base (start), so
// test code that runs before a process starts, before the build or after it, chooses the code that
// the process runs and the runtime settings that it gets: before the build, a test file's init among
// it, by ENVOY_TEST_GO_OVERLAY, GOFLAGS, the go on PATH or a file on disk; after the build, by a
// write over the binary or by a change of the runtime settings in the environment between two
// starts (readerEnv). So test code can hide a fault of the reader from the sweeps. Only a reader
// built and started outside the test process would close that.
type sweepReader struct{ binary string }

// newSweepReader builds the reader's binary once for the top-level test t (cmdtest.BuildCommand),
// which passes ENVOY_TEST_GO_OVERLAY to go build -overlay, so a probe that lays a fault in the
// reader's code by that overlay builds the binary with it: the binary is the code under test. The
// build reads that variable, the rest of the test process's environment and the files on disk when
// t calls it, after each init of the test binary has run, so test code chooses that code too; and
// the binary is a file in t's temporary folder, which each process that start starts runs, so test
// code that writes over it after the build chooses the code of those processes (sweepReader).
func newSweepReader(t *testing.T) sweepReader {
	t.Helper()
	return sweepReader{cmdtest.BuildCommand(t, cmdtest.Command{Name: "sweepreader", Target: "./internal/sweepreader"})}
}

// readerProcess is one process of the reader, which a sweep starts for each base: it reads one
// scenario per line of its input and answers each with one line (readerLine, ask).
type readerProcess struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr *cmdtest.LockedBuffer
}

// start starts one process of the reader for the test t, with the Go runtime's own settings alone
// (readerEnv), so that no other variable of the test's environment reaches it; it ends when t ends,
// and an exit other than 0 fails t with what the process wrote to its standard error.
func (r sweepReader) start(t *testing.T) *readerProcess {
	t.Helper()
	cmd := exec.Command(r.binary)
	cmd.Env = readerEnv()
	p := &readerProcess{cmd: cmd, stderr: &cmdtest.LockedBuffer{}}
	cmd.Stderr = p.stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("the reader's process: %v", err)
	}
	p.in, p.out = in, bufio.NewReader(out)
	t.Cleanup(func() {
		p.in.Close()
		if err := p.cmd.Wait(); err != nil {
			t.Errorf("the reader's process ended with %v: %s", err, p.stderr.String())
		}
	})
	return p
}

// readerEnv is the environment of the reader's process: the Go runtime's own settings and no
// variable of a test or of the application. GOMAXPROCS is the test process's own
// (runtime.GOMAXPROCS), and GOMEMLIMIT, GOGC and GODEBUG are the test environment's where it sets
// them, so each process runs under the runtime settings of the test that starts it. With no
// GOMAXPROCS, each process would size itself to the machine's processors, and a sweep's processes
// would start more threads together than a limit of the test's tasks allows.
func readerEnv() []string {
	env := []string{"GOMAXPROCS=" + strconv.Itoa(runtime.GOMAXPROCS(0))}
	for _, name := range []string{"GOMEMLIMIT", "GOGC", "GODEBUG"} {
		if value, set := os.LookupEnv(name); set {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// TestTheReadersProcessGetsTheGoRuntimesSettingsAlone starts a process as start starts the reader's,
// of the command env, which writes its environment, while the test's environment holds a variable of
// the application, the variable that names the overlay of the reader's build, and each of the
// runtime's settings: the process gets GOMAXPROCS as the test process runs with it, and GOMEMLIMIT,
// GOGC and GODEBUG as the test's environment sets them, and no other variable.
func TestTheReadersProcessGetsTheGoRuntimesSettingsAlone(t *testing.T) {
	binary, err := exec.LookPath("env")
	if err != nil {
		t.Fatalf("no command env to write the process's environment: %v", err)
	}
	t.Setenv("SWEEPREADER_PLANTED_APPLICATION_VARIABLE", "a variable of the application")
	t.Setenv("ENVOY_TEST_GO_OVERLAY", "/a/planted/overlay.json")
	t.Setenv("GOMEMLIMIT", "3GiB")
	t.Setenv("GOGC", "150")
	t.Setenv("GODEBUG", "gctrace=0")
	p := sweepReader{binary}.start(t)
	out, err := io.ReadAll(p.out)
	if err != nil {
		t.Fatalf("the process's environment: %v: %s", err, p.stderr.String())
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	want := []string{"GOMAXPROCS=" + strconv.Itoa(runtime.GOMAXPROCS(0)), "GOMEMLIMIT=3GiB", "GOGC=150", "GODEBUG=gctrace=0"}
	if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Errorf("the process started with the environment %q, and it should start with %q alone", got, want)
	}
}

// readerFile is one file of a scenario as a line of the reader's input names it: its name and its
// bytes, which encoding/json/v2 writes in base64.
type readerFile struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

// readerAnswer is the line that the reader answers one scenario with: the states of the scenario
// (sweepreader's sweepStates), or its refusal.
type readerAnswer struct {
	States  map[string]string `json:"states"`
	Refused bool              `json:"refused"`
	Refusal string            `json:"refusal"`
}

// readerLine is the line of the reader's input that names the scenario fsys: each file, in the order
// of the names, by its name and its bytes, as one JSON list and a line end; and key, the sha256 of
// those bytes, the very bytes that ask writes to the process. So two scenarios of the same files,
// byte for byte, have one line and one key, and the key of an input that a sweep judged holds what
// the reader read (sweepCount.judged).
func readerLine(t *testing.T, fsys fstest.MapFS) ([]byte, [sha256.Size]byte) {
	t.Helper()
	files := make([]readerFile, 0, len(fsys))
	for _, name := range slices.Sorted(maps.Keys(fsys)) {
		files = append(files, readerFile{Name: name, Data: fsys[name].Data})
	}
	line, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	line = append(line, '\n')
	return line, sha256.Sum256(line)
}

// ask writes line, a line of readerLine, to the process and reads its answer: the states, or the
// refusal of the reader or the derivation as the error. A process that ends, or that answers with no
// line of its form, fails the test with what it wrote to its standard error.
func (p *readerProcess) ask(t *testing.T, line []byte) (map[string]string, error) {
	t.Helper()
	if _, err := p.in.Write(line); err != nil {
		t.Fatalf("the reader's process took no line (%v): %s", err, p.stderr.String())
	}
	data, err := p.out.ReadBytes('\n')
	if err != nil {
		t.Fatalf("the reader's process gave no answer (%v): %s", err, p.stderr.String())
	}
	var a readerAnswer
	if err := json.Unmarshal(data, &a, json.RejectUnknownMembers(true)); err != nil {
		t.Fatalf("the reader's process answered %q: %v", data, err)
	}
	if a.Refused {
		return nil, errors.New(a.Refusal)
	}
	return a.States, nil
}

// states is what the process reads of the scenario fsys (readerLine, ask).
func (p *readerProcess) states(t *testing.T, fsys fstest.MapFS) (map[string]string, error) {
	t.Helper()
	line, _ := readerLine(t, fsys)
	return p.ask(t, line)
}
