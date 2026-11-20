package brokertoken_test

import (
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/brokertoken"
)

// TestVerifierCarriesHeld (K3): an introspection answer that is active and held reaches both
// checks as Caller.Held, a held answer being a live one: it is kept in the cache as any affirmative
// answer is, so a request and a heartbeat inside AffirmativeCacheTTL read it from there, and the
// broker is asked again once AffirmativeCacheTTL has passed, when its answer that the person is
// no longer held reaches the caller.
//
// Control: callerOf leaving Held false; the held answer reaches Verify not held and the test fails.
func TestVerifierCarriesHeld(t *testing.T) {
	rig := newRig(t)
	caller := newSigner(t)
	const token = "a-token-of-a-person-dispatch-holds"
	held := liveToken(caller.jkt, time.Hour)
	held.Held = true
	rig.broker.Says(token, held)

	for request := range 2 {
		decided, err := rig.verifier.Verify(t.Context(), caller.at(resourcePath, token))
		if err != nil || !decided.Held || decided.Person != held.Person {
			t.Fatalf("Verify %d of a token the broker answers held = %+v, %v; want a caller for %s with Held set", request, decided, err, held.Person)
		}
	}
	if live, err := rig.verifier.Live(t.Context(), caller.at(resourcePath, token)); err != nil || !live.Held {
		t.Fatalf("Live of a token the broker answers held = %+v, %v; want Held set", live, err)
	}
	if rig.broker.Count() != 1 {
		t.Fatalf("two requests and a heartbeat cost %d introspections, want 1: the held answer is kept as any live one", rig.broker.Count())
	}

	released := held
	released.Held = false
	rig.broker.Says(token, released)
	rig.clock.advance(brokertoken.AffirmativeCacheTTL - time.Millisecond)
	if decided, err := rig.verifier.Verify(t.Context(), caller.at(resourcePath, token)); err != nil || !decided.Held || rig.broker.Count() != 1 {
		t.Fatalf("Verify inside AffirmativeCacheTTL after the broker released the person = %+v, %v after %d introspections; want the kept held answer and no new introspection",
			decided, err, rig.broker.Count())
	}
	rig.clock.advance(time.Millisecond)
	if decided, err := rig.verifier.Verify(t.Context(), caller.at(resourcePath, token)); err != nil || decided.Held || rig.broker.Count() != 2 {
		t.Fatalf("Verify once AffirmativeCacheTTL passed = %+v, %v after %d introspections; want the broker asked again and the caller not held",
			decided, err, rig.broker.Count())
	}
}
