package supervise

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
)

func TestAFreshLaunchEnrollsAtRegistrationWithTheSession(t *testing.T) {
	h := newHarness(t)
	broker := h.withSecrets()
	h.launch()
	h.helloWithIdentity()
	h.wantState(StateShimConnected)
	if n := len(broker.enrolled()); n != 0 {
		t.Fatalf("%d enrollments before the agent registered; the session id is the wake address", n)
	}
	h.register()
	enrollments := broker.enrolled()
	if len(enrollments) != 1 {
		t.Fatalf("enrollments %+v, want one at registration", enrollments)
	}
	loc := h.locator()
	want := PodEnrollment{PodUID: loc.Incarnation, Thumbprint: testIdentity.Thumbprint, PodToken: testIdentity.PodToken, Session: session}
	if enrollments[0] != want {
		t.Fatalf("enrolled %+v, want %+v", enrollments[0], want)
	}
	stored := h.store.load(testToken)
	if stored.Enrollment == nil || stored.Enrollment.ID != "enr-1" || stored.Enrollment.Incarnation != loc.Incarnation {
		t.Fatalf("stored enrollment %+v", stored.Enrollment)
	}
	if got := h.conn.Enrollments(); len(got) != 1 || got[0] != "enr-1" {
		t.Fatalf("the shim was told %v, want [enr-1]", got)
	}
}

// In an issue pod six role processes share one pod uid, so the broker tells their enrollments
// apart by slot: the pod's real uid stays the identity the broker verifies against the projected
// token, and the slot is the daemon's own `<role>-g<generation>`, never the composed incarnation.
func TestAnIssuePodProcessEnrollsWithItsPodUIDAndRoleSlot(t *testing.T) {
	h := newHarness(t)
	broker := h.withSecrets()
	h.rt.ScriptSpawn(fake.SpawnResult{Locator: runtime.Locator{
		Runtime: runtime.RuntimeSandbox, Claim: testToken, Incarnation: runtime.SandboxIncarnation("pod-uid-9", 1),
		Sandbox: &runtime.SandboxLocator{Namespace: "legion", Name: "legion-legion-209", PodUID: "pod-uid-9", Container: "roles", Generation: 1},
	}})
	h.launch()
	h.helloWithIdentity()
	h.register()
	enrollments := broker.enrolled()
	want := PodEnrollment{PodUID: "pod-uid-9", Slot: "implementer-g1", Thumbprint: testIdentity.Thumbprint, PodToken: testIdentity.PodToken, Session: session}
	if len(enrollments) != 1 || enrollments[0] != want {
		t.Fatalf("enrolled %+v, want %+v", enrollments, want)
	}
	if stored := h.store.load(testToken); stored.Enrollment == nil || stored.Enrollment.Incarnation != "pod-uid-9/1" {
		t.Fatalf("stored enrollment %+v, want it bound to the process incarnation", stored.Enrollment)
	}
}

func TestAResumedClaimEnrollsOnItsHello(t *testing.T) {
	h := newHarness(t)
	broker := h.withSecrets()
	h.launch()
	h.helloWithIdentity()
	h.register()
	h.ready()
	first := h.claim().Enrollment.ID
	h.must(RequestSuspend{Claim: h.token, By: SuspendedByOperator})
	h.wantState(StateSuspended)
	if got := broker.revocations(); len(got) != 1 || got[0] != first {
		t.Fatalf("the suspension revoked %v, want [%s]", got, first)
	}
	h.must(RequestResume{Claim: h.token})
	h.wantState(StateLaunching)
	h.helloWithIdentity() // the resumed pod's hello: the claim already records its session
	enrollments := broker.enrolled()
	if len(enrollments) != 2 || enrollments[1].Session != session || enrollments[1].PodUID != h.locator().Incarnation ||
		enrollments[1].PodUID == enrollments[0].PodUID {
		t.Fatalf("enrollments %+v, want the resumed pod enrolled on its hello with the recorded session and its own uid", enrollments)
	}
	if c := h.claim(); c.Enrollment == nil || c.Enrollment.ID == first || c.Enrollment.Incarnation != h.locator().Incarnation {
		t.Fatalf("the resumed claim holds %+v", c.Enrollment)
	}
	if got := broker.revocations(); len(got) != 1 {
		t.Fatalf("the resume revoked %v", got[1:])
	}
}

func TestEveryEndOfAProcessRevokesItsEnrollment(t *testing.T) {
	for name, end := range map[string]func(h *harness){
		"found gone":           func(h *harness) { h.observe(runtime.Gone) },
		"not the recorded pod": func(h *harness) { h.observe(runtime.NotRecordedProcess) },
		"suspended":            func(h *harness) { h.must(RequestSuspend{Claim: h.token, By: SuspendedByOperator}) },
		"stopped":              func(h *harness) { h.must(RequestStop{Claim: h.token}) },
		"the agent exited":     func(h *harness) { h.exit() },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			broker := h.withSecrets()
			h.launch()
			h.helloWithIdentity()
			h.register()
			h.ready()
			id := h.claim().Enrollment.ID
			end(h)
			if got := broker.revocations(); len(got) != 1 || got[0] != id {
				t.Fatalf("revocations %v, want [%s]", got, id)
			}
			if c := h.claim(); c.Enrollment != nil {
				t.Fatalf("the claim still records enrollment %+v", c.Enrollment)
			}
		})
	}
}

func TestTheRegistrationDeadlineRevokesAnUnregisteredPodsEnrollment(t *testing.T) {
	// A pod whose agent never registers is enrolled only if its claim already had a session (a
	// resume); a fresh launch holds the identity and enrolls nothing, so the deadline revokes
	// nothing and discards the identity with the process.
	h := newHarness(t)
	broker := h.withSecrets()
	h.launch()
	h.helloWithIdentity()
	h.advance(deadline)
	if got := broker.revocations(); len(got) != 0 {
		t.Fatalf("revocations %v for a pod that was never enrolled", got)
	}
	if n := len(broker.enrolled()); n != 0 {
		t.Fatalf("%d enrollments", n)
	}
	// The relaunched pod's hello carries its own identity; registration enrolls it, not the old one.
	h.helloWithIdentity()
	h.register()
	if e := broker.enrolled(); len(e) != 1 || e[0].PodUID != h.locator().Incarnation {
		t.Fatalf("enrolled %+v, want the relaunched pod", e)
	}
}

// A session is held to the first claim that recorded it before anything records, launches or
// enrolls it (holdSession, dispatch://INFRA-840): the broker's enrollment takes over the grants of
// every other enrollment of the pod's account and session, so an enrollment of another claim's
// session is that claim's secrets. A registration the hold refuses is answered with the refusal
// and leads to no Enroll call, and records nothing; one it admits leads to exactly one. A claim a
// daemon before the hold recorded on a session the store now holds for no claim (two claims
// recorded it) is never relaunched under it, and, re-adopted live, enrolls nothing on its hello.
//
// The controls (the pull request records each run): the hold after the enrollment (in register,
// below m.ensureEnrolled) answers the same refusal, but only once the stub has recorded the
// enrollment of the other claim's session; no hold in ensureEnrolled enrolls the re-adopted
// claim; no hold in start resumes the relaunched one.
func TestARefusedSessionIsNeverEnrolled(t *testing.T) {
	t.Run("a registration of another claim's session", func(t *testing.T) {
		h := newHarness(t)
		broker := h.withSecrets()
		h.store.holds[session] = "legion-legion-LEGION-209-tester"
		h.launch()
		h.helloWithIdentity()
		err := h.handle(RequestRegister{Claim: h.token, Generation: h.generation(), Session: session, SessionFile: sessionFile,
			CapabilityHash: []byte("capability"), PluginContract: h.deps.PluginContract})
		var refusal claim.Refusal
		if !errors.As(err, &refusal) || refusal != claim.SessionHeldRefusal(session) {
			t.Fatalf("registering the tester's session = %v, want %v", err, claim.SessionHeldRefusal(session))
		}
		if enrolled := broker.enrolled(); len(enrolled) != 0 {
			t.Fatalf("the refused registration enrolled %+v, the session of legion-legion-LEGION-209-tester", enrolled)
		}
		if c := h.claim(); c.Session != "" || c.SessionFile != "" || c.CapabilityHash != nil || c.State != StateShimConnected {
			t.Fatalf("the refused registration left the claim %s recording %q, %q and capability %x", c.State, c.Session, c.SessionFile, c.CapabilityHash)
		}

		h.must(RequestRegister{Claim: h.token, Generation: h.generation(), Session: "ses_fresh", SessionFile: "/state/sessions/fresh.jsonl",
			CapabilityHash: []byte("capability"), PluginContract: h.deps.PluginContract})
		if enrolled := broker.enrolled(); len(enrolled) != 1 || enrolled[0].Session != "ses_fresh" {
			t.Fatalf("the admitted registration enrolled %+v, want one enrollment of ses_fresh", enrolled)
		}
		if holder := h.store.holds["ses_fresh"]; holder != h.token {
			t.Fatalf("ses_fresh is held for %q, want %s", holder, h.token)
		}
	})

	// The recorded session registering again (reregister) is held as a first registration is,
	// before anything of it is written: a claim a daemon before the hold recorded on a session the
	// store holds for another claim is refused its re-registration, and its record and the broker
	// are left as they were.
	t.Run("a re-registration of a session another claim holds", func(t *testing.T) {
		h := newHarness(t)
		broker := h.withSecrets()
		h.reach(StateReady)
		h.store.setHold(session, "legion-legion-LEGION-209-tester")
		h.restart()
		before := h.claim()
		err := h.handle(RequestRegister{Claim: h.token, Generation: h.generation(), Session: session,
			SessionFile: "/state/sessions/again.jsonl", CapabilityHash: []byte("again"), PluginContract: h.deps.PluginContract})
		var refusal claim.Refusal
		if !errors.As(err, &refusal) || refusal != claim.SessionHeldRefusal(session) {
			t.Fatalf("re-registering the tester's session = %v, want %v", err, claim.SessionHeldRefusal(session))
		}
		if c := h.claim(); c.State != before.State || c.SessionFile != before.SessionFile || !bytes.Equal(c.CapabilityHash, before.CapabilityHash) {
			t.Fatalf("the refused re-registration left the claim %s recording %q and capability %q; want it %s recording %q and %q, as before",
				c.State, c.SessionFile, c.CapabilityHash, before.State, before.SessionFile, before.CapabilityHash)
		}
		if enrolled := broker.enrolled(); len(enrolled) != 0 {
			t.Fatalf("the refused re-registration reached the broker: %+v", enrolled)
		}
	})

	t.Run("a re-adopted claim's session held for no claim", func(t *testing.T) {
		h := newHarness(t)
		broker := h.withSecrets()
		// A claim a daemon before the hold launched and recorded: ready on its session, its shim
		// connected without the pod's identity, so nothing was enrolled yet.
		h.reach(StateReady)
		// Two claims recorded the session before the daemon held sessions: the migration holds it
		// for neither, and a daemon restarted onto it re-adopts the claim recording it.
		h.store.setHold(session, "")
		h.restart()
		h.helloWithIdentity()
		if enrolled := broker.enrolled(); len(enrolled) != 0 {
			t.Fatalf("enrollments %+v, want none: the re-adopted claim's session is held for no claim", enrolled)
		}
		if c := h.claim(); c.Enrollment != nil {
			t.Fatalf("the re-adopted claim records enrollment %+v", c.Enrollment)
		}
	})

	t.Run("a relaunch of a claim whose session is held for no claim", func(t *testing.T) {
		h := newHarness(t)
		broker := h.withSecrets()
		h.launch()
		h.helloWithIdentity()
		h.register()
		h.ready()
		h.must(RequestSuspend{Claim: h.token, By: SuspendedByOperator})
		h.store.setHold(session, "")
		h.deps.Limits.LaunchFailures = 1
		h.restart()
		resumes := len(h.calls("Resume"))
		err := h.handle(RequestResume{Claim: h.token})
		var refusal claim.Refusal
		if !errors.As(err, &refusal) || refusal != claim.SessionHeldRefusal(session) {
			t.Fatalf("resuming a claim whose session is held for no claim = %v, want %v", err, claim.SessionHeldRefusal(session))
		}
		if calls := h.calls("Resume")[resumes:]; len(calls) != 0 {
			t.Fatalf("the runtime was asked to resume the session held for no claim: %+v", calls)
		}
		if enrolled := broker.enrolled(); len(enrolled) != 1 {
			t.Fatalf("enrollments %+v, want only the first process's", enrolled)
		}
		if c := h.claim(); c.State != StateFailed {
			t.Fatalf("the claim is %s, want it failed at its launch budget rather than running under %s", c.State, session)
		}
	})

	// An operator settles an id held for no claim by recording its rightful holder, and that claim's
	// next registration on the same machine is admitted: the refusal of an id held for no claim is
	// never kept, as a claim's hold is.
	t.Run("an id held for no claim, once an operator records this claim as its holder", func(t *testing.T) {
		h := newHarness(t)
		broker := h.withSecrets()
		h.store.holds[session] = ""
		h.launch()
		h.helloWithIdentity()
		registration := RequestRegister{Claim: h.token, Generation: h.generation(), Session: session, SessionFile: sessionFile,
			CapabilityHash: []byte("capability"), PluginContract: h.deps.PluginContract}
		var refusal claim.Refusal
		if err := h.handle(registration); !errors.As(err, &refusal) {
			t.Fatalf("registering a session held for no claim = %v, want the refusal", err)
		}
		h.store.setHold(session, h.token)
		h.must(registration)
		if enrolled := broker.enrolled(); len(enrolled) != 1 || enrolled[0].Session != session {
			t.Fatalf("the settled registration enrolled %+v, want one enrollment of %s", enrolled, session)
		}
	})
}

// A Claude Code launch runs under the session its recorded transcript's name gives: a fresh
// launch's own uuid, which its claude-envoy registers with that session's own transcript, and a
// relaunch's `--resume <id>` read from the recorded transcript (claudelaunch.ResumeSession). So the
// session a transcript names is held to the claim as its session is (holdRecorded,
// dispatch://INFRA-840): a registration whose transcript names another claim's Claude session is
// refused, recording and enrolling nothing, and a claim recorded before the hold whose transcript
// names one is never relaunched into it.
//
// The controls (the pull request records both runs): register holding the registered session
// alone admits the registration and records the other claim's transcript; start resuming without
// holdRecorded calls the runtime's Resume on that transcript.
func TestAClaudeLaunchIsHeldToItsClaim(t *testing.T) {
	const holder = claim.Token("legion-legion-LEGION-209-tester")
	const stolen = "11111111-2222-4333-8444-555555555555"
	transcript := "/legion/state/implementer/claude/projects/-legion-workspaces-acme-widgets-legion-209/" + stolen + ".jsonl"

	t.Run("a registration whose transcript names another claim's Claude session", func(t *testing.T) {
		h := newHarness(t)
		broker := h.withSecrets()
		h.store.holds[stolen] = holder
		h.launch()
		h.helloWithIdentity()
		err := h.handle(RequestRegister{Claim: h.token, Generation: h.generation(), Session: "22222222-3333-4444-8555-666666666666",
			SessionFile: transcript, CapabilityHash: []byte("capability"), PluginContract: h.deps.PluginContract})
		var refusal claim.Refusal
		if !errors.As(err, &refusal) || refusal != claim.SessionHeldRefusal(stolen) {
			t.Fatalf("registering a transcript of %s's Claude session = %v, want %v", holder, err, claim.SessionHeldRefusal(stolen))
		}
		if c := h.claim(); c.Session != "" || c.SessionFile != "" || c.CapabilityHash != nil {
			t.Fatalf("the refused registration left the claim recording %q, %q and capability %x", c.Session, c.SessionFile, c.CapabilityHash)
		}
		if enrolled := broker.enrolled(); len(enrolled) != 0 {
			t.Fatalf("the refused registration enrolled %+v", enrolled)
		}
	})

	t.Run("a relaunch resuming another claim's Claude session", func(t *testing.T) {
		h := newHarness(t)
		h.reach(StateReady)
		h.must(RequestSuspend{Claim: h.token, By: SuspendedByOperator})
		// A claim a daemon before the hold recorded: its own session, and a transcript naming the
		// tester's Claude session, which a resume would continue.
		stored := h.store.load(h.token)
		stored.SessionFile = transcript
		if err := h.store.PutClaim(h.ctx, stored); err != nil {
			t.Fatal(err)
		}
		h.store.setHold(stolen, holder)
		h.deps.Limits.LaunchFailures = 1
		h.restart()
		resumes := len(h.calls("Resume"))

		err := h.handle(RequestResume{Claim: h.token})
		var refusal claim.Refusal
		if !errors.As(err, &refusal) || refusal != claim.SessionHeldRefusal(stolen) {
			t.Fatalf("resuming a transcript of %s's Claude session = %v, want %v", holder, err, claim.SessionHeldRefusal(stolen))
		}
		if calls := h.calls("Resume")[resumes:]; len(calls) != 0 {
			t.Fatalf("the runtime was asked to resume %s's Claude session: %+v", holder, calls)
		}
		if c := h.claim(); c.State != StateFailed {
			t.Fatalf("the claim is %s, want it failed at its launch budget rather than running under %s", c.State, stolen)
		}
	})
}

func TestAFailedRevocationIsRetriedThenLeftToTheLease(t *testing.T) {
	h := newHarness(t)
	broker := h.withSecrets()
	broker.revokeFails = 2
	h.launch()
	h.helloWithIdentity()
	h.register()
	id := h.claim().Enrollment.ID
	h.observe(runtime.Gone) // the first attempt runs on the machine's goroutine and fails
	if got := broker.revocations(); len(got) != 0 {
		t.Fatalf("revoked %v before any retry", got)
	}
	h.advance(revokeRetryDelay) // the second attempt, from the clock; fails
	h.advance(revokeRetryDelay) // the third succeeds
	if got := broker.revocations(); len(got) != 1 || got[0] != id {
		t.Fatalf("revocations %v after two failures, want [%s] on the third attempt", got, id)
	}
	h = newHarness(t)
	broker = h.withSecrets()
	broker.revokeFails = revokeAttempts
	h.launch()
	h.helloWithIdentity()
	h.register()
	h.observe(runtime.Gone)
	for i := 0; i < revokeAttempts; i++ {
		h.advance(revokeRetryDelay)
	}
	if lines := h.logs.lines("agent-secrets: enrollment not revoked", "the lease ends it"); len(lines) != 1 {
		t.Fatalf("log %q, want the give-up line once", h.logs.lines("agent-secrets"))
	}
}

// TestARevocationTheBrokerRefusesIsNotRetried: a daemon cannot end an enrollment another launcher
// credential made (the broker answers 403 NOT_ENROLLING_CREDENTIAL, a refusal no retry changes), so
// the machine makes one attempt, retries nothing, and logs that the enrollment's lease ends it once
// the pod stops renewing.
func TestARevocationTheBrokerRefusesIsNotRetried(t *testing.T) {
	h := newHarness(t)
	broker := h.withSecrets()
	broker.revokeErr = permanentErr{"broker answered 403 NOT_ENROLLING_CREDENTIAL: only the launcher credential that enrolled a pod may end its enrollment"}
	h.launch()
	h.helloWithIdentity()
	h.register()
	h.observe(runtime.Gone)
	for range revokeAttempts {
		h.advance(revokeRetryDelay)
	}
	if broker.revokeCalls() != 1 {
		t.Fatalf("%d revocation attempts for a refusal no retry changes, want 1", broker.revokeCalls())
	}
	if lines := h.logs.lines("agent-secrets: enrollment revocation refused", "NOT_ENROLLING_CREDENTIAL", "its lease ends it"); len(lines) != 1 {
		t.Fatalf("log %q, want the refusal line once", h.logs.lines("agent-secrets"))
	}
	if lines := h.logs.lines("agent-secrets: enrollment not revoked after every attempt"); len(lines) != 0 {
		t.Fatalf("log %q: a refusal is not a run of failed attempts", lines)
	}
}

func TestATransientEnrollmentFailureIsRetriedOnTheNextAliveObservation(t *testing.T) {
	h := newHarness(t)
	broker := h.withSecrets()
	broker.enrollErr = errors.New("broker unreachable")
	h.launch()
	h.helloWithIdentity()
	h.register()
	h.wantState(StateRegistered)
	if c := h.claim(); c.Enrollment != nil {
		t.Fatalf("enrolled %+v through a failure", c.Enrollment)
	}
	broker.enrollErr = nil
	h.observe(runtime.Alive)
	if c := h.claim(); c.Enrollment == nil {
		t.Fatal("the alive observation did not retry the enrollment")
	}
	if got := h.conn.Enrollments(); len(got) != 1 {
		t.Fatalf("the shim was told %v", got)
	}
}

// A projected token is valid for an hour at most, and the shim reads it once per hello. An
// enrollment held up past the hello token's expiry (the daemon's token login failing and retrying)
// presents a token the shim reads again then, never the expired one, which the broker refuses as
// POD_TOKEN_EXPIRED.
func TestAnEnrollmentHeldPastTheHelloTokensExpiryPresentsAFreshToken(t *testing.T) {
	h := newHarness(t)
	broker := h.withSecrets()
	broker.enrollErr = errors.New("broker answered 0 NO_MACHINE_CREDENTIAL: token login failed: broker answered 503 DATABASE: down")
	h.launch()
	h.helloWithToken(podToken(h.clock.Now().Add(time.Hour)))
	h.register()
	h.ready()
	if c := h.claim(); c.Enrollment != nil {
		t.Fatalf("enrolled %+v while the login was pending", c.Enrollment)
	}

	h.clock.Advance(80 * time.Minute) // the login is approved long after the hello
	broker.enrollErr = nil
	fresh := podToken(h.clock.Now().Add(time.Hour))
	h.conn.SetAgentSecretsToken(fresh)
	h.observe(runtime.Alive)

	if refused := h.logs.lines("agent-secrets: enrollment refused"); len(refused) != 0 {
		t.Fatalf("the enrollment was refused: %q", refused)
	}
	enrollments := broker.enrolled()
	if len(enrollments) != 1 || enrollments[0].PodToken != fresh {
		t.Fatalf("enrolled %+v, want one enrollment presenting the token the shim read after the hello's expired", enrollments)
	}
	if c := h.claim(); c.Enrollment == nil || c.Enrollment.ID != "enr-1" {
		t.Fatalf("stored enrollment %+v", c.Enrollment)
	}
	if h.m.identity.PodToken != "" {
		t.Fatal("the machine kept the pod token past the enrollment")
	}
}

// The machine asks the shim for its token again only when the token it holds could expire before
// the broker verifies it: within the Enroll call's RPC bound plus a minute of clock skew. A token
// that outlives that is presented as the hello carried it.
func TestAHeldTokenIsReadAgainOnlyWhenItCouldExpireBeforeTheBrokerVerifiesIt(t *testing.T) {
	for name, tc := range map[string]struct {
		left  time.Duration // the hello token's life left when the agent registers
		reads int
	}{
		"expired":                    {-time.Minute, 1},
		"expiring within the margin": {testRPC + 30*time.Second, 1},
		"outliving the margin":       {testRPC + 2*time.Minute, 0},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			broker := h.withSecrets()
			fresh := podToken(h.clock.Now().Add(time.Hour))
			h.conn.SetAgentSecretsToken(fresh)
			hello := podToken(h.clock.Now().Add(tc.left))
			h.launch()
			h.helloWithToken(hello)
			h.register()
			want := hello
			if tc.reads > 0 {
				want = fresh
			}
			if enrollments := broker.enrolled(); len(enrollments) != 1 || enrollments[0].PodToken != want {
				t.Fatalf("enrolled %+v, want one enrollment presenting %s", enrollments, want)
			}
			if reads := h.conn.TokenReads(); reads != tc.reads {
				t.Fatalf("the shim was asked for its token %d times, want %d", reads, tc.reads)
			}
		})
	}
}

// A shim built before the token request forwards it to OMP, which refuses it as an unknown
// command. The machine presents the broker nothing it would refuse: it drops the identity and
// waits for the shim's next hello, whose token is read then, as it does after a refusal.
func TestAShimThatCannotReadItsTokenAgainWaitsForItsNextHello(t *testing.T) {
	h := newHarness(t)
	broker := h.withSecrets()
	h.conn.FailAgentSecretsToken(fmt.Errorf("worker-stream: %s refused agent-secrets-token: Unknown command: agent-secrets-token: %w", h.token, runtime.ErrTokenRefused))
	h.launch()
	h.helloWithToken(podToken(h.clock.Now().Add(-time.Minute)))
	h.register()
	h.ready()
	h.observe(runtime.Alive)
	if c := h.claim(); c.Enrollment != nil || len(broker.enrolled()) != 0 {
		t.Fatalf("enrolled %+v with no token to present", c.Enrollment)
	}
	if reads := h.conn.TokenReads(); reads != 1 {
		t.Fatalf("the shim was asked for its token %d times, want once: its refusal stands until its next hello", reads)
	}
	if lines := h.logs.lines("agent-secrets: the shim did not read its projected token again", "Unknown command"); len(lines) != 1 {
		t.Fatalf("log %q", h.logs.lines("agent-secrets"))
	}
	if refused := h.logs.lines("agent-secrets: enrollment refused"); len(refused) != 0 {
		t.Fatalf("the expired token reached the broker: %q", refused)
	}
	h.helloWithToken(podToken(h.clock.Now().Add(time.Hour))) // the shim reconnected
	if c := h.claim(); c.Enrollment == nil {
		t.Fatal("the fresh hello did not enroll")
	}
}

func TestATokenRequestTheTransportLosesIsRetriedOnTheNextAliveObservation(t *testing.T) {
	h := newHarness(t)
	broker := h.withSecrets()
	h.conn.FailAgentSecretsToken(errors.New("worker-stream: agent-secrets-token: no answer on the worker stream within the RPC timeout"))
	h.launch()
	h.helloWithToken(podToken(h.clock.Now().Add(-time.Minute)))
	h.register()
	h.ready()
	if c := h.claim(); c.Enrollment != nil {
		t.Fatalf("enrolled %+v with no token to present", c.Enrollment)
	}
	h.conn.FailAgentSecretsToken(nil)
	fresh := podToken(h.clock.Now().Add(time.Hour))
	h.conn.SetAgentSecretsToken(fresh)
	h.observe(runtime.Alive)
	if enrollments := broker.enrolled(); len(enrollments) != 1 || enrollments[0].PodToken != fresh {
		t.Fatalf("enrolled %+v, want the alive observation to ask again and present what the shim read", enrollments)
	}
}

func TestAPermanentEnrollmentRefusalWaitsForTheNextHello(t *testing.T) {
	h := newHarness(t)
	broker := h.withSecrets()
	broker.enrollErr = permanentErr{"broker answered 403 POD_IDENTITY_MISMATCH: the token names another pod"}
	h.launch()
	h.helloWithIdentity()
	h.register()
	broker.enrollErr = nil
	h.observe(runtime.Alive)
	if c := h.claim(); c.Enrollment != nil {
		t.Fatal("a permanent refusal was retried with the same identity")
	}
	if lines := h.logs.lines("agent-secrets: enrollment refused", "POD_IDENTITY_MISMATCH"); len(lines) != 1 {
		t.Fatalf("log %q", h.logs.lines("agent-secrets"))
	}
	h.helloWithIdentity() // the shim reconnected with a fresh token
	if c := h.claim(); c.Enrollment == nil {
		t.Fatal("the fresh hello did not enroll")
	}
}

func TestTheEnrollmentIsResentToAReconnectedShim(t *testing.T) {
	h := newHarness(t)
	h.withSecrets()
	h.launch()
	h.helloWithIdentity()
	h.register()
	h.ready()
	h.must(StreamClosed{Claim: h.token})
	h.observe(runtime.Alive)
	h.helloWithIdentity()
	if got := h.conn.Enrollments(); len(got) != 2 || got[0] != got[1] {
		t.Fatalf("the shim was told %v, want the same id twice", got)
	}
	// The claim was already enrolled for this incarnation when the reconnected shim's hello
	// arrived, so the fresh identity the hello carried must not keep a live pod token in memory.
	if h.m.identity == nil || h.m.identity.PodToken != "" {
		t.Fatalf("the held identity kept a live pod token after an already-enrolled hello: %+v", h.m.identity)
	}
}

func TestARestartKeepsTheEnrollmentAndTellsTheReconnectedShim(t *testing.T) {
	h := newHarness(t)
	broker := h.withSecrets()
	h.launch()
	h.helloWithIdentity()
	h.register()
	h.ready()
	id := h.claim().Enrollment.ID
	h.restart()
	h.helloWithIdentity()
	if e := broker.enrolled(); len(e) != 1 {
		t.Fatalf("the restarted daemon enrolled again: %+v", e)
	}
	if c := h.claim(); c.Enrollment == nil || c.Enrollment.ID != id {
		t.Fatalf("the restarted claim holds %+v, want %s", c.Enrollment, id)
	}
	if got := h.conn.Enrollments(); len(got) != 2 || got[1] != id {
		t.Fatalf("the shim was told %v", got)
	}
}

func TestWithoutABrokerNothingIsEnrolledOrRevoked(t *testing.T) {
	h := newHarness(t)
	h.launch()
	h.helloWithIdentity()
	h.register()
	h.ready()
	if c := h.claim(); c.Enrollment != nil {
		t.Fatalf("enrolled %+v with no broker", c.Enrollment)
	}
	h.observe(runtime.Gone)
	if got := h.conn.Enrollments(); len(got) != 0 {
		t.Fatalf("the shim was told %v", got)
	}
}

// expiredTokenErr is the broker refusing the presented pod token as expired (POD_TOKEN_EXPIRED).
type expiredTokenErr struct{ msg string }

func (e expiredTokenErr) Error() string      { return e.msg }
func (e expiredTokenErr) TokenExpired() bool { return true }

// A broker whose clock runs further ahead of the daemon's than podTokenSkew refuses as expired a
// token the daemon still holds good. The key stands: the machine drops the token, keeps the
// identity, and at its next observation asks the shim for the pod's current token and enrolls the
// same key with it, rather than waiting for a hello.
func TestAnExpiredTokenRefusalReadsTheTokenAgainAtTheNextObservation(t *testing.T) {
	h := newHarness(t)
	broker := h.withSecrets()
	broker.enrollErr = expiredTokenErr{"broker answered 403 POD_TOKEN_EXPIRED: the projected token has expired (oidc: token expired at 2026-10-11T04:54:28Z)"}
	h.launch()
	h.helloWithIdentity() // a token the daemon's clock holds good for an hour
	h.register()
	h.ready()
	if lines := h.logs.lines("agent-secrets: the broker found the pod token expired", "POD_TOKEN_EXPIRED"); len(lines) != 1 {
		t.Fatalf("log %q", h.logs.lines("agent-secrets"))
	}
	if refused := h.logs.lines("agent-secrets: enrollment refused"); len(refused) != 0 {
		t.Fatalf("an expired token was read as a refusal of the key: %q", refused)
	}
	if h.m.identity == nil || h.m.identity.Thumbprint != testIdentity.Thumbprint || h.m.identity.PodToken != "" {
		t.Fatalf("held identity %+v, want the key kept and the expired token dropped", h.m.identity)
	}
	if reads := h.conn.TokenReads(); reads != 0 {
		t.Fatalf("the shim was asked for its token %d times before the refusal, want none: the daemon held it good", reads)
	}

	broker.enrollErr = nil
	fresh := podToken(h.clock.Now().Add(time.Hour + time.Minute))
	h.conn.SetAgentSecretsToken(fresh)
	h.observe(runtime.Alive)
	if reads := h.conn.TokenReads(); reads != 1 {
		t.Fatalf("the shim was asked for its token %d times at the next observation, want once", reads)
	}
	enrollments := broker.enrolled()
	if len(enrollments) != 1 || enrollments[0].PodToken != fresh || enrollments[0].Thumbprint != testIdentity.Thumbprint {
		t.Fatalf("enrolled %+v, want the same key enrolled once, with the token the shim read", enrollments)
	}
	if c := h.claim(); c.Enrollment == nil {
		t.Fatal("the claim records no enrollment")
	}
}
