package testprereq

import "testing"

// TestTheRetryControlFailsOnBothAttempts is c-ci-retry's control, never for merge: a Go test that
// fails on every attempt, so the retry runs it twice and the envoy-go leg that holds it fails, with
// envoy-go / envoy-go, the job that carries the split job's old name, and ci-ok.
func TestTheRetryControlFailsOnBothAttempts(t *testing.T) {
	t.Error("retry control: this test fails on every attempt")
}
