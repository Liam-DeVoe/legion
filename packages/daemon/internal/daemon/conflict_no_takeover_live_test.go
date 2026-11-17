//go:build e2e

// The live proof's driver for the rule a phase move the daemon makes on its own follows — a head
// that starts conflicting with its base sends the issue back to implementing — it takes nobody's
// turn over. The start it makes queues the new phase's role its task at once, while the role whose
// phase ended is still working a turn it has not ended; nothing is sent to that role's connection
// to end its turn, and its process is neither stopped nor launched again; that role is told its
// phase is over by one message on its own Envoy role topic; and the new role's take of its task is
// accepted once that turn ends, since a take that adopts the working copy the issue's roles share
// waits for any turn of another role of the issue (supervise's take, hold issue-in-turn). A
// completion the old phase's role then reports from a turn of its own, once the issue has handed
// the phase out again, is refused HANDOFF_STALE_ENTRY, where the completion of the new phase entry,
// in the turn that entry's task starts, is taken. scripts/e2e/conflict-no-takeover.sh builds this
// driver with `-tags e2e` and owns the nats-server and the PostgreSQL it runs against.
//
// The daemon is the whole daemon: run, the function `legion start` calls, through this package's
// own whole-daemon harness (startDaemonWithin), so the proof exercises the real configuration, the
// real store and its migrations, the real HTTP API, the real claim supervision and its worker
// stream, the real openWorkflow, the real JetStream consumers and the real outbox lanes — the
// phase-ended lane (record.LaneRoleMessage) among them. Facts enter only as production's do:
// GitHub envelopes on the ENVOY_NOTIFICATIONS stream for the pushes, the synchronizes and the
// pushes onto the base; the daemon's own HTTP routes for each role's registration, readiness, task takes,
// grants and completions; and the daemon's worker stream for each role's turns, which this driver
// speaks as `legion worker-shim` does (shimwire frames), each role's plugin taking its task as the
// package's takeDriver does. Every assertion is a read of GET /legion/v1/state,
// of the operator's claim listing, of the value a role's worker reads through claims/task, of what
// the daemon published to the Envoy listener stand-in, of what it sent — or did not send — to a
// role's own connection, or of what the fake runtime was asked to launch, suspend and release.
// The store is read only for the one thing no route shows: whether the outbox still holds the
// phase-ended row of step 4.
//
// The RPC carries no abort frame, and the daemon has no quiesce of a claim and no abort of a
// connection, so the proof holds the stronger fact that leaves — that the role's connection is
// sent nothing at all while the start acts, and that the start queues its task while that role's
// claim is still `working` on a delivery it confirmed — rather than the absence of one frame type.
//
// What the proof stands in for lies outside the rule. The broker is a native nats-server the
// script starts and names in LEGION_E2E_NATS_URL. GitHub is the harness's REST stand-in for the
// one pull request the proof drives (liveGitHub, in live_issue_proof_test.go), whose base branch
// requires the check `ci` (conflictBranchRules). Dispatch is the board the package's whole-daemon
// live proofs share (liveDispatch, in live_proof_test.go). Envoy's listener is
// conflictListener, the package's noticeListener with the listener's own "no live session holds the
// role" refusal (notify.ErrNoHolder) added for the topics step 4 names, which noticeListener never
// gives. The two Apps' tokens are workflowTokenRecorder's placeholders. No agent runs: every claim
// is launched by the package's fake runtime, which says it provisions each workspace in the claim's
// own pod, as the Agent Sandbox runtime does, so the daemon clones nothing from GitHub, and each
// role the proof acts as is driven over the daemon's own worker stream and HTTP routes. The issue,
// its pull request, its slot and the implementer's claim are seeded through the store, as every
// daemon test seeds its fixture; what is driven is the change.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// conflictBase is the pull request's base branch, the repository's default, and ciRedCheck the one
// check its rulesets name — which this daemon never reads, and the stand-in answers only so a read
// nobody makes would still be served.
const (
	ciRedCheck   = "ci"
	conflictBase = "main"
)

// The three heads the proof drives: the implementer's seeded head, the fix it pushes for the
// tester's second assignment, and the fix it pushes while the reviewer reviews. Each red is
// settled at a head of its own, since a settlement that replays to the judgement already recorded
// changes nothing (classify.ApplyCheckSettlement).
const (
	conflictFirstHead  = "h1"
	conflictSecondHead = "h2"
	conflictThirdHead  = "h3"
)

// conflictNoTakeoverLive is one run of the proof: the booted daemon, the one issue it drives, the
// stand-ins the steps read the daemon's own writes back from, and the two roles the driver acts
// as over the worker stream.
type conflictNoTakeoverLive struct {
	liveIssueProof
	listener *conflictListener
	// imp and test are the implementer and the tester as their agents reach the daemon; reviewer
	// is the claim the tester's pass starts, which nothing registers, so its role topic is the one
	// with no live holder in step 5.
	imp, test *liveClaimant
	reviewer  claim.Token
}

// TestAConflictStartsTheNextRoleAtOnceAndTakesNoTurnOverLive drives the four steps of the proof
// against one booted daemon, in the order each step's state follows from the last: the tester
// working the assignment its phase handed it; the conflict that queues the implementer's task at
// once while that turn runs on untouched, tells the tester its phase ended, and whose task the
// implementer takes once that turn ends; the completion the tester reports from a turn of its own
// refused as of the entry before and the new assignment's taken; and last a conflict whose
// phase-ended message reaches a topic no session holds, which finishes the row and leaves the start
// acting all the same.
func TestAConflictStartsTheNextRoleAtOnceAndTakesNoTurnOverLive(t *testing.T) {
	r := newConflictNoTakeoverLive(t)
	r.step("step1-the-tester-works-the-assignment-its-phase-handed-it", r.testerWorksItsAssignment)
	r.step("step2-the-conflict-queues-the-implementers-task-and-takes-no-turn-over", r.conflictTakesNoTurnOver)
	r.step("step3-the-old-entrys-completion-is-refused-and-the-new-assignments-is-taken", r.staleEntryRefused)
	r.step("step4-a-phase-ended-message-no-session-holds-finishes-and-the-start-still-acts", r.noHolderFinishesTheRow)
}

// newConflictNoTakeoverLive boots the daemon over the script's broker with the issue already
// recorded: one root implementing on an open pull request whose base requires ci, holding its
// tree's admission slot, and its implementer's claim as a launch a previous daemon began and never
// recorded, which this boot relaunches. That last one is how the proof reaches a live implementer
// session, serving run 1, without an operator's spawn.
func newConflictNoTakeoverLive(t *testing.T) *conflictNoTakeoverLive {
	t.Helper()
	r := &conflictNoTakeoverLive{liveIssueProof: newLiveIssueProof(t, "scripts/e2e/conflict-no-takeover.sh",
		conflictBase, "the issue whose CI goes red")}
	r.serveGitHub(&liveGitHub{sha: conflictFirstHead, also: conflictBranchRules()})
	r.listener = newConflictListener(t, &r.liveProof)
	r.reviewer = r.token(claim.RoleReviewer)
	r.seed()
	r.boot(nil)
	return r
}

// seed records the issue as seedImplementing does, on an open pull request at its first head
// whose base requires ci. The implementer's phases row is left unwritten, as a daemon that minted
// no assignment for that phase would leave it, which is why its own completions are not held to
// one (api/handoff.go: either side empty is nothing to compare); every assignment the proof turns
// on is minted live, by the starts the two completions make.
func (r *conflictNoTakeoverLive) seed() {
	r.t.Helper()
	r.seedImplementing(conflictFirstHead)
	r.note("seeded: %s implementing on pull request #%d at %s, base %s requiring [%s], its implementer mid-launch serving run 1",
		r.key, r.number, conflictFirstHead, conflictBase, ciRedCheck)
}

// silent holds that the daemon sends this role's connection nothing at all for within: the RPC
// carries no abort frame, and nothing else it could send would end that role's turn either.
func (c *liveClaimant) silent(within time.Duration) {
	t := c.p.t
	t.Helper()
	if err := c.sh.conn.SetReadDeadline(time.Now().Add(within)); err != nil {
		t.Fatalf("set the read deadline on the %s's connection: %v", c.role, err)
	}
	line, err := c.sh.lines.ReadBytes('\n')
	var timeout net.Error
	switch {
	case errors.As(err, &timeout) && timeout.Timeout():
		return
	case err != nil:
		t.Fatalf("read the %s's connection: %v", c.role, err)
	default:
		t.Fatalf("the daemon sent the %s %q while its turn ran, want nothing at all", c.role, strings.TrimSpace(string(line)))
	}
}

// Step 1. The seeded implementer registers and takes the task its readiness left pending, as its
// plugin takes a value at once while it is idle, and in that task's turn completes implementing at
// its first head, naming the implement handoff record it wrote; that hands the testing phase to the
// tester with an assignment of its own and starts it. The tester registers, takes that task and
// works it: a claim working a delivery it confirmed, which is the turn the conflict of step 2 must
// leave alone. The implementer leaves this step holding nothing, so the task the conflict queues for
// it is the next one it is offered.
func (r *conflictNoTakeoverLive) testerWorksItsAssignment() {
	t := r.t
	r.imp = r.claimant(claim.RoleImplementer, "ses_implementer")
	resume := r.imp.takeTask("Phase: implementing")
	r.note("the implementer is registered and ready on the launch this boot made for the claim the fixture left mid-launch, and took the task its readiness left pending (%s)",
		firstLine(resume.Text))
	if status, failure := r.imp.complete("implemented it", conflictFirstHead, "", r.imp.handoff("implement")); status != http.StatusOK {
		t.Fatalf("the implementer's completion = %d %s %s, want 200", status, failure.Code, failure.Error)
	}
	r.imp.endTurn()
	r.note("drove POST /legion/v1/handoff/complete for the implementer at %s, then its agent_end and the turn's report: 200", conflictFirstHead)
	got := r.waitFor("the issue in testing", func(got api.Issue) bool { return got.Phase == phase.Testing })
	r.note("read back: %s is in %s, status %s", r.key, got.Phase, got.Status)

	r.test = r.claimant(claim.RoleTester, "ses_tester")
	task := r.test.takeTask("Phase: testing")
	r.note("read back: the tester was launched, registered and took its testing task (%s)", firstLine(task.Text))
	working := r.d.claim(r.test.token)
	if working.State != string(supervise.StateWorking) || working.Pending == nil || working.Pending.ConfirmedAt == nil {
		t.Fatalf("the tester is %s holding %+v, want working on a confirmed delivery of its testing task", working.State, working.Pending)
	}
	r.note("read back: the tester's claim is %s on delivery %s, phase %s, confirmed at %s",
		working.State, working.Pending.ID, working.Pending.Phase, working.Pending.ConfirmedAt.UTC().Format(time.RFC3339Nano))
}

// Step 2. A push onto the base turns the head the tester is testing conflicting with the base while
// the tester still works the turn it began in step 1, and the issue goes back to implementing. The
// step holds four facts of that move and nothing more:
//
//   - the conflict queues the implementer's fix task at once, in the drain that moved the phase,
//     while the tester's turn runs;
//   - the tester's turn, its process and its session run on untouched: its claim stays working on
//     the delivery it confirmed, in the same session at the same launch, its connection is sent
//     nothing at all, and the runtime is asked neither to stop nor to launch it, so what its turn
//     started in that process — a background job among it — runs on;
//   - the tester is told its phase ended, once, on its own Envoy role topic: the phase-ended row
//     (record.PhaseEnded) the move enqueued on the outbox's role-message lane, the conflict behind
//     the `Reason:` label that marks it off from the daemon's own sentences;
//   - the implementer's take of that task is accepted once the tester's turn ends. A take that
//     adopts the working copy the issue's roles share is refused while another role of the issue
//     is in a turn (supervise's take, hold issue-in-turn), so the tester ends its turn here, as an
//     agent told its phase is over does, and the implementer then takes, as behind-entry-fence's
//     implementer takes once the tester's turn ends.
func (r *conflictNoTakeoverLive) conflictTakesNoTurnOver() {
	t := r.t
	before := r.d.claim(r.test.token)
	if before.State != string(supervise.StateWorking) || before.Pending == nil || before.Locator == nil {
		t.Fatalf("the tester is %s holding %+v before the conflict, want working on a process: the proof turns on a turn that is running",
			before.State, before.Pending)
	}
	stopsBefore, launchesBefore := r.stopsOf(r.test.token), len(launchesOf(r.rt, r.test.token))
	drove := time.Now()
	r.driveConflict()
	r.note("drove the base of %s past %s, so GitHub answers its head conflicting, while the tester's turn runs", r.key, conflictFirstHead)

	// The conflict queues the implementer's fix task at once, while the tester's turn runs: read
	// on the implementer's claim, and as the value its worker reads through claims/task.
	want := "the head conflicts with " + conflictBase
	var queued api.OperatorClaim
	testwait.Eventually(t, "the implementer's fix task queued", func() bool {
		queued = r.d.claim(r.imp.token)
		return queued.Pending != nil && strings.Contains(queued.Pending.Task, want)
	})
	if queued.Pending.ConfirmedAt != nil {
		t.Fatalf("the implementer holds %+v, want its fix task queued and not yet taken", queued.Pending)
	}
	value, err := r.imp.worker.task()
	if err != nil || value.Task == nil {
		t.Fatalf("the implementer's worker read its task value as %+v, %v, want the value the queue wrote", value, err)
	}
	if got := r.issue(r.key); got.Phase != phase.Implementing {
		t.Fatalf("%s is in %s once the implementer's fix task is queued, want implementing", r.key, got.Phase)
	}
	if running := r.d.claim(r.test.token); running.State != string(supervise.StateWorking) || running.Pending == nil ||
		running.Pending.ID != before.Pending.ID {
		t.Fatalf("the tester is %s holding %+v when the implementer's task was queued, want it still working the delivery %s it confirmed",
			running.State, running.Pending, before.Pending.ID)
	}
	r.note("read back: %s is in implementing, and the implementer's fix task is queued as task %d and not yet taken (%s), while the tester is still working delivery %s",
		r.key, value.Task.Number, firstLine(queued.Pending.Task), before.Pending.ID)

	// The tester is told its phase ended, once, on its own role topic, with the reason the
	// architect's notice carries.
	topic := notify.RoleTopicPrefix + string(r.test.token)
	ended := record.PhaseEnded{Role: claim.RoleTester, From: phase.Testing, To: phase.Implementing,
		Reason: r.conflictReason(conflictFirstHead, drove)}.Message(r.key)
	var told []listenerPublish
	testwait.Eventually(t, "the phase-ended message on the tester's role topic", func() bool {
		told = r.notices.acceptedOn(topic)
		return len(told) > 0
	})
	if len(told) != 1 || told[0].Message != ended {
		t.Fatalf("the tester's topic %s took %+v, want the one message %q", topic, told, ended)
	}
	r.note("read back: one message on %s — %s", topic, told[0].Message)

	// The tester's turn, its process and its session run on untouched, and the message is not
	// told again. The process is the one the locator's incarnation names, which a stop and a
	// launch would each change.
	r.test.silent(liveSettle)
	after := r.d.claim(r.test.token)
	if after.State != string(supervise.StateWorking) || after.Pending == nil || after.Pending.ID != before.Pending.ID ||
		after.Session != before.Session || after.Generation != before.Generation || after.Locator == nil ||
		after.Locator.Incarnation != before.Locator.Incarnation {
		t.Fatalf("after the conflict settled the tester is %s holding %+v in session %s at generation %d on process %+v, want it still working delivery %s in session %s at generation %d on process %s",
			after.State, after.Pending, after.Session, after.Generation, after.Locator, before.Pending.ID, before.Session, before.Generation, before.Locator.Incarnation)
	}
	if stops, launches := r.stopsOf(r.test.token), len(launchesOf(r.rt, r.test.token)); stops != stopsBefore || launches != launchesBefore {
		t.Fatalf("since the conflict the runtime was asked to stop the tester %d times and to launch it %d times, want neither",
			stops-stopsBefore, launches-launchesBefore)
	}
	if again := r.notices.acceptedOn(topic); len(again) != 1 {
		t.Fatalf("the tester's topic took %d messages over %s, want the one", len(again), liveSettle)
	}
	r.note("read back over %s: the daemon sent the tester's connection nothing at all; its claim is still %s on delivery %s in session %s at generation %d on process %s; the runtime was asked neither to stop nor to launch it; and its topic took that one message and no other",
		liveSettle, after.State, after.Pending.ID, after.Session, after.Generation, after.Locator.Incarnation)

	// The implementer's take is accepted once the tester's turn ends.
	r.test.endTurn()
	r.note("the tester ended the turn it began in step 1")
	fix := r.imp.takeTask(want)
	if fix.Number != value.Task.Number {
		t.Fatalf("the implementer took task %d, want task %d, the one the conflict queued", fix.Number, value.Task.Number)
	}
	r.note("read back: once the tester's turn ended, the implementer's take of task %d was accepted (%s)", fix.Number, firstLine(fix.Text))
}

// conflictReason is the reason the workflow gave the conflict this proof drove at head (workflow's
// conflictReason), which the implementer's task, the architect's notice and the tester's
// phase-ended message all carry, read back from the architect's checks-red notice for that head.
// It names the base, GitHub's answer for the head — `mergeable: false` and no merge state, which
// is what the stand-in answers — and the moment that answer was in hand, which is the daemon's own
// clock and so lies between since, a moment before the proof drove the conflict, and now.
func (r *conflictNoTakeoverLive) conflictReason(head string, since time.Time) string {
	r.t.Helper()
	opening := "the head conflicts with " + conflictBase + ": GitHub answered mergeable=CONFLICTING with no mergeState for head " + head + " at "
	closing := ", and runs no checks on it; merge " + conflictBase + " forward"
	var reason string
	testwait.Eventually(r.t, "the architect's checks-red notice for head "+head, func() bool {
		for _, notice := range r.architectNotices(r.key, "checks-red") {
			if strings.HasPrefix(notice.Payload, opening) {
				reason = notice.Payload
				return true
			}
		}
		return false
	})
	stamp, closed := strings.CutSuffix(strings.TrimPrefix(reason, opening), closing)
	at, err := time.Parse(time.RFC3339, stamp)
	if !closed || err != nil || at.Before(since.Truncate(time.Second)) || at.After(time.Now()) {
		r.t.Fatalf("the architect's checks-red notice reads %q, want %q, then a read time between %s and now, then %q",
			reason, opening, since.UTC().Format(time.RFC3339), closing)
	}
	return reason
}

// stopsOf is how many times the runtime was asked to stop token's process: its suspensions and
// its releases, the two ways the daemon ends a role's process (launchesOf counts the other way).
func (r *conflictNoTakeoverLive) stopsOf(token claim.Token) int {
	n := 0
	for _, call := range r.rt.CallsOf("Suspend") {
		if call.Locator.Claim == token {
			n++
		}
	}
	for _, call := range r.rt.CallsOf("Release") {
		if call.Released.Claim == token {
			n++
		}
	}
	return n
}

// Step 3. The tester goes on working: it begins a turn of its own while the implementer works, as
// an agent that goes on working does, before it has taken any task of a later entry. The
// implementer merges the base forward and completes, which hands testing out again with a phase
// entry of its own. The tester's claim still serves the entry before it — the last task whose turn
// it ran is the first testing phase's — so the completion it reports from its own turn, its verdict
// on the head before the merge in a record that turn wrote, is refused HANDOFF_STALE_ENTRY and
// records nothing, and the refusal sends it to the new task's own work: the completion naming the
// record that task's turn writes, at the head the merge pushed, is taken.
func (r *conflictNoTakeoverLive) staleEntryRefused() {
	t := r.t
	r.test.startTurn()
	r.note("the tester began a turn of its own while the implementer works, before it has taken any task of a later entry")
	// The push is the merge forward the conflict asked for, so GitHub answers its head mergeable:
	// the read the implementer's completion makes then finds no conflict to send the issue back for.
	r.github.conflicts(false)
	r.push(conflictFirstHead, conflictSecondHead, "merge main forward")
	r.note("drove the implementer's merge forward: its push of %s onto %s, which GitHub answers mergeable, and GitHub's synchronize of #%d to it",
		conflictSecondHead, conflictFirstHead, r.number)
	if status, failure := r.imp.complete("merged main forward", conflictSecondHead, "", r.imp.handoff("implement")); status != http.StatusOK {
		t.Fatalf("the implementer's second completion = %d %s %s, want 200", status, failure.Code, failure.Error)
	}
	r.imp.endTurn()
	r.note("drove POST /legion/v1/handoff/complete for the implementer at %s, then its agent_end: 200", conflictSecondHead)
	got := r.waitFor("the issue back in testing", func(got api.Issue) bool { return got.Phase == phase.Testing })
	r.note("read back: %s is in %s again — a second testing phase, with a second assignment for the tester", r.key, got.Phase)

	// The tester's own turn has run throughout, and the tester still serves the assignment the
	// first testing phase handed it: its completion is of that assignment, not this one — the
	// verdict on the head that assignment named, in a handoff record that turn wrote.
	stale := r.d.claim(r.test.token)
	if stale.State != string(supervise.StateWorking) {
		t.Fatalf("the tester is %s, want it still working the turn of its own it began in this step", stale.State)
	}
	status, failure := r.test.complete("tested it", conflictFirstHead, "pass", r.test.handoff("test"))
	if status != http.StatusConflict || failure.Code != "HANDOFF_STALE_ENTRY" {
		t.Fatalf("the completion from the tester's own turn = %d %s %s, want 409 HANDOFF_STALE_ENTRY", status, failure.Code, failure.Error)
	}
	r.note("read back: the completion the tester reported from its own turn = 409 %s — %s", failure.Code, failure.Error)
	if after := r.issue(r.key); after.Phase != phase.Testing {
		t.Fatalf("%s is in %s after the refused completion, want testing: a refusal records nothing", r.key, after.Phase)
	}
	r.note("read back: %s is still in testing — the refusal recorded nothing", r.key)

	// The turn ends; the task of the new assignment, waiting for it, is taken. That task is work of
	// its own — the head the merge pushed — so the tester completes it with a handoff record it
	// writes for that work, at that head, never with the record or the head the refused completion
	// named, and it is taken.
	r.test.endTurn()
	task := r.test.takeTask("Phase: testing")
	r.note("read back: the tester's own turn ended and it took the second assignment's task (%s)", firstLine(task.Text))
	second := r.test.handoff("test")
	if status, failure := r.test.complete("tested the fix", conflictSecondHead, "pass", second); status != http.StatusOK {
		t.Fatalf("the completion from the tester's new turn = %d %s %s, want 200", status, failure.Code, failure.Error)
	}
	r.test.endTurn()
	moved := r.waitFor("the issue in reviewing", func(got api.Issue) bool { return got.Phase == phase.Reviewing })
	r.note("read back: the completion naming the second assignment's own handoff record (%s, at %s), in the turn its task started, was taken: %s is in %s",
		second, conflictSecondHead, r.key, moved.Phase)
}

// Step 4. The reviewer's claim was started by the tester's pass and nothing registered it, so no
// session holds its role topic. A conflict in reviewing ends the reviewer's phase: the phase-ended
// row is published to a topic the listener answers "no live session holds the role"
// (notify.ErrNoHolder), which finishes the row rather than retrying it for ever, and the start acts
// all the same — the implementer takes the new fix task, no other role of the issue being in a turn.
func (r *conflictNoTakeoverLive) noHolderFinishesTheRow() {
	t := r.t
	topic := notify.RoleTopicPrefix + string(r.reviewer)
	r.listener.noHolder(topic)
	r.awaitLaunch(r.reviewer)
	r.note("the reviewer's claim was launched by the tester's pass and never registered; its topic %s now answers 404 unclaimed, as the listener answers for a role no live session holds", topic)

	r.push(conflictSecondHead, conflictThirdHead, "fix the red")
	r.driveConflict()
	r.note("drove the base of %s past %s, so GitHub answers its head conflicting, while the issue is in reviewing", r.key, conflictThirdHead)

	fix := r.imp.takeTask("the head conflicts with " + conflictBase)
	r.note("read back: the start still acted — the implementer took its fix task (%s)", firstLine(fix.Text))
	if got := r.issue(r.key); got.Phase != phase.Implementing {
		t.Fatalf("%s is in %s after the red in reviewing, want implementing", r.key, got.Phase)
	}
	refused := r.listener.refusedNoHolder(topic)
	if len(refused) != 1 {
		t.Fatalf("the listener refused %d publishes to %s for want of a holder, want the phase-ended row's one", len(refused), topic)
	}
	r.note("read back: the listener answered the phase-ended publish to %s 404 unclaimed — %s", topic, refused[0].Message)
	// The row is finished, not retried: nothing of its kind is left in the outbox, and no second
	// attempt comes over the window a retry's first backoff (one second) falls well inside.
	testwait.Eventually(t, "the phase-ended row finished", func() bool { return r.outboxRows("phase_ended") == 0 })
	time.Sleep(liveSettle)
	if again := r.listener.refusedNoHolder(topic); len(again) != 1 {
		t.Fatalf("the listener was asked %d times over %s, want the one attempt: the row is finished, not retried", len(again), liveSettle)
	}
	if rows := r.outboxRows("phase_ended"); rows != 0 {
		t.Fatalf("the outbox holds %d phase_ended rows of %s, want none: the row finished undelivered", rows, r.key)
	}
	r.note("read back over %s: one publish attempt, and the outbox holds no phase_ended row of %s — the row finished undelivered",
		liveSettle, r.key)
	if accepted := r.notices.acceptedOn(topic); len(accepted) != 0 {
		t.Fatalf("the listener took %d messages on %s, want none: no session holds it", len(accepted), topic)
	}
	r.note("read back: nothing was delivered on %s", topic)
}

// outboxRows is how many rows of kind the outbox holds for the proof's issue. A finished row is
// deleted (record.Postgres.FinishOutbox), and no route shows the table.
func (r *conflictNoTakeoverLive) outboxRows(kind string) int {
	r.t.Helper()
	var rows int
	if err := r.st.Pool().QueryRow(context.Background(),
		"select count(*) from outbox where issue = $1 and kind = $2", r.key, kind).Scan(&rows); err != nil {
		r.t.Fatalf("read the outbox rows of %s: %v", r.key, err)
	}
	return rows
}

// driveConflict makes GitHub answer the pull request's head as conflicting with its base and
// publishes the push onto that base that asks the daemon to read it (workflow's basePush, which
// queues the merge-state read askMergeStateFor). The read applies the
// intake.PullRequestMergeability the daemon makes of GitHub's answer, and a conflicting head is
// the one thing a pull request's own state still moves an issue for (workflow's mergeability).
func (r *conflictNoTakeoverLive) driveConflict() {
	r.t.Helper()
	r.github.conflicts(true)
	r.publishBasePush()
}

// conflictListener is the Envoy listener as this proof's daemon reaches it: the package's
// noticeListener, which takes every publish, with the one answer it does not give — the 404 the
// listener answers for a role topic no live session holds (writeRoleHolderError,
// packages/envoy/cmd/listener/api.go, decoded by notify's ErrNoHolder) — added for the topics a
// step names. Everything else, the architect's notices and the tester's own message included, goes
// to noticeListener, so the harness's reads of it (architectNotices) are the reads of this one.
type conflictListener struct {
	t        *testing.T
	notices  *noticeListener
	mu       sync.Mutex
	unheld   map[string]bool
	refusals map[string][]listenerPublish
}

// newConflictListener puts this listener in front of the proof's noticeListener and points the
// configuration the daemon boots with at it.
func newConflictListener(t *testing.T, proof *liveProof) *conflictListener {
	t.Helper()
	l := &conflictListener{t: t, notices: proof.notices, unheld: map[string]bool{}, refusals: map[string][]listenerPublish{}}
	server := httptest.NewServer(l)
	t.Cleanup(server.Close)
	proof.cfg.EnvoyURL = server.URL
	return l
}

// noHolder makes the listener answer every later publish to topic as it answers one to a role no
// live session holds.
func (l *conflictListener) noHolder(topic string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.unheld[topic] = true
}

// refusedNoHolder is every publish the listener refused on topic for want of a holder, oldest
// first.
func (l *conflictListener) refusedNoHolder(topic string) []listenerPublish {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]listenerPublish(nil), l.refusals[topic]...)
}

func (l *conflictListener) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost || req.URL.Path != "/v1/messages/publish" {
		l.notices.ServeHTTP(w, req)
		return
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		l.t.Errorf("read the publish the daemon sent: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var published listenerPublish
	if err := json.Unmarshal(body, &published); err != nil {
		l.t.Errorf("decode the publish %s: %v", body, err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	l.mu.Lock()
	unheld := l.unheld[published.Topic]
	if unheld {
		l.refusals[published.Topic] = append(l.refusals[published.Topic], published)
	}
	l.mu.Unlock()
	if !unheld {
		req.Body = io.NopCloser(strings.NewReader(string(body)))
		l.notices.ServeHTTP(w, req)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	if err := json.NewEncoder(w).Encode(map[string]string{
		"error": "no live session holds the role", "reason": "unclaimed",
	}); err != nil {
		l.t.Errorf("write the listener's no-holder refusal: %v", err)
	}
}

// conflictBranchRules is what this proof's GitHub answers beyond the harness's stand-in
// (liveGitHub's also): the base branch's rulesets requiring ci (requiredchecks.Required's
// /rules/branches/<base>) and the branch itself as unprotected (its /branches/<base>, which a 404
// would make an error that keeps the set last read).
func conflictBranchRules() map[string]any {
	return map[string]any{
		"/rules/branches/" + conflictBase: []map[string]any{
			{"type": "pull_request"},
			{"type": "required_status_checks", "parameters": map[string]any{
				"required_status_checks": []map[string]string{{"context": ciRedCheck}},
			}},
		},
		"/branches/" + conflictBase: map[string]any{"name": conflictBase, "protected": false},
	}
}
