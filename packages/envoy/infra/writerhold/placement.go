package writerhold

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/nats-io/jsm.go"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// placementCode is JetStream's error code for a stream it cannot place (nats-server's
// JSClusterNoPeersErrF, "no suitable peers for placement"): too few peers for its copies, or a peer
// the meta leader still holds offline, as one is for a while after a hold's restart.
const placementCode = 10005

// PlacementRefused reports whether err is JetStream's refusal to place a stream (placementCode),
// read by its code in nats.go's two API errors or jsm.go's, never by its text.
func PlacementRefused(err error) bool {
	var legacy *nats.APIError
	if errors.As(err, &legacy) && legacy.ErrorCode == placementCode {
		return true
	}
	var current *jetstream.APIError
	if errors.As(err, &current) && current.ErrorCode == placementCode {
		return true
	}
	return jsm.IsNatsError(err, placementCode)
}

// RetryPlacement runs fn, a create of a stream or bucket after a hold's restart, and runs it again
// while JetStream refuses to place it (PlacementRefused): the operator holds no $SYS user, so
// nothing it can read says every restarted peer is back, and the create's own refusal is the only
// signal. Each retry waits the restart's poll (restartPoll) and prints one line on out naming op and
// the refusal. Within bound (the restart's limit, PlacementBound) it answers fn's first other
// answer at once, nil or any other error. Past bound it is UNKNOWN, naming op, the code and the last
// refusal, whose text an operator reads: JetStream answers the same code for a stream no retry can
// place, such as more copies than the cluster has members. `envoy-infra buckets create` and
// `recreate` run their creates through it, and `streams update` its whole run; `buckets restore`'s
// two runs share one limit (RetryPlacementUntil).
func RetryPlacement(ctx context.Context, bound time.Duration, op string, out io.Writer, fn func() error) error {
	return RetryPlacementUntil(ctx, time.Now().Add(PlacementBound(bound)), op, out, fn)
}

// PlacementBound is the placement limit of bound, the restart's limit: PodRestartLimit when zero.
func PlacementBound(bound time.Duration) time.Duration {
	if bound == 0 {
		return PodRestartLimit
	}
	return bound
}

// RetryPlacementUntil is RetryPlacement with its limit as a deadline, which several of a caller's
// creates share, each sent with what is left of it: its first attempt always runs, and past deadline
// a refusal is UNKNOWN as RetryPlacement's is.
func RetryPlacementUntil(ctx context.Context, deadline time.Time, op string, out io.Writer, fn func() error) error {
	within, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil || !PlacementRefused(err) {
			return err
		}
		fmt.Fprintf(out, "%s: JetStream could not place it yet (attempt %d): %v; trying again\n", op, attempt, err)
		select {
		case <-within.Done():
			if ctx.Err() != nil {
				return unknownf(StepPostcheck, ctx.Err(), "%s: the command's context ended while JetStream refused to place it (code %d): %v", op, placementCode, err)
			}
			return unknownf(StepPostcheck, err, "%s: JetStream refused to place it (code %d) until %s, the restart's limit; a refusal that outlasts it may be one no retry mends, such as more copies than the cluster has members",
				op, placementCode, deadline.UTC().Format(time.RFC3339))
		case <-time.After(restartPoll):
		}
	}
}
