package buckets

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/infra/writerhold"
)

// shortRequestBound sets the restore's request bound to bound for the test.
func shortRequestBound(t *testing.T, bound time.Duration) {
	t.Helper()
	saved := restoreRequest
	restoreRequest = bound
	t.Cleanup(func() { restoreRequest = saved })
}

// A restore's delete whose answer never comes is sent once and not again; the stream is read back
// after the restore's request bound. Here the delete reaches the hub, which deletes the stream, and
// only its answer is kept from the command: the read finds the stream gone, and the restore goes on.
func TestARestoresUnansweredDeleteThatLandedReadsTheStreamGone(t *testing.T) {
	const bound = 3 * time.Second
	shortRequestBound(t, bound)
	stream := "KV_delete_landed"
	relay := &answerRelay{dropAnswer: true}
	relayed, direct := relayedStream(t, stream, relay)

	start := time.Now()
	err := deleteNow(context.Background(), relayed, stream, "delete the leftover of a cut restore of "+stream)
	took := time.Since(start)
	if err != nil {
		t.Fatalf("a delete that landed, its answer kept back = %v, want the restore to go on", err)
	}
	if took < bound {
		t.Fatalf("the delete returned after %s, before its %s bound: the answer was not kept back", took.Round(time.Millisecond), bound)
	}
	if sent := relay.requests.Load(); sent != 1 {
		t.Fatalf("the delete was sent %d times, want once", sent)
	}
	if streamThere(t, direct, stream) {
		t.Fatalf("%s is still there", stream)
	}
}

// A restore's delete that never reaches the hub, so no answer comes and the stream stays, is
// UNKNOWN naming the stream once the read back finds it still there, and the stop is one that may
// have left the stream (leftoverRemains), which restoreAll records as perhaps left. The delete is
// sent once.
func TestARestoresUnansweredDeleteOfAStreamStillThereIsUnknown(t *testing.T) {
	const bound = 3 * time.Second
	shortRequestBound(t, bound)
	stream := "KV_delete_lost"
	relay := &answerRelay{dropRequest: true}
	relayed, direct := relayedStream(t, stream, relay)

	err := deleteNow(context.Background(), relayed, stream, "delete the leftover of a cut restore of "+stream)
	var unknown *writerhold.Unknown
	if !errors.As(err, &unknown) || !strings.Contains(err.Error(), stream+" is still there") {
		t.Fatalf("a delete with no answer of a stream still there = %v, want UNKNOWN naming %s still there", err, stream)
	}
	var remains *leftoverRemains
	if !errors.As(err, &remains) {
		t.Fatalf("the UNKNOWN delete %v is not marked as one that may have left the stream", err)
	}
	if sent := relay.requests.Load(); sent != 1 {
		t.Fatalf("the delete was sent %d times, want once", sent)
	}
	if !streamThere(t, direct, stream) {
		t.Fatalf("%s is gone: the relay let the delete through", stream)
	}
}
