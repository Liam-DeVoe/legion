package record

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The count of starts of one step is what carries a step record's id from its start to its end
// (workflow's StepID): the start raises it and the end reads it unchanged, in a transaction of its
// own, possibly after a restart. So the count is a row of the store, raised by one per start of
// that one place, apart from every other place and every other issue, and it keeps the agent and
// the moment the latest start wrote, which the end writes again.
func TestTheCountOfStartsOfAStepRisesByOneAndTheEndReadsItUnchanged(t *testing.T) {
	st := migratedStore(t)
	store := NewStore()
	testing3 := StepPlace{Task: "default", Kind: "step", Place: 3}
	first, later := time.Date(2026, 11, 17, 1, 0, 0, 0, time.UTC), time.Date(2026, 11, 17, 2, 0, 0, 0, time.UTC)
	inTx(t, st, func(tx pgx.Tx) {
		got, err := store.StepStarts(t.Context(), tx, "LEGION-1", testing3)
		if err != nil || got != (StepStart{}) {
			t.Fatalf("StepStarts before any start = %+v, %v; want the zero count, no agent and no moment", got, err)
		}
		for want := int64(1); want <= 2; want++ {
			starts, err := store.RaiseStepStarts(t.Context(), tx, "LEGION-1", testing3, "legion-legion-legion-1-tester", first)
			if err != nil || starts != want {
				t.Fatalf("RaiseStepStarts = %d, %v; want %d", starts, err, want)
			}
		}
		// A start of another place, of another kind at the same place, of another task and of
		// another issue each count apart.
		for _, other := range []struct {
			issue string
			place StepPlace
		}{
			{"LEGION-1", StepPlace{Task: "default", Kind: "step", Place: 2}},
			{"LEGION-1", StepPlace{Task: "default", Kind: "retro", Place: 3}},
			{"LEGION-1", StepPlace{Task: "a", Kind: "step", Place: 3}},
			{"LEGION-2", testing3},
		} {
			starts, err := store.RaiseStepStarts(t.Context(), tx, other.issue, other.place, "", first)
			if err != nil || starts != 1 {
				t.Fatalf("RaiseStepStarts of %s %+v = %d, %v; want its own first start", other.issue, other.place, starts, err)
			}
		}
		// The merge GitHub already made starts no merger, so its start names no agent, and the
		// count keeps the agent and the moment of the latest start.
		if _, err := store.RaiseStepStarts(t.Context(), tx, "LEGION-1", testing3, "", later); err != nil {
			t.Fatalf("RaiseStepStarts with no agent: %v", err)
		}
	})
	inTx(t, st, func(tx pgx.Tx) {
		got, err := store.StepStarts(t.Context(), tx, "LEGION-1", testing3)
		if err != nil || got != (StepStart{Starts: 3, Agent: "", StartedAt: later, Run: RunOpen}) {
			t.Fatalf("StepStarts after three starts = %+v, %v; want 3 starts and the last start's empty agent at %s, open", got, err, later)
		}
		ended, mark, err := store.EndStepStart(t.Context(), tx, "LEGION-1", testing3)
		if err != nil || mark != EndedNow || ended != (StepStart{Starts: 3, Agent: "", StartedAt: later, Run: RunEnded}) {
			t.Fatalf("EndStepStart of the open third start = %+v, %d, %v; want that start, ended now", ended, mark, err)
		}
		if _, err := store.RaiseStepStarts(t.Context(), tx, "LEGION-1", testing3, "legion-legion-legion-1-tester", first); err != nil {
			t.Fatalf("RaiseStepStarts: %v", err)
		}
		got, err = store.StepStarts(t.Context(), tx, "LEGION-1", testing3)
		if err != nil || got != (StepStart{Starts: 4, Agent: "legion-legion-legion-1-tester", StartedAt: first, Run: RunOpen}) {
			t.Fatalf("StepStarts after the fourth start = %+v, %v; want 4 starts with its agent and moment, open", got, err)
		}
	})
}

// One run of a step gets one end, decided in the daemon's own store in the transaction that ends
// it: the first end marks the latest start ended, and every later end of that run finds the mark
// and writes nothing, until a new start raises the count and clears it. So the linger's end and
// the hand-over's of one run, or two ends one fact writes, give one record one end.
func TestOneRunOfAStepEndsOnce(t *testing.T) {
	st := migratedStore(t)
	store := NewStore()
	merge := StepPlace{Task: "default", Kind: "merge", Place: 0}
	at := time.Date(2026, 11, 17, 1, 0, 0, 0, time.UTC)
	inTx(t, st, func(tx pgx.Tx) {
		if _, mark, err := store.EndStepStart(t.Context(), tx, "LEGION-1", merge); err != nil || mark != NotCounted {
			t.Fatalf("EndStepStart with no start counted = %d, %v; want a run no start counted", mark, err)
		}
		if _, err := store.RaiseStepStarts(t.Context(), tx, "LEGION-1", merge, "legion-legion-legion-1-merger", at); err != nil {
			t.Fatalf("RaiseStepStarts: %v", err)
		}
		if ended, mark, err := store.EndStepStart(t.Context(), tx, "LEGION-1", merge); err != nil || mark != EndedNow || ended.Starts != 1 {
			t.Fatalf("the first end = %+v, %d, %v; want the first start, ended now", ended, mark, err)
		}
		if _, mark, err := store.EndStepStart(t.Context(), tx, "LEGION-1", merge); err != nil || mark != AlreadyEnded {
			t.Fatalf("a second end of the same run = %d, %v; want the run already ended", mark, err)
		}
		if _, ok, err := store.EndUncountedStep(t.Context(), tx, "LEGION-1", merge, "", time.Time{}); err != nil || ok {
			t.Fatalf("EndUncountedStep of a place whose start ended = %t, %v; want none", ok, err)
		}
		if starts, err := store.RaiseStepStarts(t.Context(), tx, "LEGION-1", merge, "legion-legion-legion-1-merger", at); err != nil || starts != 2 {
			t.Fatalf("the next start = %d, %v; want the count 2", starts, err)
		}
		if ended, mark, err := store.EndStepStart(t.Context(), tx, "LEGION-1", merge); err != nil || mark != EndedNow || ended.Starts != 2 {
			t.Fatalf("the end of the next run = %+v, %d, %v; want the second start, ended now", ended, mark, err)
		}
	})
}

// A step that started under the release before has no count. Its first end writes the row, at
// the count 0 with the agent and the moment its start would have named (the moment unknown where
// no entry was raised), marked ended, so a second end of that run writes nothing and the next
// start counts 1.
func TestAStepNoStartCountedEndsOnce(t *testing.T) {
	st := migratedStore(t)
	store := NewStore()
	accepting := StepPlace{Task: "default", Kind: "step", Place: 5}
	at := time.Date(2026, 11, 17, 1, 0, 0, 0, time.UTC)
	inTx(t, st, func(tx pgx.Tx) {
		if starts, ok, err := store.EndUncountedStep(t.Context(), tx, "LEGION-1", accepting, "legion-legion-legion-1-architect", time.Time{}); err != nil || !ok || starts != 0 {
			t.Fatalf("the first end of an uncounted step = %d, %t, %v; want it written at the count 0", starts, ok, err)
		}
		got, err := store.StepStarts(t.Context(), tx, "LEGION-1", accepting)
		if err != nil || got != (StepStart{Agent: "legion-legion-legion-1-architect", Run: RunEnded}) {
			t.Fatalf("StepStarts after the end = %+v, %v; want the count 0, the agent, no moment, ended", got, err)
		}
		if _, mark, err := store.EndStepStart(t.Context(), tx, "LEGION-1", accepting); err != nil || mark != AlreadyEnded {
			t.Fatalf("EndStepStart after it = %d, %v; want the run already ended", mark, err)
		}
		if _, ok, err := store.EndUncountedStep(t.Context(), tx, "LEGION-1", accepting, "legion-legion-legion-1-architect", at); err != nil || ok {
			t.Fatalf("a second end of the uncounted step = %t, %v; want none", ok, err)
		}
		if starts, err := store.RaiseStepStarts(t.Context(), tx, "LEGION-1", accepting, "legion-legion-legion-1-architect", at); err != nil || starts != 1 {
			t.Fatalf("the next start = %d, %v; want the count 1", starts, err)
		}
	})
}

// A re-admission clears a generation's facts (ClearGeneration, ClearTreeGeneration), and the count
// is none of them: the generation is part of a record's id already, and a count that started again
// would give a later run of the same generation and place the id of an earlier one.
func TestAClearedGenerationKeepsTheCountOfStarts(t *testing.T) {
	st := migratedStore(t)
	store := NewStore()
	place := StepPlace{Task: "default", Kind: "merge", Place: 0}
	at := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	inTx(t, st, func(tx pgx.Tx) {
		if _, err := store.RaiseStepStarts(t.Context(), tx, "LEGION-1", place, "legion-legion-legion-1-merger", at); err != nil {
			t.Fatalf("RaiseStepStarts: %v", err)
		}
		if err := store.ClearGeneration(t.Context(), tx, "LEGION-1", at); err != nil {
			t.Fatalf("ClearGeneration: %v", err)
		}
		if err := store.ClearTreeGeneration(t.Context(), tx, "LEGION-1", at); err != nil {
			t.Fatalf("ClearTreeGeneration: %v", err)
		}
		got, err := store.StepStarts(t.Context(), tx, "LEGION-1", place)
		if err != nil || got.Starts != 1 {
			t.Fatalf("StepStarts after the clears = %+v, %v; want the one start kept", got, err)
		}
	})
}

// The roll forward of a rollback marks each place of an issue the earlier release moved uncounted
// (store migration 20261117064152), the place's run open or ended at the rollback. Its row counted
// a run that is over, so an end there finds no counted run: it ends a run of its own, at the count
// after the row's, and the row's run keeps its id and its mark. A second end of that run writes
// nothing, and the next start counts on.
func TestAnEndOfAnUncountedPlaceTakesTheNextCount(t *testing.T) {
	for _, before := range []StepRun{RunOpen, RunEnded} {
		t.Run(string(before), func(t *testing.T) {
			st := migratedStore(t)
			store := NewStore()
			testing3 := StepPlace{Task: "default", Kind: "step", Place: 3}
			counted, later := time.Date(2026, 11, 17, 1, 0, 0, 0, time.UTC), time.Date(2026, 11, 17, 2, 0, 0, 0, time.UTC)
			inTx(t, st, func(tx pgx.Tx) {
				for range 2 {
					if _, err := store.RaiseStepStarts(t.Context(), tx, "LEGION-1", testing3, "legion-legion-legion-1-tester", counted); err != nil {
						t.Fatalf("RaiseStepStarts: %v", err)
					}
				}
				if before == RunEnded {
					if _, mark, err := store.EndStepStart(t.Context(), tx, "LEGION-1", testing3); err != nil || mark != EndedNow {
						t.Fatalf("EndStepStart of the second start = %d, %v; want it ended now", mark, err)
					}
				}
				if _, err := tx.Exec(t.Context(), `update step_starts set run = 'uncounted' where issue = 'LEGION-1'`); err != nil {
					t.Fatalf("mark the place uncounted as the roll forward does: %v", err)
				}
				row, mark, err := store.EndStepStart(t.Context(), tx, "LEGION-1", testing3)
				if err != nil || mark != NotCounted || row != (StepStart{Starts: 2, Agent: "legion-legion-legion-1-tester", StartedAt: counted, Run: RunUncounted}) {
					t.Fatalf("EndStepStart of the uncounted place = %+v, %d, %v; want its row unchanged, no counted run", row, mark, err)
				}
				starts, ok, err := store.EndUncountedStep(t.Context(), tx, "LEGION-1", testing3, "legion-legion-legion-1-tester", later)
				if err != nil || !ok || starts != 3 {
					t.Fatalf("EndUncountedStep of the uncounted place = %d, %t, %v; want the count 3 after the row's 2", starts, ok, err)
				}
				if got, err := store.StepStarts(t.Context(), tx, "LEGION-1", testing3); err != nil ||
					got != (StepStart{Starts: 3, Agent: "legion-legion-legion-1-tester", StartedAt: later, Run: RunEnded}) {
					t.Fatalf("StepStarts after the end = %+v, %v; want the third count, its start, ended", got, err)
				}
				if _, ok, err := store.EndUncountedStep(t.Context(), tx, "LEGION-1", testing3, "legion-legion-legion-1-tester", later); err != nil || ok {
					t.Fatalf("a second uncounted end = %t, %v; want none", ok, err)
				}
				if _, mark, err := store.EndStepStart(t.Context(), tx, "LEGION-1", testing3); err != nil || mark != AlreadyEnded {
					t.Fatalf("a second end of that run = %d, %v; want the run already ended", mark, err)
				}
				if starts, err := store.RaiseStepStarts(t.Context(), tx, "LEGION-1", testing3, "legion-legion-legion-1-tester", later); err != nil || starts != 4 {
					t.Fatalf("the next start = %d, %v; want the count 4", starts, err)
				}
			})
		})
	}
}
