// Package testbaseline names the commits a test of the `basebuild` tag compares this build with,
// and builds the server of such a commit. Each commit is the one an environment variable names,
// and only that one: no test works it out from the repository itself. The run, which knows which
// branch the work lands on, names each.
//
// BASELINE_SHA (Variable) names the commit this build is compared with. Work done on a branch that
// lands on main compares with `git merge-base origin/main HEAD`. A child stacked on another branch
// compares with that branch's head, and the fork point with main would hold none of the routes,
// tables or rows its parent added, so a child that deleted one of them would pass.
//
// MAIN_BASELINE_SHA (MainVariable) names a second commit, main's build, for a test that serves a
// build of main beside the build BASELINE_SHA names and this one, as a roll across both releases
// serves them (cmd/dispatch's TestALensIsStaleOnceAnotherRowStandsAtItsDocumentsAddress).
//
// Build is the one build of such a commit that every test of the tag boots, whatever package the
// test stands in, so two tests never build an earlier commit two ways.
//
// It is an ordinary package rather than a _test.go file because cmd/dispatch is package main and
// can import no other package's test helpers, the reason packages/envoy/internal/testtmp gives
// for itself.
package testbaseline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/testtmp"
)

const (
	// Variable is the environment variable that names the commit this build is compared with.
	Variable = "BASELINE_SHA"
	// MainVariable is the environment variable that names the commit of main's build.
	MainVariable = "MAIN_BASELINE_SHA"
)

// Commit answers the full id of the commit BASELINE_SHA names (commitOf).
func Commit(t testing.TB) string {
	t.Helper()
	return commitOf(t, Variable, "name the commit this build is compared with, such as "+
		Variable+"=$(git merge-base origin/main HEAD) for a branch that lands on main, or the "+
		"parent branch's head for a child stacked on it")
}

// MainCommit answers the full id of the commit MAIN_BASELINE_SHA names (commitOf).
func MainCommit(t testing.TB) string {
	t.Helper()
	return commitOf(t, MainVariable, "name the commit of main's build, such as "+
		MainVariable+"=$(git merge-base origin/main HEAD), the newest commit of main this branch holds")
}

// commitOf answers the full id of the commit the environment variable variable names. It fails
// the test at once when the variable is unset or empty, naming the variable and telling the run
// what advice says to name, and when git cannot read the commit, as in a shallow clone that does
// not hold it. It runs git in the test's own working directory, which is inside the checkout.
func commitOf(t testing.TB, variable, advice string) string {
	t.Helper()
	named := strings.TrimSpace(os.Getenv(variable))
	if named == "" {
		t.Fatalf("%s is unset: %s", variable, advice)
	}
	out, err := exec.Command("git", "rev-parse", "--verify", "--quiet", named+"^{commit}").Output()
	commit := strings.TrimSpace(string(out))
	if err != nil || commit == "" {
		t.Fatalf("%s=%s names no commit this checkout holds (git rev-parse --verify: %v)", variable, named, err)
	}
	return commit
}

// Build builds `./cmd/dispatch` at commit, the commit the environment variable variable names
// (Commit, MainCommit), and answers the binary. The source is that commit's own tree, taken out of
// the repository with `git archive` into a directory of this test's own, so the build is that
// commit's and not this working copy's. The binary is stamped with that commit as the image build
// stamps one, `-ldflags "-X main.buildCommit=<commit>"`, so the process registers and reports the
// build it is (processes.Live reads two unstamped builds as one fleet). The directory and the
// binary go when the test ends, so every run extracts and builds again, which is about a minute.
// git runs in the test's own working directory, which is inside the checkout.
func Build(t testing.TB, commit, variable string) string {
	t.Helper()
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("find the checkout %s is read from: git rev-parse --show-toplevel: %v", commit, err)
	}
	root := strings.TrimSpace(string(top))
	// No socket is bound anywhere under the tree, so the room asked for is the directory the
	// build runs in rather than the longest path of the archive.
	source := testtmp.Dir(t, filepath.Join("packages", "envoy"))
	archive := exec.Command("git", "-C", root, "archive", commit)
	extract := exec.Command("tar", "-x", "-C", source)
	pipe, err := archive.StdoutPipe()
	if err != nil {
		t.Fatalf("read the tree of %s: %v", commit, err)
	}
	// The archive's stdout is the pipe tar reads, so its own failure is read from its stderr
	// alone: asking for both, as CombinedOutput does, is `exec: Stdout already set` and the
	// build never runs.
	var problems strings.Builder
	archive.Stderr = &problems
	extract.Stdin = pipe
	if err := extract.Start(); err != nil {
		t.Fatalf("extract the tree of %s: %v", commit, err)
	}
	if err := archive.Start(); err != nil {
		t.Fatalf("read the tree of %s: %v", commit, err)
	}
	if err := archive.Wait(); err != nil {
		t.Fatalf("read the tree of %s: %v\n%s", commit, err, problems.String())
	}
	if err := extract.Wait(); err != nil {
		t.Fatalf("extract the tree of %s: %v", commit, err)
	}
	binary := cmdtest.BuildCommand(t, cmdtest.Command{
		Name:   "envoy-dispatch-base",
		Dir:    filepath.Join(source, "packages", "envoy"),
		Target: "./cmd/dispatch",
		Flags:  []string{"-ldflags", "-X main.buildCommit=" + commit},
		Env:    os.Environ(),
	})
	t.Logf("the older build is %s, from %s, the commit %s names, stamped with that commit", binary, commit, variable)
	return binary
}
