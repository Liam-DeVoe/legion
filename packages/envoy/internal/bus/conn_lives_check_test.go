package bus

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// A connection life ends as soon as its status listener receives a drop report. nats.go removes a
// listener after one unread report, so the connection watch compares the reconnect count the first
// window pinned in that life read after CONNECTED. A later count ends the life even when the report
// was missed.
//
// The row drives the watch itself (lifeCheckEvery is set past the row) and decides by events:
//   - one missed report: a listener removed before one drop leaves the old life current until one
//     check ends it; the windows it carried are refused and a fresh window pins;
//   - a drop between the life and connected reads: the old life closes before the pin can record it,
//     so the loop takes a fresh life after the reconnect; that fresh window survives two checks;
//   - a clean reconnect: a reported drop ends the old life, and the post-reconnect window survives
//     two checks.
//
// The controls: a pinIf that ignores its ch argument lets a stale life pin, so the second row's
// window is refused. A check that never ends a pinned life fails the first; a < rather than <=
// fails both the second and the third. Every wait is testwait's, with no bound of the row's: each
// only guards against NATS not changing state.
func TestAConnectionsOwnCheckKeepsALifeAfterACleanReconnect(t *testing.T) {
	every := lifeCheckEvery.Swap(int64(time.Hour))
	t.Cleanup(func() { lifeCheckEvery.Store(every) })

	t.Run("one missed report", func(t *testing.T) {
		client, hold := heldHandshakeClient(t)
		conn := client.Connection()
		old := client.WindowUntil(nil).(*window)
		if err := old.pinToSend(); err != nil {
			t.Fatalf("control: pin the old window: %v", err)
		}
		life := lifeOf(conn)
		conn.RemoveStatusListener(life.reports)
		stale := dropAndPin(t, client, conn, hold)
		life.check()
		requireStale(t, old, "the old window after one missed report")
		requireStale(t, stale, "the window pinned before the missed report was found")
		requireFresh(t, client, "one missed report")
	})

	t.Run("a drop between life and connected reads", func(t *testing.T) {
		client, hold := heldHandshakeClient(t)
		conn := client.Connection()
		// The hook below holds a reconnect's handshake (Hold) until the pin has read its count; the
		// attempt ends only when the row releases it, with a connect timeout of the test's own.
		testnats.OwnConnectTimeout(t, conn)
		w := client.WindowUntil(nil).(*window)
		original := lifeOf(conn).current()
		// The hooks run on the goroutine pinFirst's Bounded call starts, where no method of t may
		// run: each waits through waitInHook and hands its first error to hookFailed, which the
		// subtest reads once pinToSend has returned. The cleanup closes ended, so a hook still
		// waiting when the subtest ends returns then rather than at the shared wait's end.
		hookFailed := make(chan error, 1)
		ended := make(chan struct{})
		failed := func(err error) {
			select {
			case hookFailed <- err:
			default:
			}
		}
		var once sync.Once
		var held <-chan struct{}
		startDrop := func() {
			once.Do(func() {
				held = hold.Hold()
				if err := waitInHook("control: nats.go's report of the drop to end the old life", original, ended); err != nil {
					failed(err)
				}
			})
		}
		releaseAfterCount := func() {
			if err := waitInHook("the reconnect handshake to reach the hold", held, ended); err != nil {
				failed(err)
				return
			}
			hold.Release()
		}
		w.afterConnected.Store(&startDrop)
		w.pinCountRead.Store(&releaseAfterCount)
		t.Cleanup(func() {
			w.afterConnected.Store(nil)
			w.pinCountRead.Store(nil)
			close(ended)
			hold.Release()
		})
		pinned := w.pinToSend()
		select {
		case err := <-hookFailed:
			t.Fatalf("a hook of the staged drop failed: %v; the pin answered %v", err, pinned)
		default:
		}
		if pinned != nil {
			t.Fatalf("a window whose life read the drop before connected answered %v, want nil", pinned)
		}
		life := lifeOf(conn)
		life.check()
		life.check()
		if err := w.pinToSend(); err != nil {
			t.Errorf("the window pinned after the reconnect answered %v after the checks, want nil", err)
		}
	})

	t.Run("a clean reconnect", func(t *testing.T) {
		client, hold := heldHandshakeClient(t)
		conn := client.Connection()
		old := client.WindowUntil(nil).(*window)
		if err := old.pinToSend(); err != nil {
			t.Fatalf("control: pin the old window: %v", err)
		}
		life := lifeOf(conn)
		before := conn.Stats().Reconnects
		oldLife := old.life
		hold.Cut()
		hold.Restore()
		waitForLifeEnded(t, oldLife)
		w := pinAfterReconnect(t, client, conn, before)
		life.check()
		life.check()
		if err := w.pinToSend(); err != nil {
			t.Errorf("a window pinned after a clean reconnect answered %v after the checks, want nil", err)
		}
		requireStale(t, old, "the old window after the reported drop")
	})
}

// dropAndPin cuts conn through hold, lets it reconnect, and returns a window pinned before the
// caller's check notices its listener was removed.
func dropAndPin(t *testing.T, client *Client, conn *nats.Conn, hold *testnats.HandshakeHold) *window {
	t.Helper()
	before := conn.Stats().Reconnects
	hold.Cut()
	cutUntilReconnecting(t, conn)
	hold.Restore()
	return pinAfterReconnect(t, client, conn, before)
}

func requireStale(t *testing.T, w *window, what string) {
	t.Helper()
	if err := w.pinToSend(); !errors.Is(err, ErrUnavailable) {
		t.Errorf("%s answered %v, want ErrUnavailable", what, err)
	}
}

func requireFresh(t *testing.T, client *Client, after string) {
	t.Helper()
	w := client.WindowUntil(nil).(*window)
	if err := w.pinToSend(); err != nil {
		t.Errorf("a fresh window after %s answered %v, want nil", after, err)
	}
}

func waitForLifeEnded(t *testing.T, life <-chan struct{}) {
	t.Helper()
	testwait.Receive(t, "control: nats.go's report of the drop to end the old life", life)
}

// waitInHook waits, inside a hook a product goroutine runs (a window's afterConnected or
// pinCountRead, which pinFirst calls on its Bounded call's goroutine), until ch closes or the row
// ends (stop closes): through testwait.ReceiveInHelper, so no method of t runs off the test
// goroutine. It answers the wait's give-up, or that the row ended first, as an error the hook hands
// to the test goroutine. A nil ch never closes.
func waitInHook(what string, ch, stop <-chan struct{}) error {
	either := make(chan struct{})
	go func() {
		defer close(either)
		select {
		case <-ch:
		case <-stop:
		}
	}()
	if _, err := testwait.ReceiveInHelper("bus", what, either); err != nil {
		return err
	}
	select {
	case <-ch:
		return nil
	default:
		return fmt.Errorf("the row ended before %s", what)
	}
}

// cutUntilReconnecting waits, with the link cut, until nats.go reads conn as RECONNECTING: it has
// reported the drop. The row's cleanup restores the link should the wait give up.
func cutUntilReconnecting(t *testing.T, conn *nats.Conn) {
	t.Helper()
	testwait.Until(t, "control: nats.go to read the connection as reconnecting once the link is cut", func() (nats.Status, bool) {
		status := conn.Status()
		return status, status == nats.RECONNECTING
	})
}

// pinAfterReconnect waits until nats.go has reconnected conn, with the reconnect counted past before,
// and returns a new window of client whose first send pinned it.
func pinAfterReconnect(t *testing.T, client *Client, conn *nats.Conn, before uint64) *window {
	t.Helper()
	testwait.Until(t, "control: nats.go to reconnect after the cut, with the reconnect counted", func() (string, bool) {
		status, reconnects := conn.Status(), conn.Stats().Reconnects
		return fmt.Sprintf("status %v, %d reconnect(s)", status, reconnects-before), status == nats.CONNECTED && reconnects != before
	})
	w := client.WindowUntil(nil).(*window)
	if err := w.pinToSend(); err != nil {
		t.Fatalf("control: a window's first send after the reconnect answered %v, want nil", err)
	}
	return w
}
