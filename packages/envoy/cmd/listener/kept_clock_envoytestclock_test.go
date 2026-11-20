//go:build envoytestclock

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A listener built with the envoytestclock tag reads its kept-copy clock moved by the duration the
// file ENVOY_TEST_KEPT_CLOCK_OFFSET_FILE names holds, read at each call, and by nothing while that
// file holds no duration.
func TestATestClockBuildsKeptCopyClockIsMovedByItsFile(t *testing.T) {
	offset := filepath.Join(t.TempDir(), "offset")
	t.Setenv(keptCopyClockOffsetFileEnv, offset)
	clock := keptCopyClock()
	if moved := clock().Sub(time.Now()); moved < -time.Second || moved > time.Second {
		t.Fatalf("with no file the clock reads %s from the wall clock, want it on the wall clock", moved)
	}
	if err := os.WriteFile(offset, []byte("2h"), 0o600); err != nil {
		t.Fatal(err)
	}
	if moved := clock().Sub(time.Now()); moved < 2*time.Hour-time.Second || moved > 2*time.Hour+time.Second {
		t.Fatalf("with 2h in the file the clock reads %s from the wall clock, want two hours ahead", moved)
	}
}
