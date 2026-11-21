package consumernames

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/jsm.go"
	"github.com/nats-io/jsm.go/api"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	selection "github.com/sjawhar/envoy/consumernames"
)

// The --backup formats create-bucket --after-loss restores from, each read by one function
// (createBucketReadBackup):
//   - a snapshot, jsm.go's backup of the selection bucket's stream (backup.json and its archive),
//     which only a hold's operator takes: a snapshot's chunks go to a deliver subject the server
//     publishes to without checking the requester's publish rights, so the grant names
//     STREAM.SNAPSHOT only while every other client is refused. It is restored with STREAM.RESTORE;
//   - a walk, the released operator's read-only backup: every message of the bucket's stream read
//     by sequence with STREAM.MSG.GET, in the format envoy-kv-records-v1
//     (createBucketWalkFormat). It is restored by a replay: a fresh bucket, and one put of each
//     record key that holds no message yet (createBucketReplayWalk), so its sequences start over.
const (
	createBucketFormatSnapshot = "snapshot"
	createBucketFormatWalk     = "walk"
)

// createBucketBackup is a --backup directory, read and hashed before the hold's journal records
// anything.
type createBucketBackup struct {
	dir string
	// format is createBucketFormatSnapshot or createBucketFormatWalk.
	format string
	// sha256 identifies the backup: a snapshot's meta file and archive (createBucketReadSnapshot),
	// or a walk's SHA256SUMS, which names the sum of each of its files.
	sha256 string
	// archive is the stream state a restore of the backup reproduces: a snapshot's recorded state,
	// and for a walk the state its replay leaves in a fresh bucket, one message for each record.
	archive createBucketArchive
	// walk is a walk backup's records, nil for a snapshot.
	walk *createBucketWalk
}

// createBucketReadBackup reads dir in whichever of the formats it holds: a walk when the walk
// format recognises it, and otherwise a snapshot, whose reader refuses a directory that holds
// none.
func createBucketReadBackup(dir string) (*createBucketBackup, error) {
	if createBucketWalkFormat.holds(dir) {
		return createBucketReadWalk(dir)
	}
	return createBucketReadSnapshot(dir)
}

// createBucketReadSnapshot reads dir, a jsm.go backup of the selection bucket's stream
// (jsm.SnapshotToDirectory's backup.json and archive): its meta file must name the bucket's
// stream, and its sha256 is of the meta file's bytes and then the archive's, so a backup whose
// configuration or whose data differs is another backup.
func createBucketReadSnapshot(dir string) (*createBucketBackup, error) {
	dataPath, err := jsm.SnapshotDataPath(dir)
	if err != nil {
		return nil, fmt.Errorf("--backup %s holds neither a jsm.go snapshot nor a %s walk: %w", dir, createBucketWalkFormatName, err)
	}
	meta, err := os.ReadFile(filepath.Join(dir, jsm.SnapshotMetaFile))
	if err != nil {
		return nil, fmt.Errorf("--backup %s: %w", dir, err)
	}
	var request api.JSApiStreamRestoreRequest
	if err := json.Unmarshal(meta, &request); err != nil {
		return nil, fmt.Errorf("--backup %s: its %s does not decode: %w", dir, jsm.SnapshotMetaFile, err)
	}
	if request.Config.Name != createBucketStreamName {
		return nil, fmt.Errorf("--backup %s is a backup of %q, not of %s", dir, request.Config.Name, createBucketStreamName)
	}
	data, err := os.Open(dataPath)
	if err != nil {
		return nil, fmt.Errorf("--backup %s: %w", dir, err)
	}
	defer data.Close()
	sum := sha256.New()
	sum.Write(meta)
	if _, err := io.Copy(sum, data); err != nil {
		return nil, fmt.Errorf("--backup %s: read its archive: %w", dir, err)
	}
	return &createBucketBackup{dir: dir, format: createBucketFormatSnapshot, sha256: hex.EncodeToString(sum.Sum(nil)), archive: createBucketArchive{
		FirstSeq: request.State.FirstSeq, LastSeq: request.State.LastSeq, Msgs: request.State.Msgs,
	}}, nil
}

// createBucketWalkMessage is one message of the selection bucket's stream as a walk recorded it:
// its sequence, subject, data and headers, a KV delete or purge marker's KV-Operation among them.
type createBucketWalkMessage struct {
	Seq     uint64
	Subject string
	Data    []byte
	Header  nats.Header
}

// createBucketWalkDecoder is one walk backup format: how to recognise a directory of it, and how
// to read its messages and its sha256. Everything after the decode is this file's, so a format
// slots in as createBucketWalkFormat alone.
type createBucketWalkDecoder interface {
	// holds reports whether dir holds a walk backup of this format.
	holds(dir string) bool
	// decode reads dir's walk: every message it recorded, in sequence order, and the backup's
	// sha256.
	decode(dir string) ([]createBucketWalkMessage, string, error)
}

// createBucketWalkFormat is the walk format this build reads, envoy-kv-records-v1.
var createBucketWalkFormat createBucketWalkDecoder = createBucketRecordsV1{}

// createBucketWalkFormatName is the walk format's name, its manifest's format.
const createBucketWalkFormatName = "envoy-kv-records-v1"

// The files of an envoy-kv-records-v1 backup.
const (
	createBucketWalkManifest = "manifest.json"
	createBucketWalkRecords  = "records.jsonl"
	createBucketWalkSums     = "SHA256SUMS"
)

// createBucketRecordsV1 reads envoy-kv-records-v1, the released operator's walk of a KV bucket's
// stream (the hold owner's format, kvrecords on its branch, which this reader follows until that
// package's own reader is in this module): manifest.json, records.jsonl (one message a line, in
// sequence order) and SHA256SUMS (sha256sum's lines for manifest.json, then records.jsonl).
type createBucketRecordsV1 struct{}

// createBucketWalkManifestV1 is an envoy-kv-records-v1 manifest.
type createBucketWalkManifestV1 struct {
	Format        string `json:"format"`
	Bucket        string `json:"bucket"`
	Stream        string `json:"stream"`
	TakenAt       string `json:"taken_at"`
	FirstSeq      uint64 `json:"first_seq"`
	LastSeq       uint64 `json:"last_seq"`
	Records       int    `json:"records"`
	RecordsSHA256 string `json:"records_sha256"`
}

// createBucketWalkRecordV1 is one line of an envoy-kv-records-v1 records.jsonl.
type createBucketWalkRecordV1 struct {
	Seq     uint64              `json:"seq"`
	Subject string              `json:"subject"`
	Key     string              `json:"key"`
	Time    string              `json:"time"`
	Headers map[string][]string `json:"headers"`
	Data    string              `json:"data"`
}

// holds reports whether dir's manifest names envoy-kv-records-v1.
func (createBucketRecordsV1) holds(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, createBucketWalkManifest))
	if err != nil {
		return false
	}
	var m createBucketWalkManifestV1
	return json.Unmarshal(raw, &m) == nil && m.Format == createBucketWalkFormatName
}

// decode reads dir as envoy-kv-records-v1, refusing, naming what failed: a missing file, a
// SHA256SUMS line that does not match its file, another format, bucket or stream, a records count
// or records_sha256 that disagree with records.jsonl, a sequence that does not increase or falls
// outside first_seq..last_seq, a subject outside the bucket or whose key disagrees, data that is
// not base64. The backup's sha256 is SHA256SUMS's own, which names both files' sums.
func (createBucketRecordsV1) decode(dir string) ([]createBucketWalkMessage, string, error) {
	sums, err := os.ReadFile(filepath.Join(dir, createBucketWalkSums))
	if err != nil {
		return nil, "", fmt.Errorf("--backup %s: %w", dir, err)
	}
	want := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(sums), "\n"), "\n") {
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || len(sum) != 64 {
			return nil, "", fmt.Errorf("--backup %s: %s holds %q, no sha256sum line", dir, createBucketWalkSums, line)
		}
		want[name] = sum
	}
	files := map[string][]byte{}
	for _, name := range []string{createBucketWalkManifest, createBucketWalkRecords} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, "", fmt.Errorf("--backup %s: %w", dir, err)
		}
		got := sha256.Sum256(raw)
		if hex.EncodeToString(got[:]) != want[name] {
			return nil, "", fmt.Errorf("--backup %s: %s's sha256 is %x, and %s names %q", dir, name, got, createBucketWalkSums, want[name])
		}
		files[name] = raw
	}
	var m createBucketWalkManifestV1
	if err := json.Unmarshal(files[createBucketWalkManifest], &m); err != nil {
		return nil, "", fmt.Errorf("--backup %s: its %s does not decode: %w", dir, createBucketWalkManifest, err)
	}
	switch {
	case m.Format != createBucketWalkFormatName:
		return nil, "", fmt.Errorf("--backup %s is format %q, not %s", dir, m.Format, createBucketWalkFormatName)
	case m.Bucket != selection.SessionBucket || m.Stream != createBucketStreamName:
		return nil, "", fmt.Errorf("--backup %s is a walk of %q (%s), not of %s", dir, m.Bucket, m.Stream, selection.SessionBucket)
	case m.RecordsSHA256 != want[createBucketWalkRecords]:
		return nil, "", fmt.Errorf("--backup %s: its manifest's records_sha256 %q is not %s's %q", dir, m.RecordsSHA256, createBucketWalkSums, want[createBucketWalkRecords])
	}
	var messages []createBucketWalkMessage
	prefix := "$KV." + m.Bucket + "."
	scanner := bufio.NewScanner(bytes.NewReader(files[createBucketWalkRecords]))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var rec createBucketWalkRecordV1
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			return nil, "", fmt.Errorf("--backup %s: line %d of %s does not decode: %w", dir, len(messages)+1, createBucketWalkRecords, err)
		}
		key, ok := strings.CutPrefix(rec.Subject, prefix)
		switch {
		case rec.Seq < m.FirstSeq || rec.Seq > m.LastSeq:
			return nil, "", fmt.Errorf("--backup %s: sequence %d is outside the manifest's %d..%d", dir, rec.Seq, m.FirstSeq, m.LastSeq)
		case len(messages) > 0 && rec.Seq <= messages[len(messages)-1].Seq:
			return nil, "", fmt.Errorf("--backup %s: sequence %d follows %d", dir, rec.Seq, messages[len(messages)-1].Seq)
		case !ok || key != rec.Key:
			return nil, "", fmt.Errorf("--backup %s: sequence %d's subject %q is not the key %q of %s", dir, rec.Seq, rec.Subject, rec.Key, m.Bucket)
		}
		data, err := base64.StdEncoding.DecodeString(rec.Data)
		if err != nil {
			return nil, "", fmt.Errorf("--backup %s: sequence %d's data is not base64: %w", dir, rec.Seq, err)
		}
		var header nats.Header
		if len(rec.Headers) > 0 {
			header = nats.Header(rec.Headers)
		}
		messages = append(messages, createBucketWalkMessage{Seq: rec.Seq, Subject: rec.Subject, Data: data, Header: header})
	}
	if err := scanner.Err(); err != nil {
		return nil, "", fmt.Errorf("--backup %s: %s: %w", dir, createBucketWalkRecords, err)
	}
	if len(messages) != m.Records {
		return nil, "", fmt.Errorf("--backup %s: %s holds %d lines, and its manifest names %d records", dir, createBucketWalkRecords, len(messages), m.Records)
	}
	all := sha256.Sum256(sums)
	return messages, hex.EncodeToString(all[:]), nil
}

// createBucketReadWalk reads dir, a walk backup of the selection bucket, through
// createBucketWalkFormat, into its records (createBucketWalkFrom).
func createBucketReadWalk(dir string) (*createBucketBackup, error) {
	messages, sum, err := createBucketWalkFormat.decode(dir)
	if err != nil {
		return nil, err
	}
	walk, err := createBucketWalkFrom(messages)
	if err != nil {
		return nil, fmt.Errorf("--backup %s: %w", dir, err)
	}
	backup := &createBucketBackup{dir: dir, format: createBucketFormatWalk, sha256: sum, walk: walk}
	if n := uint64(len(walk.Entries)); n > 0 {
		backup.archive = createBucketArchive{FirstSeq: 1, LastSeq: n, Msgs: n}
	}
	return backup, nil
}

// createBucketWalk is a walk backup's content: each record key's last message, in the order the
// walk read them, and the walked sentinel's state, nil when the bucket held none (or a marker in
// its place). Of the system keys, every key holding a '.' (plan §1, "Keys holding a . are the
// system's"), only the listener durables' records (selection.DurableKey, durable.<name>) are
// records the recovery keeps: the sentinel is written new by the recovery, with the walked one's
// boundary and cutover marker, and a hold's barrier key is one no reader reads. A walk holding any
// other key with a '.' is refused, since nothing says what it is.
type createBucketWalk struct {
	Entries  []createBucketWalkEntry
	Sentinel *selection.StoreState
}

// createBucketWalkEntry is one record key's last message: its value and headers, a KV delete or
// purge marker's among them (Op "DEL" or "PURGE"), which the replay writes again, so a key whose
// record was deleted is refused after the recovery as before it (selection.ErrRecordDeleted).
type createBucketWalkEntry struct {
	Key    string
	Value  []byte
	Header nats.Header
	Op     string
}

// createBucketWalkFrom is the walk messages' content (createBucketWalk). The messages must be in
// increasing sequence order and each a key of the selection bucket; a key the walk read twice
// keeps its later message, as the bucket's history of one does.
func createBucketWalkFrom(messages []createBucketWalkMessage) (*createBucketWalk, error) {
	prefix := "$KV." + selection.SessionBucket + "."
	walk := &createBucketWalk{}
	at := map[string]int{}
	var last uint64
	for _, m := range messages {
		if m.Seq <= last {
			return nil, fmt.Errorf("the walk's message at sequence %d follows sequence %d: a walk is in sequence order", m.Seq, last)
		}
		last = m.Seq
		key, ok := strings.CutPrefix(m.Subject, prefix)
		if !ok || key == "" {
			return nil, fmt.Errorf("the walk's message at sequence %d is on %q, no key of %s", m.Seq, m.Subject, selection.SessionBucket)
		}
		op := m.Header.Get(kvOperationHeader)
		switch {
		case key == selection.SentinelKey:
			walk.Sentinel = nil
			if op == "" {
				var state selection.StoreState
				if err := json.Unmarshal(m.Data, &state); err != nil {
					return nil, fmt.Errorf("the walk's sentinel at sequence %d does not decode: %w", m.Seq, err)
				}
				walk.Sentinel = &state
			}
			continue
		case strings.HasPrefix(key, "hold.barrier."):
			continue
		case strings.Contains(key, ".") && selection.CheckDurableKey(key) != nil:
			return nil, fmt.Errorf("the walk's message at sequence %d is on the key %q, which holds a '.' and is neither the sentinel, a hold's barrier nor a listener durable's record", m.Seq, key)
		}
		if i, seen := at[key]; seen {
			walk.Entries = append(walk.Entries[:i], walk.Entries[i+1:]...)
			for k, j := range at {
				if j > i {
					at[k] = j - 1
				}
			}
		}
		at[key] = len(walk.Entries)
		walk.Entries = append(walk.Entries, createBucketWalkEntry{Key: key, Value: m.Data, Header: m.Header, Op: op})
	}
	return walk, nil
}

// kvOperationHeader marks a KV delete or purge marker (nats.go kv.go).
const kvOperationHeader = "KV-Operation"

// createBucketWalkHolds reads what the selection bucket holds against walk, behind the caller's
// barrier, for a bucket with no sentinel: each of walk's keys the bucket holds (held), which must
// hold the walk's message, and how many of the bucket's messages are no record of the walk
// (extra): any key other than the walk's and this hold's barrier. A key that holds another message
// than the walk's is named in foreign.
func createBucketWalkHolds(ctx context.Context, nc *nats.Conn, info *jetstream.StreamInfo, walk *createBucketWalk, holdID string) (held map[string]bool, extra uint64, foreign string, err error) {
	held = map[string]bool{}
	for _, entry := range walk.Entries {
		m, found, err := replayLeaderRead(ctx, nc, selection.SessionBucket, entry.Key)
		if err != nil {
			return nil, 0, "", err
		}
		if !found {
			continue
		}
		if !createBucketSameEntry(m, entry) {
			return held, 0, entry.Key, nil
		}
		held[entry.Key] = true
	}
	counted := uint64(len(held))
	if _, found, err := replayLeaderRead(ctx, nc, selection.SessionBucket, "hold.barrier."+holdID); err != nil {
		return nil, 0, "", err
	} else if found {
		counted++
	}
	if info.State.Msgs > counted {
		extra = info.State.Msgs - counted
	}
	return held, extra, "", nil
}

// createBucketSameEntry reports whether m, a key's last message, is entry's.
func createBucketSameEntry(m replayMessage, entry createBucketWalkEntry) bool {
	return m.Op == entry.Op && bytes.Equal(m.Data, entry.Value)
}

// createBucketReplayWalk loads walk into the fresh bucket: each record's last message published
// again, in the walk's order (the plan gate's v66 decision: no snapshot restore on this path), and
// adds the sequence each put was acknowledged at to own: the postcheck's account of the command's
// own writes, whose count is also what a stop says the load left.
func createBucketReplayWalk(ctx context.Context, nc *nats.Conn, walk *createBucketWalk, own map[uint64]bool) error {
	for _, entry := range walk.Entries {
		seq, err := createBucketPutWalkEntry(ctx, nc, entry)
		if err != nil {
			return err
		}
		own[seq] = true
	}
	return nil
}

// createBucketWalkPutTries bounds the tries of one replay put whose request went unanswered: the
// operator's user expires within 10 minutes of its decision, and its connection then reconnects
// on a fresh decision (plan §3a), so a long replay meets a closed connection.
const createBucketWalkPutTries = 3

// errKeyPresent is a replay put the bucket refused because the key already holds a message
// (10071, the expected last subject sequence 0 not met).
var errKeyPresent = errors.New("the key already holds a message")

// createBucketPutWalkEntry writes entry as a new key: conditional on the key holding no message,
// so the replay never overwrites a record. A put no stream answered ("no responders", as a bucket
// just created on three servers answers it) is sent again (createBucketFreshWrite); a put whose
// answer was lost is tried again once the connection is back; one the bucket then refuses as
// present is this replay's own when the key holds entry's message (plan §2, "Ambiguous results"),
// and otherwise the bucket is not this recovery's.
func createBucketPutWalkEntry(ctx context.Context, nc *nats.Conn, entry createBucketWalkEntry) (uint64, error) {
	for try := 1; ; try++ {
		var seq uint64
		err := createBucketFreshWrite(ctx, func() error {
			var err error
			seq, err = createBucketPutNew(ctx, nc, entry)
			return err
		})
		var answer *jetstream.APIError
		switch {
		case err == nil:
			return seq, nil
		case errors.Is(err, errKeyPresent):
			m, found, rerr := replayLeaderRead(ctx, nc, selection.SessionBucket, entry.Key)
			if rerr != nil {
				return 0, rerr
			}
			if try > 1 && found && createBucketSameEntry(m, entry) {
				return m.Seq, nil
			}
			return 0, fmt.Errorf("selection bucket exists and is not this recovery's: %s's key %s holds a message the replay did not write", selection.SessionBucket, entry.Key)
		case errors.As(err, &answer) || errors.Is(err, nats.ErrPermissionViolation) || try == createBucketWalkPutTries:
			return 0, err
		}
		if werr := createBucketAwaitConnected(ctx, nc); werr != nil {
			return 0, fmt.Errorf("%w (after %w)", werr, err)
		}
	}
}

// createBucketPutNew publishes entry on its key in the selection bucket with the walk's headers,
// conditional on the key's last sequence being 0 (no message yet), and answers the acknowledged
// sequence. A header the publish's own condition sets is not taken from the walk.
func createBucketPutNew(ctx context.Context, nc *nats.Conn, entry createBucketWalkEntry) (uint64, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, requestTimeout)
		defer cancel()
	}
	msg := nats.NewMsg("$KV." + selection.SessionBucket + "." + entry.Key)
	for name, values := range entry.Header {
		if strings.HasPrefix(name, "Nats-Expected-") {
			continue
		}
		for _, value := range values {
			msg.Header.Add(name, value)
		}
	}
	msg.Header.Set(jetstream.ExpectedLastSubjSeqHeader, strconv.FormatUint(0, 10))
	msg.Data = entry.Value
	reply, err := nc.RequestMsgWithContext(ctx, msg)
	if err != nil {
		return 0, apiRefusal(nc, msg.Subject, fmt.Errorf("the replay's put of %s: %w", entry.Key, err))
	}
	var ack struct {
		Seq   uint64              `json:"seq"`
		Error *jetstream.APIError `json:"error"`
	}
	if err := json.Unmarshal(reply.Data, &ack); err != nil {
		return 0, fmt.Errorf("the acknowledgement of the replay's put of %s: %w", entry.Key, err)
	}
	switch {
	case ack.Error != nil && ack.Error.ErrorCode == errWrongLastSubjectSeq:
		return 0, fmt.Errorf("the replay's put of %s: %w", entry.Key, errKeyPresent)
	case ack.Error != nil:
		return 0, fmt.Errorf("the replay's put of %s: %w", entry.Key, ack.Error)
	case ack.Seq == 0:
		return 0, fmt.Errorf("the replay's put of %s was acknowledged with no sequence", entry.Key)
	}
	return ack.Seq, nil
}

// errWrongLastSubjectSeq is JetStream's answer to a publish whose expected last subject sequence
// the stream does not hold (10071).
const errWrongLastSubjectSeq = 10071

// createBucketAwaitConnected waits until nc is connected again, within createBucketReconnectWait.
func createBucketAwaitConnected(ctx context.Context, nc *nats.Conn) error {
	deadline := time.Now().Add(createBucketReconnectWait)
	for !nc.IsConnected() {
		if nc.IsClosed() || time.Now().After(deadline) {
			return unknownf(nil, "the operator's connection did not reconnect within %s during the replay of %s", createBucketReconnectWait, selection.SessionBucket)
		}
		if err := createBucketSleep(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}
