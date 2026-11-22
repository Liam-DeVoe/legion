package supervise

import (
	"testing"
)

// The agents of two steps in the identities a start names them by (plan.Agent.ID): the agent of
// step 1 of task a, which has no key, and the agent of the key b.
const (
	stepAgentA = "step:1:a"
	keyAgentB  = "key:b"
)

// The claim records the agent whose session it holds (Claim.SessionAgent), set by the decision
// that begins a session for a step's agent: a spawn of a queued claim for that agent records it,
// and the store holds it beside the session.
func TestASpawnForAStepsAgentRecordsTheAgentOfTheSession(t *testing.T) {
	h := newHarness(t)

	h.must(RequestSpawn{Claim: h.token, Start: StartWork{Agent: stepAgentA}})

	if got := h.claim().SessionAgent; got != stepAgentA {
		t.Fatalf("the claim spawned for %s records the agent %q, want %q", stepAgentA, got, stepAgentA)
	}
	if got := h.store.load(h.token).SessionAgent; got != stepAgentA {
		t.Fatalf("the store holds the agent %q, want %q", got, stepAgentA)
	}
}

// A workflow start names the agent of the step it brings its claim back for (StartWork.Agent),
// and the claim records the agent whose session it holds (Claim.SessionAgent). A start for
// another agent leaves that session behind in the decision that brings the claim back, recording
// the new agent with it, so the launch spawns that agent in a session of its own: the rule's resume
// of a suspended claim and the retry of a retired one alike. A start for the recorded agent and a
// start that names none resume the session; so does a start of a claim that records no agent, a
// claim the release before last ran, which then records the start's agent as the agent of the
// session it holds. The fault: a new agent on a held name continues the session of the agent that
// held it before, or an agent's own return is given a new session.
func TestAStartForAnotherAgentLeavesTheClaimsSessionBehind(t *testing.T) {
	for _, bring := range []struct {
		name  string
		stop  func(h *harness)
		start func(agent string) Event
	}{
		{
			name: "the rule's resume of a suspended claim",
			stop: func(h *harness) {
				h.must(RequestSuspend{Claim: h.token, By: SuspendedByDaemon, Reason: "its task was stopped"})
				h.wantState(StateSuspended)
			},
			start: func(agent string) Event {
				return RequestRuleResume{Claim: testToken, Reason: "start row 9", Start: StartWork{Agent: agent}}
			},
		},
		{
			name: "the retry of a retired claim",
			stop: func(h *harness) {
				h.must(RequestStop{Claim: h.token})
				h.wantState(StateRetired)
			},
			start: func(agent string) Event { return RequestRetry{Claim: testToken, Start: StartWork{Agent: agent}} },
		},
	} {
		for _, tc := range []struct {
			name             string
			recorded, agent  string
			fresh            bool
			recordsAfterward string
		}{
			{name: "a start for another agent", recorded: stepAgentA, agent: keyAgentB, fresh: true, recordsAfterward: keyAgentB},
			{name: "control: a start for the agent of the session", recorded: keyAgentB, agent: keyAgentB, recordsAfterward: keyAgentB},
			{name: "control: a start that names no agent", recorded: stepAgentA, recordsAfterward: stepAgentA},
			{name: "a claim that records no agent", agent: keyAgentB, recordsAfterward: keyAgentB},
		} {
			t.Run(bring.name+"/"+tc.name, func(t *testing.T) {
				c := queuedClaim()
				c.SessionAgent = tc.recorded
				h := newHarnessOf(t, c)
				h.reach(StateIdle)
				bring.stop(h)
				spawns, resumes := len(h.calls("Spawn")), len(h.calls("Resume"))

				h.must(bring.start(tc.agent))

				got := h.claim()
				spawned, resumed := len(h.calls("Spawn"))-spawns, len(h.calls("Resume"))-resumes
				if tc.fresh {
					if spawned != 1 || resumed != 0 || got.Session != "" {
						t.Fatalf("the start for %s of a claim whose session is %s's made %d spawns and %d resumes, the claim recording session %q; want one spawn of a new session",
							tc.agent, tc.recorded, spawned, resumed, got.Session)
					}
					if got.Abandoned.Session != session {
						t.Fatalf("the claim records the abandoned session %+v, want %q left behind", got.Abandoned, session)
					}
				} else if resumed != 1 || spawned != 0 || got.Session != session {
					t.Fatalf("the start for %q of a claim whose session is %q's made %d spawns and %d resumes, the claim recording session %q; want the session %q resumed",
						tc.agent, tc.recorded, spawned, resumed, got.Session, session)
				}
				if got.SessionAgent != tc.recordsAfterward {
					t.Fatalf("the claim records the agent %q of its session, want %q", got.SessionAgent, tc.recordsAfterward)
				}
				if stored := h.store.load(h.token).SessionAgent; stored != tc.recordsAfterward {
					t.Fatalf("the store holds the agent %q of the claim's session, want %q", stored, tc.recordsAfterward)
				}
			})
		}
	}
}
