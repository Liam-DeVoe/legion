package workflow

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// ClearGeneration empties the facts one generation of issue owns (record.Store.ClearGeneration):
// a merged or closed pull request, the last generation's reading of a still-open one, its design
// gates, and each role's handoff, review rounds, verdict and summary. If the clear deletes gate
// rows of issue, the empty set of design-gate documents is queued for it, due at now
// (publishEmptied). issue is the record as tx holds it: the decisions asked first are those its
// tree keeps on it.
//
// It and ClearTreeGeneration are the only callers that clear a generation: one issue's, for an
// adopted member and a re-entered child, or a whole tree's, for a re-admitted root. The record
// owns the statements, and each takes the one forget as an input it requires (forgetSuperseded,
// which is ForgetSuperseded, the forget every start and catch-up runs), which it runs over every
// decision kept on a cleared issue while the issue's gates still stand, so each one a later
// approval superseded is deleted before the clear deletes the gate that records that approval.
// Asked after the clear, a gate that is gone or new would make the decision read as the person's
// word again: a person who requested changes and then approved the latest version as their
// hand-over would send the new architect to revise the spec they approved.
func (e *Engine) ClearGeneration(ctx context.Context, tx pgx.Tx, issue record.Issue, now time.Time) error {
	emptied, err := e.store.ClearGeneration(ctx, tx, issue, e.forgetSuperseded)
	if err != nil {
		return err
	}
	if err := e.publishEmptied(ctx, tx, emptied, now); err != nil {
		return fmt.Errorf("clear the generation of %s: %w", issue.Key, err)
	}
	return nil
}

// ClearTreeGeneration empties them for every issue of tree, and forgets first as ClearGeneration
// says. A root set back to todo starts a new generation of the whole tree, and its children
// carried the old one's pull request, gate and handoffs into it — a child re-entered at the new
// generation read a handoff of the last.
func (e *Engine) ClearTreeGeneration(ctx context.Context, tx pgx.Tx, tree string, now time.Time) error {
	emptied, err := e.store.ClearTreeGeneration(ctx, tx, tree, e.forgetSuperseded)
	if err != nil {
		return err
	}
	if err := e.publishEmptied(ctx, tx, emptied, now); err != nil {
		return fmt.Errorf("clear the generation of tree %s: %w", tree, err)
	}
	return nil
}

// forgetSuperseded is ForgetSuperseded over the engine's store and gated steps, in the shape a
// clear is handed (record.KeptForget). A clear names nothing to an architect, so the gates
// ForgetSuperseded read for a task's text are dropped.
func (e *Engine) forgetSuperseded(ctx context.Context, tx pgx.Tx, tree string, owed record.KeptDecisions) error {
	_, err := ForgetSuperseded(ctx, tx, e.store, e.cfg.Steps, tree, owed)
	return err
}

// publishEmptied queues in tx, due at now, one publication of the empty set of design-gate
// documents for each issue a clear emptied the gates of (record.Store.ClearGeneration answers
// them): those gates recorded a document until the clear, and Dispatch holds the set they
// recorded until something tells it otherwise. It is queued through the engine's own store,
// which in the daemon wakes the issue's board lane once the clear commits (projectRecords), so
// the publication is sent then rather than at the lane's next sweep.
func (e *Engine) publishEmptied(ctx context.Context, tx pgx.Tx, emptied []string, now time.Time) error {
	for _, issue := range emptied {
		if err := e.store.EnqueueDesignGates(ctx, tx, record.DesignGatesPublish{Issue: issue, Gates: record.GateDocuments(nil)}, now); err != nil {
			return err
		}
	}
	return nil
}
