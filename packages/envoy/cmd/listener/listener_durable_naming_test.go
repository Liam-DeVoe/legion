package main

import (
	"testing"
	"time"

	"github.com/sjawhar/envoy/consumernames"
)

// The listener's durable creator names as the family table does: its Config's prefix and fixed
// name are the table's for a durable's key (consumernames.DurableNaming), both the durable's own
// name, and on the round-6 cases its own fence (the Check its read applies) and
// consumernames.CheckFamily give the same answer, the one each case pins. The control, run with a
// fault and seen failing: a creator whose prefix is not the table's fails the naming row.
func TestTheDurableCreatorNamesAsTheFamilyTable(t *testing.T) {
	r := newDurableRig(t, consumernames.ModeFresh)
	names := durableNames(r.client, r.durable)
	key := consumernames.DurableKey(r.durable)
	creator, table := names.Naming(), consumernames.DurableNaming()
	if got, want := creator.Prefix(key), table.Prefix(key); got != want || got != r.durable {
		t.Errorf("the durable creator's prefix of %s is %q, the table's %q; want both %q", key, got, want, r.durable)
	}
	if got, want := creator.Fixed(key), table.Fixed(key); got != want || got != r.durable {
		t.Errorf("the durable creator's fixed name of %s is %q, the table's %q; want both the durable's own %q", key, got, want, r.durable)
	}
	reserved := time.Unix(1_700_000_000, 0)
	for _, c := range []struct {
		name   string
		record consumernames.Record
		served bool
	}{
		{"its own current", consumernames.Record{Current: r.durable + "~A", Start: 3}, true},
		{"an open replace of its own name", consumernames.Record{Start: 3, Op: &consumernames.Op{Kind: consumernames.OpReplace, Old: r.durable, Candidate: r.durable + "~B", ReservedAt: reserved}}, true},
		{"a current of the session prefix", consumernames.Record{Current: "session~A", Start: 3}, false},
		{"another durable's old name", consumernames.Record{Start: 3, Op: &consumernames.Op{Kind: consumernames.OpReplace, Old: "listener-other", Candidate: r.durable + "~B", ReservedAt: reserved}}, false},
	} {
		own := names.Check(key, c.record)
		family := consumernames.CheckFamily(consumernames.SessionFamily, key, c.record)
		if (own == nil) != c.served || (family == nil) != c.served {
			t.Errorf("%s: the durable creator's fence = %v, CheckFamily = %v; want both served %t", c.name, own, family, c.served)
		}
	}
}
