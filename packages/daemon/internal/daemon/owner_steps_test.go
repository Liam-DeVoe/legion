// The whole daemon walks a plan whose owner does a step of its own.
//
// Each test boots the whole daemon over the package's broker, the real store, the real outbox and
// the real Dispatch of this package's tests (ownerStepsRig, owner_steps_rig_test.go), and walks
// each issue through the transports production has: each role's agent over the worker stream and
// the daemon's HTTP routes, the owner through its own routes (complete_step, sign_off), a person's
// review and the merge as GitHub's envelopes on the stream. What is stood in for lies outside the
// walk: GitHub's REST API (stepRecordsGitHub), Envoy's listener, which takes every notice and hook
// prompt the outbox publishes (noticeListener), and the panes and the slots, which the fake runtime
// launches and answers for and the test plays.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// The owner does a step of its own: the plan `implementer:owner`. No claim of the implementer is
// created; the outbox writes the owner's slot for the implementer's App and asks whether the slot
// holds it, and while the runtime answers that it does not yet, the owner is told nothing. Once
// the slot holds it, the owner is told its step as one notice owner-step. Its complete_step of
// step 1, with its handoff record written in its own session and the pull request open, moves the
// issue to the retro, which is the owner's too (plan.RetroAgent): the end of step 1 writes the
// owner's slot for its own App again, and the retro is told as an owner-step once the slot holds
// it. The owner's complete_step of the retro with its rating moves the issue to merging, and the
// merge ends it at done. The records of step 1 and of the retro name the owner's claim and its
// session, and no implementer was ever launched.
//
// The control is a mutation of the release's code, run by hand: start's branch of the owner's
// place taken out of workflow/effects.go. The step's start then launches the owner's claim again
// as a worker's start, in place of telling it, and the owner's turn to report step 1 finds its
// claim launching: the test fails there.
func TestTheOwnerDoesAStep(t *testing.T) {
	r := newOwnerStepsRig(t, newLiveProofOn(t, workflowNATS(t)), startScratchDispatchOn(t, scratchDispatchOptions{}), 1, config.Project{})
	owner := r.worker(claim.RoleArchitect)
	r.givePlan("implementer:owner")
	r.rt.ScriptSlotHolds(runtime.ErrSlotCredentialPending)
	r.openGate(phase.Implementing)
	if !testwait.Holds(stepRecordsWait, 50*time.Millisecond, func() bool { return r.slotAsked("SlotHoldsCredential", owner.token, claim.RoleImplementer) }) {
		t.Fatalf("the outbox never asked whether %s's slot holds the implementer's App: the implementer was launched %v (its claim recorded: %t), and the owner was told %d owner-step notices",
			owner.token, r.launchesOfRole(claim.RoleImplementer), r.claimRecorded(r.token(claim.RoleImplementer)), len(r.ownerNotices(record.OwnerStepNotice)))
	}
	if !r.slotAsked("WriteSlotCredential", owner.token, claim.RoleImplementer) {
		t.Fatalf("the outbox asked whether %s's slot holds the implementer's App without writing it: calls %+v", owner.token, r.slotCalls())
	}
	if testwait.Holds(liveSettle, 100*time.Millisecond, func() bool { return len(r.ownerNotices(record.OwnerStepNotice)) > 0 }) {
		t.Fatalf("the owner of %s was told its step %+v while its slot does not hold the implementer's App", r.key, r.ownerNotices(record.OwnerStepNotice))
	}
	r.rt.ScriptSlotHolds(nil)
	told := r.waitOwnerNotices(record.OwnerStepNotice, 1)
	if told[0].Role != claim.RoleImplementer || told[0].Phase != phase.Implementing || !strings.Contains(told[0].Reason, " Step 1 of 1: implementer:owner.") {
		t.Fatalf("the owner of %s was told %+v, want the owner-step of step 1, implementer:owner", r.key, told[0])
	}
	if launches := r.launchesOfRole(claim.RoleImplementer); len(launches) != 0 || r.claimRecorded(r.token(claim.RoleImplementer)) {
		t.Fatalf("%s's step of the owner launched an implementer %v (its claim recorded: %t), want no implementer claim", r.key, launches,
			r.claimRecorded(r.token(claim.RoleImplementer)))
	}

	r.github.open("legion/"+r.key, r.number)
	handoff, version := r.writeHandoff(owner.session, "implement")
	if status, body := r.ownerReports(api.CompleteStepRequest{Step: 1, Summary: "implemented", Commit: stepRecordsHead,
		HandoffID: handoff, HandoffVersion: version}); status != http.StatusOK {
		t.Fatalf("the owner's complete_step of step 1 of %s = %d %s, want 200", r.key, status, body)
	}
	r.waitPhase(phase.Retro)
	told = r.waitOwnerNotices(record.OwnerStepNotice, 2)
	if told[1].Role != "" || told[1].Phase != phase.Retro {
		t.Fatalf("the owner of %s was told %+v at the retro, want the owner-step of task a's retro", r.key, told[1])
	}
	if !r.slotAsked("WriteSlotCredential", owner.token, claim.RoleArchitect) {
		t.Fatalf("the end of %s's step 1 left the owner's slot on another App: calls %+v, want a write of the architect's", r.key, r.slotCalls())
	}
	if status, body := r.ownerReports(api.CompleteStepRequest{Step: 0, Summary: "retro written", Commit: stepRecordsHead,
		PlanRating: new(deploymentRating), PlanReason: new(deploymentReason)}); status != http.StatusOK {
		t.Fatalf("the owner's complete_step of %s's retro = %d %s, want 200", r.key, status, body)
	}
	r.waitPhase(phase.Merging)
	r.merge()
	r.waitRecordsOf(owner, 1)
	if launches := r.launchesOfRole(claim.RoleImplementer); len(launches) != 0 || r.claimRecorded(r.token(claim.RoleImplementer)) {
		t.Fatalf("%s launched an implementer %v by done, want none: the owner did step 1 and the retro", r.key, launches)
	}
}

// waitRecordsOf waits for the read of the walk's issue's steps to hold, of task a, the record of
// each step of steps and the record of the retro, each ended passed and each naming c's claim as
// its agent and c's session; the retro carries deploymentRating and deploymentReason.
func (r *ownerStepsRig) waitRecordsOf(c *stepClaimant, steps ...int) {
	r.t.Helper()
	waitRead(r.stepRecordsRig, "the records of "+r.key+"'s steps of "+string(c.token), "Dispatch", time.Second, func() (deploymentStepsRead, error) {
		var read deploymentStepsRead
		return read, r.personRead("/api/v1/issues/"+r.key+"/steps", &read)
	}, func(read deploymentStepsRead) error {
		var records []deploymentStepRead
		for _, listed := range read.Tasks {
			records = append(records, listed.Steps...)
		}
		for _, step := range append(slices.Clone(steps), 0) {
			kind, name := "step", fmt.Sprintf("step %d", step)
			if step == 0 {
				kind, name = "retro", "the retro"
			}
			at := slices.IndexFunc(records, func(got deploymentStepRead) bool { return got.Kind == kind && (step == 0 || got.Step == step) })
			if at < 0 {
				return fmt.Errorf("the read holds no record of %s", name)
			}
			got := records[at]
			switch {
			case got.Task != "a" || got.EndedAt == nil || got.Result != "passed":
				return fmt.Errorf("%s is of task %q, ended %v with result %q; want task a, ended passed", name, got.Task, got.EndedAt, got.Result)
			case got.Agent != string(c.token) || got.Session != c.session:
				return fmt.Errorf("%s names the agent %q in session %q, want %s in %s", name, got.Agent, got.Session, c.token, c.session)
			case step == 0 && (got.Rating == nil || *got.Rating != deploymentRating || got.RatingReason == nil || *got.RatingReason != deploymentReason):
				return fmt.Errorf("the retro names the rating %v and the reason %v, want %d and %q", deref(got.Rating), deref(got.RatingReason),
					deploymentRating, deploymentReason)
			}
		}
		return nil
	})
}

// ownerReports is the owner of the walk's issue, its own architect, answering in a turn of its own
// and reporting req, a step of its own (complete_step), under a grant minted for it now; it answers
// the daemon's status and body.
func (r *ownerStepsRig) ownerReports(req api.CompleteStepRequest) (int, string) {
	r.t.Helper()
	owner := r.worker(claim.RoleArchitect)
	owner.ownTurn()
	req.GrantID, req.Issue = owner.grant(), r.key
	status, body := r.d.request(http.MethodPost, "/legion/v1/steps/complete", req, false)
	return status, string(body)
}

// ownerNotices is every notice of kind the outbox has published to the topic of the walk's issue's
// own architect, the owner of its task, in order.
func (r *ownerStepsRig) ownerNotices(kind record.NoticeKind) []record.Notice {
	r.t.Helper()
	var notices []record.Notice
	for _, published := range r.notices.acceptedOn(notify.RoleTopicPrefix + string(r.token(claim.RoleArchitect))) {
		var notice record.Notice
		if err := json.Unmarshal([]byte(published.Payload), &notice); err != nil {
			r.t.Fatalf("decode the notice %q published to the owner of %s: %v", published.Payload, r.key, err)
		}
		if notice.Kind == kind {
			notices = append(notices, notice)
		}
	}
	return notices
}

// waitOwnerNotices waits for the outbox to have published count notices of kind to the owner of the
// walk's issue, and answers them; it fails where more were published.
func (r *ownerStepsRig) waitOwnerNotices(kind record.NoticeKind, count int) []record.Notice {
	r.t.Helper()
	var notices []record.Notice
	testwait.WithinPolling(r.t, stepRecordsWait, 100*time.Millisecond, fmt.Sprintf("%d %s notices to the owner of %s", count, kind, r.key), func() bool {
		notices = r.ownerNotices(kind)
		return len(notices) >= count
	})
	if len(notices) != count {
		r.t.Fatalf("the owner of %s was told %d %s notices, want %d: %+v", r.key, len(notices), kind, count, notices)
	}
	return notices
}

// slotCalls is each call the daemon made of the runtime's slot credentials
// (runtime.SlotCredentials), in order, as `<method> <claim> <role>`.
func (r *ownerStepsRig) slotCalls() []string {
	var calls []string
	for _, call := range r.rt.Calls() {
		if call.Method == "WriteSlotCredential" || call.Method == "SlotHoldsCredential" {
			calls = append(calls, fmt.Sprintf("%s %s %s", call.Method, call.Claim, call.Role))
		}
	}
	return calls
}

// slotAsked says whether the daemon has called method of the runtime's slot credentials for the
// slot of token and the App of role.
func (r *ownerStepsRig) slotAsked(method string, token claim.Token, role claim.Role) bool {
	return slices.Contains(r.slotCalls(), fmt.Sprintf("%s %s %s", method, token, role))
}

// claimRecorded says whether the daemon's store records a claim of token.
func (r *ownerStepsRig) claimRecorded(token claim.Token) bool {
	r.t.Helper()
	var recorded bool
	if err := r.st.Pool().QueryRow(context.Background(), `select exists (select 1 from claims where token = $1)`, string(token)).Scan(&recorded); err != nil {
		r.t.Fatalf("read whether the claim %s is recorded: %v", token, err)
	}
	return recorded
}
