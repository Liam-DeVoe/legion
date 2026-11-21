package store

import (
	"time"

	"github.com/sjawhar/envoy/internal/clock"
)

// MomentOn is the moment a statement that takes one in place of the database's now() is handed,
// read off c: nil for a nil c, so the statement's own now() stamps and judges, as in production,
// and no process's clock, however far it is off the database's, moves what it writes; and c's
// reading cut to the microsecond the database keeps for any other c, which is a test's fake. The
// docs service's reference excerpt retries (Deps.Clock) and the suggestion sweep's settle
// (settledOn in package api) both take their moment here.
func MomentOn(c clock.Clock) *time.Time {
	if c == nil {
		return nil
	}
	at := c.Now().Truncate(time.Microsecond)
	return &at
}
