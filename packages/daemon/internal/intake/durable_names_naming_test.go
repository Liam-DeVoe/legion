package intake

import (
	"fmt"
	"maps"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/consumernames"
)

// namingDiffers names each part of logical's naming in which creator differs from table: the
// prefix, the fixed name or the metadata.
func namingDiffers(creator, table consumernames.Naming, logical string) []string {
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
	return differs
}

// The daemon's creator names as the families' table does. For each of its three logical names, the
// Config its Names runs on (Names.Naming) gives the prefix, the fixed name and the metadata that
// consumernames.FamilyNaming gives that name in the project's family (daemon/<P>), so the creator's
// fence of a record is the one consumernames.CheckFamily applies. Its owner check is
// consumernames.OwnsFamily's: alike on its own consumer as its create makes it, on its fixed name,
// on another of the daemon's durables' consumer and on an unmarked one. The control runs in the
// row: a Config built by hand with another prefix, which the same comparison reports for every
// logical name.
func TestTheDaemonCreatorNamesAsTheFamilyTable(t *testing.T) {
	spec := consumerSpec(&lockedBuffer{})
	family := familyOf(t, spec.Project)
	d, err := newDurableNames(nil, spec)
	if err != nil {
		t.Fatalf("the daemon's creator for %s: %v", spec.Project, err)
	}
	hand := consumernames.New(consumernames.Config{
		Prefix: func(string) string { return "legion-go" },
		Fixed:  func(logical string) string { return logical },
		Metadata: func(logical string) map[string]string {
			return map[string]string{consumernames.LogicalMetadataKey: logical}
		},
	})
	consumer := func(name string, metadata map[string]string) *jetstream.ConsumerInfo {
		return &jetstream.ConsumerInfo{Name: name, Config: jetstream.ConsumerConfig{Durable: name, Metadata: metadata}}
	}
	logicals := fixedNames(spec.Project)
	for i, logical := range logicals {
		table, err := consumernames.FamilyNaming(family, logical)
		if err != nil {
			t.Fatalf("FamilyNaming(%s, %s): %v", family, logical, err)
		}
		if differs := namingDiffers(d.names.Naming(), table, logical); len(differs) != 0 {
			t.Errorf("the daemon's creator names %s apart from the table's %s: %v", logical, family, differs)
		}
		if differs := namingDiffers(hand.Naming(), table, logical); len(differs) == 0 {
			t.Errorf("control: a Config built by hand with the prefix legion-go names %s as the table does; want the row to report it", logical)
		}

		created := physicalConfig(jetstream.ConsumerConfig{Durable: logical}, logical+"~A")
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
