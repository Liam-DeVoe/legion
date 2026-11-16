package natscallout

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// withLiveness runs the rest of the test with the heartbeat's tick and staleness bound set short,
// so a stalled loop reads stale in well under a second.
func withLiveness(t *testing.T, tick, bound time.Duration) {
	t.Helper()
	wasTick, wasBound := livenessTick, livenessBound
	livenessTick, livenessBound = tick, bound
	t.Cleanup(func() { livenessTick, livenessBound = wasTick, wasBound })
}

func livenessStatus(service *Service) int {
	recorder := httptest.NewRecorder()
	service.LivenessHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/livez", nil))
	return recorder.Code
}

// The liveness handler answers 200 while the callout loop is turning, idle included, and 503 once
// it stops: here every decision slot is held by a decision that never returns, so no request can
// be decided, and the heartbeat, which beats only when it can take a slot, goes stale. The control
// is the same service idle, which stays live for as long as the stalled one takes to go stale.
func TestTheLivenessHandlerFailsWhileTheCalloutLoopIsStalled(t *testing.T) {
	withLiveness(t, 20*time.Millisecond, 200*time.Millisecond)
	withDecisionSlots(t, 1)
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)

	idle := startService(t, url, keys, Deps{}, Decide)
	time.Sleep(3 * livenessBound)
	require.Equal(t, http.StatusOK, livenessStatus(idle), "an idle service read as stalled")

	release := make(chan struct{})
	stuck := func(context.Context, Deps, []ServiceEntry, Request) (Decision, error) {
		<-release
		return Decision{}, nil
	}
	stalled := startService(t, url, keys, Deps{}, stuck)
	// Registered after startService's own Close, so it runs first: Close waits for the decision
	// it holds, which ends only here.
	t.Cleanup(func() { close(release) })
	idle.Close()
	a := newSession(t, "ses_stuck", 15*time.Minute)
	go func() { _, _ = connectSession(t, url, a, nil) }()
	testwait.WithinPolling(t, 5*time.Second, 20*time.Millisecond, "the stalled service's liveness to fail", func() bool {
		return livenessStatus(stalled) == http.StatusServiceUnavailable
	})
}
