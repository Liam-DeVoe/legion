package daemon

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// prepareBasis readies an event of an ancestor's document, of its approval request or of its
// issue before it is applied (intake's ConsumerSpec.PrepareBasis), and is the event to apply. A
// step of another root that waits, closed, on such a request opens or ends its wait from
// Dispatch's state alone, never from the event, which a late delivery or an operator's replay of a
// given-up record can apply after a later event changed what it says (dispatch://INFRA-3320, R1).
// So for an event that is a cue for such a step (workflow.PendingBasisCues, read from the store in
// a read-only transaction, as the engine reads the same steps again under the fact lock) Dispatch
// is read here, outside the fact lock, and the read is set on the fact with the steps it was read
// for (intake.BasisStep): for a document event or the close of a request, the document's approval
// and the ancestor's issue (readBasis, intake.DispatchArtifact.BasisRead and
// intake.DispatchAskClosed.BasisRead, with BasisRead.For); for an event of the ancestor's issue,
// that issue (intake.DispatchIssue.AncestorRead, with ReadFor).
//
// An event that can be no cue reads nothing, not even the store (workflow.BasisCueEvent): an issue
// event that shows no leave of R1's first condition, a close of a question, and a person's answer
// of an ask. One that is a cue for no step reads the store alone. Where the preparation already
// holds Dispatch's view of the ancestor's issue, it reads the issue no second time: a person's
// approval carries it (prepareArtifact's intake.DispatchArtifact.Issue), and an observed issue
// event is that view itself (intake.DispatchIssue.Observed). A failed read fails the preparation
// with an error naming what it read, so the event is delivered again. A step registered, or
// registered on another request, after the store's read is none of the steps the read names, so
// the workflow queues that step's pending-basis seed to read Dispatch for it.
func (w *workflowRuntime) prepareBasis(ctx context.Context, fact intake.Fact) (intake.Fact, error) {
	if !workflow.BasisCueEvent(fact) {
		return fact, nil
	}
	cued, err := store.ReadOnly(ctx, w.pool, "read the steps waiting on an ancestor's request", func(ctx context.Context, tx pgx.Tx) ([]record.DesignGate, error) {
		return workflow.PendingBasisCues(ctx, w.records, tx, fact)
	})
	if err != nil || len(cued) == 0 {
		return fact, err
	}
	readFor := make([]intake.BasisStep, len(cued))
	for i, gate := range cued {
		readFor[i] = intake.BasisStepOf(gate)
	}
	basis := *cued[0].Basis
	switch fact := fact.(type) {
	case intake.DispatchIssue:
		read := intake.AncestorRead{Status: fact.Status, HandedOver: fact.HandedOver}
		if !fact.Observed {
			ancestor, err := w.dispatch.GetIssue(ctx, fact.Key)
			if err != nil {
				return fact, fmt.Errorf("read %s from Dispatch before applying its event %d, on whose request %s waits: %w", fact.Key, fact.Seq, cued[0].Issue, err)
			}
			read = intake.AncestorRead{Status: ancestor.Status, HandedOver: ancestor.HandedOver}
		}
		fact.AncestorRead, fact.ReadFor = &read, readFor
		return fact, nil
	case intake.DispatchArtifact:
		read, err := readBasis(ctx, w.dispatch, basis.ArtifactID, fact.Issue)
		if err != nil {
			return fact, fmt.Errorf("read Dispatch before applying event %d of %s, on whose request %s waits: %w", fact.Mark.Seq, basis.ArtifactID, cued[0].Issue, err)
		}
		read.For = readFor
		fact.BasisRead = &read
		return fact, nil
	case intake.DispatchAskClosed:
		read, err := readBasis(ctx, w.dispatch, basis.ArtifactID, nil)
		if err != nil {
			return fact, fmt.Errorf("read Dispatch before applying the close of request %s, on which %s waits: %w", fact.Ask, cued[0].Issue, err)
		}
		read.For = readFor
		fact.BasisRead = &read
		return fact, nil
	}
	return fact, nil
}

// readBasis is Dispatch's state now of artifactID, an ancestor's document that a step waits on,
// closed, for a person's approval of (intake.BasisRead): the document's approval, and the issue
// Dispatch names the document on, read as register_gate reads it, or taken from shown, a view of
// that issue the caller read already (nil for none). A cue's preparation (prepareBasis) and the
// pending-basis seed (the outbox's seedPendingBasis) read it alike, so the workflow judges one
// state of Dispatch by one verdict whichever carries it.
func readBasis(ctx context.Context, client dispatch.Client, artifactID string, shown *dispatch.Issue) (intake.BasisRead, error) {
	approval, err := client.Approval(ctx, artifactID)
	if err != nil {
		return intake.BasisRead{}, fmt.Errorf("read Dispatch approval %s: %w", artifactID, err)
	}
	if shown == nil || shown.Key != approval.IssueKey {
		ancestor, err := client.GetIssue(ctx, approval.IssueKey)
		if err != nil {
			return intake.BasisRead{}, fmt.Errorf("read Dispatch issue %s, the ancestor whose document %s is: %w", approval.IssueKey, artifactID, err)
		}
		shown = &ancestor
	}
	return intake.BasisRead{Approval: approval, Ancestor: intake.AncestorRead{Status: shown.Status, HandedOver: shown.HandedOver}}, nil
}
