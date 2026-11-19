package launcher

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stubTestPG puts a `legion-testpg` on PATH that appends one line per call — the subcommand, the
// DSN it was given, the directory it was told, the log it was told, and `present` or `absent` for
// whether that log was a file when it ran — to a file, and answers the exit code the file `exit`
// holds. It answers what the worker image's script answers, so the launcher's side is what the
// test reads.
func stubTestPG(t *testing.T) (calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	script := "#!/bin/sh\nif [ -f \"$" + testPGLogVariable + "\" ]; then log=present; else log=absent; fi\n" +
		"printf '%s %s %s %s %s\\n' \"$1\" \"$" + TestPGVariable + "\" \"$" + testPGDirVariable + "\" \"$" + testPGLogVariable + "\" \"$log\" >> " + calls +
		"\nexit $(cat " + filepath.Join(dir, "exit") + ")\n"
	if err := os.WriteFile(filepath.Join(dir, testPGProgram), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "exit"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// toldTestPG starts generation with env as its start command's own Env, answers what
// TestPGVariable and TestPGLogVariable held in the child's environment, in that order and joined
// by "|", and stops the generation. A variable the child did not hold answers "unset": the shell's
// `${name-unset}` answers the value for a name set to anything, including the empty string, and
// "unset" only for a name its environment does not hold.
func (g *rig) toldTestPG(generation uint64, env []string) string {
	g.t.Helper()
	return g.told(generation, "${"+TestPGVariable+"-unset}|${"+TestPGLogVariable+"-unset}", env, "")
}

// The launcher that serves the pod's test database names it to every generation it starts, over
// whatever DSN the daemon's start command carried, and a launcher that serves none takes the
// variable out of the generation's environment. The daemon and the worker image are deployed
// from two separately pinned builds and this launcher comes from the image, so the process that
// creates the database is the one that names it: a role is told a database its own pod's server
// holds whichever of the two builds is older. It reads whether this launcher was told to serve a
// database, never whether a server answers: the first `ensure` runs before any role and its
// failure is logged rather than thrown, so neither rig here runs a server at all. The server's
// log goes with the DSN: a serving launcher's generation is told the path its role-init binds
// the log at, under the fixed root, over whatever its environment carried, and a launcher that
// serves none tells its generation of no log.
func TestEveryGenerationIsToldTheDSNItsOwnLauncherServes(t *testing.T) {
	// What a daemon of another build sends: the database every image's server serves. No daemon
	// sends the log's path; a value a start command carried all the same is replaced by this
	// launcher's own, or dropped, as the DSN is.
	const fromTheDaemon = "postgres://legion_test@127.0.0.1:5433/postgres?sslmode=disable"
	const fromElsewhere = "/somewhere/else.log"
	carried := []string{TestPGVariable + "=" + fromTheDaemon, TestPGLogVariable + "=" + fromElsewhere}
	// The DSN this launcher serves and the path role-init binds the server's log at.
	want := TestPGDSN + "|" + filepath.Join(roleFixedRoot, RoleTestPGLog)
	serving := newSessionRig(t, Config{TestPostgres: true})
	if got := serving.toldTestPG(1, carried); got != want {
		t.Errorf("a generation whose start command carried %s=%q and %s=%q ran with %s|%s=%q, want this launcher's own %q",
			TestPGVariable, fromTheDaemon, TestPGLogVariable, fromElsewhere, TestPGVariable, TestPGLogVariable, got, want)
	}
	if got := serving.toldTestPG(2, nil); got != want {
		t.Errorf("a generation whose start command named neither ran with %s|%s=%q, want this launcher's own %q",
			TestPGVariable, TestPGLogVariable, got, want)
	}
	none := newSessionRig(t, Config{})
	if got := none.toldTestPG(1, carried); got != "unset|unset" {
		t.Errorf("a generation of a launcher that serves no test database ran with %s|%s=%q, want neither variable",
			TestPGVariable, TestPGLogVariable, got)
	}
}

// The launcher serves the pod's test database before it accepts a role start: serveTestPG's first
// `ensure` has returned when it does, so a role that starts at once finds the server listening.
// Each tick asks again, which is what starts a postmaster that has died, and every call carries
// the DSN the roles are told, the cluster's directory under the roles directory and the server's
// log in it, which is a file before the first `ensure` runs: every role started after it has the
// log to bind, whatever that `ensure` came to.
func TestTheLauncherServesTheTestDatabaseBeforeAnyRoleAndKeepsAsking(t *testing.T) {
	calls := stubTestPG(t)
	testPGInterval = 20 * time.Millisecond
	t.Cleanup(func() { testPGInterval = 15 * time.Second })
	rolesDir := t.TempDir()
	var log bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	// The ticker goroutine is waited for before the cleanup above restores the interval, so no
	// read of it outlives this test.
	done := serveTestPG(ctx, Config{RolesDir: rolesDir, TestPostgres: true}, &roleLog{stdout: &log, stderr: &log}, &pidGuard{})
	t.Cleanup(func() { cancel(); <-done })
	first, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("serveTestPG returned before its first ensure ran: %v", err)
	}
	want := "ensure " + TestPGDSN + " " + filepath.Join(rolesDir, TestPGDirName) + " " + testPGLog(rolesDir) + " present\n"
	if string(first) != want {
		t.Errorf("the first call is %q, want %q", first, want)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		again, err := os.ReadFile(calls)
		if err == nil && strings.Count(string(again), "ensure ") > 1 {
			if got := strings.ReplaceAll(string(again), want, ""); got != "" {
				t.Errorf("a later call is %q, want every call to be %q", got, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the launcher asked once and never again: %q", again)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A log the launcher finds is kept as it is: a launcher that restarts in a pod whose server has
// run leaves that server's lines where every role reads them. And a log that cannot be made is
// logged and nothing else, as a script that fails is: the script still runs, and the pod still
// runs its roles.
func TestATestDatabaseLogIsKeptAndOneThatCannotBeMadeOnlyLogs(t *testing.T) {
	calls := stubTestPG(t)
	testPGInterval = time.Hour
	t.Cleanup(func() { testPGInterval = 15 * time.Second })
	serve := func(rolesDir string) string {
		t.Helper()
		log := &lockedBuffer{}
		ctx, cancel := context.WithCancel(context.Background())
		done := serveTestPG(ctx, Config{RolesDir: rolesDir, TestPostgres: true}, &roleLog{stdout: log, stderr: log}, &pidGuard{})
		cancel()
		<-done
		return log.String()
	}
	rolesDir := t.TempDir()
	const earlier = "an earlier server's line\n"
	if err := os.MkdirAll(testPGDir(rolesDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPGLog(rolesDir), []byte(earlier), 0o600); err != nil {
		t.Fatal(err)
	}
	if said := serve(rolesDir); strings.Contains(said, "launcher: test database:") {
		t.Errorf("over a log already there the container log holds %q, want no refusal", said)
	}
	if kept, err := os.ReadFile(testPGLog(rolesDir)); err != nil || string(kept) != earlier {
		t.Errorf("the log the launcher found holds %q (%v), want it kept as %q", kept, err, earlier)
	}
	// A roles directory that is a file: nothing can be made under it.
	blocked := filepath.Join(t.TempDir(), "roles")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(calls); err != nil {
		t.Fatal(err)
	}
	if said := serve(blocked); !strings.Contains(said, "launcher: test database: make the server's directory:") {
		t.Errorf("over a log that cannot be made the container log holds %q, want the refusal named", said)
	}
	want := "ensure " + TestPGDSN + " " + testPGDir(blocked) + " " + testPGLog(blocked) + " absent\n"
	if ran, err := os.ReadFile(calls); err != nil || string(ran) != want {
		t.Errorf("after a log that could not be made the script ran %q (%v), want %q", ran, err, want)
	}
}

// A script that fails is logged and nothing else: a pod whose test database cannot start still
// runs its roles, where a launcher that failed its container would cost the issue every role it
// serves.
func TestATestDatabaseThatCannotStartOnlyLogs(t *testing.T) {
	calls := stubTestPG(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(calls), "exit"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testPGInterval = time.Hour
	t.Cleanup(func() { testPGInterval = 15 * time.Second })
	// The log is read here while the script's output is still being copied into it: cmd.Wait
	// returns at WaitDelay with that copy goroutine still running, so the buffer the roleLog
	// writes through must be one that holds its own lock against this read.
	log := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := serveTestPG(ctx, Config{RolesDir: t.TempDir(), TestPostgres: true}, &roleLog{stdout: log, stderr: log}, &pidGuard{})
	t.Cleanup(func() { cancel(); <-done })
	if !strings.Contains(log.String(), "launcher: test database:") {
		t.Errorf("the container log holds %q, want the refusal named", log.String())
	}
}

// The guard holds a running child out of the reaper's pass and forgets it once its own wait has
// read its exit, so nothing of the launcher's own is left in the skip list between calls.
func TestTheGuardForgetsAChildOnceItsWaitHasRead(t *testing.T) {
	calls := stubTestPG(t)
	guard := &pidGuard{}
	if err := runTestPG(context.Background(), Config{RolesDir: t.TempDir(), TestPostgres: true}, &roleLog{}, guard, "ensure"); err != nil {
		t.Fatalf("runTestPG: %v", err)
	}
	if held := guard.held(); len(held) != 0 {
		t.Errorf("the guard still holds %v", held)
	}
	if _, err := os.Stat(calls); err != nil {
		t.Errorf("the script never ran: %v", err)
	}
}

// One run of the script is bounded, so an `ensure` that hangs — a lock a dead holder still owns —
// costs a pod one wait and a log line, not its whole boot: the launcher's first `ensure` runs
// before any role starts, inside the daemon's boot timeout.
func TestATestDatabaseRunThatHangsIsBounded(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, testPGProgram), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	testPGBound = 200 * time.Millisecond
	t.Cleanup(func() { testPGBound = 60 * time.Second })
	started := time.Now()
	err := runTestPG(context.Background(), Config{RolesDir: t.TempDir(), TestPostgres: true}, &roleLog{}, &pidGuard{}, "ensure")
	if err == nil || !strings.Contains(err.Error(), "did not finish within") {
		t.Fatalf("runTestPG = %v, want the bound named", err)
	}
	if waited := time.Since(started); waited > 10*time.Second {
		t.Errorf("runTestPG waited %s on a script that hangs", waited)
	}
}

// The image probe runs what a pod's launcher runs and then proves the DSN every role is told:
// check, ensure, a database made and dropped through it, and the server stopped, so the probe pod
// leaves nothing running. A step that fails is answered with its output, and the server is stopped
// all the same.
func TestCheckTestPostgresRunsEveryStepAndStopsTheServer(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	for _, program := range []string{testPGProgram, "psql"} {
		script := "#!/bin/sh\nprintf '%s %s\\n' \"$(basename \"$0\")\" \"$*\" >> " + calls + "\nexit $(cat " + filepath.Join(dir, "exit") + ")\n"
		if err := os.WriteFile(filepath.Join(dir, program), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "exit"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := CheckTestPostgres(context.Background(), filepath.Join(dir, "cluster")); err != nil {
		t.Fatalf("CheckTestPostgres: %v", err)
	}
	ran, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		testPGProgram + " check\n", testPGProgram + " ensure\n",
		"psql -X -q " + TestPGDSN + " -c create database legion_probe_testpg strategy file_copy\n",
		"psql -X -q " + TestPGDSN + " -c drop database legion_probe_testpg with (force)\n",
		testPGProgram + " stop\n",
	} {
		if !strings.Contains(string(ran), want) {
			t.Errorf("the probe ran %q, want a %q among them", ran, want)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "exit"), []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(calls); err != nil {
		t.Fatal(err)
	}
	err = CheckTestPostgres(context.Background(), filepath.Join(dir, "cluster"))
	if err == nil || !strings.Contains(err.Error(), "the pod's own test database") {
		t.Fatalf("CheckTestPostgres on a failing step = %v, want the failure named", err)
	}
	if again, readErr := os.ReadFile(calls); readErr != nil || !strings.Contains(string(again), testPGProgram+" stop\n") {
		t.Errorf("a failed probe ran %q (%v), want the server stopped all the same", again, readErr)
	}
}

// probeStandIn puts first on PATH a `legion-testpg` that handles its processes as the worker
// image's script does, and a `psql` that answers at once, each appending what it ran to the file
// calls. `ensure` runs its start in flock's child, as the script's start-locked runs, so a kill of
// `ensure` alone leaves that start running. The start records itself in the file starters, waits
// the seconds delay gives, and then starts a server: a sleep in a session of its own, as pg_ctl
// starts the postmaster, recorded in the cluster's postmaster.pid and in the file servers. `stop`
// ends the server postmaster.pid names, waits until it has exited, as `pg_ctl stop --wait` does,
// and removes the file, and fails where there is none, as pg_ctl does. A record is a pid and the
// process's start time, so a pid reused since is never signalled; every process a record names is
// killed when the test ends, so a failing run leaves nothing running. It answers the directory
// those files are in.
func probeStandIn(t *testing.T, delay string) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
here='` + dir + `'
printf '%s %s\n' "$(basename "$0")" "$*" >> "$here/calls"
[ "$(basename "$0")" = psql ] && exit 0
cluster=$LEGION_TESTPG_DIR
case $1 in
check) ;;
ensure)
	mkdir -p "$cluster"
	flock -o "$cluster/.lock" sh -c '
		echo "$$ $(cut -d" " -f22 /proc/$$/stat)" >> "$1/starters"
		sleep ` + delay + `
		setsid sleep 300 < /dev/null > /dev/null 2>&1 &
		server="$! $(cut -d" " -f22 /proc/$!/stat)"
		echo "$server" > "$2/postmaster.pid"
		echo "$server" >> "$1/servers"
	' start "$here" "$cluster"
	;;
stop)
	[ -f "$cluster/postmaster.pid" ] || { echo "no server running" >&2; exit 1; }
	read -r pid since < "$cluster/postmaster.pid"
	alive() {
		stat=$(cat "/proc/$pid/stat" 2> /dev/null) || return 1
		[ "$(echo "$stat" | cut -d" " -f22)" = "$since" ] && [ "$(echo "$stat" | cut -d" " -f3)" != Z ]
	}
	if alive; then
		kill "$pid"
		while alive; do sleep 0.05; done
	fi
	rm -f "$cluster/postmaster.pid"
	;;
esac
`
	for _, program := range []string{testPGProgram, "psql"} {
		if err := os.WriteFile(filepath.Join(dir, program), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		for _, record := range []string{"starters", "servers"} {
			for _, pid := range stillRunning(t, filepath.Join(dir, record)) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return dir
}

// stillRunning answers the pids of the processes a record file of probeStandIn's names that still
// run: a process that started when its line says and is no zombie. A missing file names none.
func stillRunning(t *testing.T, file string) []int {
	t.Helper()
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var running []int
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		pidText, since, _ := strings.Cut(line, " ")
		pid, err := strconv.Atoi(pidText)
		if err != nil {
			t.Fatalf("%s holds %q, which names no pid", file, line)
		}
		stat, err := os.ReadFile("/proc/" + pidText + "/stat")
		if err != nil {
			continue
		}
		// The fields after the command's closing parenthesis start at the state, field 3, so the
		// start time, field 22, is the twentieth of them.
		fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
		if len(fields) >= 20 && fields[0] != "Z" && fields[19] == since {
			running = append(running, pid)
		}
	}
	return running
}

// recordLines answers how many records a record file of probeStandIn's holds: a step that never
// started its start, or a server that was never started, holds none.
func recordLines(t *testing.T, file string) int {
	t.Helper()
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

// The probe's steps wait as long as their caller allows, and have no bound of their own: the
// launcher's testPGBound, cut here to half a second, does not reach them, so an `ensure` whose
// server starts a second and a half in, slowly as under gVisor and still working, passes. Its
// server is stopped and nothing of its start is left running.
func TestCheckTestPostgresWaitsAsLongAsItsCallerAllows(t *testing.T) {
	stand := probeStandIn(t, "1.5")
	testPGBound = 500 * time.Millisecond
	t.Cleanup(func() { testPGBound = 60 * time.Second })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := CheckTestPostgres(ctx, filepath.Join(stand, "cluster")); err != nil {
		t.Fatalf("CheckTestPostgres on a server that starts 1.5 s in = %v, want it waited for", err)
	}
	ran, err := os.ReadFile(filepath.Join(stand, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		testPGProgram + " ensure\n",
		"psql -X -q " + TestPGDSN + " -c create database legion_probe_testpg strategy file_copy\n",
		"psql -X -q " + TestPGDSN + " -c drop database legion_probe_testpg with (force)\n",
		testPGProgram + " stop\n",
	} {
		if !strings.Contains(string(ran), want) {
			t.Errorf("the probe ran %q, want a %q among them", ran, want)
		}
	}
	if started := recordLines(t, filepath.Join(stand, "servers")); started != 1 {
		t.Fatalf("the stand-in started %d servers, want the one ensure starts", started)
	}
	for _, record := range []string{"starters", "servers"} {
		if running := stillRunning(t, filepath.Join(stand, record)); len(running) > 0 {
			t.Errorf("after the probe, the %s it recorded still run: %v", record, running)
		}
	}
}

// The control: a step still running when the caller's deadline passes is named and stopped with
// its whole process group. The start flock's child runs ends with `ensure`, so it cannot start a
// server after the probe's stop has found none, and the stop runs all the same.
func TestCheckTestPostgresStopsAStepThatOutlivesItsCallersDeadline(t *testing.T) {
	stand := probeStandIn(t, "300")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	began := time.Now()
	err := CheckTestPostgres(ctx, filepath.Join(stand, "cluster"))
	if err == nil || !errors.Is(err, context.DeadlineExceeded) ||
		!strings.Contains(err.Error(), testPGProgram+" ensure did not finish before the probe's context ended") {
		t.Fatalf("CheckTestPostgres on a start that never answers = %v, want ensure named as having run out the deadline", err)
	}
	if waited := time.Since(began); waited > 20*time.Second {
		t.Errorf("CheckTestPostgres returned %s after a 5 s deadline", waited)
	}
	if started := recordLines(t, filepath.Join(stand, "starters")); started != 1 {
		t.Fatalf("the stand-in's start ran %d times, want once: a run that never reached it proves nothing", started)
	}
	if running := stillRunning(t, filepath.Join(stand, "starters")); len(running) > 0 {
		t.Errorf("the start flock's child ran still runs after the deadline: %v", running)
	}
	if started := recordLines(t, filepath.Join(stand, "servers")); started != 0 {
		t.Errorf("the stand-in started %d servers, want none", started)
	}
	if ran, readErr := os.ReadFile(filepath.Join(stand, "calls")); readErr != nil || !strings.Contains(string(ran), testPGProgram+" stop\n") {
		t.Errorf("a probe past its deadline ran %q (%v), want the server stopped all the same", ran, readErr)
	}
}
