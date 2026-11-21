package consumernames

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// testEpoch is when the rows of this file reserved an operation they write by hand.
var testEpoch = time.Unix(1_700_000_000, 0)

// A record names only its own family's consumers: its current, each retired name and an open
// operation's candidate are <prefix>~<nuid> names of its key's prefix, and an operation's old name
// one of those or the key's fixed name. Any other name, another family's consumer (a daemon's
// durable, the listener's durable for a session key, a session's for a durable key), is refused as a
// malformed record, and the protocol neither reads, creates, deletes nor binds it. The control for
// each row is today's reader, the store's strict decode, which takes the record, since every name
// in it is a name a consumer can carry; the rows that name the family's own names are served.
func TestARecordNamesOnlyItsOwnFamilysConsumers(t *testing.T) {
	const key = "ses-1"
	for _, tc := range []struct {
		name   string
		prefix string
		record Record
		served bool
	}{
		{name: "a current of the daemon's durable", prefix: "session", record: Record{Current: "legion-go-p-dispatch~AAAAAAAAAAAAAAAAAAAAAA", Start: 3}},
		{name: "a current of the listener's durable", prefix: "session", record: Record{Current: "listener-m~AAAAAAAAAAAAAAAAAAAAAA", Start: 3}},
		{name: "a current that is the fixed name", prefix: "session", record: Record{Current: "s-ses-1", Start: 3}},
		{name: "a current of the prefix with no nuid", prefix: "session", record: Record{Current: "session~", Start: 3}},
		{name: "a current of the prefix and more", prefix: "session", record: Record{Current: "session-x~AAAA", Start: 3}},
		{name: "a retired name of the daemon's durable", prefix: "session", record: Record{Current: "session~A", Start: 3, Retired: []string{"legion-go-p-dispatch"}}},
		{name: "a candidate of another family", prefix: "session", record: Record{Start: 3, Op: &Op{Kind: OpCreate, Candidate: "delivery-jobs~B", ReservedAt: testEpoch}}},
		{name: "an old name of another family", prefix: "session", record: Record{Current: "session~A", Start: 3, Op: &Op{Kind: OpDelete, Old: "delivery-events", ReservedAt: testEpoch}}},
		{name: "a session's current under a durable's prefix", prefix: "listener-m", record: Record{Current: "session~A", Start: 3}},
		{name: "control: a current of the family", prefix: "session", record: Record{Current: "session~A", Start: 3}, served: true},
		{name: "control: an old name that is the key's fixed name", prefix: "session", record: Record{Current: "session~A", Start: 3, Op: &Op{Kind: OpDelete, Old: "s-ses-1", ReservedAt: testEpoch}}, served: true},
		{name: "control: a durable's own current", prefix: "listener-m", record: Record{Current: "listener-m~A", Start: 3}, served: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			record := tc.record
			record.Key = key
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeRecord(key, data); err != nil {
				t.Fatalf("control: today's reader refused %s: %v", data, err)
			}
			for _, name := range []string{"session~A", "listener-m~A"} {
				if _, err := h.server.create(key)(context.Background(), name, Start{Seq: 3}); err != nil {
					t.Fatal(err)
				}
			}
			h.store.put(key, record)
			names := h.names("p1")
			names.cfg.Prefix = func(string) string { return tc.prefix }
			names.cfg.Fixed = func(logical string) string { return "s-" + logical }
			creates := len(h.server.creates)
			// Each call is bounded: a record taken as served can leave a call waiting on an
			// operation the row's clock never lets settle.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err = names.Select(ctx, key, h.server.create(key))
			if tc.served {
				if err != nil {
					t.Fatalf("Select of %+v = %v, want it served", record, err)
				}
				return
			}
			if !errors.Is(err, ErrMalformedRecord) {
				t.Fatalf("Select of %+v = %v, want ErrMalformedRecord", record, err)
			}
			if _, err := names.Cleanup(ctx, key); !errors.Is(err, ErrMalformedRecord) {
				t.Fatalf("Cleanup of %+v = %v, want ErrMalformedRecord", record, err)
			}
			if _, err := names.Delete(ctx, key, nil); !errors.Is(err, ErrMalformedRecord) {
				t.Fatalf("Delete of %+v = %v, want ErrMalformedRecord", record, err)
			}
			if len(h.server.creates) != creates || len(h.server.deletes) != 0 {
				t.Fatalf("the refused record had the server create %v and delete %v", h.server.creates[creates:], h.server.deletes)
			}
		})
	}
}
