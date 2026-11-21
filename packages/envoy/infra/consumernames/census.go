package consumernames

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nats-io/nats.go"

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/infra/streams"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
)

// RecordedSessionConsumers is `envoy-infra streams census`'s reader of the selection bucket
// (writerhold.RecordedSessionConsumers), which the census calls after its barrier on the bucket:
// every key of the bucket (statusSelectionKeys), and for each record, every physical name it holds
// (its current, its open operation's candidate, and its retired names, which the census keeps
// until cleanup deletes them; censusNamesOf), each by its exact name in its family's shape.
//
//   - A session's record (a key selection.CheckKey accepts) names session consumers: pull
//     consumers acknowledged one at a time with no deliver group (bus.SessionConsumers'
//     applySettings), whose filters match each form of the session's agent subject. Their whole
//     filter list is the session's interest row's, a bucket the operator's grant does not read,
//     so the shape names only what every one of them holds (cmd/listener sessionConsumerFilter
//     puts contracts.LegacyAgentSubject first, and bus.SessionConsumerFilters gives its forms).
//   - A listener durable's record (a key selection.CheckDurableKey accepts, of a durable named
//     listener-<machine id>) names that listener's push durable, which the census compares as it
//     compares the fixed one (streams.DeclaredConsumers): every notification subject the build
//     carries, delivered to a reply inbox, acknowledged one at a time.
//
// Every other key holding a '.' is a system key (selection.SentinelKey, hold.barrier.<hold id>)
// and names nothing. A key whose last write is a delete marker is lost state, which this reader
// cannot tell from a session that never had a record, so it answers an error, and the census is
// UNKNOWN and removes nothing; so is a record that does not decode, a durable key of no listener,
// and a walk of the bucket that is not whole.
func RecordedSessionConsumers(ctx context.Context, conn *nats.Conn) ([]streams.ConsumerShape, error) {
	sessions, listeners, err := censusSelectionRecords(ctx, conn)
	if err != nil {
		return nil, err
	}
	var shapes []streams.ConsumerShape
	add := func(shape streams.ConsumerShape, record selection.Record) {
		for _, name := range censusNamesOf(record) {
			named := shape
			named.Name = name
			shapes = append(shapes, named)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(sessions)) {
		add(streams.ConsumerShape{
			Stream: notifications, AckPolicy: nats.AckExplicitPolicy,
			FilterCovers: bus.SessionConsumerFilters([]string{contracts.LegacyAgentSubject(id)}),
		}, sessions[id])
	}
	for _, durable := range slices.Sorted(maps.Keys(listeners)) {
		add(streams.ConsumerShape{
			Stream: notifications, FilterSubjects: bus.StreamSubjects(), Push: true, AckPolicy: nats.AckExplicitPolicy,
		}, listeners[durable])
	}
	return shapes, nil
}

// censusSelectionRecords reads every record of the selection bucket (statusSelectionKeys): each
// session's by its id (a key selection.CheckKey accepts), and each listener durable's by its
// durable name (a key selection.DurableKey made of a listener-<machine id>). Every other key
// holding a '.' is a system key (selection.SentinelKey, hold.barrier.<hold id>) and names nothing.
// A record whose last write is a delete marker is lost state, which cannot be told from a session
// that never had a record, and a durable's record of no listener is one no census shape takes:
// each is an error, all of them named in one; so is a record that does not decode, and a walk of
// the bucket that is not whole.
func censusSelectionRecords(ctx context.Context, conn *nats.Conn) (map[string]selection.Record, map[string]selection.Record, error) {
	keys, err := statusSelectionKeys(ctx, conn)
	if err != nil {
		return nil, nil, err
	}
	sessions, listeners := map[string]selection.Record{}, map[string]selection.Record{}
	var deleted, foreign []string
	for _, key := range keys {
		session := selection.CheckKey(key.Key) == nil
		durable, isDurable := censusDurableOf(key.Key)
		switch {
		case !session && !isDurable:
			continue
		case key.Deleted:
			deleted = append(deleted, key.Key)
			continue
		case isDurable && !strings.HasPrefix(durable, listenerFixedPrefix):
			foreign = append(foreign, key.Key)
			continue
		}
		var record selection.Record
		if err := json.Unmarshal(key.Value, &record); err != nil {
			return nil, nil, fmt.Errorf("the record %s of %s: %w", key.Key, selection.SessionBucket, err)
		}
		if session {
			sessions[key.Key] = record
		} else {
			listeners[durable] = record
		}
	}
	var problems []string
	if len(deleted) > 0 {
		problems = append(problems, fmt.Sprintf("the records %s were deleted: their consumers cannot be told from planted ones", strings.Join(deleted, ", ")))
	}
	if len(foreign) > 0 {
		problems = append(problems, fmt.Sprintf("the records %s are durables' and no listener's: the census knows no shape for them", strings.Join(foreign, ", ")))
	}
	if len(problems) > 0 {
		return nil, nil, fmt.Errorf("%s: %s", selection.SessionBucket, strings.Join(problems, "; "))
	}
	return sessions, listeners, nil
}

// The keys of RecordedNames' sets that are not cutover families: the session consumers and the
// listener durables, whose records share the selection bucket (and the cutover family session)
// but whose consumers the census compares by two different shapes.
const (
	recordedSessions  = "session"
	recordedListeners = "listener"
)

// RecordedNames are the physical names each family's selection records hold, a set per family:
// "session" (the session consumers), "listener" (every listener's durable), "dispatch"
// (Dispatch's durables) and daemon/<project key> for each daemon. A name in a set is one a record
// selects or may still select: its current, its open operation's candidate, or a retired name
// cleanup has not yet deleted (censusNamesOf). Every declared family has a set, empty when its
// store holds no record.
type RecordedNames map[string]map[string]bool

// ReadRecordedNames is the census's reader of every family's recorded names (RecordedNames), which
// the census calls after its barrier on the selection bucket. It reads the bucket for the
// session and listener sets (censusSelectionRecords) and each Postgres store for its family's set
// (selection.PGStore.List, its sentinel row aside): Dispatch's when families names dispatch or
// dispatchDatabaseFile names its store, and each daemon's that daemonDatabases names, <project>=
// <file> as consumer-names' --daemon-database takes it, every daemon family of families among
// them (censusDaemonProjects). Each file holds the store's database URL with the operator's
// credential (openDatabase). A family families names whose store no flag names, a store it cannot
// open or whose table is missing, and a bucket censusSelectionRecords refuses each fail the read,
// named, and the census is then UNKNOWN and removes nothing.
func ReadRecordedNames(ctx context.Context, conn *nats.Conn, families []string, dispatchDatabaseFile string, daemonDatabases []string) (RecordedNames, error) {
	projects, err := censusDaemonProjects(families, daemonDatabases)
	if err != nil {
		return nil, fmt.Errorf("the recorded names: %w", err)
	}
	if slices.Contains(families, familyDispatch) && dispatchDatabaseFile == "" {
		return nil, fmt.Errorf("the recorded names: %s's store: no --dispatch-database-file names it", familyDispatch)
	}
	sessions, listeners, err := censusSelectionRecords(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("the recorded names: %w", err)
	}
	names := RecordedNames{recordedSessions: {}, recordedListeners: {}}
	for _, record := range sessions {
		censusAddNames(names[recordedSessions], record)
	}
	for _, record := range listeners {
		censusAddNames(names[recordedListeners], record)
	}
	var stores []string
	if dispatchDatabaseFile != "" {
		stores = append(stores, familyDispatch)
	}
	for _, project := range projects {
		stores = append(stores, daemonFamily(project))
	}
	o := options{dispatchDatabaseFile: dispatchDatabaseFile, daemonDatabases: daemonDatabases}
	var unread []string
	for _, family := range stores {
		set, err := censusStoreNames(ctx, o, family)
		if err != nil {
			unread = append(unread, fmt.Sprintf("%s's store: %v", family, err))
			continue
		}
		names[family] = set
	}
	if len(unread) > 0 {
		return nil, fmt.Errorf("the recorded names: %s", strings.Join(unread, "; "))
	}
	return names, nil
}

// RecordedFreshNames is ReadRecordedNames in the form the census's input takes
// (streams.CensusInput.RecordedFreshNames): family to the fresh names the records hold, each list
// sorted, for the listener durables, Dispatch's durables and each daemon's. The session consumers'
// names are left out: the census declares them by their records' shapes
// (RecordedSessionConsumers). A read ReadRecordedNames refuses is refused, and the census is then
// UNKNOWN and removes nothing.
func RecordedFreshNames(ctx context.Context, conn *nats.Conn, families []string, dispatchDatabaseFile string, daemonDatabases []string) (map[string][]string, error) {
	names, err := ReadRecordedNames(ctx, conn, families, dispatchDatabaseFile, daemonDatabases)
	if err != nil {
		return nil, err
	}
	fresh := make(map[string][]string, len(names))
	for family, set := range names {
		if family == recordedSessions {
			continue
		}
		list := make([]string, 0, len(set))
		for name := range set {
			if strings.Contains(name, "~") {
				list = append(list, name)
			}
		}
		slices.Sort(list)
		fresh[family] = list
	}
	return fresh, nil
}

// censusStoreNames is every name family's intake_consumers records hold, read through the
// store's list (selection.PGStore.List, which leaves its sentinel row out).
func censusStoreNames(ctx context.Context, o options, family string) (map[string]bool, error) {
	db, err := openDatabase(ctx, o, family)
	if err != nil {
		return nil, err
	}
	if pool, ok := db.(interface{ Close() }); ok {
		defer pool.Close()
	}
	records, err := selection.NewPGStore(db).List(ctx)
	if errors.Is(err, selection.ErrNoStore) {
		return nil, fmt.Errorf("it has no %s table", selection.IntakeConsumersTable)
	}
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, record := range records {
		censusAddNames(set, record)
	}
	return set, nil
}

// censusAddNames adds every name record holds (censusNamesOf) to set.
func censusAddNames(set map[string]bool, record selection.Record) {
	for _, name := range censusNamesOf(record) {
		set[name] = true
	}
}

// censusDaemonProjects are the project keys of the daemon stores a census reads, sorted: one for
// each daemonDatabases entry, <project>=<file> as consumer-names' --daemon-database takes it; and
// each daemon family of families (daemon/<project key>) must be one of them, since a declared
// daemon whose store it could not read would read as one with nothing recorded.
func censusDaemonProjects(families, daemonDatabases []string) ([]string, error) {
	var projects []string
	for _, entry := range daemonDatabases {
		project, file, ok := strings.Cut(entry, "=")
		if !ok || project == "" || file == "" {
			return nil, fmt.Errorf("--daemon-database %q is not <project>=<file>", entry)
		}
		if slices.Contains(projects, project) {
			return nil, fmt.Errorf("--daemon-database names project %s twice", project)
		}
		projects = append(projects, project)
	}
	slices.Sort(projects)
	var unnamed []string
	for _, family := range families {
		if project, ok := strings.CutPrefix(family, familyDaemonPrefix); ok && !slices.Contains(projects, project) {
			unnamed = append(unnamed, fmt.Sprintf("%s's store: no --daemon-database %s=<file> names it", family, project))
		}
	}
	if len(unnamed) > 0 {
		return nil, errors.New(strings.Join(unnamed, "; "))
	}
	return projects, nil
}

// censusNamesOf is every physical name record holds that the census keeps, once each: its
// current, its open operation's candidate, and its retired names. An open operation's predecessor
// (Op.Old) is not among them: a replace or delete the protocol reserves names the current there
// (selection.Names.Replace, Delete), which a publish retires in the same write (names.go publish),
// and convert's names the fixed name, which the declared set names by its own rule.
func censusNamesOf(record selection.Record) []string {
	names := []string{record.Current}
	if record.Op != nil {
		names = append(names, record.Op.Candidate)
	}
	names = append(names, record.Retired...)
	names = slices.DeleteFunc(names, func(name string) bool { return name == "" })
	slices.Sort(names)
	return slices.Compact(names)
}

// censusDurableOf is the durable name a selection.DurableKey key was made from, its '=' and two
// hex digits read back to their byte, and false for a key DurableKey did not make.
func censusDurableOf(key string) (string, bool) {
	if selection.CheckDurableKey(key) != nil {
		return "", false
	}
	escaped := strings.TrimPrefix(key, "durable.")
	var name strings.Builder
	// The loop steps past each escape's two digits, so it keeps its own index.
	for i := 0; i < len(escaped); i++ {
		if escaped[i] != '=' {
			name.WriteByte(escaped[i])
			continue
		}
		if i+3 > len(escaped) {
			return "", false
		}
		b, err := strconv.ParseUint(escaped[i+1:i+3], 16, 8)
		if err != nil {
			return "", false
		}
		name.WriteByte(byte(b))
		i += 2
	}
	// DurableKey writes each escape in one form, so a key read back that it would not make again
	// (lowercase digits, an escaped byte it writes plain) is no key it made.
	if selection.DurableKey(name.String()) != key {
		return "", false
	}
	return name.String(), true
}

// censusMaxConflicts is how many refused writes of one sentinel ReconcileDaemonStreamIDs reads
// again past before it stops.
const censusMaxConflicts = 10

// censusUndefinedTable is Postgres's SQLSTATE for a relation that does not exist: a store whose
// table no migration made.
const censusUndefinedTable = "42P01"

// censusDaemonSentinel is one daemon store's sentinel row as ReconcileDaemonStreamIDs read it.
type censusDaemonSentinel struct {
	family   string
	db       Database
	state    selection.StoreState
	revision uint64
	// found is false for a store whose table holds no sentinel row.
	found bool
}

// ReconcileDaemonStreamIDs is the census's writer of the daemon stores' stream identity
// (selection.StoreState.StreamID). A Legion daemon reads no stream info and tags each last floor
// with its store's recorded identity, so after a hold that can recreate ENVOY_NOTIFICATIONS a
// floor read on the old stream must stop counting on the new one. It reads the stream's identity
// on conn (selection.StreamIdentity of STREAM.INFO, a read every phase of the operator's grant
// names), then every daemon store: one for each daemonDatabases entry, <project>=<file> as
// consumer-names' --daemon-database takes it, the file holding the store's database URL with the
// operator's credential (openDatabase); and each daemon family of families (daemon/<project
// key>) must be one of them. It reads every store's sentinel row before it writes any, then
// rewrites each sentinel whose stream_id differs from the stream's identity, every other field
// kept, with selection.PGStore.WriteSentinel at the revision it read; a refused write
// (selection.ErrConflict) reads the row again. It answers one line for each store it changed, in
// the order of their project keys.
//
// A store whose table holds no sentinel row yet (one enrol has not written) records no identity
// and is left as it is. A store it cannot read fails the call before any write, naming the store:
// a declared family no entry names, a file or database it cannot open, a missing table, and a
// sentinel that does not decode or holds a field this build does not know, which a rewrite would
// drop. A stream that carries no identity fails it too: an empty identity is never written over a
// recorded one. A failed write after others answers the lines of those it changed and the error.
func ReconcileDaemonStreamIDs(ctx context.Context, conn *nats.Conn, families, daemonDatabases []string) ([]string, error) {
	projects, err := censusDaemonProjects(families, daemonDatabases)
	if err != nil {
		return nil, fmt.Errorf("the daemon stores' stream identity: %w", err)
	}
	info, err := streamInfo(ctx, conn, notifications)
	if err != nil {
		return nil, fmt.Errorf("the daemon stores' stream identity: the info of %s: %w", notifications, err)
	}
	current := selection.StreamIdentity(info)
	if current == "" {
		return nil, fmt.Errorf("the daemon stores' stream identity: %s carries no %s, so there is no identity to record", notifications, selection.StreamIdentityMetadataKey)
	}
	o := options{daemonDatabases: daemonDatabases}
	var unread []string
	var read []censusDaemonSentinel
	for _, project := range projects {
		family := daemonFamily(project)
		db, err := openDatabase(ctx, o, family)
		if err != nil {
			unread = append(unread, fmt.Sprintf("%s's store: %v", family, err))
			continue
		}
		if pool, ok := db.(interface{ Close() }); ok {
			defer pool.Close()
		}
		s := censusDaemonSentinel{family: family, db: db}
		if s.state, s.revision, s.found, err = censusReadSentinel(ctx, db); err != nil {
			unread = append(unread, fmt.Sprintf("%s's store: %v", family, err))
			continue
		}
		read = append(read, s)
	}
	if len(unread) > 0 {
		return nil, fmt.Errorf("the daemon stores' stream identity, nothing written: %s", strings.Join(unread, "; "))
	}
	var changed []string
	for _, s := range read {
		if !s.found || s.state.StreamID == current {
			continue
		}
		line, err := censusRewriteStreamID(ctx, s, current)
		if err != nil {
			return changed, fmt.Errorf("%s's sentinel: %w", s.family, err)
		}
		if line != "" {
			changed = append(changed, line)
		}
	}
	return changed, nil
}

// censusReadSentinel reads the sentinel row of the intake_consumers table on db with its
// revision, which selection.PGStore.State does not answer and the conditional write needs, and
// reports found false for a table that holds none. The row decodes as this build's
// selection.StoreState, refusing a field it does not know.
func censusReadSentinel(ctx context.Context, db Database) (selection.StoreState, uint64, bool, error) {
	var raw []byte
	var revision int64
	err := db.QueryRow(ctx, `select record, revision from `+selection.IntakeConsumersTable+` where logical = $1`, selection.PGSentinelKey).
		Scan(&raw, &revision)
	var pg *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return selection.StoreState{}, 0, false, nil
	case errors.As(err, &pg) && pg.Code == censusUndefinedTable:
		return selection.StoreState{}, 0, false, fmt.Errorf("it has no %s table", selection.IntakeConsumersTable)
	case err != nil:
		return selection.StoreState{}, 0, false, fmt.Errorf("read its %s row: %w", selection.PGSentinelKey, err)
	}
	var state selection.StoreState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return selection.StoreState{}, 0, false, fmt.Errorf("its %s row is no sentinel this build reads whole, and a rewrite would drop what it does not know: %w", selection.PGSentinelKey, err)
	}
	return state, uint64(revision), true, nil
}

// censusRewriteStreamID writes s's sentinel with stream_id current over the revision s read, and
// answers the line naming the change. A refused write reads the row again and writes over the
// revision read then, unless the row now records current, which answers no line.
func censusRewriteStreamID(ctx context.Context, s censusDaemonSentinel, current string) (string, error) {
	store := selection.NewPGStore(s.db)
	state, revision := s.state, s.revision
	for range censusMaxConflicts {
		next := state
		next.StreamID = current
		_, err := store.WriteSentinel(ctx, next, revision)
		if err == nil {
			return fmt.Sprintf("%s: stream_id %q -> %q", s.family, state.StreamID, current), nil
		}
		if !errors.Is(err, selection.ErrConflict) {
			return "", err
		}
		var found bool
		if state, revision, found, err = censusReadSentinel(ctx, s.db); err != nil {
			return "", err
		}
		if !found || state.StreamID == current {
			return "", nil
		}
	}
	return "", fmt.Errorf("it changed under %d writes: %w", censusMaxConflicts, selection.ErrConflict)
}
