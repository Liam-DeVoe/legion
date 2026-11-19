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
// record's own hooks in the orders the outbox calls them, with no database. At the first
// subtest's first wait, and in each subtest after the second, exactly one condition of the rule
// refuses the rest, so a rule without any one of them fails a subtest: the connection up (the
// first), no wake queued now (the third), no take or LISTEN without its scan (the fourth), no wake
// queued at the wait (the fifth) and no retry armed (the sixth). The last condition, no claim
// since the wait, needs no subtest: a rest recorded without it names a claim count the outbox has
// already passed, which atRest never matches.
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
	t.Run("the connection up while the holder runs the catch-up scan", func(t *testing.T) {
		// The listener sends the catch-up wake before it calls afterListening, so the holder can
		// take that wake before the connection counts as up and still be scanning when it does:
		// the take and the LISTEN have no scan yet.
		outbox, h := started()
		outbox.signal()
		<-outbox.wake
		h.listening()
		if got := h.restingAt.Load(); got != -1 {
			t.Fatalf("the connection up while the holder runs its LISTEN's catch-up scan began a rest at %d claims, want none", got)
		}
	})
	t.Run("a wake queued at the wait and taken when the connection comes up", func(t *testing.T) {
		// The wake queued at the wait has been taken by the time the connection counts as up, so
		// none is queued now, and the holder is scanning for it.
		outbox, h := started()
		outbox.signal()
		<-outbox.wake
		outbox.claims.Add(1)
		outbox.signal()
		h.waited()
		<-outbox.wake
		h.listening()
		if got := h.restingAt.Load(); got != -1 {
			t.Fatalf("the connection up while the holder scans for the wake queued at its wait began a rest at %d claims, want none", got)
		}
	})
	t.Run("a wait after a scan that armed its retry", func(t *testing.T) {
		// A scan that met a failed statement arms its retry before the wait, and the retry's scan
		// is still to come.
		outbox, h := started()
		outbox.signal()
		h.listening()
		<-outbox.wake
		outbox.claims.Add(1)
		h.armed(time.Second)
		h.waited()
		if got := h.restingAt.Load(); got != -1 {
			t.Fatalf("a wait whose scan armed a retry began a rest at %d claims, want none", got)
		}
	})
}
