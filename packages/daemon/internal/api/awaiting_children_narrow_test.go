package api

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// A caller below gateBasisContract knows neither awaiting_children word, so the value pass on that
// contract's row serves it the issue's phase as admitted and the held entry's reason as
// children_first (dispatch://INFRA-3320 R13); a caller at or above the contract reads the new
// words, which is the state as the source holds it. carriesGateBasis sees the value even in a
// state with no root carrying treeRuns, so the pass fires for it.
func TestAwaitingChildrenNarrowsToAdmittedAndChildrenFirstBelowItsContract(t *testing.T) {
	state := State{
		Issues: map[string]Issue{
			"LEGION-208": {Key: "LEGION-208", Phase: phase.AwaitingChildren},
			"LEGION-209": {Key: "LEGION-209", Phase: phase.Implementing},
		},
		Admission: Admission{Held: []HeldIssue{
			{Issue: "LEGION-208", Reason: record.HeldAwaitingChildren},
			{Issue: "LEGION-210", Reason: record.HeldDesignGate},
		}},
	}

	if !carriesGateBasis(state) {
		t.Fatalf("carriesGateBasis = false, want true: a state with an awaiting_children issue carries the value the pass narrows")
	}

	narrowed := withoutGateBasis(state)
	if got := narrowed.Issues["LEGION-208"].Phase; got != phase.Admitted {
		t.Errorf("a caller below 147 reads LEGION-208's phase %s, want admitted", got)
	}
	if got := narrowed.Issues["LEGION-209"].Phase; got != phase.Implementing {
		t.Errorf("a caller below 147 reads LEGION-209's phase %s, want implementing unchanged", got)
	}
	if got := narrowed.Admission.Held[0].Reason; got != record.HeldChildrenFirst {
		t.Errorf("a caller below 147 reads the held reason %s, want children_first", got)
	}
	if got := narrowed.Admission.Held[1].Reason; got != record.HeldDesignGate {
		t.Errorf("a caller below 147 reads the other held reason %s, want design_gate unchanged", got)
	}

	// A current caller reads the new words: withoutGateBasis leaves the source as it was.
	if got := state.Issues["LEGION-208"].Phase; got != phase.AwaitingChildren {
		t.Errorf("the source issue phase = %s, want awaiting_children left as it was", got)
	}
	if got := state.Admission.Held[0].Reason; got != record.HeldAwaitingChildren {
		t.Errorf("the source held reason = %s, want awaiting_children left as it was", got)
	}
}
