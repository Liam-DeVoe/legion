package admit

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/projection"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// pnForwardMerge is the words of the request the first implementing round after a wait on the
// children carries: merge the default branch forward, push, and complete.
const pnForwardMerge = "merge the default branch main into the branch, push, and complete with a new handoff record"

// pnAdmission is the admission pass of these cases, quiet as quiet builds one, over a workflow
// engine of the test project that holds the daemon's pod tables (workflow.Config.Pods, the
// store's PodRecordsOf), as the daemon builds its engine: the move into awaiting_children cancels
// the parent's backstops through them, and a CI settlement reads the released claims it would wake
// through them. Each call opens a store of its own on pool and builds a new engine and a new
// admission, so a second call over the same pool is the daemon a restart builds. The facts apply
// through this engine and the pass moves parents through it, as in the daemon.
func pnAdmission(t *testing.T, pool *pgxpool.Pool, cap int) *Admission {
	t.Helper()
	pods, err := claimStore(t, pool).PodRecordsOf(testProject)
	if err != nil {
		t.Fatalf("the pod records of %s: %v", testProject, err)
	}
	admission := quiet(t, cap)
	admission.engine = workflow.New(record.NewStore(), workflow.Config{Project: testProject, Repositories: testRepositories,
		Steps: config.DesignGateRootIssues.Steps(), Clock: func() time.Time { return fixedNow }, Pods: pods}, nil)
	return admission
}

// pnLingering is a workflow engine of the test project under a one-hour linger, as the daemon's
// engine applies a person's or a hand-over's Dispatch event to a tree that lingers.
func pnLingering() *workflow.Engine {
	return workflow.New(record.NewStore(), workflow.Config{Project: testProject, Repositories: testRepositories,
		Steps: config.DesignGateRootIssues.Steps(), Linger: time.Hour, Clock: func() time.Time { return fixedNow }},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// pnArmBackstop arms the backstop an earlier release of key's pod armed for role, due at due.
func pnArmBackstop(t *testing.T, pool *pgxpool.Pool, key string, role claim.Role, due time.Time) {
	t.Helper()
	project, err := claim.ProjectToken(testProject)
	if err != nil {
		t.Fatalf("project token of %s: %v", testProject, err)
	}
	if _, err := pool.Exec(context.Background(), `insert into pod_wake_backstops (project, issue, role, release_id, due_at)
		values ($1, $2, $3, 0, $4)`, project, key, string(role), due); err != nil {
		t.Fatalf("arm the %s backstop of %s: %v", role, key, err)
	}
}

// pnOpenBackstops is how many backstops of key's pod are armed and not fired.
func pnOpenBackstops(t *testing.T, pool *pgxpool.Pool, key string) int {
	t.Helper()
	var open int
	if err := pool.QueryRow(context.Background(), `select count(*) from pod_wake_backstops where issue = $1 and not fired`,
		key).Scan(&open); err != nil {
		t.Fatalf("count the open backstops of %s: %v", key, err)
	}
	return open
}

// pnReleasePod suspends every claim of key with the source `released`, as the idle release of
// its pod leaves them: the claims a wake resumes.
func pnReleasePod(t *testing.T, pool *pgxpool.Pool, key string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `update claims set state = $2, suspended_by = $3 where issue = $1`,
		key, string(supervise.StateSuspended), string(supervise.SuspendedByReleased)); err != nil {
		t.Fatalf("release the pod of %s: %v", key, err)
	}
}

// pnResumes is how many pod wakes were taken for any role of key (a pod_resumes row each).
func pnResumes(t *testing.T, pool *pgxpool.Pool, key string) int {
	t.Helper()
	var resumes int
	if err := pool.QueryRow(context.Background(), `select count(*) from pod_resumes where issue = $1`, key).Scan(&resumes); err != nil {
		t.Fatalf("count the pod wakes of %s: %v", key, err)
	}
	return resumes
}

// pnSession is the session token's claim records.
func pnSession(t *testing.T, pool *pgxpool.Pool, token claim.Token) string {
	t.Helper()
	var session string
	if err := pool.QueryRow(context.Background(), `select session from claims where token = $1`, string(token)).Scan(&session); err != nil {
		t.Fatalf("read the session of %s: %v", token, err)
	}
	return session
}

// pnState is the state `legion state` shows of the test project now (projection.Project over the
// stored records), under the engine's own design-gate steps.
func pnState(t *testing.T, pool *pgxpool.Pool, admission *Admission) api.State {
	t.Helper()
	var state api.State
	inTx(t, pool, func(tx pgx.Tx) {
		var err error
		if state, err = projection.Project(context.Background(), tx, record.NewStore(), testProject,
			admission.engine.GatedSteps(), nil, nil, nil, nil, fixedNow); err != nil {
			t.Fatalf("project the state: %v", err)
		}
	})
	return state
}

// pnBlocked is key's row of `admission.blocked`: what it waits on, and whether the list holds it.
func pnBlocked(state api.State, key string) ([]string, bool) {
	for _, row := range state.Admission.Blocked {
		if row.Issue == key {
			return row.BlockedBy, true
		}
	}
	return nil, false
}

// pnHeldRow is key's row of `admission.held`, and whether the list holds it.
func pnHeldRow(state api.State, key string) (api.HeldIssue, bool) {
	for _, row := range state.Admission.Held {
		if row.Issue == key {
			return row, true
		}
	}
	return api.HeldIssue{}, false
}

// pnInNeitherList fails unless key stands in neither `admission.active` nor `admission.waiting`.
func pnInNeitherList(t *testing.T, state api.State, key string) {
	t.Helper()
	if slices.Contains(state.Admission.Active, key) || slices.Contains(state.Admission.Waiting, key) {
		t.Fatalf("admission.active = %v, admission.waiting = %v; want %s in neither", state.Admission.Active, state.Admission.Waiting, key)
	}
}

// pnSlotted is the keys holding the cap's slots, sorted.
func pnSlotted(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	var keys []string
	for _, slot := range slots(t, pool) {
		keys = append(keys, slot.Issue)
	}
	slices.Sort(keys)
	return keys
}

// pnWorkerStart is the newest start of key's role that carries a task, as the outbox holds it.
func pnWorkerStart(t *testing.T, pool *pgxpool.Pool, key string, role claim.Role) record.SuperviseRequest {
	t.Helper()
	var found *record.SuperviseRequest
	for _, effect := range effects(t, pool) {
		if start, ok := effect.payload.(record.SuperviseRequest); ok && effect.issue == key && start.Op == "start" && start.Role == role && start.Task != "" {
			found = &start
		}
	}
	if found == nil {
		t.Fatalf("no start of the %s of %s carries a task; outbox %+v", role, key, effects(t, pool))
	}
	return *found
}

// pnMovedParent is a live tree whose root, the parent, stands in from holding the slot that phase
// keeps, with its pull request #42 open and one child filed and not released, and seed for what
// else the case needs; fact, applied from source, is the move the child holds. It fails unless the
// parent waits in awaiting_children holding the start of start for cause, and answers the pool,
// the admission and the held start.
func pnMovedParent(t *testing.T, from phase.Phase, seed func(*testing.T, *pgxpool.Pool), source string, fact intake.Fact,
	start phase.Phase, cause string,
) (*pgxpool.Pool, *Admission, record.ChildrenWait) {
	t.Helper()
	pool := migratedPool(t)
	admission := pnAdmission(t, pool, 2)
	spSeedParent(t, pool, spParent, from, record.PhaseKeepsSlot(from), nil)
	pwPullRequest(t, pool, spParent, record.PullRequestOpen)
	seedMirrorChild(t, pool, spChild, "todo", spParent)
	if seed != nil {
		seed(t, pool)
	}
	pwApplied(t, pool, admission, source, "move", fact)
	return pool, admission, pwHeld(t, pool, spParent, start, cause, spChild+" (todo)")
}

// pnChildDone closes the parent's one child in Dispatch and runs the next pass, and answers the
// parent's record after it.
func pnChildDone(t *testing.T, pool *pgxpool.Pool, admission *Admission) record.Issue {
	t.Helper()
	spMirror(t, pool, spChild, "done", spParent, 2)
	spPass(t, pool, admission)
	return issue(t, pool, spParent)
}

// A parent waiting on its open Dispatch children holds no admission slot and wakes no worker, keeps
// its issue's run, and comes back to the start it held with nobody acting (dispatch://INFRA-3320
// R11, R12, R13, and its check dispatch://INFRA-3320/artifact/verification-md#b-k13, of which this
// package proves the wait, the returns, the blocked_by order, the grandparent, the hand-over, the
// held list and the stalled measure). Every case runs through the workflow engine and the admission
// pass as the daemon runs them.
func TestParentWaitsHoldsNoSlotAndComesBack(t *testing.T) {
	// The first case is the check's own run of the wait. Its first read is what the negative
	// control fails at: with no child open, the implementer's completion moves the parent to
	// testing, which keeps its slot.
	t.Run("the wait holds no slot and wakes no worker, keeps the run through a restart, and ends with nobody acting", func(t *testing.T) {
		const (
			promoted    = "LEGION-396"
			above       = "LEGION-395"
			second      = "LEGION-310"
			secondChild = "LEGION-311"
			blocker     = "LEGION-900"
		)
		pool := migratedPool(t)
		admission := pnAdmission(t, pool, 2)
		// The cap is full: another tree's issue and the parent hold its two slots, and a root
		// waits in the line for one.
		seedStarted(t, pool, spFiller, phase.Implementing, nil)
		spSeedParent(t, pool, spParent, phase.Implementing, true, func(parent *record.Issue) {
			parent.DispatchStatus, parent.LastDispatchSeq = "in_progress", 1
		})
		seedWaiting(t, pool, promoted, "B")
		pwPullRequest(t, pool, spParent, record.PullRequestOpen)
		// The plan record the planner completed with, and the implementer at work on its third
		// entry after one review round and one move back, its tester's claim beside it.
		plan := record.PhaseRow{Issue: spParent, Role: claim.RolePlanner, Claim: "planner", Summary: "planned", HandoffCommit: "head-41",
			HandoffRecord: "plan-1", HandoffVersion: 1, LastHandoff: "plan-1", CompletedAt: fixedNow.Add(-4 * time.Hour), Entry: 1,
			EntryTask: "Continue LEGION-300. Issue: LEGION-300. Phase: planning.", EntryRaisedAt: fixedNow.Add(-5 * time.Hour)}
		spSeedPhase(t, pool, plan)
		implementer := putWorkerClaim(t, pool, spParent, spParent, claim.RoleImplementer, 1, supervise.StateIdle, "")
		markGivenEntry(t, pool, implementer, 3)
		tester := putWorkerClaim(t, pool, spParent, spParent, claim.RoleTester, 1, supervise.StateIdle, "")
		spSeedPhase(t, pool, record.PhaseRow{Issue: spParent, Role: claim.RoleImplementer, Claim: implementer, Entry: 3,
			EntryTask: pwTask(spParent, phase.Implementing), EntryRaisedAt: fixedNow.Add(-time.Hour), EntryMovedBack: true,
			LastHandoff: "implement-1", Pass: 1, Rounds: 1, BackwardMoves: map[string]int{"reviewing-implementing": 1}})
		// The backstop an earlier release of the parent's pod armed for its implementer.
		pnArmBackstop(t, pool, spParent, claim.RoleImplementer, fixedNow.Add(time.Hour))
		seedMirrorChild(t, pool, spChild, "todo", spParent)
		// A second parent, waiting on its own open child, that a blocked_by link holds as well.
		spWaiting(t, pool, second, phase.Implementing, "Continue LEGION-310. Issue: LEGION-310. Phase: implementing.",
			"a review asked for changes, while these Dispatch children of LEGION-310 were open: LEGION-311 (todo)", nil)
		seedMirror(t, pool, second, "in_progress", blocker)
		seedMirror(t, pool, blocker, "todo")
		seedMirrorChild(t, pool, secondChild, "todo", second)
		if open := pnOpenBackstops(t, pool, spParent); open != 1 {
			t.Fatalf("open backstops of %s before the move = %d, want the earlier release's one", spParent, open)
		}

		pwApplied(t, pool, admission, "api", "implemented", intake.HandoffComplete{Generation: 1, Issue: spParent,
			Role: claim.RoleImplementer, Claim: implementer, Summary: "implemented", Commit: "head-42", HandoffRecord: "implement-2", HandoffVersion: 2})

		if got := issue(t, pool, spParent); got.Phase != phase.AwaitingChildren || holdsSlot(t, pool, spParent) {
			t.Fatalf("%s after its implementer's completion = phase %s, holding a slot %t (slots %v); want awaiting_children holding none while %s is open",
				spParent, got.Phase, holdsSlot(t, pool, spParent), pnSlotted(t, pool), spChild)
		}
		wait := pwHeld(t, pool, spParent, phase.Testing, "the implementer completed implementing", spChild+" (todo)")
		// The slot the parent gave up went to the root waiting in the line, so the cap is full
		// again without it.
		if got, want := pnSlotted(t, pool), []string{spFiller, promoted}; !slices.Equal(got, want) {
			t.Fatalf("slots = %v, want %v: the parent's slot went to the next in the line", got, want)
		}

		// The state: each waiting parent stands in blocked naming its open child, in neither the
		// active set nor the line, a blocked_by link or not, and the held list names it
		// awaiting_children; the parent shows in_progress, as Dispatch does.
		state := pnState(t, pool, admission)
		for key, child := range map[string]string{spParent: spChild, second: secondChild} {
			if by, listed := pnBlocked(state, key); !listed || !slices.Equal(by, []string{child}) {
				t.Fatalf("admission.blocked = %+v, want %s blocked by %s alone", state.Admission.Blocked, key, child)
			}
			pnInNeitherList(t, state, key)
			if row, listed := pnHeldRow(state, key); !listed || row.Reason != record.HeldAwaitingChildren || !slices.Equal(row.HeldBy, []string{child}) {
				t.Fatalf("admission.held names %s as %+v (listed %t), want awaiting_children held by %s", key, row, listed, child)
			}
		}
		if view := state.Issues[spParent]; view.Phase != phase.AwaitingChildren || view.Status != "in_progress" {
			t.Fatalf("the state's %s = phase %s, status %s; want awaiting_children, in_progress", spParent, view.Phase, view.Status)
		}
		if got := issue(t, pool, spParent); got.DispatchStatus != "in_progress" {
			t.Fatalf("Dispatch shows %s as %s, want in_progress", spParent, got.DispatchStatus)
		}
		pwStatuses(t, pool, spParent, nil)

		// No worker is woken for its work: the backstop armed before the move is gone, and a CI
		// settlement on its head, with every claim of its pod released, queues no role message
		// and takes no pod wake, though the settlement is recorded.
		if open := pnOpenBackstops(t, pool, spParent); open != 0 {
			t.Fatalf("open backstops of %s after the move = %d, want none", spParent, open)
		}
		pnReleasePod(t, pool, spParent)
		pwApplied(t, pool, admission, "github", "settled", intake.PullRequestChecks{Repo: "acme/widgets", Number: 42, HeadSHA: "head-42",
			Generation: 1, Snapshot: "settled-head-42", CheckRuns: []record.AttemptRun{{Name: "unit", ID: 70}}, Failing: []string{"unit"},
			Cancelled: []string{}})
		inTx(t, pool, func(tx pgx.Tx) {
			pr, err := record.NewStore().PullRequest(context.Background(), tx, spParent)
			if err != nil || pr == nil || pr.CheckedHead != "head-42" {
				t.Fatalf("the pull request after the settlement = %+v, %v; want the settlement of head-42 recorded", pr, err)
			}
		})
		if messages := pwPayloads[record.RoleMessage](t, pool, spParent); len(messages) != 0 {
			t.Fatalf("role messages of %s = %+v, want none: no role works a waiting parent", spParent, messages)
		}
		if resumes := pnResumes(t, pool, spParent); resumes != 0 {
			t.Fatalf("pod wakes of %s = %d, want none", spParent, resumes)
		}
		for _, token := range []claim.Token{implementer, tester} {
			if state, by := pwClaimState(t, pool, token); state != supervise.StateSuspended || by != supervise.SuspendedByReleased {
				t.Fatalf("the claim %s = %s by %q, want it released still", token, state, by)
			}
		}
		// A pass and the entry sweep, which bring back the roles a slot or an entry is owed,
		// start no role of it.
		spPass(t, pool, admission)
		applySweep(t, pool, admission, "sweep")
		if starts := startsOf(t, pool, spParent); len(starts) != 0 {
			t.Fatalf("starts of %s during the wait = %+v, want none", spParent, starts)
		}

		// close_root, sign_off and request_backward_move are refused, the last two naming the
		// wait, the API's own engine calls; the issue carries its run.
		pwRefused(t, pwApply(t, pool, admission, "api", "close", intake.CloseRoot{Issue: spParent, Reason: "the change is moot"}),
			"CLOSE_AFTER_PHASE_STARTED", "close_root on the waiting parent")
		for _, act := range []struct {
			name string
			fact intake.Fact
		}{
			{name: "sign_off", fact: intake.SignOff{Issue: spParent}},
			{name: "request_backward_move", fact: intake.BackwardMove{Issue: spParent, Requester: claim.RoleArchitect, To: phase.Implementing,
				Reason: "the checks must change"}},
		} {
			result := pwApply(t, pool, admission, "api", act.name, act.fact)
			pwRefused(t, result, "WAITING_ON_CHILDREN", act.name+" on the waiting parent")
			if !strings.Contains(result.Refusal.Message, spParent+" waits in awaiting_children") {
				t.Fatalf("the refusal of %s = %q, want it to name the wait", act.name, result.Refusal.Message)
			}
		}
		if got := issue(t, pool, spParent); !got.CarriesRun() || got.Phase != phase.AwaitingChildren || !sameHeldStart(got.ChildrenWait, &wait) {
			t.Fatalf("%s after the refusals = phase %s, held start %+v, carries its run %t; want it waiting as the move left it, carrying its run",
				spParent, got.Phase, got.ChildrenWait, got.CarriesRun())
		}

		// The restart: a new store, a new engine and a new admission on the same database, whose
		// first pass reads the parent back exactly as the move left it.
		restarted := pnAdmission(t, pool, 2)
		spPass(t, pool, restarted)
		if got := issue(t, pool, spParent); got.Phase != phase.AwaitingChildren || !sameHeldStart(got.ChildrenWait, &wait) || !got.ForwardMergeOwed {
			t.Fatalf("%s after the restarted daemon's pass = phase %s, held start %+v, merge owed %t; want the start it holds, %+v",
				spParent, got.Phase, got.ChildrenWait, got.ForwardMergeOwed, wait)
		}

		// A root whose rank is above the parent's tree joins the line while the cap is full, and
		// the child reaches done.
		seedWaiting(t, pool, above, "A")
		counted := spPhaseRow(t, pool, spParent, claim.RoleImplementer)
		back := pnChildDone(t, pool, restarted)

		if back.Phase != phase.Implementing || back.ChildrenWait != nil || back.Status != "in_progress" || !back.ForwardMergeOwed {
			t.Fatalf("%s at the pass after its child is done = phase %s, held start %+v, status %s, merge owed %t; want implementing, nothing held, in_progress, the merge owed until its implementer completes the round",
				spParent, back.Phase, back.ChildrenWait, back.Status, back.ForwardMergeOwed)
		}
		starts := pwWorkerStarts(t, pool, spParent)
		if len(starts) != 1 || starts[0].role != claim.RoleImplementer || !starts[0].needsSlot || starts[0].claimable {
			t.Fatalf("starts of %s = %+v, want the implementer's one start waiting for a slot", spParent, starts)
		}
		work := starts[0].task
		for _, want := range []string{"Phase: implementing.", "in place of the start of testing", wait.Reason, pnForwardMerge, spMergeLine} {
			if !strings.Contains(work, want) {
				t.Fatalf("the implementer's task = %q, want it to say %q", work, want)
			}
		}
		if strings.Count(work, spMergeLine) != 1 {
			t.Fatalf("the implementer's task = %q, want the line %s once", work, spMergeLine)
		}
		if entry := spEntry(spLine(t, pool), spParent); entry == nil || entry.Kind != record.LineReturning || entry.Part != record.PartReturned {
			t.Fatalf("the parent's place in the line = %+v, want it among the issues whose work came back", entry)
		}
		if got, want := pnState(t, pool, restarted).Admission.Waiting, []string{spParent, above}; !slices.Equal(got, want) {
			t.Fatalf("admission.waiting = %v, want %v: the returned start before the root ranked above the parent's tree", got, want)
		}
		row := spPhaseRow(t, pool, spParent, claim.RoleImplementer)
		if row.Pass != counted.Pass || row.Rounds != counted.Rounds || !maps.Equal(row.BackwardMoves, counted.BackwardMoves) || row.Entry != counted.Entry+1 {
			t.Fatalf("the implementer's row after the return = %+v, want a new entry and the counts of %+v: the return counts no round and no move back", row, counted)
		}
		if told := spNotices(t, pool, spParent); len(told) != 1 {
			t.Fatalf("awaiting-children notices = %+v, want the move's one: the return tells nobody", told)
		}

		// The first slot that comes free is the parent's, and its start goes to the implementer
		// session the parent had.
		freed := issue(t, pool, spFiller)
		freed.Phase, freed.Status = phase.Accepting, "needs_review"
		putIssue(t, pool, freed)
		spPass(t, pool, restarted)
		if !holdsSlot(t, pool, spParent) || holdsSlot(t, pool, above) || issue(t, pool, above).Phase != phase.Admitted {
			t.Fatalf("slots = %v, %s at %s; want the freed slot the parent's and %s still waiting", pnSlotted(t, pool), above, issue(t, pool, above).Phase, above)
		}
		if starts := pwWorkerStarts(t, pool, spParent); len(starts) != 1 || !starts[0].claimable {
			t.Fatalf("starts of %s = %+v, want its start released by the slot", spParent, starts)
		}
		project, err := claim.ProjectToken(testProject)
		if err != nil {
			t.Fatalf("project token: %v", err)
		}
		start := pnWorkerStart(t, pool, spParent, claim.RoleImplementer)
		if token, err := claim.NewToken(project, start.Tree, start.Role); err != nil || token != implementer || start.FreshSession ||
			start.Generation != 1 || start.Phase != phase.Implementing || start.Task != work {
			t.Fatalf("the start = %+v, naming claim %s (%v); want the implementer's claim %s at generation 1, its session kept", start, token, err, implementer)
		}
		if session := pnSession(t, pool, implementer); session != "ses_worker" {
			t.Fatalf("the implementer's claim holds session %q, want the session it had, ses_worker", session)
		}
		for _, effect := range effects(t, pool) {
			if request, ok := effect.payload.(record.SuperviseRequest); ok && effect.issue == spParent && request.Op != "start" {
				t.Fatalf("supervise request %+v of %s, want none but starts: nothing ended a claim of the run", request, spParent)
			}
		}

		// The run is as it was: the plan record, the pull request and the generation.
		if got := spPhaseRow(t, pool, spParent, claim.RolePlanner); got.HandoffRecord != plan.HandoffRecord || got.HandoffVersion != plan.HandoffVersion ||
			got.Summary != plan.Summary || got.HandoffCommit != plan.HandoffCommit || !got.CompletedAt.Equal(plan.CompletedAt) {
			t.Fatalf("the planner's row = %+v, want the plan record %+v kept", got, plan)
		}
		inTx(t, pool, func(tx pgx.Tx) {
			pr, err := record.NewStore().PullRequest(context.Background(), tx, spParent)
			if err != nil || pr == nil || pr.Number != 42 || pr.HeadSHA != "head-42" || pr.State != record.PullRequestOpen || pr.Branch != "legion/"+spParent {
				t.Fatalf("the pull request = %+v, %v; want #42 open at head-42 on its branch", pr, err)
			}
		})
		if got := issue(t, pool, spParent); got.Generation != 1 || !got.CarriesRun() {
			t.Fatalf("%s = generation %d, carries its run %t; want generation 1, carrying it", spParent, got.Generation, got.CarriesRun())
		}
	})

	// The other returns, each from a move the engine makes.
	tested := func(verdict, summary string) intake.HandoffComplete {
		return intake.HandoffComplete{Generation: 1, Issue: spParent, Role: claim.RoleTester, Claim: "tester", Summary: summary,
			Verdict: verdict, Commit: "head-42", HandoffRecord: "test-1", HandoffVersion: 1}
	}
	implemented := intake.HandoffComplete{Generation: 1, Issue: spParent, Role: claim.RoleImplementer, Claim: "implementer",
		Summary: "implemented", Commit: "head-42", HandoffRecord: "implement-1", HandoffVersion: 1}

	t.Run("a held implementing after a tester's fail comes back with the tester's reason in the task", func(t *testing.T) {
		pool, admission, wait := pnMovedParent(t, phase.Testing, nil, "api", tested("fail", "the empty list panics"),
			phase.Implementing, "the tester completed testing")

		back := pnChildDone(t, pool, admission)

		if back.Phase != phase.Implementing || back.ChildrenWait != nil {
			t.Fatalf("the parent = phase %s, held start %+v; want implementing", back.Phase, back.ChildrenWait)
		}
		starts := pwWorkerStarts(t, pool, spParent)
		if len(starts) != 1 || starts[0].role != claim.RoleImplementer {
			t.Fatalf("starts of %s = %+v, want the implementer's", spParent, starts)
		}
		work := starts[0].task
		for _, want := range []string{wait.Task + " ", "Tester's verdict: fail.", "Handoff: test version 1", pnForwardMerge} {
			if !strings.Contains(work, want) {
				t.Fatalf("the implementer's task = %q, want it to carry %q", work, want)
			}
		}
		if strings.Count(work, spMergeLine) != 1 {
			t.Fatalf("the implementer's task = %q, want the line %s once", work, spMergeLine)
		}
	})

	for _, held := range []struct {
		name  string
		from  phase.Phase
		seed  func(*testing.T, *pgxpool.Pool)
		src   string
		fact  intake.Fact
		start phase.Phase
		cause string
	}{
		{name: "a held reviewing comes back at implementing, naming the held start", from: phase.Testing, src: "api",
			fact: tested("pass", "every check passed"), start: phase.Reviewing, cause: "the tester completed testing"},
		{name: "a held accepting comes back at implementing, naming the held start", from: phase.Reviewing,
			seed: func(t *testing.T, pool *pgxpool.Pool) { pwReviewed(t, pool) }, src: "github",
			fact: pwReview("APPROVED", "the change is right"), start: phase.Accepting, cause: "a review approved the head"},
	} {
		t.Run(held.name, func(t *testing.T) {
			pool, admission, wait := pnMovedParent(t, held.from, held.seed, held.src, held.fact, held.start, held.cause)

			back := pnChildDone(t, pool, admission)

			if back.Phase != phase.Implementing || back.ChildrenWait != nil || !back.ForwardMergeOwed {
				t.Fatalf("the parent = phase %s, held start %+v, merge owed %t; want implementing in place of %s, the merge still owed", back.Phase, back.ChildrenWait,
					back.ForwardMergeOwed, held.start)
			}
			starts := pwWorkerStarts(t, pool, spParent)
			if len(starts) != 1 || starts[0].role != claim.RoleImplementer {
				t.Fatalf("starts of %s = %+v, want the implementer's", spParent, starts)
			}
			for _, want := range []string{"Phase: implementing.", "in place of the start of " + string(held.start), wait.Reason, pnForwardMerge, spMergeLine} {
				if !strings.Contains(starts[0].task, want) {
					t.Fatalf("the implementer's task = %q, want it to say %q", starts[0].task, want)
				}
			}
		})
	}

	t.Run("a held planning after the architect's backward move comes back at planning, and its implementing round merges forward", func(t *testing.T) {
		const reason = "the plan missed the empty list"
		pool, admission, wait := pnMovedParent(t, phase.Accepting, nil, "api",
			intake.BackwardMove{Issue: spParent, Requester: claim.RoleArchitect, To: phase.Planning, Reason: reason},
			phase.Planning, "moved back to planning by the architect: "+reason)

		back := pnChildDone(t, pool, admission)

		if back.Phase != phase.Planning || back.ChildrenWait != nil || !back.ForwardMergeOwed {
			t.Fatalf("the parent = phase %s, held start %+v, merge owed %t; want planning, the merge still owed for implementing",
				back.Phase, back.ChildrenWait, back.ForwardMergeOwed)
		}
		starts := pwWorkerStarts(t, pool, spParent)
		if len(starts) != 1 || starts[0].role != claim.RolePlanner || starts[0].task != wait.Task || !strings.Contains(starts[0].task, reason) ||
			strings.Contains(starts[0].task, spMergeLine) {
			t.Fatalf("starts of %s = %+v, want the planner's on the held task %q, which carries %q and no forward merge", spParent, starts, wait.Task, reason)
		}

		pwApplied(t, pool, admission, "api", "planned", intake.HandoffComplete{Generation: 1, Issue: spParent, Role: claim.RolePlanner,
			Claim: "planner", Summary: "planned again", Commit: "head-42", HandoffRecord: "plan-2", HandoffVersion: 2})

		if got := issue(t, pool, spParent); got.Phase != phase.Implementing || !got.ForwardMergeOwed {
			t.Fatalf("the parent after its planner's completion = phase %s, merge owed %t; want implementing, the merge owed until its implementer completes the round", got.Phase, got.ForwardMergeOwed)
		}
		fmAsksOnce(t, "the first implementing task after the wait", pnWorkerStart(t, pool, spParent, claim.RoleImplementer).Task)
	})

	t.Run("a parent whose pull request merged during the wait comes back at the start it held, with no forward merge", func(t *testing.T) {
		pool, admission, wait := pnMovedParent(t, phase.Implementing, nil, "api", implemented, phase.Testing, "the implementer completed implementing")
		pwApplied(t, pool, admission, "github", "merged", intake.PullRequestMerged{Repo: "acme/widgets", Number: 42, MergeSHA: "merge-42"})
		if got := issue(t, pool, spParent); got.Phase != phase.AwaitingChildren {
			t.Fatalf("the parent after the merge = %s, want awaiting_children still", got.Phase)
		}

		back := pnChildDone(t, pool, admission)

		if back.Phase != phase.Testing || back.ChildrenWait != nil || back.ForwardMergeOwed {
			t.Fatalf("the parent = phase %s, held start %+v, merge owed %t; want testing as it was held, nothing owed", back.Phase, back.ChildrenWait, back.ForwardMergeOwed)
		}
		starts := pwWorkerStarts(t, pool, spParent)
		if len(starts) != 1 || starts[0].role != claim.RoleTester || starts[0].task != wait.Task ||
			strings.Contains(starts[0].task, spMergeLine) || strings.Contains(starts[0].task, pnForwardMerge) {
			t.Fatalf("starts of %s = %+v, want the tester's on the held task %q, with no forward merge", spParent, starts, wait.Task)
		}
	})

	t.Run("a parent in a tree whose design gate is closed at a newer version comes back without an approval", func(t *testing.T) {
		approved := 2
		gate := record.DesignGate{Issue: spParent, ArtifactID: "5b0c2d7e-1f43-4a8e-9c6d-2e7f1a3b8d40", LatestVersion: 3,
			ApprovedVersion: &approved, EverOpened: true}
		pool, admission, _ := pnMovedParent(t, phase.Implementing, func(t *testing.T, pool *pgxpool.Pool) { seedGate(t, pool, gate) },
			"api", implemented, phase.Testing, "the implementer completed implementing")

		back := pnChildDone(t, pool, admission)
		spPass(t, pool, admission)

		if back.Phase != phase.Implementing || back.ChildrenWait != nil {
			t.Fatalf("the parent = phase %s, held start %+v; want implementing", back.Phase, back.ChildrenWait)
		}
		if starts := pwWorkerStarts(t, pool, spParent); len(starts) != 1 || starts[0].role != claim.RoleImplementer || !starts[0].claimable ||
			!holdsSlot(t, pool, spParent) {
			t.Fatalf("starts of %s = %+v, slots %v; want the implementer's released by a slot, the closed gate holding nothing", spParent, starts, pnSlotted(t, pool))
		}
		inTx(t, pool, func(tx pgx.Tx) {
			gates, err := record.NewStore().GatesOf(context.Background(), tx, spParent)
			got := record.SpecGate(gates)
			if err != nil || got == nil || got.LatestVersion != 3 || got.ApprovedVersion == nil || *got.ApprovedVersion != 2 {
				t.Fatalf("the design gate after the return = %+v, %v; want version 3 still unapproved, approved at 2", got, err)
			}
		})
	})

	t.Run("a parent a blocked_by link holds at the return waits on it in the line, behind one whose wait ended earlier, until the link's issue is done", func(t *testing.T) {
		const (
			blocker    = "LEGION-900"
			early      = "LEGION-360"
			earlyChild = "LEGION-361"
			late       = "LEGION-350"
			lateChild  = "LEGION-351"
		)
		pool := migratedPool(t)
		admission := pnAdmission(t, pool, 2)
		seedMirror(t, pool, blocker, "todo")
		for parent, child := range map[string]string{early: earlyChild, late: lateChild} {
			spWaiting(t, pool, parent, phase.Implementing, "Continue "+parent+". Issue: "+parent+". Phase: implementing.", "a review asked for changes", nil)
			seedMirror(t, pool, parent, "in_progress", blocker)
			seedMirrorChild(t, pool, child, "todo", parent)
		}

		spMirror(t, pool, earlyChild, "done", early, 2)
		spPass(t, pool, admission)
		spMirror(t, pool, lateChild, "done", late, 2)
		spPass(t, pool, admission)

		state := pnState(t, pool, admission)
		if want := []string{early, late}; !slices.Equal(state.Admission.Waiting, want) {
			t.Fatalf("admission.waiting = %v, want %v: ordered by the moment each wait on the children ended", state.Admission.Waiting, want)
		}
		for _, key := range []string{early, late} {
			if got := issue(t, pool, key); got.Phase != phase.Implementing || got.ChildrenWait != nil {
				t.Fatalf("%s after its child is done = phase %s, held start %+v; want implementing", key, got.Phase, got.ChildrenWait)
			}
			if line := state.Issues[key].WaitingLine; line == nil || line.Kind != record.LineWaitingOnIssue.String() {
				t.Fatalf("the state's place of %s in the line = %+v, want the waiting kind %s", key, line, record.LineWaitingOnIssue)
			}
			if starts := startsOf(t, pool, key); len(starts) != 1 || starts[0].claimable {
				t.Fatalf("starts of %s = %+v, want one held while %s is open", key, starts, blocker)
			}
		}
		assertSlots(t, pool, nil)

		spMirror(t, pool, blocker, "done", "", 2)
		spPass(t, pool, admission)

		for _, key := range []string{early, late} {
			if starts := startsOf(t, pool, key); len(starts) != 1 || !starts[0].claimable || !holdsSlot(t, pool, key) {
				t.Fatalf("starts of %s once %s is done = %+v, slots %v; want its start delivered with a slot", key, blocker, starts, pnSlotted(t, pool))
			}
		}
	})

	t.Run("a grandparent comes back only after its waiting child has come back and reached done", func(t *testing.T) {
		const (
			grandparent = "LEGION-370"
			parent      = "LEGION-371"
			grandchild  = "LEGION-372"
		)
		pool := migratedPool(t)
		admission := pnAdmission(t, pool, 3)
		spSeedParent(t, pool, grandparent, phase.Implementing, true, nil)
		putIssue(t, pool, record.Issue{Key: parent, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
			Project: testProject, Title: parent, Tree: grandparent, Parent: record.ParentOf(grandparent), Phase: phase.Implementing,
			Generation: 1, Status: "in_progress", Rank: parent})
		takeSlot(t, pool, parent)
		seedPullRequest(t, pool, grandparent, 43, record.PullRequestOpen)
		seedMirrorChild(t, pool, parent, "in_progress", grandparent)
		seedMirrorChild(t, pool, grandchild, "todo", parent)
		spSeedPhase(t, pool, record.PhaseRow{Issue: parent, Role: claim.RoleImplementer, Claim: "implementer", Entry: 1,
			EntryTask: pwTask(parent, phase.Implementing), EntryRaisedAt: fixedNow.Add(-time.Hour)})

		// The grandparent's implementer completes while its child is open; the pass that fact runs
		// stops the child's own implementing, since a child of it is open too.
		pwApplied(t, pool, admission, "api", "implemented", intake.HandoffComplete{Generation: 1, Issue: grandparent, Role: claim.RoleImplementer,
			Claim: "implementer", Summary: "implemented", Commit: "head-43", HandoffRecord: "implement-1", HandoffVersion: 1})
		for key, start := range map[string]phase.Phase{grandparent: phase.Testing, parent: phase.Implementing} {
			if got := issue(t, pool, key); got.Phase != phase.AwaitingChildren || got.ChildrenWait == nil || got.ChildrenWait.Start != start {
				t.Fatalf("%s = phase %s, held start %+v; want awaiting_children holding %s", key, got.Phase, got.ChildrenWait, start)
			}
		}

		spMirror(t, pool, grandchild, "done", parent, 2)
		spPass(t, pool, admission)
		if got := issue(t, pool, parent); got.Phase != phase.Implementing {
			t.Fatalf("%s once its child is done = %s, want implementing", parent, got.Phase)
		}
		if got := issue(t, pool, grandparent); got.Phase != phase.AwaitingChildren {
			t.Fatalf("%s while %s works after its return = %s, want awaiting_children still", grandparent, parent, got.Phase)
		}

		closed := issue(t, pool, parent)
		closed.Phase, closed.Status = phase.Done, "done"
		putIssue(t, pool, closed)
		spMirror(t, pool, parent, "done", grandparent, 2)
		spPass(t, pool, admission)
		if got := issue(t, pool, grandparent); got.Phase != phase.Implementing || got.ChildrenWait != nil {
			t.Fatalf("%s once %s reached done = phase %s, held start %+v; want implementing", grandparent, parent, got.Phase, got.ChildrenWait)
		}
	})

	// The linger's hand-over of a closed tree's child in awaiting_children carries its run and the
	// start it holds; the handed-over root is promoted as any is, and comes back as above.
	t.Run("a linger's hand-over carries a waiting child whole, which is promoted holding no slot and comes back as above", func(t *testing.T) {
		const (
			root       = "LEGION-208"
			child      = "LEGION-209"
			grandchild = "LEGION-210"
			artifact   = "7d1e3a5c-9b2f-4c6e-8a40-3f5d7b9e1c26"
			task       = "Continue LEGION-209. Issue: LEGION-209. Phase: implementing."
			reason     = "a review asked for changes, while these Dispatch children of LEGION-209 were open: LEGION-210 (backlog)"
		)
		pool := migratedPool(t)
		admission := pnAdmission(t, pool, 1)
		seedStarted(t, pool, spFiller, phase.Implementing, nil)
		until, waited := fixedNow.Add(time.Hour), fixedNow.Add(-2*time.Hour)
		held := record.ChildrenWait{Start: phase.Implementing, Task: task, Reason: reason, Since: waited}
		parent := root
		inTx(t, pool, func(tx pgx.Tx) {
			records, ctx := record.NewStore(), context.Background()
			for _, put := range []error{
				records.PutIssue(ctx, tx, record.Issue{Key: root, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Tree: root,
					Project: testProject, Title: root, Phase: phase.Done, Generation: 1, Status: "done", LingerUntil: &until}),
				records.PutIssue(ctx, tx, record.Issue{Key: child, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Tree: root,
					Project: testProject, Title: child, Parent: &parent, Phase: phase.AwaitingChildren, Generation: 1, Status: "in_progress", Rank: "B",
					LastDispatchSeq: 1, ChildrenWait: &held, ForwardMergeOwed: true, ChildrenWaitedAt: &waited}),
				records.PutGate(ctx, tx, record.DesignGate{Issue: root, ArtifactID: artifact, LatestVersion: 2, ApprovedVersion: new(2)}),
				records.PutPullRequest(ctx, tx, record.PullRequest{Issue: child, Repo: "acme/widgets", Number: 42, Branch: "legion/LEGION-209",
					HeadSHA: "head-42", Generation: 1, State: record.PullRequestOpen}),
				records.PutPhase(ctx, tx, record.PhaseRow{Issue: child, Role: claim.RoleImplementer, Claim: "implementer", Entry: 2,
					EntryTask: task, EntryRaisedAt: waited.Add(-time.Hour)}),
				records.PutHandover(ctx, tx, record.Handover{Issue: child, Tree: root, Generation: 1}),
			} {
				if put != nil {
					t.Fatalf("seed: %v", put)
				}
			}
		})
		seedMirrorChild(t, pool, grandchild, "backlog", child)

		apply(t, pool, admission, "handover", intake.DispatchIssue{Key: child, ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated",
			Status: "todo", Title: child, Parent: root, Rank: "B", HandedOver: true, DaemonActor: true}, pnLingering())

		got := issue(t, pool, child)
		if got.Tree != child || got.Generation != 2 || got.Phase != phase.AwaitingChildren || !sameHeldStart(got.ChildrenWait, &held) ||
			!got.ForwardMergeOwed || got.ChildrenWaitedAt == nil || !got.ChildrenWaitedAt.Equal(waited) {
			t.Fatalf("the handed-over child = %+v; want its own tree at generation 2, waiting still with %+v, the merge owed and the wait's moment", got, held)
		}
		if got.Status != "in_progress" || got.HandedOverFrom != "" || holdsSlot(t, pool, child) {
			t.Fatalf("the handed-over child = status %s, handed over from %q, slots %v; want it promoted in_progress holding no slot",
				got.Status, got.HandedOverFrom, pnSlotted(t, pool))
		}
		written := false
		for _, effect := range effects(t, pool) {
			written = written || (effect.issue == child && reflect.DeepEqual(effect.payload, record.OrdinaryStatusWrite("in_progress", "todo", "")))
		}
		if !written {
			t.Fatalf("outbox effects = %+v, want %s written in_progress at its promotion", effects(t, pool), child)
		}
		if starts := pwWorkerStarts(t, pool, child); len(starts) != 0 || architectStarts(t, pool, child) != 1 {
			t.Fatalf("starts of %s = %+v, want its architect's alone", child, startsOf(t, pool, child))
		}
		inTx(t, pool, func(tx pgx.Tx) {
			records, ctx := record.NewStore(), context.Background()
			pr, err := records.PullRequest(ctx, tx, child)
			if err != nil || pr == nil || pr.HeadSHA != "head-42" || pr.Generation != 2 {
				t.Fatalf("carried pull request = %+v, %v; want head-42 at generation 2", pr, err)
			}
			gates, err := records.GatesOf(ctx, tx, child)
			if gate := record.SpecGate(gates); err != nil || gate == nil || gate.ArtifactID != artifact {
				t.Fatalf("carried gate = %+v, %v; want the old root's", gates, err)
			}
		})
		if row := spPhaseRow(t, pool, child, claim.RoleImplementer); row.Entry != 2 || row.EntryTask != task {
			t.Fatalf("the implementer's phases row = %+v, want it carried at entry 2", row)
		}

		spMirror(t, pool, grandchild, "done", child, 2)
		spPass(t, pool, admission)

		if back := issue(t, pool, child); back.Phase != phase.Implementing || back.ChildrenWait != nil || !back.ForwardMergeOwed {
			t.Fatalf("the carried root after its child is done = phase %s, held start %+v, merge owed %t; want implementing, the merge still owed", back.Phase, back.ChildrenWait, back.ForwardMergeOwed)
		}
		starts := pwWorkerStarts(t, pool, child)
		if len(starts) != 1 || starts[0].role != claim.RoleImplementer || !starts[0].needsSlot || starts[0].claimable ||
			!strings.HasPrefix(starts[0].task, task+" ") || strings.Count(starts[0].task, spMergeLine) != 1 {
			t.Fatalf("starts of %s = %+v, want the implementer's on the held task with the forward merge, waiting for a slot under the full cap", child, starts)
		}
		if entry := spEntry(spLine(t, pool), child); entry == nil || entry.Kind != record.LineReturning {
			t.Fatalf("the carried root's place in the line = %+v, want it among the issues whose work came back", entry)
		}
	})

	// A promotion of a re-admitted tree brings back the roles of its members that have run, and
	// none of a member waiting on its children.
	t.Run("a re-admission of its tree starts no role of a waiting parent", func(t *testing.T) {
		const root, member, grandchild = "LEGION-208", "LEGION-209", "LEGION-210"
		pool := migratedPool(t)
		admission := pnAdmission(t, pool, 1)
		putIssue(t, pool, record.Issue{Key: root, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: testProject,
			Title: "root", Tree: root, Phase: phase.Done, Generation: 1, Status: "done", DispatchStatus: "done", Rank: "A", LastDispatchSeq: 1})
		parent, waited := root, fixedNow.Add(-2*time.Hour)
		held := record.ChildrenWait{Start: phase.Implementing, Task: "Continue member. Issue: LEGION-209. Phase: implementing.",
			Reason: "a review asked for changes, while these Dispatch children of LEGION-209 were open: LEGION-210 (backlog)", Since: waited}
		putIssue(t, pool, record.Issue{Key: member, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: testProject,
			Title: "member", Tree: root, Parent: &parent, Phase: phase.AwaitingChildren, Generation: 1, Status: "in_progress",
			DispatchStatus: "in_progress", Rank: "B", LastDispatchSeq: 1, ChildrenWait: &held, ForwardMergeOwed: true, ChildrenWaitedAt: &waited})
		seedMirrorChild(t, pool, grandchild, "backlog", member)
		var tokens []claim.Token
		for _, role := range claim.Roles {
			tokens = append(tokens, putWorkerClaim(t, pool, root, member, role, 1, supervise.StateSuspended, supervise.SuspendedByDaemon))
		}

		apply(t, pool, admission, "readmit", intake.DispatchIssue{Key: root, ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated",
			Status: "todo", Title: "root", Rank: "A", HandedOver: true}, pnLingering())

		runBranchRows(t, pool, member)
		if starts := startsOf(t, pool, member); len(starts) != 0 {
			t.Fatalf("the starts of %s = %+v, want none: no role of a waiting parent is brought back", member, starts)
		}
		if got := issue(t, pool, member); got.Phase != phase.AwaitingChildren || !sameHeldStart(got.ChildrenWait, &held) {
			t.Fatalf("%s after the re-admission = phase %s, held start %+v; want it waiting still with the start it held", member, got.Phase, got.ChildrenWait)
		}
		if architectStarts(t, pool, root) != 1 {
			t.Fatalf("architect starts of %s = %d, want the promotion's one", root, architectStarts(t, pool, root))
		}
		states := claimStates(t, pool)
		for _, token := range tokens {
			if state := states[string(token)]; state != string(supervise.StateSuspended) {
				t.Fatalf("the claim %s = %q after the re-admission, want it suspended still", token, state)
			}
		}
	})

	// The held list of dispatch://INFRA-2964, on main: a waiting parent in a tree a person parked
	// reads its tree's reason, which the ladder reads before awaiting_children.
	t.Run("a waiting parent in a tree on the backlog reads its tree's reason in the held list", func(t *testing.T) {
		const root, member, grandchild = "LEGION-420", "LEGION-421", "LEGION-422"
		pool := migratedPool(t)
		admission := pnAdmission(t, pool, 2)
		putIssue(t, pool, record.Issue{Key: root, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: testProject,
			Title: root, Tree: root, Phase: phase.Admitted, Generation: 1, Status: "backlog", Rank: "A", HandedOver: true})
		seedLifecycle(t, pool, root)
		parent, waited := root, fixedNow.Add(-2*time.Hour)
		putIssue(t, pool, record.Issue{Key: member, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: testProject,
			Title: member, Tree: root, Parent: &parent, Phase: phase.AwaitingChildren, Generation: 1, Status: "in_progress", Rank: "M",
			HandedOver: true, ChildrenWait: &record.ChildrenWait{Start: phase.Implementing, Task: "Continue LEGION-421. Issue: LEGION-421. Phase: implementing.",
				Reason: "a review asked for changes, while these Dispatch children of LEGION-421 were open: LEGION-422 (backlog)", Since: waited},
			ForwardMergeOwed: true, ChildrenWaitedAt: &waited})
		seedMirrorChild(t, pool, member, "in_progress", root)
		seedMirrorChild(t, pool, grandchild, "backlog", member)

		state := pnState(t, pool, admission)

		if row, listed := pnHeldRow(state, member); !listed || row.Reason != record.HeldTreeParked || !slices.Equal(row.HeldBy, []string{root}) {
			t.Fatalf("admission.held names %s as %+v (listed %t), want tree_parked held by %s", member, row, listed, root)
		}
		pnInNeitherList(t, state, member)
	})

	// The stalled-tree measure of dispatch://INFRA-2560, on main, reads a parent in awaiting_children
	// as it reads a parent whose first phase waits on its children: a wait on a child filed and not
	// released is a wait by design while the project's line can move, and the line moves here
	// because a claim of another tree is at work.
	t.Run("a tree whose root waits only on a child filed and not released, quiet past the measure's bound, raises no stalled notice", func(t *testing.T) {
		const (
			root    = "LEGION-950"
			child   = "LEGION-951"
			working = "LEGION-960"
		)
		pool, admission := awaitingRun(t)
		seedMirrorChild(t, pool, child, "backlog", root)
		seedQuietRoot(t, pool, working, "B", phase.Implementing)
		seedMirror(t, pool, working, "in_progress")
		seedClaim(t, claimStore(t, pool), working, working, claim.RoleImplementer, supervise.StateWorking)
		for pass, roots := range waitPasses(t, pool, admission, nil, root, working) {
			if roots != nil {
				t.Fatalf("pass %d told the controller of %v, want nothing: %s waits by design on its child", pass+1, roots, root)
			}
		}
		if got := issue(t, pool, root); got.Phase != phase.AwaitingChildren {
			t.Fatalf("%s after the passes = %s, want it waiting still", root, got.Phase)
		}
	})
}
