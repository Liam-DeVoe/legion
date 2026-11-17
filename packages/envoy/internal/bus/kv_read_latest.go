package bus

import (
	"context"
	"fmt"
	"slices"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// BarrierKey is the one key of every bucket that ReadLatest writes and no other call may name:
// check refuses it for every get, write, delete, purge and watch through a KeyValue
// (ErrReservedKey), and kvwatch drops its records before a copy or a scan sees them. It cannot be a
// key any store here writes: a session id the listener stores or deletes is a key through this
// package's check; a role is `^[a-z0-9][a-z0-9_-]*$` (roles.PatternString), which no key beginning
// with '_' matches; a CI record's key is `<owner>.<repo>.pr<number>.<sha>` (cistore.Key), four
// tokens; and an interest or known-session record is keyed by a session id. It is a key nats.go
// accepts in every bucket: letters, '-' and '_', no dot (natsKeyValid).
const BarrierKey = "_kvwatch-barrier"

// readLatestChunk is how many keys one watch of ReadLatest filters on: each becomes a filter
// subject of one consumer create request, and 256 of the longest key this package lets a watch
// name keep that request far below the server's max payload.
const readLatestChunk = 256

// ReadLatest reads the latest record of each of keys from a store of the bucket that has applied
// every write the bucket committed before the call, and returns each key whose latest record is a
// put with that record. A key missing from the answer has no record in that store, or a delete or
// purge marker as its latest, or is a key this handle refuses (check, natsKeyValid), under which no
// call here can read a record: the bucket holds no value for it.
//
// No single read of a replicated stream says that much. A direct get is answered by any member of
// the stream's group that is 90% caught up, and a stream message get by whichever member believes
// it leads, which a member cut off from the others goes on believing until its lost-quorum check,
// 10 s (nats-server v2.15.1 raft.go:321-323). A write is the one thing a majority confirms: its
// publish is acknowledged only once it is committed. So ReadLatest first writes a delete marker to
// BarrierKey and takes its sequence from the acknowledgement, then reads the keys and BarrierKey
// through one watch per readLatestChunk keys, an ordered consumer delivering each subject's latest
// record and every record after it in stream order, and keeps reading until that watch has both
// delivered its existing records and delivered BarrierKey at the barrier's sequence or later. A
// store that has applied the barrier has applied every write committed before it, and what the
// watch delivered up to the barrier is that store's state then. A store cut off from a majority
// cannot commit the barrier, so the read fails rather than answer from it.
//
// Each step is held to RequestTimeout under ctx. An error answers nothing: a caller that removes
// what the bucket does not hold removes nothing on it.
func (kv KeyValue) ReadLatest(ctx context.Context, keys []string) (map[string]jetstream.KeyValueEntry, error) {
	readable := make([]string, 0, len(keys))
	for _, key := range keys {
		if kv.check(key, watchOverhead(kv.Bucket())) == nil && natsKeyValid(key) {
			readable = append(readable, key)
		}
	}
	latest := make(map[string]jetstream.KeyValueEntry, len(readable))
	if len(readable) == 0 {
		return latest, nil
	}
	barrier, err := kv.writeBarrier(ctx)
	if err != nil {
		return nil, fmt.Errorf("write the read barrier of KV bucket %s: %w", kv.Bucket(), err)
	}
	for chunk := range slices.Chunk(readable, readLatestChunk) {
		if err := kv.readPast(ctx, chunk, barrier, latest); err != nil {
			return nil, err
		}
	}
	return latest, nil
}

// writeBarrier writes a delete marker to BarrierKey, as nats.go's KV delete writes one (the
// KV-Operation header DEL, jetstream/kv.go), and returns the sequence the acknowledgement names:
// the barrier is committed at it. It publishes through the jetstream context, which a KeyValue's
// own Delete does not reach, since check refuses BarrierKey there.
func (kv KeyValue) writeBarrier(ctx context.Context) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	msg := nats.NewMsg("$KV." + kv.Bucket() + "." + BarrierKey)
	msg.Header.Set(kvOperationHeader, kvDeleteMarker)
	ack, err := kv.streams.PublishMsg(ctx, msg, jetstream.WithExpectStream("KV_"+kv.Bucket()))
	if err != nil {
		return 0, err
	}
	return ack.Sequence, nil
}

// readPast reads keys and BarrierKey through one watch until it has delivered every existing
// record of them and BarrierKey at barrier or later, recording in latest each key's latest put and
// removing a key whose later record is a delete or purge marker. What is recorded once the barrier
// is delivered is the answering store's state at the barrier or later.
func (kv KeyValue) readPast(ctx context.Context, keys []string, barrier uint64, latest map[string]jetstream.KeyValueEntry) error {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	// WatchFiltered writes each filter's subject into the slice it is handed.
	watcher, err := kv.watch.WatchFiltered(ctx, append(slices.Clone(keys), BarrierKey))
	if err != nil {
		return fmt.Errorf("watch %d keys of KV bucket %s past its read barrier: %w", len(keys), kv.Bucket(), invalidKey(keys, err))
	}
	defer func() {
		_ = watcher.Stop()
		go DrainWatcher(watcher)
	}()
	scanned, past := false, false
	updates := watcher.Updates()
	for !scanned || !past {
		select {
		case entry, open := <-updates:
			switch {
			case !open:
				return fmt.Errorf("the watch of %d keys of KV bucket %s ended before it delivered its read barrier at %d", len(keys), kv.Bucket(), barrier)
			case entry == nil:
				scanned = true
			case entry.Key() == BarrierKey:
				past = past || entry.Revision() >= barrier
			case entry.Operation() == jetstream.KeyValuePut:
				latest[entry.Key()] = entry
			default:
				delete(latest, entry.Key())
			}
		case <-ctx.Done():
			return fmt.Errorf("read %d keys of KV bucket %s past its read barrier at %d (existing records delivered: %v, barrier delivered: %v): %w",
				len(keys), kv.Bucket(), barrier, scanned, past, ctx.Err())
		}
	}
	return nil
}

// IsBarrier reports whether key is BarrierKey, whose records every reader of a bucket drops.
func IsBarrier(key string) bool { return key == BarrierKey }
