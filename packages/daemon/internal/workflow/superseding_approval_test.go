package workflow

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// SupersedingApproval asks the gate it is handed only when that gate is of the step the review is of
// (Decision.Step). A person's changes request on the decisions document, answered by an approval
// of a later version, is superseded by that approval where the gate is the decisions step's. The
// same approval on a gate of another step is no word of the person's on the document reviewed:
// the review stands, as it does where the issue holds no gate of its step at all. A step and a gate
// naming none are the spec step's, as the record reads them (record.DesignStep.OrSpec).
func TestADecisionIsAskedOnlyOfTheGateOfItsOwnStep(t *testing.T) {
	approved := 3
	gateAt := func(step record.DesignStep) *record.DesignGate {
		return &record.DesignGate{Issue: "LEGION-208", Step: step, ArtifactID: "artifact-208", LatestVersion: 3, ApprovedVersion: &approved,
			ApprovedMark: record.EventMark{Seq: 9}}
	}
	review := func(step record.DesignStep) Decision {
		return Decision{Kind: record.DecisionDesignChangesRequested, Step: step, Version: 2, Mark: record.EventMark{Seq: 5}}
	}
	for _, tc := range []struct {
		name       string
		gate       *record.DesignGate
		d          Decision
		superseded *Approval
	}{
		{name: "the gate of the step reviewed", gate: gateAt(record.DesignStepDecisions), d: review(record.DesignStepDecisions),
			superseded: &Approval{Version: 3}},
		{name: "a gate of another step", gate: gateAt(record.DesignStepSpec), d: review(record.DesignStepDecisions)},
		{name: "a gate naming no step, of a review of the spec step", gate: gateAt(""), d: review(record.DesignStepSpec),
			superseded: &Approval{Version: 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SupersedingApproval(tc.gate, true, tc.d)
			if (got == nil) != (tc.superseded == nil) || got != nil && *got != *tc.superseded {
				t.Fatalf("SupersedingApproval(gate of step %q, review of step %q) = %+v, want %+v", tc.gate.Step, tc.d.Step, got, tc.superseded)
			}
		})
	}
}

// A person's changes request on a root's Verification document is asked of its criteria gate
// wherever a person approves that step: a deployment that gates criteria, and one that gates spec
// and not criteria, where the root registered the document as criteria and one approval opens both
// (record.TakesStep). The approval of a later version answers the request in both, so neither a
// kept decision nor a queued notice tells the architect a request the person has since approved.
// Where no person approves criteria (a deployment that gates neither spec nor criteria), the gate's
// approval is its registration's, and the request stands.
func TestAChangesRequestOnTheVerificationDocumentIsAskedWhereAPersonApprovesIt(t *testing.T) {
	approved := 3
	gates := map[record.DesignStep]record.DesignGate{record.DesignStepCriteria: {Issue: "LEGION-208",
		Step: record.DesignStepCriteria, ArtifactID: "verification-208", LatestVersion: 3, ApprovedVersion: &approved,
		ApprovedMark: record.EventMark{Seq: 9}}}
	review := Decision{Kind: record.DecisionDesignChangesRequested, Version: 2, Mark: record.EventMark{Seq: 5}}
	ref := stepRef("LEGION-208", record.DesignStepCriteria)
	for _, tc := range []struct {
		name       string
		steps      []record.DesignStep
		superseded bool
	}{
		{name: "a deployment that gates criteria", steps: []record.DesignStep{record.DesignStepSpec, record.DesignStepCriteria},
			superseded: true},
		{name: "a deployment that gates spec and not criteria", steps: []record.DesignStep{record.DesignStepSpec}, superseded: true},
		{name: "a deployment that gates neither", steps: []record.DesignStep{record.DesignStepDecisions}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RefSuperseded(gates, tc.steps, "LEGION-208", ref, review)
			if (got != nil) != tc.superseded {
				t.Fatalf("RefSuperseded(criteria gate approved at 3, steps %v, a changes request of version 2) = %+v, want superseded %t",
					tc.steps, got, tc.superseded)
			}
		})
	}
}
