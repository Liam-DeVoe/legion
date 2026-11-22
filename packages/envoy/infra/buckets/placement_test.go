package buckets

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/infra/writerhold"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/holdrecord"
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

// JetStream refusing to place a three-copy create (code 10005, JSClusterNoPeersErrF) makes `buckets
// create` make the create again (writerhold.RetryPlacement), and the create in a set hold is done
// once placement is possible again. The refusal is the real server's, planted on one member by a
// config reload with the server tag "!jetstream" (jsExcludePlacement), which takes the member out of
// placement; a three-copy create of the bucket itself, as the operator (under a held row no other
// client connects), refused with that code, shows the plant took before the command runs, and one
// placed before the plant took is deleted again. When the command prints its first retry, the tag
// is lifted by another reload, and the create must then succeed.
//
// The reason differs from the race this guards: after a hold's restart, nats-server 2.15.1 refuses
// a placement for a member it holds no stats for yet ("peer offline", jetstream_cluster.go
// 10763-10768); here it refuses for the exclude tag. A stopped member cannot plant that race: the
// leader keeps its stats, so placement still selects it. The code is what matters, since
// RetryPlacement keys on the code alone, and both reasons answer the same code.
//
// The control is the create's one attempt, as it stood at 599c9838f2 (a go test -overlay copy of
// placement.go whose RetryPlacement runs fn once), under which the command fails with that code.
func TestHoldACreateJetStreamCannotPlaceIsMadeOncePlacementIsPossible(t *testing.T) {
	h := newHeldBuckets(t)
	ctx := context.Background()
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	h.s.HoldWithoutTheSteps(t, id)

	// The bucket the create makes: deleted inside the hold, as a lost bucket would be.
	nc, err := h.s.ConnectOperator(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream(natsgo.MaxWait(10 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := js.DeleteStream("KV_" + bus.InterestBucket); err != nil {
		t.Fatalf("setup: delete KV_%s: %v", bus.InterestBucket, err)
	}

	// One member taken out of placement by the exclude tag, until a probe create is refused.
	member := h.s.Cluster.Servers[len(h.s.Cluster.Servers)-1]
	config := member.Config(t)
	if err := member.Reload(t, config+"\nserver_tags: [\"!jetstream\"]\n"); err != nil {
		t.Fatalf("setup: reload with the exclude tag: %v", err)
	}
	planted := false
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline) && !planted; {
		_, err := bus.CreateCopiedBucket(ctx, nc, js, bus.InterestBucket, 3)
		switch {
		case err == nil:
			if err := js.DeleteStream("KV_" + bus.InterestBucket); err != nil {
				t.Fatalf("setup: delete the probe placed before the tag took: %v", err)
			}
			time.Sleep(250 * time.Millisecond)
		case writerhold.PlacementRefused(err):
			planted = true
			t.Logf("setup: a three-copy create is refused: %v", err)
		default:
			t.Fatalf("setup: the probe create: %v", err)
		}
	}
	if !planted {
		t.Fatal("setup: no three-copy create was refused within a minute of the exclude tag")
	}

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

	args := []string{"create", "--hold", id, "--bucket", bus.InterestBucket, "--broker-url", "https://broker.holdtest",
		"--token-file", "/dev/null", "--verify-keys", "AAAA", "--signing-key-secret", "projects/holdtest/secrets/" + writerhold.SigningKeySecret,
		"--nats", h.s.Cluster.URL(), "--reason", "a create JetStream cannot place yet"}
	var stderr bytes.Buffer
	code := CommandOn(ctx, args, stdout, &stderr, h.d)
	t.Logf("envoy-infra buckets create: exit %d\n%s%s", code, stdout.String(), stderr.String())
	if code != 0 {
		t.Fatalf("buckets create while placement is refused = %d, want 0\n%s%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "JetStream could not place it yet") {
		t.Fatalf("the create was placed at its first attempt, so the refusal was not planted:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "created "+bus.InterestBucket) {
		t.Fatalf("the create's output does not name the bucket made:\n%s", stdout.String())
	}
	<-lifted
	if _, err := js.StreamInfo("KV_"+bus.InterestBucket, natsgo.MaxWait(30*time.Second)); err != nil {
		t.Fatalf("KV_%s after the create: %v", bus.InterestBucket, err)
	}
}
