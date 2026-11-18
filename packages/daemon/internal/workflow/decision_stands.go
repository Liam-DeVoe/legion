package workflow

import (
	"github.com/sjawhar/legion/daemon/internal/classify"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// Decision is a person's review of a design document as a sender holds it when it asks whether
// the review is still the person's word (DecisionStands): what the person did (Kind), on which
// step's document (Step), the document itself when the sender knows it (Artifact, "" when it does
// not), the version reviewed when the sender knows it (Version, 0 when it does not: a kept
// decision's row records none), and where Dispatch recorded the review (Mark, empty where the
// sender holds no mark of it). A sender that holds only the review's Dispatch reference resolves
// its step once (RefStep).
type Decision struct {
	Kind     record.DecisionKind
	Step     record.DesignStep
	Artifact string
	Version  int
	Mark     record.EventMark
}

// Approval is a person's approval a gate records (record.DesignGate.ApprovedVersion and
// ApprovedMark): the one that superseded a review, as DecisionStands answers it.
type Approval struct {
	Version int
	Mark    record.EventMark
}

// DecisionStands is the one place that decides whether a person's recorded review of a design
// document is still their word, so a task or a notice may name it. Every sender asks it, of the
// gate the issue holds for d.Step as the record holds it when it asks (gate, nil where the issue
// holds none) and of whether the deployment gates that step (gated): the changes notice
// (tellChanges, and the registration's and the artifact event's reading of the gate before they
// apply the review), the resume of a root a person answered (resumeTask), the decisions and the
// approval kept for an architect's start or catch-up (KeptDecisionsTask, again at each send and
// each delivery) and the controller's relay of a decision nobody in its tree hears (the daemon's
// parkDecision); the clear of a generation asks it too, of each decision kept on the issues it
// clears, before it deletes their gates (Engine.ClearGeneration). It holds every guard that
// chooses whether the one rule is asked (classify.ReviewSuperseded), so a new step kind or a
// change to how an approval of the same version is read is made here once.
//
// It answers the approval that superseded the review, which a sender may name, and nil when the
// review stands. A review stands when nothing the gate records can be the person's later word: a
// decision that is no changes request or approval (an answer on an ask, a carried gate's
// opening), a document no gate of the issue holds, which is also a gate of another step than
// d.Step's (a step and a gate naming none are the spec step's, record.DesignStep.OrSpec), a gate
// that now holds another document than the one reviewed, a step the deployment does not gate (its
// approval is its registration's and no person's), and a gate with no approval. Otherwise the gate
// is asked whether its approval followed the review: at d.Version, or, where the sender knows no
// version, at the version the gate holds approved — a review of a lower one is answered by that
// approval, and one of that version is placed by its mark — and at d.Mark. Both marks are
// Dispatch's, so the two are ordered by Dispatch's sequences when both carry one and otherwise by
// its times (record.EventMark.After), never by the daemon's clock against Dispatch's. An approval
// the gate's approval is (the same version, and a mark that can be the same event:
// record.EventMark.Same) is no later word than itself, so it stands.
//
// One other function asks classify.ReviewSuperseded itself: requestStands
// (awaiting_reconcile.go). It is no sender of a task or notice about a person's recorded decision.
// It asks whether an architect's approval request of a document still waits on the person, a
// different question that the same classifier answers, so it does not go through this predicate.
func DecisionStands(gate *record.DesignGate, gated bool, d Decision) (superseded *Approval) {
	if d.Kind != record.DecisionDesignChangesRequested && d.Kind != record.DecisionDesignApproved {
		return nil
	}
	if gate == nil || gate.Step.OrSpec() != d.Step.OrSpec() || !gated || gate.ApprovedVersion == nil ||
		d.Artifact != "" && gate.ArtifactID != d.Artifact {
		return nil
	}
	version := d.Version
	if version == 0 {
		version = *gate.ApprovedVersion
	}
	if d.Kind == record.DecisionDesignApproved && version == *gate.ApprovedVersion && d.Mark.Same(gate.ApprovedMark) {
		return nil
	}
	if !classify.ReviewSuperseded(*gate, version, d.Mark) {
		return nil
	}
	return &Approval{Version: *gate.ApprovedVersion, Mark: gate.ApprovedMark}
}

// gateOf is the gate gates holds for step, nil where it holds none: the gate DecisionStands asks
// of a decision on step's document.
func gateOf(gates map[record.DesignStep]record.DesignGate, step record.DesignStep) *record.DesignGate {
	gate, ok := gates[step]
	if !ok {
		return nil
	}
	return &gate
}
