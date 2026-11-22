package consumernames

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/infra/writerhold/holdtest"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/holdrecord"
)

// replayMember maps each family's logical name, as replay --key names it, to its member: a session
// id (one that starts with s- too), a listener durable's record key (selection.DurableKey), each of
// Dispatch's durables and a Legion daemon's legion-go-<project>-<kind>. It refuses a key holding a
// '.' that is no listener's durable key, a durable key that ends inside its escape, a bare
// listener-<machine id>, and a session id selection.CheckKey refuses. Control: the bare listener
// name is a session id to classify, so the refusal is replayMember's own rule.
func TestReplayMemberMapsEachFamilysLogicalName(t *testing.T) {
	durable := bus.ListenerDurable("hub-a")
	for _, row := range []struct {
		key                      string
		family, logical, fixed   string
		prefix, session, project string
	}{
		{key: "ses_abc", family: familySession, logical: "ses_abc", fixed: "s-ses_abc", prefix: sessionFreshPrefix, session: "ses_abc"},
		{key: "s-older", family: familySession, logical: "s-older", fixed: "s-s-older", prefix: sessionFreshPrefix, session: "s-older"},
		{key: selection.DurableKey(durable), family: familySession, logical: selection.DurableKey(durable), fixed: durable, prefix: durable},
		{key: selection.DurableKey("listener-hub b"), family: familySession, logical: selection.DurableKey("listener-hub b"), fixed: "listener-hub b", prefix: "listener-hub b"},
		{key: dispatchDurables()[0], family: familyDispatch, logical: dispatchDurables()[0], fixed: dispatchDurables()[0], prefix: dispatchDurables()[0]},
		{key: "legion-go-proj-a-dispatch", family: daemonFamily("proj-a"), logical: "legion-go-proj-a-dispatch", fixed: "legion-go-proj-a-dispatch", prefix: "legion-go-proj-a-dispatch", project: "proj-a"},
	} {
		m, err := replayMember(row.key)
		require.NoError(t, err, row.key)
		require.Equal(t, member{Family: row.family, Logical: row.logical, Fixed: row.fixed, Prefix: row.prefix, SessionID: row.session, Project: row.project}, m, row.key)
	}
	for key, want := range map[string]string{
		"a.b":               "holds a '.'",
		"durable.something": "is no listener's durable",
		selection.DurableKey("listener-x") + "=4": "ends inside an escape",
		durable:                  "--key " + selection.DurableKey(durable),
		"ses with a space":       "a logical name no consumer can be named after",
		strings.Repeat("s", 254): "maximum allowed is 255",
	} {
		_, err := replayMember(key)
		require.ErrorContains(t, err, want, key)
	}
	_, ok := classify(sessionFixedPrefix + durable)
	require.True(t, ok, "control: classify reads s-%s as a session's fixed name", durable)
}

// replay, in a cutover hold after create-bucket, writes each key's record with start = SEQ and no
// current, in its family's store, with an intent and a done entry, and completes nothing: a session
// whose record held a current, an open create and retired names keeps its retired names and
// retires the current and the candidate; a key never written gets a record of its start alone; a
// key whose last message is a KV delete marker is written over the marker; a listener durable's
// key in the selection bucket; Dispatch's durable and a daemon's in their Postgres stores.
// Controls, each refused before its intent, so the journal's replay entries do not change: a
// --from past the stream's last sequence + 1, and replay --final, which the cutover's table does
// not name.
func TestReplayWritesTheKeysRecordWithItsStartAndNoCurrent(t *testing.T) {
	ctx := context.Background()
	h := newStack(t)
	createBucketNotifications(t, h, 10)
	dispatchDB := h.database(familyDispatch, dispatchMigrations...)
	daemonDB := h.database(daemonFamily("proj-a"), daemonMigrations...)
	id := h.prepared(holdrecord.WorkflowCutover)
	createBucketCohort(t, h.records(), id, 1)
	h.held(id)
	code, _, stderr := h.run("create-bucket", "--hold", id)
	require.Equal(t, 0, code, stderr)
	// The stack closes the test's operator connections before each held command, so each read
	// of the bucket after a run opens one of its own.
	bucket := func() familyStore {
		nc, _ := h.operator()
		return newSessionStore(nc)
	}
	pg := func(family string, db *pgxpool.Pool) func() familyStore {
		return func() familyStore { return pgFamilyOf(t, family, db) }
	}
	_, js := h.operator()

	current, candidate, retired := selection.FreshName(sessionFreshPrefix), selection.FreshName(sessionFreshPrefix), selection.FreshName(sessionFreshPrefix)
	_, err := bucket().Write(ctx, "ses_open", selection.Record{Current: current, Start: 3, Retired: []string{retired},
		Op:        &selection.Op{Kind: selection.OpCreate, Owner: "a crashed listener", Candidate: candidate, ReservedAt: time.Now()},
		LastFloor: &selection.Floor{Floor: 6, Name: current, StreamID: "s"}}, 0)
	require.NoError(t, err)
	marker := nats.NewMsg("$KV." + selection.SessionBucket + ".ses_deleted")
	marker.Header.Set("KV-Operation", "DEL")
	_, err = js.PublishMsg(ctx, marker)
	require.NoError(t, err)
	listenerKey := selection.DurableKey(bus.ListenerDurable(holdtest.ListenerMachines[0]))

	replays := func() int { return len(createBucketEntries(h.journal(id), replayCommand)) }
	for _, row := range []struct {
		key, from string
		store     func() familyStore
		want      selection.Record
	}{
		{"ses_open", "5", bucket, selection.Record{Key: "ses_open", Start: 5, Retired: []string{retired, current, candidate}}},
		{"ses_new", "3", bucket, selection.Record{Key: "ses_new", Start: 3}},
		{"ses_deleted", "2", bucket, selection.Record{Key: "ses_deleted", Start: 2}},
		{listenerKey, "4", bucket, selection.Record{Key: listenerKey, Start: 4}},
		{dispatchDurables()[0], "6", pg(familyDispatch, dispatchDB), selection.Record{Key: selection.PGKey(familyDispatch, dispatchDurables()[0]), Start: 6}},
		{"legion-go-proj-a-dispatch", "11", pg(daemonFamily("proj-a"), daemonDB), selection.Record{Key: selection.PGKey(daemonFamily("proj-a"), "legion-go-proj-a-dispatch"), Start: 11}},
	} {
		before := replays()
		code, _, stderr := h.run("replay", "--hold", id, "--key", row.key, "--from", row.from)
		require.Equal(t, 0, code, "replay %s\n%s", row.key, stderr)
		got, _, err := row.store().Read(ctx, row.key)
		require.NoError(t, err, row.key)
		require.Equal(t, row.want, got, row.key)
		require.Equal(t, before+2, replays(), "replay %s wrote its intent and done", row.key)
		d := h.journal(id)
		require.Equal(t, "done", d.Ops[len(d.Ops)-1].State, row.key)
		require.False(t, d.Complete, row.key)
	}

	before := replays()
	code, _, stderr = h.run("replay", "--hold", id, "--key", "ses_new", "--from", "12")
	require.Equal(t, 1, code, stderr)
	require.Contains(t, stderr, "--from 12 is past ENVOY_NOTIFICATIONS's last sequence 10 + 1")
	code, _, stderr = h.run("replay", "--hold", id, "--key", "ses_new", "--from", "2", "--final")
	require.Equal(t, 1, code, stderr)
	require.ErrorIs(t, holdrecord.Decide(h.journal(id), holdrecord.Request{Command: replayFinalCommand}), holdrecord.ErrNotAdmitted)
	require.Contains(t, stderr, "replay --final in cutover")
	require.Equal(t, before, replays(), "a refused replay wrote an entry")
	require.Empty(t, createBucketEntries(h.journal(id), replayFinalCommand))
	got, _, err := bucket().Read(ctx, "ses_new")
	require.NoError(t, err)
	require.Equal(t, selection.Record{Key: "ses_new", Start: 3}, got, "a refused replay wrote the record")
}

// In a selection-recovery hold, replay follows create-bucket --after-loss: before it, replay is
// refused naming the missing bucket, before its intent; after it, replay --final writes its record
// and completes the hold again, a replay after that leaves it incomplete, and a second replay
// --final completes it once more (its table: replay --final final and Repeatable). Control: the
// replay before the bucket leaves no entry.
func TestReplayInARecoveryFollowsCreateBucketAfterLoss(t *testing.T) {
	ctx := context.Background()
	h := newStack(t)
	createBucketNotifications(t, h, 5)
	id := h.heldHold(holdrecord.WorkflowSelectionRecovery)

	code, _, stderr := h.run("replay", "--hold", id, "--key", "ses_a", "--from", "2")
	require.Equal(t, 1, code, stderr)
	require.Contains(t, stderr, selectionStreamName+", which create-bucket makes first")
	require.Empty(t, createBucketEntries(h.journal(id), replayCommand), "control: the refused replay wrote an entry")

	code, _, stderr = h.run("create-bucket", "--hold", id, "--after-loss")
	require.Equal(t, 0, code, stderr)
	require.True(t, h.journal(id).Complete)

	bucket := func() familyStore {
		nc, _ := h.operator()
		return newSessionStore(nc)
	}
	for _, step := range []struct {
		args     []string
		complete bool
	}{
		{[]string{"--key", "ses_a", "--from", "2", "--final"}, true},
		{[]string{"--key", "ses_b", "--from", "3"}, false},
		{[]string{"--key", "ses_c", "--from", "4", "--final"}, true},
	} {
		code, _, stderr := h.run(append([]string{"replay", "--hold", id}, step.args...)...)
		require.Equal(t, 0, code, "replay %v\n%s", step.args, stderr)
		require.Equal(t, step.complete, h.journal(id).Complete, "replay %v", step.args)
		got, _, err := bucket().Read(ctx, step.args[1])
		require.NoError(t, err)
		require.Equal(t, step.args[3], formatSeq(got.Start))
	}
}

// In a selection-recovery hold after create-bucket --after-loss, replay --key K --from SEQ replaces
// a malformed session record: a STREAM.INFO answer stored under K through a reply subject, which
// the readers' strict decode refuses (readSelectionValue). After it K's value is exactly a record
// (strictRecord) with start SEQ, no current and no retired name, written over the answer's
// revision; replay's intent and done entries are written. Control: today's library decode
// (json.Unmarshal, selection.KVStore.Read) takes the answer as an empty record, which is what
// replay reads and writes over, so replay replaces it with no change of its own.
func TestReplayReplacesAMalformedSessionRecord(t *testing.T) {
	h := newStack(t)
	createBucketNotifications(t, h, 5)
	id := h.heldHold(holdrecord.WorkflowSelectionRecovery)
	code, _, stderr := h.run("create-bucket", "--hold", id, "--after-loss")
	require.Equal(t, 0, code, stderr)

	nc, _ := h.operator()
	malformed := malformedReplyStore(t, nc, "ses_bad")
	malformedRefused(t, "ses_bad", malformed)
	malformedCases("ses_bad", "")[0].lenient(t, malformed)

	code, _, stderr = h.run("replay", "--hold", id, "--key", "ses_bad", "--from", "3")
	require.Equal(t, 0, code, stderr)
	require.Equal(t, []string{"intent", "done"}, createBucketEntries(h.journal(id), replayCommand))
	nc, _ = h.operator()
	value := malformedValue(t, nc, "ses_bad")
	record, err := strictRecord(value)
	require.NoError(t, err, "ses_bad's value after replay is no exact record: %s", value)
	require.Equal(t, selection.Record{Key: "ses_bad", Start: 3}, record)
}

// A replay whose done entry is lost, after it wrote the key's record, writes its stop, recording
// what it left as unknown, and hold abort is then the way out (requireALostDoneStops, with its
// control: the intent left pending under the earlier rule).
func TestAReplayWhoseDoneEntryIsLostStops(t *testing.T) {
	h := newStack(t)
	createBucketNotifications(t, h, 3)
	id := h.prepared(holdrecord.WorkflowCutover)
	createBucketCohort(t, h.records(), id, 1)
	h.held(id)
	code, _, stderr := h.run("create-bucket", "--hold", id)
	require.Equal(t, 0, code, stderr)
	h.requireALostDoneStops(id, replayCommand, func() (int, string, string) {
		return h.run("replay", "--hold", id, "--key", "ses_lost", "--from", "2")
	})
}
