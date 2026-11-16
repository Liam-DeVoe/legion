package natscallout

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/testnats"
)

// withDecisionSlots runs the rest of the test with concurrentDecisions set to n, so a test can make
// requests queue for a slot without sending sixty-four of them.
func withDecisionSlots(t *testing.T, n int) {
	t.Helper()
	was := concurrentDecisions
	concurrentDecisions = n
	t.Cleanup(func() { concurrentDecisions = was })
}

// slowIntrospect answers each session's token after wait, giving up when ctx ends, as a broker
// whose database is slow does.
func slowIntrospect(wait time.Duration, sessions ...*session) Deps {
	answer := introspector(sessions...)
	return Deps{Introspect: func(ctx context.Context, token string) (oauthwire.Introspection, error) {
		select {
		case <-ctx.Done():
			return oauthwire.Introspection{}, ctx.Err()
		case <-time.After(wait):
		}
		return answer(ctx, token)
	}}
}

// connectAll connects every session at once and answers each one's error and how long its connect
// took, in the order of sessions.
func connectAll(t *testing.T, url string, sessions []*session) ([]error, []time.Duration) {
	t.Helper()
	errs := make([]error, len(sessions))
	took := make([]time.Duration, len(sessions))
	var wait sync.WaitGroup
	for index, s := range sessions {
		wait.Add(1)
		go func() {
			defer wait.Done()
			began := time.Now()
			nc, err := connectSession(t, url, s, nil)
			took[index], errs[index] = time.Since(began), err
			if err == nil {
				nc.Close()
			}
		}()
	}
	wait.Wait()
	return errs, took
}

func sessionsNamed(t *testing.T, prefix string, n int) []*session {
	t.Helper()
	sessions := make([]*session, n)
	for index := range sessions {
		sessions[index] = newSession(t, fmt.Sprintf("%s_%d", prefix, index), 15*time.Minute)
	}
	return sessions
}

// The budget counts from when the request arrives, not from when a decision slot frees up: a
// request that waited for a slot gets what is left of its budget, and one already past it is
// refused at once, so every client hears inside the budget, admitted or refused, and none waits
// out the server's own timeout. With one slot and a 700 ms broker, the third request of three
// waits 1.4 s for its slot: counted from its slot it would be answered at 2.1 s, after the server
// had given up on it.
func TestTheBudgetCountsTheTimeARequestWaitsForASlot(t *testing.T) {
	withDecisionSlots(t, 1)
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)
	sessions := sessionsNamed(t, "ses_queued", 3)
	startService(t, url, keys, slowIntrospect(700*time.Millisecond, sessions...), Decide)

	errs, took := connectAll(t, url, sessions)
	admitted := 0
	for index := range sessions {
		require.Less(t, took[index], decideBudget+400*time.Millisecond,
			"session %d heard after %s (err %v): its budget was counted from its slot, not from its arrival", index, took[index], errs[index])
		if errs[index] == nil {
			admitted++
		}
	}
	require.GreaterOrEqual(t, admitted, 2, "the requests that fit their budget were not admitted: %v", errs)
}

// A replica that stops answers every request the server had already routed to it before it closes:
// it drains its subscription rather than dropping what it holds. With one slot and a 300 ms broker,
// four requests sent at once are one in a decision, one waiting for the slot and two still in the
// subscription's buffer when the replica is closed.
func TestAClosingReplicaAnswersTheRequestsItHolds(t *testing.T) {
	withDecisionSlots(t, 1)
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)
	sessions := sessionsNamed(t, "ses_held", 4)
	service := startService(t, url, keys, slowIntrospect(300*time.Millisecond, sessions...), Decide)

	results := make(chan []error, 1)
	go func() {
		errs, _ := connectAll(t, url, sessions)
		results <- errs
	}()
	time.Sleep(150 * time.Millisecond)
	service.Close()
	errs := <-results
	for index, err := range errs {
		require.NoError(t, err, "session %d was refused: the closing replica dropped the request it held", index)
	}
}
