package dedupe

import (
	"testing"
	"time"
)

// Forward names the newest stored copy recorded for a request, whichever session it went to: not a
// copy recorded for another request, not a delivery recorded with RecordStored alone, and not
// after the TTL.
func TestCache_ForwardNamesTheNewestStoredCopyOfARequest(t *testing.T) {
	clock := newTestClock()
	c := newCache(10*time.Minute, clock.Now)
	defer c.Stop()

	c.RecordStored("key-1", "ses-A", 5)
	if session, _, ok := c.Forward("key-1"); ok {
		t.Fatalf("a delivery recorded by RecordStored alone is a request's forward to %q", session)
	}
	c.RecordForward("alice role-a key-1", "ses-B", 7)
	c.RecordForward("alice role-b key-1", "ses-C", 8)
	clock.Advance(time.Minute)
	if session, seq, ok := c.Forward("alice role-a key-1"); !ok || session != "ses-B" || seq != 7 {
		t.Fatalf("Forward(role a) = %q, %d, %t; want ses-B at 7, not role b's forward", session, seq, ok)
	}
	c.RecordForward("alice role-a key-1", "ses-D", 9)
	if session, seq, ok := c.Forward("alice role-a key-1"); !ok || session != "ses-D" || seq != 9 {
		t.Fatalf("Forward(role a) after a newer forward = %q, %d, %t; want ses-D at 9", session, seq, ok)
	}
	clock.Advance(11 * time.Minute)
	if session, _, ok := c.Forward("alice role-b key-1"); ok {
		t.Fatalf("Forward names %q past the TTL", session)
	}
}
