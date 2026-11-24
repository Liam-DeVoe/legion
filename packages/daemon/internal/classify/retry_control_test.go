package classify

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTheRetryControlFailsOnceAndThenPasses is c-ci-retry's control, never for merge: a Go test
// that fails on its first attempt in a job and passes on the next. The first attempt writes a
// marker under the temporary directory, which the runner keeps for the whole job, and fails;
// gotestsum's rerun finds the marker and passes, so the step passes and ci-ok's summary names the
// test as one that passed only on its retry.
func TestTheRetryControlFailsOnceAndThenPasses(t *testing.T) {
	marker := filepath.Join(os.TempDir(), "legion-retry-control-fails-once")
	if _, err := os.Stat(marker); err == nil {
		t.Logf("retry control: this attempt passes, the first having written %s", marker)
		return
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatalf("retry control: write the marker %s: %v", marker, err)
	}
	t.Errorf("retry control: the first attempt fails (marker %s written)", marker)
}
