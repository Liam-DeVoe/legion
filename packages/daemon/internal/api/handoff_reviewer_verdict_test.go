package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// reviewCompletion is the reviewer's completion of reviewing as the `legion` tool posts it: a
// fresh grant, its own review record, the head that record names (commit, "" for a record that
// names none) and the verdict it was given.
func reviewCompletion(t *testing.T, reviewer liveClaim, verdict, commit string) HandoffCompleteRequest {
	t.Helper()
	return HandoffCompleteRequest{GrantID: reviewer.grant(t), Summary: "reviewed", Verdict: verdict, Commit: commit,
		HandoffID: reviewer.writeHandoff("review", 1), HandoffVersion: 1}
}

// The verdict of a review step reaches the daemon as the verdict of its agent's completion: the
// reviewer's completion of reviewing carries approve or changes at the head its review record
// names, and the route hands each to the workflow as the reviewer sent it, which takes it only
// from the claim the step's record names (workflow's TestReviewStepVerdictFromItsOwnClaim). A
// reviewer's word that is not one of the two is refused REVIEWER_VERDICT_REQUIRED, and a
// reviewer's verdict that names no commit VERDICT_COMMIT_REQUIRED. A testing step's completion
// carries no verdict, since Dispatch's check states at the tested head decide it, so the tester's
// verdict is refused VERDICT_NOT_TAKEN (206's refusal), and the agent of every step of another
// type, or of no step, gets VERDICT_ROLE_FORBIDDEN: the implementer, the planner, the merger and
// the architect, whose acceptance is its sign_off. Each refusal is written before the fact, so
// nothing is recorded.
func TestHandoffReviewerVerdict(t *testing.T) {
	for _, verdict := range []string{"approve", "changes"} {
		t.Run(verdict+" at a commit", func(t *testing.T) {
			h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
			seedIssueAt(t, h, "LEGION-208", phase.Reviewing)
			reviewer := newLiveClaim(t, h, "LEGION-208", claim.RoleReviewer)

			completion(t, h.complete(reviewCompletion(t, reviewer, verdict, headSHA)))
			fact := lastCompletion(t, facts)
			if fact.Role != claim.RoleReviewer || fact.Claim != reviewer.token || fact.Verdict != verdict || fact.Commit != headSHA {
				t.Fatalf("the reviewer's completion = %+v, want %s's verdict %s at %s", fact, reviewer.token, verdict, headSHA)
			}
		})
	}

	// otherVerdict is a completion that names its own handoff record of word and the verdict approve.
	otherVerdict := func(word string) func(t *testing.T, c liveClaim) HandoffCompleteRequest {
		return func(t *testing.T, c liveClaim) HandoffCompleteRequest {
			return HandoffCompleteRequest{GrantID: c.grant(t), Summary: "done", Verdict: "approve", Commit: headSHA, HandoffID: c.writeHandoff(word, 1), HandoffVersion: 1}
		}
	}
	roleForbidden := []string{"only the agent of a review step", "sign_off", "nothing was recorded"}
	for _, tc := range []struct {
		name   string
		role   claim.Role
		at     phase.Phase
		send   func(t *testing.T, c liveClaim) HandoffCompleteRequest
		status int
		code   string
		says   []string
	}{
		{
			name: "a reviewer's verdict that names no commit", role: claim.RoleReviewer, at: phase.Reviewing,
			send: func(t *testing.T, c liveClaim) HandoffCompleteRequest {
				return reviewCompletion(t, c, "approve", "")
			},
			status: http.StatusBadRequest, code: "VERDICT_COMMIT_REQUIRED", says: []string{"reviewer", "`head`", "nothing was recorded"},
		},
		{
			name: "a tester's verdict", role: claim.RoleTester, at: phase.Testing,
			send: func(t *testing.T, c liveClaim) HandoffCompleteRequest {
				return HandoffCompleteRequest{GrantID: c.grant(t), Summary: "tests pass", Verdict: "pass", Commit: headSHA, HandoffID: c.writeHandoff("test", 1), HandoffVersion: 1}
			},
			status: http.StatusBadRequest, code: "VERDICT_NOT_TAKEN", says: []string{"check states", "nothing was recorded"},
		},
		{
			name: "a reviewer's pass", role: claim.RoleReviewer, at: phase.Reviewing,
			send: func(t *testing.T, c liveClaim) HandoffCompleteRequest {
				return reviewCompletion(t, c, "pass", headSHA)
			},
			status: http.StatusBadRequest, code: "REVIEWER_VERDICT_REQUIRED", says: []string{"approve", "changes", `not "pass"`},
		},
		{
			name: "a reviewer's word no verdict has", role: claim.RoleReviewer, at: phase.Reviewing,
			send: func(t *testing.T, c liveClaim) HandoffCompleteRequest {
				return reviewCompletion(t, c, "maybe", headSHA)
			},
			status: http.StatusBadRequest, code: "REVIEWER_VERDICT_REQUIRED", says: []string{"approve", "changes", `not "maybe"`},
		},
		{
			name: "a reviewer's completion with no verdict", role: claim.RoleReviewer, at: phase.Reviewing,
			send:   func(t *testing.T, c liveClaim) HandoffCompleteRequest { return reviewCompletion(t, c, "", headSHA) },
			status: http.StatusBadRequest, code: "REVIEWER_VERDICT_REQUIRED", says: []string{"approve", "changes", "names none"},
		},
		{name: "an implementer's verdict", role: claim.RoleImplementer, at: phase.Implementing, send: otherVerdict("implement"),
			status: http.StatusBadRequest, code: "VERDICT_ROLE_FORBIDDEN", says: roleForbidden},
		{name: "a planner's verdict", role: claim.RolePlanner, at: phase.Planning, send: otherVerdict("plan"),
			status: http.StatusBadRequest, code: "VERDICT_ROLE_FORBIDDEN", says: roleForbidden},
		{name: "a merger's verdict", role: claim.RoleMerger, at: phase.Merging, send: otherVerdict("merge"),
			status: http.StatusBadRequest, code: "VERDICT_ROLE_FORBIDDEN", says: roleForbidden},
		{name: "an architect's verdict", role: claim.RoleArchitect, at: phase.Accepting, send: otherVerdict("accept"),
			status: http.StatusBadRequest, code: "VERDICT_ROLE_FORBIDDEN", says: roleForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
			seedIssueAt(t, h, "LEGION-208", tc.at)
			worker := newLiveClaim(t, h, "LEGION-208", tc.role)

			message := refused(t, h.complete(tc.send(t, worker)), tc.status, tc.code)
			for _, want := range tc.says {
				if !strings.Contains(message, want) {
					t.Fatalf("the %s refusal says %q, want it to say %q", tc.code, message, want)
				}
			}
			if got := facts.recorded(); len(got) != 0 {
				t.Fatalf("facts after the refusal = %#v, want none applied", got)
			}
		})
	}
}

// makeStaleInRound is makeStale for a claim whose pod is in an update round, out of its turn:
// recorded at contract, holding its capability, and in round. The daemon serves such a claim on
// the contract it records until the round ends (supervise.StaleRefusesCalls), and the stale-plugin
// lane does not relaunch it (internal/daemon's staleForTheLane).
func (h *harness) makeStaleInRound(t *testing.T, token claim.Token, session, secret string, contract int, round string) {
	t.Helper()
	machine, ok := h.supervisor.Machine(token)
	if !ok {
		t.Fatalf("no machine for %s", token)
	}
	c := machine.Claim()
	opened := stillClock{}.Now()
	c.Session, c.CapabilityHash, c.PluginContract = session, secretHash(secret), contract
	c.UpdateRound, c.UpdateReasons, c.UpdateOpenedAt = round, "the pod template changed: the image moved", &opened
	if err := h.store.PutClaim(context.Background(), c); err != nil {
		t.Fatalf("seed a stale claim in round %s for %s: %v", round, token, err)
	}
	h.supervisor.restart(t, token)
}

// A reviewer whose plugin is from before the reviewer's verdict (ReviewerVerdictContract) knows
// the tester's words alone, and the daemon serves its claim while its turn runs and while its pod
// is in an update round (staleClaim). So its pass is recorded as approve and its fail as changes,
// at the head its record names, and its pending, or a completion with no verdict, is refused
// REVIEWER_VERDICT_REQUIRED in words that name the two words it has: pass for approve and fail for
// changes, since nothing else the old reviewer reads names a verdict for it. A reviewer on the
// contract that has the verdict is held to approve and changes, and its pass is refused.
func TestAStaleReviewerPassIsApproveAndFailIsChanges(t *testing.T) {
	stale := ReviewerVerdictContract - 1
	for _, served := range []struct {
		name string
		// serve has the reviewer's claim record the stale contract where the daemon serves it.
		serve func(t *testing.T, h *harness, reviewer liveClaim)
		// inRound is whether the claim stands in an update round, out of its turn.
		inRound bool
	}{
		{name: "in its turn", serve: func(t *testing.T, h *harness, reviewer liveClaim) {
			h.makeStale(t, reviewer.token, reviewer.session, reviewer.secret, stale)
		}},
		{name: "in an update round", inRound: true, serve: func(t *testing.T, h *harness, reviewer liveClaim) {
			endTurn(t, h, reviewer.token)
			h.makeStaleInRound(t, reviewer.token, reviewer.session, reviewer.secret, stale, "pod:6f0b5d2e-reviewer-round")
		}},
	} {
		t.Run(served.name, func(t *testing.T) {
			reviewerAt := func(t *testing.T) (*harness, *factRecorder, liveClaim) {
				t.Helper()
				h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
				seedIssueAt(t, h, "LEGION-208", phase.Reviewing)
				reviewer := newLiveClaim(t, h, "LEGION-208", claim.RoleReviewer)
				served.serve(t, h, reviewer)
				c := h.stored(reviewer.token)
				if c.PluginContract != stale || (c.UpdateRound != "") != served.inRound || (c.State == supervise.StateWorking) == served.inRound {
					t.Fatalf("the reviewer's claim is %s at contract %d in round %q, want contract %d, in a round %t: this case proves nothing",
						c.State, c.PluginContract, c.UpdateRound, stale, served.inRound)
				}
				return h, facts, reviewer
			}
			for _, word := range []struct{ sent, recorded string }{{sent: "pass", recorded: "approve"}, {sent: "fail", recorded: "changes"}} {
				t.Run(word.sent, func(t *testing.T) {
					h, facts, reviewer := reviewerAt(t)
					completion(t, h.complete(reviewCompletion(t, reviewer, word.sent, headSHA)))
					if fact := lastCompletion(t, facts); fact.Role != claim.RoleReviewer || fact.Verdict != word.recorded || fact.Commit != headSHA {
						t.Fatalf("the stale reviewer's %s = %+v, want the verdict %s at %s", word.sent, fact, word.recorded, headSHA)
					}
				})
			}
			for _, verdict := range []string{"pending", ""} {
				t.Run("verdict "+verdict, func(t *testing.T) {
					h, facts, reviewer := reviewerAt(t)
					message := refused(t, h.complete(reviewCompletion(t, reviewer, verdict, headSHA)), http.StatusBadRequest, "REVIEWER_VERDICT_REQUIRED")
					for _, want := range []string{"pass, which this daemon takes as approve", "fail, which it takes as changes", "nothing was recorded"} {
						if !strings.Contains(message, want) {
							t.Fatalf("the refusal of the stale reviewer's verdict %q says %q, want it to say %q", verdict, message, want)
						}
					}
					if got := facts.recorded(); len(got) != 0 {
						t.Fatalf("facts after the refusal = %#v, want none applied", got)
					}
				})
			}
		})
	}

	t.Run("a reviewer on the verdict's contract that sends pass", func(t *testing.T) {
		h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
		seedIssueAt(t, h, "LEGION-208", phase.Reviewing)
		reviewer := newLiveClaim(t, h, "LEGION-208", claim.RoleReviewer)
		if got := pluginContractOf(t, h, reviewer.token); got < ReviewerVerdictContract {
			t.Fatalf("the reviewer's plugin declared contract %d, below ReviewerVerdictContract %d: this case proves nothing", got, ReviewerVerdictContract)
		}
		message := refused(t, h.complete(reviewCompletion(t, reviewer, "pass", headSHA)), http.StatusBadRequest, "REVIEWER_VERDICT_REQUIRED")
		if strings.Contains(message, "takes as approve") {
			t.Fatalf("the refusal of a current reviewer's pass says %q, which names the old contract's words", message)
		}
		if got := facts.recorded(); len(got) != 0 {
			t.Fatalf("facts after the refusal = %#v, want none applied", got)
		}
	})
}
