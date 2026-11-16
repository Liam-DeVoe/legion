package natscallout

import (
	"sync"
	"time"
)

// hubClock reads how long a hub server has waited on a request, to drop undecided a request the
// server has already given up on (answer), and reads it from that server's own clock: never from
// this replica's wall clock, which can run ahead of the hub's or behind it by any skew, and with a
// skew of the server's timeout or more would read every request as given up on.
//
// The server writes two times on each request, both on its own clock and in whole seconds: its
// issue time, when it encoded the request (jwt/v2 claims.go:190), and its expiry, the moment it
// sent it plus its authorization timeout (nats-server server/auth_callout.go:386-387), after which
// it stops waiting for the answer (:462-463). A request's issue time is no later than the server's
// clock when the request reached this replica, so from then on the server's clock reads at least
// that issue time plus the time this replica's monotonic clock has run since: a lower bound on the
// server's clock that no skew enters, since it takes a difference of this replica's clock and a
// reading of the server's. The best such bound comes from the request whose issue time ran
// furthest ahead of its arrival here, and it can be an earlier one: a request this replica held
// before it reached the subscription, unseen in nats.go's list while the delivery goroutine could
// not run, shows the replica none of that time from its own arrival, and the requests the server
// sent before it do. So the clock keeps, per server, the best reading it has (hubReading).
//
// A server is a hub process: each writes its own clock, so a reading of one says nothing of
// another, and a restarted server comes back under a new ID with a reading of its own. A reading
// is kept for hubReadingWindow, after which the next request replaces it: the bound assumes the
// server's clock runs no slower than this replica's monotonic one, which a clock slewed by NTP
// breaks by at most 500 ppm, 30 ms over the window. A server whose clock steps back while a
// reading of it is kept reads as later than it is until the reading is replaced, and its requests
// meanwhile can be dropped early; a step is an operator's act on a running hub, where skew is the
// standing state the drop must not depend on.
type hubClock struct {
	mu       sync.Mutex
	readings map[string]hubReading
}

// hubReading is one reading of a server's clock: a request's issue time, in Unix seconds on that
// server's clock, and when the request reached this replica (deliver), whose monotonic reading the
// bound runs from.
type hubReading struct {
	issued int64
	at     time.Time
}

// hubReadingWindow is how long a reading of a server's clock is kept before the next request from
// that server replaces it, whatever it reads.
const hubReadingWindow = time.Minute

// hubWholeSeconds is what the drop allows for the server writing whole seconds: one for the issue
// time of the reading, which can be up to a second earlier than the server's clock was, and one
// for the request's expiry, which can name a moment up to a second before the server's timeout
// on it ends. With both allowed, a request read as given up on is one the server has given up on.
const hubWholeSeconds = 2

func newHubClock() *hubClock {
	return &hubClock{readings: make(map[string]hubReading)}
}

// gaveUp reports whether server has certainly given up on a request it issued at issued and
// expires at expires, both in Unix seconds on its own clock, which reached this replica at
// received and is read at now. It records the request's own reading of the server's clock first,
// and keeps it when it is the best the clock has.
func (h *hubClock) gaveUp(server string, issued, expires int64, received, now time.Time) bool {
	own := hubReading{issued: issued, at: received}
	h.mu.Lock()
	best, ok := h.readings[server]
	if !ok || now.Sub(best.at) > hubReadingWindow || own.later(best) {
		best = own
		if !ok {
			h.forgetOld(now)
		}
		h.readings[server] = own
	}
	h.mu.Unlock()
	serverNow := float64(best.issued) + now.Sub(best.at).Seconds()
	return serverNow >= float64(expires+hubWholeSeconds)
}

// later reports whether r reads the server's clock later than other does, both carried to the same
// moment on this replica's clock.
func (r hubReading) later(other hubReading) bool {
	return float64(r.issued-other.issued) > r.at.Sub(other.at).Seconds()
}

// forgetOld removes the readings older than hubReadingWindow, which no request uses any more, so a
// hub that restarts, each time under a new server ID, leaves no reading behind. It runs as a new
// server is first read, with h.mu held.
func (h *hubClock) forgetOld(now time.Time) {
	for server, reading := range h.readings {
		if now.Sub(reading.at) > hubReadingWindow {
			delete(h.readings, server)
		}
	}
}
