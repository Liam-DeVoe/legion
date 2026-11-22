package bus

import (
	"testing"
	"time"

	"github.com/sjawhar/envoy/consumernames"
)

// The session creator names as the family table does: its Config's prefix and fixed name are the
// table's for the session family (consumernames.SessionNaming), so its fence is the one
// consumernames.CheckFamily applies for the session family, which gives the answer each round-6
// case pins: a convert's open replace of s-ses-A served, a retired fixed name, another session's
// fixed name and another family's current refused. The controls, each run with a fault and seen
// failing: a creator whose prefix is not the table's fails the naming row, and a table with no
// fixed rule fails the convert's case.
func TestTheSessionCreatorNamesAsTheFamilyTable(t *testing.T) {
	_, consumers, _ := freshSessionConsumersOnStream(t)
	names := consumers.fresh.names
	creator, table := names.Naming(), consumernames.SessionNaming()
	for _, id := range []string{"ses-A", "ses_x9"} {
		if got, want := creator.Prefix(id), table.Prefix(id); got != want {
			t.Errorf("the session creator's prefix of %s is %q, the table's %q", id, got, want)
		}
		if got, want := creator.Fixed(id), table.Fixed(id); got != want {
			t.Errorf("the session creator's fixed name of %s is %q, the table's %q", id, got, want)
		}
	}
	reserved := time.Unix(1_700_000_000, 0)
	for _, c := range []struct {
		name   string
		record consumernames.Record
		served bool
	}{
		{"its own current", consumernames.Record{Current: "session~A", Start: 3}, true},
		{"a convert's open replace of s-ses-A", consumernames.Record{Start: 3, Op: &consumernames.Op{Kind: consumernames.OpReplace, Old: "s-ses-A", Candidate: "session~B", ReservedAt: reserved}}, true},
		{"another session's fixed name as the old name", consumernames.Record{Start: 3, Op: &consumernames.Op{Kind: consumernames.OpReplace, Old: "s-ses-B", Candidate: "session~B", ReservedAt: reserved}}, false},
		{"a retired fixed name", consumernames.Record{Current: "session~A", Start: 3, Retired: []string{"s-ses-A"}}, false},
		{"a current of the listener's durable", consumernames.Record{Current: "listener-m~A", Start: 3}, false},
	} {
		if err := consumernames.CheckFamily(consumernames.SessionFamily, "ses-A", c.record); (err == nil) != c.served {
			t.Errorf("%s: CheckFamily for the session family = %v; want served %t", c.name, err, c.served)
		}
	}
}
