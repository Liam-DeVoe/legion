package outbox

import (
	"log/slog"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/duty"
)

// TestTheOutboxDoesNotScanWhileIdle's record of the holder begins a rest at either of the two
// moments one can begin, and at neither while the holder has a scan still to run (heldScans.rest).
// The row reaches a rest at listening only when the holder took the catch-up wake, scanned and
// parked before the connection counted as up, which a run cannot arrange, so this calls the
// record's own hooks in the orders the outbox calls them, with no database.
func TestTheIdleRowRestsAtEitherCallbackOnlyWithNothingLeftToDo(t *testing.T) {
	// The hold taken and the first connection's LISTEN succeeded, its catch-up wake not sent yet.
	started := func() (*Outbox, *heldScans) {
		outbox := New(Deps{})
		h := &heldScans{outbox: outbox}
		h.restingAt.Store(-1)
		outbox.hold.Store(new(duty.Hold))
		h.logged(slog.NewRecord(time.Now(), slog.LevelInfo, tookHoldLine, 0))
		h.listened()
		return outbox, h
	}
	t.Run("the connection up after the wait", func(t *testing.T) {
		outbox, h := started()
		outbox.signal()
		<-outbox.wake
		outbox.claims.Add(1)
		h.waited()
		if got := h.restingAt.Load(); got != -1 {
			t.Fatalf("a wait before the connection counted as up began a rest at %d claims, want none", got)
		}
		h.listening()
		if got, claims := h.restingAt.Load(), outbox.claims.Load(); got != claims {
			t.Fatalf("the connection up after the wait began a rest at %d claims, want one at the wait's %d", got, claims)
		}
	})
	t.Run("the connection up before the wait", func(t *testing.T) {
		outbox, h := started()
		outbox.signal()
		h.listening()
		<-outbox.wake
		outbox.claims.Add(1)
		h.waited()
		if got, claims := h.restingAt.Load(), outbox.claims.Load(); got != claims {
			t.Fatalf("the wait after the connection came up began a rest at %d claims, want one at %d", got, claims)
		}
	})
	t.Run("a wake queued when the connection comes up", func(t *testing.T) {
		outbox, h := started()
		outbox.signal()
		<-outbox.wake
		outbox.claims.Add(1)
		h.waited()
		outbox.signal()
		h.listening()
		if got := h.restingAt.Load(); got != -1 {
			t.Fatalf("the connection up with a wake queued began a rest at %d claims, want none", got)
		}
	})
}
