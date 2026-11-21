package consumernames

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// The JetStream API's answers status reads by code.
const (
	statusNoMessageFound = 10037
	statusStreamNotFound = 10059
)

// statusKVOperation is the header that marks a KV delete or purge marker (nats.go kv.go).
const statusKVOperation = "KV-Operation"

// statusKey is one key of the selection bucket, as its last message holds it.
type statusKey struct {
	// Key is the KV key: a session id, a listener durable's selection.DurableKey, or a system key
	// (selection.SentinelKey, hold.barrier.<hold id>).
	Key string
	// Seq is the stream sequence of the key's last message, which is the record's revision.
	Seq uint64
	// Deleted is a last message that is a delete or a purge marker; Value is then nil.
	Deleted bool
	// Value is the last message's data.
	Value []byte
}

// statusSelectionKeys reads every key of the selection bucket on nc, by sequence: STREAM.INFO of
// its stream, then STREAM.MSG.GET from the first sequence with next_by_subj on the bucket's
// subjects, each answer's sequence + 1 the next request's, until the server answers that no
// message is left (10037). These are the two calls natsgrant.Operator(released) names on the
// bucket (natsgrant.go operatorReleased), so status, which runs in phase released, reads with no
// key listing. The last message of each key is kept: a key written again during the walk is met
// again past the end it had, so the walk ends only once it has passed every key's last write. A
// missing bucket wraps selection.ErrNoStore; a walk that cannot end, a refused request or an answer
// that does not decode is *Unknown. The keys are sorted.
func statusSelectionKeys(ctx context.Context, nc *nats.Conn) ([]statusKey, error) {
	stream := "KV_" + selection.SessionBucket
	info, err := streamInfo(ctx, nc, stream)
	var api *jetstream.APIError
	if errors.As(err, &api) && api.ErrorCode == statusStreamNotFound {
		return nil, fmt.Errorf("%w: %s", selection.ErrNoStore, stream)
	}
	if err != nil {
		return nil, unknownf(err, "the info of %s", stream)
	}
	prefix := "$KV." + selection.SessionBucket + "."
	keys := map[string]statusKey{}
	next := max(info.State.FirstSeq, 1)
	// The walk reads each message once, and a key written during it once more: twice the
	// messages the stream held, and a margin, bound it.
	for range 2*info.State.Msgs + 10_000 {
		raw, err := apiRequest(ctx, nc, "$JS.API.STREAM.MSG.GET."+stream, map[string]any{"seq": next, "next_by_subj": prefix + ">"})
		if errors.As(err, &api) && api.ErrorCode == statusNoMessageFound {
			out := make([]statusKey, 0, len(keys))
			for _, key := range keys {
				out = append(out, key)
			}
			slices.SortFunc(out, func(a, b statusKey) int { return strings.Compare(a.Key, b.Key) })
			return out, nil
		}
		if err != nil {
			return nil, unknownf(err, "the message of %s at or after sequence %d", stream, next)
		}
		var answer struct {
			Message *struct {
				Subject string `json:"subject"`
				Seq     uint64 `json:"seq"`
				Header  []byte `json:"hdrs"`
				Data    []byte `json:"data"`
			} `json:"message"`
		}
		if err := json.Unmarshal(raw, &answer); err != nil || answer.Message == nil || answer.Message.Seq < next {
			return nil, unknownf(err, "the answer for %s at or after sequence %d holds no message past it", stream, next)
		}
		m := answer.Message
		key := statusKey{Key: strings.TrimPrefix(m.Subject, prefix), Seq: m.Seq, Value: m.Data}
		if len(m.Header) > 0 {
			header, err := nats.DecodeHeadersMsg(m.Header)
			if err != nil {
				return nil, unknownf(err, "the header of %s's message %d", stream, m.Seq)
			}
			if op := header.Get(statusKVOperation); op == "DEL" || op == "PURGE" {
				key.Deleted, key.Value = true, nil
			}
		}
		keys[key.Key] = key
		next = m.Seq + 1
	}
	return nil, unknownf(nil, "the walk of %s did not end", stream)
}

// statusFamily is one family's store as status read it.
type statusFamily struct {
	family string
	// where names the store: the bucket's stream or the table.
	where string
	// present is a store that exists: the bucket, or the table.
	present bool
	// state is the store's sentinel, nil where none was read.
	state *selection.StoreState
	// records are the family's records by logical name.
	records map[string]selection.Record
	// deleted are keys whose last write is a delete or a purge marker.
	deleted []string
}

// status reads, with the row released, every family's store and the notification stream's
// consumers, and prints: the row; with --hold, that hold's journal; the broker's cutover rows;
// each store's sentinel; each record, with its current and whether the stream holds it, its open
// operation, start, last floor, retired names and restore; and each blocker: a store or sentinel
// that is missing, a record deleted, a current the stream does not hold, an open operation, and a
// fixed name still on the stream. It changes nothing. A complete reading exits 0, blockers or
// not, and ends with their count; a store it could not read (no flag names it, or a read failed)
// or a listing it could not complete is UNKNOWN, exit 2, after it has printed what it read.
func (r *run) status(ctx context.Context) error {
	row, err := r.requirePhase(ctx, natsgrant.OperatorPhaseReleased)
	if err != nil {
		return err
	}
	out := r.stdout
	fmt.Fprintf(out, "row: gen %d, phase %s, hold %q\n", row.Gen, row.Phase, row.HoldID)
	if r.o.holdID != "" {
		d, gen, err := r.records.Journal(ctx, r.o.holdID)
		if err != nil {
			return unknownf(err, "hold %s's journal", r.o.holdID)
		}
		fmt.Fprintf(out, "hold %s: workflow %s, record gen %d, complete %t, hold_gen %d\n", r.o.holdID, d.Workflow, gen, d.Complete, d.HoldGen)
		for i, op := range d.Ops {
			fmt.Fprintf(out, "  %d. %s %s\n", i+1, op.Command, op.State)
		}
	}
	cutovers, err := r.records.Cutovers(ctx)
	if err != nil {
		return unknownf(err, "the broker's cutover rows")
	}
	slices.SortFunc(cutovers, func(a, b holdrecord.CutoverRow) int { return strings.Compare(a.Family, b.Family) })
	for _, c := range cutovers {
		fmt.Fprintf(out, "cutover %s: %s (hold %s, hold_gen %d)\n", c.Family, c.State, c.HoldID, c.HoldGen)
	}
	nc, _, err := r.conn(ctx)
	if err != nil {
		return unknownf(err, "the hub")
	}
	names, err := consumerNames(ctx, nc, notifications)
	if err != nil {
		return err
	}
	held := map[string]bool{}
	for _, name := range names {
		held[name] = true
	}

	var unknown, blockers []string
	families := []statusFamily{}
	session, err := statusSessionFamily(ctx, nc)
	if err != nil {
		unknown = append(unknown, err.Error())
	} else {
		families = append(families, session)
	}
	for _, family := range statusPGFamilies(r.o, cutovers, names) {
		got, err := r.statusPGFamily(ctx, family)
		if err != nil {
			unknown = append(unknown, err.Error())
			continue
		}
		families = append(families, got)
	}
	for _, f := range families {
		blockers = append(blockers, statusPrintFamily(out, f, held)...)
	}
	for _, name := range names {
		if m, ok := classify(name); ok {
			blockers = append(blockers, fmt.Sprintf("%s: the fixed name %s is on %s", m.Family, name, notifications))
		}
	}
	for _, b := range blockers {
		fmt.Fprintf(out, "BLOCKER %s\n", b)
	}
	if len(unknown) > 0 {
		return &Unknown{What: strings.Join(unknown, "; ")}
	}
	fmt.Fprintf(out, "blockers: %d\n", len(blockers))
	return nil
}

// statusSessionFamily is the session family's store read whole: the selection bucket's sentinel
// and every record of a session key (selection.CheckKey) or a durable key
// (selection.CheckDurableKey). A missing bucket is a store status reports as missing, not an
// error; a walk that is not whole is.
func statusSessionFamily(ctx context.Context, nc *nats.Conn) (statusFamily, error) {
	f := statusFamily{family: familySession, where: "KV_" + selection.SessionBucket, records: map[string]selection.Record{}}
	keys, err := statusSelectionKeys(ctx, nc)
	if errors.Is(err, selection.ErrNoStore) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	for _, key := range keys {
		switch {
		case key.Key == selection.SentinelKey:
			if key.Deleted {
				continue
			}
			var state selection.StoreState
			if err := json.Unmarshal(key.Value, &state); err != nil {
				return f, unknownf(err, "%s's %s", f.where, selection.SentinelKey)
			}
			f.state = &state
		case selection.CheckKey(key.Key) != nil && selection.CheckDurableKey(key.Key) != nil:
			// Another system key (hold.barrier.<hold id>), which no record is.
		case key.Deleted:
			f.deleted = append(f.deleted, key.Key)
		default:
			var record selection.Record
			if err := json.Unmarshal(key.Value, &record); err != nil {
				return f, unknownf(err, "%s's record %s", f.where, key.Key)
			}
			f.records[key.Key] = record
		}
	}
	f.present = true
	return f, nil
}

// statusPGFamilies are the families whose store is a Postgres intake_consumers table: Dispatch,
// and each daemon that --daemon-database, the broker's cutover rows or a consumer on the stream
// names, in order.
func statusPGFamilies(o options, cutovers []holdrecord.CutoverRow, names []string) []string {
	daemons := map[string]bool{}
	for _, project := range o.daemonProjects() {
		daemons[daemonFamily(project)] = true
	}
	for _, c := range cutovers {
		if strings.HasPrefix(c.Family, familyDaemonPrefix) {
			daemons[c.Family] = true
		}
	}
	for _, name := range names {
		if m, ok := classify(selection.LogicalOf(name)); ok && m.Project != "" {
			daemons[m.Family] = true
		}
	}
	families := []string{familyDispatch}
	for family := range daemons {
		families = append(families, family)
	}
	slices.Sort(families[1:])
	return families
}

// statusPGFamily reads family's intake_consumers table: its sentinel row and every record
// (selection.PGStore.List). A family whose store no flag names is an error, which status reports
// as UNKNOWN; a missing table is a missing store.
func (r *run) statusPGFamily(ctx context.Context, family string) (statusFamily, error) {
	f := statusFamily{family: family, where: selection.IntakeConsumersTable, records: map[string]selection.Record{}}
	named := r.o.dispatchDatabaseFile != ""
	flagName := "--dispatch-database-file"
	if project, ok := strings.CutPrefix(family, familyDaemonPrefix); ok {
		_, named = r.o.daemonDatabase(project)
		flagName = "--daemon-database " + project + "=<file>"
	}
	if !named {
		return f, fmt.Errorf("%s's store: no %s names it", family, flagName)
	}
	db, err := r.database(ctx, family)
	if err != nil {
		return f, fmt.Errorf("%s's store: %w", family, err)
	}
	store := selection.NewPGStore(db)
	state, err := store.State(ctx)
	switch {
	case errors.Is(err, selection.ErrNoStore):
		// The table is missing: a store status reports as missing.
		return f, nil
	case errors.Is(err, selection.ErrNoSentinel):
	case err != nil:
		return f, fmt.Errorf("%s's sentinel: %w", family, err)
	default:
		f.state = &state
	}
	records, err := store.List(ctx)
	if err != nil {
		return f, fmt.Errorf("%s's records: %w", family, err)
	}
	f.records = records
	f.present = true
	return f, nil
}

// statusPrintFamily prints f's store and records, and answers its blockers.
func statusPrintFamily(out io.Writer, f statusFamily, held map[string]bool) []string {
	var blockers []string
	switch {
	case !f.present:
		fmt.Fprintf(out, "store %s: missing\n", f.family)
		return append(blockers, fmt.Sprintf("%s: its store is missing", f.family))
	case f.state == nil:
		fmt.Fprintf(out, "store %s (%s): no sentinel\n", f.family, f.where)
		blockers = append(blockers, fmt.Sprintf("%s: its store has no sentinel", f.family))
	default:
		s := f.state
		fmt.Fprintf(out, "store %s (%s): cutover=%s boundary=%d replay_from=%d orphans=%v hold=%s\n", f.family,
			f.where, statusOrNone(s.Cutover), s.Boundary, s.ReplayFrom, s.Orphans, statusOrNone(s.Hold))
	}
	for _, key := range f.deleted {
		fmt.Fprintf(out, "record %s %s: deleted\n", f.family, key)
		blockers = append(blockers, fmt.Sprintf("%s %s: its record's last write is a delete marker", f.family, key))
	}
	logicals := make([]string, 0, len(f.records))
	for logical := range f.records {
		logicals = append(logicals, logical)
	}
	slices.Sort(logicals)
	for _, logical := range logicals {
		record := f.records[logical]
		fmt.Fprintf(out, "record %s %s: %s\n", f.family, logical, statusRecordLine(record, held))
		if record.Current != "" && !held[record.Current] {
			blockers = append(blockers, fmt.Sprintf("%s %s: its current %s is not on %s", f.family, logical, record.Current, notifications))
		}
		if op := record.Op; op != nil {
			blockers = append(blockers, fmt.Sprintf("%s %s: an open %s (candidate %s, old %s, owner %s, reserved %s)", f.family, logical,
				op.Kind, statusOrNone(op.Candidate), statusOrNone(op.Old), statusOrNone(op.Owner), op.ReservedAt.UTC().Format(time.RFC3339)))
		}
	}
	return blockers
}

// statusRecordLine is one record as status prints it.
func statusRecordLine(record selection.Record, held map[string]bool) string {
	on := func(name string) string {
		if held[name] {
			return name + " (on the stream)"
		}
		return name + " (absent)"
	}
	parts := []string{"current -"}
	if record.Current != "" {
		parts[0] = "current " + on(record.Current)
	}
	if op := record.Op; op != nil {
		parts = append(parts, fmt.Sprintf("op %s candidate %s old %s", op.Kind, statusOrNone(op.Candidate), statusOrNone(op.Old)))
	}
	parts = append(parts, fmt.Sprintf("start %d", record.Start))
	if lf := record.LastFloor; lf != nil {
		parts = append(parts, fmt.Sprintf("last_floor %d of %s on stream %s", lf.Floor, lf.Name, statusOrNone(lf.StreamID)))
	}
	if len(record.Retired) > 0 {
		retired := make([]string, len(record.Retired))
		for i, name := range record.Retired {
			retired[i] = on(name)
		}
		parts = append(parts, "retired ["+strings.Join(retired, ", ")+"]")
	}
	if rs := record.Restore; rs != nil {
		parts = append(parts, fmt.Sprintf("restore target %s start %d phase %s", rs.Target, rs.Start, rs.Phase))
	}
	return strings.Join(parts, ", ")
}

// statusOrNone is s, or "-" for an empty one.
func statusOrNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
