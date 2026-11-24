package supervise

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
)

// A launch the runtime refuses because the claim's issue pod cannot run it until that pod's update
// round or its release replaces it (runtime.AwaitsPodUpdate) is charged nothing and leaves the
// claim suspended by the daemon, running nothing, so no live claim keeps either release rule off
// the pod. The claim is the only claim of its pod here. The issue and the controller are told at
// the refusal, naming the pod; whether a start of the claim is queued is the daemon's to read from
// its outbox (workflow's podRefusedLaunch). While that pod is still the issue's pod, each resume is
// refused again with no launch and no write of the claim, so the pod comes to be quiet, and the
// telling comes again only after an hour. Once another pod stands, the next resume launches the
// claim.
//
// The control is the rule before this one: the refused claim is left launching, tried again at
// every probe interval, written at each try, and nobody is told.
func TestALaunchItsPodRefusesLeavesTheClaimSuspendedUntilAnotherPodStands(t *testing.T) {
	h := newHarness(t)
	var told []string
	h.deps.PodRefused = func(_ Claim, pod, _ string) { told = append(told, pod) }
	h.restart()
	issue := h.claim().Issue
	h.rt.ScriptIssuePod(issue, "pod-1")
	refusal := runtime.AwaitsPodUpdate{Pod: "pod-1", Reason: "launch " + string(testToken) + ": the pod pod-1 of " + issue + " was built without checker, the role of its step"}
	h.rt.ScriptSpawn(fake.SpawnResult{Err: refusal})

	if err := h.handle(RequestSpawn{Claim: testToken}); !errors.Is(err, runtime.ErrAwaitsPodUpdate) {
		t.Fatalf("the spawn answered %v, want the runtime's refusal, which is runtime.ErrAwaitsPodUpdate", err)
	}
	if c := h.claim(); c.State != StateSuspended || c.SuspendedBy != SuspendedByDaemon || c.Budgets != (Budgets{}) || c.Locator != nil ||
		!strings.Contains(c.LastStartFailure, refusal.Reason) {
		t.Fatalf("after the refusal the claim is %s by %q with budgets %+v, locator %+v and last start failure %q; want it suspended by the daemon, charged nothing, running nothing, keeping the refusal",
			c.State, c.SuspendedBy, c.Budgets, c.Locator, c.LastStartFailure)
	}
	if len(told) != 1 || told[0] != "pod-1" {
		t.Fatalf("the refusal told %q, want one report of pod-1", told)
	}

	writes := 0
	h.store.onPut = func(Claim) { writes++ }
	for range 3 {
		h.advance(testProbe)
		if err := h.handle(RequestRuleResume{Claim: testToken, Reason: "it owns its issue's step"}); !errors.Is(err, runtime.ErrAwaitsPodUpdate) {
			t.Fatalf("a resume while pod-1 stands answered %v, want a refusal that is runtime.ErrAwaitsPodUpdate", err)
		}
	}
	h.wantCalls("Spawn", 1)
	h.wantState(StateSuspended)
	if writes != 0 || len(told) != 1 {
		t.Fatalf("the resumes while pod-1 stands wrote the claim %d times and told %q; want no write and the one report", writes, told)
	}
	h.advance(time.Hour)
	if err := h.handle(RequestRuleResume{Claim: testToken, Reason: "it owns its issue's step"}); !errors.Is(err, runtime.ErrAwaitsPodUpdate) {
		t.Fatalf("a resume an hour later answered %v, want a refusal that is runtime.ErrAwaitsPodUpdate", err)
	}
	if len(told) != 2 || told[1] != "pod-1" || writes != 0 {
		t.Fatalf("an hour later the reports are %q and the claim was written %d times; want a second report of pod-1, and no write", told, writes)
	}

	h.rt.ScriptIssuePod(issue, "pod-2")
	if err := h.handle(RequestRuleResume{Claim: testToken, Reason: "it owns its issue's step"}); err != nil {
		t.Fatalf("the resume once another pod stands = %v, want the claim launched", err)
	}
	h.wantCalls("Spawn", 2)
	if c := h.claim(); c.Locator == nil {
		t.Fatalf("after the resume once another pod stands the claim records no process: %+v", c)
	}
}

// A runtime that cannot read which pod the issue's Sandbox holds now (runtime.IssuePodReader's
// read false) has not said the refusing pod is gone, so a resume while it cannot read is refused
// as one while that pod stands: nothing is launched and nothing of the claim written, and the
// telling comes again only after an hour, where forgetting the pod would launch into it at each
// try, be refused there again, write the claim and tell the issue and the controller each time.
// Once the runtime reads another pod, the next resume launches the claim.
func TestAResumeWhileTheRuntimeCannotReadTheIssuesPodIsRefusedAsWhileThePodStands(t *testing.T) {
	h := newHarness(t)
	var told []string
	h.deps.PodRefused = func(_ Claim, pod, _ string) { told = append(told, pod) }
	h.restart()
	issue := h.claim().Issue
	h.rt.ScriptIssuePod(issue, "pod-1")
	refusal := runtime.AwaitsPodUpdate{Pod: "pod-1", Reason: "launch " + string(testToken) + ": the pod pod-1 of " + issue + " was built without checker, the role of its step"}
	h.rt.ScriptSpawn(fake.SpawnResult{Err: refusal})
	if err := h.handle(RequestSpawn{Claim: testToken}); !errors.Is(err, runtime.ErrAwaitsPodUpdate) {
		t.Fatalf("the spawn answered %v, want the runtime's refusal", err)
	}

	h.rt.ScriptIssuePodUnread(issue)
	writes := 0
	h.store.onPut = func(Claim) { writes++ }
	for range 3 {
		h.advance(testProbe)
		if err := h.handle(RequestRuleResume{Claim: testToken, Reason: "it owns its issue's step"}); !errors.Is(err, runtime.ErrAwaitsPodUpdate) {
			t.Fatalf("a resume while the runtime cannot read the issue's pod answered %v, want a refusal that is runtime.ErrAwaitsPodUpdate", err)
		}
	}
	h.wantCalls("Spawn", 1)
	h.wantState(StateSuspended)
	if writes != 0 || len(told) != 1 {
		t.Fatalf("the resumes while the runtime cannot read the pod wrote the claim %d times and told %q; want no write and the one report", writes, told)
	}
	h.advance(time.Hour)
	if err := h.handle(RequestRuleResume{Claim: testToken, Reason: "it owns its issue's step"}); !errors.Is(err, runtime.ErrAwaitsPodUpdate) {
		t.Fatalf("a resume an hour later answered %v, want a refusal that is runtime.ErrAwaitsPodUpdate", err)
	}
	if len(told) != 2 || told[1] != "pod-1" || writes != 0 {
		t.Fatalf("an hour later the reports are %q and the claim was written %d times; want a second report of pod-1 and no write", told, writes)
	}

	h.rt.ScriptIssuePod(issue, "pod-2")
	if err := h.handle(RequestRuleResume{Claim: testToken, Reason: "it owns its issue's step"}); err != nil {
		t.Fatalf("the resume once another pod stands = %v, want the claim launched", err)
	}
	h.wantCalls("Spawn", 2)
}
