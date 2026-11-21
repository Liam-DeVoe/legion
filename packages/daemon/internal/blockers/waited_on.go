package blockers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// ReadFanout is how many blocker keys one controller tick reads one at a time: up to this many are
// read singly, and above it the whole project is listed once instead (ReadPlan). The listing is a
// single unpaged request and Dispatch caps an issue at 20 blockers, so the fan-out a waiting set
// can ask for is bounded on both sides of the choice.
const ReadFanout = 25

// WaitedOn is the distinct keys of everything project's daemon holds something back for, read in
// tx: the judgement the deciders themselves make, read rather than remade (Blocked), over every
// root on the waiting line (record.Waiting), every recorded child a blocker holds at
// phase.Admitted, and every open Dispatch child holding a parent's own first phase
// (record.ChildWaitsOf), judged under rule with the deployment's gated steps. A candidate the rule
// exempts is read for nothing, as promotion admits it whatever its links say and nothing consults
// the answer (Exempt), and the keys come out in that judgement's own order, by the issue waiting
// on them.
//
// An open child is in the set because the tick's re-read is what writes its row of the copy
// again: a reading of all five values, each of which replaces the one an older reading wrote. A
// child the architect filed and has not released has no event the daemon consumes — it reaches
// the copy from a reading of Dispatch alone — so without this its row would stand until the next
// boot, and its move to icebox, to another parent or out from under its parent would not end the
// wait it holds.
//
// It is the one selection of the keys a tick re-reads, which the daemon's tick
// (internal/daemon's waitedOn) and a replay of a recorded window (internal/replay, which builds the
// tick's observation from its copy of Dispatch) both call, so the two read the same keys. The
// judgement drops the held rows, so now decides nothing here; it is the caller's own clock.
func WaitedOn(ctx context.Context, tx pgx.Tx, store record.Store, project string, steps []record.DesignStep, rule record.SlotRule,
	now time.Time,
) ([]string, error) {
	issues, err := store.LiveIssues(ctx, tx, project)
	if err != nil {
		return nil, fmt.Errorf("list the issues of %s: %w", project, err)
	}
	slots, err := store.Slots(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("list the admission slots: %w", err)
	}
	facts, err := record.ReadLineFactsUnder(ctx, tx, store, project, issues, steps, rule)
	if err != nil {
		return nil, err
	}
	// Judged through the one judgement admission itself promotes from (JudgeLine): the rows of
	// every issue a blocker can hold come before the line, which leaves such an issue out and
	// cannot judge that itself (record.LineFacts.Held); the issues the line names are read after
	// it, since only the line says which they are.
	rows, facts, line, _, err := JudgeLine(ctx, tx, store, project, issues, slots, facts, nil, now)
	if err != nil {
		return nil, err
	}
	var keys []string
	seen := make(map[string]struct{})
	for _, blocked := range Blocked(rows, issues, line, record.ChildWaitRows(facts.ChildWaits)) {
		for _, blocker := range blocked.BlockedBy {
			if _, repeated := seen[blocker]; repeated {
				continue
			}
			seen[blocker] = struct{}{}
			keys = append(keys, blocker)
		}
	}
	return keys, nil
}

// ReadPlan is how a tick reads keys, the keys WaitedOn names, for project: listProject when they
// number more than ReadFanout, which reads the project's whole listing once and takes every
// summary of it, and single, the keys read one at a time — every key below the fan-out, and above
// it only the keys of another project, which the project's listing does not hold. The daemon's
// tick and a replay's both read by it.
func ReadPlan(project string, keys []string) (listProject bool, single []string) {
	if len(keys) <= ReadFanout {
		return false, keys
	}
	for _, key := range keys {
		if keyProject, _, _ := strings.Cut(key, "-"); keyProject != project {
			single = append(single, key)
		}
	}
	return true, single
}
