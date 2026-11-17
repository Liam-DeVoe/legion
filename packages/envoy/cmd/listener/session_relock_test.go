package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
)

// relockBudget is the budget of the relock in the row below: long enough for the connection to
// come back (nats.go waits one second between attempts) and short of the other holder's hold.
const relockBudget = 2500 * time.Millisecond

// A call that lost its connection under a session's lock releases the lock, waits for the
// connection, and takes the lock again, all inside its own budget (relockAfterReconnect): a
// registration of the session that takes the lock while the call waits for the connection, and
// keeps it past that budget, leaves the relock answering no retry at the budget's end, holding
// nothing, rather than waiting for that holder.
//
// The control is the same row at 2b8a5d7cbb: the relock took the lock with no bound once the
// connection was back, so it waited for the other holder past its budget, still waiting at 5 s.
func TestARelockPastItsBudgetGivesUpRatherThanWaitForAnotherHolder(t *testing.T) {
	proxy := testnats.StartProxy(t, testnats.URL(t))
	client, err := bus.ConnectOwningStream([]string{proxy.URL()}, bus.WithReplicas(1))
	if err != nil {
		t.Fatalf("connect through the proxy: %v", err)
	}
	t.Cleanup(client.Close)
	m := &sessionDelivery{}
	const sessionID = "ses_relock_budget"
	unlock := m.locks.lock(sessionID)

	proxy.Cut()
	for client.Connected() {
		time.Sleep(time.Millisecond)
	}
	_, lost := bus.Request(context.Background(), client, func(context.Context) (struct{}, error) { return struct{}{}, nil })
	if !errors.Is(lost, bus.ErrConnectionLost) {
		t.Fatalf("a request while the connection was down answered %v, want ErrConnectionLost", lost)
	}

	ctx, cancel := context.WithTimeout(context.Background(), relockBudget)
	defer cancel()
	budgetEnds, _ := ctx.Deadline()
	retried := make(chan bool, 1)
	go func() { retried <- m.relockAfterReconnect(ctx, sessionID, &unlock, lost) }()
	// The other holder takes the lock the moment the relock releases it, and keeps it to the end.
	other := m.locks.lock(sessionID)
	t.Cleanup(other)
	proxy.Restore()

	select {
	case again := <-retried:
		late := time.Since(budgetEnds)
		if again {
			t.Fatalf("the relock reported a retry %s after its budget, with another holder keeping the lock", late)
		}
		if late > 200*time.Millisecond {
			t.Fatalf("the relock gave up %s after its budget ended, want at it", late)
		}
		t.Logf("the relock gave up %s after its budget ended", late)
	case <-time.After(5 * time.Second):
		t.Fatalf("the relock was still waiting for the other holder 5 s on, past its %s budget", relockBudget)
	}
	// The release the caller makes on its way out releases nothing: the other holder still holds.
	unlock()
	if users := lockUsers(&m.locks, sessionID); users != 1 {
		t.Fatalf("%d callers hold or wait for the lock after the caller's release, want the other holder alone", users)
	}
}
