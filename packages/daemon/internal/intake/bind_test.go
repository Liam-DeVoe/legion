package intake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// jsTemporarilyUnavailable is JetStream's error code 10008, "JetStream system temporarily
// unavailable", which a cluster member answers while its JetStream metadata is not ready, as on a
// loaded machine right after a consumer's create.
const jsTemporarilyUnavailable = 10008

// bindBound is how long a forced-order row's bind waits out 10008 (bindConsumer): 30 s, or what
// the test's deadline (go test -timeout) leaves less 5 s when that is sooner, so the row fails
// naming the bind before go test ends the binary.
func bindBound(t *testing.T) time.Duration {
	bound := 30 * time.Second
	if deadline, ok := t.Deadline(); ok {
		if left := time.Until(deadline) - 5*time.Second; left < bound {
			bound = max(left, 0)
		}
	}
	return bound
}

// bindConsumer binds name on stream, as a forced-order row binds a consumer it has just created
// (ackThrough). A cluster member answers JetStream's 10008 while its metadata is not ready, which a
// loaded machine shows right after a create, so the bind is sent again on 10008 every 100 ms until
// it is answered or bound has passed, and then returns the last answer. Any other error returns at
// once.
func bindConsumer(ctx context.Context, stream jetstream.Stream, name string, bound time.Duration) (jetstream.Consumer, error) {
	deadline := time.Now().Add(bound)
	for {
		consumer, err := stream.Consumer(ctx, name)
		var apiErr *jetstream.APIError
		if !errors.As(err, &apiErr) || apiErr.ErrorCode != jsTemporarilyUnavailable || !time.Now().Before(deadline) {
			return consumer, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// unavailableStream is a stream whose first fails binds answer JetStream's 10008, and whose
// later ones answer as stream does.
type unavailableStream struct {
	jetstream.Stream
	fails int
	binds int
}

func (s *unavailableStream) Consumer(ctx context.Context, name string) (jetstream.Consumer, error) {
	s.binds++
	if s.binds <= s.fails {
		return nil, &jetstream.APIError{Code: 503, ErrorCode: jsTemporarilyUnavailable, Description: "JetStream system temporarily unavailable"}
	}
	return s.Stream.Consumer(ctx, name)
}

// A forced-order row's bind of a consumer it has just created waits out a cluster's 10008, which
// a member answers under load before its metadata is ready (review round 8, R8-3: a three-server
// row failed on one bind's 503 err_code=10008), and fails on it only once its bound has passed.
// The bind is answered after two 10008s; one that answers 10008 every time returns it once the
// bound (here 300 ms) has passed, well before the test's own deadline; and another error, such
// as "consumer not found", returns at once. The control is the helper before this row, a single
// bind, which returned the first 10008.
func TestABindWaitsOutJetStreamsTemporaryUnavailability(t *testing.T) {
	js, stream, _ := freshJetStream(t)
	if _, err := stream.CreateConsumer(context.Background(), jetstream.ConsumerConfig{Durable: "bound-after-two-refusals", AckPolicy: jetstream.AckExplicitPolicy}); err != nil {
		t.Fatalf("create the consumer to bind: %v", err)
	}
	_ = js

	twice := &unavailableStream{Stream: stream, fails: 2}
	if _, err := bindConsumer(context.Background(), twice, "bound-after-two-refusals", 30*time.Second); err != nil {
		t.Fatalf("the bind answered after two 10008s = %v, want it bound", err)
	}
	if twice.binds != 3 {
		t.Fatalf("the bind was sent %d times, want 3: two refusals and the answer", twice.binds)
	}

	always := &unavailableStream{Stream: stream, fails: 1 << 30}
	started := time.Now()
	_, err := bindConsumer(context.Background(), always, "bound-after-two-refusals", 300*time.Millisecond)
	var apiErr *jetstream.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode != jsTemporarilyUnavailable {
		t.Fatalf("the bind that is refused every time = %v, want the last 10008 once its bound passed", err)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("the bind refused every time took %s, want it ended near its 300 ms bound", took)
	}

	other := &unavailableStream{Stream: stream}
	if _, err := bindConsumer(context.Background(), other, "no-such-consumer", 30*time.Second); !errors.Is(err, jetstream.ErrConsumerNotFound) {
		t.Fatalf("the bind of a missing consumer = %v, want consumer not found at once", err)
	}
	if other.binds != 1 {
		t.Fatalf("the bind of a missing consumer was sent %d times, want once", other.binds)
	}
}
