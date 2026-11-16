package main

// Red tests of the t-ci-2 acceptance run at b121dfc35f. Each pins one defect the run found on the
// real surface; the reports in testdata/acceptance-*.jsonl are events of real `go test -json`
// runs of the acceptance run's own module (example.com/acc).

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cmdtest"
)

// verdictOf is the --verdict of the stub's call for check, or "" when no call names it.
func verdictOf(calls []call, check string) string {
	for _, c := range calls {
		at := slices.Index(c.args, "--check")
		if at >= 0 && at+1 < len(c.args) && c.args[at+1] == check {
			v := slices.Index(c.args, "--verdict")
			if v >= 0 && v+1 < len(c.args) {
				return c.args[v+1]
			}
		}
	}
	return ""
}

// D3. The step reads the check nodes of the files at its commit (R12). A change of the spec files
// in the working tree that is not committed makes no check of the commit lose its result: either
// the step reports the check as the commit's files name it, or it refuses the tree with no call.
func TestAcceptanceTheStepReadsTheFilesAtItsCommitAndNotTheWorkingTree(t *testing.T) {
	binary := cmdtest.Build(t, "ci-report")
	dir, commit := checkout(t, filepath.Join("testdata", "acceptance-spec"))
	file := filepath.Join(dir, ".dispatch", "spec", "probe.md")
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	const committed = `selector="example.com/acc/probe TestBeta" job="go-checks"`
	if !strings.Contains(string(text), committed) {
		t.Fatalf("the fixture holds no %s", committed)
	}
	// A step before this one rewrites the spec file and commits nothing.
	writeFile(t, file, strings.Replace(string(text), committed, `selector="example.com/acc/probe TestBeta" job="lint"`, 1))
	report, err := filepath.Abs(filepath.Join("testdata", "acceptance-beta-fails.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s := newStub(t)
	got := runReporter(t, binary, dir, s.env("store"),
		"--dir", ".dispatch/spec", "--job", "go-checks", "--report", report, "--project", "ACME", "--dispatch-cli", s.path)
	calls := s.calls(t)
	reportedAsCommitted := verdictOf(calls, "beta-passes") == "fail"
	refused := got.code != 0 && len(calls) == 0
	if !reportedAsCommitted && !refused {
		t.Errorf("at %s the commit's files give beta-passes to job go-checks and its test failed, and the step neither recorded that failure nor refused the tree: exit %d, %d calls (%v)\nstdout:\n%s\nstderr:\n%s",
			commit, got.code, len(calls), calls, got.stdout, got.stderr)
	}
}
