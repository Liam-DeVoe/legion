package routes

import (
	"fmt"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// TestThePasskeyBeginLimiterHoldsEachAddressAndEveryAddress: one address gets passkeyBeginAddressBurst
// begins at once and then one a second; every address together gets passkeyBeginRouteBurst at once,
// so a client spreading its begins over many addresses is held too; and at passkeyBeginAddresses
// remembered addresses the limiter forgets the idle ones rather than grow.
//
// Control: spend from the address's bucket alone (allow skipping l.all), and the begin past the
// route-wide burst is allowed and the row fails.
func TestThePasskeyBeginLimiterHoldsEachAddressAndEveryAddress(t *testing.T) {
	at := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	limiter := newPasskeyBeginLimiter()
	for n := range passkeyBeginAddressBurst {
		if !limiter.allow("198.51.100.7", at) {
			t.Fatalf("begin %d from one address at once refused, want the first %d allowed", n+1, passkeyBeginAddressBurst)
		}
	}
	if limiter.allow("198.51.100.7", at) {
		t.Fatalf("begin %d from one address at once allowed, want it refused", passkeyBeginAddressBurst+1)
	}
	if !limiter.allow("198.51.100.7", at.Add(time.Second)) {
		t.Fatal("a begin from the address a second later refused, want one a second")
	}

	everyAddress := newPasskeyBeginLimiter()
	for n := range passkeyBeginRouteBurst {
		if !everyAddress.allow(fmt.Sprintf("203.0.113.%d:%d", n%250, n), at) {
			t.Fatalf("begin %d over many addresses at once refused, want the first %d allowed", n+1, passkeyBeginRouteBurst)
		}
	}
	if everyAddress.allow("192.0.2.1", at) {
		t.Fatalf("begin %d over many addresses at once allowed, want the route-wide bound to refuse it", passkeyBeginRouteBurst+1)
	}

	remembering := newPasskeyBeginLimiter()
	for n := range passkeyBeginAddresses {
		remembering.byAddress[fmt.Sprintf("address %d", n)] = &passkeyBeginBucket{limiter: nil, seen: at}
	}
	remembering.allow("a new address", at.Add(passkeyBeginIdle))
	if got := len(remembering.byAddress); got != 1 {
		t.Fatalf("at the cap the limiter remembers %d addresses after forgetting the idle ones, want only the new one", got)
	}
}

// TestThePasskeyBeginLimiterRefusesANewAddressAtTheCap: with passkeyBeginAddresses addresses
// remembered and none idle, a begin from a new address is refused, as one over the route-wide
// bound is, and the limiter remembers no more than the cap: it neither grows past it nor sweeps
// the whole map for each new address, but at most once a passkeyBeginIdle, so a new address two
// seconds after a sweep is refused even though the remembered ones have gone idle since. A known
// address still begins, and at the next sweep a new address is remembered in the idle ones' place.
//
// Controls: insert the new address's bucket whatever the map holds (allow without the cap's
// refusal), and the first new address begins and the map holds one more than the cap; sweep for
// every new address (allow without the sweep's interval), and the new address two seconds later
// begins; either way the row fails.
func TestThePasskeyBeginLimiterRefusesANewAddressAtTheCap(t *testing.T) {
	at := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	full := newPasskeyBeginLimiter()
	// Seen a second short of idle at `at`, so idle from a second later.
	seen := at.Add(-passkeyBeginIdle + time.Second)
	for n := range passkeyBeginAddresses {
		full.byAddress[fmt.Sprintf("address %d", n)] = &passkeyBeginBucket{limiter: rate.NewLimiter(passkeyBeginsPerAddress, passkeyBeginAddressBurst), seen: seen}
	}
	if full.allow("a new address", at) {
		t.Errorf("a new address at the cap, none idle, began; want it refused")
	}
	if got := len(full.byAddress); got != passkeyBeginAddresses {
		t.Fatalf("at the cap the limiter remembers %d addresses after a new one asked, want %d", got, passkeyBeginAddresses)
	}
	if !full.allow("address 7", at) {
		t.Errorf("a remembered address at the cap was refused; want it to begin")
	}
	if full.allow("another new address", at.Add(2*time.Second)) {
		t.Errorf("a new address two seconds after a sweep began; want it refused without another sweep")
	}
	if !full.allow("a later new address", at.Add(passkeyBeginIdle)) {
		t.Fatalf("a new address at the next sweep, the remembered ones idle, was refused; want it remembered in their place")
	}
	if got := len(full.byAddress); got > passkeyBeginAddresses {
		t.Fatalf("after the sweep the limiter remembers %d addresses, want at most %d", got, passkeyBeginAddresses)
	}
}
