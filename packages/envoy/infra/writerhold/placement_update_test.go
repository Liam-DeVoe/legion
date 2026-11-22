package writerhold

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/internal/streamset"
)

// signalWriter is a command's stdout that closes seen once a line holds mark.
type signalWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	mark string
	seen chan struct{}
	once sync.Once
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if strings.Contains(w.buf.String(), w.mark) {
		w.once.Do(func() { close(w.seen) })
	}
	return n, err
}

func (w *signalWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// updateHold is a streams-update hold set without its steps, with ENVOY_NOTIFICATIONS deleted
// inside it, as a lost stream would be, so the update's first change is its create; and the
// operator's JetStream, the one client a held row admits.
func (h *heldStack) updateHold() (string, nats.JetStreamContext) {
	h.t.Helper()
	ctx := context.Background()
	id := h.s.Prepare(h.t, holdrecord.WorkflowStreamsUpdate)
	h.s.HoldWithoutTheSteps(h.t, id)
	nc, err := h.s.ConnectOperator(ctx)
	require.NoError(h.t, err)
	h.t.Cleanup(nc.Close)
	js, err := nc.JetStream(nats.MaxWait(10 * time.Second))
	require.NoError(h.t, err)
	require.NoError(h.t, js.DeleteStream(bus.Stream))
	return id, js
}

// JetStream refusing to place a three-copy create (code 10005, JSClusterNoPeersErrF) makes `streams
// update` send its run again (RetryPlacement), and the update in its hold is done once placement is
// possible again. The refusal is the real server's, planted on one member by a config reload with
// the server tag "!jetstream" (jsExcludePlacement), which takes the member out of placement; a
// three-copy create of ENVOY_NOTIFICATIONS as the operator, refused with that code, shows the plant
// took before the command runs, and one placed before the plant took is deleted again. When the
// command prints its first retry, the tag is lifted by another reload, and the update must then
// make the stream and be done.
//
// Red before the update went through RetryPlacement: its one attempt failed with that code (exit 1).
func TestHoldAnUpdateJetStreamCannotPlaceYetIsMadeOncePlacementIsPossible(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	ctx := context.Background()
	id, js := h.updateHold()
	cluster := h.s.Cluster // its members, for the plant
	member := cluster.Servers[len(cluster.Servers)-1]
	config := member.Config(t)
	require.NoError(t, member.Reload(t, config+"\nserver_tags: [\"!jetstream\"]\n"), "setup: reload with the exclude tag")
	notifications := streamset.Declared(streamset.Settings{Replicas: 3})[0]
	require.Equal(t, bus.Stream, notifications.Name)
	conn, err := h.s.ConnectOperator(ctx)
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	planted := false
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline) && !planted; {
		err := streamset.Ensure(ctx, conn, js, notifications)
		switch {
		case err == nil:
			require.NoError(t, js.DeleteStream(bus.Stream), "setup: delete the probe placed before the tag took")
			time.Sleep(250 * time.Millisecond)
		case PlacementRefused(err):
			planted = true
			t.Logf("setup: a three-copy create is refused: %v", err)
		default:
			t.Fatalf("setup: the probe create: %v", err)
		}
	}
	require.True(t, planted, "setup: no three-copy create was refused within a minute of the exclude tag")

	stdout := &signalWriter{mark: "JetStream could not place it yet", seen: make(chan struct{})}
	lifted := make(chan struct{})
	go func() {
		defer close(lifted)
		select {
		case <-stdout.seen:
		case <-time.After(time.Minute):
		}
		if err := member.Reload(t, config); err != nil {
			t.Errorf("lift the exclude tag: %v", err)
		}
	}()
	t.Cleanup(func() { <-lifted })

	var stderr bytes.Buffer
	code := StreamsCommandOn(ctx, h.streamsArgs("update", "--hold", id), stdout, &stderr, h.d)
	t.Logf("envoy-infra streams update: exit %d\n%s%s", code, stdout.String(), stderr.String())
	require.Zero(t, code, "streams update while placement is refused")
	require.Contains(t, stdout.String(), "JetStream could not place it yet", "the create was placed at its first attempt, so the refusal was not planted")
	require.Contains(t, stdout.String(), "CHANGED "+bus.Stream+": made")
	require.Contains(t, stdout.String(), "streams update done in hold")
	require.Contains(t, h.journalCommands(id), CommandStreamsUpdate+" done")
	<-lifted
	_, err = js.StreamInfo(bus.Stream, nats.MaxWait(30*time.Second))
	require.NoError(t, err, "%s after the update", bus.Stream)
}

// A create JetStream refuses with the same code for a reason no retry mends, five copies asked of
// a three-member cluster, is sent again until the restart's limit and then stops the update as
// UNKNOWN (exit 2), naming the command, the code and the last refusal's text, so an operator can
// tell it from a restarted peer still held offline. The update writes a stop, no done entry, and
// makes nothing. Red before the update went through RetryPlacement: one attempt, a FAIL (exit 1)
// that named no code.
func TestHoldAnUpdateJetStreamCanNeverPlaceStopsUnknownNamingTheCode(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	id, js := h.updateHold()
	h.op.RestartLimit = 3 * time.Second
	t.Cleanup(func() { h.op.RestartLimit = 3 * time.Minute })

	code, stdout, stderr := h.streamsRun(nil, "update", "--hold", id, "--replicas", "5")
	require.Equal(t, 2, code, "an update whose create can never be placed")
	require.Contains(t, stderr, "UNKNOWN")
	require.Contains(t, stderr, "streams update")
	require.Contains(t, stderr, "code 10005")
	require.Contains(t, stderr, "no suitable peers for placement", "the last refusal's text")
	require.Contains(t, stdout, "JetStream could not place it yet (attempt 2)", "the create was not sent again")
	require.NotContains(t, stdout, "CLEAN")
	require.NotContains(t, h.journalCommands(id), CommandStreamsUpdate+" done")
	require.Equal(t, holdrecord.StateStop, latestState(h.journal(id), CommandStreamsUpdate))
	_, err := js.StreamInfo(bus.Stream)
	require.ErrorIs(t, err, nats.ErrStreamNotFound, "a stream no member set could hold was made")
}
