package admission

import (
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The work outside admission holds at most the request pool's connections admission leaves, 8 of
// the 48, and is never queued for them: a ninth asks and is refused at once, counted by who asked,
// and the place one gives back is the next one's. A Limiter's places are its own, and a nil Outside
// limits nothing.
func TestTheWorkOutsideAdmissionHoldsTheShareAdmissionLeaves(t *testing.T) {
	if OutsideLimit != store.SharedPoolSize-MaxAdmittedUnits {
		t.Fatalf("OutsideLimit = %d, want the request pool's %d less admission's %d", OutsideLimit, store.SharedPoolSize, MaxAdmittedUnits)
	}
	if OutsideLimit != 8 {
		t.Fatalf("OutsideLimit = %d, want 8", OutsideLimit)
	}
	outside := New(DefaultLimits()).Outside()
	for place := range OutsideLimit {
		if !outside.Take(HeartbeatCheck) {
			t.Fatalf("place %d of %d refused, want it taken", place+1, OutsideLimit)
		}
	}
	if outside.Take(DPoPDecision) || outside.Take(HeartbeatCheck) || outside.Take(HeartbeatCheck) {
		t.Fatalf("a place past the %d taken, want it refused", OutsideLimit)
	}
	if release, held := outside.Hold(SocketAdmission); held {
		t.Fatalf("a Hold past the %d held, want it refused", OutsideLimit)
	} else {
		release()
	}
	want := OutsideStats{Limit: OutsideLimit, InUse: OutsideLimit}
	want.Refused[DPoPDecision], want.Refused[HeartbeatCheck], want.Refused[SocketAdmission] = 1, 2, 1
	if got := outside.Stats(); got != want {
		t.Fatalf("stats %+v, want %+v: a refused Hold's release gives nothing back", got, want)
	}
	if other := New(DefaultLimits()).Outside(); !other.Take(DPoPDecision) {
		t.Fatal("another Limiter's first place refused, want each Limiter's places its own")
	}

	outside.Release()
	release, held := outside.Hold(SocketAdmission)
	if !held {
		t.Fatal("the place given back refused a Hold, want it the next one's")
	}
	if got := outside.Stats().InUse; got != OutsideLimit {
		t.Fatalf("%d places in use, want %d", got, OutsideLimit)
	}
	release()
	release()
	if got := outside.Stats().InUse; got != OutsideLimit-1 {
		t.Fatalf("%d places in use after a Hold's release was called twice, want %d: one place given back once", got, OutsideLimit-1)
	}

	var none *Outside
	if !none.Take(DPoPDecision) {
		t.Fatal("a nil Outside refused a place, want it to limit nothing")
	}
	none.Release()
	if release, held := none.Hold(SocketAdmission); !held {
		t.Fatal("a nil Outside refused a Hold, want it to limit nothing")
	} else {
		release()
	}
}
