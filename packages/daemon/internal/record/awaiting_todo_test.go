package record

import (
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/phase"
)

// A new run that starts as a root's does and that Dispatch shows in work at a status other than
// todo waits for a person's todo (LineFacts.AwaitsTodo, R20 of dispatch://INFRA-3320) where it
// holds no slot row, and reads the word after it where it does: a new run whose promotion started
// its architect holds its row at the in_progress the promotion wrote, and its own gate, which that
// architect designs, is what holds it. The waiting line's drop rows carry that word for the slot
// holder (Waiting), and the stalled-tree measure reads them (admit's silencedTree, where
// design_gate silences a tree and awaiting_todo does not), so the slot is read on both paths. A
// first run, a new run at todo and a new run whose label is off read no awaiting_todo either.
func TestOnlyANewRunThatHoldsNoSlotAndStandsOutsideTodoAwaitsTodo(t *testing.T) {
	newRun := Issue{Key: "LEGION-900", Tree: "LEGION-900", Project: "LEGION", Phase: phase.Admitted, Generation: 2,
		Status: "in_progress", DispatchStatus: "in_progress", HandedOver: true}
	factsOf := func(issue Issue) LineFacts {
		return LineFacts{Chains: chainsOf([]Issue{issue}, copyOf(issue))}
	}

	if word, by := HeldReasonOf(newRun, false, factsOf(newRun)); word != HeldAwaitingTodo || by != nil {
		t.Fatalf("the new run holding no slot reads %s held by %v, want awaiting_todo held by no issue", word, by)
	}
	if word, by := HeldReasonOf(newRun, true, factsOf(newRun)); word != HeldDesignGate || !slices.Equal(by, []string{newRun.Key}) {
		t.Fatalf("the new run holding its slot reads %s held by %v, want design_gate held by its own gate", word, by)
	}
	for _, tc := range []struct {
		name  string
		slots []Slot
		want  HeldReason
	}{
		{"holding no slot", nil, HeldAwaitingTodo},
		{"holding its slot", []Slot{{Issue: newRun.Key}}, HeldDesignGate},
	} {
		if _, dropped := Waiting([]Issue{newRun}, tc.slots, factsOf(newRun)); len(dropped) == 0 || dropped[0].Reason != tc.want {
			t.Errorf("the waiting line leaves the new run %s behind as %+v, want its first row %s", tc.name, dropped, tc.want)
		}
	}

	firstRun := newRun
	firstRun.Generation = 1
	atTodo := newRun
	atTodo.Status, atTodo.DispatchStatus = "todo", "todo"
	labelOff := newRun
	labelOff.HandedOver = false
	for _, tc := range []struct {
		name  string
		issue Issue
	}{
		{"a first run", firstRun},
		{"a new run at todo", atTodo},
		{"a new run whose label is off", labelOff},
	} {
		if word, _ := HeldReasonOf(tc.issue, false, factsOf(tc.issue)); word == HeldAwaitingTodo {
			t.Errorf("%s reads awaiting_todo, want another word: it waits for no todo", tc.name)
		}
	}
}
