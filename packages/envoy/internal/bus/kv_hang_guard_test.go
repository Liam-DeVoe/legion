package bus

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/logtest"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// A start whose lookup the server never answers ends at the start's hang guard, with an error that
// names the lookup, the sends that drew no answer and the guard, rather than hanging the program's
// start for as long as the server stays silent. The guard is shortened here (ensureKeyValue), to
// two seconds: each lookup is a read, waited for up to its read limit (firstReadLimit, then twice
// that) and sent again, so the guard passes during the third send and cuts it (Bounded). Each
// lookup the server left unanswered while the guard had time left is followed by a WARN line that
// says it is sent again, and the lookup that follows it; the one the guard cut is followed by
// neither, so the lines are one fewer than the lookups, and the error names the limit of a lookup
// that reached it, not of the one the guard cut.
func TestABucketLookupTheServerNeverAnswersEndsAtTheHangGuard(t *testing.T) {
	logs := captureLogs(t)
	const bucket = "envoy_unanswered_never"
	relay := startUnansweredRelayOn(t, testnats.URL(t), openSubject(bucket))
	conn, js := relayedJetStream(t, relay)
	relay.arm(1 << 30)

	_, err := ensureKeyValue(context.Background(), 2*time.Second, conn, js, &nats.KeyValueConfig{Bucket: bucket, Storage: nats.MemoryStorage})
	if !errors.Is(err, errHangGuard) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a start whose lookup the server never answers = %v, want the start's hang guard, with context.DeadlineExceeded", err)
	}
	for _, want := range []string{"KV_" + bucket + " STREAM.INFO", "sends drew no answer when the wait ended", "no answer within ", "hang guard passed after 2s"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("a start whose lookup the server never answers = %v, want it to name %q", err, want)
		}
	}
	warns := logs.recordsWithMsg(t, "envoy nats stream request unanswered; sending it again")
	// The lookup the guard cut was sent before the guard passed; wait for it to reach the relay.
	testwait.Holds(time.Second, testwait.Poll, func() bool { return relay.sent() > len(warns) })
	sends := relay.sent()
	if sends < 2 {
		t.Fatalf("lookups sent = %d, want the lookup sent again after its read limit", sends)
	}
	if len(warns) != sends-1 {
		t.Fatalf("the start logged %d lines that a lookup is sent again for %d lookups sent, want %d: each line is followed by a lookup, and the lookup the hang guard cut by none", len(warns), sends, sends-1)
	}
	match := regexp.MustCompile(`no answer within ([0-9.]+[a-zµ]+)`).FindStringSubmatch(err.Error())
	if match == nil {
		t.Fatalf("a start whose lookup the server never answers = %v, want it to name the read limit of the last lookup that reached it", err)
	}
	if limit, parseErr := time.ParseDuration(match[1]); parseErr != nil || limit > 2*time.Second {
		t.Fatalf("a start whose lookup the server never answers = %v, names a read limit of %s, past the 2s guard: the limit of the lookup the guard cut, which never reached it, and not of one that did", err, match[1])
	}
}

// cancelOnUnansweredWarning ends a read run as the warning for its next send is emitted. It makes
// the race between the warning and Bounded's context check deterministic.
type cancelOnUnansweredWarning struct {
	cancel  context.CancelFunc
	request string
	warns   atomic.Int32
}

func (h *cancelOnUnansweredWarning) Enabled(context.Context, slog.Level) bool { return true }

func (h *cancelOnUnansweredWarning) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "envoy nats stream request unanswered; sending it again" {
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key != "request" || attr.Value.String() != h.request {
				return true
			}
			h.warns.Add(1)
			h.cancel()
			return false
		})
	}
	return nil
}

func (h *cancelOnUnansweredWarning) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *cancelOnUnansweredWarning) WithGroup(string) slog.Handler      { return h }

// A warning about another send is written from the function Bounded starts. When cancellation
// lands at that line, the send has already started; it is never a warning for a send Bounded
// declines to start.
func TestAReadAgainWarningOnlyOccursWhenTheSendStarts(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handler := &cancelOnUnansweredWarning{cancel: cancel, request: "warning-order STREAM.INFO"}
	logtest.SetDefault(t, handler)
	var sends atomic.Int32
	limit := &readLimit{first: 20 * time.Millisecond, limit: 20 * time.Millisecond}
	_, err := readAgain(ctx, "warning-order STREAM.INFO", limit, func() (int, error) {
		sends.Add(1)
		return 0, nats.ErrTimeout
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the canceled retry = %v, want context.Canceled", err)
	}
	// Bounded can return on the cancellation before the warned callback reaches read.
	testwait.Eventually(t, "the send whose warning canceled its wait to start", func() bool { return sends.Load() == 2 })
	if got, want := handler.warns.Load(), sends.Load()-1; got != want {
		t.Fatalf("warnings for another send = %d, sends = %d, want one fewer: a warning must follow the send it names", got, sends.Load())
	}
}

// heldUnansweredWarningError holds formatting of the first lost send's error past the next
// send's read limit, while readAgain replaces its last error and continues on another send.
type heldUnansweredWarningError struct {
	ctx     context.Context
	release <-chan struct{}
}

func (e heldUnansweredWarningError) Error() string {
	select {
	case <-e.release:
	case <-e.ctx.Done():
	}
	return "the first read drew no answer"
}

func (heldUnansweredWarningError) Unwrap() error { return nats.ErrTimeout }

// A warning can outlive its own read limit. Its error belongs to that send, not to the
// mutable last error of the caller that has already continued. The race detector also sees
// the unsynchronized read and replacement when the warning closes over that mutable error.
func TestAReadAgainWarningKeepsTheErrorOfItsPreviousSend(t *testing.T) {
	logs := captureLogs(t)
	ctx, cancel := context.WithTimeout(t.Context(), testwait.Bounded(t, time.Minute, time.Second))
	defer cancel()
	released := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	var sends atomic.Int32
	limit := &readLimit{first: firstReadLimit, limit: firstReadLimit}
	value, err := readAgain(ctx, "held-warning STREAM.INFO", limit, func() (int, error) {
		if sends.Add(1) == 1 {
			return 0, heldUnansweredWarningError{ctx: ctx, release: released}
		}
		return 42, nil
	})
	release()
	if err != nil || value != 42 {
		t.Fatalf("the read after the held warning = %d, %v, want 42 and no error", value, err)
	}
	testwait.Eventually(t, "the send whose warning outlived its read limit to finish", func() bool { return sends.Load() == 3 })
	for _, warn := range logs.recordsWithMsg(t, "envoy nats stream request unanswered; sending it again") {
		if warn["request"] == "held-warning STREAM.INFO" && warn["sent"] == float64(1) {
			if warn["error"] != "the first read drew no answer" {
				t.Fatalf("the first retry's error = %v, want the first read's error", warn["error"])
			}
			return
		}
	}
	t.Fatal("the retry whose warning was held logged no warning for its first lost send")
}
