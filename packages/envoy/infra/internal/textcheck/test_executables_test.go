package textcheck

import (
	"strings"
	"testing"

	"github.com/sjawhar/envoy/testexec"
)

// Every file this module writes and then runs, or hands to a program that runs it, is written by
// programfile.Write, or by testexec.WriteFile in a test: in the code a deployment runs, whose
// processes start commands from many goroutines, in every test-support package, and in the tests
// of a package whose tests run beside each other. A write by package os there (os.WriteFile,
// os.OpenFile, or os.Create or os.CreateTemp and a Chmod) can make the run fail with ETXTBSY ("text
// file busy") whenever a goroutine beside it forks while the file is open (docs/testing.md at the
// repository's root, "A file a test runs"). testexec.Unguarded names each such write, with a mode
// it cannot read as no execute bit, as file:line, and refuses a walk that read no Go file.
func TestEveryProgramFileIsWrittenUnderTheForkLock(t *testing.T) {
	offences, err := testexec.Unguarded("../..")
	if err != nil {
		t.Fatalf("read the module's Go files: %v", err)
	}
	if len(offences) > 0 {
		t.Fatalf("%d writes of a file that runs can meet a fork beside it:\n\t%s\n"+
			"write each with programfile.Write, or with testexec.WriteFile in a test (docs/testing.md at the repository's root, \"A file a test runs\")",
			len(offences), strings.Join(offences, "\n\t"))
	}
}
