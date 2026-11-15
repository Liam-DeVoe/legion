package daemon

// What the project move tests (dispatch://INFRA-3583 R9, project_move_test.go) read and drive on
// the idle rig: the real `legion` command an operator runs, the outbox rows the daemon's boot
// writes, the wakes its pacing holds, the claims as the operator's listing answers them, and the
// workspace bundles the idle release would have saved.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// goHomeKeys are the homes `go` derives its caches and its configuration from where GOPATH,
// GOMODCACHE, GOCACHE and GOENV are unset, as on a CI runner: the module cache under $HOME/go, the
// build cache under the cache home and the configuration under the config home.
var goHomeKeys = []string{"HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME"}

// goHomes is each of goHomeKeys the test process held as it began, before any test set its own on
// the process, as KEY=value entries. The idle rig sets the home of its workspaces on the process
// (idleRigWorkspaces, fixtureenv.Home), and a `go build` under that home finds a cold module cache
// in the test's temporary directory: it downloads every module there, and the read-only files
// it leaves fail t.TempDir's removal.
var goHomes = func() []string {
	var homes []string
	for _, key := range goHomeKeys {
		if value, ok := os.LookupEnv(key); ok {
			homes = append(homes, key+"="+value)
		}
	}
	return homes
}()

// buildLegion builds this checkout's `legion` into a directory of t's, the command every step of a
// move runs as an operator runs it. It builds with the caches of the process's own homes
// (goHomes), whatever homes the test has set since.
func buildLegion(t *testing.T) string {
	t.Helper()
	module, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "legion")
	started := time.Now()
	build := exec.Command("go", "build", "-o", binary, "./cmd/legion")
	build.Dir = module
	environ := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return slices.Contains(goHomeKeys, key)
	})
	build.Env = slices.Concat(environ, goHomes, []string{"GOFLAGS="})
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build legion from %s: %v\n%s", module, err, out)
	}
	t.Logf("built legion in %s", time.Since(started).Round(time.Millisecond))
	return binary
}

// idleRigCommand is one run of the `legion` command: what it printed, its exit, and the error of a
// command that could not be run at all.
type idleRigCommand struct {
	stdout, stderr string
	code           int
	err            error
}

// legion runs the `legion` binary with args against the rig's daemon, as an operator runs it on the
// daemon's own host: it finds the daemon by --port (daemonAddress) and reads the operator's bearer
// from the file the configuration names.
func (r *idleWakeRig) legion(binary string, args ...string) idleRigCommand {
	r.t.Helper()
	ran := legionRun(binary, args...)
	if ran.err != nil {
		r.t.Fatalf("run legion %s: %v", strings.Join(args, " "), ran.err)
	}
	return ran
}

// legionRun runs the `legion` binary with args and answers what it printed and its exit. It fails
// nothing itself, so a goroutine of its own may run it.
func legionRun(binary string, args ...string) idleRigCommand {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	ran := idleRigCommand{stdout: stdout.String(), stderr: stderr.String()}
	if exit, ok := err.(*exec.ExitError); ok {
		ran.code = exit.ExitCode()
	} else if err != nil {
		ran.err = err
	}
	return ran
}

// operatorFlags are the flags every operator command of the rig takes: the operator's bearer and
// the daemon's port.
func (r *idleWakeRig) operatorFlags() []string {
	return []string{"--operator-token-file", r.cfg.OperatorTokenFile, "--port", strconv.Itoa(r.cfg.Port)}
}

// projectRelease runs `legion project release … --json` with the extra flags given, and answers
// the run and the release it printed.
func (r *idleWakeRig) projectRelease(binary string, flags ...string) (idleRigCommand, api.ProjectReleaseResponse) {
	r.t.Helper()
	args := append(append([]string{"project", "release"}, r.operatorFlags()...), append(flags, "--json")...)
	ran := r.legion(binary, args...)
	var release api.ProjectReleaseResponse
	if err := json.Unmarshal([]byte(ran.stdout), &release); err != nil {
		r.t.Fatalf("legion project release %s exited %d printing %q (stderr %q), which is no release: %v",
			strings.Join(flags, " "), ran.code, ran.stdout, ran.stderr, err)
	}
	return ran, release
}

// claimsByToken is the operator's listing of every claim, by token.
func (r *idleWakeRig) claimsByToken() map[claim.Token]api.OperatorClaim {
	r.t.Helper()
	listed := map[claim.Token]api.OperatorClaim{}
	for _, c := range r.api.claims() {
		listed[c.Token] = c
	}
	return listed
}

// bundleRows is every row of workspace_bundles, one line each in a stable order, and whether the
// table stands at all (bundlesTable false, with no rows, where it does not):
// cybermonklabs/legion#3831 replaces it, and the move test reads it where it still stands.
func (r *idleWakeRig) bundleRows() (rows []string, bundlesTable bool) {
	r.t.Helper()
	ctx := context.Background()
	if err := r.pool.QueryRow(ctx, `select to_regclass('workspace_bundles') is not null`).Scan(&bundlesTable); err != nil {
		r.t.Fatalf("read whether workspace_bundles stands: %v", err)
	}
	if !bundlesTable {
		return nil, false
	}
	listed, err := r.pool.Query(ctx, `select row_to_json(b)::text from workspace_bundles b`)
	if err != nil {
		r.t.Fatalf("read workspace_bundles: %v", err)
	}
	defer listed.Close()
	for listed.Next() {
		var row string
		if err := listed.Scan(&row); err != nil {
			r.t.Fatalf("read a row of workspace_bundles: %v", err)
		}
		rows = append(rows, row)
	}
	if err := listed.Err(); err != nil {
		r.t.Fatalf("read workspace_bundles: %v", err)
	}
	slices.Sort(rows)
	return rows, true
}

// holdsRunningCopies is whether the migrated database holds cybermonklabs/legion#3831's table of
// each issue pod's running copy, which decides how the move test reads a deletion's lost work and
// the bundle requests the role processes get, so whichever of that change and this one lands
// second changes no test.
func (r *idleWakeRig) holdsRunningCopies() bool {
	r.t.Helper()
	var held bool
	if err := r.pool.QueryRow(context.Background(), `select to_regclass('workspace_running_copies') is not null`).Scan(&held); err != nil {
		r.t.Fatalf("read whether workspace_running_copies stands: %v", err)
	}
	return held
}

// runningCopyAt is when the running copy cybermonklabs/legion#3831 keeps of issue's workspace was
// taken (workspace_running_copies.at, the store's clock as the copy began), and whether one is
// kept. A copy holds the workspace as it stood then, so the work a pod restored from it misses is
// the work left after that moment. Only a database that holds the table (holdsRunningCopies) is
// asked.
func (r *idleWakeRig) runningCopyAt(issue string) (time.Time, bool) {
	r.t.Helper()
	var at time.Time
	err := r.pool.QueryRow(context.Background(), `select at from workspace_running_copies where issue = $1`, issue).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false
	}
	if err != nil {
		r.t.Fatalf("read the running copy of %s: %v", issue, err)
	}
	return at, true
}

// runningCopyLines is how many lines cybermonklabs/legion#3831's copy of a running workspace has
// logged, each beginning "running copy:" (its workspace_copies.go). That copy asks a shim over the
// same bundle request the idle release sends (shimwire.BundleWorkspace), which names no caller,
// and every copy that asks ends on one such line whatever its end, so a bundle request beyond
// those lines was sent by another caller of the daemon.
func (r *idleWakeRig) runningCopyLines() int {
	count := 0
	for _, line := range strings.Split(r.logs.String(), "\n") {
		var entry struct {
			Msg string `json:"msg"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && strings.HasPrefix(entry.Msg, "running copy:") {
			count++
		}
	}
	return count
}

// idleRigStart is one `start` row of the outbox as the rig's database recorded it when the row was
// written: its id, the issue it is filed under, and what it asks.
type idleRigStart struct {
	id      int64
	issue   string
	request record.SuperviseRequest
}

// idleRigStartsTable is the table of the test's own into which the rig's database copies every
// supervise start row written into the outbox (watchStarts).
const idleRigStartsTable = "idle_rig_outbox_starts"

// startWatcher reads every `start` row written into the outbox since it began. The outbox deletes
// a row once its start ran, which at a boot is a few milliseconds after the row's write, so a
// reader of the outbox itself misses rows however often it reads: the rig's database copies each
// row into idleRigStartsTable in the transaction that writes it, and the watcher reads that copy.
type startWatcher struct {
	pool *store.Pool
	// floor is the newest start recorded when the watch began: a row at or under it was written
	// before, by whatever the test did earlier, and is not counted.
	floor int64
}

// watchStarts begins reading the outbox's start rows written from now on. A rig's first watch
// makes the table and the trigger that fills it, in one transaction, so a later watch finds both
// or neither. The database is the rig's own clone (isolatedOutboxPool), dropped when the test
// ends, so no other test sees either.
func (r *idleWakeRig) watchStarts() *startWatcher {
	r.t.Helper()
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var made bool
		if err := tx.QueryRow(ctx, `select to_regclass($1) is not null`, idleRigStartsTable).Scan(&made); err != nil || made {
			return err
		}
		for _, statement := range []string{
			`create table ` + idleRigStartsTable + ` (id bigint not null, issue text not null, payload jsonb not null)`,
			`create function idle_rig_record_start() returns trigger language plpgsql as $$
				begin insert into ` + idleRigStartsTable + ` (id, issue, payload) values (new.id, new.issue, new.payload); return null; end $$`,
			`create trigger idle_rig_record_start after insert on outbox for each row
				when (new.kind = 'supervise' and new.payload->>'op' = 'start') execute function idle_rig_record_start()`,
		} {
			if _, err := tx.Exec(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		r.t.Fatalf("make the record of the outbox's start rows: %v", err)
	}
	w := &startWatcher{pool: r.pool}
	if err := r.pool.QueryRow(ctx, `select coalesce(max(id), 0) from `+idleRigStartsTable).Scan(&w.floor); err != nil {
		r.t.Fatalf("read the newest start row recorded: %v", err)
	}
	return w
}

// seen is every start written since the watch began, by the issue and role each starts, in id
// order.
func (w *startWatcher) seen() (map[string][]idleRigStart, error) {
	rows, err := w.pool.Query(context.Background(),
		`select id, issue, payload::text from `+idleRigStartsTable+` where id > $1 order by id`, w.floor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byRole := map[string][]idleRigStart{}
	for rows.Next() {
		var start idleRigStart
		var payload string
		if err := rows.Scan(&start.id, &start.issue, &payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &start.request); err != nil {
			return nil, fmt.Errorf("decode start row %d: %w", start.id, err)
		}
		key := idleRigKey(start.issue, string(start.request.Role))
		byRole[key] = append(byRole[key], start)
	}
	return byRole, rows.Err()
}
