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
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// owedAskHead is the head of the pull request these tests hold an ask of.
const owedAskHead = "sha-1"

// newOwedAskRig is freshSessionRig's issue moved on into testing while the implementer's session
// of its first launch still runs: its pull request #86 is open at owedAskHead onto main, and the
// record holds the implementer's ask, made in testing, to merge main forward onto it, so the
// tester is held. The ask was written after that launch's ready, as the act writes it while the
// claim runs, so no ready has sent it.
func newOwedAskRig(t *testing.T) *freshSessionRig {
	t.Helper()
	rig := newFreshSessionRig(t)
	rig.issue.Phase, rig.issue.Status = phase.Testing, "testing"
	asked := &record.MergeForward{Head: owedAskHead, Base: "main", Role: claim.RoleImplementer, Phase: phase.Testing,
		Why: record.ConflictCheckNotGreen, AskedAt: time.Now()}
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
