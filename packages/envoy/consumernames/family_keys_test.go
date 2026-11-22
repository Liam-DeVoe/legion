package consumernames

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// The table takes only its family's keys: Dispatch's store only Dispatch's two durables, a
// daemon's store only legion-go-<project>-<kind> of its own project, and the session store a key
// CheckKey passes or a durable's key DurableKey made. So FamilyNaming, CheckFamily and OwnsFamily
// refuse any other key as ErrInvalidKey, naming it, before they judge a record or a consumer: the
// fence and the owner check judge by the key's own prefix and fixed name, so a record of daemon/p
// keyed by the listener's durable, an open replace of that durable, passed both, and so did the
// durable itself. The control is each family's own keys, which pass. Before, FamilyNaming took any
// key for Dispatch's and the daemons' stores and any key without DurableKey's prefix for the
// session store, and an empty Dispatch key got an empty prefix.
func TestTheFamilyTableTakesOnlyItsFamilysKeys(t *testing.T) {
	consumer := func(name, logical string) *jetstream.ConsumerInfo {
		return &jetstream.ConsumerInfo{Name: name, Config: jetstream.ConsumerConfig{Durable: name, Metadata: map[string]string{LogicalMetadataKey: logical}}}
	}
	// replace is an open replace of old by a fresh name of prefix.
	replace := func(old, prefix string) Record {
		return Record{Start: 3, Op: &Op{Kind: OpReplace, Old: old, Candidate: prefix + "~C", ReservedAt: testEpoch}}
	}
	for _, c := range []struct{ name, family, logical string }{
		{"the listener's durable in a daemon's store", "daemon/p", "listener-m"},
		{"another project's durable in a daemon's store", "daemon/p", "legion-go-q-wake"},
		{"a durable of a project that ends in the project's name", "daemon/p", "legion-go-xp-wake"},
		{"a durable of no daemon kind", "daemon/p", "legion-go-p-other"},
		{"a session's id in a daemon's store", "daemon/p", "ses-A"},
		{"Dispatch's durable in a daemon's store", "daemon/p", "delivery-events"},
		{"no key in Dispatch's store", DispatchFamily, ""},
		{"the listener's durable in Dispatch's store", DispatchFamily, "listener-m"},
		{"a daemon's durable in Dispatch's store", DispatchFamily, "legion-go-p-wake"},
		{"no key in the session store", SessionFamily, ""},
		{"a key holding a '.' in the session store", SessionFamily, "ses.A"},
	} {
		t.Run(c.name, func(t *testing.T) {
			named := fmt.Sprintf("%q", c.logical)
			refused := func(call string, err error) {
				t.Helper()
				if !errors.Is(err, ErrInvalidKey) || !strings.Contains(err.Error(), named) {
					t.Errorf("%s(%s, %s) = %v; want ErrInvalidKey naming %s", call, c.family, named, err, named)
				}
			}
			_, err := FamilyNaming(c.family, c.logical)
			refused("FamilyNaming", err)
			refused("CheckFamily", CheckFamily(c.family, c.logical, replace(c.logical, c.logical)))
			refused("OwnsFamily", OwnsFamily(c.family, c.logical, consumer(c.logical, c.logical)))
		})
	}
	for _, c := range []struct{ family, logical string }{
		{DispatchFamily, "delivery-events"},
		{DispatchFamily, "delivery-jobs"},
		{"daemon/p", "legion-go-p-dispatch"},
		{"daemon/p", "legion-go-p-github"},
		{"daemon/p", "legion-go-p-wake"},
		{"daemon/p", "legion-go-p-ready"},
		{"daemon/a-b", "legion-go-a-b-wake"},
		{SessionFamily, "ses-A"},
		{SessionFamily, DurableKey("listener-m")},
	} {
		naming, err := FamilyNaming(c.family, c.logical)
		if err != nil {
			t.Errorf("control: FamilyNaming(%s, %s) = %v", c.family, c.logical, err)
			continue
		}
		fixed := naming.Fixed(c.logical)
		if err := CheckFamily(c.family, c.logical, replace(fixed, naming.Prefix(c.logical))); err != nil {
			t.Errorf("control: CheckFamily(%s, %s) of an open replace of its fixed name = %v", c.family, c.logical, err)
		}
		if err := OwnsFamily(c.family, c.logical, consumer(fixed, "")); err != nil {
			t.Errorf("control: OwnsFamily(%s, %s) of its fixed consumer = %v", c.family, c.logical, err)
		}
	}
}

// A reader's Names built from the table deletes no consumer of a key outside its family. daemon/p's
// store holds a planted record keyed by listener-m, the listener's durable: an open replace of
// listener-m, its fixed name, with a saved start, while the server holds listener-m. A reader that
// builds its Names from FamilyNaming of the key is refused the key; one that built its Names for
// its own key, legion-go-p-wake, has the family's key rule in it, so its Cleanup of listener-m is
// ErrInvalidKey. Neither sends a delete, and the server still holds listener-m. The control is the
// same reader's Cleanup of its own key over the same record shape, which deletes the fixed name as
// Step 0 does. Before, each reader's Cleanup deleted the listener's live durable.
func TestAReaderOfTheTableDeletesNoConsumerOfAKeyOutsideItsFamily(t *testing.T) {
	const family, planted, own = "daemon/p", "listener-m", "legion-go-p-wake"
	ctx := context.Background()
	reader := func(h *harness, naming Naming) *Names {
		cfg := naming.names().cfg
		cfg.Store, cfg.Server, cfg.Owner, cfg.Now = h.store, h.server, "reader", h.clock.Now
		cfg.Floor = func(info *jetstream.ConsumerInfo) uint64 { return info.AckFloor.Stream }
		cfg.StreamID = func(context.Context) (string, error) { return "stream-1", nil }
		return New(cfg)
	}
	plant := func(t *testing.T, key string) *harness {
		t.Helper()
		h := newHarness()
		if _, err := h.server.create(key)(ctx, key, Start{Seq: 3}); err != nil {
			t.Fatal(err)
		}
		h.store.put(key, Record{Key: key, Start: 3, Op: &Op{Kind: OpReplace, Old: key, Candidate: key + "~C", Owner: "planter", ReservedAt: h.clock.Now().Add(-time.Hour)}})
		return h
	}
	refused := func(t *testing.T, h *harness, err error) {
		t.Helper()
		if !errors.Is(err, ErrInvalidKey) || len(h.server.deletes) != 0 || !h.server.holds(planted) {
			t.Fatalf("a reader's Cleanup of %s's planted %s = %v; deletes sent %v, the server still holds %s: %t; want ErrInvalidKey, no delete, %s held",
				family, planted, err, h.server.deletes, planted, h.server.holds(planted), planted)
		}
	}
	t.Run("a Names built from the planted key", func(t *testing.T) {
		h := plant(t, planted)
		naming, err := FamilyNaming(family, planted)
		if err == nil {
			_, err = reader(h, naming).Cleanup(ctx, planted)
		}
		refused(t, h, err)
	})
	t.Run("a Names built from the reader's own key", func(t *testing.T) {
		h := plant(t, planted)
		naming, err := FamilyNaming(family, own)
		if err != nil {
			t.Fatal(err)
		}
		_, err = reader(h, naming).Cleanup(ctx, planted)
		refused(t, h, err)
	})
	t.Run("control: the reader's own key", func(t *testing.T) {
		h := plant(t, own)
		naming, err := FamilyNaming(family, own)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reader(h, naming).Cleanup(ctx, own); err != nil || !slices.Equal(h.server.deletes, []string{own}) || h.server.holds(own) {
			t.Fatalf("control: the reader's Cleanup of its own %s = %v; deletes sent %v, the server still holds it: %t; want %s deleted",
				own, err, h.server.deletes, h.server.holds(own), own)
		}
	})
}
