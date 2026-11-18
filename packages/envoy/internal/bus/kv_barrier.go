package bus

import (
	"context"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// BarrierKey is the one key of every bucket that WriteBarrier writes and no other call may name:
// check refuses it for every get, write, delete, purge and watch through a KeyValue
// (ErrReservedKey), and kvwatch drops its records before a copy or a scan sees them. It cannot be a
// key any store here writes: a session id the listener stores or deletes is a key through this
// package's check; a role is `^[a-z0-9][a-z0-9_-]*$` (roles.PatternString), which no key beginning
// with '_' matches; a CI record's key is `<owner>.<repo>.pr<number>.<sha>` (cistore.Key), four
// tokens; and an interest or known-session record is keyed by a session id. It is a key nats.go
// accepts in every bucket: letters, '-' and '_', no dot (natsKeyValid).
const BarrierKey = "_kvwatch-barrier"

// WriteBarrier writes a delete marker to BarrierKey, as nats.go's KV delete writes one (the
// KV-Operation header DEL, jetstream/kv.go), and returns the stream sequence the acknowledgement
// names. The acknowledgement is the one thing a majority of the stream's group confirms: the
// leader sends it only from its apply of the committed entry (nats-server v2.15.1
// jetstream_cluster.go:5947, :6025, applyStreamMsgOp calling processJetStreamMsg, which answers
// only as leader, stream.go:6560), so a member cut off from the others cannot write one, and a
// store holding the barrier has applied every entry committed before it, in log order (raft.go:4009
// applyCommit, jetstream_cluster.go:5513 applyStreamEntries). kvwatch's read reads its own
// consumer past one (Watcher.consume) before it removes anything.
//
// It publishes through the jetstream context, which a KeyValue's own Delete does not reach, since
// check refuses BarrierKey there. The publish is held to RequestTimeout under ctx.
func (kv KeyValue) WriteBarrier(ctx context.Context) (uint64, error) {
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
