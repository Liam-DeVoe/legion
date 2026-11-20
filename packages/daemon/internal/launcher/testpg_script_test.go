package launcher

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sjawhar/envoy/testdeadline"
	"github.com/sjawhar/legion/daemon/internal/testtmp"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// testPGScript is the worker image's script, which every other test of this file's package runs a
// stand-in of; LEGION_TESTPG_SCRIPT_UNDER_TEST names a copy instead, which a check of this test
// itself runs it against (a mutant of the script, or an older one).
var testPGScript = func() string {
	if script := os.Getenv("LEGION_TESTPG_SCRIPT_UNDER_TEST"); script != "" {
		return script
	}
	return filepath.Join("..", "..", "docker", testPGProgram)
}()

// scriptRig is a run of the real legion-testpg against this machine's real PostgreSQL server: the
// script and the server programs are the real ones, behind wrappers that record, each on a line of
// calls, the clock when they ran and their arguments (psql its PGCONNECT_TIMEOUT too), and then
// exec the real program. Two of them answer for what this machine may lack: psql answers the one
// statement that needs pgvector (`create extension if not exists vector`) itself, and pg_ctl gives
// a server older than 18 `log_connections=on` for the script's `all`, which 18 alone takes.
type scriptRig struct {
	t       *testing.T
	dir     string // LEGION_TESTPG_DIR
	calls   string
	partial string // the file whose presence makes the next initdb stop part way
	bin     string // first on PATH
	pgbin   string // PGBIN
	realBin string // the real server programs
	script  string // the script this rig runs; testPGScript when empty
	port    int
}

// scriptPGBin finds the server programs: LEGION_TEST_PGBIN, else the newest of Debian's and
// Ubuntu's /usr/lib/postgresql/<major>/bin. GitHub's ubuntu-24.04 runner has PostgreSQL 16 there,
// so in CI a machine with none fails the test rather than skip it.
func scriptPGBin(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("LEGION_TEST_PGBIN"); dir != "" {
		return dir
	}
	found, _ := filepath.Glob("/usr/lib/postgresql/*/bin/postgres")
	sort.Slice(found, func(i, j int) bool {
		a, _ := strconv.Atoi(filepath.Base(filepath.Dir(filepath.Dir(found[i]))))
		b, _ := strconv.Atoi(filepath.Base(filepath.Dir(filepath.Dir(found[j]))))
		return a > b
	})
	for _, postgres := range found {
		dir := filepath.Dir(postgres)
		if _, err := os.Stat(filepath.Join(dir, "initdb")); err == nil {
			return dir
		}
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Fatal("no PostgreSQL server under /usr/lib/postgresql/*/bin, and none named by LEGION_TEST_PGBIN: the runner image ships one, so this job must find it")
	}
	t.Skip("no PostgreSQL server under /usr/lib/postgresql/*/bin; set LEGION_TEST_PGBIN to run the real legion-testpg")
	return ""
}

func newScriptRig(t *testing.T) *scriptRig {
	t.Helper()
	realBin := scriptPGBin(t)
	tools := map[string]string{}
	for _, name := range []string{"psql", "pg_isready", "flock"} {
		path, err := exec.LookPath(name)
		if err != nil {
			if os.Getenv("GITHUB_ACTIONS") == "true" {
				t.Fatalf("%s is not on PATH: %v", name, err)
			}
			t.Skipf("%s is not on PATH: %v", name, err)
		}
		tools[name] = path
	}
	// The server's admin socket is <dir>/sockets/.s.PGSQL.<port>, and a Unix socket's path holds at
	// most 107 bytes (sun_path). testtmp.Dir keeps that path under the limit, falling back from a
	// long TMPDIR to /tmp only where the measured path needs it.
	root := testtmp.Dir(t, filepath.Join("testpg", "sockets", ".s.PGSQL.65535"))
	r := &scriptRig{t: t, dir: filepath.Join(root, "testpg"), calls: filepath.Join(root, "calls"),
		partial: filepath.Join(root, "partial-initdb"), bin: filepath.Join(root, "bin"),
		pgbin: filepath.Join(root, "pgbin"), realBin: realBin}
	for _, dir := range []string{r.bin, r.pgbin} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	record := `printf '%s %s\n' "$(date +%s)" "$(basename "$0") $*" >> '` + r.calls + `'`
	scripts := map[string]string{
		filepath.Join(r.bin, "psql"): `#!/bin/sh
printf '%s %s\n' "$(date +%s)" "psql PGCONNECT_TIMEOUT=${PGCONNECT_TIMEOUT-unset} $*" >> '` + r.calls + `'
case "$*" in *"create extension if not exists vector"*) exit 0 ;; esac
exec '` + tools["psql"] + `' "$@"
`,
		filepath.Join(r.bin, "pg_isready"): "#!/bin/sh\n" + record + "\nexec '" + tools["pg_isready"] + "' \"$@\"\n",
		filepath.Join(r.bin, "flock"):      "#!/bin/sh\n" + record + "\nexec '" + tools["flock"] + "' \"$@\"\n",
		filepath.Join(r.pgbin, "pg_ctl"): `#!/bin/sh
` + record + `
major=$('` + realBin + `/postgres' --version | sed -n 's/.* \([0-9][0-9]*\)\..*/\1/p')
if [ "${major:-0}" -lt 18 ]; then
	for arg do
		shift
		set -- "$@" "$(printf '%s' "$arg" | sed 's/log_connections=all/log_connections=on/')"
	done
fi
exec '` + realBin + `/pg_ctl' "$@"
`,
		// With the file partial in place, an initdb writes PG_VERSION and global/pg_control into its
		// data directory, removes the file, and is killed: what a stop of the script's process group
		// part way through a slow initdb leaves (both files exist before initdb ends: PG_VERSION is
		// its first write, pg_control its bootstrap's).
		filepath.Join(r.pgbin, "initdb"): `#!/bin/sh
` + record + `
if [ -e '` + r.partial + `' ]; then
	rm -f '` + r.partial + `'
	prev=
	for arg do [ "$prev" = --pgdata ] && data=$arg; prev=$arg; done
	mkdir -p "$data/global" && echo 16 > "$data/PG_VERSION" && echo partial > "$data/global/pg_control"
	kill -KILL $$
fi
exec '` + realBin + `/initdb' "$@"
`,
	}
	for path, text := range scripts {
		if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.port = freeScriptPort(t)
	t.Cleanup(r.stopNow)
	return r
}

// freeScriptPort is a loopback port the kernel chose and this process let go of again: the server
// the script starts has to bind the port its DSN names itself.
func freeScriptPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func (r *scriptRig) dsn() string {
	return "postgres://legion_test@127.0.0.1:" + strconv.Itoa(r.port) + "/legion_test?sslmode=disable"
}

// run runs `legion-testpg <command>`, told deadline as LEGION_TESTPG_DEADLINE where it is not the
// zero time, and answers its output and its error.
func (r *scriptRig) run(command string, deadline time.Time) (string, error) {
	r.t.Helper()
	script := r.script
	if script == "" {
		script = testPGScript
	}
	cmd := exec.Command("sh", script, command)
	env := withoutEnv(os.Environ(), testPGDeadlineVariable, "PATH", TestPGVariable, testPGDirVariable, testPGLogVariable)
	env = append(env, "PATH="+r.bin+string(os.PathListSeparator)+os.Getenv("PATH"), "PGBIN="+r.pgbin,
		TestPGVariable+"="+r.dsn(), testPGDirVariable+"="+r.dir)
	if !deadline.IsZero() {
		env = append(env, testPGDeadlineVariable+"="+strconv.FormatInt(deadline.Unix(), 10))
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ensure runs ensure again after another process wins the race between freeScriptPort and pg_ctl.
// It keeps trying until the test's own deadline, never behind a new fixed retry bound.
func (r *scriptRig) ensure(deadline time.Time) (string, error) {
	r.t.Helper()
	var output strings.Builder
	for {
		out, err := r.run("ensure", deadline)
		output.WriteString(out)
		if err == nil || !strings.Contains(r.serverLog(), `could not bind IPv4 address "127.0.0.1": Address already in use`) {
			return output.String(), err
		}
		if testDeadline, ok := r.t.Deadline(); ok && !time.Now().Before(testDeadline) {
			return output.String(), err
		}
		r.port = freeScriptPort(r.t)
	}
}

func TestScriptRigEnsureRetriesAPortAnotherProcessTook(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "port-conflict.sh")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
set -eu
mkdir -p "$LEGION_TESTPG_DIR"
attempt="$LEGION_TESTPG_DIR/attempt"
if [ ! -e "$attempt" ]; then
	: > "$attempt"
	printf '%s\n' 'could not bind IPv4 address "127.0.0.1": Address already in use' > "$LEGION_TESTPG_DIR/server.log"
	exit 1
fi
printf 'second ensure reached\n'
`), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &scriptRig{t: t, dir: filepath.Join(root, "testpg"), script: script, port: freeScriptPort(t)}
	port := r.port
	out, err := r.ensure(time.Time{})
	if err != nil {
		t.Fatalf("ensure after a port conflict = %v: %s", err, out)
	}
	if r.port == port {
		t.Fatal("ensure kept the port another process took")
	}
	if !strings.Contains(out, "second ensure reached") {
		t.Fatalf("ensure output %q does not show its retry", out)
	}
}

// serverLog answers the last lines of the server's log, for a failure's message.
func (r *scriptRig) serverLog() string {
	text, _ := os.ReadFile(filepath.Join(r.dir, "server.log"))
	lines := strings.Split(strings.TrimSpace(string(text)), "\n")
	if len(lines) > 15 {
		lines = lines[len(lines)-15:]
	}
	return strings.Join(lines, "\n")
}

// stopNow ends the rig's server at once, if one runs, when the test ends.
func (r *scriptRig) stopNow() {
	data := filepath.Join(r.dir, "data")
	if _, err := os.Stat(filepath.Join(data, "postmaster.pid")); err == nil {
		_ = exec.Command(filepath.Join(r.realBin, "pg_ctl"), "--pgdata", data, "--mode", "immediate", "--wait", "stop").Run()
	}
}

// scriptCall is one line of the rig's calls: the clock when the wrapper ran, and what it ran.
type scriptCall struct {
	at   int64
	line string
}

// recorded answers the calls recorded since the last take, and leaves the record as it is. A line a
// wrapper is still writing has no newline yet and is left out until it has one.
func (r *scriptRig) recorded() []scriptCall {
	r.t.Helper()
	text, err := os.ReadFile(r.calls)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	var calls []scriptCall
	for _, line := range strings.SplitAfter(string(text), "\n") {
		if !strings.HasSuffix(line, "\n") {
			continue
		}
		at, call, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
		seconds, err := strconv.ParseInt(at, 10, 64)
		if err != nil {
			r.t.Fatalf("a call line %q has no clock", line)
		}
		calls = append(calls, scriptCall{at: seconds, line: call})
	}
	return calls
}

// takeCalls answers the calls recorded since the last take, and empties the record.
func (r *scriptRig) takeCalls() []scriptCall {
	r.t.Helper()
	calls := r.recorded()
	if err := os.Remove(r.calls); err != nil && !os.IsNotExist(err) {
		r.t.Fatal(err)
	}
	return calls
}

// postmaster answers the pid the server of the rig's data directory wrote on the first line of its
// postmaster.pid.
func (r *scriptRig) postmaster() int {
	r.t.Helper()
	text, err := os.ReadFile(filepath.Join(r.dir, "data", "postmaster.pid"))
	if err != nil {
		r.t.Fatalf("no server of the rig's data directory wrote its pid: %v", err)
	}
	first, _, _ := strings.Cut(string(text), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil {
		r.t.Fatalf("the server's postmaster.pid begins %q, which is no pid", first)
	}
	return pid
}

// held says whether pid is stopped by a signal: the state /proc/<pid>/stat gives after the command
// in parentheses, read as zombie reads it.
func held(pid int) bool {
	body, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	_, after, ok := strings.Cut(string(body), ") ")
	return ok && strings.HasPrefix(after, "T")
}

// untilAnswered asks condition every testwait.Poll until it holds, and answers false once ctx ends
// first. ctx is the test's testdeadline.Answered context, so the wait has no bound of its own: what
// it waits for may be slow on a loaded machine, and one that never comes fails at the test's
// deadline, naming the wait.
func untilAnswered(ctx context.Context, condition func() bool) bool {
	for !condition() {
		select {
		case <-ctx.Done():
			return condition()
		case <-time.After(testwait.Poll):
		}
	}
	return true
}

// scriptBound is one bound a wait of the script was given: the wait, the clock it started at, and
// the value, "" where it was given none.
type scriptBound struct {
	wait  string
	at    int64
	value string
}

// bounds reads, from calls, the bound of each wait of the script on the server: a connection
// through the DSN (psql's PGCONNECT_TIMEOUT), a connection to the port (pg_isready's -t), the
// cluster's lock (flock's -w) and the server's start and stop (pg_ctl's --timeout, or `none` for
// --no-wait).
func (r *scriptRig) bounds(calls []scriptCall) []scriptBound {
	var out []scriptBound
	for _, call := range calls {
		words := strings.Fields(call.line)
		option := func(name string) string {
			for i, word := range words {
				if word == name && i+1 < len(words) {
					return words[i+1]
				}
			}
			return ""
		}
		switch {
		case words[0] == "psql" && strings.Contains(call.line, r.dsn()):
			out = append(out, scriptBound{"connect", call.at, strings.TrimPrefix(words[1], "PGCONNECT_TIMEOUT=")})
		case words[0] == "pg_isready":
			out = append(out, scriptBound{"pg_isready", call.at, option("-t")})
		case words[0] == "flock":
			out = append(out, scriptBound{"flock", call.at, option("-w")})
		case words[0] == "pg_ctl" && (strings.HasSuffix(call.line, " start") || strings.HasSuffix(call.line, " stop")):
			value := option("--timeout")
			if strings.Contains(call.line, "--no-wait") {
				value = "none"
			}
			out = append(out, scriptBound{"pg_ctl " + words[len(words)-1], call.at, value})
		}
	}
	return out
}

// Every wait of legion-testpg takes what its caller's deadline leaves, and has no bound of its own:
// the connections through the DSN and to the port, the cluster's lock, and the server's start and
// stop, run here against this machine's real PostgreSQL. With no deadline none has a bound: 0 for
// PGCONNECT_TIMEOUT and pg_isready, no -w for flock, and pg_ctl's largest count. Once the deadline
// has passed, stop still asks for the shutdown and ends at once, waiting on nothing (pg_ctl
// --no-wait). And a cluster an initdb left part way, as a stop of the script's process group during
// a slow initdb leaves one, is made again by the next start, which names what it removed: the part
// in initdb's own directory, and a data directory holding PG_VERSION and no global/pg_control, as an
// initdb of an earlier layout, which wrote in place, left one. A whole cluster is never made again
// or removed.
func TestLegionTestPGWaitsTakeTheCallersDeadlineAndAPartInitdbIsMadeAgain(t *testing.T) {
	r := newScriptRig(t)
	// An initdb stopped part way: the start fails, and leaves the part in its own directory.
	if err := os.WriteFile(r.partial, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.ensure(time.Now().Add(10 * time.Minute))
	if err == nil || !strings.Contains(out, "initdb failed") {
		t.Fatalf("legion-testpg ensure over an initdb killed part way = %v: %s; want initdb's failure named", err, out)
	}
	r.takeCalls()
	if _, err := os.Stat(filepath.Join(r.dir, "data", "PG_VERSION")); err == nil {
		t.Fatal("the part an initdb left is in the cluster's data directory, where the next start would take it for a whole cluster")
	}

	// The next start, with a deadline: it removes the part, makes the cluster, serves, and every wait
	// is given what the deadline leaves when it starts.
	began := time.Now()
	deadline := began.Add(10 * time.Minute)
	out, err = r.ensure(deadline)
	if err != nil {
		t.Fatalf("legion-testpg ensure after a part initdb = %v: %s; want the cluster made again and served; the server's log ends:\n%s", err, out, r.serverLog())
	}
	if !strings.Contains(out, "removing "+filepath.Join(r.dir, "data.initdb")+", which holds no whole cluster") {
		t.Errorf("legion-testpg ensure after a part initdb said %q, want the part it removed named", out)
	}
	if answer, err := exec.Command("psql", "-X", "-w", "-tAq", r.dsn(), "-c", "select 1").CombinedOutput(); err != nil || strings.TrimSpace(string(answer)) != "1" {
		t.Fatalf("the DSN does not answer after ensure: %v: %s", err, answer)
	}
	seen := map[string]bool{}
	for _, b := range r.bounds(r.takeCalls()) {
		seen[b.wait] = true
		value, err := strconv.ParseInt(b.value, 10, 64)
		if err != nil || value > deadline.Unix()-began.Unix() || value < deadline.Unix()-b.at {
			t.Errorf("%s was given %q at %d, want what the deadline %d left: %d to %d", b.wait, b.value, b.at,
				deadline.Unix(), deadline.Unix()-b.at, deadline.Unix()-began.Unix())
		}
	}
	for _, wait := range []string{"connect", "pg_isready", "flock", "pg_ctl start"} {
		if !seen[wait] {
			t.Errorf("legion-testpg ensure ran no %s, so its bound was not read", wait)
		}
	}
	stopAt := time.Now()
	if out, err := r.run("stop", deadline); err != nil {
		t.Fatalf("legion-testpg stop = %v: %s", err, out)
	}
	stops := r.bounds(r.takeCalls())
	if len(stops) != 1 || stops[0].wait != "pg_ctl stop" {
		t.Fatalf("legion-testpg stop ran %v, want one pg_ctl stop", stops)
	}
	if value, err := strconv.ParseInt(stops[0].value, 10, 64); err != nil || value > deadline.Unix()-stopAt.Unix() || value < deadline.Unix()-stops[0].at {
		t.Errorf("pg_ctl stop was given %q, want what the deadline left", stops[0].value)
	}

	// A whole cluster: the next start, with no deadline, serves it as it is, makes nothing again and
	// removes nothing, and no wait has a bound.
	marker := filepath.Join(r.dir, "data", "marker-of-this-cluster")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = r.ensure(time.Time{})
	if err != nil {
		t.Fatalf("legion-testpg ensure over a whole cluster = %v: %s", err, out)
	}
	calls := r.takeCalls()
	if strings.Contains(out, "removing") {
		t.Errorf("legion-testpg ensure over a whole cluster removed something: %s", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("a file of the whole cluster is gone after the next ensure (%v): the cluster was made again", err)
	}
	for _, call := range calls {
		if strings.HasPrefix(call.line, "initdb ") {
			t.Errorf("legion-testpg ensure over a whole cluster ran %q", call.line)
		}
	}
	want := map[string]string{"connect": "0", "pg_isready": "0", "flock": "", "pg_ctl start": "214748364"}
	seen = map[string]bool{}
	for _, b := range r.bounds(calls) {
		seen[b.wait] = true
		if b.value != want[b.wait] {
			t.Errorf("with no deadline %s was given %q, want %q, no bound", b.wait, b.value, want[b.wait])
		}
	}
	for _, wait := range []string{"connect", "pg_isready", "flock", "pg_ctl start"} {
		if !seen[wait] {
			t.Errorf("legion-testpg ensure with no deadline ran no %s, so its bound was not read", wait)
		}
	}
	if out, err := r.run("stop", time.Time{}); err != nil {
		t.Fatalf("legion-testpg stop with no deadline = %v: %s", err, out)
	}
	if stops := r.bounds(r.takeCalls()); len(stops) != 1 || stops[0].value != "214748364" {
		t.Errorf("legion-testpg stop with no deadline ran %v, want one pg_ctl stop given 214748364", stops)
	}

	// The steps below take what the test binary's deadline leaves, less its cleanup reserve: the
	// script is told it as its caller's deadline, and the test's own waits below (for the hold, the
	// late stop and the server's exit) end at it. With no test deadline (-timeout 0) none has one.
	ctx, cancel := testdeadline.Answered(t, context.Background())
	defer cancel()
	answered, _ := ctx.Deadline()

	// A data directory holding PG_VERSION and no global/pg_control: initdb writes PG_VERSION first
	// and pg_control in its bootstrap step, so an initdb of an earlier layout, which wrote in place,
	// left this when it was stopped before that step. PG_VERSION alone is no whole cluster: the next
	// start names the directory, removes it, runs initdb again and serves.
	data := filepath.Join(r.dir, "data")
	if err := os.RemoveAll(data); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(data, "global"), 0o700); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(data, "marker-of-the-part")
	for path, text := range map[string]string{filepath.Join(data, "PG_VERSION"): "16\n", part: ""} {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err = r.ensure(answered)
	if err != nil {
		t.Fatalf("legion-testpg ensure over a data directory holding PG_VERSION and no global/pg_control = %v: %s; want that directory removed, initdb run again and the DSN served; the server's log ends:\n%s", err, out, r.serverLog())
	}
	if !strings.Contains(out, "removing "+data+", which holds no whole cluster") {
		t.Errorf("legion-testpg ensure over a data directory holding PG_VERSION and no global/pg_control said %q, want that directory named as removed", out)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Errorf("a file of the data directory holding PG_VERSION and no global/pg_control is still there after the next ensure (%v): the directory was not removed", err)
	}
	initdb := false
	for _, call := range r.takeCalls() {
		initdb = initdb || strings.HasPrefix(call.line, "initdb ")
	}
	if !initdb {
		t.Error("legion-testpg ensure over a data directory holding PG_VERSION and no global/pg_control ran no initdb")
	}
	if answer, err := exec.Command("psql", "-X", "-w", "-tAq", r.dsn(), "-c", "select 1").CombinedOutput(); err != nil || strings.TrimSpace(string(answer)) != "1" {
		t.Fatalf("the DSN does not answer after ensure over a data directory holding PG_VERSION and no global/pg_control: %v: %s", err, answer)
	}

	// The caller's deadline has passed: stop still asks for the shutdown and does not wait for it
	// (pg_ctl --no-wait), so it ends at once. The test holds the postmaster (SIGSTOP) from before the
	// stop until the stop has returned, so a stop that waits for the server cannot end while it is
	// held. Once the record shows pg_ctl stop given anything but --no-wait, the hold is let go, so
	// such a stop fails here rather than at the test's deadline. Let go, the server leaves: the
	// shutdown was asked for.
	pid := r.postmaster()
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatalf("holding the postmaster %d: %v", pid, err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			_ = syscall.Kill(pid, syscall.SIGCONT)
		}
	}
	t.Cleanup(release)
	if !untilAnswered(ctx, func() bool { return held(pid) }) {
		t.Fatalf("the postmaster %d was still not stopped by the test's SIGSTOP at the test's deadline", pid)
	}
	type result struct {
		out string
		err error
	}
	stopped := make(chan result, 1)
	go func() {
		out, err := r.run("stop", time.Now().Add(-time.Second))
		stopped <- result{out, err}
	}()
	var stop result
	waited := ""
	for ended := false; !ended; {
		select {
		case stop = <-stopped:
			ended = true
		case <-ctx.Done():
			if waited == "" {
				waited = "the test's deadline, less its cleanup reserve, passed while it ran"
			}
			release()
			stop, ended = <-stopped, true
		case <-time.After(testwait.Poll):
			for _, b := range r.bounds(r.recorded()) {
				if b.wait == "pg_ctl stop" && b.value != "none" && waited == "" {
					waited = "its pg_ctl stop was given --timeout " + strconv.Quote(b.value) + " in place of --no-wait, so it waited for the server"
					release()
				}
			}
		}
	}
	if waited != "" {
		t.Errorf("legion-testpg stop with its caller's deadline passed did not end while the server was held: %s; want it to ask for the shutdown and end at once", waited)
	}
	if stop.err != nil {
		t.Errorf("legion-testpg stop with its caller's deadline passed = %v: %s", stop.err, stop.out)
	}
	if stops := r.bounds(r.takeCalls()); len(stops) != 1 || stops[0].wait != "pg_ctl stop" || stops[0].value != "none" {
		t.Errorf("legion-testpg stop with its caller's deadline passed ran %v, want one pg_ctl stop given --no-wait (none)", stops)
	}
	release()
	if !untilAnswered(ctx, func() bool {
		_, err := os.Stat(filepath.Join(data, "postmaster.pid"))
		return os.IsNotExist(err) && (errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) || zombie(pid))
	}) {
		t.Fatalf("the server (postmaster %d) still ran at the test's deadline after legion-testpg stop with its caller's deadline passed: the stop did not ask for its shutdown; the server's log ends:\n%s", pid, r.serverLog())
	}
}
