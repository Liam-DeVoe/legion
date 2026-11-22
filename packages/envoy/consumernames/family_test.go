package consumernames

import (
	"errors"
	"testing"
)

// familyCase is a record of logical in the store of family, and whether its family's creator
// serves it: the round-6 cases, which CheckFamily and each creator's own read must answer alike.
type familyCase struct {
	name, family, logical string
	record                Record
	served                bool
}

// familyCases are the round-6 cases for every family the table names: a convert's open replace of
// a session's fixed name among them, which a family table with no fixed rule refuses.
func familyCases() []familyCase {
	durable := DurableKey("listener-m a")
	daemon := "daemon/p"
	return []familyCase{
		{"a session's own current", SessionFamily, "ses-A", Record{Current: "session~A", Start: 3}, true},
		{"a current that is the session's fixed name", SessionFamily, "ses-A", Record{Current: "s-ses-A", Start: 3}, false},
		{"a convert's open replace of s-ses-A", SessionFamily, "ses-A", Record{Start: 3, Op: &Op{Kind: OpReplace, Old: "s-ses-A", Candidate: "session~B", ReservedAt: testEpoch}}, true},
		{"another session's fixed name as the old name", SessionFamily, "ses-A", Record{Start: 3, Op: &Op{Kind: OpReplace, Old: "s-ses-B", Candidate: "session~B", ReservedAt: testEpoch}}, false},
		{"a retired fixed name", SessionFamily, "ses-A", Record{Current: "session~A", Start: 3, Retired: []string{"s-ses-A"}}, false},
		{"a session's current of the listener's durable", SessionFamily, "ses-A", Record{Current: "listener-m~A", Start: 3}, false},
		{"a durable's own current", SessionFamily, durable, Record{Current: "listener-m a~A", Start: 3}, true},
		{"a durable's current of the session prefix", SessionFamily, durable, Record{Current: "session~A", Start: 3}, false},
		{"a durable's old name that is its own name", SessionFamily, durable, Record{Start: 3, Op: &Op{Kind: OpReplace, Old: "listener-m a", Candidate: "listener-m a~B", ReservedAt: testEpoch}}, true},
		{"Dispatch's own current", DispatchFamily, "delivery-events", Record{Current: "delivery-events~A", Start: 3}, true},
		{"Dispatch's current of its other durable", DispatchFamily, "delivery-events", Record{Current: "delivery-jobs~A", Start: 3}, false},
		{"Dispatch's old name that is its fixed name", DispatchFamily, "delivery-events", Record{Start: 3, Op: &Op{Kind: OpReplace, Old: "delivery-events", Candidate: "delivery-events~B", ReservedAt: testEpoch}}, true},
		{"a daemon's own current", daemon, "legion-go-p-wake", Record{Current: "legion-go-p-wake~A", Start: 3}, true},
		{"a daemon's current of a session", daemon, "legion-go-p-wake", Record{Current: "session~A", Start: 3}, false},
	}
}

// CheckFamily judges each record as the family's creator does, by the family's own naming: the
// cases its creators serve pass, and the others are ErrMalformedRecord. A family no store keeps is
// ErrInvalidFamily, and a durable's key DurableKey did not make ErrInvalidKey. Each creator's own
// answer and naming are held to the table in its package (internal/bus, cmd/listener).
func TestCheckFamilyJudgesARecordAsItsFamilysCreator(t *testing.T) {
	for _, c := range familyCases() {
		t.Run(c.name, func(t *testing.T) {
			err := CheckFamily(c.family, c.logical, c.record)
			if c.served && err != nil || !c.served && !errors.Is(err, ErrMalformedRecord) {
				t.Fatalf("CheckFamily(%s, %s, %+v) = %v; want served %t", c.family, c.logical, c.record, err, c.served)
			}
		})
	}
	if err := CheckFamily("daemon", "legion-go-p-wake", Record{}); !errors.Is(err, ErrInvalidFamily) {
		t.Errorf("CheckFamily of the family daemon = %v, want ErrInvalidFamily", err)
	}
	if err := CheckFamily(SessionFamily, "durable.", Record{}); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("CheckFamily of the key durable. = %v, want ErrInvalidKey", err)
	}
	if durable, ok := durableOfKey(DurableKey("listener-m a/b")); !ok || durable != "listener-m a/b" {
		t.Errorf("durableOfKey(DurableKey(listener-m a/b)) = %q, %t", durable, ok)
	}
}

// A durable's key is one DurableKey made, and only such a key: CheckDurableKey refuses a key whose
// escapes durableOfKey cannot read back (durable.a=zz, a '=' with no two hex digits after it) or
// that DurableKey would not write (a lowercase escape, an escape of a byte it writes as itself), so
// CheckFamily never judges a record by an empty or wrong prefix. The control is DurableKey's own
// keys, which pass. Before, CheckDurableKey took any '=' and CheckFamily served a record of
// durable.a=zz naming ~X under the empty prefix.
func TestCheckDurableKeyTakesOnlyAKeyDurableKeyMade(t *testing.T) {
	for _, key := range []string{"durable.a=zz", "durable.a=", "durable.a=4", "durable.a=3d", "durable.=41", "durable."} {
		if err := CheckDurableKey(key); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("CheckDurableKey(%q) = %v, want ErrInvalidKey", key, err)
		}
		if err := CheckFamily(SessionFamily, key, Record{Current: "~X", Start: 3}); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("CheckFamily(session, %q) = %v, want ErrInvalidKey", key, err)
		}
	}
	for _, durable := range []string{"listener-m", "listener-m a", "a=b", "x/y.z"} {
		key := DurableKey(durable)
		if err := CheckDurableKey(key); err != nil {
			t.Errorf("control: CheckDurableKey(DurableKey(%q)) = %v", durable, err)
		}
		if got, ok := durableOfKey(key); !ok || got != durable {
			t.Errorf("control: durableOfKey(DurableKey(%q)) = %q, %t", durable, got, ok)
		}
	}
}

// Every family's naming carries its fixed name, so a reader in another module reads it from the
// table and never calls a nil func or copies a default: the session's is s-<id>, a durable's and
// Dispatch's and a daemon's the logical name, the names their creators use. Before, Fixed was nil
// for the durable, Dispatch and daemon families, and only Names.fixed knew that nil meant the
// prefix.
func TestEveryFamilysNamingCarriesItsFixedName(t *testing.T) {
	durable := DurableKey("listener-m a")
	for _, c := range []struct{ family, logical, fixed string }{
		{SessionFamily, "ses-A", "s-ses-A"},
		{SessionFamily, durable, "listener-m a"},
		{DispatchFamily, "delivery-events", "delivery-events"},
		{"daemon/p", "legion-go-p-wake", "legion-go-p-wake"},
	} {
		naming, err := FamilyNaming(c.family, c.logical)
		switch {
		case err != nil:
			t.Errorf("FamilyNaming(%s, %s) = %v", c.family, c.logical, err)
		case naming.Fixed == nil:
			t.Errorf("FamilyNaming(%s, %s).Fixed is nil; want %q", c.family, c.logical, c.fixed)
		case naming.Fixed(c.logical) != c.fixed:
			t.Errorf("FamilyNaming(%s, %s).Fixed = %q, want %q", c.family, c.logical, naming.Fixed(c.logical), c.fixed)
		}
	}
}
