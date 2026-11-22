package workflow

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// VerdictStep is one step of the task a Legion issue's run walks, as the writer of legion-verdict
// reads it (the daemon's legionInputs): its place, which keys a review step's verdict
// (store.StepVerdictTx), its type (#4611's plan.Type), and its agent: the claim the latest start of
// its place records, and for a reviewer's step no start recorded the issue's reviewer claim
// (stepAgent); "" where none is known.
type VerdictStep struct {
	Place record.StepPlace
	Type  plan.Type
	Agent claim.Token
}

// VerdictSteps is each step of the task issue's run walks (walk), in the task's order, with its
// type and its agent. It only reads.
func (e *Engine) VerdictSteps(ctx context.Context, tx pgx.Tx, issue record.Issue) ([]VerdictStep, error) {
	w, err := e.walk(ctx, tx, issue)
	if err != nil {
		return nil, err
	}
	steps := make([]VerdictStep, 0, len(w.Steps))
	for i, step := range w.Steps {
		if i >= len(w.Line) {
			continue
		}
		place := record.TaskPlace{Kind: record.PlaceStep, Position: i + 1}.StepPlace(w.Task)
		agent, err := e.stepAgent(ctx, tx, issue, place, w.Line[i])
		if err != nil {
			return nil, err
		}
		steps = append(steps, VerdictStep{Place: place, Type: step.Type, Agent: agent})
	}
	return steps, nil
}
