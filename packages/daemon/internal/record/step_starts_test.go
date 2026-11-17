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
		if err != nil || got != (StepStart{Starts: 3, Agent: "", StartedAt: later}) {
			t.Fatalf("StepStarts after three starts = %+v, %v; want 3 starts and the last start's empty agent at %s", got, err, later)
		}
		if _, err := store.RaiseStepStarts(t.Context(), tx, "LEGION-1", testing3, "legion-legion-legion-1-tester", first); err != nil {
			t.Fatalf("RaiseStepStarts: %v", err)
		}
		got, err = store.StepStarts(t.Context(), tx, "LEGION-1", testing3)
		if err != nil || got != (StepStart{Starts: 4, Agent: "legion-legion-legion-1-tester", StartedAt: first}) {
			t.Fatalf("StepStarts after the fourth start = %+v, %v; want 4 starts with its agent and moment", got, err)
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
