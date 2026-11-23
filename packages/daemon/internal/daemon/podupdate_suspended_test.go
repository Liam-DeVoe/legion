package daemon

// A claim the daemon suspended holds no pod update round (dispatch://INFRA-4109 R1, R3,
// verification's c-suspended-asked). Such a claim, suspended by the daemon's own rule, by a
// root's exit, by an idle release, by the work hold or by an unknown source (supervise's
// Claim.PassedOverByRound), runs no process and no turn in the pod. So the round does not ask
// it, wait for it, wake it or resume it. What it left unpushed goes with the pod, unless the
// pod's disk is a kept volume that the issue's next pod mounts (roundKeptVolume). Once every
// claim the round asks has answered and no turn runs, the replacement reads the issue's
// workspace through a role whose process runs in the pod, names on its line each claim it did
// not wait for and what the read found, and, where the read found work or failed, writes one
// message on the issue. An operator's suspension still holds the round past its bound
// (dispatch://INFRA-2981 R5).
//
// These cases drive the pod-update rig of podupdate_test.go. The suspended claims are seeded in
// the store and restored as a boot restores them (newPodUpdateRig's sup.restore).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

const (
	// passedOverHead is the working-copy commit every read these tests script answers.
	passedOverHead = "3f9a0c4e1b7d52a8c6e0f1d2b3a4c5d6e7f80912"
	// passedOverCommit is the one commit that no branch or tag on origin holds, where a read
	// finds one.
	passedOverCommit = "8c2e4a6b0d1f3e5a7c9b1d3f5a7c9e1b3d5f7a9c"
)

// errPassedOverRead is a read of the workspace that fails, as the Sandbox runtime answers a read
// the role's shim refused.
var errPassedOverRead = errors.New("the shim refused workspace-work: jj log: the working copy is stale")

var (
	// passedOverWorkers are the two ready workers of R3's round, each with a process in the pod.
	passedOverWorkers = []claim.Role{claim.RoleImplementer, claim.RoleTester}
	// passedOverSuspended are its two claims the daemon suspended: the planner by the daemon's
	// own rule and the reviewer by an idle release (replacedOverPassedOverClaims).
	passedOverSuspended = []claim.Role{claim.RolePlanner, claim.RoleReviewer}
)

// suspendedRole is a claim with a session that by suspended, as the store holds it and a boot
// restores it: the session is kept, and no process runs.
func suspendedRole(role claim.Role, by supervise.SuspendSource) podUpdateRole {
	return podUpdateRole{role: role, state: supervise.StateSuspended, session: true, contract: api.DaemonAPIVersion, suspendedBy: by}
}

// replacedOverPassedOverClaims carries R3's round to its replacement: two ready workers and two
// claims the daemon suspended (passedOverSuspended). script gives the issue's workspace what the
// read finds there. The round opens, both workers are asked and answer, and the sweep replaces
// the pod. What every such round shows whatever the read found is held here, and the
// replacement's line is returned:
//
//   - the pod is replaced once, at the held clock the round opened at, long before its bound;
//   - the workspace is read once, through the worker whose process the pod is replaced through,
//     and the replacement made comes straight after that read, with no refusal between: the read
//     holds nothing back;
//   - neither suspended claim is asked (no update-ask row, no publish to its topic), woken or
//     resumed (no Resume call), and each is still suspended once the round is over;
//   - no bundle of the workspace is saved.
func replacedOverPassedOverClaims(t *testing.T, script func(*podUpdateRig)) (*podUpdateRig, map[string]any) {
	t.Helper()
	rig := newPodUpdateRig(t,
		readyWorker(claim.RoleImplementer), readyWorker(claim.RoleTester),
		suspendedRole(claim.RolePlanner, supervise.SuspendedByDaemon),
		suspendedRole(claim.RoleReviewer, supervise.SuspendedByReleased),
	)
	script(rig)
	clocks := rig.replacementClocks()
	rig.drifts(podUpdateDrift)

	rig.tick()
	for _, role := range passedOverWorkers {
		rig.awaitAsked(role, podUpdateRound)
	}
	for _, role := range passedOverSuspended {
		rig.awaitRound(role, podUpdateRound)
	}
	if calls := rig.replacements(); len(calls) != 0 {
		t.Fatalf("the pod was replaced %d times before either worker answered", len(calls))
	}
	for _, role := range passedOverWorkers {
		rig.signal(role, "a1b2c3d")
	}
	// Nothing is deciding when the sweep reads the claims, so the first sweep after the answers
	// is the one that replaces the pod, and the read cannot be followed by a refusal that a
	// machine mid-decision would cause (roundGroup's mask).
	rig.settleAll()
	replaced := rig.awaitReplacement(1, "once both workers answered")

	at := clocks()
	if len(at) == 0 {
		t.Fatal("the runtime was never asked to replace the pod")
	}
	for _, when := range at {
		if !when.Equal(podUpdateStart) {
			t.Fatalf("the replacement was asked at %s, want %s, when the round opened: the bound %s holds nothing", when, podUpdateStart, podUpdateBound)
		}
	}
	var order []string
	for _, call := range rig.rt.Calls() {
		switch call.Method {
		case "WorkspaceWork", "ReplacePod", "ReplacePodNotDue", "ReplacePodNoPod":
			order = append(order, call.Method)
		}
	}
	if !slices.Equal(order, []string{"WorkspaceWork", "ReplacePod"}) {
		t.Fatalf("the runtime was asked %v, want one read of the workspace and then the replacement it stood in front of", order)
	}
	if replaced[0].Pod.Locator == nil || podOf(replaced[0].Pod.Locator) != podUpdatePod {
		t.Fatalf("the pod was replaced through %+v, want a process in pod %s", replaced[0].Pod, podUpdatePod)
	}
	through, _ := replaced[0].Pod.Claim.Role()
	reads := rig.rt.CallsOf("WorkspaceWork")
	if reads[0].Issue != podUpdateIssue || reads[0].Role != through || !slices.Contains(passedOverWorkers, through) {
		t.Fatalf("the workspace was read as %+v, want the issue %s read through the %s, whose process the pod is replaced through",
			reads[0], podUpdateIssue, through)
	}

	for _, role := range append(slices.Clone(passedOverWorkers), passedOverSuspended...) {
		rig.awaitClaim(role, "its round to end with the replacement", func(c supervise.Claim) bool { return c.UpdateRound == "" })
	}
	for _, role := range passedOverSuspended {
		if n := rig.askedOf(role); n != 0 {
			t.Errorf("the round wrote %d update-ask rows for the %s, want none: the daemon suspended it", n, role)
		}
		if n := rig.deliveredTo(role); n != 0 {
			t.Errorf("the %s's topic was published %d asks, want none: a publish to a role no session holds wakes it", role, n)
		}
		if c := rig.claimOf(role); c.State != supervise.StateSuspended {
			t.Errorf("the %s is %s after the round, want it still suspended", role, c.State)
		}
	}
	if resumes := rig.rt.CallsOf("Resume"); len(resumes) != 0 {
		t.Errorf("the round resumed %d processes, want none", len(resumes))
	}
	var bundles int
	if err := rig.pool.QueryRow(rig.ctx, "select count(*) from workspace_bundles").Scan(&bundles); err != nil {
		t.Fatalf("read the workspace bundles: %v", err)
	}
	if bundles != 0 {
		t.Errorf("the round saved %d workspace bundles, want none: the read keeps nothing", bundles)
	}

	lines := loggedLines(t, rig.logs.String(), podUpdateReplacedLine)
	if len(lines) != 1 {
		t.Fatalf("the replacement was logged %d times, want once: %+v", len(lines), lines)
	}
	line := lines[0]
	if line["atTheBound"] != false {
		t.Errorf("the replacement's line says atTheBound=%v, want false: both workers answered long before the bound", line["atTheBound"])
	}
	wantField(t, line, "notWaitedFor", passedOverSuspended...)
	if line["workspaceReadBy"] != string(through) {
		t.Errorf("the replacement's line names the read made by %v, want the %s", line["workspaceReadBy"], through)
	}
	return rig, line
}

// R3's first round: a claim suspended by the daemon's own rule and one an idle release
// suspended, beside two ready workers, over a workspace whose read finds no work. The pod is
// replaced once both workers answered and no turn ran, long before the bound, and neither
// suspended claim is asked, woken or resumed (replacedOverPassedOverClaims). The replacement's
// line names both suspended roles as the claims it did not wait for, and a read that found no
// work. The issue is written nothing: nothing the read found went with the pod.
//
// The negative control is main at fd1f52f1, BET-1182's pod on 2026-11-16: the round waited for
// every claim with a session, asked only a ready one, and held the pod to its bound.
func TestAnUpdateRoundDoesNotWaitForAClaimTheDaemonSuspended(t *testing.T) {
	rig, line := replacedOverPassedOverClaims(t, func(rig *podUpdateRig) {
		rig.rt.ScriptWorkspaceWork(podUpdateIssue, runtime.WorkspaceWork{Head: passedOverHead})
	})

	if line["workspaceRead"] != "found no work" || line["workspaceHead"] != passedOverHead || line["workspaceCommitCount"] != float64(0) {
		t.Errorf("the replacement's line names the read %+v, want one that found no work at the working copy %s", line, passedOverHead)
	}
	if messages := rig.messages(); len(messages) != 0 {
		t.Fatalf("the issue was written %q, want nothing: the read found no work", messages)
	}
}

// The work hold's stop holds no pod update round either (dispatch://INFRA-4109 R1). A claim the
// hold stopped (supervise.SuspendedByHold) runs no process and no turn in the pod, and only the
// hold's end or an operator starts it (supervise's Machine.EndHold). So the round passes it over
// as it passes over a claim the daemon suspended (supervise's Claim.PassedOverByRound): it does
// not ask it, wait for it, wake it or resume it. Once the claims the round asks have answered and
// no turn runs, the pod is replaced long before the bound. The replacement first reads the
// issue's workspace through a worker whose process runs in the pod, and its line names the held
// claim as the one it did not wait for. Once the round is over, the claim is still the hold's.
//
// The control is a worker whose work hold stop still waits for the turn that its close message
// starts (supervise.RequestSuspend.TurnAfter). Its agent runs in the pod and can answer, so the
// round asks it and does not replace the pod before it answers. The tree's record of the hold
// stands, as the close wrote it, so no decision of the round drops that stop.
func TestWorkHoldAnUpdateRoundPassesOverAClaimTheHoldStopped(t *testing.T) {
	const held, stopping = claim.RolePlanner, claim.RoleTester
	workers := []claim.Role{claim.RoleImplementer, stopping}
	rig := newPodUpdateRig(t,
		readyWorker(claim.RoleImplementer), readyWorker(stopping),
		suspendedRole(held, supervise.SuspendedByHold),
	)
	rig.rt.ScriptWorkspaceWork(podUpdateIssue, runtime.WorkspaceWork{Head: passedOverHead})
	if c := rig.claimOf(held); c.State != supervise.StateSuspended || !c.WorkHeld() {
		t.Fatalf("the restored %s is %s, suspended by %q; want it held by the work hold", held, c.State, c.SuspendedBy)
	}
	// The tree's record of the hold, as the close writes it (workflow's workHoldClose). The hold's
	// stop and each decision of the tester while the stop waits for the turn read its row for
	// share, and a decision that finds no row makes no stop (supervise's readHoldForStop).
	if err := store.Transact(rig.ctx, rig.pool, func(_ context.Context, tx pgx.Tx) error {
		return rig.records.PutWorkHold(rig.ctx, tx, record.WorkHold{Project: "LEGION", Tree: podUpdateIssue, RootGeneration: 1,
			Step: record.DesignStepSpec, ArtifactID: "artifact-484", Version: 2, ClosedAt: rig.clock.Now()})
	}); err != nil {
		t.Fatalf("record the work hold of %s: %v", podUpdateIssue, err)
	}
	// The tester's stop as the outbox posts it: the close message reached the tester now, and the
	// stop waits for the turn that message starts, up to its deadline 2 minutes later.
	messageAt := rig.clock.Now()
	m, _ := rig.sup.Machine(rig.tokens[stopping])
	stop := supervise.RequestSuspend{Claim: rig.tokens[stopping], By: supervise.SuspendedByHold,
		Reason: "the design gate of LEGION-208 closed", TurnAfter: messageAt, Deadline: messageAt.Add(2 * time.Minute)}
	if err := m.Handle(rig.ctx, stop); !errors.Is(err, supervise.ErrSuspendHeld) {
		t.Fatalf("the work hold's stop of the ready %s answered %v, want it held for the turn its message starts", stopping, err)
	}
	if c := rig.claimOf(stopping); c.State != supervise.StateReady || !c.WorkHeld() || c.PassedOverByRound() {
		t.Fatalf("the %s is %s, WorkHeld %v, PassedOverByRound %v; want it ready, its hold stop held for its turn and not passed over",
			stopping, c.State, c.WorkHeld(), c.PassedOverByRound())
	}
	clocks := rig.replacementClocks()
	rig.drifts(podUpdateDrift)

	rig.tick()
	for _, role := range workers {
		rig.awaitAsked(role, podUpdateRound)
	}
	if c := rig.claimOf(stopping); !c.WorkHeld() {
		t.Fatalf("after the round asked it the %s is %s, suspended by %q, with no hold stop; want its hold stop still held for its turn",
			stopping, c.State, c.SuspendedBy)
	}
	rig.awaitRound(held, podUpdateRound)
	rig.signal(claim.RoleImplementer, "a1b2c3d")
	rig.sweepSettled()
	if calls := rig.replacements(); len(calls) != 0 {
		t.Fatalf("the pod was replaced %d times before the %s, whose hold stop waits for its turn, answered", len(calls), stopping)
	}
	rig.signal(stopping, "a1b2c3d")
	rig.settleAll()
	replaced := rig.awaitReplacement(1, "once both workers answered")

	for _, when := range clocks() {
		if !when.Equal(podUpdateStart) {
			t.Fatalf("the replacement was asked at %s, want %s, when the round opened: the bound %s holds nothing", when, podUpdateStart, podUpdateBound)
		}
	}
	var order []string
	for _, call := range rig.rt.Calls() {
		switch call.Method {
		case "WorkspaceWork", "ReplacePod", "ReplacePodNotDue", "ReplacePodNoPod":
			order = append(order, call.Method)
		}
	}
	if !slices.Equal(order, []string{"WorkspaceWork", "ReplacePod"}) {
		t.Fatalf("the runtime was asked %v, want one read of the workspace and then the replacement it stood in front of", order)
	}
	through, _ := replaced[0].Pod.Claim.Role()
	if reads := rig.rt.CallsOf("WorkspaceWork"); reads[0].Role != through || !slices.Contains(workers, through) {
		t.Fatalf("the workspace was read as %+v, want it read through the %s, whose process the pod is replaced through", reads[0], through)
	}

	c := rig.awaitClaim(held, "its round to end with the replacement", func(c supervise.Claim) bool { return c.UpdateRound == "" })
	if c.State != supervise.StateSuspended || c.SuspendedBy != supervise.SuspendedByHold {
		t.Errorf("the %s is %s, suspended by %q, after the round; want it still held by the work hold", held, c.State, c.SuspendedBy)
	}
	if n := rig.askedOf(held); n != 0 {
		t.Errorf("the round wrote %d update-ask rows for the %s, want none: the work hold stopped it", n, held)
	}
	if n := rig.deliveredTo(held); n != 0 {
		t.Errorf("the %s's topic was published %d asks, want none: a publish to a role no session holds wakes it", held, n)
	}
	if resumes := rig.rt.CallsOf("Resume"); len(resumes) != 0 {
		t.Errorf("the round resumed %d processes, want none", len(resumes))
	}

	lines := loggedLines(t, rig.logs.String(), podUpdateReplacedLine)
	if len(lines) != 1 {
		t.Fatalf("the replacement was logged %d times, want once: %+v", len(lines), lines)
	}
	line := lines[0]
	if line["atTheBound"] != false {
		t.Errorf("the replacement's line says atTheBound=%v, want false: both workers answered long before the bound", line["atTheBound"])
	}
	wantField(t, line, "notWaitedFor", held)
	if line["workspaceReadBy"] != string(through) || line["workspaceRead"] != "found no work" {
		t.Errorf("the replacement's line names the read %+v, want one by the %s that found no work", line, through)
	}
	if messages := rig.messages(); len(messages) != 0 {
		t.Fatalf("the issue was written %q, want nothing: the read found no work", messages)
	}
}

// workspaceUnsavedLine is the line of a replacement that a save of the issue workspace held back
// (workspaceNotSaved).
const workspaceUnsavedLine = "supervise: the issue workspace could not be saved as a bundle before this update round's replacement; nothing was replaced and the round stays open"

// bundlingRuntime is the pod-update rig's runtime with the workspace bundle that a role's shim
// builds (runtime.WorkspaceBundler). It asks the role's agent connection, as the Sandbox runtime
// does, and that fake connection answers the bundle a test set (fake.Conn.SetWorkspaceBundle) or
// the error a test set. At each replacement of the pod it reads which save the store holds for
// the issue before the replacement goes on, so a test sees what the store held when the pod went.
type bundlingRuntime struct {
	*fake.Runtime
	rig *podUpdateRig
	mu  sync.Mutex
	// savedAtReplace is, for each replacement, the id of the save of the issue's workspace bundle
	// the store held then, 0 for none.
	savedAtReplace []int64
	// failNext is the error the next replacement fails with before it reaches the runtime, nil for
	// none (failNextReplacement).
	failNext error
}

func (b *bundlingRuntime) BundleWorkspace(ctx context.Context, issue string, role claim.Role) (runtime.WorkspaceBundle, error) {
	conn, ok := b.rig.workers[role]
	if !ok {
		return runtime.WorkspaceBundle{}, fmt.Errorf("no process of the %s runs to bundle the workspace", role)
	}
	return conn.BundleWorkspace(ctx, issue, time.Minute)
}

func (b *bundlingRuntime) ReplacePod(ctx context.Context, k runtime.Known, due func(context.Context) error) error {
	var release int64
	if err := b.rig.pool.QueryRow(ctx, `select coalesce(max(release_id), 0) from workspace_bundles where issue = $1`, podUpdateIssue).Scan(&release); err != nil {
		return err
	}
	b.mu.Lock()
	b.savedAtReplace = append(b.savedAtReplace, release)
	fail := b.failNext
	b.failNext = nil
	b.mu.Unlock()
	if fail != nil {
		return fail
	}
	return b.Runtime.ReplacePod(ctx, k, due)
}

// replacedWith is the save of the issue's workspace bundle the store held at each replacement.
func (b *bundlingRuntime) replacedWith() []int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.savedAtReplace)
}

// failNextReplacement makes the next replacement fail with err before it reaches the runtime: a
// ReplacePod that fails in another way than the last word's refusal, as an API call that does not
// reach the cluster fails. It asks no last word, so nothing deletes the save made for it.
func (b *bundlingRuntime) failNextReplacement(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failNext = err
}

// Spec step 6 under `work`: the daemon keeps the pod of a claim the work hold stopped, so that the
// worker's unpushed work stays, and a pod update round that replaces that pod keeps the work as
// the idle release keeps it. The round still passes the held claim over: it does not ask it or
// wait for it. Before it replaces the pod, the shim of the role it replaces the pod through
// bundles the issue workspace, and the daemon saves the bundle in the store the idle release saves
// through (store.PutRoundWorkspaceBundle). The next pod of the issue takes that bundle for its one
// restore (savedWorkspaceBundle, store.TakeWorkspaceBundleForRestore), and the pod the work came
// off is sent nothing. The replacement's line names the save, and the message on the issue says
// that the next pod restores the work rather than that the work is lost.
//
// A save that fails holds the replacement: nothing is replaced, a line names the issue and the
// error, and the round stays open. The next sweep saves again and replaces the pod.
func TestWorkHoldAnUpdateRoundSavesTheWorkOfAClaimTheHoldStopped(t *testing.T) {
	const held = claim.RolePlanner
	workers := []claim.Role{claim.RoleImplementer, claim.RoleTester}
	rig := newPodUpdateRig(t,
		readyWorker(claim.RoleImplementer), readyWorker(claim.RoleTester),
		suspendedRole(held, supervise.SuspendedByHold),
	)
	bundler := &bundlingRuntime{Runtime: rig.rt, rig: rig}
	rig.s.runtime = bundler
	saved := runtime.WorkspaceBundle{Bundle: []byte("PACK the planner's unpushed work"), Head: passedOverHead,
		Tips: []string{passedOverCommit}, At: podUpdateStart, PodUID: podUpdatePod}
	refused := errors.New("the shim refused bundle-workspace: jj git bundle: the working copy is stale")
	for _, role := range workers {
		rig.workers[role].SetWorkspaceBundle(saved)
		rig.workers[role].FailBundleWorkspace(refused)
	}
	rig.rt.ScriptWorkspaceWork(podUpdateIssue, runtime.WorkspaceWork{Head: passedOverHead, Commits: []string{passedOverCommit}, Count: 1})
	rig.drifts(podUpdateDrift)

	rig.tick()
	for _, role := range workers {
		rig.awaitAsked(role, podUpdateRound)
	}
	rig.awaitRound(held, podUpdateRound)
	for _, role := range workers {
		rig.signal(role, "a1b2c3d")
	}
	rig.settleAll()

	testwait.Eventually(t, "a sweep to reach the replacement and fail to save the held claim's work", func() bool {
		rig.tick()
		return len(loggedLines(t, rig.logs.String(), workspaceUnsavedLine)) > 0
	})
	if calls := rig.replacements(); len(calls) != 0 {
		t.Fatalf("the pod was replaced %d times though the save of the held claim's work failed, want none", len(calls))
	}
	unsaved := loggedLines(t, rig.logs.String(), workspaceUnsavedLine)[0]
	if unsaved["issue"] != podUpdateIssue || !strings.Contains(fmt.Sprint(unsaved["error"]), refused.Error()) {
		t.Fatalf("the failed save logged %+v, want a line naming %s and the error %q", unsaved, podUpdateIssue, refused)
	}
	wantField(t, unsaved, "held", held)
	if c := rig.claimOf(held); c.UpdateRound != podUpdateRound {
		t.Fatalf("after the failed save the %s is in round %q, want the round still open", held, c.UpdateRound)
	}

	for _, role := range workers {
		rig.workers[role].FailBundleWorkspace(nil)
	}
	replaced := rig.awaitReplacement(1, "once the held claim's work was saved")

	through, _ := replaced[0].Pod.Claim.Role()
	if !slices.Contains(workers, through) {
		t.Fatalf("the pod was replaced through %+v, want a worker's process", replaced[0].Pod)
	}
	if asked := rig.workers[through].Bundlings(); len(asked) == 0 || asked[len(asked)-1].Issue != podUpdateIssue {
		t.Fatalf("the %s's shim was asked to bundle %+v, want the workspace of %s bundled through the role the pod is replaced through", through, asked, podUpdateIssue)
	}
	at := bundler.replacedWith()
	if len(at) != 1 || at[0] <= 0 {
		t.Fatalf("when the pod was replaced the store held the saves %v, want the round's save before the replacement", at)
	}
	release := at[0]

	take := savedWorkspaceBundle(rig.st, podUpdateProject)
	if _, how, err := take(rig.ctx, podUpdateIssue, podUpdatePod, podUpdateStart.Add(time.Minute)); err != nil || how != runtime.WorkspaceBundleNone {
		t.Fatalf("the pod the work came off asked for the bundle and was answered %v, %v; want nothing", how, err)
	}
	restored, how, err := take(rig.ctx, podUpdateIssue, podUpdateReplacementPod, podUpdateStart.Add(time.Minute))
	if err != nil || how != runtime.WorkspaceBundleSend {
		t.Fatalf("the next pod asked for the bundle and was answered %v, %v; want the bundle sent", how, err)
	}
	if !bytes.Equal(restored.Bundle, saved.Bundle) || restored.Head != saved.Head || !slices.Equal(restored.Tips, saved.Tips) || restored.Release != release {
		t.Fatalf("the next pod's restore received %q at %s, tips %v, save %d; want %q at %s, tips %v, save %d",
			restored.Bundle, restored.Head, restored.Tips, restored.Release, saved.Bundle, saved.Head, saved.Tips, release)
	}

	c := rig.awaitClaim(held, "its round to end with the replacement", func(c supervise.Claim) bool { return c.UpdateRound == "" })
	if c.State != supervise.StateSuspended || c.SuspendedBy != supervise.SuspendedByHold {
		t.Errorf("the %s is %s, suspended by %q, after the round; want it still held by the work hold", held, c.State, c.SuspendedBy)
	}
	if n := rig.askedOf(held); n != 0 {
		t.Errorf("the round wrote %d update-ask rows for the %s, want none: the work hold stopped it", n, held)
	}
	lines := loggedLines(t, rig.logs.String(), podUpdateReplacedLine)
	if len(lines) != 1 {
		t.Fatalf("the replacement was logged %d times, want once: %+v", len(lines), lines)
	}
	wantField(t, lines[0], "notWaitedFor", held)
	if lines[0]["workspaceBundleSave"] != float64(release) || lines[0]["workspaceBundleBytes"] != float64(len(saved.Bundle)) {
		t.Errorf("the replacement's line names the save %v of %v bytes, want save %d of %d bytes", lines[0]["workspaceBundleSave"],
			lines[0]["workspaceBundleBytes"], release, len(saved.Bundle))
	}
	messages := rig.messages()
	if len(messages) != 1 {
		t.Fatalf("the issue was written %q, want one message naming the work the read found", messages)
	}
	if !strings.Contains(messages[0], "Before the replacement, the daemon saved the work that is not on a branch at origin as a bundle. The next pod of this issue restores the bundle before any role starts.") ||
		strings.Contains(messages[0], "is lost") {
		t.Errorf("the issue was told %q, want the work saved for the next pod and not lost", messages[0])
	}
}

// heldWorkNoProcessError is what the line of a held replacement names where no role process runs
// in the round's pod to build the bundle (saveRoundWorkspace's rule for such a pod).
const heldWorkNoProcessError = "no role process runs in the pod"

// A round whose pod no role process runs in, where the work hold stopped a claim of the round.
// Only a shim can build the bundle, so the round does not replace that pod, as the idle release
// keeps a pod whose bundle cannot be built: nothing is replaced by issue and no launch replaces
// the pod. The round holds the replacement as a failed save holds it: a line names the issue, the
// held claim and the error, and once the round's bound has come the operator's side is told once,
// naming the held work. The round still asks the held claim nothing. Once a role runs in the pod
// again — here the tester, which the operator resumes into the round's own pod, kept for the held
// work — the round replaces the pod through that role's process and saves the bundle before it
// does.
func TestWorkHoldAnUpdateRoundKeepsAPodWithNoProcessToSaveTheHeldWork(t *testing.T) {
	const held, worker = claim.RolePlanner, claim.RoleTester
	rig := newPodUpdateRig(t,
		podlessMember(held, supervise.StateSuspended, supervise.SuspendedByHold, false),
		answeredThenSuspended(worker),
	)
	bundler := &bundlingRuntime{Runtime: rig.rt, rig: rig}
	rig.s.runtime = bundler
	rig.drifts(podUpdateDrift)
	rig.rt.ScriptIssuePod(podUpdateIssue, podUpdatePod)

	rig.wantPodKept("a role process runs in the pod to save the held claim's work")
	unsaved := loggedLines(t, rig.logs.String(), workspaceUnsavedLine)
	if len(unsaved) == 0 || unsaved[0]["issue"] != podUpdateIssue || !strings.Contains(fmt.Sprint(unsaved[0]["error"]), heldWorkNoProcessError) {
		t.Fatalf("the held replacement logged %+v, want a line naming %s and that %s", unsaved, podUpdateIssue, heldWorkNoProcessError)
	}
	wantField(t, unsaved[0], "held", held)
	if n := rig.askedOf(held); n != 0 {
		t.Fatalf("the round wrote %d update-ask rows for the %s, want none: the work hold stopped it", n, held)
	}
	if notices := rig.notices(); len(notices) != 0 {
		t.Fatalf("the operator's side was told %+v before the round's bound, want nothing", notices)
	}

	rig.clockAt(podUpdateBound)
	rig.wantPodKept("a role process runs in the pod, whatever the round's bound")
	if notices := rig.notices(); len(notices) != 1 || !strings.Contains(notices[0].Reason, "a claim that the work hold stopped") {
		t.Fatalf("past the bound the operator's side was told %+v, want one notice naming the held work", notices)
	}

	rig.intoRoundPod(worker)
	saved := runtime.WorkspaceBundle{Bundle: []byte("PACK the planner's unpushed work"), Head: passedOverHead, At: podUpdateStart, PodUID: podUpdatePod}
	rig.workers[worker].SetWorkspaceBundle(saved)
	rig.handle(worker, supervise.RequestResume{Claim: rig.tokens[worker]})
	rig.registerInto(worker, podUpdatePod)
	if replaced := rig.rt.CallsOf("ReplaceRoundPodAtLaunch"); len(replaced) != 0 {
		t.Fatalf("the tester's launch replaced the round's pod %+v, want the pod kept for the held work", replaced)
	}
	rig.signal(worker, "a1b2c3d")
	rig.settleAll()

	rig.awaitReplacement(1, "once a role process runs in the pod to save the held work")
	if at := bundler.replacedWith(); len(at) != 1 || at[0] <= 0 {
		t.Fatalf("when the pod was replaced the store held the saves %v, want the round's save before the replacement", at)
	}
	if asked := rig.byIssueAsks(); len(asked) != 0 {
		t.Fatalf("the runtime was asked to replace the pod by issue %+v, want none", asked)
	}
	if c := rig.claimOf(held); c.State != supervise.StateSuspended || c.SuspendedBy != supervise.SuspendedByHold {
		t.Fatalf("the %s is %s, suspended by %q, after the round; want it still held by the work hold", held, c.State, c.SuspendedBy)
	}
}

var (
	// heldWorkSave is the bundle of the planner's unpushed work that a role's shim builds in the
	// rows below.
	heldWorkSave = runtime.WorkspaceBundle{Bundle: []byte("PACK the planner's unpushed work"), Head: passedOverHead,
		Tips: []string{passedOverCommit}, At: podUpdateStart, PodUID: podUpdatePod}
	// nothingUnpushed is the bundle of a workspace with no unpushed work: it carries no bytes.
	nothingUnpushed = runtime.WorkspaceBundle{Head: passedOverHead, At: podUpdateStart.Add(time.Minute), PodUID: podUpdatePod}
)

// bundling makes the rig's runtime a bundlingRuntime, and scripts the read of the issue's
// workspace to find one unpushed commit. A rig that a restart made needs its own.
func (r *podUpdateRig) bundling() *bundlingRuntime {
	bundler := &bundlingRuntime{Runtime: r.rt, rig: r}
	r.s.runtime = bundler
	r.rt.ScriptWorkspaceWork(podUpdateIssue, runtime.WorkspaceWork{Head: passedOverHead, Commits: []string{passedOverCommit}, Count: 1})
	return bundler
}

// bundlesTo makes the shim of every role that has a process connection bundle b from now on.
func (r *podUpdateRig) bundlesTo(b runtime.WorkspaceBundle) {
	for _, conn := range r.workers {
		conn.SetWorkspaceBundle(b)
	}
}

// nextPodTakes is what the issue's next pod is answered when it asks for the saved bundle: a pod
// created an hour after the round opened, after every save of these rows. The ask spends the
// bundle's one restore attempt.
func (r *podUpdateRig) nextPodTakes() (runtime.WorkspaceBundle, runtime.WorkspaceBundleTake) {
	r.t.Helper()
	restored, how, err := savedWorkspaceBundle(r.st, podUpdateProject)(r.ctx, podUpdateIssue, podUpdateReplacementPod, podUpdateStart.Add(time.Hour))
	if err != nil {
		r.t.Fatalf("the next pod asked for the saved bundle: %v", err)
	}
	return restored, how
}

// heldWorkRound carries the round of TestWorkHoldAnUpdateRoundSavesTheWorkOfAClaimTheHoldStopped
// to its replacement: two ready workers and the planner, which the work hold stopped. Each shim
// bundles the planner's work (heldWorkSave). The round asks both workers, and both answer.
func heldWorkRound(t *testing.T) (*podUpdateRig, *bundlingRuntime) {
	t.Helper()
	rig := newPodUpdateRig(t,
		readyWorker(claim.RoleImplementer), readyWorker(claim.RoleTester),
		suspendedRole(claim.RolePlanner, supervise.SuspendedByHold),
	)
	bundler := rig.bundling()
	rig.bundlesTo(heldWorkSave)
	rig.drifts(podUpdateDrift)
	rig.tick()
	for _, role := range passedOverWorkers {
		rig.awaitAsked(role, podUpdateRound)
	}
	rig.awaitRound(claim.RolePlanner, podUpdateRound)
	for _, role := range passedOverWorkers {
		rig.signal(role, "a1b2c3d")
	}
	rig.settleAll()
	return rig, bundler
}

// refuseBundleDeletes makes the store refuse each delete of a workspace bundle, as a database
// that fails the statement does, until allow runs.
func refuseBundleDeletes(t *testing.T, rig *podUpdateRig) (allow func()) {
	t.Helper()
	for _, statement := range []string{
		`create function refuse_bundle_delete() returns trigger language plpgsql as $$
			begin raise exception 'the test refuses the delete of the bundle of %', old.issue; end $$`,
		`create trigger refuse_bundle_delete before delete on workspace_bundles for each row execute function refuse_bundle_delete()`,
	} {
		if _, err := rig.pool.Exec(rig.ctx, statement); err != nil {
			t.Fatalf("make the store refuse the delete of a bundle: %v", err)
		}
	}
	return func() {
		t.Helper()
		if _, err := rig.pool.Exec(rig.ctx, `drop trigger refuse_bundle_delete on workspace_bundles`); err != nil {
			t.Fatalf("let the store delete bundles again: %v", err)
		}
	}
}

// dropFailedLine is the line of a refused replacement whose delete of its own save failed
// (dropRoundSave).
const dropFailedLine = "supervise: the bundle saved for an update round's replacement that was not made could not be deleted"

// A replacement that the last word refuses deletes its save (dropRoundSave). Where that delete
// fails, the save stays in the store. The next replacement finds a workspace with nothing
// unpushed, because the work was pushed since. Its save is empty, and an empty save deletes the
// bundle that no pod has had its restore attempt of, as an idle release's empty save does. So the
// next pod restores nothing over the workspace that moved on. A daemon restart between the failed
// delete and the empty save changes nothing, since the store alone holds the bundle.
func TestWorkHoldAnEmptySaveDeletesTheBundleARefusedReplacementLeft(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "at the next sweep"
		if restart {
			name = "after a daemon restart"
		}
		t.Run(name, func(t *testing.T) {
			rig, bundler := heldWorkRound(t)
			// A turn of the implementer starts while the runtime waits for the pod's launch turn,
			// so the last word refuses the replacement. The store refuses the delete of the save.
			token := rig.tokens[claim.RoleImplementer]
			allow := refuseBundleDeletes(t, rig)
			rig.rt.HoldReplacePod(func() {
				rig.rt.HoldReplacePod(nil)
				rig.handle(claim.RoleImplementer, supervise.StreamTurnStart{Claim: token})
			})
			testwait.Eventually(t, "a sweep to reach the replacement and the last word to refuse it", func() bool {
				rig.tick()
				return len(rig.notDue()) > 0
			})
			stale := bundler.replacedWith()
			if len(stale) != 1 || stale[0] <= 0 || len(rig.replacements()) != 0 {
				t.Fatalf("the store held the saves %v at the refused replacement, and the pod was replaced %d times; want one save and no replacement",
					stale, len(rig.replacements()))
			}
			if lines := loggedLines(t, rig.logs.String(), dropFailedLine); len(lines) != 1 {
				t.Fatalf("the failed delete of the save was logged %d times, want once", len(lines))
			}
			allow()
			rig.handle(claim.RoleImplementer, supervise.StreamTurnEnd{Claim: token})
			rig.reportsNothingRunning(claim.RoleImplementer)
			if restart {
				rig = rig.restart()
				rig.drifts(podUpdateDrift)
				bundler = rig.bundling()
			}
			rig.bundlesTo(nothingUnpushed)

			rig.awaitReplacement(1, "once the turn ended")
			if at := bundler.replacedWith(); at[len(at)-1] != 0 {
				t.Errorf("when the pod was replaced the store held the save %d, want none: the empty save deletes the save that the refused replacement left",
					at[len(at)-1])
			}
			if restored, how := rig.nextPodTakes(); how != runtime.WorkspaceBundleNone {
				t.Fatalf("the next pod was answered %v with the bundle at %s, want nothing: that work was pushed since, and a restore moves the issue's bookmarks back over it",
					how, restored.Head)
			}
		})
	}
}

// A hold stop can land between the sweep's reading and the last word. The sweep read no claim
// that the hold stopped, so the replacement saved nothing, and the claim that the stop lands on
// leaves its unpushed work on the pod's disk. The last word reads the held claims again under the
// pod's launch turn and refuses the replacement (replacementDue). The next sweep saves the work of
// that claim and replaces the pod, and the next pod restores the work.
//
// Here the tester has not answered when the round's bound comes, so the bound lets the
// replacement through, and the hold stops the tester while the runtime waits for the launch turn.
func TestWorkHoldAnUpdateRoundSavesTheWorkOfAClaimTheHoldStopsBeforeTheLastWord(t *testing.T) {
	const stopped = claim.RoleTester
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer), readyWorker(stopped))
	bundler := rig.bundling()
	rig.bundlesTo(heldWorkSave)
	// The tree's record of the hold, as the close writes it (workflow's workHoldClose). The hold's
	// stop reads it, and a stop that finds no record makes no stop (supervise's readHoldForStop).
	if err := store.Transact(rig.ctx, rig.pool, func(_ context.Context, tx pgx.Tx) error {
		return rig.records.PutWorkHold(rig.ctx, tx, record.WorkHold{Project: "LEGION", Tree: podUpdateIssue, RootGeneration: 1,
			Step: record.DesignStepSpec, ArtifactID: "artifact-484", Version: 2, ClosedAt: rig.clock.Now()})
	}); err != nil {
		t.Fatalf("record the work hold of %s: %v", podUpdateIssue, err)
	}
	rig.drifts(podUpdateDrift)

	rig.tick()
	for _, role := range passedOverWorkers {
		rig.awaitAsked(role, podUpdateRound)
	}
	rig.signal(claim.RoleImplementer, "a1b2c3d")
	rig.settleAll()
	rig.clockAt(podUpdateBound)
	rig.rt.HoldReplacePod(func() {
		rig.rt.HoldReplacePod(nil)
		rig.handle(stopped, supervise.RequestSuspend{Claim: rig.tokens[stopped], By: supervise.SuspendedByHold,
			Reason: "the design gate of LEGION-208 closed", Deadline: rig.clock.Now().Add(2 * time.Minute)})
	})
	testwait.Eventually(t, "a sweep past the bound to reach the replacement", func() bool {
		rig.tick()
		return len(rig.notDue())+len(rig.replacements()) > 0
	})
	if calls := rig.replacements(); len(calls) != 0 {
		t.Fatalf("the pod was replaced %d times with the work of the %s unsaved: the hold stopped it after the sweep's save", len(calls), stopped)
	}
	if c := rig.claimOf(stopped); c.State != supervise.StateSuspended || !c.WorkHeld() {
		t.Fatalf("the %s is %s, suspended by %q; want it stopped by the work hold", stopped, c.State, c.SuspendedBy)
	}

	rig.awaitReplacement(1, "once the stopped claim's work was saved")
	at := bundler.replacedWith()
	if len(at) != 2 || at[0] != 0 || at[1] <= 0 {
		t.Fatalf("at the two replacements the store held the saves %v, want none at the refused one and the round's save at the one made", at)
	}
	restored, how := rig.nextPodTakes()
	if how != runtime.WorkspaceBundleSend || !bytes.Equal(restored.Bundle, heldWorkSave.Bundle) || restored.Release != at[1] {
		t.Fatalf("the next pod was answered %v with %q of save %d, want %q of save %d", how, restored.Bundle, restored.Release, heldWorkSave.Bundle, at[1])
	}
}

// A daemon restart, or a ReplacePod that fails in another way than the last word's refusal, after
// the round saved the planner's work leaves that save in the store: only a refusal of the last
// word deletes it (dropRoundSave). The hold then ends, and the planner runs in the pod again and
// answers the round. So the next sweep holds no claim that the hold stopped, and the earlier save
// still stands. The replacement saves again (saveRoundWorkspace): a workspace with nothing unpushed
// deletes the earlier save, and a workspace with work replaces it with the workspace as it is now.
// Either way the next pod restores no earlier work over the work done since.
func TestWorkHoldAnUpdateRoundLeavesNoEarlierSaveForTheNextPod(t *testing.T) {
	newer := runtime.WorkspaceBundle{Bundle: []byte("PACK the work done since the first save"), Head: passedOverCommit,
		At: podUpdateStart.Add(time.Minute), PodUID: podUpdatePod}
	for _, tc := range []struct {
		name    string
		restart bool
		// now is the bundle of the workspace at the replacement after the hold ended.
		now runtime.WorkspaceBundle
	}{
		{name: "a daemon restart, then nothing unpushed", restart: true, now: nothingUnpushed},
		{name: "a ReplacePod that failed, then new work", now: newer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const held = claim.RolePlanner
			rig, bundler := heldWorkRound(t)
			bundler.failNextReplacement(errors.New("the Kubernetes API did not answer"))
			testwait.Eventually(t, "a sweep to save the planner's work and reach the replacement", func() bool {
				rig.tick()
				return len(bundler.replacedWith()) > 0
			})
			earlier := bundler.replacedWith()
			if len(earlier) != 1 || earlier[0] <= 0 || len(rig.replacements()) != 0 {
				t.Fatalf("the store held the saves %v at the failed replacement, and the pod was replaced %d times; want one save and no replacement",
					earlier, len(rig.replacements()))
			}
			if tc.restart {
				rig = rig.restart()
				rig.drifts(podUpdateDrift)
				bundler = rig.bundling()
			}

			// The hold ends: the planner starts again in the round's own pod, and the round asks it.
			rig.intoRoundPod(held)
			m, ok := rig.sup.Machine(rig.tokens[held])
			if !ok {
				t.Fatalf("the %s is not supervised", held)
			}
			if started, err := m.EndHold(rig.ctx, nil); err != nil || !started {
				t.Fatalf("the end of the hold started the %s: %t, %v; want it started", held, started, err)
			}
			rig.registerInto(held, podUpdatePod)
			rig.bundlesTo(tc.now)
			rig.awaitAsked(held, podUpdateRound)
			rig.signal(held, "a1b2c3d")
			rig.settleAll()

			rig.awaitReplacement(1, "once the planner answered")
			at := bundler.replacedWith()
			last := at[len(at)-1]
			restored, how := rig.nextPodTakes()
			if tc.now.Size() == 0 {
				if last != 0 || how != runtime.WorkspaceBundleNone {
					t.Fatalf("the store held the save %d at the replacement and the next pod was answered %v, want no save and nothing: the earlier save %d is older than the workspace",
						last, how, earlier[0])
				}
				return
			}
			if last <= earlier[0] || how != runtime.WorkspaceBundleSend || !bytes.Equal(restored.Bundle, tc.now.Bundle) || restored.Release != last {
				t.Fatalf("the store held the save %d at the replacement and the next pod was answered %v with %q of save %d; want %q of a save after %d",
					last, how, restored.Bundle, restored.Release, tc.now.Bundle, earlier[0])
			}
		})
	}
}

// A bundle whose one restore attempt is spent is never sent to a pod again, so no earlier work can
// come of it, and it stays for a person to recover. A round that passed over no claim the hold
// stopped replaces the pod over such a bundle as it replaces a pod with no bundle, though the
// workspace holds unpushed work. The store's read of the bundle says that its attempt is spent
// (store.WorkspaceBundleSize), and a round's save never replaces such a bundle
// (store.ErrWorkspaceBundleSpent). So the round asks no shim to bundle the workspace: the bundle
// would run to tens of megabytes, and the store would refuse it.
func TestWorkHoldAnUpdateRoundReplacesThePodOverASpentBundle(t *testing.T) {
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer), readyWorker(claim.RoleTester))
	rig.bundling()
	rig.bundlesTo(heldWorkSave)
	spent := runtime.WorkspaceBundle{Bundle: []byte("PACK the work of an earlier release"), Head: passedOverCommit,
		At: podUpdateStart.Add(-time.Hour), PodUID: "pod-of-an-earlier-release"}
	if err := rig.st.PutWorkspaceBundle(rig.ctx, podUpdateIssue, 7, spent); err != nil {
		t.Fatalf("save the earlier release's bundle: %v", err)
	}
	// The round's pod took that bundle for its one restore, and no report of the restore came.
	take := savedWorkspaceBundle(rig.st, podUpdateProject)
	if _, how, err := take(rig.ctx, podUpdateIssue, podUpdatePod, podUpdateStart.Add(-30*time.Minute)); err != nil || how != runtime.WorkspaceBundleSend {
		t.Fatalf("the round's pod asked for the earlier bundle and was answered %v, %v; want it sent", how, err)
	}
	rig.drifts(podUpdateDrift)

	rig.tick()
	for _, role := range passedOverWorkers {
		rig.awaitAsked(role, podUpdateRound)
	}
	for _, role := range passedOverWorkers {
		rig.signal(role, "a1b2c3d")
	}
	rig.settleAll()
	replaced := rig.awaitReplacement(1, "over the spent bundle")

	through, _ := replaced[0].Pod.Claim.Role()
	if asked := rig.workers[through].Bundlings(); len(asked) != 0 {
		t.Errorf("the %s's shim was asked to bundle the workspace %d times, want none: a round that holds no claim builds no bundle over a spent one", through, len(asked))
	}
	if size, held, attempted, err := rig.st.WorkspaceBundleSize(rig.ctx, podUpdateIssue); err != nil || !held || !attempted || size != len(spent.Bundle) {
		t.Errorf("after the replacement the store holds %d bytes (held=%t, spent=%t, %v), want the %d of the spent bundle kept", size, held, attempted, err, len(spent.Bundle))
	}
	if lines := loggedLines(t, rig.logs.String(), workspaceUnsavedLine); len(lines) != 0 {
		t.Errorf("the round logged %d failed saves, want none: a spent bundle holds nothing", len(lines))
	}
}

// R3's second and third rounds: the same claims over a read that finds an unpushed commit, and
// over a read that fails. The replacement is made at the same point as over a read that finds no
// work (replacedOverPassedOverClaims), so what the read found decides nothing. Its line names both
// suspended roles and the commit or the error, and one message on the issue names both roles,
// each with its state, and the commit or the error. The sweeps after the round's end write no
// second message.
//
// Where the work is, as the line and the message say it, is roundKeptVolume's answer (#4550). On
// a kept volume made before the round opened, which the issue's next pod mounts, the message says
// the work is still on that volume rather than lost, and the line says the volume is kept: nothing
// went with the pod. Over any other disk — an ephemeral one, or a kept volume made after the round
// opened — the line names no kept volume, and the message says the pod and its disk are gone and
// the work is lost, whatever the read found. A message that calls lost work safe tells a role not
// to do that work again.
func TestAnUpdateRoundNamesTheWorkItsSuspendedClaimsLeft(t *testing.T) {
	// commitRead scripts a read that finds one unpushed commit, and foundCommit holds the
	// replacement's line to that read, whatever the disk.
	commitRead := func(rig *podUpdateRig) {
		rig.rt.ScriptWorkspaceWork(podUpdateIssue, runtime.WorkspaceWork{Head: passedOverHead, Commits: []string{passedOverCommit}, Count: 1})
	}
	foundCommit := func(t *testing.T, line map[string]any) {
		commits, _ := line["workspaceCommits"].([]any)
		if line["workspaceRead"] != "found work" || len(commits) != 1 || commits[0] != passedOverCommit || line["workspaceCommitCount"] != float64(1) {
			t.Errorf("the replacement's line names the read %+v, want one that found the commit %s", line, passedOverCommit)
		}
	}
	// madeBefore and madeAfter are when the issue's Sandbox made its kept volume: before the
	// round opened, and after it.
	madeBefore, madeAfter := podUpdateStart.Add(-time.Hour), podUpdateStart.Add(time.Minute)
	for _, tc := range []struct {
		name   string
		script func(*podUpdateRig)
		// line holds the replacement's line to what the read found.
		line func(t *testing.T, line map[string]any)
		// says is what the message must carry of the read.
		says string
		// keptVolumeAt is when the issue's Sandbox made the kept volume that is the pod's disk, as
		// roundKeptVolume reads it. nil is an ephemeral disk, which goes with the pod. Only a kept
		// volume made before the round opened is one that the issue's next pod mounts.
		keptVolumeAt *time.Time
	}{
		{name: "an unpushed commit", script: commitRead, line: foundCommit, says: passedOverCommit},
		{name: "an unpushed commit on a kept volume", script: commitRead, line: foundCommit, says: passedOverCommit, keptVolumeAt: &madeBefore},
		// A Sandbox made after the round opened holds a new volume, which holds nothing of the
		// round's pod.
		{name: "an unpushed commit on a kept volume made after the round opened", script: commitRead, line: foundCommit, says: passedOverCommit, keptVolumeAt: &madeAfter},
		{
			name:   "a read that fails",
			script: func(rig *podUpdateRig) { rig.rt.FailWorkspaceWork(podUpdateIssue, errPassedOverRead) },
			line: func(t *testing.T, line map[string]any) {
				if line["workspaceRead"] != "failed" || line["workspaceReadError"] != errPassedOverRead.Error() {
					t.Errorf("the replacement's line names the read %+v, want one that failed with %q", line, errPassedOverRead)
				}
			},
			says: errPassedOverRead.Error(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, line := replacedOverPassedOverClaims(t, func(rig *podUpdateRig) {
				tc.script(rig)
				if tc.keptVolumeAt != nil {
					rig.rt.ScriptKeptVolume(podUpdateIssue, *tc.keptVolumeAt)
				}
			})
			tc.line(t, line)

			messages := rig.messages()
			if len(messages) != 1 {
				t.Fatalf("the issue was written %d messages, want one: %q", len(messages), messages)
			}
			for _, want := range []string{"planner (suspended)", "reviewer (suspended)", tc.says} {
				if !strings.Contains(messages[0], want) {
					t.Errorf("the message does not name %q:\n%s", want, messages[0])
				}
			}
			// kept is whether the disk is a kept volume that the issue's next pod mounts
			// (roundKeptVolume): one made before the round opened.
			kept := tc.keptVolumeAt != nil && tc.keptVolumeAt.Before(podUpdateStart)
			if kept {
				if line["keptVolume"] != true {
					t.Errorf("the replacement's line says keptVolume=%v, want true: the disk is a kept volume that the issue's next pod mounts", line["keptVolume"])
				}
				for _, want := range []string{"Its disk is a kept volume, and the volume is still there.", "Work that is not on a branch at origin is on that volume."} {
					if !strings.Contains(messages[0], want) {
						t.Errorf("the message does not say %q:\n%s", want, messages[0])
					}
				}
				if strings.Contains(messages[0], "is lost") {
					t.Errorf("the message calls work lost on a kept volume that the issue's next pod mounts:\n%s", messages[0])
				}
			} else {
				if value, named := line["keptVolume"]; named {
					t.Errorf("the replacement's line says keptVolume=%v, want no kept volume: the disk went with the pod", value)
				}
				for _, want := range []string{"The pod and its disk are gone.", "Work that is not on a branch at origin is lost."} {
					if !strings.Contains(messages[0], want) {
						t.Errorf("the message does not say %q:\n%s", want, messages[0])
					}
				}
				if strings.Contains(messages[0], "kept volume") {
					t.Errorf("the message names a kept volume over a disk that went with the pod:\n%s", messages[0])
				}
			}

			rig.tick()
			rig.tick()
			if messages := rig.messages(); len(messages) != 1 {
				t.Fatalf("the issue was written %d messages after two more sweeps, want the one: %q", len(messages), messages)
			}
		})
	}
}

// R3's last round: a claim an operator suspended, restored as a boot restores it, beside a worker
// that answered. A person's hold is not the daemon's, so the round still asks for it and waits
// for it whatever its bound (dispatch://INFRA-2981 R5): the pod is not replaced before the bound
// or an hour past it, and the operator's side is told once, at the bound, naming the suspension.
// Once the operator resumes the claim and it answers, the pod is replaced, and nobody is told
// again.
func TestAnUpdateRoundStillWaitsPastItsBoundForAnOperatorSuspension(t *testing.T) {
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer), suspendedRole(claim.RoleTester, supervise.SuspendedByOperator))
	rig.drifts(podUpdateDrift)

	rig.tick()
	rig.awaitAsked(claim.RoleImplementer, podUpdateRound)
	rig.awaitRound(claim.RoleTester, podUpdateRound)
	rig.signal(claim.RoleImplementer, "a1b2c3d")

	for _, at := range []time.Time{podUpdateStart, podUpdateBound.Add(-time.Minute), podUpdateBound, podUpdateBound.Add(time.Hour)} {
		rig.clockAt(at)
		rig.tick()
		rig.tick()
		if calls := rig.replacements(); len(calls) != 0 {
			t.Fatalf("at %s the pod was replaced %d times under the operator's suspension of the tester (bound %s)", at, len(calls), podUpdateBound)
		}
		if notices := rig.notices(); at.Before(podUpdateBound) && len(notices) != 0 {
			t.Fatalf("at %s, before the bound, the controller was told %+v", at, notices)
		}
	}
	notices := rig.notices()
	if len(notices) != 1 || !strings.Contains(notices[0].Reason, "An operator suspended") {
		t.Fatalf("the controller was told %+v, want one notice naming the operator's suspension", notices)
	}
	wantWaiting(t, notices[0], map[claim.Role]bool{claim.RoleImplementer: true, claim.RoleTester: false})

	rig.resumeInPod(claim.RoleTester)
	rig.signal(claim.RoleTester, "e4f5a6b")
	rig.awaitReplacement(1, "once the operator resumed the tester and it answered")
	if notices := rig.notices(); len(notices) != 1 {
		t.Fatalf("the controller was told %d times, want the one notice: %+v", len(notices), notices)
	}
}

// The read of the workspace is made once in a round, and only where it names something: where
// the round passed over a claim that has not answered, through the role whose process the pod is
// replaced through, and once every claim the round asks has answered and no turn runs.
func TestAnUpdateRoundReadsTheWorkspaceOncePerRound(t *testing.T) {
	// No claim is passed over: two workers that answer, and a failed claim the round does not
	// wait for either. The line names the failed claim, and nothing is read: no role the round
	// passed over left work that no role was asked to push.
	t.Run("no claim passed over", func(t *testing.T) {
		rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer), readyWorker(claim.RoleTester),
			podUpdateRole{role: claim.RolePlanner, state: supervise.StateFailed, session: true, contract: api.DaemonAPIVersion})
		rig.drifts(podUpdateDrift)

		rig.tick()
		for _, role := range passedOverWorkers {
			rig.awaitAsked(role, podUpdateRound)
			rig.signal(role, "a1b2c3d")
		}
		rig.awaitReplacement(1, "once both workers answered")

		if reads := rig.rt.CallsOf("WorkspaceWork"); len(reads) != 0 {
			t.Fatalf("the workspace was read %+v, want no read: the round passed over no claim", reads)
		}
		lines := loggedLines(t, rig.logs.String(), podUpdateReplacedLine)
		if len(lines) != 1 || lines[0]["workspaceRead"] != "not made" {
			t.Fatalf("the replacement was logged %+v, want one line that says no read was made", lines)
		}
		wantField(t, lines[0], "notWaitedFor", claim.RolePlanner)
	})

	// A claim the daemon suspended, the architect, sorts first in the group and has no process.
	// The one worker, the implementer, runs in the pod. Nothing is read while the implementer has
	// not answered. Once it answers, the last word refuses the replacement three times, each time
	// over a turn that starts while the replacement waits for the pod's launch turn, and then the
	// runtime fails the replacement once. The read is made once, through the implementer, and each
	// later sweep names that read again. The issue is written nothing until a replacement is made,
	// and then once.
	t.Run("once over the sweeps that replace nothing", func(t *testing.T) {
		rig := newPodUpdateRig(t, suspendedRole(claim.RoleArchitect, supervise.SuspendedByDaemon), readyWorker(claim.RoleImplementer))
		rig.rt.ScriptWorkspaceWork(podUpdateIssue, runtime.WorkspaceWork{Head: passedOverHead, Commits: []string{passedOverCommit}, Count: 1})
		rig.drifts(podUpdateDrift)

		rig.tick()
		rig.awaitAsked(claim.RoleImplementer, podUpdateRound)
		rig.awaitRound(claim.RoleArchitect, podUpdateRound)
		for range 3 {
			rig.tick()
		}
		if reads := rig.rt.CallsOf("WorkspaceWork"); len(reads) != 0 {
			t.Fatalf("the workspace was read %+v while the implementer had not answered, want no read", reads)
		}

		token := rig.tokens[claim.RoleImplementer]
		rig.signal(claim.RoleImplementer, "a1b2c3d")
		for refused := 1; refused <= 3; refused++ {
			rig.rt.HoldReplacePod(func() {
				rig.rt.HoldReplacePod(nil)
				rig.handle(claim.RoleImplementer, supervise.StreamTurnStart{Claim: token})
			})
			testwait.Eventually(t, fmt.Sprintf("the last word to refuse the replacement %d times", refused), func() bool {
				if len(rig.notDue()) < refused {
					rig.tick()
				}
				return len(rig.notDue()) >= refused
			})
			rig.handle(claim.RoleImplementer, supervise.StreamTurnEnd{Claim: token})
		}
		rig.rt.FailReplacePod(errors.New("the Sandbox API refused the suspension"))
		rig.awaitReplacement(1, "that the runtime fails")
		if messages := rig.messages(); len(messages) != 0 {
			t.Fatalf("the issue was written %q before any replacement was made, want nothing", messages)
		}

		rig.rt.FailReplacePod(nil)
		rig.awaitReplacement(2, "once the runtime replaces it")
		reads := rig.rt.CallsOf("WorkspaceWork")
		if len(reads) != 1 || reads[0].Role != claim.RoleImplementer {
			t.Fatalf("the workspace was read %+v, want one read through the implementer, the one role whose process is in the pod", reads)
		}
		messages := rig.messages()
		if len(messages) != 1 || !strings.Contains(messages[0], passedOverCommit) || !strings.Contains(messages[0], "architect (suspended)") {
			t.Fatalf("the issue was written %q, want one message naming the architect and the commit %s", messages, passedOverCommit)
		}
		lines := loggedLines(t, rig.logs.String(), podUpdateReplacedLine)
		if len(lines) != 1 || lines[0]["workspaceRead"] != "found work" {
			t.Fatalf("the replacement was logged %+v, want one line naming the read that found work", lines)
		}
		wantField(t, lines[0], "notWaitedFor", claim.RoleArchitect)
	})

	// The round's bound lets the replacement through over a worker that never answered. The
	// round passed over a claim, but a claim it asks has not answered, so the replacement makes
	// no read, and the issue is written nothing.
	t.Run("not at the bound over a claim that has not answered", func(t *testing.T) {
		rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer), readyWorker(claim.RoleTester),
			suspendedRole(claim.RolePlanner, supervise.SuspendedByDaemon))
		rig.drifts(podUpdateDrift)

		rig.tick()
		for _, role := range passedOverWorkers {
			rig.awaitAsked(role, podUpdateRound)
		}
		rig.awaitRound(claim.RolePlanner, podUpdateRound)
		rig.signal(claim.RoleImplementer, "a1b2c3d")
		rig.tick()
		rig.tick()
		if calls := rig.replacements(); len(calls) != 0 {
			t.Fatalf("the pod was replaced %d times before the bound with the tester unanswered", len(calls))
		}

		rig.clockAt(podUpdateBound)
		rig.awaitReplacement(1, "at the bound, with the tester unanswered")
		if reads := rig.rt.CallsOf("WorkspaceWork"); len(reads) != 0 {
			t.Fatalf("the workspace was read %+v, want no read: the tester had not answered", reads)
		}
		lines := loggedLines(t, rig.logs.String(), podUpdateReplacedLine)
		if len(lines) != 1 || lines[0]["atTheBound"] != true || lines[0]["workspaceRead"] != "not made" {
			t.Fatalf("the replacement was logged %+v, want one line of a bounded end that says no read was made", lines)
		}
		wantField(t, lines[0], "notWaitedFor", claim.RolePlanner)
		if messages := rig.messages(); len(messages) != 0 {
			t.Fatalf("the issue was written %q, want nothing: no read was made", messages)
		}
	})
}
