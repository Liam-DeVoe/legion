package consumernames

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/jsm.go"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// createBucketMakeBucket makes the selection bucket at one copy with its sentinel state through
// nc as create-bucket does: the create, the new stream's leader (createBucketAwaitLeader), and the
// sentinel's write sent again while no stream answers it (createBucketFreshWrite).
// selection.CreateSessionBucket writes it at once, which a fresh stream answers "no responders" on
// a cluster, one copy or three (runs of 10:09Z and 10:28Z).
func createBucketMakeBucket(t testing.TB, nc *nats.Conn, state selection.StoreState) *selection.KVStore {
	t.Helper()
	ctx := context.Background()
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, selection.SessionBucketConfig(1))
	require.NoError(t, err)
	require.NoError(t, createBucketAwaitLeader(ctx, nc))
	store := selection.NewKVStore(selection.SessionBucket, selection.ConnRequester{Conn: nc})
	require.NoError(t, createBucketFreshWrite(ctx, func() error {
		_, err := store.WriteSentinel(ctx, state, 0)
		return err
	}))
	return store
}

// createBucketRefused is the journal's entries of command after one refusal after its intent: its
// intent, and its stop where workflow's table makes it Stoppable.
func createBucketRefused(w holdrecord.Workflow, command string) []string {
	if holdrecord.Stoppable(w, command) {
		return []string{"intent", "stop"}
	}
	return []string{"intent"}
}

// createBucketWriteWalk writes messages into dir as an envoy-kv-records-v1 walk of the selection
// bucket (records.jsonl, manifest.json, SHA256SUMS), as the released operator's backup writes one
// (the hold owner's format), its manifest naming first..last.
func createBucketWriteWalk(t *testing.T, dir string, messages []createBucketWalkMessage, first, last uint64) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	var records bytes.Buffer
	prefix := "$KV." + selection.SessionBucket + "."
	for _, m := range messages {
		headers := map[string][]string{}
		for name, values := range m.Header {
			headers[name] = values
		}
		line, err := json.Marshal(createBucketWalkRecordV1{Seq: m.Seq, Subject: m.Subject, Key: strings.TrimPrefix(m.Subject, prefix),
			Time: time.Now().UTC().Format(time.RFC3339Nano), Headers: headers, Data: base64.StdEncoding.EncodeToString(m.Data)})
		require.NoError(t, err)
		records.Write(line)
		records.WriteByte('\n')
	}
	recordsSum := sha256.Sum256(records.Bytes())
	manifest, err := json.Marshal(createBucketWalkManifestV1{Format: createBucketWalkFormatName, Bucket: selection.SessionBucket, Stream: createBucketStreamName,
		TakenAt: time.Now().UTC().Format(time.RFC3339), FirstSeq: first, LastSeq: last, Records: len(messages), RecordsSHA256: hex.EncodeToString(recordsSum[:])})
	require.NoError(t, err)
	manifestSum := sha256.Sum256(manifest)
	require.NoError(t, os.WriteFile(filepath.Join(dir, createBucketWalkRecords), records.Bytes(), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, createBucketWalkManifest), manifest, 0o600))
	sums := fmt.Sprintf("%x  %s\n%x  %s\n", manifestSum, createBucketWalkManifest, recordsSum, createBucketWalkRecords)
	require.NoError(t, os.WriteFile(filepath.Join(dir, createBucketWalkSums), []byte(sums), 0o600))
}

// createBucketWalkAs walks the selection bucket through nc as the released operator's backup
// does: STREAM.INFO once, then STREAM.MSG.GET of every sequence from its first to its last, a
// sequence the server holds no message at (10037) left out; and writes the walk into a directory
// of t's, which it answers.
func createBucketWalkAs(t *testing.T, nc *nats.Conn) string {
	t.Helper()
	ctx := context.Background()
	info, err := streamInfo(ctx, nc, createBucketStreamName)
	require.NoError(t, err, "the walk's STREAM.INFO")
	var messages []createBucketWalkMessage
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq && seq > 0; seq++ {
		raw, err := apiRequest(ctx, nc, "$JS.API.STREAM.MSG.GET."+createBucketStreamName, map[string]uint64{"seq": seq})
		var api *jetstream.APIError
		if errors.As(err, &api) && api.ErrorCode == errNoMessageFound {
			continue
		}
		require.NoError(t, err, "the walk's STREAM.MSG.GET of %d", seq)
		var answer struct {
			Message struct {
				Subject string `json:"subject"`
				Seq     uint64 `json:"seq"`
				Header  []byte `json:"hdrs"`
				Data    []byte `json:"data"`
			} `json:"message"`
		}
		require.NoError(t, json.Unmarshal(raw, &answer))
		m := createBucketWalkMessage{Seq: answer.Message.Seq, Subject: answer.Message.Subject, Data: answer.Message.Data}
		if len(answer.Message.Header) > 0 {
			m.Header, err = nats.DecodeHeadersMsg(answer.Message.Header)
			require.NoError(t, err)
		}
		messages = append(messages, m)
	}
	dir := filepath.Join(t.TempDir(), "walk")
	createBucketWriteWalk(t, dir, messages, info.State.FirstSeq, info.State.LastSeq)
	return dir
}

// createBucketMarker publishes a KV marker (op DEL or PURGE) on key through js, as nats.go's
// Delete and Purge send one.
func createBucketMarker(t *testing.T, js jetstream.JetStream, key, op string) {
	t.Helper()
	msg := nats.NewMsg("$KV." + selection.SessionBucket + "." + key)
	msg.Header.Set(kvOperationHeader, op)
	if op == "PURGE" {
		msg.Header.Set("Nats-Rollup", "sub")
	}
	_, err := js.PublishMsg(context.Background(), msg)
	require.NoError(t, err)
}

// A walk in envoy-kv-records-v1 reads into the selection bucket's records: every record's last
// message in sequence order, a DEL and a PURGE marker kept as markers, a listener durable's record
// (a key with a '.') kept, the sentinel taken out as the walk's sentinel, and a hold's barrier key
// left out; its sha256 is SHA256SUMS's own. Controls, each refused naming what failed: a changed
// byte in records.jsonl, a manifest whose records count disagrees, and a key with a '.' that is no
// system key the recovery knows. A directory of neither format is refused naming both.
func TestAWalkBackupReadsIntoTheBucketsRecords(t *testing.T) {
	prefix := "$KV." + selection.SessionBucket + "."
	sentinel, err := json.Marshal(selection.StoreState{Cutover: selection.CutoverDone, Boundary: 2, Hold: "the-cutover"})
	require.NoError(t, err)
	durable := selection.DurableKey(bus.ListenerDurable("hub-a"))
	messages := []createBucketWalkMessage{
		{Seq: 1, Subject: prefix + selection.SentinelKey, Data: sentinel},
		{Seq: 3, Subject: prefix + "ses_a", Data: []byte(`{"start":3}`)},
		{Seq: 4, Subject: prefix + "ses_gone", Header: nats.Header{kvOperationHeader: {"DEL"}}},
		{Seq: 5, Subject: prefix + "hold.barrier.h1", Data: []byte("h1")},
		{Seq: 6, Subject: prefix + durable, Data: []byte(`{"start":5}`)},
		{Seq: 8, Subject: prefix + "ses_purged", Header: nats.Header{kvOperationHeader: {"PURGE"}, "Nats-Rollup": {"sub"}}},
	}
	dir := filepath.Join(t.TempDir(), "walk")
	createBucketWriteWalk(t, dir, messages, 1, 8)
	backup, err := createBucketReadBackup(dir)
	require.NoError(t, err)
	require.Equal(t, createBucketFormatWalk, backup.format)
	sums, err := os.ReadFile(filepath.Join(dir, createBucketWalkSums))
	require.NoError(t, err)
	want := sha256.Sum256(sums)
	require.Equal(t, hex.EncodeToString(want[:]), backup.sha256)
	require.Equal(t, &selection.StoreState{Cutover: selection.CutoverDone, Boundary: 2, Hold: "the-cutover"}, backup.walk.Sentinel)
	var keys, ops []string
	for _, entry := range backup.walk.Entries {
		keys, ops = append(keys, entry.Key), append(ops, entry.Op)
	}
	require.Equal(t, []string{"ses_a", "ses_gone", durable, "ses_purged"}, keys)
	require.Equal(t, []string{"", "DEL", "", "PURGE"}, ops)
	require.Equal(t, createBucketArchive{FirstSeq: 1, LastSeq: 4, Msgs: 4}, backup.archive)

	t.Run("a changed byte", func(t *testing.T) {
		changed := filepath.Join(t.TempDir(), "walk")
		createBucketWriteWalk(t, changed, messages, 1, 8)
		path := filepath.Join(changed, createBucketWalkRecords)
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		raw[len(raw)/2] ^= 1
		require.NoError(t, os.WriteFile(path, raw, 0o600))
		_, err = createBucketReadBackup(changed)
		require.ErrorContains(t, err, createBucketWalkRecords+"'s sha256 is")
	})
	t.Run("a records count that disagrees", func(t *testing.T) {
		short := filepath.Join(t.TempDir(), "walk")
		createBucketWriteWalk(t, short, messages, 1, 8)
		var m createBucketWalkManifestV1
		raw, err := os.ReadFile(filepath.Join(short, createBucketWalkManifest))
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &m))
		m.Records--
		raw, err = json.Marshal(m)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(short, createBucketWalkManifest), raw, 0o600))
		sum := sha256.Sum256(raw)
		sums, err := os.ReadFile(filepath.Join(short, createBucketWalkSums))
		require.NoError(t, err)
		_, rest, _ := strings.Cut(string(sums), "\n")
		require.NoError(t, os.WriteFile(filepath.Join(short, createBucketWalkSums), []byte(fmt.Sprintf("%x  %s\n%s", sum, createBucketWalkManifest, rest)), 0o600))
		_, err = createBucketReadBackup(short)
		require.ErrorContains(t, err, "holds 6 lines, and its manifest names 5 records")
	})
	t.Run("an unknown key with a dot", func(t *testing.T) {
		odd := filepath.Join(t.TempDir(), "walk")
		createBucketWriteWalk(t, odd, append(slices.Clone(messages), createBucketWalkMessage{Seq: 9, Subject: prefix + "other.key", Data: []byte("x")}), 1, 9)
		_, err := createBucketReadBackup(odd)
		require.ErrorContains(t, err, `the key "other.key", which holds a '.'`)
	})
	t.Run("neither format", func(t *testing.T) {
		_, err := createBucketReadBackup(t.TempDir())
		require.ErrorContains(t, err, "holds neither a jsm.go snapshot nor a "+createBucketWalkFormatName+" walk")
	})
}

// createBucketLoseBucket sets a selection-recovery hold and deletes the selection bucket in it,
// as its loss leaves the hold to recover from, and answers the hold's id.
func createBucketLoseBucket(t *testing.T, h *stack) string {
	t.Helper()
	id := h.heldHold(holdrecord.WorkflowSelectionRecovery)
	_, js := h.operator()
	require.NoError(t, js.DeleteKeyValue(context.Background(), selection.SessionBucket))
	return id
}

// createBucketBackedBucket makes the selection bucket as a cutover left it, inside a
// buckets-create hold set and released with its journal complete: its sentinel (Cutover done,
// Boundary 2), a record, a deleted record (a DEL marker), a purged record (a PURGE marker over a
// value) and a listener durable's record. The row is released again when it returns.
func createBucketBackedBucket(t *testing.T, h *stack) map[string]selection.Record {
	t.Helper()
	ctx := context.Background()
	id := h.heldHold(holdrecord.WorkflowBucketsCreate)
	command := holdrecord.Commands(holdrecord.WorkflowBucketsCreate)[0]
	_, err := h.records().Admit(ctx, id, holdrecord.Request{Command: command}, nil)
	require.NoError(t, err)
	nc, js := h.operator()
	store := createBucketMakeBucket(t, nc, selection.StoreState{Cutover: selection.CutoverDone, Boundary: 2, Hold: "the-cutover"})
	records := map[string]selection.Record{
		"ses_backed": {Start: 4, Retired: []string{selection.FreshName(sessionFreshPrefix)}},
		selection.DurableKey(bus.ListenerDurable("hub-a")): {Current: selection.FreshName(bus.ListenerDurable("hub-a")), Start: 2},
	}
	for key, record := range records {
		_, err := store.Write(ctx, key, record, 0)
		require.NoError(t, err)
	}
	_, err = store.Write(ctx, "ses_gone", selection.Record{Start: 1}, 0)
	require.NoError(t, err)
	createBucketMarker(t, js, "ses_gone", "DEL")
	_, err = store.Write(ctx, "ses_purged", selection.Record{Start: 1}, 0)
	require.NoError(t, err)
	createBucketMarker(t, js, "ses_purged", "PURGE")
	require.NoError(t, h.records().AppendDone(ctx, id, command, nil))
	h.closeOperators()
	require.NoError(t, h.release(id))
	return records
}

// The selection bucket's walk, taken as the released operator (its STREAM.INFO and STREAM.MSG.GET,
// the only reads natsgrant.Operator(released) holds of the bucket, in envoy-kv-records-v1),
// restores with create-bucket --after-loss --backup after the bucket is lost: the load publishes
// each record's last message into a fresh bucket, its sequences started over; every record, the
// DEL and PURGE markers (a deleted record is refused after the recovery as before it) and the
// listener durable's record come back, the sentinel holds the walked sentinel's boundary and
// cutover marker in replay mode, the postcheck passes and the hold completes. Controls, each on
// the walk's path itself (the plan gate's v66 decision), on a hub whose operator user holds
// exactly Operator(held): the same walk restored by it succeeds, and without
// $KV.envoy_session_consumers.> (the load's publishes) or without the bucket's STREAM.CREATE it
// fails, naming the refused subject; and the same loss recovered with no backup brings no record
// back. The walk here is the test's own writer of the hold owner's format, and the row takes it
// with the owner's writer once that is in this module.
func TestTheReleasedOperatorsBackupRestoresTheSelectionBucket(t *testing.T) {
	ctx := context.Background()
	h := newStack(t)
	createBucketNotifications(t, h, 3)
	records := createBucketBackedBucket(t, h)

	released, _ := h.operator()
	before, err := streamInfo(ctx, released, createBucketStreamName)
	require.NoError(t, err)
	dir := createBucketWalkAs(t, released)
	h.closeOperators()

	// The controls on the walk's path: the held grant, then without each of the load's subjects.
	held := natsgrant.Operator(natsgrant.OperatorPhaseHeld)
	for _, row := range []struct {
		name, without string
	}{
		{"the whole held grant", ""},
		{"without the load's publishes", "$KV." + selection.SessionBucket + ".>"},
		{"without the bucket's create", "$JS.API.STREAM.CREATE." + createBucketStreamName},
	} {
		grant := held
		grant.Publish = slices.DeleteFunc(slices.Clone(held.Publish), func(entry string) bool { return entry == row.without })
		g := grantsNewRig(t, 1, grant)
		g.notifications(0)
		id := g.hold(holdrecord.WorkflowSelectionRecovery, natsgrant.OperatorPhaseHeld)
		code, stdout, stderr := createBucketRun(t, ctx, g.env(), "create-bucket", "--hold", id, "--after-loss", "--backup", dir)
		if row.without == "" {
			require.Equal(t, 0, code, "control %s\n%s%s", row.name, stdout, stderr)
			continue
		}
		require.NotEqual(t, 0, code, "control %s\n%s", row.name, stderr)
		require.True(t, grantsNamesOneOf(stderr, []string{row.without}), "control %s names no refused subject of %s\n%s", row.name, row.without, stderr)
	}

	id := createBucketLoseBucket(t, h)
	code, stdout, stderr := h.run("create-bucket", "--hold", id, "--after-loss", "--backup", dir)
	require.Equal(t, 0, code, stderr)
	require.Contains(t, stdout, "restored and its sentinel written")
	require.Contains(t, stdout, "postcheck passed")
	require.True(t, h.journal(id).Complete)
	info, _, sentinel, _ := createBucketState(h)
	require.Less(t, info.State.LastSeq, before.State.LastSeq, "the replay's sequences start over")
	require.Equal(t, uint64(2), sentinel.state.Boundary)
	require.Equal(t, selection.CutoverDone, sentinel.state.Cutover)
	require.Equal(t, uint64(1), sentinel.state.ReplayFrom)
	nc, _ := h.operator()
	store := selection.NewKVStore(selection.SessionBucket, selection.ConnRequester{Conn: nc})
	for key, record := range records {
		got, _, err := store.Read(ctx, key)
		require.NoError(t, err, key)
		require.Equal(t, record, got, key)
	}
	for _, key := range []string{"ses_gone", "ses_purged"} {
		_, _, err := store.Read(ctx, key)
		require.ErrorIs(t, err, selection.ErrRecordDeleted, "%s's marker came back", key)
	}
	h.closeOperators()
	require.NoError(t, h.release(id))

	// The control: the same loss, recovered with no backup.
	id = createBucketLoseBucket(t, h)
	code, _, stderr = h.run("create-bucket", "--hold", id, "--after-loss")
	require.Equal(t, 0, code, stderr)
	nc, _ = h.operator()
	_, _, err = selection.NewKVStore(selection.SessionBucket, selection.ConnRequester{Conn: nc}).Read(ctx, "ses_backed")
	require.ErrorIs(t, err, selection.ErrNoRecord, "control: a recovery with no backup brought the record back")
}

// createBucketPutCutDialer dials as nats.go does, and once, when a connection writes its after-th
// put of a record key of the selection bucket (a PUB on $KV.envoy_session_consumers.ses_), ends
// the run (cut) and closes that connection before that put is sent: a replay killed part way.
type createBucketPutCutDialer struct {
	after int
	cut   func()
	puts  atomic.Int32
	done  atomic.Bool
}

func (d *createBucketPutCutDialer) Dial(network, address string) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).Dial(network, address)
	if err != nil {
		return nil, err
	}
	return &createBucketPutCutConn{Conn: conn, d: d}, nil
}

type createBucketPutCutConn struct {
	net.Conn
	d *createBucketPutCutDialer
}

func (c *createBucketPutCutConn) Write(p []byte) (int, error) {
	// "PUB $KV.…" matches a headered HPUB too, which each replay put is.
	puts := bytes.Count(p, []byte("PUB $KV."+selection.SessionBucket+".ses_"))
	if puts > 0 && int(c.d.puts.Add(int32(puts))) >= c.d.after && c.d.done.CompareAndSwap(false, true) {
		c.d.cut()
		_ = c.Conn.Close()
		return 0, net.ErrClosed
	}
	return c.Conn.Write(p)
}

// createBucketWalkOfRecords is a walk of the selection bucket holding n records ses_<i> and a
// sentinel with boundary 2.
func createBucketWalkOfRecords(t *testing.T, n int) []createBucketWalkMessage {
	t.Helper()
	prefix := "$KV." + selection.SessionBucket + "."
	sentinel, err := json.Marshal(selection.StoreState{Cutover: selection.CutoverDone, Boundary: 2, Hold: "the-cutover"})
	require.NoError(t, err)
	messages := []createBucketWalkMessage{{Seq: 1, Subject: prefix + selection.SentinelKey, Data: sentinel}}
	for i := range n {
		record, err := json.Marshal(selection.Record{Start: uint64(i + 1)})
		require.NoError(t, err)
		messages = append(messages, createBucketWalkMessage{Seq: uint64(i + 2), Subject: fmt.Sprintf("%sses_%02d", prefix, i), Data: record})
	}
	return messages
}

// A cut walk load is deleted and loaded again (the plan gate's v66 decision): create-bucket
// --after-loss from a walk of 12 records, killed after it put 3 of them (its connection closed and
// its run ended), leaves a bucket holding part of the walk and no sentinel; the rerun reads that,
// by each key's last message and the count of its messages, as the leftover of its own cut load,
// deletes the bucket and loads the walk into a fresh one, writes the sentinel, passes its
// postcheck and completes: all 12 records, and no message but theirs, the sentinel and this hold's
// barrier. Control: a bucket that also holds a record the walk does not hold stops the rerun with
// "selection bucket exists and is not this recovery's", naming the messages that are no record of
// the walk, and deletes nothing.
func TestACutWalkLoadIsDeletedAndLoadedAgain(t *testing.T) {
	ctx := context.Background()
	messages := createBucketWalkOfRecords(t, 12)
	killed := func(t *testing.T) (*stack, string, string) {
		h := newStack(t)
		createBucketNotifications(t, h, 3)
		dir := filepath.Join(t.TempDir(), "walk")
		createBucketWriteWalk(t, dir, messages, 1, uint64(len(messages)))
		id := h.heldHold(holdrecord.WorkflowSelectionRecovery)
		run, kill := context.WithCancel(ctx)
		defer kill()
		dialer := &createBucketPutCutDialer{after: 4, cut: kill}
		e := h.e
		e.connect = func(context.Context, options) (*nats.Conn, error) {
			return nats.Connect(h.s.Cluster.URL(), append(h.s.OperatorCredential(), nats.SetCustomDialer(dialer), nats.Name("envoy-infra consumer-names"),
				nats.MaxReconnects(-1), nats.ReconnectWait(100*time.Millisecond), nats.Timeout(10*time.Second))...)
		}
		h.closeOperators()
		code, _, stderr := createBucketRun(t, run, e, "create-bucket", "--hold", id, "--after-loss", "--backup", dir)
		require.True(t, dialer.done.Load(), "the replay was never cut\n%s", stderr)
		require.NotEqual(t, 0, code, stderr)
		info, present, _, found := createBucketState(h)
		require.True(t, present)
		require.False(t, found, "the cut replay wrote the sentinel")
		require.Less(t, info.State.Msgs, uint64(12), "the cut replay put every record")
		return h, id, dir
	}

	t.Run("deleted and loaded again", func(t *testing.T) {
		h, id, dir := killed(t)
		cut, _, _, _ := createBucketState(h)
		code, stdout, stderr := h.run("create-bucket", "--hold", id, "--after-loss", "--backup", dir)
		require.Equal(t, 0, code, stderr)
		require.Contains(t, stdout, "restored and its sentinel written")
		require.Contains(t, stdout, "postcheck passed")
		require.True(t, h.journal(id).Complete)
		info, _, sentinel, _ := createBucketState(h)
		require.False(t, info.Created.Equal(cut.Created), "the partly loaded bucket was not deleted and made again")
		require.Equal(t, uint64(12+2), info.State.Msgs, "the loaded bucket: the walk's 12 records, the sentinel and this hold's barrier")
		require.Equal(t, uint64(2), sentinel.state.Boundary)
		nc, _ := h.operator()
		store := selection.NewKVStore(selection.SessionBucket, selection.ConnRequester{Conn: nc})
		for i := range 12 {
			got, _, err := store.Read(ctx, fmt.Sprintf("ses_%02d", i))
			require.NoError(t, err)
			require.Equal(t, uint64(i+1), got.Start)
		}
	})

	t.Run("control: a record the walk does not hold", func(t *testing.T) {
		h, id, dir := killed(t)
		nc, _ := h.operator()
		_, err := selection.NewKVStore(selection.SessionBucket, selection.ConnRequester{Conn: nc}).Write(ctx, "ses_foreign", selection.Record{Start: 9}, 0)
		require.NoError(t, err)
		code, _, stderr := h.run("create-bucket", "--hold", id, "--after-loss", "--backup", dir)
		require.Equal(t, 1, code, stderr)
		require.Contains(t, stderr, "selection bucket exists and is not this recovery's: "+selection.SessionBucket+" holds 1 messages that are no record of the walk")
		require.False(t, h.journal(id).Complete)
		_, present, _, _ := createBucketState(h)
		require.True(t, present, "control: the stopped rerun deleted the bucket")
	})
}

// createBucketHeldSnapshotGrant is the held operator's grant of the selection bucket's snapshot
// (STREAM.SNAPSHOT and its chunks' acknowledgements), which the hold's owner moves into
// natsgrant.operatorHeld alone.
var createBucketHeldSnapshotGrant = []string{"$JS.API.STREAM.SNAPSHOT." + createBucketStreamName, "$JS.SNAPSHOT.ACK." + createBucketStreamName + ".>"}

// A snapshot of the selection bucket taken inside a hold, as the held operator, through jsm.go's
// SnapshotToDirectory on its own connection, restores with create-bucket --after-loss --backup
// after the bucket is lost: the recovery completes and the records come back. The row waits for
// the held grant to name the snapshot's subjects, and says so when it does not.
func TestAHeldSnapshotRestoresTheSelectionBucket(t *testing.T) {
	for _, subject := range createBucketHeldSnapshotGrant {
		if !slices.Contains(natsgrant.Operator(natsgrant.OperatorPhaseHeld).Publish, subject) {
			t.Skipf("natsgrant.Operator(held) does not name %s yet: this row waits for the hold's grant to take the selection bucket's snapshot", subject)
		}
	}
	ctx := context.Background()
	h := newStack(t)
	createBucketNotifications(t, h, 3)
	records := createBucketBackedBucket(t, h)

	id := h.heldHold(holdrecord.WorkflowBucketsCreate)
	command := holdrecord.Commands(holdrecord.WorkflowBucketsCreate)[0]
	_, err := h.records().Admit(ctx, id, holdrecord.Request{Command: command}, nil)
	require.NoError(t, err)
	held, _ := h.operator()
	mgr, err := jsm.New(held, jsm.WithTimeout(30*time.Second))
	require.NoError(t, err)
	stream, err := mgr.LoadStream(createBucketStreamName)
	require.NoError(t, err)
	dir := filepath.Join(t.TempDir(), "snapshot")
	_, err = stream.SnapshotToDirectory(ctx, dir)
	require.NoError(t, err, "the snapshot as the held operator")
	require.NoError(t, h.records().AppendDone(ctx, id, command, nil))
	h.closeOperators()
	require.NoError(t, h.release(id))

	id = createBucketLoseBucket(t, h)
	code, stdout, stderr := h.run("create-bucket", "--hold", id, "--after-loss", "--backup", dir)
	require.Equal(t, 0, code, stderr)
	require.Contains(t, stdout, "restored and its sentinel written")
	require.True(t, h.journal(id).Complete)
	nc, _ := h.operator()
	store := selection.NewKVStore(selection.SessionBucket, selection.ConnRequester{Conn: nc})
	for key, record := range records {
		got, _, err := store.Read(ctx, key)
		require.NoError(t, err, key)
		require.Equal(t, record, got, key)
	}
}

// createBucketRequireStop requires that command's latest entry in hold id's journal is a stop
// whose reason names each of left, and that the stop gives the hold its way out: hold abort is
// admitted. Then, as the control, it writes command's intent again as a run that dies before its
// stop would, and requires hold release refused while that intent is pending.
func createBucketRequireStop(t *testing.T, h *stack, id, command string, left ...string) {
	t.Helper()
	d := h.journal(id)
	var last holdrecord.Operation
	for _, op := range d.Ops {
		if op.Command == command {
			last = op
		}
	}
	require.Equal(t, "stop", last.State, "%s's latest entry", command)
	for _, want := range left {
		require.Contains(t, last.Outputs["reason"], want, "%s's stop names what it left", command)
	}
	require.NoError(t, holdrecord.Decide(d, holdrecord.Request{Command: holdrecord.CommandAbort}), "the stop leaves the hold no way out")

	_, err := h.records().Admit(context.Background(), id, holdrecord.Request{Command: command}, nil)
	require.NoError(t, err)
	require.ErrorIs(t, holdrecord.Decide(h.journal(id), holdrecord.Request{Command: holdrecord.CommandRelease}), holdrecord.ErrBusy,
		"control: with no stop after %s's intent, hold release is admitted", command)
}

// createBucketSkipUnlessStoppable skips a row whose command the workflow's table does not make
// Stoppable yet.
func createBucketSkipUnlessStoppable(t *testing.T, w holdrecord.Workflow, command string) {
	t.Helper()
	if !holdrecord.Stoppable(w, command) {
		t.Skipf("%s is not Stoppable in %s yet: this row waits for the hold's table to stop it", command, w)
	}
}

// create-bucket's refusal after its intent (a present bucket with another configuration, history
// 2, which is UNKNOWN) writes its stop entry naming what it left, here nothing outside the
// journal, and the hold has its way out. Control: the same command's intent with no stop after it
// leaves hold release refused.
func TestACreateBucketRefusalAfterItsIntentStops(t *testing.T) {
	createBucketSkipUnlessStoppable(t, holdrecord.WorkflowCutover, createBucketCommand)
	h := newStack(t)
	createBucketNotifications(t, h, 0)
	id := h.prepared(holdrecord.WorkflowCutover)
	createBucketCohort(t, h.records(), id, 2)
	h.held(id)
	_, js := h.operator()
	config := selection.SessionBucketConfig(1)
	config.History = 2
	_, err := js.CreateKeyValue(context.Background(), config)
	require.NoError(t, err)
	code, _, stderr := h.run("create-bucket", "--hold", id)
	require.Equal(t, 2, code, stderr)
	require.Contains(t, stderr, "it left nothing outside the journal")
	createBucketRequireStop(t, h, id, createBucketCommand, "it left nothing outside the journal")
}

// create-bucket --after-loss's refusal after its intent (a bucket that is not this recovery's)
// writes its stop entry naming what it left: the hold's selection record and this hold's barrier
// key in the bucket; the hold has its way out. Control: the same command's intent with no stop
// after it leaves hold release refused.
func TestARecoveryRefusalAfterItsIntentStops(t *testing.T) {
	createBucketSkipUnlessStoppable(t, holdrecord.WorkflowSelectionRecovery, createBucketAfterLossCommand)
	h := newStack(t)
	createBucketNotifications(t, h, 0)
	id := h.heldHold(holdrecord.WorkflowSelectionRecovery)
	nc, _ := h.operator()
	createBucketMakeBucket(t, nc, selection.StoreState{Cutover: selection.CutoverDone, Boundary: 3, Hold: "another-hold"})
	code, _, stderr := h.run("create-bucket", "--hold", id, "--after-loss")
	require.Equal(t, 1, code, stderr)
	createBucketRequireStop(t, h, id, createBucketAfterLossCommand, "the hold's selection record", "this hold's barrier key in "+createBucketStreamName)
}

// replay's refusal after its intent (a record that does not decode) writes its stop entry naming
// what it left, this hold's barrier key in the bucket, and the hold has its way out. Control: the
// same command's intent with no stop after it leaves hold release refused.
func TestAReplayRefusalAfterItsIntentStops(t *testing.T) {
	createBucketSkipUnlessStoppable(t, holdrecord.WorkflowCutover, replayCommand)
	h := newStack(t)
	createBucketNotifications(t, h, 3)
	id := h.prepared(holdrecord.WorkflowCutover)
	createBucketCohort(t, h.records(), id, 1)
	h.held(id)
	code, _, stderr := h.run("create-bucket", "--hold", id)
	require.Equal(t, 0, code, stderr)
	_, js := h.operator()
	_, err := js.Publish(context.Background(), "$KV."+selection.SessionBucket+".ses_bad", []byte("no record"))
	require.NoError(t, err)
	code, _, stderr = h.run("replay", "--hold", id, "--key", "ses_bad", "--from", "2")
	require.Equal(t, 1, code, stderr)
	createBucketRequireStop(t, h, id, replayCommand, "this hold's barrier key in "+createBucketStreamName)
}
