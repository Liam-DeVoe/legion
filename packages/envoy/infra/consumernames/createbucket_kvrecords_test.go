package consumernames

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus/kvrecords"
)

// The agreement of the walk backup's restore rule (kvrecords.ForRestore, which create-bucket
// --after-loss --backup reads a walk through, createBucketWalkFrom) with the selection records'
// keys: a listener durable's selection key (selection.DurableKey) starts with kvrecords'
// DurableKeyPrefix, so ForRestore keeps it, as it keeps every undotted key, and the durable's record
// comes back with a recovery from a walk; the sentinel (selection.SentinelKey) and this package's
// barrier key (hold.barrier.<hold id>) are skipped. Control: a dotted key that is neither the
// sentinel, a hold's barrier nor a durable key is refused by ForRestore, naming it, so the rule can
// refuse.
func TestTheWalksRestoreKeepsTheListenerDurablesRecords(t *testing.T) {
	prefix := "$KV." + selection.SessionBucket + "."
	key := selection.DurableKey("d1")
	require.True(t, strings.HasPrefix(key, kvrecords.DurableKeyPrefix), "%s does not start with kvrecords.DurableKeyPrefix %q", key, kvrecords.DurableKeyPrefix)
	kept, err := kvrecords.ForRestore([]kvrecords.Record{
		{Seq: 1, Subject: prefix + selection.SentinelKey, Key: selection.SentinelKey},
		{Seq: 2, Subject: prefix + key, Key: key},
		{Seq: 3, Subject: prefix + "hold.barrier.h1", Key: "hold.barrier.h1"},
		{Seq: 4, Subject: prefix + "ses_a", Key: "ses_a"},
	})
	require.NoError(t, err)
	var keys []string
	for _, record := range kept {
		keys = append(keys, record.Key)
	}
	require.Equal(t, []string{key, "ses_a"}, keys)

	_, err = kvrecords.ForRestore([]kvrecords.Record{{Seq: 5, Subject: prefix + "other.k", Key: "other.k"}})
	require.ErrorContains(t, err, "other.k", "control: ForRestore kept an unknown dotted key")
}
