package api

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The two holds before a retro that FixBesideReviewContract added: an issue held while a fix of
// its task runs, and an issue held until each check a CI job runs passes at its head. Both stand
// from the retro, which keeps no slot.
const (
	fixHeldIssue = "LEGION-250"
	ciHeldIssue  = "LEGION-251"
)

// fixAndCIHolds is the state golden with the two holds before a retro beside its own issues and an
// escalated hold, the one reason every reader knows. Each of the two has the held row this daemon
// writes for it: no word of the held list names either hold yet, so both read unclassified.
func fixAndCIHolds(t *testing.T) State {
	t.Helper()
	state := populatedState(t)
	issues := make(map[string]Issue, len(state.Issues)+3)
	maps.Copy(issues, state.Issues)
	issues[fixHeldIssue] = heldFor(fixHeldIssue, phase.Retro, record.HoldForFix)
	issues[ciHeldIssue] = heldFor(ciHeldIssue, phase.Retro, record.HoldForCI)
	issues[escalatedIssue] = heldFor(escalatedIssue, phase.Implementing, record.HoldEscalated)
	state.Issues = issues
	state.Admission.Held = append(slices.Clone(state.Admission.Held),
		HeldIssue{Issue: fixHeldIssue, Reason: record.HeldUnclassified},
		HeldIssue{Issue: ciHeldIssue, Reason: record.HeldUnclassified})
	return state
}

// A caller whose plugin speaks a contract below FixBesideReviewContract reads `holdReason` with
// a `z.enum` of `escalated`, `task-stopped` and `children`, and refuses the whole body for another
// word. So it is served an issue held for a fix or for CI with no holdReason, as a hold of no
// reason is served: heldFrom stays, and so does every escalated hold. A caller at the contract is
// served both reasons. The rule is the value strip's (withoutUnadmittedHoldReason), so it follows
// the contract the caller states, else its pod's lowest, and the record route serves each issue as
// the state does. The state the source answered keeps both reasons for the next caller.
//
// Each reason's arrival is read from the declared table (answers.go's holdReasons) at
// FixBesideReviewContract. A reason the table leaves out reads as arriving at this daemon's own
// contract (fieldArrivals.heldBack), which is FixBesideReviewContract only until the next bump:
// from then on such a reason would be held back from a caller at FixBesideReviewContract, whose
// reader admits it.
func TestAnOlderCallerIsServedNoFixOrCIHoldReason(t *testing.T) {
	const remote = "192.0.2.1:41000"
	state := fixAndCIHolds(t)
	handler := stateServer(t, state, podClaims(DaemonAPIVersion))
	newer := map[string][2]string{
		fixHeldIssue:   {"fix", "retro"},
		ciHeldIssue:    {"ci", "retro"},
		escalatedIssue: {"escalated", "implementing"},
	}
	older := map[string][2]string{
		fixHeldIssue:   {"", "retro"},
		ciHeldIssue:    {"", "retro"},
		escalatedIssue: {"escalated", "implementing"},
	}
	for _, tc := range []struct {
		name   string
		served map[string][2]string
		want   map[string][2]string
	}{
		{"a caller stating the contract", holdsServed(t, stateStating(t, handler, remote, FixBesideReviewContract)), newer},
		{"a caller stating the contract before it", holdsServed(t, stateStating(t, handler, remote, FixBesideReviewContract-1)), older},
		{"a pod stating nothing at the contract before shapedAnswersContract",
			holdsServed(t, stateFrom(t, state, statePodAddr+":41000", podClaims(shapedAnswersContract-1))), older},
	} {
		for key, hold := range tc.want {
			if tc.served[key] != hold {
				t.Errorf("%s: %s is served held %q, want %q", tc.name, key, tc.served[key], hold)
			}
		}
	}
	for key, hold := range newer {
		if got := recordHoldServed(t, handler, key, FixBesideReviewContract); got != hold {
			t.Errorf("the record of %s is served to a caller at the contract held %q, want %q", key, got, hold)
		}
		if got := recordHoldServed(t, handler, key, FixBesideReviewContract-1); got != older[key] {
			t.Errorf("the record of %s is served to a caller one contract behind held %q, want %q", key, got, older[key])
		}
	}
	if got := state.Issues[fixHeldIssue].HoldReason; got != string(record.HoldForFix) {
		t.Errorf("the state the source answered holds %s's reason as %q after the narrowing, want it kept", fixHeldIssue, got)
	}
	for _, reason := range []record.HoldReason{record.HoldForFix, record.HoldForCI} {
		if arrival, declared := holdReasonArrivals.values[string(reason)]; !declared || arrival != FixBesideReviewContract {
			t.Errorf("the declared table records the hold reason %q at %d (declared: %v), want FixBesideReviewContract (%d): "+
				"an undeclared reason arrives at this daemon's own contract, which the next bump moves past the readers that admit it",
				reason, arrival, declared, FixBesideReviewContract)
		}
	}
}

// The state route's body for a caller whose plugin is at the contract before
// FixBesideReviewContract, asking from a pod whose claims record it and stating it: the state of
// fixAndCIHolds, each of the two holds before a retro served with no holdReason. Their held rows
// read unclassified at both contracts, since no word of the held list names either hold yet.
// `packages/contracts/src/legion-api.test.ts` parses the body with a frozen copy of the hold
// reasons and the held list that reader knows, which is the reader the strip is written for.
func TestStateBeforeTheFixHoldsGolden(t *testing.T) {
	server := stateServer(t, fixAndCIHolds(t), podClaims(FixBesideReviewContract-1))
	body := stateStating(t, server, statePodAddr+":41000", FixBesideReviewContract-1)
	holds := holdsServed(t, body)
	for _, key := range []string{fixHeldIssue, ciHeldIssue} {
		if holds[key] != [2]string{"", "retro"} {
			t.Errorf("%s is served held %q to a caller before the contract, want no reason and heldFrom retro", key, holds[key])
		}
	}
	golden(t, "state-before-fix-holds.json", json.RawMessage(body))
}
