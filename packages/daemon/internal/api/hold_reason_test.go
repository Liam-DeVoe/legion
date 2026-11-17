package api

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The three held issues the hold reasons are read on: a task its owner stopped at testing, a
// parent whose plan has no acceptance step waiting before its retro for the issues beneath it,
// and an escalated hold, the one reason a caller below OwnerPlanContract knows.
const (
	taskStoppedIssue = "LEGION-230"
	childrenIssue    = "LEGION-231"
	escalatedIssue   = "LEGION-232"
)

// heldFor is key held from from, for reason.
func heldFor(key string, from phase.Phase, reason record.HoldReason) Issue {
	return Issue{Key: key, Generation: 1, Phase: phase.Held, Status: "in_progress", Tree: key, Repo: "acme/widgets",
		HoldReason: string(reason), HeldFrom: string(from)}
}

// heldIssues is the state golden with the three held issues beside its own.
func heldIssues(t *testing.T) State {
	t.Helper()
	state := populatedState(t)
	issues := make(map[string]Issue, len(state.Issues)+3)
	maps.Copy(issues, state.Issues)
	issues[taskStoppedIssue] = heldFor(taskStoppedIssue, phase.Testing, record.HoldTaskStopped)
	issues[childrenIssue] = heldFor(childrenIssue, phase.Retro, record.HoldForChildren)
	issues[escalatedIssue] = heldFor(escalatedIssue, phase.Implementing, record.HoldEscalated)
	state.Issues = issues
	return state
}

// holdsServed is each issue's holdReason and heldFrom as the body carries them, "" for a key the
// body leaves out.
func holdsServed(t *testing.T, body []byte) map[string][2]string {
	t.Helper()
	var served struct {
		Issues map[string]struct {
			HoldReason string `json:"holdReason"`
			HeldFrom   string `json:"heldFrom"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(body, &served); err != nil {
		t.Fatalf("decode state %s: %v", body, err)
	}
	holds := make(map[string][2]string, len(served.Issues))
	for key, issue := range served.Issues {
		holds[key] = [2]string{issue.HoldReason, issue.HeldFrom}
	}
	return holds
}

// A caller whose plugin declares a contract below OwnerPlanContract reads `holdReason` with the
// one value `escalated` and refuses the whole body for another, so it is served an issue held for
// task-stopped or children with no holdReason, as a hold of no reason is served; heldFrom stays,
// and so does every escalated hold. A caller at the contract is served the three reasons. The
// state the source answered keeps the two reasons for the next caller.
func TestAnOlderCallerIsServedNoNewHoldReason(t *testing.T) {
	state := heldIssues(t)
	at := holdsServed(t, stateFrom(t, state, statePodAddr+":41000", podClaims(OwnerPlanContract)))
	want := map[string][2]string{
		taskStoppedIssue: {"task-stopped", "testing"},
		childrenIssue:    {"children", "retro"},
		escalatedIssue:   {"escalated", "implementing"},
	}
	for key, hold := range want {
		if at[key] != hold {
			t.Errorf("%s is served to a pod at the contract held %q, want %q", key, at[key], hold)
		}
	}

	below := holdsServed(t, stateFrom(t, state, statePodAddr+":41000", podClaims(OwnerPlanContract-1)))
	want[taskStoppedIssue] = [2]string{"", "testing"}
	want[childrenIssue] = [2]string{"", "retro"}
	for key, hold := range want {
		if below[key] != hold {
			t.Errorf("%s is served to a pod one contract behind held %q, want %q", key, below[key], hold)
		}
	}
	if got := state.Issues[taskStoppedIssue].HoldReason; got != string(record.HoldTaskStopped) {
		t.Errorf("the state the source answered holds %s's reason as %q after the narrowing, want it kept", taskStoppedIssue, got)
	}
}
