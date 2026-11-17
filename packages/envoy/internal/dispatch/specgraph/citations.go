package specgraph

import "fmt"

// CodeTests finds how many tests a check's test part picks in the code at one commit. GoCode is
// the one for runner go.
type CodeTests interface {
	Find(t Test) (int, error)
}

// JobReport is what the citations job knows of one job of its workflow in one leg of its matrix
// (Leg "" for none): whether the job ran, its result being success or failure; whether it left its
// report; and the tests that its report holds.
type JobReport struct {
	Job, Leg       string
	Ran, HasReport bool
	Tests          []TestOutcome
}

// CitationFault is one check that CI refuses at a commit (R48): the check, where its block is, the
// rule it breaks and a Detail that begins with the rule.
type CitationFault struct {
	Check        string
	Pos          Position
	Rule, Detail string
}

// CitationFaults is the faults of one commit, one for each check that breaks a rule.
type CitationFaults []CitationFault

// The rules of R48, each the Rule of a CitationFault.
const (
	citeNoTest           = "no-test"
	citeTestNotInCode    = "test-not-in-code"
	citeTwoTests         = "two-tests"
	citeTestLookupFailed = "test-lookup-failed"
	citeJobNotInWorkflow = "job-not-in-workflow"
	citeJobNotAwaited    = "job-not-awaited"
	citeTestNotRun       = "test-not-run"
)

// Citations finds each program check of t that CI refuses at the commit (R48), in the order of
// t.Checks, with the first rule it breaks of these:
//
//   - no-test: the check names no runner or no selector, or one with no letter and no digit;
//   - test-not-in-code, two-tests: code finds no test or more than one for its test part (R6: "a
//     runner and a selector, which pick one test in the code"); test-lookup-failed: code cannot
//     search for it, as for a package that go cannot list;
//   - job-not-in-workflow, job-not-awaited: jobs has no job of its name, or the citations job
//     does not wait for it and so cannot read its report;
//   - test-not-run: code that the check runs changed and the job did not run its test (R8: "CI
//     runs a check again when that code changed"). The code changed when the check's tie is not
//     known or ties does not hold it (R8: "A check whose tie to its code is unknown runs on each
//     push to the branch"), when allChanged says that the commit has no base commit, or when a
//     path of changed is in the tie at either end: the tie at this commit, ties, or the tie at the
//     base commit of changed, baseTie (the first in sorted order named). The base commit is where
//     each result that this commit carries stands, which for a change's branch is its merge base
//     with the default branch and never its push before: a list from that push misses the changes
//     of the pushes before it. A result carries from the base only while no file that its test
//     read there changed, so a file that the change deleted or moved, which the tie at this commit
//     no longer holds, counts by the base's tie. A base tie that baseTie cannot derive, or no
//     baseTie, counts as changed. baseTie is asked only for a check whose test did not run and
//     whose tie at this commit holds no changed path. Each report of the job, one per leg, must
//     hold the test as a pass or a failure: a job that did not run, left no report, skipped the
//     test, does not hold it, could not run it or replayed it from go test's cache did not run it.
func Citations(t Tree, code CodeTests, jobs WorkflowJobs, reports []JobReport, changed []string, allChanged bool, ties map[string]Tie, baseTie func(Check) Tie) CitationFaults {
	in := citations{code: code, jobs: jobs, reports: reports, changed: changed, allChanged: allChanged, ties: ties, baseTie: baseTie}
	var faults CitationFaults
	for _, c := range t.Checks {
		if c.Procedure != ProcedureProgram {
			continue
		}
		if rule, detail := in.cite(c); rule != "" {
			faults = append(faults, CitationFault{Check: c.ID, Pos: c.Pos, Rule: rule, Detail: rule + ": " + detail})
		}
	}
	return faults
}

// citations is the input of Citations beyond the tree.
type citations struct {
	code       CodeTests
	jobs       WorkflowJobs
	reports    []JobReport
	changed    []string
	allChanged bool
	ties       map[string]Tie
	baseTie    func(Check) Tie
}

// cite is the first rule that the program check c breaks and what it names, or "".
func (in citations) cite(c Check) (rule, detail string) {
	if c.Test == nil || !hasWord(c.Test.Runner) || !hasWord(c.Test.Selector) {
		return citeNoTest, "the check names no test"
	}
	test := *c.Test
	switch n, err := in.code.Find(test); {
	case err != nil:
		return citeTestLookupFailed, fmt.Sprintf("%s could not be looked up in the code at this commit (%v)", test.Selector, err)
	case n == 0:
		return citeTestNotInCode, fmt.Sprintf("%s is not in the code at this commit", test.Selector)
	case n > 1:
		return citeTwoTests, fmt.Sprintf("%s picks %d tests", test.Selector, n)
	}
	if !in.jobs.Jobs[test.Job] {
		return citeJobNotInWorkflow, fmt.Sprintf("no job %q", test.Job)
	}
	if !in.jobs.Awaited[test.Job] {
		return citeJobNotAwaited, fmt.Sprintf("the citations job does not wait for %q, so it cannot read its report", test.Job)
	}
	// The reports first: the base tie is a checkout of the base commit, asked for only when the
	// test did not run.
	job, why := in.notRun(test)
	if why == "" {
		return "", ""
	}
	cause := in.changeCause(c)
	if cause == "" {
		return "", ""
	}
	return citeTestNotRun, fmt.Sprintf("%s, and %s did not run %s (%s)", cause, job, test.Selector, why)
}

// changeCause is why the code that check c runs counts as changed, or "" when it did not change.
func (in citations) changeCause(c Check) string {
	tie, ok := in.ties[c.ID]
	switch {
	case !ok:
		return "its tie is unknown (no tie was derived for the check)"
	case !tie.Known:
		return fmt.Sprintf("its tie is unknown (%s)", tie.Why)
	case in.allChanged:
		return "no base commit"
	}
	if path := in.firstCovered(tie); path != "" {
		return path + " changed"
	}
	if len(in.changed) == 0 {
		return ""
	}
	base := Tie{Why: "no tie at the base commit was given"}
	if in.baseTie != nil {
		base = in.baseTie(c)
	}
	if !base.Known {
		return fmt.Sprintf("its tie at the base commit is unknown (%s)", base.Why)
	}
	if path := in.firstCovered(base); path != "" {
		return path + " changed (the tie at the base commit holds it)"
	}
	return ""
}

// firstCovered is the first path of changed, in sorted order, that tie covers, or "".
func (in citations) firstCovered(tie Tie) string {
	first := ""
	for _, path := range in.changed {
		if tie.Covers(path) && (first == "" || path < first) {
			first = path
		}
	}
	return first
}

// notRun is the job of test, with the first leg in sorted order whose report does not show test
// run, and why it does not; why is "" when every leg ran test, and "the job did not run" when the
// reports hold no leg of the job.
func (in citations) notRun(test Test) (job, why string) {
	reported := false
	var first JobReport
	for _, r := range in.reports {
		if r.Job != test.Job {
			continue
		}
		reported = true
		if legWhy := notRunInLeg(test, r); legWhy != "" && (why == "" || r.Leg < first.Leg) {
			first, why = r, legWhy
		}
	}
	switch {
	case !reported:
		return test.Job, "the job did not run"
	case why == "":
		return "", ""
	case first.Leg != "":
		return fmt.Sprintf("%s (leg %s)", test.Job, first.Leg), why
	}
	return test.Job, why
}

// notRunInLeg is why the report of one leg does not show test run, or "" when it ran: a pass or a
// failure. A runner that MatchTests cannot read (code found the test, so the selector is sound)
// leaves the test in no report that Citations can read.
func notRunInLeg(test Test, leg JobReport) string {
	switch {
	case !leg.Ran:
		return "the job did not run"
	case !leg.HasReport:
		return "the job left no report"
	}
	matched, err := MatchTests(test, leg.Tests)
	if err != nil || len(matched) == 0 {
		return "its report does not hold it"
	}
	for _, o := range matched {
		if o.Outcome == string(OutcomeCouldNotRun) {
			return "it could not run"
		}
	}
	for _, o := range matched {
		if o.Outcome == outcomeCached {
			return "its report holds a result that go test replayed from its cache, which did not run at this commit"
		}
	}
	for _, o := range matched {
		if o.Outcome == "skip" {
			return "its report shows it skipped"
		}
	}
	return ""
}

// Uncited is the rule of each check that f refuses, by check id: the Uncited of the Input of the
// states at the commit (R48: "At that commit, such a check reads unknown, and no earlier result of
// it counts").
func (f CitationFaults) Uncited() map[string]string {
	uncited := make(map[string]string, len(f))
	for _, fault := range f {
		if _, seen := uncited[fault.Check]; !seen {
			uncited[fault.Check] = fault.Rule
		}
	}
	return uncited
}
