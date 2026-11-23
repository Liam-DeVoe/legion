package leakguard

import (
	"errors"
	"strings"
	"testing"
	"unicode"
)

// The write's name and a setup failure's text are written as a refusal's lines are: the name can
// hold a remote's URL or a local path, which git hands the pre-push hook and the push's author
// chose, and a setup failure carries what git wrote on its standard error, which can quote such a
// path. An escape sequence and a byte 0x7f in the name, in the reason and in the failure beneath
// are each escaped (termsafe.Escaped); the failure beneath keeps its own line breaks, each of its
// lines escaped. A *NotFound writes its name the same way.
func TestAWritesNameAndASetupFailureWriteNoControlByteRaw(t *testing.T) {
	const name = "the push to /tmp/a\x1b[2Kb.git"
	written := map[string]string{
		"a refusal": (&Refusal{What: name, By: byRules, Lines: []string{"notes.md: " + theClass}}).Error(),
		"a setup failure": (&SetupFailure{
			What: name,
			Why:  "the push's commits could not be read\x7f",
			Err:  errors.New("git diff-tree exited 128: warning: one\nfatal: bad path 'x\x1b]0;title\x07y'"),
		}).Error(),
		"a not-found answer": (&NotFound{What: name, Repo: "invented-org/invented-repo"}).Error(),
	}
	wants := map[string][]string{
		"a refusal": {"LEAK CHECK: refusing the push to /tmp/a\\x1b[2Kb.git: "},
		"a setup failure": {
			"LEAK CHECK SETUP ERROR: the push to /tmp/a\\x1b[2Kb.git: the push's commits could not be read\\x7f: ",
			"git diff-tree exited 128: warning: one\nfatal: bad path 'x\\x1b]0;title\\x07y'",
		},
		"a not-found answer": {"the push to /tmp/a\\x1b[2Kb.git: GitHub answers 404 for "},
	}
	for kind, text := range written {
		for _, want := range wants[kind] {
			if !strings.Contains(text, want) {
				t.Errorf("%s is %q, want it to hold %q", kind, text, want)
			}
		}
		for _, r := range text {
			if r != '\n' && !unicode.IsPrint(r) {
				t.Errorf("%s writes %U raw: %q", kind, r, text)
			}
		}
	}
}
