// The live proof's driver for what a head that starts conflicting with its base does while a role
// of its issue is in a turn: the issue goes back to implementing at once, and nobody's turn is
// taken over. GitHub sends no event when a head starts to conflict, so what finds the conflict is
// a read of the pull request's merge state, and a push onto the base while the tester's turn runs
// is what asks the daemon for that read (workflow's basePush, which asks for it wherever the issue
// stands in a phase a conflict sends back to implementing: askMergeStateFor, conflictReadPhase).
// GitHub's answer that the head conflicts moves the issue back to implementing while the tester's
// turn runs on. The tester keeps its delivery, its session and its turn: nothing is sent to its
// connection to end that turn, its process is not suspended, and it is told its phase is over by
// one message on its own Envoy role topic. The implementer's task is queued on its claim at once
// and waits there unsent until the tester's turn ends, because the daemon sends no role a task
// that adopts the working copy the issue's roles share while another role of the issue is in a
// turn (supervise's sendPending). Once the implementer has merged the base forward and handed
// testing out again, a completion of the tester's first entry, reported from a turn that began
// before the new entry's task reached it, is refused HANDOFF_STALE_ENTRY, where the completion of
// the new entry, in the turn that entry's task starts, is taken. Last, a conflict in reviewing
// whose phase-ended message goes to a topic no session holds finishes that message's row and
// sends the implementer its task all the same. Every completion names a handoff record of its own
// phase and the head it is of, as a worker's does.
//
// The test runs by name in CI's daemon-live job (.github/workflows/ci-job-daemon-live.yaml), on
// the job's PostgreSQL and the package's test NATS server, with the job's git and jj 0.38 or later,
// which the daemon's boot refuses to start without. Each step prints `== <step>`, what it drove
// and what it read back, and `ok <step>`, so the job's log is the proof's own record.
//
// The daemon is the whole daemon: run, the function `legion start` calls, through this package's
// own whole-daemon harness (startDaemonWithin), so the proof exercises the real configuration, the
// real store and its migrations, the real HTTP API, the real claim supervision and its worker
// stream, the real openWorkflow, the real JetStream consumers, the real merge-state reads and the
// real outbox lanes — the phase-ended lane (record.LaneRoleMessage) among them. Facts enter only
// as production's do: GitHub envelopes on the ENVOY_NOTIFICATIONS stream for the pushes onto the
// base, the implementer's push and its synchronize; the daemon's own HTTP routes for each role's
// registration, readiness, grants and completions; and the daemon's worker stream for each role's
// turns, which this driver speaks as `legion worker-shim` does (shimwire frames). Every assertion
// is a read of GET /legion/v1/state, of the operator's claim listing, of what the daemon published
// to the Envoy listener stand-in, of what it sent — or did not send — to a role's own connection,
// or of what the fake runtime was asked to launch and suspend. The store is read only for the one
// thing no route shows: whether the outbox still holds the phase-ended row of step 6.
//
// The RPC carries no abort frame, and the daemon has no quiesce of a claim and no abort of a
// connection, so the proof holds the stronger fact that leaves — that the tester's connection is
// sent nothing at all while the issue leaves its phase, and that the issue leaves it while the
// tester's claim is still `working` on a delivery it confirmed — rather than the absence of one
// frame type.
//
// What the proof stands in for lies outside the rule. The broker is the NATS server the package's
// other tests take (workflowNATS, through testnats). GitHub is the harness's REST stand-in for the
// one pull request the proof drives (liveGitHub, in live_issue_proof_test.go), whose base branch
// requires the check `ci` (conflictBranchRules), and whose `mergeable` the driver sets
// (driveConflict, liveGitHub's conflicts). Dispatch is the board the package's whole-daemon live
// proofs share (liveDispatch, in live_proof_test.go), which holds the handoff record each
// completion names. Envoy's listener is conflictListener, the package's noticeListener with the
// listener's own "no live session holds the role" refusal (notify.ErrNoHolder) added for the
// topics step 6 names, which noticeListener never gives. The two Apps' tokens are
// workflowTokenRecorder's placeholders. No agent runs: every claim is launched by the package's
// fake runtime, which says it provisions each workspace in the claim's own pod, as the Agent
// Sandbox runtime does, so the daemon clones nothing from GitHub, and each role the proof acts as
// is driven over the daemon's own worker stream and HTTP routes. The issue, its pull request, its
// slot and the implementer's claim are seeded through the store, as every daemon test seeds its
// fixture; what is driven is the change.
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

// conflictReason is the reason the workflow gives a conflicting head, which the implementer's
// task, the architect's notice and the tester's phase-ended message all carry (workflow's
// mergeability).
const conflictReason = "the head conflicts with " + conflictBase + ": GitHub runs no checks on it; merge " + conflictBase + " forward"

// The two heads the proof drives: the implementer's seeded head, which the tester's first entry
// tests and the push onto the base of step 2 leaves conflicting, and the head the implementer
// pushes once it has merged the base forward, which the tester's second entry tests and the push
// onto the base of step 6 leaves conflicting in its turn. Every completion names one of them.
const (
	conflictFirstHead  = "h1"
	conflictSecondHead = "h2"
)

// conflictNoTakeoverLive is one run of the proof: the booted daemon, the one issue it drives, the
// stand-ins the steps read the daemon's own writes back from, and the two roles the driver acts
// as over the worker stream.
type conflictNoTakeoverLive struct {
	liveIssueProof
	listener *conflictListener
	// imp and test are the implementer and the tester as their agents reach the daemon; reviewer
	// is the claim the tester's pass starts, which nothing registers, so its role topic is the one
	// with no live holder in step 6.
	imp, test *liveClaimant
	reviewer  claim.Token
	// served is the testing entry whose task the tester took in step 1, and queued the delivery
	// of the implementer's task the conflict of step 2 queued, which step 4 reads it take.
	served int64
	queued string
}

// TestAConflictStartsTheNextRoleAtOnceAndTakesNoTurnOverLive drives the six steps of the proof
// against one booted daemon, in the order each step's state follows from the last: the tester
// working the entry its phase handed it, the push onto the base that makes the head conflict and
// sends the issue back to implementing while that turn runs on, the message that tells the tester
// its phase ended, the implementer taking its task once the tester's turn ends, the first entry's
// completion refused and the second entry's taken, and last a conflict in reviewing whose
// phase-ended message reaches a topic no session holds, which finishes the row and leaves the
// start acting all the same.
func TestAConflictStartsTheNextRoleAtOnceAndTakesNoTurnOverLive(t *testing.T) {
	r := newConflictNoTakeoverLive(t)
	r.step("step1-the-tester-works-the-entry-its-phase-handed-it", r.testerWorksItsEntry)
	r.step("step2-a-push-onto-the-base-sends-the-issue-back-to-implementing-while-the-testers-turn-runs-on", r.conflictMovesAtOnce)
	r.step("step3-the-tester-is-told-its-phase-ended-on-its-own-role-topic", r.testerIsToldItsPhaseEnded)
	r.step("step4-the-implementer-takes-its-task-once-the-testers-turn-ends", r.implementerTakesItsTaskAfterTheTurn)
	r.step("step5-the-first-entrys-completion-is-refused-and-the-second-entrys-is-taken", r.staleEntryRefused)
	r.step("step6-a-phase-ended-message-no-session-holds-finishes-and-the-start-still-acts", r.noHolderFinishesTheRow)
}

// newConflictNoTakeoverLive boots the daemon over the package's test broker with the issue already
// recorded: one root implementing on an open pull request whose base requires ci, holding its
// tree's admission slot, and its implementer's claim as a launch a previous daemon began and never
// recorded, which this boot relaunches. That last one is how the proof reaches a live implementer
// session, serving run 1, without an operator's spawn.
func newConflictNoTakeoverLive(t *testing.T) *conflictNoTakeoverLive {
	t.Helper()
	r := &conflictNoTakeoverLive{liveIssueProof: newLiveIssueProof(t,
		conflictBase, "the issue whose head starts conflicting with its base")}
	r.serveGitHub(&liveGitHub{sha: conflictFirstHead, also: conflictBranchRules()})
	r.listener = newConflictListener(t, &r.liveProof)
	r.reviewer = r.token(claim.RoleReviewer)
	r.seed()
	r.boot(nil)
	return r
}

// seed records the issue as seedImplementing does, on an open pull request at its first head
// whose base requires ci. No phases row is seeded: every entry the proof turns on is raised live,
// by the moves the completions and the conflicts make.
func (r *conflictNoTakeoverLive) seed() {
	r.t.Helper()
	r.seedImplementing(conflictFirstHead)
	r.note("seeded: %s implementing on pull request #%d at %s, base %s requiring [%s], its implementer mid-launch serving run 1",
		r.key, r.number, conflictFirstHead, conflictBase, ciRedCheck)
}

// silent holds that the daemon sends this role's connection nothing at all for within: the RPC
// carries no abort frame, and nothing else it could send would end that role's turn or start one.
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
		t.Fatalf("the daemon sent the %s %q, want nothing at all", c.role, strings.TrimSpace(string(line)))
	}
}

// Step 1. The seeded implementer completes implementing at the first head, naming its handoff
// record, which hands the testing phase to the tester with an entry of its own and starts it. The
// tester registers, takes that entry's task and works it: a claim working a delivery it
// confirmed, which is the turn the conflict of step 2 must leave alone.
func (r *conflictNoTakeoverLive) testerWorksItsEntry() {
	t := r.t
	r.imp = r.claimant(claim.RoleImplementer, "ses_implementer")
	r.note("the implementer is registered and ready on the launch this boot made for the claim the fixture left mid-launch")
	r.imp.startTurn("")
	handoff := r.imp.handoff("implement")
	if status, failure := r.imp.complete("implemented it", conflictFirstHead, "", handoff); status != http.StatusOK {
		t.Fatalf("the implementer's completion = %d %s %s, want 200", status, failure.Code, failure.Error)
	}
	r.imp.endTurn()
	r.note("drove POST /legion/v1/handoff/complete for the implementer at head %s naming handoff record %s, then its agent_end: 200",
		conflictFirstHead, handoff)
	got := r.waitFor("the issue in testing", func(got api.Issue) bool { return got.Phase == phase.Testing })

	r.test = r.claimant(claim.RoleTester, "ses_tester")
	prompt := r.test.takeTask("Phase: testing")
	working := r.d.claim(r.test.token)
	if working.State != string(supervise.StateWorking) || working.Pending == nil || working.Pending.ConfirmedAt == nil {
		t.Fatalf("the tester is %s holding %+v, want working on a confirmed delivery of its testing task", working.State, working.Pending)
	}
	r.served = got.PhaseEntry
	r.note("read back: %s is in %s on entry %d at head %s, and the tester is %s on the delivery %s of that entry's task under session %s (%s)",
		r.key, got.Phase, r.served, got.PullRequest.Head, working.State, working.Pending.ID, working.Session, firstLine(prompt.Message))
}

// Step 2. Another pull request merges onto the base while the tester's turn runs, and GitHub
// answers the tester's head as conflicting with the base it moved. The push asks the daemon for a
// read of the merge state, since the issue stands in testing, a phase a conflict sends back to
// implementing, and the read's answer sends the issue back to implementing at once: the move
// waits for no turn. The implementer's task is queued on its claim and not taken — the daemon
// sends its connection nothing, since the tester is in a turn and the task adopts the working
// copy the two share. The tester keeps what it had: its claim stays working on the delivery it
// confirmed, under the session it registered, its connection is sent nothing at all, and its
// process is not suspended.
func (r *conflictNoTakeoverLive) conflictMovesAtOnce() {
	t := r.t
	before := r.d.claim(r.test.token)
	if before.State != string(supervise.StateWorking) || before.Pending == nil || before.Pending.ConfirmedAt == nil {
		t.Fatalf("the tester is %s holding %+v before the push onto the base, want working on the delivery it confirmed: the proof turns on a turn that is running",
			before.State, before.Pending)
	}
	suspendsBefore := len(r.rt.CallsOf("Suspend"))
	pushed, readsBefore := time.Now(), r.github.reads()
	r.driveConflict()
	r.note("drove a push onto %s while the tester's turn runs, with GitHub answering the head %s of #%d conflicting with it", conflictBase, conflictFirstHead, r.number)

	got := r.waitFor("the issue back in implementing while the tester's turn runs", func(got api.Issue) bool {
		return got.Phase == phase.Implementing
	})
	r.note("read back: %s went back to %s %s after the push onto the base, while the tester's turn runs; the push asked GitHub for %d read(s) of #%d by then",
		r.key, got.Phase, time.Since(pushed).Round(time.Millisecond), r.github.reads()-readsBefore, r.number)

	// The implementer's task is queued on its claim and held there: the claim holds it unsent and
	// out of any turn, and nothing reaches the implementer's connection while the tester's turn
	// runs.
	var queued *api.DeliveryView
	testwait.Eventually(t, "the implementer's claim queued its task", func() bool {
		queued = r.d.claim(r.imp.token).Pending
		return queued != nil
	})
	if queued.Phase != phase.Implementing || !strings.Contains(queued.Task, "the head conflicts with "+conflictBase) {
		t.Fatalf("the implementer's claim queued %+v, want the implementing phase's task saying the head conflicts with %s", queued, conflictBase)
	}
	r.imp.silent(liveSettle)
	waiting := r.d.claim(r.imp.token)
	if waiting.State == string(supervise.StateWorking) || waiting.Pending == nil || waiting.Pending.ID != queued.ID ||
		waiting.Pending.DeliveredAt != nil || waiting.Pending.ConfirmedAt != nil {
		t.Fatalf("the implementer is %s holding %+v, want its task %s queued and not taken while the tester's turn runs", waiting.State, waiting.Pending, queued.ID)
	}
	r.queued = queued.ID
	r.note("read back over %s: the implementer's task %s is queued and not taken — its claim is %s holding it undelivered, and the daemon sent its connection nothing (%s); GitHub answered %d read(s) of #%d since the push",
		liveSettle, queued.ID, waiting.State, firstLine(queued.Task), r.github.reads()-readsBefore, r.number)

	// The tester's turn continues: same delivery, same session, still working, sent nothing, and
	// not suspended.
	r.test.silent(liveSettle)
	after := r.d.claim(r.test.token)
	if after.State != string(supervise.StateWorking) || after.Pending == nil || after.Pending.ID != before.Pending.ID ||
		after.Pending.ConfirmedAt == nil || after.Session != r.test.session {
		t.Fatalf("the tester is %s holding %+v under session %q after the conflict, want it still working the delivery %s it confirmed, under session %s",
			after.State, after.Pending, after.Session, before.Pending.ID, r.test.session)
	}
	if suspends := r.rt.CallsOf("Suspend"); len(suspends) != suspendsBefore {
		t.Fatalf("the conflict suspended %d claims, want none: no role is stopped by it", len(suspends)-suspendsBefore)
	}
	r.note("read back over %s: the tester's turn continues — its claim is still %s on delivery %s, the one it confirmed before the push, under session %s; the daemon sent its connection nothing at all (no abort, no shutdown, no prompt) and the fake runtime was asked to suspend nothing (%d suspensions, as before the push)",
		liveSettle, after.State, after.Pending.ID, after.Session, suspendsBefore)
}

// Step 3. The tester is told, once, on its own Envoy role topic, that its phase ended: the
// phase-ended row (record.PhaseEnded) the conflict enqueued on the outbox's role-message lane, the
// conflict itself behind the `Reason:` label that marks it off from the daemon's own sentences. It
// is not the architect's checks-red notice, which the same conflict sends to another topic
// altogether.
func (r *conflictNoTakeoverLive) testerIsToldItsPhaseEnded() {
	t := r.t
	topic := notify.RoleTopicPrefix + string(r.test.token)
	var told []listenerPublish
	testwait.Eventually(t, "the phase-ended message on the tester's role topic", func() bool {
		told = r.notices.acceptedOn(topic)
		return len(told) == 1
	})
	want := record.PhaseEnded{Role: claim.RoleTester, From: phase.Testing, To: phase.Implementing,
		Reason: conflictReason}.Message(r.key)
	if told[0].Message != want {
		t.Fatalf("the tester was told %q on %s, want %q", told[0].Message, topic, want)
	}
	r.note("read back: one message on %s — %s", topic, told[0].Message)
	architect := r.architectNotices(r.key, "checks-red")
	if len(architect) != 1 {
		t.Fatalf("the architect was told %d checks-red notices, want the conflict's one", len(architect))
	}
	r.note("read back: the architect's own topic took the conflict's one checks-red notice (%s), a different message on a different topic",
		architect[0].Payload)
	// Nothing repeats either of them, and nothing else reaches the tester's topic.
	time.Sleep(liveSettle)
	if again := r.notices.acceptedOn(topic); len(again) != 1 {
		t.Fatalf("the tester's topic took %d messages over %s, want the one", len(again), liveSettle)
	}
	r.note("read back over %s: the tester's topic has taken that one message and no other", liveSettle)
}

// Step 4. The tester ends its turn, and only then is the implementer's task sent: up to that end
// the claim still holds it undelivered and the implementer's connection is sent nothing, and at
// it the daemon sends the task it queued in step 2, which the implementer takes. While the
// implementer works, the tester begins a turn of its own, as an agent that goes on working does,
// before any task of a later entry could reach it: the turn step 5's first completion is reported
// from.
func (r *conflictNoTakeoverLive) implementerTakesItsTaskAfterTheTurn() {
	t := r.t
	r.imp.silent(liveSettle)
	waiting := r.d.claim(r.imp.token)
	if waiting.State == string(supervise.StateWorking) || waiting.Pending == nil || waiting.Pending.ID != r.queued ||
		waiting.Pending.ConfirmedAt != nil {
		t.Fatalf("the implementer is %s holding %+v before the tester's turn ends, want its task %s still queued and not taken", waiting.State, waiting.Pending, r.queued)
	}
	if tester := r.d.claim(r.test.token); tester.State != string(supervise.StateWorking) {
		t.Fatalf("the tester is %s, want it still in the turn it began in step 1", tester.State)
	}
	r.note("read back over %s: up to the end of the tester's turn the implementer's task %s stayed queued and unsent", liveSettle, r.queued)
	r.test.endTurn()
	r.note("the tester's turn on entry %d's task ended", r.served)
	prompt := r.imp.takeTask("the head conflicts with " + conflictBase)
	if prompt.DeliveryID != r.queued {
		t.Fatalf("the implementer was sent delivery %s, want %s, the task the conflict queued", prompt.DeliveryID, r.queued)
	}
	r.note("read back: the implementer got its task %s only after the tester's turn ended, and is working it (%s)", prompt.DeliveryID, firstLine(prompt.Message))
	r.test.startTurn("")
	r.note("the tester began a turn of its own while the implementer works")
}

// Step 5. The implementer merges the base forward and completes at the head it pushed, which
// hands testing out again with an entry of its own. GitHub answers the new head mergeable, as it
// does once the base is merged forward into it, so the daemon's read after that completion finds
// nothing to send back. The tester's own turn began before the new entry's task could reach it,
// so its claim still serves the first entry, and its completion at the first head, from that
// turn, is refused HANDOFF_STALE_ENTRY and records nothing. The turn ends, the new entry's task
// reaches the tester, and its completion at the head the implementer pushed, in the turn that task
// starts, is taken.
func (r *conflictNoTakeoverLive) staleEntryRefused() {
	t := r.t
	r.github.conflicts(false)
	r.push(conflictFirstHead, conflictSecondHead, "merge "+conflictBase+" forward")
	r.note("GitHub answers the head mergeable again; drove the implementer's push of %s onto %s and GitHub's synchronize of #%d to it",
		conflictSecondHead, conflictFirstHead, r.number)
	handoff := r.imp.handoff("implement")
	if status, failure := r.imp.complete("merged the base forward", conflictSecondHead, "", handoff); status != http.StatusOK {
		t.Fatalf("the implementer's second completion = %d %s %s, want 200", status, failure.Code, failure.Error)
	}
	r.imp.endTurn()
	r.note("drove POST /legion/v1/handoff/complete for the implementer at head %s naming handoff record %s, then its agent_end: 200",
		conflictSecondHead, handoff)
	got := r.waitFor("the issue in testing on a later entry", func(got api.Issue) bool {
		return got.Phase == phase.Testing && got.PhaseEntry > r.served
	})
	if got.PullRequest == nil || got.PullRequest.Head != conflictSecondHead {
		t.Fatalf("the record holds %+v for %s, want the pull request at %s, the head the implementer pushed", got.PullRequest, r.key, conflictSecondHead)
	}
	r.note("read back: %s is in testing again on entry %d at head %s, while the tester's claim serves entry %d", r.key, got.PhaseEntry, got.PullRequest.Head, r.served)

	held := r.d.claim(r.test.token)
	if held.State != string(supervise.StateWorking) || (held.Pending != nil && (held.Pending.ConfirmedAt != nil || held.Pending.DeliveredAt != nil)) {
		t.Fatalf("the tester is %s holding %+v, want it in a turn of its own with no task of the new entry delivered", held.State, held.Pending)
	}
	handoff = r.test.handoff("test")
	status, failure := r.test.complete("tested it", conflictFirstHead, "pass", handoff)
	if status != http.StatusConflict || failure.Code != "HANDOFF_STALE_ENTRY" {
		t.Fatalf("the tester's completion of entry %d at %s = %d %s %s, want 409 HANDOFF_STALE_ENTRY", r.served, conflictFirstHead, status, failure.Code, failure.Error)
	}
	if heads := "this completion is at commit " + conflictFirstHead + " while the pull request the record holds is at head " + conflictSecondHead; !strings.Contains(failure.Error, heads) {
		t.Fatalf("the refusal = %q, want it to say %q", failure.Error, heads)
	}
	r.note("read back: the tester's completion of entry %d at head %s naming handoff record %s, from its own turn = 409 %s — %s",
		r.served, conflictFirstHead, handoff, failure.Code, failure.Error)
	if after := r.issue(r.key); after.Phase != phase.Testing {
		t.Fatalf("%s is in %s after the refused completion, want testing: a refusal records nothing", r.key, after.Phase)
	}
	r.note("read back: %s is still in testing — the refusal recorded nothing", r.key)

	r.test.endTurn()
	prompt := r.test.takeTask("Phase: testing")
	r.note("read back: the tester's own turn ended and the task of entry %d reached it (%s)", got.PhaseEntry, firstLine(prompt.Message))
	handoff = r.test.handoff("test")
	if status, failure := r.test.complete("tested the merge forward", conflictSecondHead, "pass", handoff); status != http.StatusOK {
		t.Fatalf("the tester's completion of entry %d at %s = %d %s %s, want 200", got.PhaseEntry, conflictSecondHead, status, failure.Code, failure.Error)
	}
	r.test.endTurn()
	moved := r.waitFor("the issue in reviewing", func(got api.Issue) bool { return got.Phase == phase.Reviewing })
	r.note("read back: the tester's completion of entry %d at head %s naming handoff record %s, in the turn that entry's task started, was taken 200: %s is in %s",
		got.PhaseEntry, conflictSecondHead, handoff, r.key, moved.Phase)
}

// Step 6. The reviewer's claim was started by the tester's pass and nothing registered it, so no
// session holds its role topic. A push onto the base makes the head conflict in reviewing, which
// ends the reviewer's phase: the phase-ended row is published with keep to a topic the listener
// answers 404 "no live session holds the role" (notify.ErrNoHolder), which is what a listener
// without the kept lane, as this proof's stand-in is, answers whatever the publish's keep. That
// finishes the row rather than retrying it for ever, and the start acts all the same — the
// implementer, in no turn, as no role of the issue is, is prompted the new task at once.
func (r *conflictNoTakeoverLive) noHolderFinishesTheRow() {
	t := r.t
	topic := notify.RoleTopicPrefix + string(r.reviewer)
	r.listener.noHolder(topic)
	r.awaitLaunch(r.reviewer)
	r.note("the reviewer's claim was launched by the tester's pass and never registered; its topic %s now answers 404 unclaimed, which is what a listener without the kept lane, as this stand-in is, answers for a role no live session holds, a keep: true publish included", topic)

	readsBefore := r.github.reads()
	r.driveConflict()
	r.note("drove a push onto %s while %s is in reviewing, with GitHub answering the head %s conflicting with it", conflictBase, r.key, conflictSecondHead)

	prompt := r.imp.takeTask("the head conflicts with " + conflictBase)
	r.note("read back: the start still acted — the implementer was prompted its task (%s); the push asked GitHub for %d read(s) of #%d by then", firstLine(prompt.Message), r.github.reads()-readsBefore, r.number)
	if got := r.issue(r.key); got.Phase != phase.Implementing {
		t.Fatalf("%s is in %s after the conflict in reviewing, want implementing", r.key, got.Phase)
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
// publishes another pull request's merge onto that base, which asks the daemon to read the merge
// state wherever the issue stands in a phase a conflict sends back to implementing (workflow's
// basePush, askMergeStateFor, conflictReadPhase). GitHub sends no event when a head starts to
// conflict, and no envelope carries mergeability (intake's decode), so that read is what finds
// the conflict: it applies the intake.PullRequestMergeability the daemon makes of GitHub's answer,
// and a conflicting head is the one thing a pull request's own state still moves an issue for
// (workflow's mergeability).
func (r *conflictNoTakeoverLive) driveConflict() {
	r.t.Helper()
	r.github.conflicts(true)
	r.publishBasePush()
}

// conflictListener is the Envoy listener as this proof's daemon reaches it: the package's
// noticeListener, which takes every publish, with the one answer it does not give — the 404 a
// listener without the kept lane answers for a role topic no live session holds, whatever the
// publish's keep (writeRoleHolderError, packages/envoy/cmd/listener/api.go, decoded by notify's
// ErrNoHolder) — added for the topics a step names. Everything else, the architect's notices and
// the tester's own message included, goes to noticeListener, so the harness's reads of it
// (architectNotices) are the reads of this one.
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

// noHolder makes the listener answer every later publish to topic as a listener without the kept
// lane answers one to a role no live session holds.
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
