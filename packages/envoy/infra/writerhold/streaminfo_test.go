package writerhold

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/natsgrant"
)

// StreamInfo sends a STREAM.INFO again while nothing answers it: a clustered stream's info is
// answered by its group's leader alone, so a read sent while the group elects one draws no answer.
// TestHoldPreflightAnInterruptedRestore's buckets-create preflight once read KV_envoy_roles once,
// drew no answer at a load average near 800, and stopped UNKNOWN ("read KV_envoy_roles: context
// deadline exceeded"). Scripted on a plain server: the first read unanswered, the second answered,
// so StreamInfo returns the stream at its second read. The control is the one read
// (streamInfoOnce) of the same unanswered group, which fails. A group that never answers is the
// error at the bound.
func TestStreamInfoIsSentAgainUntilAnswered(t *testing.T) {
	_, url := testnats.StartConfig(t, "", nil, true)
	nc, err := testnats.ConnectSeed(t, url, "", nats.CustomInboxPrefix(natsgrant.OperatorInbox))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	// The control: one read of a group that answers nothing.
	interests := kvStreamPrefix + bus.InterestBucket
	newLedResponder(t, url, interests, "")
	once, cancelOnce := context.WithTimeout(ctx, time.Second)
	defer cancelOnce()
	_, _, err = streamInfoOnce(once, nc, interests)
	require.ErrorIs(t, err, context.DeadlineExceeded, "control: the one read of a group that answers nothing")

	sessions := kvStreamPrefix + bus.SessionBucket
	led := newLedResponder(t, url, sessions, "", ledInfo(t, sessions, "n1"))
	info, found, err := StreamInfo(ctx, nc, sessions)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, sessions, info.Config.Name)
	require.Equal(t, 2, led.count(), "StreamInfo did not send the read again after its unanswered first")

	// A group that never answers is the error when the caller's bound ends.
	roles := kvStreamPrefix + bus.RoleClaimBucket
	newLedResponder(t, url, roles, "")
	limited, cancelLimited := context.WithTimeout(ctx, 2*time.Second)
	defer cancelLimited()
	_, _, err = StreamInfo(limited, nc, roles)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
