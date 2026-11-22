package writerhold

import (
	"encoding/json"
	"strings"
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/natsgrant"
)

// A `create-bucket --after-loss` that dies during its snapshot restore leaves the restore's stream
// assignment: STREAM.NAMES names KV_envoy_session_consumers, STREAM.LIST counts it in its total
// and answers it `missing` (no stream answers its info), and STREAM.INFO answers it absent (t-7's
// deep review round 3, H-1). The writer census accepts such a `missing` entry only when its caller
// declares the stream absent (MissingAccepted: the selection-recovery workflow's selection
// bucket) and STREAM.INFO answers it absent; any other `missing` entry stays UNKNOWN. So, with
// that leftover:
//   - with the selection-recovery workflow's set, the census passes and lists the other streams;
//   - with no set (every other workflow), it is UNKNOWN, naming the missing entry;
//   - with the set but STREAM.INFO answering the stream present, it is UNKNOWN;
//   - a missing entry of another stream, which STREAM.INFO answers absent too, is UNKNOWN with the
//     set.
//
// Control: 31a8f84577's census, which took any `missing` entry as a list that is not whole, so the
// first case was UNKNOWN too.
func TestTheCensusAcceptsADeclaredAbsentStreamThatStreamListAnswersMissing(t *testing.T) {
	ctx := t.Context()
	_, url := testnats.StartConfig(t, "", nil, true)
	lists := newListResponder(t, url)
	leftover := kvStreamPrefix + bus.SessionConsumersBucket
	sessions := kvStreamPrefix + bus.SessionBucket
	lists.mu.Lock()
	lists.streams = []jetstream.StreamInfo{
		{Config: jetstream.StreamConfig{Name: "WORK", Subjects: []string{"work.>"}}},
		{Config: jetstream.StreamConfig{Name: leftover, Subjects: []string{"$KV." + bus.SessionConsumersBucket + ".>"}}},
		{Config: jetstream.StreamConfig{Name: sessions, Subjects: []string{"$KV." + bus.SessionBucket + ".>"}}},
	}
	lists.mu.Unlock()
	// STREAM.LIST answers missingName missing on its page: no stream info, the total unchanged.
	missingOf := func(missingName string) func(string, int, map[string]any) {
		return func(subject string, _ int, page map[string]any) {
			if subject != natsserver.JSApiStreamList {
				return
			}
			kept := []any{}
			for _, entry := range page["streams"].([]any) {
				if entry.(jetstream.StreamInfo).Config.Name == missingName {
					page["missing"] = []string{missingName}
					continue
				}
				kept = append(kept, entry)
			}
			page["streams"] = kept
		}
	}

	// STREAM.INFO answers the leftover absent, or present when present is set, and every other
	// name as the list holds it, WORK absent too for the last case.
	present := false
	infos, err := testnats.ConnectSeed(t, url, "")
	require.NoError(t, err)
	t.Cleanup(infos.Close)
	notFound := map[string]any{"error": map[string]any{"code": 404, "err_code": jetstream.JSErrCodeStreamNotFound, "description": "stream not found"}}
	_, err = infos.Subscribe(strings.Replace(natsserver.JSApiStreamInfoT, "%s", "*", 1), func(msg *nats.Msg) {
		name := strings.TrimPrefix(msg.Subject, strings.Replace(natsserver.JSApiStreamInfoT, "%s", "", 1))
		answer := any(notFound)
		if name == leftover && present {
			answer = jetstream.StreamInfo{Config: jetstream.StreamConfig{Name: leftover}}
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

	declared := MissingAccepted(holdrecord.WorkflowSelectionRecovery)
	require.Equal(t, []string{leftover}, declared)
	for _, w := range []holdrecord.Workflow{holdrecord.WorkflowCutover, holdrecord.WorkflowBucketsCreate, holdrecord.WorkflowHubRestore} {
		require.Empty(t, MissingAccepted(w), "workflow %s accepts a missing entry", w)
	}

	lists.set(missingOf(leftover))
	listing, err := TakeCensus(ctx, census, []string{sessions}, declared)
	require.NoError(t, err, "the census with the leftover declared absent and answered absent")
	var listed []string
	for _, info := range listing.Streams {
		listed = append(listed, info.Config.Name)
	}
	require.Equal(t, []string{"WORK", sessions}, listed)

	_, err = TakeCensus(ctx, census, []string{sessions}, nil)
	requireUnknown(t, err, StepCensus, natsserver.JSApiStreamList, "missing ["+leftover+"]")

	present = true
	_, err = TakeCensus(ctx, census, []string{sessions}, declared)
	requireUnknown(t, err, StepCensus, leftover, "present")
	present = false

	lists.set(missingOf("WORK"))
	_, err = TakeCensus(ctx, census, []string{sessions}, declared)
	requireUnknown(t, err, StepCensus, natsserver.JSApiStreamList, "missing [WORK]")
}
