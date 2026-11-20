//go:build envoytestclock

package main

import (
	"os"
	"strings"
	"time"
)

// keptCopyClockOffsetFileEnv names the file whose Go duration moves the kept-copy clock of a
// listener built with the envoytestclock tag (keptCopyClock).
const keptCopyClockOffsetFileEnv = "ENVOY_TEST_KEPT_CLOCK_OFFSET_FILE"

// keptCopyClock, in a listener built with the envoytestclock tag, is the wall clock moved forward
// by the Go duration the file keptCopyClockOffsetFileEnv names holds, read at each call; a file
// that is unset, missing, empty or not a duration moves it by nothing. Only a test builds the tag:
// the Legion daemon's test of a provider-errors notice whose kept copy outlives its hour moves the
// listener's clock for a kept copy's hour past that hour while the listener runs, so the copy is
// dropped and its dead letter sent at once instead of an hour later
// (packages/daemon/internal/daemon's provider_errors_dead_letter_test.go). Every other clock of the
// listener, the session registry's live window among them, stays the wall clock.
func keptCopyClock() func() time.Time {
	path := os.Getenv(keptCopyClockOffsetFileEnv)
	return func() time.Time {
		now := time.Now()
		if path == "" {
			return now
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return now
		}
		offset, err := time.ParseDuration(strings.TrimSpace(string(raw)))
		if err != nil {
			return now
		}
		return now.Add(offset)
	}
}
