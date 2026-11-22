package consumernames

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/testnats"
)

// A store refuses to write a record every read of the key would refuse (check), as
// ErrMalformedRecord with nothing written, so a writer outside Names that builds one, such as an
// operator's cleanup that drops a retired name from Retired alone, fails at its own write and the
// key stays readable for every other reader. The records: an abandoned name that is not retired,
// an abandoned name with no time, and an operation reserved at no time. Before, each was written,
// and every later read of the key, every listener's among them, was ErrMalformedRecord. The control
// in each store is the same record with the abandoned name retired, which is written and read back.
func TestAStoreRefusesToWriteARecordNoReaderWouldRead(t *testing.T) {
	ctx := context.Background()
	until := time.Unix(1_700_000_000, 0)
	stores := map[string]struct {
		store   Store
		logical string
		prefix  string
	}{
		"the KV store":       {store: newLiveStore(t, testnats.URL(t), &StoreState{Cutover: CutoverDone}).store, logical: "ses-1", prefix: "session"},
		"the Postgres store": {store: pgStore(t, DispatchFamily, newMemPG()), logical: DispatchDeliveryDurable, prefix: DispatchDeliveryDurable},
	}
	for storeName, s := range stores {
		a, x := s.prefix+"~AAAAAAAAAAAAAAAAAAAAAA", s.prefix+"~XXXXXXXXXXXXXXXXXXXXXX"
		for i, c := range []struct {
			name   string
			record Record
		}{
			{name: "an abandoned name that is not retired", record: Record{Start: 1, Retired: []string{a}, Abandoned: []Abandoned{{Name: x, Until: until}}}},
			{name: "an abandoned name with no time", record: Record{Start: 1, Retired: []string{a, x}, Abandoned: []Abandoned{{Name: x}}}},
			{name: "an operation reserved at no time", record: Record{Start: 1, Op: &Op{Kind: OpCreate, Candidate: x}}},
		} {
			t.Run(storeName+", "+c.name, func(t *testing.T) {
				logical := fmt.Sprintf("%s-%d", s.logical, i)
				_, err := s.store.Write(ctx, logical, c.record, 0)
				_, _, read := s.store.Read(ctx, logical)
				if !errors.Is(err, ErrMalformedRecord) || !errors.Is(read, ErrNoRecord) {
					t.Fatalf("Write of %+v = %v, and the key then reads %v; want ErrMalformedRecord, nothing written (ErrNoRecord)", c.record, err, read)
				}
			})
		}
		t.Run(storeName+", control: the abandoned name retired too", func(t *testing.T) {
			sound := Record{Start: 1, Retired: []string{a, x}, Abandoned: []Abandoned{{Name: x, Until: until}}}
			if _, err := s.store.Write(ctx, s.logical, sound, 0); err != nil {
				t.Fatalf("Write of %+v = %v", sound, err)
			}
			if record, _, err := s.store.Read(ctx, s.logical); err != nil || len(record.Abandoned) != 1 || len(record.Retired) != 2 {
				t.Fatalf("Read of the written record = %+v, %v; want it as written", record, err)
			}
		})
	}
}
