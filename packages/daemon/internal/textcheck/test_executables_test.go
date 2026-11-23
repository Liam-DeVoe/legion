package textcheck

import (
	"strings"
	"testing"

	"github.com/sjawhar/envoy/testexec"
)

// Every file a test of this module writes and then runs is written by testexec.WriteFile, in a
// package whose tests run beside each other and in every test-support package: a write by
// os.WriteFile or os.OpenFile there can make the run fail with ETXTBSY ("text file busy") whenever
// a test beside it forks while the file is open (docs/testing.md, "A file a test runs").
// testexec.Unguarded names each such write, with a mode it cannot read as no execute bit, as
// file:line.
func TestEveryFileATestRunsBesideAForkIsWrittenUnderTheForkLock(t *testing.T) {
	offences, err := testexec.Unguarded("../..")
	if err != nil {
		t.Fatalf("read the module's Go files: %v", err)
	}
	if len(offences) > 0 {
		t.Fatalf("%d writes of a file a test runs can meet a fork beside it:\n\t%s\n"+
			"write each with testexec.WriteFile (docs/testing.md, \"A file a test runs\")",
			len(offences), strings.Join(offences, "\n\t"))
	}
}
