package api

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
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

// heldRowsServed is each row of the body's `admission.held` by issue key, as the wire carries it,
// so a key the row leaves off is absent from its map and not an empty value.
func heldRowsServed(t *testing.T, body []byte) map[string]map[string]any {
	t.Helper()
	var served struct {
		Admission struct {
			Held []map[string]any `json:"held"`
		} `json:"admission"`
	}
	if err := json.Unmarshal(body, &served); err != nil {
		t.Fatalf("decode state %s: %v", body, err)
	}
	rows := make(map[string]map[string]any, len(served.Admission.Held))
	for _, row := range served.Admission.Held {
		rows[row["issue"].(string)] = row
	}
	return rows
}

// A caller whose plugin declares a contract below OwnerPlanContract reads `admission.held`'s
// `reason` as a strict list of the eleven words before it, and refuses the whole body for
// another. So the rows of the two words that contract added, children_before_retro and
// task_stopped, are served to it as what it read for those issues before: unclassified, with no
// heldBy. Every other row is served as it stands, and a caller at the contract is served the two
// words with their heldBy. The state the source answered keeps the two words for the next caller.
func TestAnOlderCallerReadsTheNewHeldWordsAsUnclassified(t *testing.T) {
	const stoppedInMerge, beneath = "LEGION-233", "LEGION-235"
	state := heldIssues(t)
	golden := len(state.Admission.Held)
	state.Admission.Held = append(slices.Clone(state.Admission.Held),
		HeldIssue{Issue: childrenIssue, Reason: record.HeldChildrenBeforeRetro, HeldBy: []string{beneath}},
		HeldIssue{Issue: stoppedInMerge, Reason: record.HeldTaskStopped})
	if golden == 0 {
		t.Fatal("the state golden holds no held row, so nothing here shows a row of another word served as it stands")
	}

	at := heldRowsServed(t, stateFrom(t, state, statePodAddr+":41000", podClaims(OwnerPlanContract)))
	if row := at[childrenIssue]; row["reason"] != string(record.HeldChildrenBeforeRetro) || !reflect.DeepEqual(row["heldBy"], []any{beneath}) {
		t.Errorf("%s is served to a pod at the contract as %v, want children_before_retro held by %s", childrenIssue, row, beneath)
	}
	if row := at[stoppedInMerge]; row["reason"] != string(record.HeldTaskStopped) {
		t.Errorf("%s is served to a pod at the contract as %v, want task_stopped", stoppedInMerge, row)
	}

	below := heldRowsServed(t, stateFrom(t, state, statePodAddr+":41000", podClaims(OwnerPlanContract-1)))
	for _, key := range []string{childrenIssue, stoppedInMerge} {
		if row := below[key]; !reflect.DeepEqual(row, map[string]any{"issue": key, "reason": string(record.HeldUnclassified)}) {
			t.Errorf("%s is served to a pod one contract behind as %v, want unclassified with no heldBy", key, row)
		}
	}
	for _, row := range state.Admission.Held[:golden] {
		if served := below[row.Issue]; served["reason"] != string(row.Reason) {
			t.Errorf("%s is served to a pod one contract behind as %v, want its word %s as it stands", row.Issue, served, row.Reason)
		}
	}
	if got := state.Admission.Held[golden].Reason; got != record.HeldChildrenBeforeRetro {
		t.Errorf("the state the source answered holds %s's word as %q after the narrowing, want it kept", childrenIssue, got)
	}
}
