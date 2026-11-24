package daemon

// A merge forward asked of a role stays that role's work until it lands, whatever ends the role's
// claim on the way. The workflow hears no fact of two of those ends: an operator's stop and the
// agent's report of its own exit each retire the claim (supervise's stop and exit), and a row of
// the ask queued for a retired claim ends undelivered (deliverHeld). So the ask is sent again at
// the ready of the role's next launch, a launch later than the one it was last sent at
// (record.MergeForward.SentLaunch). These tests end the implementer's claim by each cause through
// its supervise machine, as the daemon does, and relaunch it through the operator's retry.

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// owedAskHead is the head of the pull request these tests hold an ask of.
const owedAskHead = "sha-1"

// newOwedAskRig is freshSessionRig's issue moved on into testing while the implementer's session
// of its first launch still runs: its pull request #86 is open at owedAskHead onto main, and the
// record holds the implementer's ask, made in testing, to merge main forward onto it, so the
// tester is held. The ask's row was delivered to that launch, which the record keeps, and left the
// outbox, as a delivered row does.
func newOwedAskRig(t *testing.T) *freshSessionRig {
	t.Helper()
	rig := newFreshSessionRig(t)
	rig.issue.Phase, rig.issue.Status = phase.Testing, "testing"
	asked := &record.MergeForward{Head: owedAskHead, Base: "main", Role: claim.RoleImplementer, Phase: phase.Testing,
		Why: record.ConflictCheckNotGreen, AskedAt: time.Now(), SentLaunch: rig.machine.Claim().Generation}
	if err := store.Transact(context.Background(), rig.pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := rig.records.PutIssue(ctx, tx, rig.issue); err != nil {
			return err
		}
		return rig.records.PutPullRequest(ctx, tx, record.PullRequest{State: record.PullRequestOpen, Issue: rig.issue.Key,
			Repo: "acme/widgets", Number: 86, Branch: "legion/" + rig.issue.Key, HeadSHA: owedAskHead, HeadUpdatedAt: time.Now(),
			Failing: []string{}, CheckRuns: []record.AttemptRun{}, Base: "main", MergeForward: asked})
	}); err != nil {
		t.Fatalf("move %s into testing with the implementer's ask: %v", rig.issue.Key, err)
	}
	return rig
}

// owedAsk is the implementer's ask of owedAskHead on #86 in testing, as the act queues it.
var owedAsk = record.MergeForwardMessage{Role: claim.RoleImplementer, Step: record.MergeForwardAsk, Head: owedAskHead, Base: "main",
	Phase: phase.Testing, PullRequest: 86, Why: record.ConflictCheckNotGreen}

// wokenByTheAsk is mergeForwardRunner's LEGION-208 in testing with the implementer's ask of
// owedAskHead owed and its row queued, the implementer's claim as an idle release left it, and the
// workflow behind the supervisor, so each launch's ready is applied as the daemon applies it
// (workflowRuntime.terminal). The ask's first attempt has run: it woke the implementer, whose new
// launch is coming up, and published nothing, since no session holds the role yet.
func wokenByTheAsk(t *testing.T) (*outbox, *store.Pool, *holderPublisher, *supervise.Machine) {
	t.Helper()
	publisher := &holderPublisher{}
	asked := &record.MergeForward{Head: owedAskHead, Base: "main", Role: claim.RoleImplementer, Phase: phase.Testing,
		Why: record.ConflictCheckNotGreen, AskedAt: time.Now()}
	runner, sup, pool, _ := mergeForwardRunner(t, publisher, asked)
	engine := workflow.New(runner.records, workflow.Config{Project: "legion", Repositories: testRepositories}, quietLogger())
	sup.OnDurableState((&workflowRuntime{pool: pool, records: runner.records, handlers: []intake.Handler{engine}, log: quietLogger()}).terminal)
	token := mustClaimToken(t, "LEGION-208", claim.RoleImplementer)
	publisher.setAbsent(notify.RoleTopicPrefix + string(token))
	machine := releasedClaim(t, sup, claim.RoleImplementer)
	sup.deps.Conns.(*fake.Conns).Register(token, fake.NewConn())
	enqueueOutbox(t, pool, runner.records, mustOutboxRow(t, "LEGION-208", owedAsk, time.Now().Add(-time.Minute)))
	drainAt(t, runner, 0, "the ask's first attempt")
	machine.Wait()
	if held := machine.Claim(); held.Released() || held.Generation < 2 {
		t.Fatalf("after the ask's first attempt the implementer is %s at launch %d, want it woken into a new launch", held.State, held.Generation)
	}
	return runner, pool, publisher, machine
}

// comesUp drives machine's current launch through its shim's hello and its agent's registration,
// and its ready too where ready is set.
func comesUp(t *testing.T, machine *supervise.Machine, ready bool) {
	t.Helper()
	c := machine.Claim()
	events := []supervise.Event{
		supervise.StreamHello{Claim: c.Token, Generation: c.Generation},
		supervise.RequestRegister{Claim: c.Token, Generation: c.Generation, Session: c.Session, SessionFile: c.SessionFile},
	}
	if ready {
		events = append(events, supervise.RequestReady{Claim: c.Token, Generation: c.Generation, Session: c.Session})
	}
	for _, ev := range events {
		if err := machine.Handle(context.Background(), ev); err != nil {
			t.Fatalf("bring up the implementer's launch %d: handle %T: %v", c.Generation, ev, err)
		}
	}
	machine.Wait()
}

// askToldOnce fails the test unless the implementer was told the ask once in all, the outbox holds no
// merge-forward row of LEGION-208 any more, and the record's ask keeps launch as the one it was
// delivered to.
func askToldOnce(t *testing.T, pool *store.Pool, publisher *holderPublisher, launch uint64) {
	t.Helper()
	_, delivered := publisher.snapshot()
	if len(delivered) != 1 || delivered[0].message != owedAsk.Message("LEGION-208", record.PullRequest{Repo: "acme/widgets"}) {
		t.Errorf("the implementer was told %d messages (%+v), want the ask once", len(delivered), delivered)
	}
	if rows := outboxRowsOfKind(t, pool, "LEGION-208", record.OutboxKindMergeForward); rows != 0 {
		t.Errorf("the outbox holds %d merge-forward rows of LEGION-208, want none: no second ask", rows)
	}
	var sent uint64
	if err := pool.QueryRow(context.Background(), `select coalesce((merge_forward->>'sentLaunch')::bigint, 0) from pull_requests where issue = 'LEGION-208'`).
		Scan(&sent); err != nil {
		t.Fatalf("read the record's ask: %v", err)
	}
	if sent != launch {
		t.Errorf("the record's ask was sent at launch %d, want launch %d, the one its row reached", sent, launch)
	}
}

// The ask's own row wakes the released implementer, and the woken launch comes ready while the row
// is still queued: the row reaches that launch by its own delivery, so its ready writes no second
// ask, and the implementer is told the ask once.
func TestAnAskWhoseRowWakesItsRoleIsToldOnce(t *testing.T) {
	t.Parallel()
	runner, pool, publisher, machine := wokenByTheAsk(t)
	comesUp(t, machine, true)
	publisher.setAbsent()
	drainAt(t, runner, 10*time.Minute, "the attempt once the woken session holds the role")
	drainAt(t, runner, 20*time.Minute, "a later attempt")
	askToldOnce(t, pool, publisher, machine.Claim().Generation)
}

// The ask's row reaches the woken launch before that launch's ready: the delivery records the
// launch on the record's ask, so the ready of the same launch sends nothing more.
func TestTheReadyOfTheLaunchAnAskWasDeliveredToSendsNothing(t *testing.T) {
	t.Parallel()
	runner, pool, publisher, machine := wokenByTheAsk(t)
	comesUp(t, machine, false)
	publisher.setAbsent()
	drainAt(t, runner, 10*time.Minute, "the attempt once the woken session holds the role")
	launch := machine.Claim().Generation
	if err := machine.Handle(context.Background(), supervise.RequestReady{Claim: machine.Claim().Token, Generation: launch,
		Session: machine.Claim().Session}); err != nil {
		t.Fatalf("the implementer's ready: %v", err)
	}
	machine.Wait()
	drainAt(t, runner, 20*time.Minute, "a later attempt")
	askToldOnce(t, pool, publisher, launch)
}

// An operator's stop of the implementer, asked to merge the base forward while the issue stays in
// testing, retires its claim: the stop moves no issue, the merge stays owed to the implementer and
// the tester stays held. The operator's retry relaunches the implementer, and the ready of that
// launch sends the ask once.
func TestAnAskOwedToAStoppedClaimIsSentAgainAtItsRelaunch(t *testing.T) {
	t.Parallel()
	rig := newOwedAskRig(t)
	if recorder := rig.operator(http.MethodPost, "/legion/v1/operator/claims/"+string(rig.implementer)+"/stop", nil); recorder.Code != http.StatusOK {
		t.Fatalf("the operator's stop = %d, want 200; body %s", recorder.Code, recorder.Body)
	}
	rig.relaunchSendsTheOwedAskOnce("the operator's stop")
}

// The implementer, asked to merge the base forward while the issue stays in testing, reports its
// own exit, which retires its claim: the merge stays owed to the implementer and the tester stays
// held. The operator's retry relaunches the implementer, and the ready of that launch sends the
// ask once.
func TestAnAskOwedToAClaimThatExitedIsSentAgainAtItsRelaunch(t *testing.T) {
	t.Parallel()
	rig := newOwedAskRig(t)
	rig.must(supervise.RequestExit{Claim: rig.implementer, Generation: rig.machine.Claim().Generation, Session: oldSession,
		Reason: "the agent ended its session"})
	rig.relaunchSendsTheOwedAskOnce("the implementer's exit")
}

// relaunchSendsTheOwedAskOnce is what follows an end of the implementer's claim, named by ended,
// while the ask is owed: the claim is retired and the end queued no ask, the issue is still in
// testing and the tester still held; then the operator's retry relaunches the implementer, the
// ready of its new launch queues the ask once and records that launch on the record's ask, and
// the tester is still held.
func (r *freshSessionRig) relaunchSendsTheOwedAskOnce(ended string) {
	r.t.Helper()
	retired := r.machine.Claim()
	if retired.State != supervise.StateRetired {
		r.t.Fatalf("after %s the implementer is %s, want retired", ended, retired.State)
	}
	if current, from := r.recordedPhase(); current != phase.Testing || from != "" {
		r.t.Fatalf("after %s the issue is in %s held from %q, want testing and not held", ended, current, from)
	}
	if asks := r.queuedMergeForwards(); len(asks) != 0 {
		r.t.Fatalf("after %s the outbox holds the merge-forward messages %+v, want none", ended, asks)
	}
	r.testerHeld("after " + ended)

	if recorder := r.operator(http.MethodPost, "/legion/v1/operator/claims/"+string(r.implementer)+"/retry", nil); recorder.Code != http.StatusOK &&
		recorder.Code != http.StatusAccepted {
		r.t.Fatalf("the operator's retry of the implementer = %d, want 200 or 202; body %s", recorder.Code, recorder.Body)
	}
	r.machine.Wait()
	if got := r.machine.Claim(); got.State != supervise.StateLaunching || got.Generation <= retired.Generation {
		r.t.Fatalf("after the retry the implementer is %s at launch %d, want it launching past launch %d", got.State, got.Generation, retired.Generation)
	}
	r.register(oldSession, oldSessionFile)
	launch := r.machine.Claim().Generation

	want := []record.MergeForwardMessage{{Role: claim.RoleImplementer, Step: record.MergeForwardAsk, Head: owedAskHead, Base: "main",
		Phase: phase.Testing, PullRequest: 86, Why: record.ConflictCheckNotGreen}}
	if asks := r.queuedMergeForwards(); !slices.Equal(asks, want) {
		r.t.Errorf("after the ready of the implementer's launch %d the outbox holds the merge-forward messages %+v, want %+v", launch, asks, want)
	}
	if asked := r.recordedAsk(); asked.SentLaunch != launch {
		r.t.Errorf("after the ready of the implementer's launch %d the record's ask is %+v, want it sent at that launch", launch, asked)
	}
	r.testerHeld("after the relaunch")
}

// queuedMergeForwards is every merge-forward message the outbox holds for the rig's issue, oldest
// first, each as the outbox decodes it.
func (r *freshSessionRig) queuedMergeForwards() []record.MergeForwardMessage {
	r.t.Helper()
	rows, err := r.pool.Query(context.Background(), `select id, kind, issue, payload from outbox where kind = $1 and issue = $2 order by id`,
		string(record.OutboxKindMergeForward), r.issue.Key)
	if err != nil {
		r.t.Fatalf("read the merge-forward rows: %v", err)
	}
	defer rows.Close()
	var messages []record.MergeForwardMessage
	for rows.Next() {
		var row record.OutboxRow
		if err := rows.Scan(&row.ID, &row.Kind, &row.Issue, &row.Payload); err != nil {
			r.t.Fatalf("scan a merge-forward row: %v", err)
		}
		payload, err := record.DecodeOutboxPayload(row)
		if err != nil {
			r.t.Fatalf("decode merge-forward row %d: %v", row.ID, err)
		}
		messages = append(messages, payload.(record.MergeForwardMessage))
	}
	if err := rows.Err(); err != nil {
		r.t.Fatalf("read the merge-forward rows: %v", err)
	}
	return messages
}

// recordedAsk is the merge forward the record holds for the rig's issue's pull request.
func (r *freshSessionRig) recordedAsk() record.MergeForward {
	r.t.Helper()
	var pr *record.PullRequest
	if err := store.Transact(context.Background(), r.pool, func(ctx context.Context, tx pgx.Tx) (err error) {
		pr, err = r.records.PullRequest(ctx, tx, r.issue.Key)
		return err
	}); err != nil {
		r.t.Fatalf("read the pull request of %s: %v", r.issue.Key, err)
	}
	if pr == nil || pr.MergeForward == nil {
		r.t.Fatalf("the record holds the pull request %+v of %s, want one holding a merge forward", pr, r.issue.Key)
	}
	return *pr.MergeForward
}

// testerHeld fails the test unless the merge of owedAskHead is still owed where the issue stands,
// at when: the tester's hold is still one the executor delivers (workflow.MergeForwardAt, the rule
// it reads at each attempt of a row), and the record's ask has neither landed nor been withdrawn.
func (r *freshSessionRig) testerHeld(when string) {
	r.t.Helper()
	var left string
	if err := store.Transact(context.Background(), r.pool, func(ctx context.Context, tx pgx.Tx) (err error) {
		_, _, left, err = workflow.MergeForwardAt(ctx, r.records, tx, r.issue.Key, claim.RoleTester, record.MergeForwardHold, owedAskHead)
		return err
	}); err != nil {
		r.t.Fatalf("read the rule of the tester's hold: %v", err)
	}
	if left != "" {
		r.t.Errorf("%s the tester's hold of %s is no longer delivered (%s), want the tester held", when, owedAskHead, left)
	}
	if asked := r.recordedAsk(); asked.Landed != "" || asked.Withdrawn || asked.Asked() != claim.RoleImplementer {
		r.t.Errorf("%s the record's ask is %+v, want the implementer's, neither landed nor withdrawn", when, asked)
	}
}
