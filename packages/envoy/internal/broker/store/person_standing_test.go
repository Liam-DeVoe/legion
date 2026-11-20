package store_test

import (
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/store/storetest"
)

// TestPersonStandingKeepsTheLaterAnswerOfACanonicalLogin: a person the broker kept no answer for
// is not found, and KeepPersonStanding writes an answer unless the row holds one Dispatch gave
// later by its own clock, so an older answer written after a newer one leaves the newer. The
// table takes a person as record.CanonicalLogin spells one and refuses every other spelling.
func TestPersonStandingKeepsTheLaterAnswerOfACanonicalLogin(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	const ada = "ada@example.com"
	if held, at, found, err := st.PersonStanding(ctx, ada); err != nil || found || held || !at.IsZero() {
		t.Fatalf("PersonStanding(ada) with nothing kept = %t, %v, %t, %v; want not found", held, at, found, err)
	}
	t1 := time.Date(2026, 11, 16, 0, 0, 1, 0, time.UTC)
	t2, t3 := t1.Add(time.Second), t1.Add(2*time.Second)
	read := func(wantHeld bool, wantAt time.Time, after string) {
		t.Helper()
		held, at, found, err := st.PersonStanding(ctx, ada)
		if err != nil || !found || held != wantHeld || !at.Equal(wantAt) {
			t.Fatalf("PersonStanding(ada) after %s = %t, %v, %t, %v; want held %t as of %v", after, held, at, found, err, wantHeld, wantAt)
		}
	}
	for _, write := range []struct {
		held bool
		asOf time.Time
	}{{true, t2}, {false, t1}} {
		if err := st.KeepPersonStanding(ctx, ada, write.held, write.asOf); err != nil {
			t.Fatalf("KeepPersonStanding(ada, %t, %v): %v", write.held, write.asOf, err)
		}
	}
	read(true, t2, "a held answer as of t2, then a not-held one as of t1")
	if err := st.KeepPersonStanding(ctx, ada, false, t3); err != nil {
		t.Fatal(err)
	}
	read(false, t3, "a not-held answer as of t3")
	for _, spelled := range []string{"Ada@example.com", " ada@example.com", "ada@example.com ", ""} {
		if err := st.KeepPersonStanding(ctx, spelled, true, t3); !isCheckViolation(err) {
			t.Errorf("KeepPersonStanding(%q) = %v, want the table's check to refuse a login not spelled canonically", spelled, err)
		}
	}
}
