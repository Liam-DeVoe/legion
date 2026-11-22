package intake

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/consumernames"
)

// An open that has ended leaves its logical name's count of opens in flight at 0 (opening), so a
// reader's abandon after it closes the abandoned candidate's connection (closeRetired). Two opens
// run and end (a first create, then an update of the current); a create another boot left open
// is planted, and a reader abandons it. The count must be empty, and the daemon must hold no
// connection of the abandoned candidate. The control is opened's decrement removed: the count
// stays at 2 and the reader closes nothing, which no other row catches, since the leak row reads
// on a creator that never opened and the interleaving rows assert only that nothing closes.
func TestAnEndedOpenLeavesAReaderFreeToClose(t *testing.T) {
	pool := migratedPool(t)
	cutover(t, pool, consumernames.CutoverDone)
	js, _, _ := freshJetStream(t)
	spec := freshSpec(t, &lockedBuffer{}, js)
	d, err := newDurableNames(js, pool, spec)
	if err != nil {
		t.Fatalf("the daemon's creator: %v", err)
	}
	defer d.close()
	github := githubConsumerConfig(spec, spec.Repositories)
	for i := range 2 {
		if _, err := d.open(context.Background(), github); err != nil {
			t.Fatalf("open %d of %s: %v", i+1, github.Durable, err)
		}
	}
	rec := selectionOf(t, pool, github.Durable)
	abandoned := consumernames.FreshName(github.Durable)
	rec.Op = &consumernames.Op{Kind: consumernames.OpCreate, Owner: "another boot", Candidate: abandoned, Native: consumernames.NativeNew, ReservedAt: time.Now().Add(-time.Hour)}
	putRecord(t, pool, github.Durable, rec)
	if _, err := d.info(context.Background(), github.Durable); err != nil {
		t.Fatalf("the reader's info of %s: %v", github.Durable, err)
	}
	if !slices.Contains(selectionOf(t, pool, github.Durable).Retired, abandoned) {
		t.Fatalf("the reader retired nothing of %s, so the row tests nothing", abandoned)
	}
	d.mu.Lock()
	held := slices.Collect(maps.Keys(d.generations))
	count := d.opening[github.Durable]
	d.mu.Unlock()
	if slices.Contains(held, abandoned) || count != 0 {
		t.Fatalf("after two opens ended, opening = %d and the daemon holds %v; want 0 and no connection of %s", count, held, abandoned)
	}
}
