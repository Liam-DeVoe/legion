package daemon

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/runtime/sandbox"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// newLeftPodRig is the idle rig (idleRig) over a child of a running tree rather than over a root:
// the root LEGION-208 stands in the workflow at implementing, and its child LEGION-209 holds one
// claim, its planner's, the role that worked the child's first phase. With parked false the child
// stands at implementing and its planner is idle in the child's pod, holding a process, as a
// planner stays once it has handed its phase on. With parked true the child stands where a park
// leaves it, at phase done with the status backlog, and its planner is suspended by the daemon
// with no process, as the park's own stop left it.
//
// Either way the pod has been quiet for half an hour, its session reported no background work
// after that, the Envoy listener reports itself ready and the runtime holds the issue's Sandbox,
// so nothing outside the issue's own standing keeps the pod. The rig's issue and token are the
// child's and its planner's, which is what idleRig's own readers (drain, queuedBy, rows, records)
// read.
func newLeftPodRig(t *testing.T, parked bool) *idleRig {
	t.Helper()
	ctx := context.Background()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	root := record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
		Project: "LEGION", Tree: "LEGION-208", Title: "root", Phase: phase.Implementing, Generation: 1,
		Status: "in_progress", Rank: "U", LastDispatchSeq: 1}
	parent := root.Key
	child := record.Issue{Key: "LEGION-209", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
		Project: "LEGION", Tree: root.Key, Parent: &parent, Title: "child", Phase: phase.Implementing, Generation: 1,
		Status: "in_progress", Rank: "V", LastDispatchSeq: 1}
	if parked {
		child.Phase, child.Status, child.DispatchStatus = phase.Done, "backlog", "backlog"
	}
	putOutboxIssue(t, pool, records, root)
	putOutboxIssue(t, pool, records, child)
	st := outboxTreeStore(t, pool, root.Key)

	rt := &releaseRuntime{Runtime: fake.NewRuntime(), onImage: true}
	sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
	sup.deps.Runtime, sup.deps.Store, sup.deps.PluginContract = rt, st, api.DaemonAPIVersion
	sup.store = st

	token := mustClaimToken(t, child.Key, claim.RolePlanner)
	// The roles of an issue share one Sandbox, named as the Kubernetes runtime names it from a
	// role claim of the issue (sandbox.SandboxName).
	rt.sandboxName = sandbox.SandboxName(token)
	stored := supervise.Claim{Token: token, Project: "legion", Tree: root.Key, Issue: child.Key,
		Role: claim.RolePlanner, TreeEpoch: 1, Generation: 1, ServingGeneration: 1,
		State: supervise.StateIdle, Session: "ses-planner", SessionFile: "/tmp/planner.jsonl",
		PluginContract: api.DaemonAPIVersion}
	if parked {
		stored.State, stored.SuspendedBy = supervise.StateSuspended, supervise.SuspendedByDaemon
	} else {
		locator, err := rt.Spawn(ctx, runtime.SpawnSpec{Claim: token, Role: claim.RolePlanner})
		if err != nil {
			t.Fatalf("mint a locator: %v", err)
		}
		stored.Locator = &locator
	}
	if err := st.PutClaim(ctx, stored); err != nil {
		t.Fatalf("seed the child's planner: %v", err)
	}
	if _, err := sup.restore(ctx, []supervise.Claim{stored}); err != nil {
		t.Fatalf("restore the child's planner: %v", err)
	}
	machine, ok := sup.Machine(token)
	if !ok {
		t.Fatalf("no machine for %s", token)
	}
	// The idle rule decides no pod unless the Envoy listener has reported itself ready inside its
	// freshness window (workflowRuntime.listenerLive); a rig stands for a deployment whose
	// listener reports.
	if _, err := pool.Exec(ctx, `insert into listener_reports (project, ready_at) values ($1, $2)
		on conflict (project) do update set ready_at = excluded.ready_at`, "legion", time.Now().UTC()); err != nil {
		t.Fatalf("seed the listener's report: %v", err)
	}
	runner := &outbox{
		pool: pool, records: records, project: "legion", dispatchProject: "LEGION", supervisor: sup, trees: st,
		notices: &outboxPublisher{}, dispatch: &roleHoldersDispatch{}, log: quietLogger(),
		now: func() time.Time { return time.Now().Add(time.Minute) },
	}
	w := &workflowRuntime{pool: pool, projectID: "legion", dispatchProject: "LEGION", log: quietLogger(),
		idleStore: st, idleRuntime: rt, idlePeriod: 10 * time.Minute}
	rig := &idleRig{pool: pool, st: st, issues: records, runner: runner, sup: sup, rt: rt, w: w,
		issue: child, token: token, machine: machine}
	quietPod(t, rig)
	return rig
}

// quietPod leaves the rig's pod quiet for half an hour: its claims last written then, and their
// sessions' last report, read now, naming no background work and no reminder.
// claims.updated_at is what the idle period is measured from (pods.IdleView.QuietSince). The
// report is read after the claim's last turn, as a session's report after a turn is: a claim
// whose report predates its last turn is held as unreported (claim.ReadSessionWork), and a
// relaunched claim that holds no task is sent, at its ready, the daemon's own delivery of what
// the relaunch ended (supervise's queueRelaunch), whose send is such a turn.
func quietPod(t *testing.T, rig *idleRig) {
	t.Helper()
	if _, err := rig.pool.Exec(context.Background(), `update claims set updated_at = now() - interval '30 minutes',
		activity_at = now(), activity_background = false,
		activity_kinds = '', activity_next_reminder_at = null where issue = $1`, rig.issue.Key); err != nil {
		t.Fatalf("leave the pod quiet: %v", err)
	}
}

// parkChild parks the rig's child as the workflow parks a child moved to backlog (the workflow's
// leave): the record stands at phase done with the status backlog, and the outbox holds the park's
// stop — a `suspend` of every role a child can hold, at the child's generation, and the
// issue_suspend that deletes the child's Sandbox once every role has stopped. The departure's two
// other rows, the release of the issue's Dispatch claim and the notice to the architect above,
// reach no role of this pod, and are left out: the stop is what this file reads.
func parkChild(t *testing.T, rig *idleRig) {
	t.Helper()
	ctx := context.Background()
	parked := rig.issue
	parked.Phase, parked.Status, parked.DispatchStatus = phase.Done, "backlog", "backlog"
	payloads := make([]record.OutboxPayload, 0, len(claim.Roles)+1)
	for _, role := range claim.Roles {
		payloads = append(payloads, record.SuperviseRequest{Op: "suspend", Tree: parked.Tree, Role: role,
			Generation: parked.Generation, Reason: parked.Key + " is backlog"})
	}
	payloads = append(payloads, record.IssueSuspend{Tree: parked.Tree, Generation: parked.Generation, TreeGeneration: 1})
	if err := pgx.BeginFunc(ctx, rig.pool, func(tx pgx.Tx) error {
		if err := rig.issues.PutIssue(ctx, tx, parked); err != nil {
			return err
		}
		for _, payload := range payloads {
			if err := rig.issues.Enqueue(ctx, tx, mustOutboxRow(t, parked.Key, payload, time.Now())); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("park %s: %v", parked.Key, err)
	}
	rig.issue = parked
}

// resumeAsOperator is an operator's resume of the rig's stopped planner: the event the operator's
// resume route posts (the api's resumeEvent) and the controller's fresh session posts after its
// own suspension (the api's freshSession), then the launch's registration on the daemon's plugin
// contract and its ready. Neither route reads the standing of the claim's issue, so the planner of
// a child the workflow has taken out comes back, idle, in a pod built again for it.
func resumeAsOperator(t *testing.T, rig *idleRig) {
	t.Helper()
	ctx := context.Background()
	rig.sup.deps.Conns.(*fake.Conns).Register(rig.token, fake.NewConn())
	if err := rig.machine.Handle(ctx, supervise.RequestResume{Claim: rig.token}); err != nil {
		t.Fatalf("the operator's resume of %s: %v", rig.token, err)
	}
	rig.machine.Wait()
	generation := rig.machine.Claim().Generation
	for _, ev := range []supervise.Event{
		supervise.StreamHello{Claim: rig.token, Generation: generation},
		supervise.RequestRegister{Claim: rig.token, Generation: generation, Session: "ses-planner",
			SessionFile: "/tmp/planner.jsonl", PluginContract: api.DaemonAPIVersion},
		supervise.RequestReady{Claim: rig.token, Generation: generation, Session: "ses-planner"},
	} {
		if err := rig.machine.Handle(ctx, ev); err != nil {
			t.Fatalf("handle %T for %s: %v", ev, rig.token, err)
		}
	}
	rig.machine.Wait()
	if got := rig.machine.Claim(); got.State != supervise.StateReady && got.State != supervise.StateIdle {
		t.Fatalf("the planner after the operator's resume = %s, want it ready in a pod of its own", got.State)
	}
	// The resume's launch built the issue's pod again, so the runtime holds a Sandbox for it.
	rig.rt.mu.Lock()
	rig.rt.sandboxGone = false
	rig.rt.mu.Unlock()
	quietPod(t, rig)
}

// leftPodGone is the runtime's informer after a teardown took the issue's pod: it holds no
// Sandbox of the issue any more (releaseRuntime.IssueSandbox).
func leftPodGone(rig *idleRig) {
	rig.rt.mu.Lock()
	defer rig.rt.mu.Unlock()
	rig.rt.sandboxGone = true
}

// idlePasses runs the tick's two passes once, in the tick's order (tickIdle): the pass over the
// pods no claim owns, then the idle release.
func idlePasses(rig *idleRig) {
	ctx := context.Background()
	rig.w.releaseUnownedPods(ctx)
	rig.w.releaseIdlePods(ctx)
}

// holdsOf is every hold record of the rig's project under reason.
func holdsOf(t *testing.T, rig *idleRig, reason workflow.IdleHold) []store.PodHold {
	t.Helper()
	var held []store.PodHold
	for _, hold := range rig.records(t).Holds {
		if hold.Reason == reason.String() {
			held = append(held, hold)
		}
	}
	return held
}

// A child a park took out of the workflow while its tree runs on had its one close at that park:
// the park suspended its claims and deleted its Sandbox, and nothing of its running tree closes
// it again. A role that runs there after that park runs in a pod built again for it — an
// operator's resume, an operator's task to the suspended claim, an operator's retry and the
// controller's fresh session each start a claim without reading its issue's standing — and once
// its turn ends it stands idle. A deployment's parked child stood so: phase done, status backlog,
// its one claim, the planner, idle, and its pod running, every idle pass holding that pod as
// tree_not_live, a close's to stop, with no close left to stop it.
//
// The test parks a finished child whose planner is idle in its running pod, runs the park's stop,
// and reads what it did: the planner suspended and the Sandbox deleted. The operator's resume then
// brings the planner back, idle, into a pod built again, which is the state that deployment's
// child stood in. The idle passes then run the park's stop again themselves
// (workflow.LeftPodClose): the planner reads suspended, by the daemon and with its session kept,
// the Sandbox goes a second time, and no tree_not_live hold is recorded, after that stop or
// before it.
func TestAParkedIssuesIdleClaimIsStoppedSoItsPodIsNotHeldTreeNotLive(t *testing.T) {
	rig := newLeftPodRig(t, false)

	parkChild(t, rig)
	rig.drain(t)
	if got := rig.machine.Claim(); got.State != supervise.StateSuspended || got.SuspendedBy != supervise.SuspendedByDaemon {
		t.Fatalf("the planner after the park's stop = %s by %s, want it suspended by the daemon", got.State, got.SuspendedBy)
	}
	if asked := rig.rt.teardownsAsked(); !slices.Equal(asked, []string{"SuspendIssue"}) {
		t.Fatalf("teardowns asked by the park = %v, want the park's own Sandbox suspension", asked)
	}
	if _, suspended := rig.rt.reads(); suspended != 1 {
		t.Fatalf("Sandbox teardowns the store authorized after the park = %d, want the park's one", suspended)
	}
	leftPodGone(rig)

	resumeAsOperator(t, rig)
	if left := rig.rows(t); len(left) != 0 {
		t.Fatalf("rows of the parked child = %v, want none: the park's stop has finished and nothing else is queued", left)
	}
	if record.InWorkflow(rig.issue) || rig.issue.Phase != phase.Done || rig.issue.Status != "backlog" {
		t.Fatalf("the child = %s at %s, want it parked: phase done, status backlog", rig.issue.Phase, rig.issue.Status)
	}

	idlePasses(rig)
	rig.drain(t)
	if got := rig.machine.Claim(); got.State != supervise.StateSuspended || got.SuspendedBy != supervise.SuspendedByDaemon {
		t.Fatalf("the parked child's planner after the idle passes = %s by %s, want it suspended by the daemon; tree_not_live holds recorded: %+v",
			got.State, got.SuspendedBy, holdsOf(t, rig, workflow.IdleHoldTreeNotLive))
	}
	if got := rig.storedClaim(t, rig.token); got.State != supervise.StateSuspended || got.Session != "ses-planner" || got.Locator != nil {
		t.Fatalf("the stored planner = %s, session %q, locator %+v; want it suspended with its session kept and no process",
			got.State, got.Session, got.Locator)
	}
	if asked := rig.rt.teardownsAsked(); !slices.Equal(asked, []string{"SuspendIssue", "SuspendIssue"}) {
		t.Fatalf("teardowns asked = %v, want the park's and then the close the idle pass finished", asked)
	}
	if _, suspended := rig.rt.reads(); suspended != 2 {
		t.Fatalf("Sandbox teardowns the store authorized = %d, want the park's and the idle pass's close", suspended)
	}
	if left := rig.rows(t); len(left) != 0 {
		t.Fatalf("rows left after the idle pass's close = %v, want none", left)
	}
	if rig.marked(t) {
		t.Fatal("the close left a release mark: a close is no release")
	}

	leftPodGone(rig)
	for range 3 {
		idlePasses(rig)
		rig.drain(t)
	}
	if held := holdsOf(t, rig, workflow.IdleHoldTreeNotLive); len(held) != 0 {
		t.Fatalf("tree_not_live holds recorded = %+v, want none: the pod's close has run, and nothing is left of the pod to hold", held)
	}
	if got := rig.machine.Claim(); got.State != supervise.StateSuspended {
		t.Fatalf("the planner after the later passes = %s, want it left suspended", got.State)
	}
}

// A pod can stand after its issue left the workflow with no claim left to stop: the issue's
// close stopped every claim and its Sandbox suspension never took the pod, or an operator's
// start built the pod again after that close and the role it started has stopped since, by its
// own exit or a failure. The release of a pod no claim owns decides only issues still in the
// workflow (store.UnownedPods), and its rule would hold this one as unowned_tree_not_live; the idle
// rule decides only pods with a live claim (store.IdlePods). So such a pod stood, with nobody in
// it, for as long as its issue did.
//
// The test stages that pod — a parked child of a running tree, its one claim suspended by the
// daemon and recording no process, the runtime still holding its Sandbox, quiet for half an hour
// — and reads that a pass takes it: the passes queue the close's Sandbox suspension alone, which
// deletes the Sandbox, the pod and the disk, and leave the claim as it was.
func TestAPodHeldTreeNotLiveIsTakenOnceItsCloseHasNothingLeftToStop(t *testing.T) {
	rig := newLeftPodRig(t, true)
	if record.InWorkflow(rig.issue) {
		t.Fatalf("the staged child = %s at %s, want it out of the workflow", rig.issue.Phase, rig.issue.Status)
	}
	if _, present := rig.rt.IssueSandbox(rig.issue.Key); !present {
		t.Fatal("the runtime holds no Sandbox of the staged child, want its pod standing")
	}

	queued := rig.queuedBy(t, func() { idlePasses(rig) })
	if !slices.Equal(queued, []string{"issue_suspend"}) {
		t.Fatalf("the passes queued %v, want the close's Sandbox suspension alone: no claim is left to stop, and nothing else takes the pod", queued)
	}
	rig.drain(t)

	if asked := rig.rt.teardownsAsked(); !slices.Equal(asked, []string{"SuspendIssue"}) {
		t.Fatalf("teardowns asked = %v, want the close's Sandbox suspension, which deletes the Sandbox of an issue out of the workflow", asked)
	}
	if _, suspended := rig.rt.reads(); suspended != 1 {
		t.Fatalf("Sandbox teardowns the store authorized = %d, want the one that takes the pod", suspended)
	}
	if got := rig.storedClaim(t, rig.token); got.State != supervise.StateSuspended || got.SuspendedBy != supervise.SuspendedByDaemon || got.Session != "ses-planner" {
		t.Fatalf("the planner = %s by %s with session %q, want it left as its close left it", got.State, got.SuspendedBy, got.Session)
	}
	if left := rig.rows(t); len(left) != 0 {
		t.Fatalf("rows left after the close = %v, want none", left)
	}
	if rig.marked(t) {
		t.Fatal("the close left a release mark: a close is no release")
	}

	// The pod is gone, and the passes after it decide nothing more of the issue.
	leftPodGone(rig)
	if queued := rig.queuedBy(t, func() { idlePasses(rig) }); len(queued) != 0 {
		t.Fatalf("the passes after the close queued %v, want nothing: the runtime holds no pod of the issue", queued)
	}
}

// The close of a left pod with a live role deletes a disk that role worked on, so it reads that
// disk first, as the idle release does (workflow.LeftPodClose). The test stands the parked child
// whose planner an operator's resume brought back, live and idle, in a pod built again, with one
// commit origin does not hold in its workspace. The passes queue the close's workspace check
// alone, which reads the workspace through the planner, finds the commit and declines: the
// planner stays live in its pod, no stop and no Sandbox suspension is queued or run, the pod is
// held as unpushed_work, and the planner is told once which commit keeps it. The passes after the
// decline ask nothing more.
func TestALeftPodWithALiveRoleIsReadBeforeItsClose(t *testing.T) {
	rig := newLeftPodRig(t, false)
	parkChild(t, rig)
	rig.drain(t)
	leftPodGone(rig)
	resumeAsOperator(t, rig)
	publisher := &outboxPublisher{}
	rig.runner.notices = publisher
	rig.rt.ScriptWorkspaceWork(rig.issue.Key, idleRigUnpushed)
	parkTeardowns := rig.rt.teardownsAsked()

	if queued := rig.queuedBy(t, func() { idlePasses(rig) }); !slices.Equal(queued, []string{"issue_bundle"}) {
		t.Fatalf("the passes queued %v, want the close's workspace check alone: a live role's disk is read before anything stops it", queued)
	}
	rig.drain(t)

	if read, suspended := rig.rt.reads(); !slices.Equal(read, []claim.Role{claim.RolePlanner}) || suspended != 1 {
		t.Fatalf("the workspace was read through %v and %d Sandbox teardowns were authorized, want one read through the planner and the park's teardown alone",
			read, suspended)
	}
	if got := rig.machine.Claim(); got.State == supervise.StateSuspended {
		t.Fatalf("the planner = %s by %s, want it live in the pod its work is in", got.State, got.SuspendedBy)
	}
	if got := rig.storedClaim(t, rig.token); got.State == supervise.StateSuspended || got.Locator == nil {
		t.Fatalf("the stored planner = %s with process %+v, want it live with the process it had", got.State, got.Locator)
	}
	if asked := rig.rt.teardownsAsked(); !slices.Equal(asked, parkTeardowns) {
		t.Fatalf("teardowns asked = %v, want the park's %v alone", asked, parkTeardowns)
	}
	for _, left := range rig.rows(t) {
		if left == "issue_suspend" || left == "supervise:suspend" {
			t.Fatalf("rows left after the check = %v, want no stop and no Sandbox suspension queued", rig.rows(t))
		}
	}
	if rig.marked(t) {
		t.Fatal("the close's check left a release mark: a close is no release")
	}
	if !rig.declined(t) {
		t.Fatal("the check that found the commit recorded no decline")
	}
	if held := holdsOf(t, rig, workflow.IdleHoldUnpushedWork); len(held) != 1 {
		t.Fatalf("holds = %+v, want one %s", rig.records(t).Holds, workflow.IdleHoldUnpushedWork)
	}
	messages := roleMessages(publisher, string(rig.token))
	if len(messages) != 1 || !strings.Contains(messages[0], idleRigUnpushed.Commits[0]) {
		t.Fatalf("messages to %s = %q, want one naming the commit %s", rig.token, messages, idleRigUnpushed.Commits[0])
	}

	rows := rig.sequenceValue(t, "outbox")
	for range idleRigPasses {
		idlePasses(rig)
		rig.drain(t)
	}
	if read, _ := rig.rt.reads(); len(read) != 1 {
		t.Fatalf("the workspace was read %d times in all, want only the first check's read", len(read))
	}
	if after := rig.sequenceValue(t, "outbox"); after != rows {
		t.Fatalf("the passes after the decline queued outbox rows (the sequence moved from %d to %d), want none", rows, after)
	}
	if messages := roleMessages(publisher, string(rig.token)); len(messages) != 1 {
		t.Fatalf("messages to %s after the passes = %d, want the decline's one", rig.token, len(messages))
	}
	if got := rig.machine.Claim(); got.State == supervise.StateSuspended {
		t.Fatalf("the planner after the passes = %s, want it still live", got.State)
	}
}
