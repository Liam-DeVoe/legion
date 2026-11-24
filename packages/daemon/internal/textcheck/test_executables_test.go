package textcheck

import (
	"testing"

	"github.com/sjawhar/envoy/testexec"
)

// Every file this module writes and then runs, or hands to a program that runs it, is written
// under the fork lock (docs/testing.md, "A file a test runs"). testexec.RequireGuarded says what
// it reads and how it fails.
func TestEveryProgramFileIsWrittenUnderTheForkLock(t *testing.T) {
	testexec.RequireGuarded(t, "../..")
}
