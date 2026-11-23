package supervise

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
)

// The pods these tests start processes in and refuse launches for: the pod a refusal names, and
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

// wantRegistered holds the set of pods an agent of the claim registered a session in
// (Claim.RegisteredPods) to want, as Claim, View and the accessor the daemon's sweep reads
// (Machine.RegisteredPods) each report it, and holds the store to none of it: the set is memory
// only.
func (h *harness) wantRegistered(want RegisteredPods, when string) {
	h.t.Helper()
	for name, got := range map[string]RegisteredPods{
		"Claim": h.claim().RegisteredPods, "View": h.m.View().RegisteredPods, "the accessor": h.m.RegisteredPods(),
	} {
		if !slices.Equal(got.Pods, want.Pods) || got.Unknown != want.Unknown {
			h.t.Fatalf("%s %s reports the claim registered in %+v, want %+v", when, name, got, want)
		}
	}
	if stored := h.store.load(h.token).RegisteredPods; len(stored.Pods) != 0 || stored.Unknown {
		h.t.Fatalf("%s the store holds the set %+v, want none: it is memory only", when, stored)
	}
}

// A registration adds the pod of the process that registered to the claim's set
// (Claim.RegisteredPods), and for a claim no idle release suspended nothing else adds to it: not a
// refusal for a pod, and not a process started in a pod before its agent registers. Nothing takes
// a pod out of it either: a relaunch into another pod adds that pod beside the first once its agent
// registers, and a claim that lost its session and started a fresh agent in a third pod still holds
// both, since an agent of it registered in each, and what that agent's worker did not push was on
// that pod's disk.
func TestARegistrationAddsItsPodToTheSetAndNothingTakesItOut(t *testing.T) {
	h := newHarness(t)
	h.wantRegistered(RegisteredPods{}, "before any launch,")
	h.rt.OnSpawn = refusedFor(refusedPod)
	if err := h.handle(RequestSpawn{Claim: h.token}); !errors.Is(err, runtime.ErrAwaitingPodRound) {
		t.Fatalf("the launch answered %v, want the runtime's wait for the pod's round", err)
	}
	h.wantRegistered(RegisteredPods{}, "at a refusal for a pod,")

	h.rt.OnSpawn = nil
	h.rt.ScriptSpawn(fake.SpawnResult{Locator: podProcess(refusedPod, 2)})
	h.advance(podRoundRetry)
	if c := h.claim(); locatorPod(c.Locator) != refusedPod {
		t.Fatalf("the retry that went through left the claim at %+v, want a process in pod %s", c.Locator, refusedPod)
	}
	h.wantRegistered(RegisteredPods{}, "once a retry started a process in the refused pod, before its agent registered,")
	h.connect()
	h.register()
	h.wantRegistered(RegisteredPods{Pods: []string{refusedPod}}, "once its agent registered in that pod,")
	h.ready()

	h.diesIntoPod(replacedPod)
	h.wantRegistered(RegisteredPods{Pods: []string{refusedPod, replacedPod}}, "once a relaunch into another pod registered there,")

	h.rt.ScriptResume(fake.SpawnResult{Err: fmt.Errorf("resume %s: %w: no store holds it", testToken, runtime.ErrSessionFileMissing)})
	h.rt.ScriptSpawn(fake.SpawnResult{Locator: podProcess(anotherPod, 1)})
	h.observe(runtime.Gone)
	if c := h.claim(); c.Session != "" || c.Abandoned.Session != session || locatorPod(c.Locator) != anotherPod {
		t.Fatalf("the claim records session %q, abandoned %q, at %+v; want its session abandoned and a fresh agent in pod %s",
			c.Session, c.Abandoned.Session, c.Locator, anotherPod)
	}
	h.wantRegistered(RegisteredPods{Pods: []string{refusedPod, replacedPod}}, "once the claim lost its session and started fresh in a third pod,")
}

// The one pod that goes in the set with no registration is the pod of the first launch of a role
// an idle release suspended, an agent of which had run (addRestoringPod): the release saved the
// unpushed work of the issue's workspace and took the pod, and the issue's next pod restored that
// work before any role started, so the claim's work is on that pod's disk before its agent
// registers there. A wake's resume (resumeByRule) that comes up in the next pod puts that pod in
// the set while the claim is still launching; the claim's later launches add their pods only once
// an agent registers, as every launch does. A wake's resume that the runtime refuses for the next
// pod puts the pod the refusal names in the set, and the retry that later comes up in another pod
// adds nothing before its agent registers. A wake whose launch an open pause's gate stops before
// it reaches any pod leaves the claim the release's (suspended): the release's bundle is still
// unrestored, so the launch after the pause puts its pod in the set. So does a resume whose start
// a daemon's suspension cuts short before it reaches any pod: that launch restored nothing, so
// the release's mark stays (Machine.releaseUnrestored, which launchNow leaves), and the daemon's
// resume after that suspension puts the pod it comes up in in the set. And the pod a launch after
// the release comes up in takes that mark off (addRestoringPod), since the bundle is sent once, to
// that pod: after a daemon's suspension, the daemon's resume into a third pod puts that pod in the
// set only once an agent registers there.
//
// The cases after them put no pod in the set at the resume (markRestoringLaunch). A claim the
// daemon suspended for its own rule was saved by no release. A project pause's stop suspends as
// released too, but the pause keeps the pod and saves no bundle, whether or not an agent of the
// claim ran. And a claim that never ran an agent anywhere, queued with no session and refused for
// a pod, has no work of its own in any bundle, whether a release or a pause's gate stopped it.
func TestTheFirstLaunchAfterAnIdleReleaseAddsItsPodToTheSet(t *testing.T) {
	released := func(t *testing.T) *harness {
		t.Helper()
		h := newHarness(t)
		h.inPod(refusedPod)
		h.must(RequestSuspend{Claim: h.token, Reason: "the idle pod was released", By: SuspendedByReleased})
		if c := h.claim(); !c.Released() {
			t.Fatalf("the claim is %s by %q, want it released", c.State, c.SuspendedBy)
		}
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod}}, "once released,")
		return h
	}

	t.Run("a resume that comes up in the next pod", func(t *testing.T) {
		h := released(t)
		h.rt.ScriptResume(fake.SpawnResult{Locator: podProcess(replacedPod, 2)})
		h.must(RequestRuleResume{Claim: h.token, Reason: "a wake for a delivery"})
		if c := h.claim(); c.State != StateLaunching || locatorPod(c.Locator) != replacedPod {
			t.Fatalf("the woken claim is %s at %+v, want it launching in pod %s", c.State, c.Locator, replacedPod)
		}
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod, replacedPod}}, "once the wake's resume came up in the next pod, before its agent registered,")
		h.connect()
		h.register()
		h.ready()

		h.rt.ScriptResume(fake.SpawnResult{Locator: podProcess(anotherPod, 3)})
		h.observe(runtime.Gone)
		if c := h.claim(); c.State != StateLaunching || locatorPod(c.Locator) != anotherPod {
			t.Fatalf("the relaunched claim is %s at %+v, want it launching in pod %s", c.State, c.Locator, anotherPod)
		}
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod, replacedPod}}, "once a later relaunch came up in a third pod, before its agent registered,")
		h.connect()
		h.register()
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod, replacedPod, anotherPod}}, "once that relaunch's agent registered,")
	})

	t.Run("a resume refused for the next pod", func(t *testing.T) {
		h := released(t)
		h.rt.OnResume = refusedFor(replacedPod)
		if err := h.handle(RequestRuleResume{Claim: h.token, Reason: "a wake for a delivery"}); !errors.Is(err, runtime.ErrAwaitingPodRound) {
			t.Fatalf("the wake's resume answered %v, want the runtime's wait for the pod's round", err)
		}
		if c := h.claim(); c.Locator != nil || locatorPod(c.AwaitsPod) != replacedPod {
			t.Fatalf("the refused claim records %+v and waits on %+v, want no process and the wait on pod %s", c.Locator, c.AwaitsPod, replacedPod)
		}
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod, replacedPod}}, "once the wake's resume was refused for the next pod,")

		h.rt.OnResume = nil
		h.rt.ScriptResume(fake.SpawnResult{Locator: podProcess(anotherPod, 3)})
		h.advance(podRoundRetry)
		if c := h.claim(); locatorPod(c.Locator) != anotherPod {
			t.Fatalf("the retry that went through left the claim at %+v, want a process in pod %s", c.Locator, anotherPod)
		}
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod, replacedPod}}, "once the retry came up in another pod, before its agent registered,")
	})

	t.Run("a wake whose launch a pause stops before any pod, then a resume after the pause", func(t *testing.T) {
		h, flag, _ := pauseHarness(t, queuedClaim())
		h.inPod(refusedPod)
		h.must(RequestSuspend{Claim: h.token, Reason: "the idle pod was released", By: SuspendedByReleased})
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod}}, "once released,")
		flag.set(7)
		if err := h.handle(RequestRuleResume{Claim: h.token, Reason: "a wake for a delivery"}); err != nil {
			t.Fatalf("the wake's resume under the open pause answered %v, want its launch held by the pause's gate", err)
		}
		h.advance(0)
		if c := h.claim(); !c.Released() || c.Locator != nil {
			t.Fatalf("the woken claim is %s by %q at %+v, want it stopped by the pause's gate as released before any pod", c.State, c.SuspendedBy, c.Locator)
		}
		h.wantCalls("Resume", 0)
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod}}, "once the pause's gate stopped its launch,")
		flag.set(0)
		h.rt.ScriptResume(fake.SpawnResult{Locator: podProcess(replacedPod, 2)})
		h.must(RequestRuleResume{Claim: h.token, Reason: "the pause ended"})
		if c := h.claim(); c.State != StateLaunching || locatorPod(c.Locator) != replacedPod {
			t.Fatalf("the resumed claim is %s at %+v, want it launching in pod %s", c.State, c.Locator, replacedPod)
		}
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod, replacedPod}}, "once its resume after the pause came up in the next pod, before its agent registered,")
	})

	t.Run("a resume whose start a daemon's suspension cuts short before any pod, then the daemon's resume", func(t *testing.T) {
		h := released(t)
		started, cut := make(chan struct{}), make(chan error, 1)
		h.rt.OnResume = func(ctx context.Context) error {
			close(started)
			<-ctx.Done() // the start waits in the runtime until something ends its context
			cut <- ctx.Err()
			return ctx.Err()
		}
		resuming := make(chan error, 1)
		go func() { resuming <- h.handle(RequestRuleResume{Claim: h.token, Reason: "a wake for a delivery"}) }()
		<-started
		suspending := make(chan error, 1)
		go func() {
			suspending <- h.handle(RequestSuspend{Claim: h.token, Reason: "its tree lingers", By: SuspendedByDaemon})
		}()
		if err := <-cut; err == nil {
			t.Fatal("the resume's start ran to completion, want it cut short by the suspension")
		}
		<-resuming
		if err := <-suspending; err != nil {
			t.Fatalf("the daemon's suspension answered %v, want the claim suspended", err)
		}
		h.rt.OnResume = nil
		if c := h.claim(); c.State != StateSuspended || c.SuspendedBy != SuspendedByDaemon || c.Locator != nil {
			t.Fatalf("the claim is %s by %q at %+v, want it suspended by the daemon before any pod", c.State, c.SuspendedBy, c.Locator)
		}
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod}}, "once the daemon's suspension cut the resume's start short before any pod,")

		h.rt.ScriptResume(fake.SpawnResult{Locator: podProcess(replacedPod, 3)})
		h.must(RequestRuleResume{Claim: h.token, Reason: "it owns its phase"})
		if c := h.claim(); c.State != StateLaunching || locatorPod(c.Locator) != replacedPod {
			t.Fatalf("the resumed claim is %s at %+v, want it launching in pod %s", c.State, c.Locator, replacedPod)
		}
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod, replacedPod}}, "once the daemon's resume came up in the pod that restored the release's bundle, before its agent registered,")
	})

	t.Run("a resume that came up in the next pod, then the daemon's suspension and resume into a third pod", func(t *testing.T) {
		h := released(t)
		h.rt.ScriptResume(fake.SpawnResult{Locator: podProcess(replacedPod, 2)})
		h.must(RequestRuleResume{Claim: h.token, Reason: "a wake for a delivery"})
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod, replacedPod}}, "once the wake's resume came up in the next pod, before its agent registered,")
		h.connect()
		h.register()
		h.ready()
		h.must(RequestSuspend{Claim: h.token, Reason: "its tree lingers", By: SuspendedByDaemon})
		if c := h.claim(); c.State != StateSuspended || c.SuspendedBy != SuspendedByDaemon {
			t.Fatalf("the claim is %s by %q, want it suspended by the daemon", c.State, c.SuspendedBy)
		}

		h.rt.ScriptResume(fake.SpawnResult{Locator: podProcess(anotherPod, 3)})
		h.must(RequestRuleResume{Claim: h.token, Reason: "it owns its phase"})
		if c := h.claim(); c.State != StateLaunching || locatorPod(c.Locator) != anotherPod {
			t.Fatalf("the resumed claim is %s at %+v, want it launching in pod %s", c.State, c.Locator, anotherPod)
		}
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod, replacedPod}}, "once the daemon's resume came up in a third pod, before its agent registered,")
	})

	t.Run("a resume of a claim the daemon suspended", func(t *testing.T) {
		h := newHarness(t)
		h.inPod(refusedPod)
		h.must(RequestSuspend{Claim: h.token, Reason: "its tree lingers", By: SuspendedByDaemon})
		h.rt.ScriptResume(fake.SpawnResult{Locator: podProcess(replacedPod, 2)})
		h.must(RequestRuleResume{Claim: h.token, Reason: "it owns its phase"})
		if c := h.claim(); c.State != StateLaunching || locatorPod(c.Locator) != replacedPod {
			t.Fatalf("the resumed claim is %s at %+v, want it launching in pod %s", c.State, c.Locator, replacedPod)
		}
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod}}, "once its resume came up in another pod, before its agent registered,")
	})

	t.Run("a resume after a pause's stop of a claim that ran", func(t *testing.T) {
		h, flag, _ := pauseHarness(t, queuedClaim())
		h.inPod(refusedPod)
		flag.set(7)
		h.must(pauseStop(h.token, 7))
		if c := h.claim(); !c.Released() {
			t.Fatalf("the claim is %s by %q, want it stopped by the pause as released", c.State, c.SuspendedBy)
		}
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod}}, "once the pause stopped it,")
		flag.set(0)
		h.rt.ScriptResume(fake.SpawnResult{Locator: podProcess(replacedPod, 2)})
		h.must(RequestRuleResume{Claim: h.token, Reason: "the pause ended"})
		if c := h.claim(); c.State != StateLaunching || locatorPod(c.Locator) != replacedPod {
			t.Fatalf("the resumed claim is %s at %+v, want it launching in pod %s", c.State, c.Locator, replacedPod)
		}
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod}}, "once its resume after the pause came up in another pod, before its agent registered,")
	})

	// The tester's order (dispatch://INFRA-4425/artifact/tester-pausestop-test-md): the pause's
	// gate stops the never-run claim's retry, and its resume by rule is refused for the pod again.
	t.Run("a resume after a pause's stop of a claim that never ran", func(t *testing.T) {
		h, flag, _ := pauseHarness(t, queuedClaim())
		h.rt.OnSpawn = refusedFor(refusedPod)
		if err := h.handle(RequestSpawn{Claim: h.token}); !errors.Is(err, runtime.ErrAwaitingPodRound) {
			t.Fatalf("the first launch answered %v, want the runtime's wait for the pod's round", err)
		}
		flag.set(7)
		h.advance(podRoundRetry)
		h.advance(0)
		if c := h.claim(); !c.Released() || c.Session != "" {
			t.Fatalf("the claim is %s by %q with session %q, want it stopped by the pause's gate as released with no session", c.State, c.SuspendedBy, c.Session)
		}
		h.wantRegistered(RegisteredPods{}, "once the pause stopped it,")
		flag.set(0)
		if err := h.handle(RequestRuleResume{Claim: h.token, Reason: "the pause ended"}); err != nil && !errors.Is(err, runtime.ErrAwaitingPodRound) {
			t.Fatalf("the resume answered %v, want nil or the runtime's wait for the pod's round", err)
		}
		if c := h.claim(); c.Locator != nil || locatorPod(c.AwaitsPod) != refusedPod {
			t.Fatalf("the resumed claim records %+v and waits on %+v, want no process and the wait on pod %s", c.Locator, c.AwaitsPod, refusedPod)
		}
		h.wantRegistered(RegisteredPods{}, "once its launch after the pause was refused for the pod again,")
	})

	t.Run("a resume after an idle release of a claim that never ran", func(t *testing.T) {
		h := newHarness(t)
		h.rt.OnSpawn = refusedFor(refusedPod)
		if err := h.handle(RequestSpawn{Claim: h.token}); !errors.Is(err, runtime.ErrAwaitingPodRound) {
			t.Fatalf("the first launch answered %v, want the runtime's wait for the pod's round", err)
		}
		if err := h.handle(RequestSuspend{Claim: h.token, Reason: "the idle pod was released", By: SuspendedByReleased}); err != nil && !errors.Is(err, ErrSuspendHeld) {
			t.Fatalf("the release's suspension answered %v, want nil or held for the waiting launch", err)
		}
		h.advance(podRoundRetry)
		if c := h.claim(); !c.Released() || c.Session != "" {
			t.Fatalf("the claim is %s by %q with session %q, want it released with no session", c.State, c.SuspendedBy, c.Session)
		}
		h.wantRegistered(RegisteredPods{}, "once released,")
		if err := h.handle(RequestRuleResume{Claim: h.token, Reason: "a wake for a delivery"}); err != nil && !errors.Is(err, runtime.ErrAwaitingPodRound) {
			t.Fatalf("the wake's resume answered %v, want nil or the runtime's wait for the pod's round", err)
		}
		if c := h.claim(); c.Locator != nil || locatorPod(c.AwaitsPod) != refusedPod {
			t.Fatalf("the woken claim records %+v and waits on %+v, want no process and the wait on pod %s", c.Locator, c.AwaitsPod, refusedPod)
		}
		h.wantRegistered(RegisteredPods{}, "once its wake's launch was refused for the pod again,")
	})
}

// A machine built over a stored claim does not know where the claim registered before, so it reads
// the set off the stored claim (bootRegisteredPods), and the set it reads holds no pod. Where the
// stored claim shows a session it held, now or abandoned (Claim.Abandoned), the set is Unknown;
// where it shows none, the set is known and empty. A registered agent, whose process runs in a pod
// or in no pod (a tmux pane), a session whose agent has not registered since its launch, a session
// with no process and an abandoned one all read Unknown: a stored claim whose state holds a
// registered agent always shows the session that agent registered, so the pod its process runs in
// would decide nothing, and it is not read.
//
// The cases after the table drive it on. A registered agent's claim reads Unknown through the
// restart, and a relaunch's registration in another pod after the restart adds that pod and
// leaves the set Unknown: where an earlier agent registered stays unknown. And a registration
// through a process in no pod adds nothing.
func TestAMachineBuiltOverAStoredClaimReadsTheSetOffIt(t *testing.T) {
	inPod := podProcess(refusedPod, 4)
	pane := runtime.Locator{Runtime: runtime.RuntimeTmux, Claim: testToken, Incarnation: "4242:1",
		Tmux: &runtime.TmuxLocator{Window: "@1", Pane: "%1"}}
	for name, tc := range map[string]struct {
		claim Claim
		want  RegisteredPods
	}{
		"no session and none abandoned": {claim: queuedClaim(), want: RegisteredPods{}},
		"a registered agent whose process is in a pod": {claim: func() Claim {
			c := queuedClaim()
			c.State, c.Session, c.SessionFile, c.Locator = StateReady, session, sessionFile, &inPod
			return c
		}(), want: RegisteredPods{Unknown: true}},
		"a registered agent whose process is in no pod": {claim: func() Claim {
			c := queuedClaim()
			c.State, c.Session, c.SessionFile, c.Locator = StateReady, session, sessionFile, &pane
			return c
		}(), want: RegisteredPods{Unknown: true}},
		"a session whose agent has not registered since its launch into a pod": {claim: func() Claim {
			c := queuedClaim()
			c.State, c.Session, c.SessionFile, c.Locator = StateLaunching, session, sessionFile, &inPod
			return c
		}(), want: RegisteredPods{Unknown: true}},
		"a session and no process": {claim: func() Claim {
			c := queuedClaim()
			c.State, c.SuspendedBy, c.Session, c.SessionFile = StateSuspended, SuspendedByOperator, session, sessionFile
			return c
		}(), want: RegisteredPods{Unknown: true}},
		"an abandoned session and none now": {claim: func() Claim {
			c := queuedClaim()
			c.Abandoned = AbandonedSession{Session: "ses_before", Why: "no store holds it", At: epoch}
			return c
		}(), want: RegisteredPods{Unknown: true}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newBareHarness(t)
			h.store.PutClaim(h.ctx, tc.claim)
			h.start(h.store.load(testToken))
			h.wantRegistered(tc.want, "built over the stored claim,")
		})
	}

	t.Run("a registered agent's claim through a restart, and a registration after it", func(t *testing.T) {
		h := newHarness(t)
		h.inPod(refusedPod)
		h.wantRegistered(RegisteredPods{Pods: []string{refusedPod}}, "once ready in the pod,")

		h.restart()
		h.wantRegistered(RegisteredPods{Unknown: true}, "after the restart,")
		h.diesIntoPod(replacedPod)
		h.wantRegistered(RegisteredPods{Pods: []string{replacedPod}, Unknown: true}, "once a relaunch registered in another pod after the restart,")
	})

	t.Run("a registration through a process in no pod", func(t *testing.T) {
		c := queuedClaim()
		c.Abandoned = AbandonedSession{Session: "ses_before", Why: "no store holds it", At: epoch}
		h := newHarnessOf(t, c)
		h.wantRegistered(RegisteredPods{Unknown: true}, "built over a claim that abandoned a session,")
		h.reach(StateRegistered)
		if loc := h.claim().Locator; loc == nil || loc.Sandbox != nil {
			t.Fatalf("the claim registered through %+v, want a process in no pod", loc)
		}
		h.wantRegistered(RegisteredPods{Unknown: true}, "once a session registered through a process in no pod,")
	})
}

// StoredClaim clears every field the machine overlays on the claim it reports (current), each of
// which only the machine holds, in memory: with each of the machine's memory set, the claim it
// reports carries each overlay, and that claim in its stored form equals the claim the machine
// holds in its stored form, which carries none. A field current comes to overlay that StoredClaim
// leaves set fails here once this test sets its memory.
func TestStoredClaimClearsEveryFieldTheMachineOverlays(t *testing.T) {
	h := newHarness(t)
	h.inPod(refusedPod)
	awaited := podProcess(replacedPod, 2)
	h.m.mu.Lock()
	h.m.held = &RequestSuspend{Claim: h.token, By: SuspendedByOperator}
	h.m.compacting = &compacting{}
	h.m.processGone = true
	h.m.awaitsPod = &awaited
	reported, held := h.m.current(), copyClaim(h.m.claim)
	h.m.held, h.m.compacting, h.m.processGone, h.m.awaitsPod = nil, nil, false, nil
	h.m.mu.Unlock()

	if !reported.SuspensionHeld || reported.SuspensionHeldBy != SuspendedByOperator || !reported.Compacting || !reported.ProcessGone ||
		reported.AwaitsPod == nil || len(reported.RegisteredPods.Pods) == 0 {
		t.Fatalf("the claim the machine reports carries %+v, want every overlay set", reported)
	}
	if stored, want := StoredClaim(reported), StoredClaim(held); !reflect.DeepEqual(stored, want) {
		t.Fatalf("the reported claim's stored form is\n%+v\nwant the held claim's\n%+v", stored, want)
	}
}
