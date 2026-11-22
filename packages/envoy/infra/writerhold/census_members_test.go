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
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/natsgrant"
)

// With a stream's group leaderless, every member that knows the group answers STREAM.INFO itself
// (nats-server jetstream_api.go:2100-2134): a member with the stream answers it, and one without a
// local copy answers 10059 (2165-2178). So the census accepts a declared-absent name that
// STREAM.LIST answers missing only when no reply within the request's window finds the stream: a
// member that holds it (42 messages, a source) answering 100 ms after one that does not makes the
// census UNKNOWN, whichever answers first. A dead restore's leftover has no stream on any member,
// so every member answers 10059, and the census passes. The reviewer's case (round 6, Minor 1);
// control: 17f5b356b0's census, which took the first reply and passed with the holding member
// second.
func TestTheCensusAcceptsAMissingNameOnlyWhenNoMemberFindsIt(t *testing.T) {
	leftover := kvStreamPrefix + bus.SessionConsumersBucket
	sessions := kvStreamPrefix + bus.SessionBucket
	notFound, err := json.Marshal(map[string]any{"error": map[string]any{"code": 404, "err_code": jetstream.JSErrCodeStreamNotFound, "description": "stream not found"}})
	require.NoError(t, err)
	found, err := json.Marshal(jetstream.StreamInfo{Config: jetstream.StreamConfig{Name: leftover, Sources: []*jetstream.StreamSource{{Name: "ELSEWHERE"}}},
		State: jetstream.StreamState{Msgs: 42, LastSeq: 42}})
	require.NoError(t, err)
	type member struct {
		answer []byte
		delay  time.Duration
	}
	for _, row := range []struct {
		name    string
		members []member
		passes  bool
	}{
		{"control: no member has the stream", []member{{notFound, 0}, {notFound, 100 * time.Millisecond}}, true},
		{"a member without the stream answers first", []member{{notFound, 0}, {found, 100 * time.Millisecond}}, false},
		{"the member with the stream answers first", []member{{found, 0}, {notFound, 100 * time.Millisecond}}, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			ctx := t.Context()
			_, url := testnats.StartConfig(t, "", nil, true)
			lists := newListResponder(t, url)
			lists.mu.Lock()
			lists.streams = []jetstream.StreamInfo{
				{Config: jetstream.StreamConfig{Name: leftover, Subjects: []string{"$KV." + bus.SessionConsumersBucket + ".>"}}},
				{Config: jetstream.StreamConfig{Name: sessions, Subjects: []string{"$KV." + bus.SessionBucket + ".>"}}},
			}
			lists.mu.Unlock()
			lists.set(func(subject string, _ int, page map[string]any) {
				if subject != natsserver.JSApiStreamList {
					return
				}
				kept := []any{}
				for _, entry := range page["streams"].([]any) {
					if entry.(jetstream.StreamInfo).Config.Name == leftover {
						page["missing"] = []string{leftover}
						continue
					}
					kept = append(kept, entry)
				}
				page["streams"] = kept
			})
			for _, m := range row.members {
				conn, err := testnats.ConnectSeed(t, url, "")
				require.NoError(t, err)
				t.Cleanup(conn.Close)
				_, err = conn.Subscribe(strings.Replace(natsserver.JSApiStreamInfoT, "%s", "*", 1), func(msg *nats.Msg) {
					time.Sleep(m.delay)
					_ = msg.Respond(m.answer)
				})
				require.NoError(t, err)
				require.NoError(t, conn.Flush())
			}
			census, err := testnats.ConnectSeed(t, url, "", nats.CustomInboxPrefix(natsgrant.OperatorInbox))
			require.NoError(t, err)
			t.Cleanup(census.Close)

			listing, err := TakeCensus(ctx, census, []string{sessions}, MissingAccepted(holdrecord.WorkflowSelectionRecovery))
			if row.passes {
				require.NoError(t, err, "the census with every member answering not found")
				require.Len(t, listing.Streams, 1)
				return
			}
			requireUnknown(t, err, StepCensus, leftover, "present")
		})
	}
}
