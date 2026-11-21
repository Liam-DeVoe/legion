package consumernames

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/infra/writerhold"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testpg/testdb"
	"github.com/sjawhar/envoy/natscred"
	"github.com/sjawhar/envoy/natsgrant"
)

// censusReaders are the census's readers and writer as cmd/envoy-infra passes them to
// writerhold.StreamsCommand: this package's reader of the recorded session consumers
// (RecordedSessionConsumers), of the recorded fresh names (RecordedFreshNames) and its writer of
// the daemon stores' stream ids (ReconcileDaemonStreamIDs).
func censusReaders() writerhold.CensusReaders {
	return writerhold.CensusReaders{Recorded: RecordedSessionConsumers, FreshNames: RecordedFreshNames, Stores: ReconcileDaemonStreamIDs}
}

// censusCommand runs `envoy-infra streams census --hold holdID` in the stack with this package's
// readers (censusReaders) and extra, the census's own flags: --family, --listener-machine-id,
// --daemon-consumer, --dispatch-database-file and --daemon-database. It returns the exit code and
// the output.
func (h *stack) censusCommand(holdID string, extra ...string) (int, string, string) {
	h.t.Helper()
	return h.censusCommandWith(censusReaders(), holdID, extra...)
}

// censusCommandWith is censusCommand with readers as the census's readers and writer; a nil one
// makes the census UNKNOWN (writerhold.CensusReaders).
//
// It is t-10's word itself, writerhold.StreamsCommandOn on the stack's hold routes and hub
// (streamsDeps), with the hold's flags, whose values it does not read. The word runs inside its
// hold (writerhold.RunHeld): it writes its intent, then its done entry on a clean run or its stop
// entry otherwise, and its held re-read and postcheck admit no connection to the hub but its own
// and the auth service's, so this first closes every connection operator opened
// (closeOperators), as run does: a row connects again after it.
func (h *stack) censusCommandWith(readers writerhold.CensusReaders, holdID string, extra ...string) (int, string, string) {
	h.t.Helper()
	h.closeOperators()
	args := append([]string{"census", "--hold", holdID, "--reason", "the row's streams census"}, extra...)
	args = append(append(args, commonArgs...), writingArgs...)
	var stdout, stderr bytes.Buffer
	code := writerhold.StreamsCommandOn(context.Background(), args, &stdout, &stderr, h.streamsDeps(readers))
	h.t.Logf("envoy-infra streams census %s: exit %d\n%s%s", strings.Join(extra, " "), code, stdout.String(), stderr.String())
	return code, stdout.String(), stderr.String()
}

// censusStreamID is the notification stream's identity in the census rows
// (censusNotificationStream).
const censusStreamID = "the-census-notification-stream"

// censusNotificationStream creates ENVOY_NOTIFICATIONS on js with the build's configuration at one
// copy, as notificationStream does, and with the stream's identity
// (selection.StreamIdentityMetadataKey), as the listener writes it when it creates the stream: the
// census's writer of the daemon stores' stream ids (ReconcileDaemonStreamIDs), which the word runs
// after a clean census, refuses a stream that carries none.
func censusNotificationStream(t *testing.T, js jetstream.JetStream) {
	t.Helper()
	raw, err := json.Marshal(bus.NotificationStreamConfig(1, 0))
	require.NoError(t, err)
	var config jetstream.StreamConfig
	require.NoError(t, json.Unmarshal(raw, &config))
	config.Metadata = map[string]string{selection.StreamIdentityMetadataKey: censusStreamID}
	_, err = js.CreateStream(t.Context(), config)
	require.NoError(t, err)
}

// censusDatabaseFile writes the URL of the store on pool, a database the stack made (database),
// to a file of the test's, as the census's --dispatch-database-file and --daemon-database name one:
// the census's readers open each store from its file (openDatabase), where the stack's commands
// take the pool itself.
func censusDatabaseFile(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "store.url")
	require.NoError(t, os.WriteFile(file, []byte(pool.Config().ConnString()+"\n"), 0o600))
	return file
}

// censusSessionConsumer creates, as the operator, session sessionID's consumer name on the
// notification stream as the listener's fresh mode creates it (bus.SessionConsumers freshCreate
// and applySettings): a pull consumer acknowledged one at a time, filtered on filters, carrying
// the session in its metadata.
func censusSessionConsumer(t *testing.T, nc *nats.Conn, sessionID, name string, filters []string) {
	t.Helper()
	_, err := writeConsumer(t.Context(), nc, notifications, jetstream.ConsumerConfig{
		Durable: name, AckPolicy: jetstream.AckExplicitPolicy, MaxDeliver: -1, MaxAckPending: bus.SessionConsumerMaxAckPending,
		AckWait: bus.SessionConsumerAckWait, InactiveThreshold: time.Hour, FilterSubjects: filters,
		Metadata: map[string]string{selection.LogicalMetadataKey: sessionID, bus.SessionIDMetadataKey: sessionID},
	}, actionCreateConsumer)
	require.NoError(t, err, "create %s", name)
}

// censusListenerDurable creates, as the operator, a listener's push durable name as
// cmd/listener's fresh mode creates it (listener_durable_names.go, applyListenerConsumerPolicy):
// every notification subject the build carries, delivered to a fresh reply inbox, acknowledged
// one at a time.
func censusListenerDurable(t *testing.T, nc *nats.Conn, durable, name string) {
	t.Helper()
	_, err := writeConsumer(t.Context(), nc, notifications, jetstream.ConsumerConfig{
		Durable: name, DeliverSubject: nats.NewInbox(), FilterSubjects: bus.StreamSubjects(), AckPolicy: jetstream.AckExplicitPolicy,
		Metadata: map[string]string{selection.LogicalMetadataKey: durable},
	}, actionCreateConsumer)
	require.NoError(t, err, "create %s", name)
}

// censusConsumers are the notification stream's consumer names.
func censusConsumers(t *testing.T, nc *nats.Conn) []string {
	t.Helper()
	names, err := consumerNames(t.Context(), nc, notifications)
	require.NoError(t, err)
	return names
}

// The streams census command (t-10's word, with this package's readers), given no reader of the
// recorded consumers, exits 2 printing UNKNOWN, removes nothing, and writes its stop entry,
// recording that it left everything unchanged. With this package's reader it runs complete, in a
// held hold whose session family's cutover row is done, over a bucket holding three sessions'
// records: it keeps each recorded session~<nuid> consumer, whose filter holds its session's agent
// subject and a topic beside it, a retired one that cleanup has not yet deleted, the fresh
// listener durable that the listener's durable.<escaped durable> record names, and the fresh
// listener durable of a machine no --listener-machine-id names, which its record alone names (the
// recorded fresh names keep a listener-<m>~<nuid> only through a declared listener-<m>, so
// RecordedSessionConsumers' listener shapes keep this one). It removes a stray listener-<m>~<nuid>
// that no record names and an unrecorded session~<nuid> of a recorded session, in that session's
// own shape, exits 1 and writes its stop entry naming each removal. Control: a recorded
// session~<nuid> whose filter does not hold its session's agent subject is removed, so a recorded
// name is compared by its shape and never by name alone. A second run is clean, and its done entry
// completes the hold's record, the census being the streams-census workflow's final command.
func TestTheCensusReadsTheRecordedSessionConsumers(t *testing.T) {
	h := newStack(t)
	ctx := t.Context()
	id := h.heldHold(holdrecord.WorkflowStreamsCensus)
	nc, js := h.operator()
	censusNotificationStream(t, js)
	_, err := selection.CreateSessionBucket(ctx, nc, 1, selection.StoreState{Cutover: selection.CutoverDone, Boundary: 1})
	require.NoError(t, err)
	store := selection.NewKVStore(selection.SessionBucket, selection.ConnRequester{Conn: nc})

	agent := func(sessionID string) []string {
		return bus.SessionConsumerFilters([]string{contracts.LegacyAgentSubject(sessionID), "notifications.github.pr.owner.repo.7"})
	}
	kept := map[string]string{}
	for _, sessionID := range []string{"ses_a", "ses_b", "ses_c"} {
		name := selection.FreshName(sessionBucketPrefix)
		censusSessionConsumer(t, nc, sessionID, name, agent(sessionID))
		record := selection.Record{Current: name, Start: 1}
		if sessionID == "ses_a" {
			retired := selection.FreshName(sessionBucketPrefix)
			censusSessionConsumer(t, nc, sessionID, retired, agent(sessionID))
			record.Retired = []string{retired}
			kept[retired] = "the retired name of ses_a, which cleanup deletes and the census keeps until then"
		}
		_, err := store.Write(ctx, sessionID, record, 0)
		require.NoError(t, err)
		kept[name] = "the current of " + sessionID
	}
	durable := bus.ListenerDurable("hub")
	listener := selection.FreshName(durable)
	censusListenerDurable(t, nc, durable, listener)
	_, err = store.Write(ctx, selection.DurableKey(durable), selection.Record{Current: listener, Start: 1}, 0)
	require.NoError(t, err)
	kept[listener] = "the listener durable its record names"
	undeclared := bus.ListenerDurable("undeclared")
	undeclaredListener := selection.FreshName(undeclared)
	censusListenerDurable(t, nc, undeclared, undeclaredListener)
	_, err = store.Write(ctx, selection.DurableKey(undeclared), selection.Record{Current: undeclaredListener, Start: 1}, 0)
	require.NoError(t, err)
	kept[undeclaredListener] = "the listener durable of a machine no --listener-machine-id names, which its record names"
	stray := selection.FreshName(durable)
	censusListenerDurable(t, nc, durable, stray)
	unrecordedSession := selection.FreshName(sessionBucketPrefix)
	censusSessionConsumer(t, nc, "ses_b", unrecordedSession, agent("ses_b"))
	planted := selection.FreshName(sessionBucketPrefix)
	censusSessionConsumer(t, nc, "ses_d", planted, []string{"notifications.dispatch.issue.PRIV.>"})
	_, err = store.Write(ctx, "ses_d", selection.Record{Current: planted, Start: 1}, 0)
	require.NoError(t, err)
	_, err = h.records().PutCutover(ctx, familySession, selection.CutoverDone, id)
	require.NoError(t, err)

	flags := []string{"--family", familySession, "--listener-machine-id", "hub"}
	noRecorded := censusReaders()
	noRecorded.Recorded = nil
	code, stdout, stderr := h.censusCommandWith(noRecorded, id, flags...)
	require.Equal(t, 2, code, "the census with no reader")
	require.Contains(t, stdout, "UNKNOWN")
	require.Contains(t, stderr, writerhold.ErrNoRecordReader.Error())
	last := h.enrollLast(id)
	require.Equal(t, writerhold.CommandStreamsCensus+" stop", last.Command+" "+last.State, "the census with no reader ends in its stop entry")
	require.Equal(t, holdrecord.LeftUnchanged, last.Outputs["left"])
	nc, _ = h.operator()
	require.Contains(t, censusConsumers(t, nc), stray, "a census with no reader removed a consumer")

	code, stdout, _ = h.censusCommand(id, flags...)
	require.Equal(t, 1, code, "the census with the reader is complete and removes the stray: %s", stdout)
	require.NotContains(t, stdout, "UNKNOWN")
	require.Contains(t, stdout, "CHANGED "+notifications+" consumer "+stray+": removed")
	require.Contains(t, stdout, "CHANGED "+notifications+" consumer "+planted+": removed", "control: the recorded name with another filter")
	require.Contains(t, stdout, "CHANGED "+notifications+" consumer "+unrecordedSession+": removed", "the unrecorded session consumer")
	last = h.enrollLast(id)
	require.Equal(t, writerhold.CommandStreamsCensus+" stop", last.Command+" "+last.State, "the census that removed consumers ends in its stop entry")
	for _, removed := range []string{stray, planted, unrecordedSession} {
		require.Contains(t, last.Outputs["left"], removed, "the stop entry names each removal")
	}
	nc, _ = h.operator()
	left := censusConsumers(t, nc)
	for name, what := range kept {
		require.Contains(t, left, name, "the census removed %s", what)
	}
	require.NotContains(t, left, stray)
	require.NotContains(t, left, planted)
	require.NotContains(t, left, unrecordedSession)

	code, stdout, _ = h.censusCommand(id, flags...)
	require.Equal(t, 0, code, "the second census: %s", stdout)
	require.True(t, slices.Contains(strings.Split(strings.TrimSpace(stdout), "\n"), "CLEAN"), stdout)
	require.Equal(t, writerhold.CommandStreamsCensus+" done", h.enrollLast(id).Command+" "+h.enrollLast(id).State)
	require.True(t, h.journal(id).Complete, "the clean census's done entry completes the streams-census hold's record")
}

// sessionBucketPrefix is every fresh session consumer's prefix (bus.SessionConsumerNamePrefix).
const sessionBucketPrefix = bus.SessionConsumerNamePrefix

// The census reads a listener durable's record key back to the durable it was made from
// (selection.DurableKey), escapes included, and takes no other key for a durable's: a session id,
// the sentinel, a barrier key, and a key DurableKey would not write (lowercase hex, a byte it
// writes plain written escaped, a cut escape).
func TestTheCensusReadsADurableKeyBack(t *testing.T) {
	for _, durable := range []string{"listener-hub", "listener-a.b", "listener-a b", "listener-ünï~x"} {
		got, ok := censusDurableOf(selection.DurableKey(durable))
		require.True(t, ok, "the key of %q", durable)
		require.Equal(t, durable, got)
	}
	for _, key := range []string{"ses_a", selection.SentinelKey, "hold.barrier.h1", "durable.listener-a=2eb", "durable.listener=2Dhub",
		"durable.listener-a=2", "durable."} {
		_, ok := censusDurableOf(key)
		require.False(t, ok, "%q read as a durable's key", key)
	}
}

// censusDaemonStore makes daemon project's store a database of the test's own (censusStore) and
// answers the --daemon-database value naming it, <project>=<file>, and the store's pool.
func censusDaemonStore(t *testing.T, project string, migrations ...string) (string, *pgxpool.Pool) {
	t.Helper()
	file, pool := censusStore(t, project, migrations...)
	return project + "=" + file, pool
}

// censusStore makes a family's store a database of the test's own on the test's Postgres server,
// migrated by each of migrations (none: a store with no table), writes its URL to a file as the
// operator's --dispatch-database-file and --daemon-database name one, and answers the file and
// the store's pool. It skips the test when BROKER_TEST_DATABASE_URL is unset, as the stack does.
func censusStore(t *testing.T, name string, migrations ...string) (string, *pgxpool.Pool) {
	t.Helper()
	admin := os.Getenv("BROKER_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("BROKER_TEST_DATABASE_URL is unset: the family stores are Postgres databases")
	}
	url := testdb.DatabaseOn(t, admin, "cnames")
	pool, err := pgxpool.New(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	for _, file := range migrations {
		sql, err := os.ReadFile(filepath.FromSlash(file))
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), string(sql))
		require.NoError(t, err, "migrate %s's store with %s", name, file)
	}
	file := filepath.Join(t.TempDir(), name+".url")
	require.NoError(t, os.WriteFile(file, []byte(url+"\n"), 0o600))
	return file, pool
}

// censusSentinel reads the sentinel row of the store on pool: its state and its revision.
func censusSentinel(t *testing.T, pool *pgxpool.Pool) (selection.StoreState, uint64) {
	t.Helper()
	var revision int64
	require.NoError(t, pool.QueryRow(t.Context(), `select revision from `+selection.IntakeConsumersTable+` where logical = $1`,
		selection.PGSentinelKey).Scan(&revision))
	state, err := selection.NewPGStore(pool).State(t.Context())
	require.NoError(t, err)
	return state, uint64(revision)
}

// The census's writer of the daemon stores' stream identity (ReconcileDaemonStreamIDs), as the
// operator under exactly natsgrant.Operator(held), reads ENVOY_NOTIFICATIONS' identity and every
// daemon store the census names, and rewrites, by a write conditional on its revision, each
// sentinel whose stream_id differs from it: a stale one and one that recorded none are rewritten,
// every other field kept, and each is named; an equal one is left at its revision; a store with no
// sentinel yet (a store enrol has not written) is left without one. A second run changes nothing.
// A store it cannot read makes it fail, naming the store, and it then writes nothing: a store with
// no table, a declared daemon family whose store no --daemon-database names, and a stream that
// carries no identity. Control: in those runs the stale store is still stale afterwards, so the
// writer that fails reads before it writes.
func TestTheCensusReconcilesEachDaemonStoresStreamID(t *testing.T) {
	ctx := t.Context()
	url := statusWalkServer(t, "operator", natsgrant.Operator(natsgrant.OperatorPhaseHeld))
	admin := testnats.Connect(t, url)
	t.Cleanup(admin.Close)
	js, err := admin.JetStream()
	require.NoError(t, err)
	stream := bus.NotificationStreamConfig(1, 0)
	stream.Metadata = map[string]string{selection.StreamIdentityMetadataKey: "stream-2"}
	_, err = js.AddStream(&stream)
	require.NoError(t, err)
	conn, err := nats.Connect(url, nats.UserInfo(natscred.ServiceUser, "operator"), nats.CustomInboxPrefix(natsgrant.OperatorInbox))
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	sentinel := func(pool *pgxpool.Pool, state selection.StoreState) {
		t.Helper()
		_, err := selection.NewPGStore(pool).WriteSentinel(ctx, state, 0)
		require.NoError(t, err)
	}
	stale, staleDB := censusDaemonStore(t, "STALE", daemonMigrations...)
	sentinel(staleDB, selection.StoreState{Cutover: selection.CutoverDone, Boundary: 7, StreamID: "stream-1", Hold: "h0"})
	equal, equalDB := censusDaemonStore(t, "EQUAL", daemonMigrations...)
	sentinel(equalDB, selection.StoreState{Cutover: selection.CutoverDone, Boundary: 3, StreamID: "stream-2"})
	unrecorded, unrecordedDB := censusDaemonStore(t, "UNREC", daemonMigrations...)
	sentinel(unrecordedDB, selection.StoreState{Cutover: selection.CutoverDone, Boundary: 5})
	fresh, freshDB := censusDaemonStore(t, "FRESH", daemonMigrations...)
	families := []string{familySession, familyDispatch, daemonFamily("STALE"), daemonFamily("EQUAL")}

	changed, err := ReconcileDaemonStreamIDs(ctx, conn, families, []string{stale, equal, unrecorded, fresh})
	require.NoError(t, err)
	require.Len(t, changed, 2, "%v", changed)
	require.Contains(t, changed[0], "daemon/STALE")
	require.Contains(t, changed[0], "stream-1")
	require.Contains(t, changed[0], "stream-2")
	require.Contains(t, changed[1], "daemon/UNREC")
	state, revision := censusSentinel(t, staleDB)
	require.Equal(t, selection.StoreState{Cutover: selection.CutoverDone, Boundary: 7, StreamID: "stream-2", Hold: "h0"}, state)
	require.Equal(t, uint64(2), revision, "the stale sentinel's rewrite")
	state, revision = censusSentinel(t, equalDB)
	require.Equal(t, "stream-2", state.StreamID)
	require.Equal(t, uint64(1), revision, "the equal sentinel was written")
	state, _ = censusSentinel(t, unrecordedDB)
	require.Equal(t, selection.StoreState{Cutover: selection.CutoverDone, Boundary: 5, StreamID: "stream-2"}, state)
	_, err = selection.NewPGStore(freshDB).State(ctx)
	require.ErrorIs(t, err, selection.ErrNoSentinel, "the writer made a sentinel for a store with none")

	changed, err = ReconcileDaemonStreamIDs(ctx, conn, families, []string{stale, equal, unrecorded, fresh})
	require.NoError(t, err)
	require.Empty(t, changed, "the second run")
	_, revision = censusSentinel(t, staleDB)
	require.Equal(t, uint64(2), revision)

	_, err = selection.NewPGStore(staleDB).WriteSentinel(ctx, selection.StoreState{Cutover: selection.CutoverDone, Boundary: 7, StreamID: "stream-1", Hold: "h0"}, 2)
	require.NoError(t, err)
	stillStale := func(what string) {
		t.Helper()
		state, _ := censusSentinel(t, staleDB)
		require.Equal(t, "stream-1", state.StreamID, "control: %s wrote the stale store", what)
	}
	noTable, _ := censusDaemonStore(t, "NOTABLE")
	changed, err = ReconcileDaemonStreamIDs(ctx, conn, families, []string{stale, equal, noTable})
	require.ErrorContains(t, err, "daemon/NOTABLE")
	require.Empty(t, changed)
	stillStale("a run with a store that has no table")

	changed, err = ReconcileDaemonStreamIDs(ctx, conn, append(slices.Clone(families), daemonFamily("UNNAMED")), []string{stale, equal})
	require.ErrorContains(t, err, "daemon/UNNAMED")
	require.Empty(t, changed)
	stillStale("a run with a declared family whose store no flag names")

	_, err = ReconcileDaemonStreamIDs(ctx, conn, families, []string{stale, "EQUAL"})
	require.ErrorContains(t, err, `"EQUAL"`)
	stillStale("a run with a malformed --daemon-database")

	info, err := js.StreamInfo(bus.Stream)
	require.NoError(t, err)
	anonymous := info.Config
	anonymous.Metadata = map[string]string{}
	_, err = js.UpdateStream(&anonymous)
	require.NoError(t, err)
	changed, err = ReconcileDaemonStreamIDs(ctx, conn, families, []string{stale, equal})
	require.ErrorContains(t, err, selection.StreamIdentityMetadataKey)
	require.Empty(t, changed)
	stillStale("a run on a stream with no identity")
}

// The census's reader of every family's recorded names (ReadRecordedNames), as the operator under
// exactly natsgrant.Operator(held), answers one set per declared family, each exactly the names
// its records hold (a current, an open operation's candidate, a retired name), so an unrecorded
// <family>~<nuid> is in none: the session consumers and the listener durables from the selection
// bucket, whose sentinel and barrier key name nothing; Dispatch's from its intake_consumers, whose
// sentinel row names nothing; and each daemon's from its own, a declared daemon whose store holds
// no record answering an empty set rather than none. An open operation's predecessor is no
// recorded name: a convert's reserve whose current is empty names the fixed name there, which the
// set leaves out. A run that declares only the session family reads only the bucket. Control: a
// store it cannot read fails the run, naming it (a declared Dispatch with no
// --dispatch-database-file, a daemon store with no table, a declared daemon no --daemon-database
// names), and so does a bucket holding a deleted session record or a durable record of no
// listener, naming each.
func TestTheRecordedNamesOfEveryFamily(t *testing.T) {
	ctx := t.Context()
	url := statusWalkServer(t, "operator", natsgrant.Operator(natsgrant.OperatorPhaseHeld))
	admin := testnats.Connect(t, url)
	t.Cleanup(admin.Close)
	kv, err := selection.CreateSessionBucket(ctx, admin, 1, selection.StoreState{Cutover: selection.CutoverDone, Boundary: 1})
	require.NoError(t, err)
	statusPublishKV(t, admin, "hold.barrier.h1", []byte("h1"), "")
	conn, err := nats.Connect(url, nats.UserInfo(natscred.ServiceUser, "operator"), nats.CustomInboxPrefix(natsgrant.OperatorInbox))
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	write := func(store selection.Store, logical string, record selection.Record) {
		t.Helper()
		_, err := store.Write(ctx, logical, record, 0)
		require.NoError(t, err, "write %s", logical)
	}
	set := func(names ...string) map[string]bool {
		out := map[string]bool{}
		for _, name := range names {
			out[name] = true
		}
		return out
	}
	fresh := selection.FreshName

	s0, s1, s2 := fresh(bus.SessionConsumerNamePrefix), fresh(bus.SessionConsumerNamePrefix), fresh(bus.SessionConsumerNamePrefix)
	write(kv, "ses_a", selection.Record{Current: s1, Start: 4, Retired: []string{s0},
		Op: &selection.Op{Kind: selection.OpReplace, Candidate: s2, Old: s1}})
	s4 := fresh(bus.SessionConsumerNamePrefix)
	write(kv, "ses_e", selection.Record{Start: 4, Op: &selection.Op{Kind: selection.OpReplace, Old: sessionFixedPrefix + "ses_e", Candidate: s4}})
	l0, l1 := fresh(bus.ListenerDurable("hub")), fresh(bus.ListenerDurable("hub"))
	write(kv, selection.DurableKey(bus.ListenerDurable("hub")), selection.Record{Current: l1, Start: 4, Retired: []string{l0}})
	dispatchFile, dispatchDB := censusStore(t, familyDispatch, dispatchMigrations...)
	dispatch := selection.NewPGStore(dispatchDB)
	_, err = dispatch.WriteSentinel(ctx, selection.StoreState{Cutover: selection.CutoverDone}, 0)
	require.NoError(t, err)
	d0, d1, d2 := fresh("delivery-events"), fresh("delivery-events"), fresh("delivery-events")
	write(dispatch, "delivery-events", selection.Record{Current: d1, Start: 4, Retired: []string{d0},
		Op: &selection.Op{Kind: selection.OpReplace, Candidate: d2, Old: d1}})
	daemon, daemonDB := censusDaemonStore(t, "PROJ", daemonMigrations...)
	w0, w1 := fresh("legion-go-PROJ-wake"), fresh("legion-go-PROJ-wake")
	write(selection.NewPGStore(daemonDB), "legion-go-PROJ-wake", selection.Record{Current: w1, Start: 4, Retired: []string{w0}})
	empty, _ := censusDaemonStore(t, "EMPTY", daemonMigrations...)
	families := []string{familySession, familyDispatch, daemonFamily("PROJ"), daemonFamily("EMPTY")}

	names, err := ReadRecordedNames(ctx, conn, families, dispatchFile, []string{daemon, empty})
	require.NoError(t, err)
	require.Equal(t, RecordedNames{
		recordedSessions: set(s0, s1, s2, s4), recordedListeners: set(l0, l1), familyDispatch: set(d0, d1, d2), daemonFamily("PROJ"): set(w0, w1),
		daemonFamily("EMPTY"): set(),
	}, names)
	families = families[:3]
	names, err = ReadRecordedNames(ctx, conn, []string{familySession}, "", nil)
	require.NoError(t, err)
	require.Equal(t, RecordedNames{recordedSessions: set(s0, s1, s2, s4), recordedListeners: set(l0, l1)}, names, "a run that declares the session family alone")

	_, err = ReadRecordedNames(ctx, conn, families, "", []string{daemon})
	require.ErrorContains(t, err, "dispatch's store", "control: a declared Dispatch with no store file")
	noTable, _ := censusDaemonStore(t, "NOTABLE")
	_, err = ReadRecordedNames(ctx, conn, families, dispatchFile, []string{daemon, noTable})
	require.ErrorContains(t, err, "daemon/NOTABLE", "control: a daemon store with no table")
	_, err = ReadRecordedNames(ctx, conn, append(slices.Clone(families), daemonFamily("UNNAMED")), dispatchFile, []string{daemon})
	require.ErrorContains(t, err, "daemon/UNNAMED", "control: a declared daemon no flag names")

	write(kv, selection.DurableKey("delivery-planted"), selection.Record{Current: fresh("delivery-planted")})
	statusPublishKV(t, admin, "ses_a", nil, "DEL")
	_, err = ReadRecordedNames(ctx, conn, families, dispatchFile, []string{daemon})
	require.ErrorContains(t, err, "ses_a", "control: a deleted session record")
	require.ErrorContains(t, err, selection.DurableKey("delivery-planted"), "control: a durable record of no listener")
}

// censusCreateConfig creates, as the operator on nc, a consumer of the notification stream with a
// configuration in nats.go's older form (bus.DispatchDeliveryConsumer gives that form), through the
// one create the operator's grant names (writeConsumer).
func censusCreateConfig(t *testing.T, nc *nats.Conn, config nats.ConsumerConfig) {
	t.Helper()
	raw, err := json.Marshal(config)
	require.NoError(t, err)
	var converted jetstream.ConsumerConfig
	require.NoError(t, json.Unmarshal(raw, &converted))
	_, err = writeConsumer(t.Context(), nc, notifications, converted, actionCreateConsumer)
	require.NoError(t, err, "create %s", config.Durable)
}

// A census whose reader cannot read a declared family's store is UNKNOWN, exit 2, naming the
// family, removes nothing, and writes its stop entry, recording that it left everything unchanged:
// the census declares the session family and daemon/PROJ, and no --daemon-database names PROJ's
// store, so the recorded fresh names (RecordedFreshNames) are refused before the census lists
// anything, and the stray listener-hub~<nuid> stays. The word prints no report it did not make, so
// its standard output holds no CLEAN. Control: with the store named, the same census runs complete
// and removes the stray.
func TestTheCensusIsUnknownWhenADeclaredFamilyIsUnread(t *testing.T) {
	h := newStack(t)
	ctx := t.Context()
	id := h.heldHold(holdrecord.WorkflowStreamsCensus)
	nc, js := h.operator()
	censusNotificationStream(t, js)
	_, err := selection.CreateSessionBucket(ctx, nc, 1, selection.StoreState{Cutover: selection.CutoverDone, Boundary: 1})
	require.NoError(t, err)
	stray := selection.FreshName(bus.ListenerDurable("hub"))
	censusListenerDurable(t, nc, bus.ListenerDurable("hub"), stray)
	for _, family := range []string{familySession, daemonFamily("PROJ")} {
		_, err := h.records().PutCutover(ctx, family, selection.CutoverDone, id)
		require.NoError(t, err)
	}
	flags := []string{"--family", familySession, "--family", daemonFamily("PROJ"), "--listener-machine-id", "hub"}

	code, stdout, stderr := h.censusCommand(id, flags...)
	require.Equal(t, 2, code, "a census with a declared family unread: %s%s", stdout, stderr)
	require.Contains(t, stderr, "UNKNOWN")
	require.Contains(t, stderr, daemonFamily("PROJ"))
	require.NotContains(t, stdout, "CHANGED")
	require.NotContains(t, stdout, "CLEAN", "an UNKNOWN census printed a report it did not make")
	last := h.enrollLast(id)
	require.Equal(t, writerhold.CommandStreamsCensus+" stop", last.Command+" "+last.State, "the UNKNOWN census ends in its stop entry")
	require.Equal(t, holdrecord.LeftUnchanged, last.Outputs["left"])
	nc, _ = h.operator()
	require.Contains(t, censusConsumers(t, nc), stray, "an UNKNOWN census removed the stray")

	daemon, _ := censusDaemonStore(t, "PROJ", daemonMigrations...)
	code, stdout, _ = h.censusCommand(id, append(flags, "--daemon-database", daemon)...)
	require.Equal(t, 1, code, "control: the census with every declared family read: %s", stdout)
	require.Contains(t, stdout, "CHANGED "+notifications+" consumer "+stray+": removed")
}

// The census declares the session family, Dispatch and daemon/PROJ, each with its records:
// Dispatch's intake_consumers names delivery-events~<nuid> and PROJ's names
// legion-go-PROJ-wake~<nuid>, each a consumer in its family's own shape. The word passes the census
// the recorded fresh names (RecordedFreshNames) of every store its flags name, so the census keeps
// each recorded name and removes a stray delivery-events~<nuid> and a stray
// legion-go-PROJ-wake~<nuid> that no record names, in the same shapes, naming each removal; a
// second run is clean, and its done entry completes the hold's record. Control: the same census
// with no --dispatch-database-file cannot read a declared family's records, so it is UNKNOWN and
// removes neither stray.
func TestTheCensusRemovesAStrayDispatchAndDaemonName(t *testing.T) {
	h := newStack(t)
	ctx := t.Context()
	id := h.heldHold(holdrecord.WorkflowStreamsCensus)
	nc, js := h.operator()
	censusNotificationStream(t, js)
	_, err := selection.CreateSessionBucket(ctx, nc, 1, selection.StoreState{Cutover: selection.CutoverDone, Boundary: 1})
	require.NoError(t, err)

	events := bus.DispatchDeliveryConsumer()
	recordedD, strayD := selection.FreshName(events.Durable), selection.FreshName(events.Durable)
	for _, name := range []string{recordedD, strayD} {
		config := events
		config.Durable, config.DeliverSubject = name, nats.NewInbox()
		config.Metadata = map[string]string{selection.LogicalMetadataKey: events.Durable}
		censusCreateConfig(t, nc, config)
	}
	dispatchFile, dispatchDB := censusStore(t, familyDispatch, dispatchMigrations...)
	_, err = selection.NewPGStore(dispatchDB).Write(ctx, events.Durable, selection.Record{Current: recordedD, Start: 1}, 0)
	require.NoError(t, err)

	const wake = "legion-go-PROJ-wake"
	recordedW, strayW := selection.FreshName(wake), selection.FreshName(wake)
	for _, name := range []string{recordedW, strayW} {
		_, err := writeConsumer(ctx, nc, notifications, jetstream.ConsumerConfig{Durable: name, FilterSubject: bus.LegionWakeTopic,
			AckPolicy: jetstream.AckExplicitPolicy, Metadata: map[string]string{selection.LogicalMetadataKey: wake}}, actionCreateConsumer)
		require.NoError(t, err, "create %s", name)
	}
	daemon, daemonDB := censusDaemonStore(t, "PROJ", daemonMigrations...)
	_, err = selection.NewPGStore(daemonDB).Write(ctx, wake, selection.Record{Current: recordedW, Start: 1}, 0)
	require.NoError(t, err)

	flags := []string{"--daemon-consumer", wake + "=" + bus.LegionWakeTopic, "--daemon-database", daemon}
	for _, family := range []string{familySession, familyDispatch, daemonFamily("PROJ")} {
		_, err := h.records().PutCutover(ctx, family, selection.CutoverDone, id)
		require.NoError(t, err)
		flags = append(flags, "--family", family)
	}

	code, stdout, stderr := h.censusCommand(id, flags...)
	require.Equal(t, 2, code, "control: the census with Dispatch's records unread: %s%s", stdout, stderr)
	require.Contains(t, stderr, familyDispatch+"'s store")
	require.NotContains(t, stdout, "CHANGED")
	nc, _ = h.operator()
	left := censusConsumers(t, nc)
	require.Contains(t, left, strayD, "control: an UNKNOWN census removed the stray Dispatch name")
	require.Contains(t, left, strayW, "control: an UNKNOWN census removed the stray daemon name")

	flags = append(flags, "--dispatch-database-file", dispatchFile)
	code, stdout, _ = h.censusCommand(id, flags...)
	require.Equal(t, 1, code, "the census removes the strays: %s", stdout)
	require.Contains(t, stdout, "CHANGED "+notifications+" consumer "+strayD+": removed", "the stray Dispatch name")
	require.Contains(t, stdout, "CHANGED "+notifications+" consumer "+strayW+": removed", "the stray daemon name")
	nc, _ = h.operator()
	left = censusConsumers(t, nc)
	require.Contains(t, left, recordedD)
	require.Contains(t, left, recordedW)
	require.NotContains(t, left, strayD)
	require.NotContains(t, left, strayW)
	code, stdout, _ = h.censusCommand(id, flags...)
	require.Equal(t, 0, code, "the second census: %s", stdout)
	require.True(t, slices.Contains(strings.Split(strings.TrimSpace(stdout), "\n"), "CLEAN"), stdout)
	require.True(t, h.journal(id).Complete, "the clean census's done entry completes the streams-census hold's record")
}
