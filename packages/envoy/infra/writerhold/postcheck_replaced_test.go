package writerhold

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A restored stream that the command then writes to holds the archive's messages plus its writes,
// less the writes that replaced a key the archive holds: a KV put to a key already there raises
// last_seq by one and leaves the message count as it was, as `buckets create`'s sentinel does when
// it replaces the archive's (t-7). So for a restored stream last_seq is the archive's plus Writes,
// and the message count is the archive's plus Writes less Replaced. A restore followed by one new
// key and one replaced key passes; the same with the message count off by one is refused naming
// both counts. Control: 976298e263's rule, message count = archive's plus Writes, which refuses the
// first case.
func TestThePostcheckAccountsARestoredStreamsReplacedKeys(t *testing.T) {
	archive := ArchiveState{LastSeq: 10, Messages: 8}
	was := StreamReading{Name: "KV_envoy_roles", Created: time.Unix(1, 0), LastSeq: 10, Messages: 8}
	change := Change{Kind: ChangeRestored, Writes: 2, Replaced: 1, Archive: archive}

	now := StreamReading{Name: "KV_envoy_roles", Created: time.Unix(2, 0), LastSeq: 12, Messages: 9}
	require.NoError(t, compare(now.Name, was, now, change), "a restore, one new key and one replaced key")

	now.Messages = 10
	err := compare(now.Name, was, now, change)
	var refused *Refusal
	require.ErrorAs(t, err, &refused, "a message count off by one")
	require.ErrorContains(t, err, "holds 10 messages")
	require.ErrorContains(t, err, "1 replaced")
	require.ErrorContains(t, err, "is 9")
}

// A command that states more replaced keys than writes to a stream is refused before any stream is
// read: a replaced key is one of the writes. Control: 976298e263, which had no Replaced to check.
func TestThePostcheckRefusesMoreReplacedKeysThanWrites(t *testing.T) {
	step1 := []StreamReading{{Name: "KV_envoy_roles", Created: time.Unix(1, 0), LastSeq: 10, Messages: 8}}
	changes := map[string]Change{"KV_envoy_roles": {Kind: ChangeRestored, Writes: 1, Replaced: 2, Archive: ArchiveState{LastSeq: 10, Messages: 8}}}
	_, err := Postcheck(context.Background(), nil, step1, changes)
	var refused *Refusal
	require.ErrorAs(t, err, &refused)
	require.ErrorContains(t, err, "2 replaced")
	require.ErrorContains(t, err, "1 writes")
}
