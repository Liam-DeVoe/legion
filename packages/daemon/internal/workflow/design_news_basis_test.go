package workflow

import (
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// basisGates is newsGates with the spec's version 5 standing open on an ancestor's approval
// (record.OpenOnBasis): the root's architect registered it on a person's approval of version 7 of
// LEGION-100's primary document, and no person approved version 5 of the root's own spec.
func basisGates() map[record.DesignStep]record.DesignGate {
	gates := newsGates()
	spec := gates[record.DesignStepSpec]
	spec.ApprovedVersion, spec.ApprovedMark = new(4), record.EventMark{Seq: 20}
	gates[record.DesignStepSpec] = record.OpenOnBasis(spec, record.GateBasis{Issue: "LEGION-100", ArtifactID: "artifact-100", Version: 7,
		Approval: record.EventMark{Seq: 40}, Approver: "user:alice", Reason: "The root delivers P2.", Session: "ses-architect",
		RegisteredAt: time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)})
	return gates
}

// A task of news about a version that stands approved on an ancestor's approval says so in the
// words of the opened message, never that a person approved that version: the holder's item and
// the root architect's summary name the step's basis, the summary of such versions alone opens on
// the ancestor's approval, and a gate sentence the task leads with is the opened message of a
// basis. The control is the same news over the person's approval of version 5 (newsGates), which
// reads as it always has.
func TestNewsOfAVersionOpenOnABasisNamesTheAncestorsApproval(t *testing.T) {
	const basis = "The spec step stands open on an ancestor's approval: a person approved version 7 of"
	const noPerson = "No person approved version 5 of its spec document (artifact-spec)."
	holder := RenderNews(News{Audience: record.AudienceHolder, Tree: newsTree, Issue: newsTree, Phase: phase.Implementing,
		Steps: newsSteps, Gates: basisGates(), OpeningOwed: true, Items: []NewsChange{specChanges(4)}})
	t.Logf("the holder's news:\n%s", holder)
	for _, want := range []string{basis, noPerson, "every step the deployment gates now stands open. A merge the closed gate held is released. " + basis} {
		if !strings.Contains(holder, want) {
			t.Errorf("the holder's news of a version open on a basis does not say %q:\n%s", want, holder)
		}
	}
	if strings.Contains(holder, "a person approved version 5") {
		t.Errorf("the holder's news says a person approved version 5, which a basis opened:\n%s", holder)
	}

	summary := RenderNews(News{Audience: record.AudienceArchitect, Tree: newsTree, Steps: newsSteps, Gates: basisGates(),
		Items: []NewsChange{specChanges(4)}})
	t.Logf("the summary:\n%s", summary)
	for _, want := range []string{"A new version of the design of LEGION-208 stands approved on an ancestor's approval.", basis, noPerson} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary of a version open on a basis does not say %q:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "A person approved a new version") {
		t.Errorf("the summary says a person approved the version a basis opened:\n%s", summary)
	}

	control := RenderNews(News{Audience: record.AudienceArchitect, Tree: newsTree, Steps: newsSteps, Gates: newsGates(),
		Items: []NewsChange{specChanges(4)}})
	if !strings.Contains(control, "A person approved a new version of the design of LEGION-208.") || strings.Contains(control, "ancestor") {
		t.Errorf("the summary of a person's approval reads otherwise than it did:\n%s", control)
	}
}
