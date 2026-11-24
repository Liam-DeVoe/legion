package daemon

import "testing"

// TestOneTestAddedToASplitPackageRunsInOneLeg is c-ci-shape's control, never for merge: one test
// added to internal/daemon, whose tests daemon-go-race's two legs split at run time by a hash of
// each name (.github/scripts/go-test-split.sh), so exactly one leg runs it.
func TestOneTestAddedToASplitPackageRunsInOneLeg(t *testing.T) {
	t.Log("c-ci-shape control: the one test this branch adds ran in this leg")
}
