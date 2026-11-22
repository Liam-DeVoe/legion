package intake

import (
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/consumernames"
)

// namingDiffers names each part of logical's naming in which creator differs from table: the
// prefix, the fixed name, the metadata, or the key rule on any of keys (a nil CheckKey is the rule
// consumernames.New gives it, consumernames.CheckKey).
func namingDiffers(creator, table consumernames.Naming, logical string, keys []string) []string {
	var differs []string
	if got, want := creator.Prefix(logical), table.Prefix(logical); got != want {
		differs = append(differs, fmt.Sprintf("prefix %q, the table's %q", got, want))
	}
	if got, want := creator.Fixed(logical), table.Fixed(logical); got != want {
		differs = append(differs, fmt.Sprintf("fixed name %q, the table's %q", got, want))
	}
	if got, want := creator.Metadata(logical), table.Metadata(logical); !maps.Equal(got, want) {
		differs = append(differs, fmt.Sprintf("metadata %v, the table's %v", got, want))
	}
	rule := func(check func(string) error) func(string) error {
		if check == nil {
			return consumernames.CheckKey
		}
		return check
	}
	for _, key := range keys {
		if got, want := rule(creator.CheckKey)(key), rule(table.CheckKey)(key); (got == nil) != (want == nil) {
			differs = append(differs, fmt.Sprintf("key %q answered %v, the table's %v", key, got, want))
		}
	}
	return differs
}

// The daemon's creator names as the families' table does. For each of its three logical names, the
// Config its Names runs on (Names.Naming) gives the prefix, the fixed name, the metadata and the key
// rule that consumernames.FamilyNaming gives that name in the project's family (daemon/<P>): the
// rule takes the project's own legion-go-<P>-<kind> and refuses another project's durable, the
// listener's and Dispatch's, so the creator's fence of a record is the one consumernames.CheckFamily
// applies. Its owner check is consumernames.OwnsFamily's: alike on its own consumer as its create
// makes it, on its fixed name, on another of the daemon's durables' consumer and on an unmarked
// one. The control runs in the row: a Config built by hand with another prefix and no key rule,
// which the same comparison reports for every logical name.
func TestTheDaemonCreatorNamesAsTheFamilyTable(t *testing.T) {
	spec := consumerSpec(&lockedBuffer{})
	family := familyOf(t, spec.Project)
	d, err := newDurableNames(nil, nil, spec)
	if err != nil {
		t.Fatalf("the daemon's creator for %s: %v", spec.Project, err)
	}
	hand := consumernames.New(consumernames.Config{
		Prefix: func(string) string { return "legion-go" },
		Fixed:  func(logical string) string { return logical },
		Metadata: func(logical string) map[string]string {
			return map[string]string{consumernames.LogicalMetadataKey: logical}
		},
		CreateBound: createBound,
	})
	consumer := func(name string, metadata map[string]string) *jetstream.ConsumerInfo {
		return &jetstream.ConsumerInfo{Name: name, Config: jetstream.ConsumerConfig{Durable: name, Metadata: metadata}}
	}
	logicals := fixedNames(spec.Project)
	keys := append(slices.Clone(logicals), "legion-go-"+spec.Project+"-ready", wakeConsumerName("OTHER"), "listener-m", consumernames.DispatchDeliveryDurable, "")
	for i, logical := range logicals {
		table, err := consumernames.FamilyNaming(family, logical)
		if err != nil {
			t.Fatalf("FamilyNaming(%s, %s): %v", family, logical, err)
		}
		if differs := namingDiffers(d.names.Naming(), table, logical, keys); len(differs) != 0 {
			t.Errorf("the daemon's creator names %s apart from the table's %s: %v", logical, family, differs)
		}
		if differs := namingDiffers(hand.Naming(), table, logical, keys); len(differs) == 0 {
			t.Errorf("control: a Config built by hand with the prefix legion-go names %s as the table does; want the row to report it", logical)
		}

		created := d.physicalConfig(jetstream.ConsumerConfig{Durable: logical}, logical+"~A")
		other := logicals[(i+1)%len(logicals)]
		for _, c := range []struct {
			name  string
			info  *jetstream.ConsumerInfo
			owned bool
		}{
			{"its own consumer, as its create makes it", &jetstream.ConsumerInfo{Name: created.Durable, Config: created}, true},
			{"its fixed consumer, unmarked", consumer(logical, nil), true},
			{"the consumer of " + other, consumer(logical+"~A", map[string]string{consumernames.LogicalMetadataKey: other}), false},
			{"an unmarked consumer", consumer(logical+"~A", nil), false},
		} {
			own, table := d.names.Owns(logical, c.info), consumernames.OwnsFamily(family, logical, c.info)
			if (own == nil) != c.owned || (table == nil) != c.owned {
				t.Errorf("%s, %s: the daemon creator's Owns = %v, OwnsFamily = %v; want both owned %t", logical, c.name, own, table, c.owned)
			}
		}
	}
}
