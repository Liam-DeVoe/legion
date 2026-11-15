package supervise

import (
	"context"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/phase"
)

// The task of a phase the workflow stopped under its worker (a parent moved into
// awaiting_children) is retired, and nothing of it is sent afterwards: not to the agent a
// relaunch or a resume brings back during the wait, and not after the return puts the issue back
// in that same phase at the same run, where the phase fence holds a task of that phase again. A
// task of another phase, another run or a later entry is not this phase's work and stays; the
// return's own start, on an entry of its own, is delivered as any start is.
func TestRetiringAStoppedPhasesTaskLeavesNothingOfItToSend(t *testing.T) {
	restored := fixture(StateReady)
	restored.Pending.Phase, restored.Pending.Generation, restored.Pending.Entry = phase.Implementing, restored.Generation, 3
	h := newHarnessOf(t, restored)
	if err := h.store.PutDelivery(h.ctx, testToken, *restored.Pending); err != nil {
		t.Fatalf("seed the restored delivery: %v", err)
	}
	// The issue stands in implementing again, as it does after the return: the fence that drops a
	// task of a phase the issue has left holds this one.
	h.deps.PhaseHolds = func(context.Context, string, phase.Phase, uint64) (bool, error) { return true, nil }
	// No agent is connected yet, so nothing is sent while the claim is built.
	h.start(restored)

	for _, other := range []struct {
		name       string
		phase      phase.Phase
		generation uint64
		entry      int64
	}{
		{name: "another phase", phase: phase.Reviewing, generation: restored.Generation, entry: 3},
		{name: "another run", phase: phase.Implementing, generation: restored.Generation + 1, entry: 3},
		{name: "a later entry than the stop names", phase: phase.Implementing, generation: restored.Generation, entry: 2},
	} {
		retired, err := h.m.RetireStoppedTask(h.ctx, other.phase, other.generation, other.entry)
		if err != nil || retired || h.m.Claim().Pending == nil {
			t.Fatalf("a retirement of %s = %t, %v, pending %+v; want the task left where it is", other.name, retired, err, h.m.Claim().Pending)
		}
	}

	retired, err := h.m.RetireStoppedTask(h.ctx, phase.Implementing, restored.Generation, 3)
	if err != nil || !retired {
		t.Fatalf("the retirement of the stopped phase's task = %t, %v; want it retired", retired, err)
	}
	if p := h.m.Claim().Pending; p != nil {
		t.Fatalf("the stopped phase's task is still pending as %+v", p)
	}

	// The relaunched agent connects and says it is ready: nothing of the stopped phase is sent.
	h.conns.Register(testToken, h.conn)
	h.must(RequestReady{Claim: testToken, Generation: restored.Generation, Session: session})
	h.wantPrompts(0)

	// The return's own start, on an entry of its own, reaches the worker.
	h.must(RequestDeliver{Claim: testToken, Task: "merge the default branch forward", ID: "outbox:9",
		Phase: phase.Implementing, Generation: restored.Generation, Entry: 4})
	if sent := h.wantPrompts(1)[0]; !strings.Contains(sent.Message, "merge the default branch forward") {
		t.Fatalf("prompt after the return = %+v, want the return's own task", sent)
	}
}
