//go:build !envoytestclock

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A release build of the listener, built without the envoytestclock tag, reads the wall clock for
// a kept copy's hour whatever its environment names (keptCopyClock in kept_clock.go): the file
// ENVOY_TEST_KEPT_CLOCK_OFFSET_FILE names, which moves the clock of a listener built with the tag
// (kept_clock_envoytestclock.go), moves nothing here. The test is built only without the tag, so
// a seam that reached a release build is a failure here.
func TestAReleaseBuildsKeptCopyClockIsTheWallClock(t *testing.T) {
	offset := filepath.Join(t.TempDir(), "offset")
	if err := os.WriteFile(offset, []byte("2h"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENVOY_TEST_KEPT_CLOCK_OFFSET_FILE", offset)
	before := time.Now()
	read := keptCopyClock()()
	after := time.Now()
	if read.Before(before) || read.After(after) {
		t.Fatalf("a release build's kept-copy clock read %s, want the wall clock between %s and %s: the moved clock of the envoytestclock build reached a release build",
			read, before, after)
	}
}
