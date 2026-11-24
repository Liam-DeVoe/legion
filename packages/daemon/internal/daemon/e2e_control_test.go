//go:build e2e

package daemon

import "testing"

// TestTheE2ETagControlRunsInNoJob is c-search's negative control A1, never for merge: a test file
// whose build tag, e2e, no step of a job ci-ok needs passes for this package, so the lint job's
// search must fail and name this file and its tag.
func TestTheE2ETagControlRunsInNoJob(t *testing.T) {}
