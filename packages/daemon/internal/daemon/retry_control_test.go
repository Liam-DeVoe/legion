package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// retryControlFailsFirstAttempt is c-ci-retry's control, never for merge: it fails t on the first
// attempt of its test in a job and passes it on every later one. The first attempt writes a marker
// named for name under the temporary directory, which the runner keeps for the whole job, and
// fails; gotestsum's rerun of the root test finds the marker and passes.
func retryControlFailsFirstAttempt(t *testing.T, name string) {
	t.Helper()
	marker := filepath.Join(os.TempDir(), "legion-retry-control-"+name)
	if _, err := os.Stat(marker); err == nil {
		t.Logf("retry control: %s passes on this attempt, its first attempt having written %s", name, marker)
		return
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatalf("retry control: write the marker %s: %v", marker, err)
	}
	t.Errorf("retry control: %s fails on its first attempt alone (marker %s written)", name, marker)
}
