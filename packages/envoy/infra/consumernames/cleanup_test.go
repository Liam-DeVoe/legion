package consumernames

import (
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/holdrecord"
)

// cleanupRetired reads logical's retired names in store.
func cleanupRetired(t *testing.T, store selection.Store, logical string) []string {
	t.Helper()
	record, _, err := store.Read(t.Context(), logical)
	require.NoError(t, err)
	return record.Retired
}

// cleanupPull creates, as the operator, a pull consumer named name on the notification stream.
func cleanupPull(t *testing.T, nc *nats.Conn, name string) {
	t.Helper()
	_, err := writeConsumer(t.Context(), nc, notifications, jetstream.ConsumerConfig{Durable: name, AckPolicy: jetstream.AckExplicitPolicy,
		FilterSubject: "notifications.agent.cleanup." + name[len(name)-3:]}, actionCreateConsumer)
	require.NoError(t, err, "create %s", name)
}

// cleanup, with the row held, deletes the retired names of every family that the server holds:
// a session's, the listener durable's, and a daemon's, whose grant deletes no consumer; and drops
// from each record every retired name the server reports absent, a deleted one included, so
// Dispatch's absent retired name is dropped with no delete. It never deletes a record's current,
// even one its retired list names, never a consumer no record retired (an unrecorded
// session~<nuid> stays), and writes no journal entry, since no workflow names it. A second run
// deletes nothing. Control: a run with no flag naming the store of a daemon the broker's cutover
// rows name is refused before it deletes anything.
func TestTheCleanupDeletesOnlyRecordedRetiredNames(t *testing.T) {
	h := newStack(t)
	ctx := t.Context()
	id := h.heldHold(holdrecord.WorkflowStreamsCensus)
	nc, js := h.operator()
	h.notificationStream(js)
	_, err := selection.CreateSessionBucket(ctx, nc, 1, selection.StoreState{Cutover: selection.CutoverDone, Boundary: 1})
	require.NoError(t, err)
	kv := selection.NewKVStore(selection.SessionBucket, selection.ConnRequester{Conn: nc})
	dispatch := selection.NewPGStore(h.database(familyDispatch, dispatchMigrations...))
	daemon := selection.NewPGStore(h.database(daemonFamily("PROJ"), daemonMigrations...))
	write := func(store selection.Store, logical string, record selection.Record) {
		t.Helper()
		_, err := store.Write(ctx, logical, record, 0)
		require.NoError(t, err, "write %s", logical)
	}
	const (
		sessionCurrent  = "session~cleanupSessionCurrentAAA"
		sessionRetired  = "session~cleanupSessionRetiredBBB"
		sessionAbsent   = "session~cleanupSessionAbsentCCC"
		selfRetired     = "session~cleanupSelfRetiredDDDDD"
		unrecorded      = "session~cleanupUnrecordedEEEEEE"
		listenerCurrent = "listener-hub~cleanupListenerFFF"
		listenerOld     = "listener-hub~cleanupListenerGGG"
		daemonCurrent   = "legion-go-PROJ-github~cleanupHHH"
		daemonRetired   = "legion-go-PROJ-github~cleanupIII"
		dispatchCurrent = "delivery-events~cleanupDispaJJJ"
		dispatchAbsent  = "delivery-events~cleanupDispaKKK"
	)
	for _, name := range []string{sessionCurrent, sessionRetired, selfRetired, unrecorded, listenerCurrent, listenerOld, daemonCurrent, daemonRetired, dispatchCurrent} {
		cleanupPull(t, nc, name)
	}
	write(kv, "ses_a", selection.Record{Current: sessionCurrent, Start: 2, Retired: []string{sessionRetired, sessionAbsent}})
	write(kv, "ses_b", selection.Record{Current: selfRetired, Start: 2, Retired: []string{selfRetired}})
	listenerKey := selection.DurableKey("listener-hub")
	write(kv, listenerKey, selection.Record{Current: listenerCurrent, Start: 2, Retired: []string{listenerOld}})
	write(daemon, "legion-go-PROJ-github", selection.Record{Current: daemonCurrent, Start: 2, Retired: []string{daemonRetired}})
	write(dispatch, "delivery-events", selection.Record{Current: dispatchCurrent, Start: 2, Retired: []string{dispatchAbsent}})
	for _, family := range []string{familySession, familyDispatch, daemonFamily("PROJ")} {
		_, err := h.records().PutCutover(ctx, family, selection.CutoverDone, id)
		require.NoError(t, err)
	}
	before := len(h.journal(id).Ops)

	code, _, stderr := h.run(actionCleanup, "--hold", id, "--dispatch-database-file", "dispatch.url")
	require.Equal(t, 1, code, "control: no flag names daemon/PROJ's store")
	require.Contains(t, stderr, "no flag names the store of daemon/PROJ")
	// h.run closes the row's operator connections before a held command (closeOperators), so the
	// row connects again after each.
	nc, _ = h.operator()
	require.Contains(t, censusConsumers(t, nc), sessionRetired, "a refused cleanup deleted a name")

	code, stdout, stderr := h.run(actionCleanup, "--hold", id, "--dispatch-database-file", "dispatch.url", "--daemon-database", "PROJ=proj.url")
	require.Equal(t, 0, code, "%s", stderr)
	require.Contains(t, stdout, "cleanup: 5 records with retired names, 3 names deleted, 5 dropped from retired")
	nc, _ = h.operator()
	kv = selection.NewKVStore(selection.SessionBucket, selection.ConnRequester{Conn: nc})
	left := censusConsumers(t, nc)
	for _, name := range []string{sessionCurrent, selfRetired, unrecorded, listenerCurrent, daemonCurrent, dispatchCurrent} {
		require.Contains(t, left, name, "cleanup deleted %s", name)
	}
	for _, name := range []string{sessionRetired, listenerOld, daemonRetired} {
		require.NotContains(t, left, name, "cleanup left the retired %s", name)
	}
	require.Empty(t, cleanupRetired(t, kv, "ses_a"))
	require.Equal(t, []string{selfRetired}, cleanupRetired(t, kv, "ses_b"), "a retired name the record selects")
	require.Empty(t, cleanupRetired(t, kv, listenerKey))
	require.Empty(t, cleanupRetired(t, daemon, "legion-go-PROJ-github"))
	require.Empty(t, cleanupRetired(t, dispatch, "delivery-events"))
	require.Len(t, h.journal(id).Ops, before, "cleanup wrote a journal entry")

	code, stdout, stderr = h.run(actionCleanup, "--hold", id, "--dispatch-database-file", "dispatch.url", "--daemon-database", "PROJ=proj.url")
	require.Equal(t, 0, code, "%s", stderr)
	require.Contains(t, stdout, "cleanup: 1 records with retired names, 0 names deleted, 0 dropped from retired")
}
