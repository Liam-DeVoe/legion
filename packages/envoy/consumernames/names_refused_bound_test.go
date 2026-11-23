package consumernames

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// A session refused a consumer is asked again at every heartbeat (the listener's registration),
// and here every Ensure of its key is refused, 200 times: at the stream's max_consumers, or by a
// create the server answers with an API error. The record stays bounded, and so do each call's
// reads. A candidate the server refused at the cap is not retired, since the server answered its
// create; and each retirement drops the retired names the server does not hold whose time has
// passed (DropRetired's rule), so the record keeps only the candidates retired less than the
// create bound (and the settle age, the dropper's margin) ago, which may still land. The bound: at
// calls 30 s apart, past both, one retired name, the candidate that call's Step 0 retired, and at
// most 3 info reads a call (Step 0's candidate, the name its retirement drops, the cap's read of
// the one it keeps); at calls 3 s apart, 4 names and 9 reads; under an API error, which reads
// nothing at the cap, one name and 2 reads. The same holds when a call that creates nothing comes
// between the refused creates, as a Resolve does: its Step 0 retires the candidate the refused
// Ensure left open (abandon), and that retirement drops too, so 200 Ensures each followed by a
// Resolve, 15 s apart, keep one name and read at most 2 infos a call. Before, each call at the cap
// added two names, its refused candidate and the last call's, that nothing but a Cleanup removed,
// and read them all: after 200 calls 30 s apart, 399 retired names, a 42,486-byte record and 399
// info reads on the last call (the reviewer's measurement on this harness); and Step 0's abandon
// dropped nothing, so the Ensure and Resolve pairs reached 200 names and 21,392 bytes (the
// reviewer's probe).
func TestAKeyRefusedAtEveryCallKeepsABoundedRecord(t *testing.T) {
	const key, calls, maxBytes = "ses-refused", 200, 1024
	full := func(h *harness) error {
		if _, err := h.server.create("ses-other")(context.Background(), "session~other", Start{Seq: 1}); err != nil {
			t.Fatal(err)
		}
		h.server.capacity = 1
		return ErrConsumerLimit
	}
	insufficient := &jetstream.APIError{Code: 500, ErrorCode: 10023, Description: "insufficient resources"}
	refusing := func(h *harness) error {
		h.server.createFault = func(string) (bool, error) { return false, insufficient }
		return insufficient
	}
	for _, c := range []struct {
		name    string
		apart   time.Duration
		refuse  func(*harness) error
		resolve bool
		retired int
		infos   int
	}{
		{name: "at a full cap, calls 30 s apart", apart: 30 * time.Second, refuse: full, retired: 1, infos: 3},
		{name: "at a full cap, calls 3 s apart", apart: 3 * time.Second, refuse: full, retired: 4, infos: 9},
		{name: "a create the server refuses, calls 30 s apart", apart: 30 * time.Second, refuse: refusing, retired: 1, infos: 2},
		{name: "a create the server refuses, each Ensure followed by a Resolve, calls 15 s apart", apart: 15 * time.Second, refuse: refusing, resolve: true, retired: 1, infos: 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness()
			refusal := c.refuse(h)
			counting := &countingServer{fakeServer: h.server}
			names := h.names("p1")
			names.cfg.Server = counting
			most := struct{ retired, abandoned, infos, bytes int }{}
			steps := calls
			if c.resolve {
				steps = 2 * calls
			}
			for call := range steps {
				if call > 0 {
					h.clock.advance(c.apart)
				}
				counting.calls = nil
				if c.resolve && call%2 == 1 {
					if _, err := names.Resolve(context.Background(), key); err != nil {
						t.Fatalf("Resolve %d = %v, want it served", call+1, err)
					}
				} else if _, _, err := names.Ensure(context.Background(), key, CreateRequest{Create: h.server.create(key), First: first(1)}); !errors.Is(err, refusal) {
					t.Fatalf("Ensure %d = %v, want it refused with %v", call+1, err, refusal)
				}
				infos := 0
				for _, n := range counting.calls {
					infos += n
				}
				record := h.store.record(t, key)
				data, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				most.retired, most.abandoned = max(most.retired, len(record.Retired)), max(most.abandoned, len(record.Abandoned))
				most.infos, most.bytes = max(most.infos, infos), max(most.bytes, len(data))
				if len(record.Retired) > c.retired || len(record.Abandoned) > c.retired || infos > c.infos || len(data) > maxBytes {
					t.Fatalf("after call %d the record holds %d retired and %d abandoned names in %d bytes, and the call read %d infos; want at most %d names, %d bytes and %d infos",
						call+1, len(record.Retired), len(record.Abandoned), len(data), infos, c.retired, maxBytes, c.infos)
				}
			}
			h.server.createdTwice(t)
			t.Logf("over %d calls: at most %d retired and %d abandoned names, %d infos in a call, %d bytes", steps, most.retired, most.abandoned, most.infos, most.bytes)
		})
	}
}

// A retirement keeps an abandoned candidate for the bound its create ran under (Op.CreateWithin),
// never longer than the family's own (Config.CreateBound): every process of a family runs its
// creates under the one bound its creator sets, so a longer one is a record a writer chose, and it
// would keep the name, and the read of it at every retirement, for as long as that writer said.
// Here a record's open create carries an hour, and both of Step 0's retirements, a Resolve's
// (abandon) and an Ensure's (rereserve), keep its candidate until now plus the family's 10 s.
// Controls: an operation that records no bound, or a shorter one, keeps its candidate for the
// family's bound or its own.
func TestARetirementKeepsACandidateNoLongerThanTheFamilysCreateBound(t *testing.T) {
	const key = "ses-1"
	ctx := context.Background()
	for _, call := range []struct {
		name string
		run  func(n *Names, h *harness) error
	}{
		{name: "Resolve", run: func(n *Names, _ *harness) error { _, err := n.Resolve(ctx, key); return err }},
		{name: "Ensure", run: func(n *Names, h *harness) error {
			_, _, err := n.Ensure(ctx, key, CreateRequest{Create: h.server.create(key), First: first(1)})
			return err
		}},
	} {
		for _, c := range []struct {
			name   string
			within time.Duration
			kept   time.Duration
		}{
			{name: "an hour, past the family's bound", within: time.Hour, kept: testCreateBound},
			{name: "control: none recorded", within: 0, kept: testCreateBound},
			{name: "control: 5 s, within the family's bound", within: 5 * time.Second, kept: 5 * time.Second},
		} {
			t.Run(call.name+", a create bound of "+c.name, func(t *testing.T) {
				h := newHarness()
				candidate := FreshName("session")
				h.store.put(key, Record{Key: key, Start: 1, Op: &Op{Kind: OpCreate, Owner: "reserver", Candidate: candidate, CreateWithin: c.within, ReservedAt: h.clock.Now()}})
				h.clock.advance(time.Minute)
				if err := call.run(h.names("p1"), h); err != nil {
					t.Fatal(err)
				}
				rec := h.store.record(t, key)
				until, ok := rec.abandonedUntil(candidate)
				if want := h.clock.Now().Add(c.kept); !ok || !until.Equal(want) {
					t.Fatalf("after the %s the record keeps %s until %s (abandoned: %t), want until %s, now plus %s; the record is %+v",
						call.name, candidate, until.Format(time.RFC3339), ok, want.Format(time.RFC3339), c.kept, rec)
				}
			})
		}
	}
}
