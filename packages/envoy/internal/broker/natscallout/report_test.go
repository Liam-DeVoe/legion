package natscallout

import (
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/testnats"
)

// blockingLog stops every write once stalled is set, and counts the writes waiting on it.
type blockingLog struct {
	stalled  atomic.Bool
	blocked  atomic.Int32
	released chan struct{}
}

func (l *blockingLog) Write(p []byte) (int, error) {
	if l.stalled.Load() {
		l.blocked.Add(1)
		defer l.blocked.Add(-1)
		<-l.released
	}
	return len(p), nil
}

// A stalled log grows nothing past its bound: while it takes no write, fifty refusals are each
// answered at once, the one write waiting on the log is the reporter's, and what the service keeps
// for the log meanwhile is a count per label and one detail, not a line, a goroutine or a buffer
// per refusal. Written where it is made, each refusal's line would be a write of its own waiting
// on the log, holding its goroutine and its answer.
func TestAStalledLogHoldsOneWriteAndCountsTheRest(t *testing.T) {
	withLiveness(t, 10*time.Millisecond, 10*time.Second)
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)
	log := &blockingLog{released: make(chan struct{})}
	service := startService(t, url, keys, Deps{Log: slog.New(slog.NewTextHandler(log, nil))}, Decide)
	t.Cleanup(func() { close(log.released) })
	log.stalled.Store(true)

	for range 50 {
		requireRefusedAtOnce(t, url, "next")
	}
	time.Sleep(100 * time.Millisecond)
	require.LessOrEqual(t, log.blocked.Load(), int32(1), "more than the reporter's one write waits on the stalled log")
	refusals := service.answer.refusals
	refusals.mu.Lock()
	defer refusals.mu.Unlock()
	require.LessOrEqual(t, len(refusals.counts), 1, "the refusals since the last line are kept by more than their one label: %v", refusals.counts)
}
