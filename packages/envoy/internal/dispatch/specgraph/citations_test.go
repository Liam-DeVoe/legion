package specgraph

import (
	"errors"
	"maps"
	"slices"
	"testing"
)

// codeStub is a CodeTests that answers the count it holds for a selector, and an error for any
// other. citationCode holds what GoCode finds in testdata/citations/repo (TestGoCodeFindsTests).
type codeStub map[string]int

func (c codeStub) Find(t Test) (int, error) {
	n, ok := c[t.Selector]
	if !ok {
		return 0, errors.New("no required module provides package")
	}
	return n, nil
}

var citationCode = codeStub{
	"example.com/citations/a TestA":       1,
	"example.com/citations/a TestRenamed": 0,
	"example.com/citations/b TestB":       2,
}

// citationCheck is the program check of TestCitations: its test is TestA of package a, run by
// go-checks.
var citationCheck = Check{ID: "check-a", Serves: "a-statement", Subject: SubjectCode, Procedure: ProcedureProgram,
	Test: &Test{Runner: "go", Selector: "example.com/citations/a TestA", Job: "go-checks"},
	Pos:  Position{File: "checks.md", Line: 12}}

// citationJobs is the workflow of testdata/citations/workflow.yaml: spec-citations waits for
// changes and go-checks, and not for envoy-go.
var citationJobs = WorkflowJobs{
	Jobs:    map[string]bool{"changes": true, "go-checks": true, "envoy-go": true, "spec-citations": true},
	Awaited: map[string]bool{"changes": true, "go-checks": true},
}

// citationCase is the input of one row of TestCitations.
type citationCase struct {
	tree       Tree
	code       CodeTests
	jobs       WorkflowJobs
	reports    []JobReport
	changed    []string
	allChanged bool
	ties       map[string]Tie
}

// newCitationCase is the base of TestCitations: check-a, whose test is in the code once and whose
// tie is a/* and fixtures/a.json; go-checks ran and its report holds TestA as a pass; nothing
// changed since the base commit.
func newCitationCase(t *testing.T) *citationCase {
	t.Helper()
	tie, err := NewTie([]string{"a/*", "fixtures/a.json"})
	if err != nil {
		t.Fatal(err)
	}
	return &citationCase{
		tree: Tree{Checks: []Check{citationCheck}},
		code: citationCode,
		jobs: citationJobs,
		reports: []JobReport{{Job: "go-checks", Ran: true, HasReport: true,
			Tests: []TestOutcome{{Package: "example.com/citations/a", Test: "TestA", Outcome: "pass"}}}},
		ties: map[string]Tie{citationCheck.ID: tie},
	}
}

// test sets the test part of check-a.
func (c *citationCase) test(selector, job string) {
	check := citationCheck
	check.Test = &Test{Runner: "go", Selector: selector, Job: job}
	c.tree.Checks = []Check{check}
}

// outcome makes go-checks' report hold TestA with outcome.
func (c *citationCase) outcome(outcome string) {
	c.reports = []JobReport{{Job: "go-checks", Ran: true, HasReport: true,
		Tests: []TestOutcome{{Package: "example.com/citations/a", Test: "TestA", Outcome: outcome}}}}
}

// fault is the one fault of check-a with rule and detail.
func fault(rule, detail string) CitationFaults {
	return CitationFaults{{Check: citationCheck.ID, Pos: citationCheck.Pos, Rule: rule, Detail: detail}}
}

// R48: CI fails on a check whose test is not in the code at the commit, and on a check whose test
// did not run although code that the check runs changed (R8). The rows of v-citations, each run
// alone; fixture 4, "the pass at the commit before no longer counts", is a row of TestCheckState.
func TestCitations(t *testing.T) {
	t.Parallel()
	const notRun = "test-not-run"
	for _, row := range []struct {
		name   string
		change func(c *citationCase)
		want   CitationFaults
	}{
		{name: "v-citations 1: the check names a test that someone renamed", change: func(c *citationCase) {
			c.test("example.com/citations/a TestRenamed", "go-checks")
		}, want: fault("test-not-in-code", "test-not-in-code: example.com/citations/a TestRenamed is not in the code at this commit")},
		{name: "v-citations 2: code of the tie changed and the job skipped the test", change: func(c *citationCase) {
			c.changed = []string{"a/a.go"}
			c.outcome("skip")
		}, want: fault(notRun, "test-not-run: a/a.go changed, and go-checks did not run example.com/citations/a TestA (its report shows it skipped)")},
		{name: "v-citations 3: a program check names no test", change: func(c *citationCase) {
			check := citationCheck
			check.Test = nil
			c.tree.Checks = []Check{check}
		}, want: fault("no-test", "no-test: the check names no test")},
		{name: "v-citations 5: no code of the tie changed and the job skipped the test", change: func(c *citationCase) {
			c.changed = []string{"docs/x.md"}
			c.outcome("skip")
		}},
		{name: "v-citations 6: the selector picks two tests", change: func(c *citationCase) {
			c.test("example.com/citations/b TestB", "go-checks")
		}, want: fault("two-tests", "two-tests: example.com/citations/b TestB picks 2 tests")},
		{name: "code of the tie changed and the job did not run", change: func(c *citationCase) {
			c.changed = []string{"a/a.go"}
			c.reports = []JobReport{{Job: "go-checks"}}
		}, want: fault(notRun, "test-not-run: a/a.go changed, and go-checks did not run example.com/citations/a TestA (the job did not run)")},
		{name: "code of the tie changed and nothing says the job ran", change: func(c *citationCase) {
			c.changed = []string{"a/a.go"}
			c.reports = nil
		}, want: fault(notRun, "test-not-run: a/a.go changed, and go-checks did not run example.com/citations/a TestA (the job did not run)")},
		{name: "code of the tie changed and the job ran and uploaded no report", change: func(c *citationCase) {
			c.changed = []string{"a/a.go"}
			c.reports = []JobReport{{Job: "go-checks", Ran: true}}
		}, want: fault(notRun, "test-not-run: a/a.go changed, and go-checks did not run example.com/citations/a TestA (the job left no report)")},
		{name: "B5: a read that the repository records for the test changed", change: func(c *citationCase) {
			c.changed = []string{"fixtures/a.json"}
			c.outcome("skip")
		}, want: fault(notRun, "test-not-run: fixtures/a.json changed, and go-checks did not run example.com/citations/a TestA (its report shows it skipped)")},
		{name: "a file moves out of the tie", change: func(c *citationCase) {
			c.changed = []string{"docs/data.json", "a/data.json"}
			c.outcome("skip")
		}, want: fault(notRun, "test-not-run: a/data.json changed, and go-checks did not run example.com/citations/a TestA (its report shows it skipped)")},
		{name: "the first changed path that the tie covers, sorted", change: func(c *citationCase) {
			c.changed = []string{"fixtures/a.json", "docs/x.md", "a/b.go"}
			c.outcome("skip")
		}, want: fault(notRun, "test-not-run: a/b.go changed, and go-checks did not run example.com/citations/a TestA (its report shows it skipped)")},
		{name: "the check names a job that the citations job does not wait for", change: func(c *citationCase) {
			c.test("example.com/citations/a TestA", "envoy-go")
		}, want: fault("job-not-awaited", `job-not-awaited: the citations job does not wait for "envoy-go", so it cannot read its report`)},
		{name: "the check names a job that the workflow does not have", change: func(c *citationCase) {
			c.test("example.com/citations/a TestA", "gone-job")
		}, want: fault("job-not-in-workflow", `job-not-in-workflow: no job "gone-job"`)},
		{name: "the tie is unknown, nothing changed and the test did not run", change: func(c *citationCase) {
			c.ties = map[string]Tie{citationCheck.ID: {Why: "go list failed"}}
			c.reports = []JobReport{{Job: "go-checks", Ran: true, HasReport: true}}
		}, want: fault(notRun, "test-not-run: its tie is unknown (go list failed), and go-checks did not run example.com/citations/a TestA (its report does not hold it)")},
		{name: "no tie was given for the check", change: func(c *citationCase) {
			c.ties = nil
			c.outcome("skip")
		}, want: fault(notRun, "test-not-run: its tie is unknown (no tie was derived for the check), and go-checks did not run example.com/citations/a TestA (its report shows it skipped)")},
		{name: "the tie is unknown and the test ran", change: func(c *citationCase) {
			c.ties = map[string]Tie{citationCheck.ID: {Why: "go list failed"}}
		}},
		{name: "no base commit and the test ran and passed", change: func(c *citationCase) {
			c.allChanged = true
		}},
		{name: "no base commit and the test ran and failed", change: func(c *citationCase) {
			c.allChanged = true
			c.outcome("fail")
		}},
		{name: "no base commit and the report does not hold the test", change: func(c *citationCase) {
			c.allChanged = true
			c.reports[0].Tests = []TestOutcome{{Package: "example.com/citations/b", Test: "TestA", Outcome: "pass"}}
		}, want: fault(notRun, "test-not-run: no base commit, and go-checks did not run example.com/citations/a TestA (its report does not hold it)")},
		{name: "code of the tie changed and the test could not run", change: func(c *citationCase) {
			c.changed = []string{"a/a.go"}
			c.outcome("could_not_run")
		}, want: fault(notRun, "test-not-run: a/a.go changed, and go-checks did not run example.com/citations/a TestA (it could not run)")},
		{name: "code of the tie changed and one leg of the job skipped the test", change: func(c *citationCase) {
			c.changed = []string{"a/a.go"}
			c.reports = []JobReport{
				{Job: "go-checks", Leg: "amd64", Ran: true, HasReport: true,
					Tests: []TestOutcome{{Package: "example.com/citations/a", Test: "TestA", Outcome: "pass"}}},
				{Job: "go-checks", Leg: "arm64", Ran: true, HasReport: true,
					Tests: []TestOutcome{{Package: "example.com/citations/a", Test: "TestA", Outcome: "skip"}}},
				{Job: "envoy-go", Ran: false},
			}
		}, want: fault(notRun, "test-not-run: a/a.go changed, and go-checks (leg arm64) did not run example.com/citations/a TestA (its report shows it skipped)")},
		{name: "code of the tie changed and two legs did not run the test: the first leg in sorted order", change: func(c *citationCase) {
			c.changed = []string{"a/a.go"}
			c.reports = []JobReport{
				{Job: "go-checks", Leg: "linux", Ran: true},
				{Job: "go-checks", Leg: "darwin", Ran: true, HasReport: true,
					Tests: []TestOutcome{{Package: "example.com/citations/a", Test: "TestA", Outcome: "skip"}}},
			}
		}, want: fault(notRun, "test-not-run: a/a.go changed, and go-checks (leg darwin) did not run example.com/citations/a TestA (its report shows it skipped)")},
		{name: "code of the tie changed and the test ran", change: func(c *citationCase) {
			c.changed = []string{"a/a.go"}
		}},
		{name: "the code cannot be searched for the test", change: func(c *citationCase) {
			c.test("example.com/citations/gone TestA", "go-checks")
		}, want: fault("test-lookup-failed", "test-lookup-failed: example.com/citations/gone TestA could not be looked up in the code at this commit (no required module provides package)")},
		{name: "no program check", change: func(c *citationCase) {
			c.tree.Checks = []Check{checkStateProduction}
			c.changed = []string{"a/a.go"}
			c.reports = nil
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			c := newCitationCase(t)
			row.change(c)
			got := Citations(c.tree, c.code, c.jobs, c.reports, c.changed, c.allChanged, c.ties)
			if !slices.Equal(got, row.want) {
				t.Errorf("Citations =\n%+v\nwant\n%+v", got, row.want)
			}
		})
	}
}

// Each faulty check gets one fault, in the order of the tree, and Uncited maps it to its rule, the
// input of a check's state (Input.Uncited).
func TestCitationFaultsUncited(t *testing.T) {
	t.Parallel()
	c := newCitationCase(t)
	renamed, untested, sound := citationCheck, citationCheck, citationCheck
	renamed.ID, renamed.Test = "check-b", &Test{Runner: "go", Selector: "example.com/citations/a TestRenamed", Job: "gone-job"}
	untested.ID, untested.Test = "check-c", nil
	sound.ID = "check-d"
	c.tree.Checks = []Check{citationCheck, renamed, untested, sound}
	got := Citations(c.tree, c.code, c.jobs, c.reports, c.changed, c.allChanged, c.ties)
	if ids := []string{}; len(got) != 2 || got[0].Check != "check-b" || got[1].Check != "check-c" {
		for _, f := range got {
			ids = append(ids, f.Check+" "+f.Rule)
		}
		t.Fatalf("Citations gave %q; want one fault of check-b and one of check-c", ids)
	}
	want := map[string]string{"check-b": "test-not-in-code", "check-c": "no-test"}
	if uncited := got.Uncited(); !maps.Equal(uncited, want) {
		t.Errorf("Uncited = %v; want %v", uncited, want)
	}
	if uncited := CitationFaults(nil).Uncited(); len(uncited) != 0 {
		t.Errorf("Uncited of no fault = %v; want an empty map", uncited)
	}
}
