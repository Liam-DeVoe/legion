//go:build kvrecords

// The agreement of the walk backup's restore rule with the selection records' keys. It builds only
// with the tag kvrecords, until the package internal/bus/kvrecords, which writes and reads the
// released operator's walk (envoy-kv-records-v1), is in this module; then the tag goes, and
// create-bucket --after-loss reads its walk through kvrecords.Read and kvrecords.ForRestore.

package consumernames

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus/kvrecords"
)

// A listener durable's selection key (selection.DurableKey) starts with kvrecords'
// DurableKeyPrefix, so kvrecords.ForRestore keeps it, as it keeps every undotted key, and the
// durable's record comes back with a recovery from a walk. Control: a dotted key that is neither
// the sentinel, a hold's barrier nor a durable key is refused by ForRestore, naming it, so the rule
// can refuse.
func TestTheWalksRestoreKeepsTheListenerDurablesRecords(t *testing.T) {
	prefix := "$KV." + selection.SessionBucket + "."
	key := selection.DurableKey("d1")
	require.True(t, strings.HasPrefix(key, kvrecords.DurableKeyPrefix), "%s does not start with kvrecords.DurableKeyPrefix %q", key, kvrecords.DurableKeyPrefix)
	kept, err := kvrecords.ForRestore([]kvrecords.Record{
		{Seq: 1, Subject: prefix + selection.SentinelKey, Key: selection.SentinelKey},
		{Seq: 2, Subject: prefix + key, Key: key},
		{Seq: 3, Subject: prefix + "ses_a", Key: "ses_a"},
	})
	require.NoError(t, err)
	var keys []string
	for _, record := range kept {
		keys = append(keys, record.Key)
	}
	require.Equal(t, []string{key, "ses_a"}, keys)

	_, err = kvrecords.ForRestore([]kvrecords.Record{{Seq: 4, Subject: prefix + "other.k", Key: "other.k"}})
	require.ErrorContains(t, err, "other.k", "control: ForRestore kept an unknown dotted key")
}
