// Package testbaseline names the commits a test of the `basebuild` tag compares this build with.
// Each commit is the one an environment variable names, and only that one: no test works it out
// from the repository itself. The run, which knows which branch the work lands on, names each.
//
// BASELINE_SHA (Variable) names the commit this build is compared with. Work done on a branch that
// lands on main compares with `git merge-base origin/main HEAD`. A child stacked on another branch
// compares with that branch's head, and the fork point with main would hold none of the routes,
// tables or rows its parent added, so a child that deleted one of them would pass. The
// Verification document's runs of this change name it as
// `BASELINE_SHA=${BASELINE_SHA:-$(git merge-base origin/main HEAD)}`.
//
// MAIN_BASELINE_SHA (MainVariable) names a second commit, main's build, for a test that serves a
// build of main beside the build BASELINE_SHA names and this one, as a roll across both releases
// serves them (cmd/dispatch's TestALensIsStaleOnceAnotherRowStandsAtItsDocumentsAddress). Its run
// names it as `MAIN_BASELINE_SHA=${MAIN_BASELINE_SHA:-$(git merge-base origin/main HEAD)}`.
//
// It is an ordinary package rather than a _test.go file because cmd/dispatch is package main and
// can import no other package's test helpers, the reason packages/envoy/internal/testtmp gives
// for itself.
package testbaseline

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

const (
	// Variable is the environment variable that names the commit this build is compared with.
	Variable = "BASELINE_SHA"
	// MainVariable is the environment variable that names the commit of main's build.
	MainVariable = "MAIN_BASELINE_SHA"
)

// unsetAdvice is what the failure of each variable left unset tells the run to name.
var unsetAdvice = map[string]string{
	Variable: "name the commit this build is compared with, such as " +
		Variable + "=$(git merge-base origin/main HEAD) for a branch that lands on main, or the " +
		"parent branch's head for a child stacked on it",
	MainVariable: "name the commit of main's build, such as " +
		MainVariable + "=$(git merge-base origin/main HEAD), the newest commit of main this branch holds",
}

// Commit answers the full id of the commit BASELINE_SHA names (CommitOf).
func Commit(t testing.TB) string {
	t.Helper()
	return CommitOf(t, Variable)
}

// CommitOf answers the full id of the commit the environment variable variable names. It fails
// the test at once, naming the variable, when the variable is unset or empty, and when git cannot
// read the commit, as in a shallow clone that does not hold it. It runs git in the test's own
// working directory, which is inside the checkout.
func CommitOf(t testing.TB, variable string) string {
	t.Helper()
	named := strings.TrimSpace(os.Getenv(variable))
	if named == "" {
		advice, known := unsetAdvice[variable]
		if !known {
			advice = "name a commit this checkout holds"
		}
		t.Fatalf("%s is unset: %s", variable, advice)
	}
	out, err := exec.Command("git", "rev-parse", "--verify", "--quiet", named+"^{commit}").Output()
	commit := strings.TrimSpace(string(out))
	if err != nil || commit == "" {
		t.Fatalf("%s=%s names no commit this checkout holds (git rev-parse --verify: %v)", variable, named, err)
	}
	return commit
}
