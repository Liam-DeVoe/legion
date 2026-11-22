package writerhold

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/jsm.go/api"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// placementRefusal is JetStream's answer to a create it cannot place, as nats.go returns it.
func placementRefusal() error {
	return fmt.Errorf("create envoy_roles: %w", &nats.APIError{Code: 400, ErrorCode: placementCode, Description: "no suitable peers for placement, peer offline"})
}

// RetryPlacement runs a create again while JetStream refuses to place it, by the refusal's code in
// nats.go's legacy or jetstream error or jsm.go's: the create that is placed on its third attempt
// is done, with one line per retry. Any other error, an API error of another code or a text that
// merely reads like the refusal, returns at once after one attempt. A create still refused at the
// bound is UNKNOWN, naming the op. The control for "at once" is a go test -overlay copy of
// placement.go whose PlacementRefused answers true for every error, under which the other-error
// cases retry until the bound and fail.
func TestRetryPlacement(t *testing.T) {
	ctx := context.Background()
	t.Run("placed on the third attempt", func(t *testing.T) {
		for name, refusal := range map[string]func() error{
			"nats.go legacy": placementRefusal,
			"nats.go jetstream": func() error {
				return fmt.Errorf("create: %w", &jetstream.APIError{Code: 400, ErrorCode: placementCode, Description: "no suitable peers for placement"})
			},
			"jsm.go": func() error {
				return fmt.Errorf("restore: %w", api.ApiError{Code: 400, ErrCode: placementCode, Description: "no suitable peers for placement"})
			},
		} {
			t.Run(name, func(t *testing.T) {
				calls := 0
				var out bytes.Buffer
				err := RetryPlacement(ctx, time.Minute, "create KV_envoy_roles", &out, func() error {
					calls++
					if calls < 3 {
						return refusal()
					}
					return nil
				})
				require.NoError(t, err)
				require.Equal(t, 3, calls)
				require.Equal(t, 2, bytes.Count(out.Bytes(), []byte("JetStream could not place it yet")), out.String())
			})
		}
	})
	t.Run("another error returns at once", func(t *testing.T) {
		for name, other := range map[string]error{
			"another API code":         &nats.APIError{Code: 400, ErrorCode: 10058, Description: "stream name already in use with a different configuration"},
			"the refusal's text alone": errors.New("nats: no suitable peers for placement, peer offline"),
			"a timeout":                context.DeadlineExceeded,
		} {
			t.Run(name, func(t *testing.T) {
				calls := 0
				err := RetryPlacement(ctx, 2*time.Second, "create KV_envoy_roles", &bytes.Buffer{}, func() error { calls++; return other })
				require.ErrorIs(t, err, other)
				require.Equal(t, 1, calls, "an error that is no placement refusal was retried")
			})
		}
	})
	t.Run("still refused at the bound", func(t *testing.T) {
		calls := 0
		err := RetryPlacement(ctx, time.Second, "create KV_envoy_roles", &bytes.Buffer{}, func() error { calls++; return placementRefusal() })
		unknown := requireUnknown(t, err, StepPostcheck, "create KV_envoy_roles", "refused to place it", "code 10005")
		require.ErrorContains(t, unknown, "no suitable peers for placement", "the last refusal's text")
		require.Greater(t, calls, 1)
	})
}

// RetryPlacement names the caller's context ending, inside its wait between two attempts, as that
// context's error, never as the placement limit passing: the caller's cancel is no evidence that
// JetStream could not place the stream for the restart's limit. Control: 870ce0c500, which answered
// "JetStream refused to place it (code 10005) for 1m0s, the restart's limit".
func TestRetryPlacementNamesACancelledCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := RetryPlacement(ctx, time.Minute, "create KV_envoy_roles", &bytes.Buffer{}, func() error {
		calls++
		cancel()
		return placementRefusal()
	})
	require.ErrorIs(t, err, context.Canceled, "the caller's cancel was not named: %v", err)
	require.NotContains(t, err.Error(), "the restart's limit", "the caller's cancel was called the limit passing")
	require.ErrorContains(t, err, "create KV_envoy_roles")
	require.Equal(t, 1, calls)
}
