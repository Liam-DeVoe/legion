package projectaccess

import "time"

// SetClock makes c read now instead of the wall clock, for the test that the cache keeps an
// answer CacheTTL and no longer.
func SetClock(c *Client, now func() time.Time) { c.now = now }

// InFlight reports whether c has a question to Dispatch under way, for the test that two
// questions in flight at once leave the later one's answer in the cache.
func InFlight(c *Client) bool { return c.inFlight.Load() > 0 }
