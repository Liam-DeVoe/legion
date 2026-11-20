package api

import "time"

// SetPrivateListClock makes the server d registers age the answer GET /v1/private-projects holds
// by now instead of the wall clock, for the test that the answer is served 5 minutes and no longer.
func SetPrivateListClock(d *Deps, now func() time.Time) { d.now = now }
