package writerhold

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/natsgrant"
)

// The postcheck's writer census reads the account again while its answer is not whole, as the
// hold's own census does (wholeCensus): right after a command recreated a stream, a STREAM.LIST
// answer can still lack it while its STREAM.INFO finds it, which is a reading not whole, never a
// clean one and no refusal. Here the account's first STREAM.LIST and STREAM.NAMES answers lack
// KV_envoy_sessions and the next ones hold it; the postcheck reads again after censusPause and
// passes. Seen at 132256b2ad under load: `buckets recreate` in TestHoldBypassUnlistedMachine
// stopped UNKNOWN, "STREAM.LIST does not hold KV_envoy_sessions, which its STREAM.INFO read found
// present". Control: 132256b2ad, whose postcheck read the census once and was UNKNOWN.
func TestThePostcheckReadsTheCensusAgainWhileItIsNotWhole(t *testing.T) {
	ctx := t.Context()
	_, url := testnats.StartConfig(t, "", nil, true)
	lists := newListResponder(t, url)
	sessions := kvStreamPrefix + bus.SessionBucket
	created := time.Date(2026, 11, 22, 4, 0, 0, 0, time.UTC)
	info := jetstream.StreamInfo{
		Config:  jetstream.StreamConfig{Name: sessions, Subjects: []string{"$KV." + bus.SessionBucket + ".>"}},
		Created: created,
		State:   jetstream.StreamState{LastSeq: 5, Msgs: 5},
	}
	lists.mu.Lock()
	lists.streams = []jetstream.StreamInfo{info}
	lists.mu.Unlock()
	readings := 0
	lists.set(func(subject string, offset int, page map[string]any) {
		if subject == natsserver.JSApiStreamList && offset == 0 {
			readings++
		}
		if readings == 1 && (subject == natsserver.JSApiStreamList || subject == natsserver.JSApiStreams) {
			page["total"], page["streams"] = 0, []any{}
		}
	})

	// STREAM.INFO answers from the same streams: each protected stream but sessions is absent.
	infos, err := testnats.ConnectSeed(t, url, "")
	require.NoError(t, err)
	t.Cleanup(infos.Close)
	_, err = infos.Subscribe(strings.Replace(natsserver.JSApiStreamInfoT, "%s", "*", 1), func(msg *nats.Msg) {
		name := strings.TrimPrefix(msg.Subject, strings.Replace(natsserver.JSApiStreamInfoT, "%s", "", 1))
		answer := any(map[string]any{"error": map[string]any{"code": 404, "err_code": jetstream.JSErrCodeStreamNotFound, "description": "stream not found"}})
		if name == sessions {
			answer = info
		}
		body, err := json.Marshal(answer)
		if err != nil {
			panic(err)
		}
		_ = msg.Respond(body)
	})
	require.NoError(t, err)
	require.NoError(t, infos.Flush())

	census, err := testnats.ConnectSeed(t, url, "", nats.CustomInboxPrefix(natsgrant.OperatorInbox))
	require.NoError(t, err)
	t.Cleanup(census.Close)
	var step1 []StreamReading
	for _, name := range ProtectedStreams() {
		reading := StreamReading{Name: name, Absent: true}
		if name == sessions {
			reading = readingOf(&info)
		}
		step1 = append(step1, reading)
	}

	_, err = Postcheck(ctx, census, step1, nil)
	require.NoError(t, err, "the postcheck took a census not yet whole as its answer")
	lists.mu.Lock()
	defer lists.mu.Unlock()
	require.GreaterOrEqual(t, readings, 2, "the postcheck read the census once")
}
