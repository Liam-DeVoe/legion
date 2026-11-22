package writerhold

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/testnats"
)

// A restored stream's counts after the command's writes cannot tell a key the restore lost and
// the command wrote again from a key the command replaced, so the command states the stream's
// reading taken right after the restore, before any write (Change.Restored), and the postcheck
// requires it to be the archive's, then counts the writes from it. On a real KV stream whose
// archive holds keys a and b (last_seq 2, 2 messages), the command writes a new barrier key and the
// sentinel a, Writes 2 and Replaced 1:
//   - a whole restore reads 2 messages to seq 2 after the restore, and passes;
//   - a restore that lost a (purged) reads 1 message to seq 2 after it, and is refused naming the
//     reading and the archive, though its final state, 3 messages to seq 4, is the whole one's.
//
// The reviewer's case (round 6, Minor 2); control: 17f5b356b0's compare, which counted from the
// archive alone and passed the lost restore with Replaced 1.
func TestThePostcheckCountsARestoredStreamFromItsReadingAfterTheRestore(t *testing.T) {
	archive := ArchiveState{LastSeq: 2, Messages: 2}
	for _, row := range []struct {
		name string
		lost bool
	}{{"control: a whole restore", false}, {"a restore that lost a key the command then wrote", true}} {
		t.Run(row.name, func(t *testing.T) {
			ctx := t.Context()
			_, url := testnats.Start(t)
			nc, err := testnats.ConnectSeed(t, url, "")
			require.NoError(t, err)
			t.Cleanup(nc.Close)
			js, err := jetstream.New(nc)
			require.NoError(t, err)
			kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "roles", History: 1})
			require.NoError(t, err)
			for _, key := range []string{"a", "b"} {
				_, err = kv.Put(ctx, key, []byte("1"))
				require.NoError(t, err)
			}
			stream, err := js.Stream(ctx, "KV_roles")
			require.NoError(t, err)
			if row.lost {
				require.NoError(t, stream.Purge(ctx, jetstream.WithPurgeSubject("$KV.roles.a")))
			}
			restored, err := stream.Info(ctx)
			require.NoError(t, err)
			_, err = kv.Put(ctx, "barrier.h1", []byte("1"))
			require.NoError(t, err)
			_, err = kv.Put(ctx, "a", []byte("sentinel"))
			require.NoError(t, err)
			info, err := stream.Info(ctx)
			require.NoError(t, err)
			now := StreamReading{Name: "KV_roles", Created: info.Created, LastSeq: info.State.LastSeq, Messages: info.State.Msgs}
			require.Equal(t, StreamReading{Name: "KV_roles", Created: info.Created, LastSeq: 4, Messages: 3}, now, "both restores end in the same state")
			was := StreamReading{Name: "KV_roles", Created: info.Created.Add(-time.Hour), LastSeq: 2, Messages: 2}
			change := Change{Kind: ChangeRestored, Writes: 2, Replaced: 1, Archive: archive,
				Restored: ArchiveState{LastSeq: restored.State.LastSeq, Messages: restored.State.Msgs}}

			err = compare(now.Name, was, now, change)
			if !row.lost {
				require.NoError(t, err)
				return
			}
			var refused *Refusal
			require.ErrorAs(t, err, &refused, "a restore that lost a key passed")
			require.ErrorContains(t, err, "1 messages to seq 2 right after the restore")
			require.ErrorContains(t, err, "the archive's 2 messages to seq 2")
		})
	}
}

// A restored stream's change must state its reading after the restore: one that states the
// archive and no reading (a zero reading for an archive that holds messages) is refused, so a
// caller that forgets it fails closed. Control: 17f5b356b0, which had no reading to check.
func TestThePostcheckRefusesARestoreWithNoReadingAfterIt(t *testing.T) {
	was := StreamReading{Name: "KV_roles", Created: time.Unix(1, 0), LastSeq: 2, Messages: 2}
	now := StreamReading{Name: "KV_roles", Created: time.Unix(2, 0), LastSeq: 2, Messages: 2}
	err := compare(now.Name, was, now, Change{Kind: ChangeRestored, Archive: ArchiveState{LastSeq: 2, Messages: 2}})
	var refused *Refusal
	require.ErrorAs(t, err, &refused)
	require.ErrorContains(t, err, "0 messages to seq 0 right after the restore")
}
