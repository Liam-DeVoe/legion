package launcher

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/testtmp"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// The free bytes and the size a case's disk check reads (testPGDiskSpace): a disk of INFRA's
// 20 GiB, with 512 MiB free under the floor and the floor itself at it.
const (
	pgDiskSize  = 20 << 30
	pgDiskUnder = 512 << 20
)

// pgDiskWait bounds a wait on one reset, whose own bound is testPGResetBound: a reset that waits
// on the lock or on a process in the cluster waits once more for its stop, its initdb and its
// start after that wait ends.
const pgDiskWait = testPGResetBound

// pgDiskRig is one test's own PostgreSQL, served by the worker image's real legion-testpg from a
// short directory of the test's own (testtmp: the server binds its socket there) on a free
// loopback port of its own. testPGServedDSN points the launcher at that port for the test's life,
// so neither the launcher nor the script ever reaches the machine's shared server.
type pgDiskRig struct {
	cfg       Config
	dir       string // the cluster's directory, testPGDir(cfg.RolesDir)
	dsn       string
	port      int
	superuser string
	bin       string // the real server bin directory the script would pick
	log       *lockedBuffer
}

// newPGDiskRig serves the test's own cluster with the script's `ensure`, as the launcher's first
// attempt does, and stops it when the test ends. It skips the test, in one line naming what is
// missing, where the machine has no server for the script to run.
func newPGDiskRig(t *testing.T) *pgDiskRig {
	t.Helper()
	path := t.TempDir()
	if err := os.Symlink(pgDiskScript(t), filepath.Join(path, testPGProgram)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", path+string(os.PathListSeparator)+os.Getenv("PATH"))
	var missing []string
	for _, program := range []string{"psql", "pg_isready", "pg_config", "flock"} {
		if _, err := exec.LookPath(program); err != nil {
			missing = append(missing, program)
		}
	}
	if len(missing) > 0 {
		t.Skipf("TestPGDisk needs %s on PATH, for the real %s", strings.Join(missing, ", "), testPGProgram)
	}
	if out, err := exec.Command(testPGProgram, "check").CombinedOutput(); err != nil {
		t.Skipf("TestPGDisk needs a PostgreSQL server under /usr/lib/postgresql/*/bin (or PGBIN) with pgvector: %s check: %v: %s",
			testPGProgram, err, strings.Join(strings.Fields(string(out)), " "))
	}
	superuser, err := exec.Command("id", "-un").Output()
	if err != nil {
		t.Fatalf("id -un: %v", err)
	}
	root := testtmp.Dir(t, filepath.Join("roles", TestPGDirName, "sockets", ".s.PGSQL.65535.lock"))
	r := &pgDiskRig{
		cfg:       Config{RolesDir: filepath.Join(root, "roles"), TestPostgres: true, TestPostgresDisk: true},
		port:      pgDiskFreePort(t),
		superuser: strings.TrimSpace(string(superuser)),
		bin:       pgDiskServerBin(t),
		log:       &lockedBuffer{},
	}
	r.dir = testPGDir(r.cfg.RolesDir)
	r.dsn = fmt.Sprintf("postgres://legion_test@127.0.0.1:%d/legion_test?sslmode=disable", r.port)
	testPGServedDSN = r.dsn
	t.Cleanup(func() { testPGServedDSN = TestPGDSN })
	t.Cleanup(func() { r.stop(t) })
	if err := makeTestPGLog(r.cfg.RolesDir); err != nil {
		t.Fatal(err)
	}
	if err := runTestPG(context.Background(), r.cfg, r.roleLog(), &pidGuard{}, "ensure", testPGBound); err != nil {
		t.Fatalf("the first ensure: %v\n%s", err, r.log)
	}
	return r
}

// pgDiskScript is the worker image's legion-testpg in this repository, found from the test's
// working directory up.
func pgDiskScript(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		script := filepath.Join(dir, "packages", "daemon", "docker", testPGProgram)
		if _, err := os.Stat(script); err == nil {
			return script
		}
		if filepath.Dir(dir) == dir {
			t.Fatalf("no packages/daemon/docker/%s in %s or any directory above it", testPGProgram, wd)
		}
	}
}

// pgDiskServerBin is the server bin directory the script picks: PGBIN, or the newest
// /usr/lib/postgresql/<major>/bin that holds a postgres.
func pgDiskServerBin(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("PGBIN"); bin != "" {
		return bin
	}
	dirs, _ := filepath.Glob("/usr/lib/postgresql/*/bin")
	newest, major := "", -1
	for _, dir := range dirs {
		version, err := strconv.Atoi(filepath.Base(filepath.Dir(dir)))
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "postgres")); err == nil && version > major {
			newest, major = dir, version
		}
	}
	if newest == "" {
		t.Fatal("legion-testpg check passed, yet no /usr/lib/postgresql/*/bin holds a postgres")
	}
	return newest
}

// pgDiskFreePort is a loopback port nothing listens on as the test starts.
func pgDiskFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// stop ends the test's own server wherever it runs, its data directory or a cluster set aside, so
// nothing of the test outlives it. An immediate stop is enough for a cluster deleted next.
func (r *pgDiskRig) stop(t *testing.T) {
	for _, data := range []string{filepath.Join(r.dir, "data"), filepath.Join(r.dir, testPGSetAsideName)} {
		if _, err := os.Stat(filepath.Join(data, "postmaster.pid")); err != nil {
			continue
		}
		out, err := exec.Command(filepath.Join(r.bin, "pg_ctl"), "--pgdata", data, "--mode", "immediate", "--wait", "--timeout", "60", "stop").CombinedOutput()
		if err != nil {
			t.Logf("stopping the test's own server in %s: %v: %s", data, err, out)
		}
	}
}

func (r *pgDiskRig) roleLog() *roleLog { return &roleLog{stdout: r.log, stderr: r.log} }

// pgDiskSpace is a disk reading of free bytes on a disk of pgDiskSize.
func pgDiskSpace(free uint64) func(string) (uint64, uint64, error) {
	return func(string) (uint64, uint64, error) { return free, pgDiskSize, nil }
}

// check runs the launcher's own disk check, the one its loop runs, with the disk reading free.
func (r *pgDiskRig) check(free uint64) {
	checkTestPGDisk(context.Background(), r.cfg, r.roleLog(), &pidGuard{}, pgDiskSpace(free))
}

// checkInBackground runs check in a goroutine of its own and answers a channel closed when it
// returns. The test waits for it before the rig stops the server.
func (r *pgDiskRig) checkInBackground(t *testing.T, free uint64) <-chan struct{} {
	checked := make(chan struct{})
	go func() {
		defer close(checked)
		r.check(free)
	}()
	t.Cleanup(func() { <-checked })
	return checked
}

// script runs one command of the script by hand, with the environment the launcher gives it.
func (r *pgDiskRig) script(t *testing.T, command string) {
	t.Helper()
	cmd := exec.Command(testPGProgram, command)
	cmd.Env = append(os.Environ(),
		TestPGVariable+"="+r.dsn,
		testPGDirVariable+"="+r.dir,
		testPGLogVariable+"="+testPGLog(r.cfg.RolesDir),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v: %s", testPGProgram, command, err, out)
	}
}

// dsnSQL runs sql through the DSN, as every role's tests reach the server.
func (r *pgDiskRig) dsnSQL(sql string) (string, error) {
	cmd := exec.Command("psql", "-X", "-w", "-tAq", r.dsn, "-c", sql)
	cmd.Env = append(os.Environ(), "PGCONNECT_TIMEOUT=10")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("psql %s -c %q: %w: %s", r.dsn, sql, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// superSQL runs sql as the cluster's superuser over its socket, which the DSN's login running
// out of connections leaves open.
func (r *pgDiskRig) superSQL(sql string) (string, error) {
	cmd := exec.Command("psql", "-X", "-w", "-tAq", "-h", filepath.Join(r.dir, "sockets"), "-p", strconv.Itoa(r.port),
		"-U", r.superuser, "-d", "postgres", "-c", sql)
	cmd.Env = append(os.Environ(), "PGCONNECT_TIMEOUT=10")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("psql as %s -c %q: %w: %s", r.superuser, sql, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// madeSQL lists the databases a case made, every one named tpg_…, by name.
const madeSQL = `select coalesce(string_agg(datname, ' ' order by datname), '') from pg_database where datname like 'tpg\_%'`

// makeDatabases makes each database through the DSN, as a test harness does.
func (r *pgDiskRig) makeDatabases(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := r.dsnSQL("create database " + name + " strategy file_copy"); err != nil {
			t.Fatal(err)
		}
	}
}

// made is the databases a case made that the cluster holds, read through the DSN.
func (r *pgDiskRig) made(t *testing.T) []string {
	t.Helper()
	out, err := r.dsnSQL(madeSQL)
	if err != nil {
		t.Fatalf("the DSN does not answer: %v\n%s", err, r.log)
	}
	return strings.Fields(out)
}

// answers makes and drops a database through the DSN, as a test that reaches the server does.
func (r *pgDiskRig) answers(t *testing.T) {
	t.Helper()
	for _, sql := range []string{"create database tpg_after strategy file_copy", "drop database tpg_after with (force)"} {
		if _, err := r.dsnSQL(sql); err != nil {
			t.Fatalf("the DSN does not serve a database: %v\n%s", err, r.log)
		}
	}
}

// listening is whether a server accepts connections on the test's port.
func (r *pgDiskRig) listening() bool {
	return exec.Command("pg_isready", "-q", "-h", "127.0.0.1", "-p", strconv.Itoa(r.port)).Run() == nil
}

// resetLines are the reset lines in the container's log, behind the launcher's prefix.
func (r *pgDiskRig) resetLines() []string {
	var lines []string
	for _, line := range strings.Split(r.log.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, testPGProgram+": "); ok && strings.HasPrefix(rest, "legion-testpg: reset") {
			lines = append(lines, rest)
		}
	}
	return lines
}

// pgDiskExists is whether path is there.
func pgDiskExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// assertReset holds what a reset leaves: none of the databases the case made, the DSN serving a
// database made and dropped through it, nothing set aside, and one reset line, in the container's
// log and in the server's log after the new cluster's own lines. It answers that line.
func (r *pgDiskRig) assertReset(t *testing.T) string {
	t.Helper()
	if made := r.made(t); len(made) != 0 {
		t.Errorf("after the reset the cluster still holds %v, want none of the databases made before it", made)
	}
	r.answers(t)
	if aside := filepath.Join(r.dir, testPGSetAsideName); pgDiskExists(t, aside) {
		t.Errorf("after the reset %s is still there, want the old cluster deleted", aside)
	}
	lines := r.resetLines()
	if len(lines) != 1 {
		t.Fatalf("the container's log holds %d reset lines, want one:\n%s", len(lines), r.log)
	}
	server, err := os.ReadFile(testPGLog(r.cfg.RolesDir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(server), lines[0]+"\n") {
		t.Errorf("the server's log holds no line %q:\n%s", lines[0], server)
	}
	if !strings.Contains(lines[0], "512.0 MiB free of 20.0 GiB") || !strings.Contains(lines[0], "floor of 2.0 GiB") {
		t.Errorf("the reset line %q does not give the disk's use, 512.0 MiB free of 20.0 GiB, and the 2.0 GiB floor", lines[0])
	}
	t.Logf("the reset's line: %s", lines[0])
	return lines[0]
}

// With fewer than 2 GiB free on the cluster's own disk, the check resets a cluster whose server
// answers: the databases a harness made and never dropped are gone, the DSN serves a new empty
// cluster, and the reset's line names every database the old cluster held, with its owner and
// its size, so a role reads which harness filled the disk.
func TestPGDiskUnderTheFloorResetsAServerThatAnswersAndNamesWhatItHeld(t *testing.T) {
	r := newPGDiskRig(t)
	r.makeDatabases(t, "tpg_one", "tpg_two")
	r.check(pgDiskUnder)
	line := r.assertReset(t)
	for _, name := range []string{"tpg_one", "tpg_two", "legion_test"} {
		held := regexp.MustCompile(`\b` + name + ` \(owner legion_test, [0-9]+ (bytes|kB|MB|GB|TB)\)`)
		if !held.MatchString(line) {
			t.Errorf("the reset line %q does not name %s with its owner, legion_test, and its size", line, name)
		}
	}
}

// A server that does not answer is reset all the same, since waiting on it would let a full disk
// crash it again and again, and the line says it read no databases.
func TestPGDiskUnderTheFloorResetsAServerThatDoesNotAnswer(t *testing.T) {
	r := newPGDiskRig(t)
	r.makeDatabases(t, "tpg_one", "tpg_two")
	r.script(t, "stop")
	r.check(pgDiskUnder)
	line := r.assertReset(t)
	if !strings.Contains(line, "read no databases") || strings.Contains(line, "tpg_one") {
		t.Errorf("the reset line %q, of a server that did not answer, does not say it read no databases", line)
	}
}

// At 2 GiB free or more the check is the plain `ensure` of today: it resets nothing, whether the
// server answers, is down (and is started again) or refuses the DSN's login for want of
// connections, which the check may log.
func TestPGDiskAtOrOverTheFloorChangesNothing(t *testing.T) {
	r := newPGDiskRig(t)
	r.makeDatabases(t, "tpg_one", "tpg_two")
	want := []string{"tpg_one", "tpg_two"}
	kept := func(t *testing.T, made []string) {
		t.Helper()
		if !slices.Equal(made, want) {
			t.Errorf("after the check the cluster holds %v, want %v kept", made, want)
		}
		if lines := r.resetLines(); len(lines) != 0 {
			t.Errorf("over the floor the check reset the cluster: %q", lines)
		}
		if aside := filepath.Join(r.dir, testPGSetAsideName); pgDiskExists(t, aside) {
			t.Errorf("over the floor the check set the cluster aside at %s", aside)
		}
	}
	t.Run("up", func(t *testing.T) {
		r.check(testPGResetFloor)
		kept(t, r.made(t))
	})
	t.Run("down", func(t *testing.T) {
		r.script(t, "stop")
		r.check(pgDiskSize)
		kept(t, r.made(t))
	})
	t.Run("out of connections", func(t *testing.T) {
		if _, err := r.superSQL("alter role legion_test connection limit 0"); err != nil {
			t.Fatal(err)
		}
		r.check(pgDiskSize)
		out, err := r.superSQL(madeSQL)
		if err != nil {
			t.Fatal(err)
		}
		kept(t, strings.Fields(out))
		if _, err := r.superSQL("alter role legion_test connection limit -1"); err != nil {
			t.Fatal(err)
		}
		kept(t, r.made(t))
	})
}

// pgDiskHolder starts cmd in a process group of its own and answers a release that kills the
// group and waits for it, once, however often it is called.
func pgDiskHolder(t *testing.T, cmd *exec.Cmd) (release func()) {
	t.Helper()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		})
	}
}

// A reset takes the lock a start takes, so it never runs beside one: while a start holds the
// lock, the reset waits and the databases made before it are still there, served through the DSN,
// and once the lock is released the reset runs.
func TestPGDiskAResetWaitsForTheStartLock(t *testing.T) {
	r := newPGDiskRig(t)
	r.makeDatabases(t, "tpg_one", "tpg_two")
	lock := filepath.Join(r.dir, ".lock")
	release := pgDiskHolder(t, exec.Command("flock", "--close", lock, "sleep", "600"))
	defer release()
	testwait.Eventually(t, "the start lock to be held", func() bool {
		var exit *exec.ExitError
		err := exec.Command("flock", "--nonblock", "--conflict-exit-code", "99", lock, "true").Run()
		return errors.As(err, &exit) && exit.ExitCode() == 99
	})
	checked := r.checkInBackground(t, pgDiskUnder)
	var changed string
	if testwait.Holds(5*time.Second, 250*time.Millisecond, func() bool {
		select {
		case <-checked:
			changed = "the check returned"
			return true
		default:
		}
		out, err := r.dsnSQL(madeSQL)
		if err != nil {
			changed = err.Error()
			return true
		}
		if made := strings.Fields(out); !slices.Equal(made, []string{"tpg_one", "tpg_two"}) {
			changed = fmt.Sprintf("the cluster holds %v", made)
			return true
		}
		return false
	}) {
		t.Fatalf("while a start held the lock the reset went ahead: %s\n%s", changed, r.log)
	}
	release()
	select {
	case <-checked:
	case <-time.After(pgDiskWait):
		t.Fatalf("the reset had not finished %s after the lock was released\n%s", pgDiskWait, r.log)
	}
	r.assertReset(t)
}

// A reset moves nothing a process still holds: with a process whose working directory is the
// data directory, as a backend's is, the reset stops the server and then waits, and the cluster
// stays where it is, whole, until that process is gone; then the reset runs.
func TestPGDiskAResetMovesNothingAProcessStillHolds(t *testing.T) {
	r := newPGDiskRig(t)
	r.makeDatabases(t, "tpg_one", "tpg_two")
	data := filepath.Join(r.dir, "data")
	holder := exec.Command("sleep", "600")
	holder.Dir = data
	release := pgDiskHolder(t, holder)
	defer release()
	checked := r.checkInBackground(t, pgDiskUnder)
	testwait.WithinPolling(t, 2*time.Minute, 100*time.Millisecond, "the reset to stop the server", func() bool { return !r.listening() })
	aside := filepath.Join(r.dir, testPGSetAsideName)
	var moved string
	if testwait.Holds(5*time.Second, 250*time.Millisecond, func() bool {
		select {
		case <-checked:
			moved = "the check returned"
			return true
		default:
		}
		switch {
		case !pgDiskExists(t, filepath.Join(data, "PG_VERSION")):
			moved = data + " holds no PG_VERSION"
		case pgDiskExists(t, aside):
			moved = aside + " is there"
		default:
			return false
		}
		return true
	}) {
		t.Fatalf("with a process in %s the reset moved the cluster: %s\n%s", data, moved, r.log)
	}
	release()
	select {
	case <-checked:
	case <-time.After(pgDiskWait):
		t.Fatalf("the reset had not finished %s after the process left the data directory\n%s", pgDiskWait, r.log)
	}
	r.assertReset(t)
}

// A reset cut right after its rename, before its initdb — by its bound, a restart of the roles
// container or a kill — leaves the old cluster set aside whole and no cluster in the data
// directory, and the next check, over the floor or under it, deletes what was set aside and
// serves an empty cluster on the DSN. The cut is an initdb that kills the reset's own shell.
func TestPGDiskACheckAfterAResetCutAtItsRenameDeletesTheOldClusterAndServesAnEmptyOne(t *testing.T) {
	r := newPGDiskRig(t)
	r.makeDatabases(t, "tpg_one", "tpg_two")
	cut := t.TempDir()
	programs, err := os.ReadDir(r.bin)
	if err != nil {
		t.Fatal(err)
	}
	for _, program := range programs {
		if program.Name() != "initdb" {
			if err := os.Symlink(filepath.Join(r.bin, program.Name()), filepath.Join(cut, program.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	initdb := "#!/bin/sh\n# The reset's own shell runs initdb: killing it here cuts the reset right after its rename.\nkill -KILL \"$PPID\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(cut, "initdb"), []byte(initdb), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGBIN", cut)
	r.check(pgDiskUnder)
	aside := filepath.Join(r.dir, testPGSetAsideName)
	if !pgDiskExists(t, filepath.Join(aside, "PG_VERSION")) || !pgDiskExists(t, filepath.Join(aside, "base")) {
		t.Fatalf("the cut reset left no whole cluster at %s\n%s", aside, r.log)
	}
	if data := filepath.Join(r.dir, "data"); pgDiskExists(t, filepath.Join(data, "PG_VERSION")) {
		t.Fatalf("the cut reset left a cluster in %s\n%s", data, r.log)
	}
	if lines := r.resetLines(); len(lines) != 0 {
		t.Fatalf("the cut reset wrote its line %q", lines)
	}
	t.Setenv("PGBIN", r.bin)
	r.check(pgDiskSize)
	if said := r.log.String(); !strings.Contains(said, "launcher: test database: deleted "+aside+",") {
		t.Errorf("the check that followed the cut does not say it deleted %s:\n%s", aside, said)
	}
	if pgDiskExists(t, aside) {
		t.Errorf("the check that followed the cut left %s", aside)
	}
	if made := r.made(t); len(made) != 0 {
		t.Errorf("after the cut and a check the cluster holds %v, want an empty one", made)
	}
	r.answers(t)
}

// stubPGDisk puts a `legion-testpg` on PATH that appends one line per call — the subcommand and
// the free bytes, the size and the floor it was told, or `unset` — to a file, and answers 0.
func stubPGDisk(t *testing.T) (calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s %s %s %s\\n' \"$1\" \"${" + testPGFreeVariable + "-unset}\" \"${" + testPGSizeVariable + "-unset}\" \"${" +
		testPGFloorVariable + "-unset}\" >> " + calls + "\n"
	if err := os.WriteFile(filepath.Join(dir, testPGProgram), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// callLines are the whole lines of the stub's calls so far.
func callLines(calls string) []string {
	got, _ := os.ReadFile(calls)
	lines := strings.SplitAfter(string(got), "\n")
	return slices.DeleteFunc(lines, func(line string) bool { return !strings.HasSuffix(line, "\n") })
}

// Where the cluster is on a disk of its own, the launcher's first attempt is still the plain
// `ensure` its roles wait on, and the loop then runs the disk check at once and at every tick:
// under the floor each one resets, told the free bytes, the disk's size and the floor, and the
// first deletes a cluster a cut reset set aside.
func TestPGDiskTheLoopChecksTheDiskAtOnceAndAtEveryTick(t *testing.T) {
	calls := stubPGDisk(t)
	testPGInterval = 20 * time.Millisecond
	t.Cleanup(func() { testPGInterval = 15 * time.Second })
	space := testPGDiskSpace
	testPGDiskSpace = pgDiskSpace(1 << 30)
	t.Cleanup(func() { testPGDiskSpace = space })
	rolesDir := t.TempDir()
	aside := filepath.Join(testPGDir(rolesDir), testPGSetAsideName)
	if err := os.MkdirAll(filepath.Join(aside, "base"), 0o700); err != nil {
		t.Fatal(err)
	}
	log := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	// The ticker goroutine is waited for before the cleanups above restore the interval and the
	// disk's reader, so no read of either outlives this test.
	done := serveTestPG(ctx, Config{RolesDir: rolesDir, TestPostgres: true, TestPostgresDisk: true}, &roleLog{stdout: log, stderr: log}, &pidGuard{})
	t.Cleanup(func() { cancel(); <-done })
	if first := callLines(calls); len(first) == 0 || first[0] != "ensure unset unset unset\n" {
		t.Fatalf("serveTestPG returned with the calls %q, want the plain ensure first", first)
	}
	reset := fmt.Sprintf("reset %d %d %d\n", 1<<30, pgDiskSize, testPGResetFloor)
	testwait.Eventually(t, "two resets", func() bool { return len(callLines(calls)) >= 3 })
	if later := callLines(calls)[1:]; slices.ContainsFunc(later, func(line string) bool { return line != reset }) {
		t.Errorf("the loop's calls are %q, want every one %q", later, reset)
	}
	if pgDiskExists(t, aside) {
		t.Errorf("the loop's first check left %s", aside)
	}
	if said := log.String(); !strings.Contains(said, "launcher: test database: deleted "+aside+",") {
		t.Errorf("the container log holds %q, want the deletion of %s named", said, aside)
	}
}

// A disk the check cannot read is logged, and that check runs the plain `ensure`: a reading that
// fails never resets a cluster, and never leaves a dead server unstarted.
func TestPGDiskACheckThatCannotReadTheDiskLogsAndEnsures(t *testing.T) {
	calls := stubPGDisk(t)
	rolesDir := t.TempDir()
	if err := makeTestPGLog(rolesDir); err != nil {
		t.Fatal(err)
	}
	log := &lockedBuffer{}
	unread := func(string) (uint64, uint64, error) { return 0, 0, syscall.EIO }
	checkTestPGDisk(context.Background(), Config{RolesDir: rolesDir, TestPostgres: true, TestPostgresDisk: true}, &roleLog{stdout: log, stderr: log}, &pidGuard{}, unread)
	if got := callLines(calls); !slices.Equal(got, []string{"ensure unset unset unset\n"}) {
		t.Errorf("the check ran %q, want the plain ensure alone", got)
	}
	if said := log.String(); !strings.Contains(said, "launcher: test database: read the bytes free") || !strings.Contains(said, syscall.EIO.Error()) {
		t.Errorf("the container log holds %q, want the failed reading named", said)
	}
}
