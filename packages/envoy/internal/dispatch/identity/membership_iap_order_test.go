package identity

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/identity/directorytest"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// lateActive is the people store with the write of each Active outcome held until release is
// called: entered closes as the write is reached, after the check's Directory read has answered.
// The write runs without the check's 15-second deadline, so how long the test holds it decides
// only the order of the writes, never whether this one misses its deadline.
type lateActive struct {
	*store.PgPeopleStore
	entered chan struct{}
	gate    chan struct{}
}

func (s lateActive) RecordDirectoryActive(ctx context.Context, email, userID string, readAt time.Time) (time.Time, error) {
	close(s.entered)
	<-s.gate
	return s.PgPeopleStore.RecordDirectoryActive(context.WithoutCancel(ctx), email, userID, readAt)
}

// TestIAPActiveAnswerFromAnEarlierReadReleasesNoLaterHold: two Dispatch processes check ada's
// account at once. Process A reads her row and the Directory answers Active; then Google suspends
// her, and process B reads her row, the Directory answers suspended, and B records Held. A's
// Active outcome is written only after B's Held: it rests on a Directory read older than the
// hold, so it writes nothing, A counts RefreshNone, and ada stays held. The order control writes
// A's Active outcome before Google suspends her: A counts RefreshConfirmed, and B's Held then
// holds her, so the test fails only on the order of the writes.
//
// Control: clear held_since in the Active write whatever the time of the read it rests on; the
// first case fails, naming the row A's write released.
func TestIAPActiveAnswerFromAnEarlierReadReleasesNoLaterHold(t *testing.T) {
	for _, c := range []struct {
		name        string
		activeFirst bool
		want        RefreshOutcome
	}{
		{"an Active outcome written after a later read's Held", false, RefreshNone},
		{"order control: the Active outcome written before Google suspends her", true, RefreshConfirmed},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			r := newDirectoryStandingRig(t)
			adaActive(r.google)
			r.record(directoryAda)
			r.active(directoryAda, "u-ada", 2*time.Hour)
			reader, err := NewDirectoryReader(t.Context(), directorySigner, directoryAdmin, r.google.ClientOptions()...)
			if err != nil {
				t.Fatalf("NewDirectoryReader: %v", err)
			}
			late := lateActive{PgPeopleStore: r.people, entered: make(chan struct{}), gate: make(chan struct{})}
			release := sync.OnceFunc(func() { close(late.gate) })
			a := &Membership{People: late, Rule: workspace, Directory: reader, Outliving: r.outliving}
			type result struct {
				outcome RefreshOutcome
				err     error
			}
			done := make(chan result, 1)
			go func() {
				outcome, err := a.ConfirmDirectory(ctx, directoryAda)
				done <- result{outcome, err}
			}()
			// A failure below leaves A's write held: release it and wait for it before the
			// rig's database is dropped.
			var got result
			waited := false
			t.Cleanup(func() {
				release()
				if !waited {
					<-done
				}
			})
			<-late.entered
			if c.activeFirst {
				release()
				got, waited = <-done, true
			}
			r.google.SetUser(directoryAda, directorytest.User{ID: "u-ada", PrimaryEmail: directoryAda, Suspended: true})
			r.newProcess()
			r.age(directoryAda, 2*time.Hour)
			if outcome, err := r.membership.ConfirmDirectory(ctx, directoryAda); err != nil || outcome != RefreshHeld {
				t.Fatalf("B's check of the suspended account = %v, %v; want RefreshHeld", outcome, err)
			}
			if answer := r.standing(directoryAda); !answer.Held {
				t.Fatalf("after B's Held outcome Standing = %+v; want held", answer)
			}
			if !c.activeFirst {
				release()
				got, waited = <-done, true
			}
			if got.err != nil || got.outcome != c.want {
				t.Errorf("A's check counted %v, %v; want %v", got.outcome, got.err, c.want)
			}
			row := r.row(directoryAda)
			r.newProcess()
			if answer := r.standing(directoryAda); !answer.Held {
				t.Fatalf("a Held outcome from the later Directory read was released by an Active outcome from an earlier one: "+
					"Standing = %+v; held_since %s, directory_confirmed_at %s, not_held_until %s",
					answer, stamp(row.heldSince), stamp(row.confirmedAt), stamp(row.notHeldUntil))
			}
		})
	}
}
