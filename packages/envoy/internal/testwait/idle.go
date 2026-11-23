package testwait

import (
	"testing/synctest"
	"time"
)

// This file holds IdleAnHour, the proof of an absence under testing/synctest (the package doc's
// "Proving an absence") for code that a timer of its own may let act late.

// IdleAnHour runs inside a synctest bubble, and synctest.Wait panics outside one. It returns once
// every other goroutine of the bubble is blocked, an hour later on the bubble's clock, so work that
// a bound of its own would let return early has returned by then. That clock moves only once every
// goroutine in the bubble is durably blocked, and synctest.Wait returns before it moves, so an
// absence read after synctest.Wait alone is read at the bubble's first moment: work that a timer of
// its own lets go early, a wait with a bound of its own, has not gone yet and passes. The hour
// costs nothing, and every shorter timer fires inside it, so such work has gone by the read.
func IdleAnHour() {
	synctest.Wait()
	time.Sleep(time.Hour)
}
