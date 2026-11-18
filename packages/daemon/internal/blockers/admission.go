package blockers

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// AdmissionJudgement is one judgement of a project's admission (JudgeAdmission): the block the
// state route serves under `admission` and a state_admission event carries, beside the parts of the
// judgement the route reads again for each issue's own view — the line each member's
// `waitingLine` is read from, the mirror rows and the facts each issue's `why` is read from, and
// the live trees each member's quiet measure is shown for.
type AdmissionJudgement struct {
	Block record.AdmissionBlock
	Line  []record.LineEntry
	Rows  map[string]Row
	Facts record.LineFacts
	// Live is the roots of the live trees (record.LiveRoots), which Block.Trees lists by key.
	Live map[string]struct{}
}

// JudgeAdmission is project's admission block, judged from one read of tx: the project's live
// records (record.Store.LiveIssues), its own slots among every project's (record.OwnSlots), the
// pass's facts (record.ReadLineFacts) under the design-gate steps the deployment gates, and the
// line those leave (JudgeLine). From them, under cap: `active`, the issues holding the project's
// own slots in slot order; `waiting`, the line in its order; `trees`, the live roots by key
// (record.LiveRoots); `blocked`, what waits on another issue (Blocked); and `held`, what the daemon
// holds in the workflow and runs nothing of (record.HeldList). now is the caller's clock, the one
// thing the judgement reads beside the records (JudgeLine).
//
// It is the one computation of the block, and both readers of it call it: the state route
// (projection.Project), which serves it, and the admission pass, which at the end of each of its
// passes writes the block's state event where the block changed (admit's queueAdmissionBlock).
// The pass's own read is the live records, which leave out a done record holding no slot, so the
// route judges the block from that read too, and the two cannot differ for any record of the
// project. The records a live read leaves out still reach the judgement where it needs them: a done
// parent on a chain (LineFacts.Parents) and a done or parked root (LineFacts.Roots), each read by
// ReadLineFacts.
func JudgeAdmission(ctx context.Context, tx pgx.Tx, store record.Store, project string, steps []record.DesignStep,
	cap int, now time.Time,
) (AdmissionJudgement, error) {
	issues, err := store.LiveIssues(ctx, tx, project)
	if err != nil {
		return AdmissionJudgement{}, fmt.Errorf("judge the admission of %s: %w", project, err)
	}
	every, err := store.Slots(ctx, tx)
	if err != nil {
		return AdmissionJudgement{}, fmt.Errorf("judge the admission of %s: %w", project, err)
	}
	slots := record.OwnSlots(issues, every)
	facts, err := record.ReadLineFacts(ctx, tx, store, project, issues, steps)
	if err != nil {
		return AdmissionJudgement{}, fmt.Errorf("judge the admission of %s: %w", project, err)
	}
	rows, facts, line, dropped, err := JudgeLine(ctx, tx, store, project, issues, slots, facts, nil, now)
	if err != nil {
		return AdmissionJudgement{}, fmt.Errorf("judge the admission of %s: %w", project, err)
	}
	active := make([]string, len(slots))
	for i, slot := range slots {
		active[i] = slot.Issue
	}
	waiting := make([]string, len(line))
	for i, entry := range line {
		waiting[i] = entry.Issue.Key
	}
	live := record.LiveRoots(issues, facts)
	return AdmissionJudgement{
		Block: record.AdmissionBlock{
			Cap:     cap,
			Active:  active,
			Waiting: waiting,
			Trees:   slices.Sorted(maps.Keys(live)),
			Blocked: Blocked(rows, issues, line, record.ChildWaitRows(facts.ChildWaits)),
			Held:    record.HeldList(issues, slots, line, dropped, facts),
		},
		Line:  line,
		Rows:  rows,
		Facts: facts,
		Live:  live,
	}, nil
}
