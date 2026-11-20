package outbox

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// answeringHolds answers every statement of a hold as one that affected the hold's row, so
// duty.TryHold gives a hold with no database behind it, whose deadline Release pulls back to the
// moment it is released.
type answeringHolds struct{}

func (answeringHolds) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (answeringHolds) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

// TestTheOutboxDoesNotScanWhileIdle's record of the holder reads which event gave a take's scan
// each of its batches. A LISTEN connection lost with the database comes back while the outbox holds
// no hold, and its catch-up wake waits for the next holder (Outbox.listen): the take's scan reads
// the take's batch and the connection's, or the scan after it reads the connection's. A wake
// queued with no LISTEN before the take is no catch-up, and the row counts its batch. This calls
// the record's hooks in the orders the outbox calls them, with no database, and judges the scans
// against a witness that saw the database fail over the loss.
func TestTheIdleRowReadsTheCatchUpOfALISTENBackBeforeATakeByItsEvent(t *testing.T) {
	// lostHold runs the record to a hold taken, its LISTEN connection lost with the database and
	// the hold lost after renewals the database left unanswered, and returns the record and the
	// witness's evidence over that loss.
	lostHold := func(t *testing.T) (*Outbox, *heldScans, evidence) {
		outbox := New(Deps{})
		h := &heldScans{outbox: outbox}
		h.restingAt.Store(-1)
		hold, err := duty.TryHold(context.Background(), answeringHolds{}, publisherHold, uuid.New(), publisherHoldOptions)
		if err != nil || hold == nil {
			t.Fatalf("take a hold with no database: %v", err)
		}
		outbox.hold.Store(hold)
		h.logged(slog.NewRecord(time.Now(), slog.LevelInfo, tookHoldLine, 0))
		h.listened()
		outbox.signal()
		h.listening()
		<-outbox.wake
		outbox.claims.Add(1)
		h.waited()
		if err := hold.Release(context.Background()); err != nil {
			t.Fatalf("release the hold: %v", err)
		}
		lapsed := hold.Deadline()
		redial := slog.NewRecord(lapsed.Add(-2*time.Second), slog.LevelWarn, redialLine, 0)
		redial.AddAttrs(slog.String("channels", "["+eventsChannel+"]"), slog.String("error", "connection refused"))
		h.logged(redial)
		failed := slog.NewRecord(lapsed.Add(-time.Second), slog.LevelWarn, renewFailedLine+"; the loss deadline stays where it was", 0)
		failed.AddAttrs(slog.String("hold", publisherHold), slog.Time("deadline", lapsed), slog.String("error", "timeout"))
		h.logged(failed)
		outbox.hold.Store(nil)
		window := failureWindow{from: lapsed.Add(-3 * time.Second), to: lapsed.Add(200 * time.Millisecond), err: "timeout"}
		return outbox, h, evidence{witnessed: witnessed{windows: []failureWindow{window}}}
	}
	// takeBack takes the next hold, as the standby does.
	takeBack := func(t *testing.T, outbox *Outbox, h *heldScans) {
		hold, err := duty.TryHold(context.Background(), answeringHolds{}, publisherHold, uuid.New(), publisherHoldOptions)
		if err != nil || hold == nil {
			t.Fatalf("take the next hold with no database: %v", err)
		}
		t.Cleanup(func() { _ = hold.Release(context.Background()) })
		outbox.hold.Store(hold)
		h.logged(slog.NewRecord(time.Now(), slog.LevelInfo, tookHoldLine, 0))
	}
	// last is the last scan the record ended, judged.
	last := func(h *heldScans, saw evidence) judgedScan {
		return h.scans[len(h.scans)-1].judge(saw)
	}

	t.Run("the connection back before the take, its wake in the take's scan", func(t *testing.T) {
		outbox, h, saw := lostHold(t)
		h.listened()
		outbox.signal()
		takeBack(t, outbox, h)
		<-outbox.wake
		outbox.claims.Add(2)
		h.waited()
		if judged := last(h, saw); judged.plain() {
			t.Fatalf("the take's scan that read the connection's catch-up was counted: %d by %s", judged.claims, judged.woken)
		}
	})
	t.Run("the connection back before the take, its wake in the scan after", func(t *testing.T) {
		outbox, h, saw := lostHold(t)
		h.listened()
		outbox.signal()
		takeBack(t, outbox, h)
		outbox.claims.Add(1)
		h.waited()
		<-outbox.wake
		outbox.claims.Add(1)
		h.waited()
		for _, scan := range h.scans[len(h.scans)-2:] {
			if judged := scan.judge(saw); judged.plain() {
				t.Fatalf("a scan of the take or of the connection's queued catch-up was counted: %d by %s", judged.claims, judged.woken)
			}
		}
	})
	t.Run("a wake queued with no connection back, in the take's scan", func(t *testing.T) {
		outbox, h, saw := lostHold(t)
		outbox.signal()
		takeBack(t, outbox, h)
		<-outbox.wake
		outbox.claims.Add(2)
		h.waited()
		if judged := last(h, saw); !judged.plain() || judged.claims != 1 || !strings.Contains(judged.woken, "a wake the take's catch-up took") {
			t.Fatalf("the take's scan that read a queued wake no LISTEN sent was judged %d plain=%v %q, want 1 counted as a wake the take's catch-up took",
				judged.claims, judged.plain(), judged.woken)
		}
	})
	t.Run("a wake queued with no connection back, in the scan after", func(t *testing.T) {
		outbox, h, saw := lostHold(t)
		outbox.signal()
		takeBack(t, outbox, h)
		outbox.claims.Add(1)
		h.waited()
		<-outbox.wake
		outbox.claims.Add(1)
		h.waited()
		if judged := last(h, saw); !judged.plain() || judged.claims != 1 {
			t.Fatalf("the scan of a queued wake no LISTEN sent was judged %d plain=%v %q, want 1 counted", judged.claims, judged.plain(), judged.woken)
		}
	})
}
