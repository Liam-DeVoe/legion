//go:build !envoytestclock

package main

import "time"

// keptCopyClock is the listener's one clock for a kept copy's hour (listenerDeliveryHandlerConfig.now,
// set in main.go): the wall clock. A listener built with the envoytestclock tag reads a moved one
// instead (kept_clock_envoytestclock.go), which only a test builds; no release is built with it.
func keptCopyClock() func() time.Time {
	return time.Now
}
