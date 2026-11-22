package buckets

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/infra/writerhold"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/internal/testnats"
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

	member, config := excludeOneMember(t, nc, js, h.s.Cluster.Servers)

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

// excludeOneMember reloads the last of members with the server tag "!jetstream"
// (jsExcludePlacement), which takes it out of placement, and makes a three-copy create of
// KV_envoy_interests as the operator on nc until JetStream refuses it with code 10005, so the plant
// has taken; a create placed before the tag took is deleted again, so the bucket is absent after it.
// It answers the member and its configuration, for the reload that lifts the tag.
func excludeOneMember(t *testing.T, nc *natsgo.Conn, js natsgo.JetStreamContext, members []*testnats.Server) (*testnats.Server, string) {
	t.Helper()
	member := members[len(members)-1]
	config := member.Config(t)
	if err := member.Reload(t, config+"\nserver_tags: [\"!jetstream\"]\n"); err != nil {
		t.Fatalf("setup: reload with the exclude tag: %v", err)
	}
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); {
		_, err := bus.CreateCopiedBucket(context.Background(), nc, js, bus.InterestBucket, 3)
		switch {
		case err == nil:
			if err := js.DeleteStream("KV_" + bus.InterestBucket); err != nil {
				t.Fatalf("setup: delete the probe placed before the tag took: %v", err)
			}
			time.Sleep(250 * time.Millisecond)
		case writerhold.PlacementRefused(err):
			t.Logf("setup: a three-copy create is refused: %v", err)
			return member, config
		default:
			t.Fatalf("setup: the probe create: %v", err)
		}
	}
	t.Fatal("setup: no three-copy create was refused within a minute of the exclude tag")
	return nil, ""
}

// A restore JetStream never places (one member of three out of placement, never put back) runs
// against one placement limit across its two runs, and the limit passing is UNKNOWN naming the
// restore and code 10005: no run is called stopped part way, and no leftover is searched for, since
// the server stored nothing. With a 4 s limit the call ends within the limit and one attempt. The
// reviewer's case (round 3's L-2): at 870ce0c500 each run took a full limit and then a 20 s search
// for a leftover, 48.1 s in all with two "stopped part way" lines. Control: 870ce0c500.
func TestHoldARestoreJetStreamNeverPlacesStopsAtOneLimit(t *testing.T) {
	h := newHeldBuckets(t)
	ctx := context.Background()
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsRestore)
	h.s.HoldWithoutTheSteps(t, id)
	nc, err := h.s.ConnectOperator(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	dir := t.TempDir()
	if _, err := Backup(ctx, nc, dir, []string{bus.InterestBucket}, &bytes.Buffer{}); err != nil {
		t.Fatalf("setup: backup: %v", err)
	}
	archive, err := readArchive(filepath.Join(dir, bus.InterestBucket))
	if err != nil {
		t.Fatal(err)
	}
	js, err := nc.JetStream(natsgo.MaxWait(10 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	member, config := excludeOneMember(t, nc, js, h.s.Cluster.Servers)
	t.Cleanup(func() { _ = member.Reload(t, config) })

	const limit = 4 * time.Second
	var out bytes.Buffer
	start := time.Now()
	_, err = restoreOne(ctx, nc, &out, bus.InterestBucket, filepath.Join(dir, bus.InterestBucket), archive, limit)
	took := time.Since(start)
	t.Logf("restoreOne with a %s limit took %s: %v\n%s", limit, took, err, out.String())
	var unknown *writerhold.Unknown
	if !errors.As(err, &unknown) || !strings.Contains(err.Error(), "code 10005") || !strings.Contains(err.Error(), "restore KV_"+bus.InterestBucket) {
		t.Fatalf("restoreOne past its placement limit = %v, want UNKNOWN naming the restore and code 10005", err)
	}
	if strings.Contains(out.String(), "stopped part way") {
		t.Fatalf("a placement refusal was called a cut restore:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "JetStream could not place it yet (attempt 2)") {
		t.Fatalf("the restore was not sent again within its limit:\n%s", out.String())
	}
	if bound := limit + restoreAttempt; took > bound {
		t.Fatalf("restoreOne took %s, past its %s limit and one attempt (%s)", took, limit, bound)
	}
}

// restoreAttempt bounds one restore start JetStream refuses to place, in the row above: the
// server's answer to one request, with the stack under load.
const restoreAttempt = 10 * time.Second
