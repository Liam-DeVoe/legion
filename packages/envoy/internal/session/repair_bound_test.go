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

// The background retry's attempt lets repairMu go at its window even while its requests get no
// answer: the proxy holds every byte (Freeze), as a connection mid-handshake answers nothing, and
// the attempt, bounded at its window (repairAttemptWindow, here 300 ms), ends, so a reconnect
// hook's move and the next attempt are not held behind it. The bucket lookup an attempt opens with
// would otherwise wait its own 2 s (knownBucketOpenWait), so the row takes the lock again and again
// for 3 s, longer than one such lookup: each take must come within 1 s.
//
// The control is the attempt bounded only by the store's stop: an attempt holds the lock for its
// lookup's 2 s, and a take waits past 1 s.
func TestTheRetrysAttemptLetsItsLockGoAtItsWindow(t *testing.T) {
	known, proxy := openUnopenedStore(t, 300*time.Millisecond)
	proxy.Freeze()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); {
		if !lockFreeWithin(known, time.Second) {
			t.Fatal("repairMu was held for over 1 s while the proxy held every byte: the retry's attempt waited for its requests past its 300 ms window")
		}
		// The retry attempts every 10 ms: this pause lets the next attempt take the lock.
		time.Sleep(50 * time.Millisecond)
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
