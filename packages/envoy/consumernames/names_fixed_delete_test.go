package consumernames

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// A convert's replace deletes the key's fixed old name and never retires it, so a failed delete
// keeps the replace open, naming that fixed name: the call that published the candidate serves it,
// and the next call's Step 0 deletes the fixed name before anything else, sending no create, and
// then closes the replace. The row plants a convert's open replace of s-ses-1 whose candidate the
// server holds, and a delete of s-ses-1 that fails once. The control is fd6c87d3d5's publish,
// which closed the replace before the delete, so the failed delete left the fixed name held with
// no operation naming it.
func TestAFailedDeleteOfAFixedOldNameKeepsTheReplaceOpenForStep0(t *testing.T) {
	const key, fixed, candidate = "ses-1", "s-ses-1", "session~C"
	ctx := context.Background()
	h := newHarness()
	for _, name := range []string{fixed, candidate} {
		if _, err := h.server.create(key)(ctx, name, Start{Seq: 3}); err != nil {
			t.Fatal(err)
		}
	}
	h.store.put(key, Record{Key: key, Start: 3, Op: &Op{Kind: OpReplace, Old: fixed, Candidate: candidate, Owner: "converter", ReservedAt: h.clock.Now()}})
	failures := 1
	h.server.deleteFault = func(name string) error {
		if name == fixed && failures > 0 {
			failures--
			return errors.New("the server refused the delete")
		}
		return nil
	}
	names := h.names("p1")
	names.cfg.Fixed = func(logical string) string { return "s-" + logical }
	request := CreateRequest{Create: h.server.create(key), First: first(3)}

	sel, _, err := names.Ensure(ctx, key, request)
	if err != nil || sel.Record.Current != candidate {
		t.Fatalf("the first Ensure = %+v, %v; want the candidate %s published", sel.Record, err, candidate)
	}
	record := h.store.record(t, key)
	if op := record.Op; op == nil || op.Kind != OpReplace || op.Old != fixed || record.Current != candidate || !h.server.holds(fixed) {
		t.Fatalf("after the failed delete the record is %+v (server holds %s: %t); want the replace open, naming %s, with %s current", record, fixed, h.server.holds(fixed), fixed, candidate)
	}

	creates, deletes := len(h.server.creates), len(h.server.deletes)
	sel, _, err = names.Ensure(ctx, key, request)
	if err != nil || sel.Record.Current != candidate || sel.Record.Op != nil {
		t.Fatalf("the next Ensure = %+v, %v; want %s current and the replace closed", sel.Record, err, candidate)
	}
	if h.server.holds(fixed) || !slices.Contains(h.server.deletes[deletes:], fixed) || len(h.server.creates) != creates {
		t.Fatalf("the next Ensure's Step 0 sent deletes %v and creates %v; want %s deleted and nothing created", h.server.deletes[deletes:], h.server.creates[creates:], fixed)
	}
	if record := h.store.record(t, key); record.Op != nil || slices.Contains(record.Retired, fixed) {
		t.Fatalf("the record after Step 0 is %+v; want no operation and %s never retired", record, fixed)
	}
}
