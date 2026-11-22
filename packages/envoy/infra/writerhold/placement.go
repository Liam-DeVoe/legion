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
// the refusal. Within bound (the restart's limit, PodRestartLimit when zero) it answers fn's first
// other answer at once, nil or any other error. Past bound it is UNKNOWN, naming op, the code and
// the last refusal, whose text an operator reads: JetStream answers the same code for a stream no
// retry can place, such as more copies than the cluster has members. `envoy-infra buckets create`,
// `recreate` and `restore` run their creates through it, and `streams update` its whole run.
func RetryPlacement(ctx context.Context, bound time.Duration, op string, out io.Writer, fn func() error) error {
	if bound == 0 {
		bound = PodRestartLimit
	}
	within, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil || !PlacementRefused(err) {
			return err
		}
		fmt.Fprintf(out, "%s: JetStream could not place it yet (attempt %d): %v; trying again\n", op, attempt, err)
		select {
		case <-within.Done():
			return unknownf(StepPostcheck, err, "%s: JetStream refused to place it (code %d) for %s, the restart's limit; a refusal that outlasts it may be one no retry mends, such as more copies than the cluster has members",
				op, placementCode, bound)
		case <-time.After(restartPoll):
		}
	}
}
