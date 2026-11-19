package session

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/testnats"
)

// openUnopenedStore is a known-session store over a connection through proxy whose user may not
// open the bucket, so the store's background retry runs (armRepair) and attempts every
// retryFirst, each attempt bounded at window (withKnownSessionAttemptWindow; 0 for the default).
func openUnopenedStore(t *testing.T, window time.Duration) (*KnownSessions, *testnats.Proxy) {
	t.Helper()
	seed, public := testnats.User(t)
	grant := testnats.StartNkeyGranted(t, public, "nothing.this.store.uses")
	proxy := testnats.StartProxy(t, grant.URL)
	t.Cleanup(proxy.Restore)
	conn, err := testnats.ConnectSeed(t, proxy.URL(), seed)
	if err != nil {
		t.Fatalf("connect through the proxy as the user whose grant does not carry the bucket: %v", err)
	}
	t.Cleanup(conn.Close)
	options := []KnownSessionsOption{
		WithKnownSessionReplicas(1),
		WithKnownSessionLogger(slog.New(slog.NewTextHandler(&lockedWriter{}, nil))),
		withKnownSessionRetry(10*time.Millisecond, 10*time.Millisecond),
	}
	if window > 0 {
		options = append(options, withKnownSessionAttemptWindow(window))
	}
	known, err := OpenKnownSessions(t.Context(), conn, options...)
	if err != nil {
		t.Fatalf("open the store over a bucket its user may not open: %v", err)
	}
	t.Cleanup(known.StopWatch)
	if !repairRunning(known) {
		t.Fatal("the store's background retry is not running; the row needs it")
	}
	return known, proxy
}

// lockFreeWithin reports whether repairMu could be taken within wait.
func lockFreeWithin(known *KnownSessions, wait time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if known.repairMu.LockContext(ctx) != nil {
		return false
	}
	known.repairMu.Unlock()
	return true
}

// The background retry's attempt lets repairMu go at its window even while its request gets no
// answer: the proxy holds every byte (Freeze), as a connection mid-handshake answers nothing, and
// the attempt, bounded at its window (repairAttemptWindow, here 300 ms), ends, so a reconnect
// hook's move and the next attempt are not held behind it.
//
// The control is the attempt bounded only by the store's stop: the lock stays held while the
// proxy holds the bytes, and the row fails.
func TestTheRetrysAttemptLetsItsLockGoAtItsWindow(t *testing.T) {
	known, proxy := openUnopenedStore(t, 300*time.Millisecond)
	proxy.Freeze()
	// An attempt made after the freeze holds the lock until its window ends, 300 ms; the wait
	// below starts once one has begun, and 1 s bounds a row that fails.
	time.Sleep(100 * time.Millisecond)
	if !lockFreeWithin(known, time.Second) {
		t.Fatal("repairMu was still held 1 s after an attempt began while the proxy held every byte: the retry's attempt waited for its request past its window")
	}
}

// An arm of the background repair made while the retry's attempt holds repairMu returns at once:
// arming reads and writes only the retry's state (repairStateMu), never the attempt's lock, so the
// open of a bucket that failed and a move's failure arm or ask again without waiting for an attempt
// a handshake holds. An arm made while the retry runs asks it for one more attempt (rearm). The row
// holds repairMu as the retry's attempt would.
//
// The control is armRepair taking repairMu: it waits for the lock, and the row fails.
func TestAnArmWhileTheRetrysAttemptHoldsItsLockReturnsAtOnce(t *testing.T) {
	known, _ := openUnopenedStore(t, 0)
	known.repairMu.Lock()
	unlocked := false
	t.Cleanup(func() {
		if !unlocked {
			known.repairMu.Unlock()
		}
	})
	armed := make(chan struct{})
	go func() {
		known.armRepair()
		close(armed)
	}()
	select {
	case <-armed:
	case <-time.After(2 * time.Second):
		known.repairMu.Unlock()
		unlocked = true
		t.Fatal("armRepair had not returned 2 s after it was called while repairMu was held: it waited for the retry's attempt")
	}
	known.repairStateMu.Lock()
	asked := known.rearm
	known.repairStateMu.Unlock()
	if !asked {
		t.Fatal("an arm made while the retry ran did not ask it for another attempt")
	}
}
