package supervise

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
)

// The pods these tests refuse launches for and start processes in: the pod a refusal names, and
// two others, the one that replaced it and one more.
const (
	refusedPod  = "0d4e7a1c-5b38-4f62-9c07-3e1a8b5d2f94"
	replacedPod = "8f2c6b0e-1d47-4a93-b5e8-7c0a2d9f3e61"
	anotherPod  = "5a9e3c1d-7f02-4b68-8d4c-2e6f0b1a9c37"
)

// refusedFor is the runtime's refusal of a launch into pod for want of its update round
// (runtime.AwaitingPodRound), naming that pod.
func refusedFor(pod string) func(context.Context) error {
	return func(context.Context) error {
		return fmt.Errorf("launch %s: %w", testToken, &runtime.AwaitingPodRound{Pod: podProcess(pod, 1),
			Reason: "pod legion-legion-209 carries no exchange identity and dispatch_token_transition_file is not set; its update round replaces it"})
	}
}

// wantRefused is the pod the claim was refused for with no session (Claim.RefusedWithNoSession),
// "" for none, and the pod it last held a session in (Claim.SessionPod), as Claim, View and the
// accessor the daemon's sweep reads (Machine.RefusedWithNoSession) report them, and the store
// holding neither: both are memory only.
func (h *harness) wantRefused(pod string, held SessionPod, when string) {
	h.t.Helper()
	viewed, viewedHeld := h.m.RefusedWithNoSession()
	for name, got := range map[string]struct {
		refused *runtime.Locator
		held    SessionPod
	}{
		"Claim":        {h.claim().RefusedWithNoSession, h.claim().SessionPod},
		"View":         {h.m.View().RefusedWithNoSession, h.m.View().SessionPod},
		"the accessor": {viewed, viewedHeld},
	} {
		if gotPod := locatorPod(got.refused); gotPod != pod || (pod == "") != (got.refused == nil) {
			h.t.Fatalf("%s %s reports the claim refused for pod %q with no session (%+v), want %q", when, name, gotPod, got.refused, pod)
		}
		if got.held != held {
			h.t.Fatalf("%s %s reports the claim's last session in %+v, want %+v", when, name, got.held, held)
		}
	}
	if stored := h.store.load(h.token); stored.RefusedWithNoSession != nil || stored.SessionPod != (SessionPod{}) {
		h.t.Fatalf("%s the store holds the refusal %+v and the guard %+v, want neither: both are memory only",
			when, stored.RefusedWithNoSession, stored.SessionPod)
	}
}

// A launch of a claim with no session that the runtime refuses for want of its pod's update round
// records that pod as the one the claim was refused for with no session (Claim.RefusedWithNoSession).
// The record stays where the wait's pod goes: through the retries that are refused again, and
// through the retry that starts the claim's process in that very pod, which ends the wait
// (Claim.AwaitsPod). The registration of a session from that process voids it, since that
// session's worker ran in the pod, and makes that pod the guard (Claim.SessionPod).
//
// The negative control (the pull request records the run): register without the void, where the
// record stays at the registration.
func TestARefusalWithNoSessionIsRecordedUntilASessionRegistersInThatPod(t *testing.T) {
	h := newHarness(t)
	h.wantRefused("", SessionPod{}, "before any launch,")
	h.rt.OnSpawn = refusedFor(refusedPod)
	if err := h.handle(RequestSpawn{Claim: h.token}); !errors.Is(err, runtime.ErrAwaitingPodRound) {
		t.Fatalf("the launch answered %v, want the runtime's wait for the pod's round", err)
	}
	h.wantRefused(refusedPod, SessionPod{}, "at the refusal,")

	h.advance(podRoundRetry)
	h.wantCalls("Spawn", 2)
	h.wantRefused(refusedPod, SessionPod{}, "at the retry's refusal,")

	h.rt.OnSpawn = nil
	h.rt.ScriptSpawn(fake.SpawnResult{Locator: podProcess(refusedPod, 3)})
	h.advance(podRoundRetry)
	if c := h.claim(); locatorPod(c.Locator) != refusedPod || c.AwaitsPod != nil {
		t.Fatalf("the retry that went through left the claim at %+v waiting on %+v, want a process in pod %s and no wait", c.Locator, c.AwaitsPod, refusedPod)
	}
	h.wantRefused(refusedPod, SessionPod{}, "once a retry started a process in the refused pod,")

	h.connect()
	h.register()
	h.wantRefused("", SessionPod{Pod: refusedPod}, "once a session registered in the refused pod,")
}

// A claim refused for a pod with no session whose retry starts it in another pod, the one that
// replaced the refused pod, keeps the record when its agent registers there: no session of it ran
// in the refused pod. The guard moves to the pod it registered in.
func TestARefusalRecordStaysThroughARegistrationInAnotherPod(t *testing.T) {
	h := newHarness(t)
	h.rt.OnSpawn = refusedFor(refusedPod)
	if err := h.handle(RequestSpawn{Claim: h.token}); !errors.Is(err, runtime.ErrAwaitingPodRound) {
		t.Fatalf("the launch answered %v, want the runtime's wait for the pod's round", err)
	}
	h.rt.OnSpawn = nil
	h.rt.ScriptSpawn(fake.SpawnResult{Locator: podProcess(replacedPod, 2)})
	h.advance(podRoundRetry)
	h.connect()
	h.register()
	h.wantRefused(refusedPod, SessionPod{Pod: replacedPod}, "once a session registered in the pod that replaced the refused one,")
}

// A refusal while the claim has a session records nothing: that session's worker may have run in
// the refused pod. The claim here registered its session in another pod, so the guard names
// another pod than the refused one, and the claim's session alone keeps the record off.
//
// The negative control (the pull request records the run): startWaitsForPodRound setting the
// record at a refusal whatever the claim's session, where the claim is recorded as refused.
func TestARefusalWithASessionRecordsNothing(t *testing.T) {
	h := newHarness(t)
	h.inPod(anotherPod)
	h.wantRefused("", SessionPod{Pod: anotherPod}, "once ready in its own pod,")
	h.rt.OnResume = refusedFor(refusedPod)
	if err := h.handle(RuntimeObservation{Observation: runtime.Observation{Locator: h.locator(), Kind: runtime.Gone, At: h.clock.Now()}}); !errors.Is(err, runtime.ErrAwaitingPodRound) {
		t.Fatalf("the death's relaunch answered %v, want the runtime's wait for the pod's round", err)
	}
	if c := h.claim(); c.Session != session || locatorPod(c.AwaitsPod) != refusedPod {
		t.Fatalf("the refused claim records session %q and waits on %+v, want its session kept and the wait on pod %s", c.Session, c.AwaitsPod, refusedPod)
	}
	h.wantRefused("", SessionPod{Pod: anotherPod}, "at a refusal with a session,")
}

// A claim that held a session in a pod and lost it — a relaunch whose resume finds the session in
// no store forgets it and spawns a fresh agent in the same try (startFresh) — has no session at
// the refusal of that spawn for the same pod. The guard still names the pod (Claim.SessionPod),
// through the forget, the fresh start and the let-go, so the refusal records nothing: the worker
// of the session it lost can have left work on that pod's disk. A refusal with no session for
// another pod is recorded, since the guard names that pod of no session.
//
// The negative control (the pull request records the run): startWaitsForPodRound setting the
// record at a refusal with no session whatever the guard says, where the claim is recorded as
// refused for the pod it held its session in.
func TestTheGuardKeepsTheRecordOffAClaimThatHeldASessionInTheRefusedPod(t *testing.T) {
	h := newHarness(t)
	h.inPod(refusedPod)
	h.wantRefused("", SessionPod{Pod: refusedPod}, "once ready in the pod,")
	h.rt.ScriptResume(fake.SpawnResult{Err: fmt.Errorf("resume %s: %w: no store holds it", testToken, runtime.ErrSessionFileMissing)})
	h.rt.OnSpawn = refusedFor(refusedPod)
	if err := h.handle(RuntimeObservation{Observation: runtime.Observation{Locator: h.locator(), Kind: runtime.Gone, At: h.clock.Now()}}); !errors.Is(err, runtime.ErrAwaitingPodRound) {
		t.Fatalf("the death's relaunch answered %v, want the fresh spawn refused for the pod's round", err)
	}
	if c := h.claim(); c.Session != "" || c.Abandoned.Session != session || locatorPod(c.AwaitsPod) != refusedPod {
		t.Fatalf("the claim records session %q, abandoned %q, waiting on %+v; want its session forgotten and abandoned, and the wait on pod %s",
			c.Session, c.Abandoned.Session, c.AwaitsPod, refusedPod)
	}
	h.wantRefused("", SessionPod{Pod: refusedPod}, "at the fresh spawn's refusal for the pod the session was held in,")

	h.advance(podRoundRetry)
	h.wantRefused("", SessionPod{Pod: refusedPod}, "at the retry's refusal,")

	h.rt.OnSpawn = refusedFor(anotherPod)
	h.advance(podRoundRetry)
	h.wantRefused(anotherPod, SessionPod{Pod: refusedPod}, "at a refusal for another pod,")
}

// A machine built over a stored claim has no memory of a session the claim held and lost before
// the restart, so it reads the guard off the stored claim (bootSessionPod), and where it cannot
// place a session the claim held, the guard is unknown and names every pod (SessionPod.Names):
// a stored session whose process is in no pod, and an abandoned session (Claim.Abandoned), which
// forgetSession writes with the forget and the store keeps. A stored session whose process is in
// a pod is placed there. A claim that records neither has held no session.
//
// The last case drives it through a restart: a claim that held its session in a pod and lost it,
// whose fresh agent started in that same pod, is refused for that pod after the restart with no
// session, and nothing is recorded, as before the restart. Registering again places the guard.
//
// The negative control (the pull request records the run): bootSessionPod answering
// SessionPod{} for every claim, where the abandoned row reads a known guard and the restart's
// refusal is recorded.
func TestAMachineBuiltOverAStoredClaimReadsTheGuardOffIt(t *testing.T) {
	inPod := podProcess(refusedPod, 4)
	pane := runtime.Locator{Runtime: runtime.RuntimeTmux, Claim: testToken, Incarnation: "4242:1",
		Tmux: &runtime.TmuxLocator{Window: "@1", Pane: "%1"}}
	for name, tc := range map[string]struct {
		claim Claim
		want  SessionPod
	}{
		"no session and none abandoned": {claim: queuedClaim(), want: SessionPod{}},
		"a session whose process is in a pod": {claim: func() Claim {
			c := queuedClaim()
			c.State, c.Session, c.SessionFile, c.Locator = StateReady, session, sessionFile, &inPod
			return c
		}(), want: SessionPod{Pod: refusedPod}},
		"a session whose process is in no pod": {claim: func() Claim {
			c := queuedClaim()
			c.State, c.Session, c.SessionFile, c.Locator = StateReady, session, sessionFile, &pane
			return c
		}(), want: SessionPod{Unknown: true}},
		"a session and no process": {claim: func() Claim {
			c := queuedClaim()
			c.State, c.SuspendedBy, c.Session, c.SessionFile = StateSuspended, SuspendedByOperator, session, sessionFile
			return c
		}(), want: SessionPod{Unknown: true}},
		"an abandoned session and none now": {claim: func() Claim {
			c := queuedClaim()
			c.Abandoned = AbandonedSession{Session: "ses_before", Why: "no store holds it", At: epoch}
			return c
		}(), want: SessionPod{Unknown: true}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newBareHarness(t)
			h.store.PutClaim(h.ctx, tc.claim)
			h.start(h.store.load(testToken))
			if got := h.claim().SessionPod; got != tc.want {
				t.Fatalf("the machine built over the stored claim reads the guard %+v, want %+v", got, tc.want)
			}
		})
	}

	t.Run("a session lost in the refused pod before a restart", func(t *testing.T) {
		h := newHarness(t)
		h.inPod(refusedPod)
		h.rt.ScriptResume(fake.SpawnResult{Err: fmt.Errorf("resume %s: %w: no store holds it", testToken, runtime.ErrSessionFileMissing)})
		h.rt.ScriptSpawn(fake.SpawnResult{Locator: podProcess(refusedPod, 2)})
		h.observe(runtime.Gone)
		if c := h.claim(); c.Session != "" || c.Abandoned.Session != session || locatorPod(c.Locator) != refusedPod {
			t.Fatalf("the claim records session %q, abandoned %q, at %+v; want its session abandoned and its fresh agent in pod %s",
				c.Session, c.Abandoned.Session, c.Locator, refusedPod)
		}

		h.restart()
		h.wantRefused("", SessionPod{Unknown: true}, "after the restart,")
		h.rt.OnSpawn = refusedFor(refusedPod)
		if err := h.handle(RuntimeObservation{Observation: runtime.Observation{Locator: h.locator(), Kind: runtime.Gone, At: h.clock.Now()}}); !errors.Is(err, runtime.ErrAwaitingPodRound) {
			t.Fatalf("the death's relaunch answered %v, want the runtime's wait for the pod's round", err)
		}
		h.wantRefused("", SessionPod{Unknown: true}, "at the refusal after the restart,")

		h.rt.OnSpawn = nil
		h.rt.ScriptSpawn(fake.SpawnResult{Locator: podProcess(replacedPod, 5)})
		h.advance(podRoundRetry)
		h.connect()
		h.register()
		h.wantRefused("", SessionPod{Pod: replacedPod}, "once a session registered again,")
	})
}
