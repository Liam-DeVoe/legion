// Package ghroutetest holds what the tests of internal/ghroute and the tests of `legion gh`
// (cmd/legion) both read of gh calls: gh's saved help, answered as the real gh answers it
// (SavedHelp); the calls that write nothing and the calls that do (calls.go), which ghroute's
// tests hold its read rule (ghroute.Target.WritesNothing) to and `legion gh`'s tests run end to
// end; and a port of the pinned shared check's own read of a call (PinnedCheckReads), which
// ghroute's tests prove the rule against and `legion gh`'s stand-in check answers with.
//
// It is an ordinary package rather than a _test.go file of one of them because cmd/legion is
// package main, which can import no other package's test-only helpers (internal/leakguard's
// leakguardtest says the same of itself). It imports ghroute not at all, so that ghroute's own
// in-package tests can use it: a file of package ghroute importing a package that imported
// ghroute would be an import cycle.
package ghroutetest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SavedHelp answers gh's help of a command path from dir, which holds the real gh's help saved
// one file per path, its words joined by "_", and gh's own help (`gh --help`) as gh.txt
// (cmd/legion's testdata/gh-help), as the real gh does: a path it has no file for prints the help
// of the nearest parent it has (gh's answer to a subcommand it does not know), and one with no
// such parent at all is an error (gh's `unknown command`, exit 1).
func SavedHelp(dir string) func(path []string) (string, error) {
	return func(path []string) (string, error) {
		if len(path) == 0 {
			data, err := os.ReadFile(filepath.Join(dir, "gh.txt"))
			return string(data), err
		}
		for p := path; len(p) > 0; p = p[:len(p)-1] {
			data, err := os.ReadFile(filepath.Join(dir, strings.Join(p, "_")+".txt"))
			if err == nil {
				return string(data), nil
			}
		}
		return "", fmt.Errorf("unknown command %q for \"gh\"", path[0])
	}
}
