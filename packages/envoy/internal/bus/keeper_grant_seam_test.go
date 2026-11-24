package bus

import (
	"testing"

	"github.com/nats-io/nats.go"
)

// keeperClockOption runs a keeper's no-grant backoff on clock: the client's own clock
// (WithReconnectErrClock), which the keeper reads (keepBuckets).
func keeperClockOption(clock *steppedClock) ConnectOption { return WithReconnectErrClock(clock.Now) }

// keeperBucketPass is a keeper's bucket pass (keepBuckets) over buckets at three copies, on js and
// clock, one call a pass, with the record of refusals a keeper keeps across its passes (keep).
func keeperBucketPass(t *testing.T, js nats.JetStreamContext, clock *steppedClock, buckets ...string) func() {
	t.Helper()
	client := &Client{name: "keeper-grant-test", stopCh: make(chan struct{}), reconnectErrClock: clock.Now}
	refused := refusedBuckets{}
	return func() { client.keepBuckets(t.Context(), js, buckets, 3, refused) }
}
