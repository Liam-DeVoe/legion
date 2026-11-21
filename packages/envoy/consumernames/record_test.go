package consumernames

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/testnats"
)

// A value under a key is that key's record only when it is one JSON object of a Record's fields
// and nothing else, carries the key it belongs to, and every field it sets is well formed. Anything
// else is ErrMalformedRecord: any principal holding a JetStream API grant can have the server store
// its answer under any key of the bucket through a request's reply subject, or copy another key's
// message there whole. The control of each malformed value is today's decode, json.Unmarshal into
// a Record, which takes every one of them that is JSON for a record.
func TestDecodeRecordTakesOnlyTheRecordOfTheKeyItRead(t *testing.T) {
	const key = "ses_reader"
	reserved := `"reserved_at":"2026-11-21T12:00:00Z"`
	for _, value := range []string{
		`{"key":"ses_reader"}`,
		`{"key":"ses_reader","current":"session~A","start":5,"last_floor":{"floor":4,"name":"session~A","stream_id":"stream"},"retired":["session~B"]}`,
		`{"key":"ses_reader","current":"s-ses_reader"}`,
		`{"key":"ses_reader","current":"session~A","op":{"kind":"replace","owner":"o","candidate":"session~C","old":"session~A",` + reserved + `}}`,
		`{"key":"ses_reader","op":{"kind":"create","candidate":"session~C","native":"new",` + reserved + `}}`,
		`{"key":"ses_reader","current":"session~A","op":{"kind":"delete","old":"session~A","floor_recorded":true,` + reserved + `}}`,
		`{"key":"ses_reader","restore":{"target":"s-ses_reader","start":3,"phase":"planned"}}`,
		` {"key":"ses_reader"} ` + "\n",
	} {
		t.Run("takes "+value, func(t *testing.T) {
			record, err := decodeRecord(key, []byte(value))
			if err != nil || record.Key != key {
				t.Fatalf("decodeRecord(%q) = %+v, %v; want the record of %s", value, record, err, key)
			}
		})
	}
	for name, value := range map[string]string{
		"a STREAM.INFO answer":       `{"type":"io.nats.jetstream.api.v1.stream_info_response","total":0,"config":{"name":"KV_envoy_session_consumers"},"state":{"messages":1}}`,
		"a lease of another bucket":  `{"holder":"listener-a","renewed_at":"2026-11-21T12:00:00Z"}`,
		"another key's record":       `{"key":"ses_other","current":"session~A","start":5}`,
		"a record carrying no key":   `{"current":"session~A","start":5}`,
		"an empty key":               `{"key":"","current":"session~A"}`,
		"a field no record has":      `{"key":"ses_reader","currnet":"session~A"}`,
		"an op field no op has":      `{"key":"ses_reader","op":{"kind":"create","candidate":"session~C","extra":1,` + reserved + `}}`,
		"data after the record":      `{"key":"ses_reader"} {"key":"ses_reader"}`,
		"bytes after the record":     `{"key":"ses_reader"}x`,
		"a field of the wrong type":  `{"key":"ses_reader","start":"5"}`,
		"an unknown op kind":         `{"key":"ses_reader","op":{"kind":"rename","candidate":"session~C",` + reserved + `}}`,
		"an op reserved at no time":  `{"key":"ses_reader","op":{"kind":"create","candidate":"session~C"}}`,
		"a create with no candidate": `{"key":"ses_reader","op":{"kind":"create",` + reserved + `}}`,
		"a replace with no old":      `{"key":"ses_reader","op":{"kind":"replace","candidate":"session~C",` + reserved + `}}`,
		"a delete with no old":       `{"key":"ses_reader","op":{"kind":"delete",` + reserved + `}}`,
		"an unknown native policy":   `{"key":"ses_reader","op":{"kind":"create","candidate":"session~C","native":"last",` + reserved + `}}`,
		"a current no consumer has":  `{"key":"ses_reader","current":"session A"}`,
		"a dotted current":           `{"key":"ses_reader","current":"session.A"}`,
		"an empty retired name":      `{"key":"ses_reader","retired":[""]}`,
		"a last floor of no name":    `{"key":"ses_reader","last_floor":{"floor":4,"stream_id":"stream"}}`,
		"a restore of no target":     `{"key":"ses_reader","restore":{"start":3,"phase":"planned"}}`,
		"a restore of no phase":      `{"key":"ses_reader","restore":{"target":"s-ses_reader","start":3}}`,
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			if _, err := decodeRecord(key, []byte(value)); !errors.Is(err, ErrMalformedRecord) {
				t.Fatalf("decodeRecord(%q) = %v, want ErrMalformedRecord", value, err)
			}
		})
	}
	for name, value := range map[string]string{"null": "null", "an array": "[]", "a string": `"x"`, "nothing": "", "a number": "5"} {
		t.Run("refuses "+name, func(t *testing.T) {
			if _, err := decodeRecord(key, []byte(value)); !errors.Is(err, ErrMalformedRecord) {
				t.Fatalf("decodeRecord(%q) = %v, want ErrMalformedRecord", value, err)
			}
		})
	}
	t.Run("control: today's decode takes a STREAM.INFO answer and another key's record", func(t *testing.T) {
		for _, value := range []string{
			`{"type":"io.nats.jetstream.api.v1.stream_info_response","total":0,"config":{"name":"KV_envoy_session_consumers"}}`,
			`{"key":"ses_other","current":"session~A","start":5}`,
		} {
			var record Record
			if err := json.Unmarshal([]byte(value), &record); err != nil {
				t.Fatalf("json.Unmarshal(%q) = %v, want today's lenient decode to take it", value, err)
			}
		}
	})
}

// The sentinel is the sentinel key's own StoreState, read as strictly as a record: anything else
// under that key is a missing sentinel, never a sentinel with no cutover marker. It names its
// store's family, which a sentinel without one, with one of no form, or with another store's fails:
// Key alone cannot tell two Postgres stores apart. The controls are today's decode, which takes a
// STREAM.INFO answer for a sentinel whose cutover has not run, and takes a sentinel that names no
// family.
func TestDecodeSentinelTakesOnlyTheSentinelOfItsKey(t *testing.T) {
	for value, family := range map[string]string{
		`{"key":"envoy.bucket","family":"session"}`:                                                                                                     SessionFamily,
		`{"key":"envoy.bucket","family":"session","cutover":"done","boundary":1,"stream_id":"stream"}`:                                                  SessionFamily,
		`{"key":"envoy.bucket","family":"dispatch","cutover":"rolled_back","replay_from":7,"orphans":["session~A"],"backup_sha256":"none","hold":"h1"}`: DispatchFamily,
		`{"key":"envoy.bucket","family":"daemon/legion-go"}`:                                                                                            daemonFamily(t, "legion-go"),
	} {
		t.Run("takes "+value, func(t *testing.T) {
			state, err := decodeSentinel(SentinelKey, family, []byte(value))
			if err != nil || state.Key != SentinelKey || state.Family != family {
				t.Fatalf("decodeSentinel(%q) = %+v, %v; want the sentinel of the family %s", value, state, err, family)
			}
		})
	}
	for name, value := range map[string]string{
		"a STREAM.INFO answer":          `{"type":"io.nats.jetstream.api.v1.stream_info_response","total":0,"config":{"name":"KV_envoy_session_consumers"}}`,
		"a sentinel carrying no key":    `{"family":"session","cutover":"done","boundary":1}`,
		"a sentinel naming no family":   `{"key":"envoy.bucket","cutover":"done","boundary":1}`,
		"an empty family":               `{"key":"envoy.bucket","family":"","cutover":"done"}`,
		"a family of no form":           `{"key":"envoy.bucket","family":"sessions","cutover":"done"}`,
		"a daemon family of no project": `{"key":"envoy.bucket","family":"daemon/","cutover":"done"}`,
		"a daemon project with a slash": `{"key":"envoy.bucket","family":"daemon/a/b","cutover":"done"}`,
		"a record of a session":         `{"key":"ses_a","current":"session~A"}`,
		"another store's sentinel":      `{"key":"envoy.sentinel","family":"session","cutover":"done"}`,
		"another family's sentinel":     `{"key":"envoy.bucket","family":"dispatch","cutover":"done"}`,
		"a daemon's sentinel":           `{"key":"envoy.bucket","family":"daemon/legion-go","cutover":"done"}`,
		"an unknown cutover marker":     `{"key":"envoy.bucket","family":"session","cutover":"started"}`,
		"an orphan no consumer has":     `{"key":"envoy.bucket","family":"session","orphans":["session A"]}`,
		"data after the sentinel":       `{"key":"envoy.bucket","family":"session"}{}`,
		"null":                          `null`,
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			if _, err := decodeSentinel(SentinelKey, SessionFamily, []byte(value)); !errors.Is(err, ErrMalformedSentinel) || errors.Is(err, ErrNoSentinel) {
				t.Fatalf("decodeSentinel(%q) = %v, want ErrMalformedSentinel", value, err)
			}
		})
	}
	t.Run("control: today's decode takes a STREAM.INFO answer for a sentinel with no marker", func(t *testing.T) {
		var state StoreState
		if err := json.Unmarshal([]byte(`{"type":"io.nats.jetstream.api.v1.stream_info_response","total":0}`), &state); err != nil || state.Cutover != "" {
			t.Fatalf("json.Unmarshal = %+v, %v; want a sentinel with no marker, taken", state, err)
		}
	})
	t.Run("control: today's decode takes a sentinel that names no family", func(t *testing.T) {
		var state StoreState
		if err := json.Unmarshal([]byte(`{"key":"envoy.bucket","cutover":"done","boundary":1}`), &state); err != nil || state.Family != "" || state.Cutover != CutoverDone {
			t.Fatalf("json.Unmarshal = %+v, %v; want a done sentinel with no family, taken", state, err)
		}
	})
}

// A sentinel's family round-trips through both stores and CreateSessionBucket: what the caller
// gives is what State reads back, and CreateSessionBucket names the session family alone.
func TestASentinelsFamilyRoundTrips(t *testing.T) {
	ctx := context.Background()
	for _, family := range []string{DispatchFamily, daemonFamily(t, "legion-go")} {
		store := pgStore(t, family, newMemPG())
		if _, err := store.WriteSentinel(ctx, StoreState{Family: family, Cutover: CutoverDone}, 0); err != nil {
			t.Fatal(err)
		}
		if state, err := store.State(ctx); err != nil || state.Family != family {
			t.Fatalf("PGStore's sentinel written with the family %s read back as %+v, %v", family, state, err)
		}
	}
	_, uri := testnats.Start(t)
	conn := testnats.Connect(t, uri)
	t.Cleanup(conn.Close)
	if _, err := CreateSessionBucket(ctx, conn, 1, StoreState{Family: DispatchFamily, Cutover: CutoverDone}); err == nil || !strings.Contains(err.Error(), SessionFamily) {
		t.Fatalf("CreateSessionBucket with the family %s = %v, want a refusal naming %s", DispatchFamily, err, SessionFamily)
	}
	kv, err := CreateSessionBucket(ctx, conn, 1, StoreState{Cutover: CutoverDone, Boundary: 1})
	if err != nil {
		t.Fatal(err)
	}
	if state, err := kv.State(ctx); err != nil || state.Family != SessionFamily {
		t.Fatalf("the session bucket's sentinel read back as %+v, %v; want the family %s", state, err, SessionFamily)
	}
}

// sentinelStore is a store's sentinel, as each store reads and writes it.
type sentinelStore interface {
	State(ctx context.Context) (StoreState, error)
	WriteSentinel(ctx context.Context, state StoreState, revision uint64) (uint64, error)
}

// Each store's WriteSentinel writes its own family itself: a state that names no family is
// written naming the store's, and one that names another family is refused, naming both, with
// nothing written, so no caller writes another family's sentinel, or one its store cannot read, by
// mistake. Before, WriteSentinel wrote whatever family its caller gave: a caller that forgot the
// family wrote a sentinel its own store then refused to read, and one that named another family
// wrote that family's. The control is the family-less sentinel put under the key as bytes, which
// the store refuses to read.
func TestWriteSentinelWritesItsStoresOwnFamily(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, store sentinelStore, family, key string, put func(data []byte)) {
		revision, err := store.WriteSentinel(ctx, StoreState{Cutover: CutoverDone, Boundary: 1}, 0)
		if err != nil {
			t.Fatalf("WriteSentinel of a state naming no family = %v", err)
		}
		if state, err := store.State(ctx); err != nil || state.Family != family || state.Cutover != CutoverDone {
			t.Errorf("the sentinel written with no family read back as %+v, %v; want the store's family %s, done", state, err, family)
		}
		_, err = store.WriteSentinel(ctx, StoreState{Family: DispatchFamily, Cutover: CutoverRolledBack}, revision)
		if !errors.Is(err, ErrSentinelFamily) || !strings.Contains(err.Error(), `"`+DispatchFamily+`"`) || !strings.Contains(err.Error(), `"`+family+`"`) {
			t.Errorf("WriteSentinel of another family's state = %v; want ErrSentinelFamily naming both families", err)
		}
		if state, err := store.State(ctx); err != nil || state.Cutover == CutoverRolledBack {
			t.Errorf("after the refused write the sentinel reads %+v, %v; want the one written before", state, err)
		}
		data, err := json.Marshal(StoreState{Key: key, Cutover: CutoverDone})
		if err != nil {
			t.Fatal(err)
		}
		put(data)
		if state, err := store.State(ctx); !errors.Is(err, ErrMalformedSentinel) {
			t.Errorf("control: a sentinel under the key that names no family read as %+v, %v; want ErrMalformedSentinel", state, err)
		}
	}
	t.Run("PGStore", func(t *testing.T) {
		db := newMemPG()
		family := daemonFamily(t, "legion-go")
		run(t, pgStore(t, family, db), family, PGSentinelKey(family), func(data []byte) {
			db.rows[PGSentinelKey(family)] = pgRow{data: data, revision: 9}
		})
	})
	t.Run("KVStore", func(t *testing.T) {
		live := newLiveStore(t, testnats.URL(t), nil)
		run(t, live.store, SessionFamily, SentinelKey, func(data []byte) {
			if _, err := live.js.Publish(ctx, "$KV."+SessionBucket+"."+SentinelKey, data); err != nil {
				t.Fatal(err)
			}
		})
	})
}

// Each store reads only a sentinel that names its own family (KVStore the session family's, a
// PGStore the one NewPGStore was given):
// a key alone cannot tell two stores apart, so a well-formed sentinel naming another family, or one
// naming none, under the store's sentinel key is a malformed sentinel, never a marker read. The
// control in each store is a sentinel of its own family, read. Without the family, PGStore read
// another store's sentinel as its own: Dispatch's store took a daemon's done marker, which a creator
// in the fixed mode would obey.
func TestEachStoreReadsOnlyTheSentinelOfItsOwnFamily(t *testing.T) {
	ctx := context.Background()
	others := map[string][]string{
		SessionFamily:                {DispatchFamily, daemonFamily(t, "legion-go")},
		DispatchFamily:               {SessionFamily, daemonFamily(t, "legion-go")},
		daemonFamily(t, "legion-go"): {daemonFamily(t, "legion-other"), DispatchFamily, SessionFamily},
	}
	refused := func(t *testing.T, store, family string, state StoreState, err error) {
		t.Helper()
		if !errors.Is(err, ErrMalformedSentinel) || errors.Is(err, ErrNoSentinel) {
			t.Fatalf("%s reading a sentinel of the family %q = %+v, %v; want ErrMalformedSentinel, never a missing sentinel", store, family, state, err)
		}
		if family != "" && (!errors.Is(err, ErrSentinelFamily) || !strings.Contains(err.Error(), family)) {
			t.Fatalf("%s reading a sentinel of the family %q = %v; want ErrSentinelFamily naming it", store, family, err)
		}
	}
	for _, family := range []string{DispatchFamily, daemonFamily(t, "legion-go")} {
		t.Run("PGStore of "+family, func(t *testing.T) {
			db := newMemPG()
			store := pgStore(t, family, db)
			sentinel := func(of string) {
				data, err := json.Marshal(StoreState{Key: PGSentinelKey(family), Family: of, Cutover: CutoverDone})
				if err != nil {
					t.Fatal(err)
				}
				db.rows[PGSentinelKey(family)] = pgRow{data: data, revision: 1}
			}
			for _, other := range append(others[family], "") {
				sentinel(other)
				state, err := store.State(ctx)
				refused(t, "PGStore of "+family, other, state, err)
			}
			sentinel(family)
			if state, err := store.State(ctx); err != nil || state.Family != family || state.Cutover != CutoverDone {
				t.Fatalf("control: PGStore of %s reading its own family's sentinel = %+v, %v", family, state, err)
			}
		})
	}
	for _, other := range append(others[SessionFamily], "") {
		t.Run("KVStore of session, a sentinel of "+other, func(t *testing.T) {
			live := newLiveStore(t, testnats.URL(t), nil)
			// WriteSentinel writes the session family alone, so another family's sentinel is put
			// under the key as bytes, as a principal with a write of the bucket can.
			data, err := json.Marshal(StoreState{Key: SentinelKey, Family: other, Cutover: CutoverDone, Boundary: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := live.js.Publish(ctx, "$KV."+SessionBucket+"."+SentinelKey, data); err != nil {
				t.Fatal(err)
			}
			state, err := live.store.State(ctx)
			refused(t, "KVStore of session", other, state, err)
		})
	}
	live := newLiveStore(t, testnats.URL(t), &StoreState{Cutover: CutoverDone, Boundary: 1})
	if state, err := live.store.State(ctx); err != nil || state.Family != SessionFamily {
		t.Fatalf("control: KVStore of session reading its own family's sentinel = %+v, %v", state, err)
	}
}

// A sentinel row that is there but is not its store's own exact sentinel is never a missing
// sentinel, in either store: a creator in the fixed mode reads ErrNoSentinel as no marker (Dispatch
// and the daemons do), so such a row must be a failed read, which never runs fixed names over a
// store a newer build may have converted, and it still refuses every call in the fresh mode. The
// rows are another family's sentinel, a sentinel under another key, one carrying a field this
// build does not know (as a newer operator build's could), and the server's STREAM.INFO answer. The
// control in each store is the absent row, the one missing sentinel.
func TestAMalformedSentinelIsNeverNoMarker(t *testing.T) {
	ctx := context.Background()
	// noMarker is how a fixed-mode creator reads a store's sentinel: ErrNoSentinel is no marker.
	noMarker := func(err error) bool { return errors.Is(err, ErrNoSentinel) }
	malformed := func(key, family string) map[string]string {
		return map[string]string{
			"another family's sentinel":        fmt.Sprintf(`{"key":%q,"family":"daemon/legion-other","cutover":"done"}`, key),
			"a sentinel under another key":     fmt.Sprintf(`{"key":"envoy.sentinel/daemon/legion-other","family":%q,"cutover":"done"}`, family),
			"a field this build does not know": fmt.Sprintf(`{"key":%q,"family":%q,"cutover":"done","epoch":2}`, key, family),
			"a STREAM.INFO answer":             `{"type":"io.nats.jetstream.api.v1.stream_info_response","total":0}`,
		}
	}
	refused := func(t *testing.T, row string, err error) {
		t.Helper()
		if noMarker(err) || !errors.Is(err, ErrMalformedSentinel) || !IsSelectionStateLost(err) {
			t.Fatalf("State over %s = %v; want ErrMalformedSentinel, which is no missing sentinel and refuses every call", row, err)
		}
	}
	absent := func(t *testing.T, err error) {
		t.Helper()
		if !noMarker(err) {
			t.Fatalf("control: State with no sentinel row = %v, want ErrNoSentinel", err)
		}
	}
	pgRows := func(t *testing.T, store *PGStore, put func(data string)) {
		_, err := store.State(ctx)
		absent(t, err)
		for row, data := range malformed(PGSentinelKey(store.family), store.family) {
			put(data)
			_, err := store.State(ctx)
			refused(t, row, err)
		}
	}
	t.Run("PGStore", func(t *testing.T) {
		db := newMemPG()
		store := pgStore(t, DispatchFamily, db)
		pgRows(t, store, func(data string) {
			db.rows[PGSentinelKey(DispatchFamily)] = pgRow{data: []byte(data), revision: 1}
		})
	})
	t.Run("PGStore on a real Postgres", func(t *testing.T) {
		pool := pgTestDatabase(t)
		store := pgStore(t, DispatchFamily, pool)
		pgRows(t, store, func(data string) {
			if _, err := pool.Exec(ctx, `insert into `+IntakeConsumersTable+` (logical, record, revision) values ($1, $2, 1)
				on conflict (logical) do update set record = excluded.record`, PGSentinelKey(DispatchFamily), data); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("KVStore", func(t *testing.T) {
		live := newLiveStore(t, testnats.URL(t), nil)
		_, err := live.store.State(ctx)
		absent(t, err)
		for row, data := range malformed(SentinelKey, SessionFamily) {
			if _, err := live.js.Publish(ctx, "$KV."+SessionBucket+"."+SentinelKey, []byte(data)); err != nil {
				t.Fatal(err)
			}
			_, err := live.store.State(ctx)
			refused(t, row, err)
		}
	})
}

// pgRow is one row of memPG: the record's bytes and the revision, or err.
type pgRow struct {
	data     []byte
	revision int64
	err      error
}

func (r pgRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	switch len(dest) {
	case 1:
		*dest[0].(*int64) = r.revision
	case 2:
		*dest[0].(*[]byte) = r.data
		*dest[1].(*int64) = r.revision
	}
	return nil
}

// PGStore reads and writes the same self-keyed records: a write stamps the record and the sentinel
// with their own keys, the family's (PGKey, PGSentinelKey), and a read refuses a row whose record
// is another key's or no record at all, as KVStore does.
func TestThePGStoreWritesAndReadsOnlySelfKeyedRecords(t *testing.T) {
	ctx := context.Background()
	db := newMemPG()
	family := daemonFamily(t, "legion-go")
	store := pgStore(t, family, db)
	if _, err := store.Write(ctx, "legion-go-CAPTURE-wake", Record{Current: "legion-go-CAPTURE-wake~A"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteSentinel(ctx, StoreState{Family: family, Cutover: CutoverDone}, 0); err != nil {
		t.Fatal(err)
	}
	own := "daemon/legion-go/legion-go-CAPTURE-wake"
	if got := string(db.rows[own].data); !strings.Contains(got, `"key":"`+own+`"`) {
		t.Fatalf("the written record %s carries not its own key %s", got, own)
	}
	if got := string(db.rows["envoy.sentinel/daemon/legion-go"].data); !strings.Contains(got, `"key":"envoy.sentinel/daemon/legion-go"`) {
		t.Fatalf("the written sentinel %s carries not its own key", got)
	}
	db.rows[PGKey(family, "legion-go-other")] = pgRow{data: db.rows[own].data, revision: 4}
	if record, _, err := store.Read(ctx, "legion-go-CAPTURE-wake"); err != nil || record.Current != "legion-go-CAPTURE-wake~A" {
		t.Fatalf("control: Read of the record's own key = %+v, %v", record, err)
	}
	if state, err := store.State(ctx); err != nil || state.Cutover != CutoverDone {
		t.Fatalf("control: State = %+v, %v", state, err)
	}
	if _, _, err := store.Read(ctx, "legion-go-other"); !errors.Is(err, ErrMalformedRecord) || !strings.Contains(err.Error(), "legion-go-other") {
		t.Fatalf("Read of a row holding another key's record = %v, want ErrMalformedRecord naming legion-go-other", err)
	}
	db.rows[PGSentinelKey(family)] = pgRow{data: []byte(`{"cutover":"done"}`), revision: 2}
	if _, err := store.State(ctx); !errors.Is(err, ErrMalformedSentinel) {
		t.Fatalf("State of a sentinel row carrying no key = %v, want ErrMalformedSentinel", err)
	}
}

// memPG is a database of one intake_consumers table in memory, as PGStore's statements use it: a
// select reads a row by key, an insert at revision 0 refuses a row that exists, an update writes
// only where the row holds the revision given, and List's query reads every row its where clause
// keeps, in key order.
type memPG struct{ rows map[string]pgRow }

func newMemPG() *memPG { return &memPG{rows: map[string]pgRow{}} }

func (m *memPG) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	key := args[0].(string)
	row, held := m.rows[key]
	switch statement := strings.TrimSpace(sql); {
	case strings.HasPrefix(statement, "select"):
		if !held {
			return pgRow{err: pgx.ErrNoRows}
		}
		return row
	case strings.HasPrefix(statement, "insert"):
		if held {
			return pgRow{err: pgx.ErrNoRows}
		}
		m.rows[key] = pgRow{data: args[1].([]byte), revision: 1}
		return pgRow{revision: 1}
	default:
		if !held || row.revision != args[2].(int64) {
			return pgRow{err: pgx.ErrNoRows}
		}
		m.rows[key] = pgRow{data: args[1].([]byte), revision: row.revision + 1}
		return pgRow{revision: row.revision + 1}
	}
}

// Query answers List's query: `logical <> $1` keeps every row but the key given, and
// `left(logical, length($1)) = $1` every row whose key begins with it.
func (m *memPG) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	arg := args[0].(string)
	var keys []string
	for key := range m.rows {
		switch {
		case strings.Contains(sql, "logical <> $1") && key != arg,
			strings.Contains(sql, "left(logical, length($1)) = $1") && strings.HasPrefix(key, arg):
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return &memRows{m: m, keys: keys, at: -1}, nil
}

// memRows is memPG's answer to a query, one row of (key, record) at a time. It implements only
// what List calls; the embedded pgx.Rows is nil, so any other call panics.
type memRows struct {
	pgx.Rows
	m    *memPG
	keys []string
	at   int
}

func (r *memRows) Close()     {}
func (r *memRows) Err() error { return nil }
func (r *memRows) Next() bool { r.at++; return r.at < len(r.keys) }
func (r *memRows) Scan(dest ...any) error {
	*dest[0].(*string) = r.keys[r.at]
	*dest[1].(*[]byte) = r.m.rows[r.keys[r.at]].data
	return nil
}

// Several families' stores can share one database: several projects' daemons do. Each reads and
// writes only its own family's sentinel, under its own key, and lists only its own family's records
// and never a sentinel, so one project's cutover (its convert writing done to its sentinel) leaves
// another project's fixed-mode daemon reading "not done", and each reads only its own records. A
// record copied into one family's place from another's carries the other family's key, and is
// malformed there. The control is the single sentinel row every store shared before: the second
// project's sentinel could not be written beside the first's, and once the first's was written, the
// second's daemon read it as another family's, malformed, and could never boot.
func TestStoresOfSeveralFamiliesShareOneDatabase(t *testing.T) {
	ctx := context.Background()
	db := newMemPG()
	p, q, d := pgStore(t, daemonFamily(t, "p"), db), pgStore(t, daemonFamily(t, "q"), db), pgStore(t, DispatchFamily, db)
	for name, store := range map[string]*PGStore{"p": p, "q": q, "dispatch": d} {
		if _, err := store.WriteSentinel(ctx, StoreState{Family: store.family}, 0); err != nil {
			t.Fatalf("the %s store's first sentinel beside the others' = %v", name, err)
		}
	}
	writes := map[*PGStore]string{p: "legion-go-p-github", q: "legion-go-q-github", d: "delivery-events"}
	for store, logical := range writes {
		if _, err := store.Write(ctx, logical, Record{Start: 7}, 0); err != nil {
			t.Fatalf("write %s in the %s store = %v", logical, store.family, err)
		}
	}
	// p's convert marks p's store converted.
	if _, err := p.WriteSentinel(ctx, StoreState{Family: daemonFamily(t, "p"), Cutover: CutoverDone}, 1); err != nil {
		t.Fatalf("p's convert writing done to p's sentinel = %v", err)
	}
	if state, err := p.State(ctx); err != nil || state.Family != daemonFamily(t, "p") || state.Cutover != CutoverDone {
		t.Fatalf("p's sentinel after p's convert = %+v, %v; want p's, done", state, err)
	}
	for _, store := range []*PGStore{q, d} {
		if state, err := store.State(ctx); err != nil || state.Family != store.family || state.Cutover != "" {
			t.Fatalf("the %s store's sentinel after p's convert = %+v, %v; want its own, not done", store.family, state, err)
		}
	}
	for store, logical := range writes {
		records, err := store.List(ctx)
		if err != nil || len(records) != 1 || records[logical].Start != 7 {
			t.Fatalf("List of the %s store = %v, %v; want %s alone, and no sentinel", store.family, records, err, logical)
		}
		for other, otherLogical := range writes {
			if other != store {
				if _, _, err := store.Read(ctx, otherLogical); !errors.Is(err, ErrNoRecord) {
					t.Fatalf("the %s store reading %s, the %s store's = %v; want ErrNoRecord", store.family, otherLogical, other.family, err)
				}
			}
		}
	}
	// q's record copied whole into p's place for the same name.
	db.rows[daemonFamily(t, "p")+"/legion-go-q-github"] = db.rows[daemonFamily(t, "q")+"/legion-go-q-github"]
	if _, _, err := p.Read(ctx, "legion-go-q-github"); !errors.Is(err, ErrMalformedRecord) {
		t.Fatalf("p reading q's record copied into p's place = %v; want ErrMalformedRecord", err)
	}
}

// A store is made only for a family it can keep, so no store reads, writes or lists the rows of a
// family it does not keep: NewPGStore and DaemonFamily refuse any other, as ErrInvalidFamily naming
// it. Before, a store of "daemon", with no project, listed every project's records, each well
// formed under its own key, and a store of daemon/p/b wrote rows that daemon/p's store then listed
// as its own. The control is today's acceptance of every family a store keeps.
func TestAStoreIsMadeOnlyForAFamilyItCanKeep(t *testing.T) {
	db := newMemPG()
	for _, family := range []string{"daemon", "daemon/", "daemon/p/b", "daemon/p q", "", "sessions", "dispatch/x"} {
		if store, err := NewPGStore(family, db); !errors.Is(err, ErrInvalidFamily) || !strings.Contains(err.Error(), fmt.Sprintf("%q", family)) || store != nil {
			t.Fatalf("NewPGStore(%q) = %v, %v; want ErrInvalidFamily naming it", family, store, err)
		}
	}
	for _, project := range []string{"", "p/b", "p q"} {
		if family, err := DaemonFamily(project); !errors.Is(err, ErrInvalidFamily) || !strings.Contains(err.Error(), fmt.Sprintf("%q", "daemon/"+project)) || family != "" {
			t.Fatalf("DaemonFamily(%q) = %q, %v; want ErrInvalidFamily naming daemon/%s", project, family, err, project)
		}
	}
	for _, family := range []string{SessionFamily, DispatchFamily, "daemon/p", "daemon/legion-go_a.b"} {
		if store, err := NewPGStore(family, db); err != nil || store.family != family {
			t.Fatalf("control: NewPGStore(%q) = %v, %v; want the store of that family", family, store, err)
		}
	}
	if family, err := DaemonFamily("legion-go"); err != nil || family != "daemon/legion-go" {
		t.Fatalf("control: DaemonFamily(legion-go) = %q, %v; want daemon/legion-go", family, err)
	}
}

// pgStore is NewPGStore's store of family on db, failing the test when NewPGStore refuses it.
func pgStore(t testing.TB, family string, db PGQuerier) *PGStore {
	t.Helper()
	store, err := NewPGStore(family, db)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// daemonFamily is DaemonFamily(project), failing the test when DaemonFamily refuses it.
func daemonFamily(t testing.TB, project string) string {
	t.Helper()
	family, err := DaemonFamily(project)
	if err != nil {
		t.Fatal(err)
	}
	return family
}
