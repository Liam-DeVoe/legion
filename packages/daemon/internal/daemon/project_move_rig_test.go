package daemon

// What the project move tests (dispatch://INFRA-3583 R9, project_move_test.go) read and drive on
// the idle rig: the real `legion` command an operator runs, the outbox rows the daemon's boot
// writes, the wakes its pacing holds, the claims as the operator's listing answers them, and the
// workspace bundles the idle release would have saved.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// buildLegion builds this checkout's `legion` into a directory of t's, the command every step of a
// move runs as an operator runs it.
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
	build.Env = append(os.Environ(), "GOFLAGS=")
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

// podUIDOf is the uid of the pod the claim's process runs in, "" while it records none.
func podUIDOf(c api.OperatorClaim) string {
	if c.Locator == nil || c.Locator.Sandbox == nil {
		return ""
	}
	return c.Locator.Sandbox.PodUID
}

// wakesWaiting is the state's `wakesWaiting`: every wake the daemon's pacing holds.
func (r *idleWakeRig) wakesWaiting() []api.WakeWaiting {
	r.t.Helper()
	status, body := r.api.request(http.MethodGet, "/legion/v1/state", nil, false)
	if status != http.StatusOK {
		r.t.Fatalf("GET /legion/v1/state = %d; body %s", status, body)
	}
	var state struct {
		WakesWaiting []api.WakeWaiting `json:"wakesWaiting"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		r.t.Fatalf("decode the state's wakesWaiting: %v", err)
	}
	return state.WakesWaiting
}

// bundleRows is every row of workspace_bundles, one line each in a stable order, or nil with
// held false where the migrated database holds no such table: cybermonklabs/legion#3831 replaces
// it, and the move test reads it where it still stands.
func (r *idleWakeRig) bundleRows() (rows []string, held bool) {
	r.t.Helper()
	ctx := context.Background()
	if err := r.pool.QueryRow(ctx, `select to_regclass('workspace_bundles') is not null`).Scan(&held); err != nil {
		r.t.Fatalf("read whether workspace_bundles stands: %v", err)
	}
	if !held {
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
// each issue pod's running copy, which decides how the move test reads a deletion's lost work (the
// "Answered by the tree's architect" section of the plan): whichever of the two lands second then
// changes no test.
func (r *idleWakeRig) holdsRunningCopies() bool {
	r.t.Helper()
	var held bool
	if err := r.pool.QueryRow(context.Background(), `select to_regclass('workspace_running_copies') is not null`).Scan(&held); err != nil {
		r.t.Fatalf("read whether workspace_running_copies stands: %v", err)
	}
	return held
}

// idleRigStart is one `start` row of the outbox as a watcher saw it: its id, the issue it is
// filed under, and what it asks.
type idleRigStart struct {
	id      int64
	issue   string
	request record.SuperviseRequest
	seen    time.Time
}

// startWatcher records every `start` row the outbox holds while it runs, polling the table: a
// row is deleted once its start ran, so a test that reads the starts a boot wrote reads them as
// they stand, from before that boot until the starts it waits for have run.
type startWatcher struct {
	// floor is the newest outbox row when the watch began: a row at or under it was written
	// before, by whatever the test did earlier, and is not counted.
	floor  int64
	mu     sync.Mutex
	starts map[int64]idleRigStart
	stop   chan struct{}
	done   chan struct{}
	err    error
}

// watchStarts begins recording the outbox's start rows written from now on; end stops it.
func (r *idleWakeRig) watchStarts() *startWatcher {
	r.t.Helper()
	w := &startWatcher{starts: map[int64]idleRigStart{}, stop: make(chan struct{}), done: make(chan struct{})}
	if err := r.pool.QueryRow(context.Background(), `select coalesce(max(id), 0) from outbox`).Scan(&w.floor); err != nil {
		r.t.Fatalf("read the outbox's newest row: %v", err)
	}
	go func() {
		defer close(w.done)
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			if err := w.read(r); err != nil {
				w.mu.Lock()
				w.err = err
				w.mu.Unlock()
				return
			}
			select {
			case <-w.stop:
				_ = w.read(r)
				return
			case <-tick.C:
			}
		}
	}()
	r.owner().Cleanup(func() { w.end() })
	return w
}

func (w *startWatcher) read(r *idleWakeRig) error {
	rows, err := r.pool.Query(context.Background(),
		`select id, issue, payload::text from outbox where kind = 'supervise' and payload->>'op' = 'start' and id > $1`, w.floor)
	if err != nil {
		return err
	}
	defer rows.Close()
	now := time.Now()
	for rows.Next() {
		var start idleRigStart
		var payload string
		if err := rows.Scan(&start.id, &start.issue, &payload); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(payload), &start.request); err != nil {
			return fmt.Errorf("decode start row %d: %w", start.id, err)
		}
		w.mu.Lock()
		if _, seen := w.starts[start.id]; !seen {
			start.seen = now
			w.starts[start.id] = start
		}
		w.mu.Unlock()
	}
	return rows.Err()
}

// end stops the watcher once, and waits for it.
func (w *startWatcher) end() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
	<-w.done
}

// seen is every start the watcher recorded, by the issue and role each starts, in id order.
func (w *startWatcher) seen() (map[string][]idleRigStart, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	byRole := map[string][]idleRigStart{}
	for _, start := range w.starts {
		key := start.issue + "/" + string(start.request.Role)
		byRole[key] = append(byRole[key], start)
	}
	for _, starts := range byRole {
		slices.SortFunc(starts, func(a, b idleRigStart) int { return int(a.id - b.id) })
	}
	return byRole, w.err
}
