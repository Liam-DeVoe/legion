package specgraph

import (
	"cmp"
	"slices"
	"time"
)

// Point is the subject that states are derived at (R10): the head of a branch, with Branch set and
// Deploy "", or the commit that production runs, with Deploy set and Branch "". It is not named
// Subject, which is what a check reads.
type Point struct {
	Commit string `json:"commit"`
	Branch string `json:"branch"`
	Deploy string `json:"deploy"`
}

// Input is what the states at one subject are derived from: the spec files at Subject.Commit, the
// approved spec, every stored result, the runs of CI jobs, the tie of each check by its id, the
// history of the repository, the time of the derivation, and the checks that CI refused at
// Subject.Commit (R48), each with the rule that refused it.
type Input struct {
	Files    Tree
	Approved Approved
	Results  []Result
	JobRuns  []JobRun
	Ties     map[string]Tie
	History  History
	Subject  Point
	Now      time.Time
	Uncited  map[string]string
}

// CheckState is the state of one check at the subject (R7): State "true", "false" or "unknown",
// with the Reason code of the step that decided it. From is the commit (production: the deploy;
// the graph: the transaction) of the run that counts, for "true" and "false". Disagree marks
// results that disagree (Reason "disagree"). Detail is what the reason names: the R48 rule for
// "uncited", the statement whose text changed for "statement_changed", and for "ci_fault" the leg
// that left no result, "" when the job left none at all.
type CheckState struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Reason   string `json:"reason"`
	Detail   string `json:"detail"`
	From     string `json:"from"`
	Disagree bool   `json:"disagree"`
}

// The states of a check (R7).
const (
	checkTrue    = "true"
	checkFalse   = "false"
	checkUnknown = "unknown"
)

// The reason codes of a check state, each the step of CheckStates that gives it.
const (
	reasonNotBuilt             = "not_built"
	reasonRefused              = "refused"
	reasonUncited              = "uncited"
	reasonStatementChanged     = "statement_changed"
	reasonCIFault              = "ci_fault"
	reasonSubjectMoved         = "subject_moved"
	reasonNoResult             = "no_result"
	reasonDisagree             = "disagree"
	reasonCouldNotRun          = "could_not_run"
	reasonExpired              = "expired"
	reasonNoFailingExample     = "no_failing_example"
	reasonFailingExampleMissed = "failing_example_missed"
	reasonFaultByTestAuthor    = "fault_by_test_author"
	reasonPassed               = "passed"
	reasonFailed               = "failed"
)

// CheckStates is the state at in.Subject of each check that the approved spec holds a current text
// of, sorted by id. A check that reads production or the graph has no state at the head of a
// branch (R10: "A check whose subject is production decides nothing for the head of a branch").
//
// The state of a check C at subject S comes from the first of these steps that decides; no later
// step overrides an earlier one, and a failure decides only at step 8, after every unknown case of
// R9 ("Five cases read as unknown, and never as true"):
//
//  1. The files do not hold C's current approved text: unknown, not_built. That text names no
//     failing example, a text with no letter and no digit by the rule that Validate reads it with
//     (hasWord): unknown, no_failing_example, whatever C's results (R9: "No setting makes the
//     failing example optional"; "a check whose current text has no failing example that failed
//     when it should" reads unknown). Validate refuses such a text in the files, and the pilot
//     builds the approved spec from the store, with no reader. A problem that Validate finds in
//     the files and that refuses C (refusals): unknown, refused, Detail its rule, since the
//     derivation gives no clean state for a tree that the rules refuse.
//  2. CI refused C at S.Commit (R48): unknown, uncited, and no earlier result counts.
//  3. The candidates are C's results of kind run or failing_example that carry the digest of C's
//     current text (R7: a result counts "only for the procedure that ran it"), a run of a program
//     check only when the check's job reported it (R6: "only the reports of that job count"), and
//     only those recorded at or after the newest approval of C's statement and of each statement
//     above it (R7: "A change to the text of a statement makes every result under it suspect").
//     Results that pass the other tests and are all older than that approval: unknown,
//     statement_changed.
//  4. The runs that count at S. For a code check, a run at S.Commit, or at a commit whose tree
//     differs from S.Commit's in no file of C's tie (R8), every such commit, and with a tie that is
//     not known only runs at S.Commit. For a production check, the runs under S.Deploy; for a graph
//     check, the runs of the newest transaction. No run counts: unknown, ci_fault when a run of C's
//     job ended at S.Commit (R9), else subject_moved when C has a run, else no_result. A code check
//     whose job ended at S.Commit in a leg with no run that counts: unknown, ci_fault (R7: "silence
//     is not a pass"; R12: "Each leg of a matrix reports its own result").
//  5. For a code check: a pass and a failure that count at one commit, a pass that counts recorded
//     after a failure that counts (both read the same code of C's tie), or newest runs of two legs
//     that disagree: unknown, disagree (R7: "A pass and a failure on one such subject and one
//     procedure make the state unknown", "a later pass does not end the report"). A production or
//     graph check skips this step: its newest run decides (R7: "A pass, then a failure, then a pass
//     across deploys is ordinary"). Then a newest run that could not run: unknown, could_not_run.
//  6. C has a time limit and its newest run that counts is older: unknown, expired.
//  7. The newest failing example that counts: for a code check, one that counts at S as a run does
//     (R8: it counts "only while no file that the test reads changed"); for a program check of
//     production or the graph, one whose TestCommit differs from S.Commit in no file of the tie
//     (R9: "its test, as it is now"); for a query or an agent, the newest. None: unknown,
//     no_failing_example; it did not fail: unknown, failing_example_missed; for a program check,
//     its record does not show that an agent that did not write the test wrote the fault (R9): it
//     names no author of the fault, no author of the test, or the author of the fault among the
//     authors of the test: unknown, fault_by_test_author.
//  8. The newest run of each leg passed: true, passed; each failed: false, failed (R7: "the latest
//     counts, and a later failure cancels an earlier pass").
func CheckStates(in Input) []CheckState {
	return newCheckJudge(in).states()
}

// newCheckJudge is the judge of the checks of in: the current approved text of each node, which
// approved text of each node the files hold, the checks and statements of the files that the tree
// rules refuse, and the results of each check.
func newCheckJudge(in Input) *checkJudge {
	current, _ := in.Approved.Current()
	j := &checkJudge{in: in, current: current, holds: Compare(in.Files, in.Approved).Holds, refused: refusals(in.Files),
		results: map[string][]Result{}, changed: map[string]changedPaths{}}
	for _, r := range in.Results {
		j.results[r.Check] = append(j.results[r.Check], r)
	}
	return j
}

// states is the state of each check that CheckStates judges, sorted by id.
func (j *checkJudge) states() []CheckState {
	var states []CheckState
	for _, c := range j.current.Checks {
		if readsProduction(c) && j.in.Subject.Deploy == "" {
			continue
		}
		state := j.state(c)
		state.ID = c.ID
		states = append(states, state)
	}
	return states
}

// checkJudge derives the state of each check of one Input.
type checkJudge struct {
	in      Input
	current Tree                    // the current text of each node of the approved spec
	holds   map[string]Holding      // which approved text of each node the files hold
	refused map[string]string       // the rule that refuses each check and statement of the files
	results map[string][]Result     // the results of each check, by its id
	changed map[string]changedPaths // History.Changed(commit, Subject.Commit), by commit
}

// refusals is the first rule, in Validate's order, that refuses each check and statement of files
// (the derivation gives no clean state for a tree that the rules refuse, since the pilot calls it
// with no reader). A problem that names a check or a statement refuses that node. Any other
// problem, of a component, an interface, a term or no node, refuses each check and statement of
// files: a file is no relation of The Spec, so the split of the tree into files changes no state.
// Validate runs with no Options, as the scenario reader runs it: the files may still hold a node
// that the approved spec removed (Compare).
func refusals(files Tree) map[string]string {
	problems := Validate(files, Options{})
	if len(problems) == 0 {
		return nil
	}
	nodes := make([]string, 0, len(files.Statements)+len(files.Checks))
	for _, s := range files.Statements {
		nodes = append(nodes, s.ID)
	}
	for _, c := range files.Checks {
		nodes = append(nodes, c.ID)
	}
	refused := map[string]string{}
	refuse := func(rule string, ids ...string) {
		for _, id := range ids {
			if _, done := refused[id]; !done {
				refused[id] = rule
			}
		}
	}
	for _, p := range problems {
		if slices.Contains(nodes, p.Node) {
			refuse(p.Rule, p.Node)
		} else {
			refuse(p.Rule, nodes...)
		}
	}
	return refused
}

// changedPaths is one answer of History.Changed.
type changedPaths struct {
	paths []string
	ok    bool
}

// state is the state of c at the subject, in the order of CheckStates.
func (j *checkJudge) state(c Check) CheckState {
	if h := j.holds[c.ID]; !h.Present || !h.Current {
		return unknownCheck(reasonNotBuilt, "")
	}
	if !hasWord(c.FailingExample) {
		return unknownCheck(reasonNoFailingExample, "")
	}
	if rule, refused := j.refused[c.ID]; refused {
		return unknownCheck(reasonRefused, rule)
	}
	if rule, refused := j.in.Uncited[c.ID]; refused {
		return unknownCheck(reasonUncited, rule)
	}

	job := ""
	if c.Test != nil {
		job = c.Test.Job
	}
	statement, since := j.newestApproval(c.Serves)
	digest := c.Digest()
	var runs, faults []Result
	suspect := false
	for _, r := range j.results[c.ID] {
		switch {
		case r.CheckDigest != digest || r.Kind != ResultRun && r.Kind != ResultFailingExample:
		case r.Kind == ResultRun && c.Procedure == ProcedureProgram && r.Job != job:
		case r.At.Before(since):
			suspect = true
		case r.Kind == ResultRun:
			runs = append(runs, r)
		default:
			faults = append(faults, r)
		}
	}
	if suspect && len(runs) == 0 && len(faults) == 0 {
		return unknownCheck(reasonStatementChanged, statement)
	}

	counting := j.countingRuns(c, runs)
	if len(counting) == 0 {
		switch {
		case job != "" && j.jobEnded(job):
			return unknownCheck(reasonCIFault, "")
		case len(runs) > 0:
			return unknownCheck(reasonSubjectMoved, "")
		default:
			return unknownCheck(reasonNoResult, "")
		}
	}
	latest, _ := newestResult(counting)
	newest := map[string]Result{latest.Leg: latest}
	if !readsProduction(c) {
		newest = newestOfEachLeg(counting)
		if leg, silent := j.silentLeg(job, newest); job != "" && silent {
			return unknownCheck(reasonCIFault, leg)
		}
		if runsDisagree(counting, newest) {
			state := unknownCheck(reasonDisagree, "")
			state.Disagree = true
			return state
		}
	}
	for _, r := range newest {
		if r.Outcome != OutcomePass && r.Outcome != OutcomeFail {
			return unknownCheck(reasonCouldNotRun, "")
		}
	}

	if c.TimeLimit > 0 && j.in.Now.Sub(latest.At) > c.TimeLimit {
		return unknownCheck(reasonExpired, "")
	}

	fault, caught := newestResult(j.countingFaults(c, faults))
	switch {
	case !caught:
		return unknownCheck(reasonNoFailingExample, "")
	case fault.Outcome != OutcomeFail:
		return unknownCheck(reasonFailingExampleMissed, "")
	case c.Procedure == ProcedureProgram && !byAnotherAgent(fault):
		return unknownCheck(reasonFaultByTestAuthor, "")
	}

	// After step 5 the newest run of each leg has latest's outcome: latest is the newest of its leg.
	from := latest.Commit
	switch c.Subject {
	case SubjectProduction:
		from = latest.Deploy
	case SubjectGraph:
		from = latest.GraphTx
	}
	if latest.Outcome == OutcomePass {
		return CheckState{State: checkTrue, Reason: reasonPassed, From: from}
	}
	return CheckState{State: checkFalse, Reason: reasonFailed, From: from}
}

// byAnotherAgent is whether the record of the failing example fault shows that an agent that did
// not write the test wrote the fault (R9: "An agent that did not write the test writes the fault"):
// it names the author of the fault and one or more authors of the test, and the author of the fault
// is none of them. A record that names no author of one of the two cannot show it.
func byAnotherAgent(fault Result) bool {
	namesTestAuthor := slices.ContainsFunc(fault.TestAuthors, func(a string) bool { return a != "" })
	return fault.FaultAuthor != "" && namesTestAuthor && !slices.Contains(fault.TestAuthors, fault.FaultAuthor)
}

// newestApproval is the statement, of statement and each statement above it in the approved spec,
// whose current text was approved last, and the time of that approval.
func (j *checkJudge) newestApproval(statement string) (string, time.Time) {
	newest, at := "", time.Time{}
	for seen := map[string]bool{}; statement != "" && !seen[statement]; {
		seen[statement] = true
		if since := j.in.Approved.Since(statement); since.After(at) {
			newest, at = statement, since
		}
		s, held := j.current.StatementByID(statement)
		if !held {
			break
		}
		statement = s.Parent
	}
	return newest, at
}

// countingRuns is the runs of c that count at the subject (step 4).
func (j *checkJudge) countingRuns(c Check, runs []Result) []Result {
	switch c.Subject {
	case SubjectProduction:
		return resultsWhere(runs, func(r Result) bool { return r.Deploy == j.in.Subject.Deploy })
	case SubjectGraph:
		latest, held := newestResult(resultsWhere(runs, func(r Result) bool { return r.GraphTx != "" }))
		return resultsWhere(runs, func(r Result) bool { return held && r.GraphTx == latest.GraphTx })
	}
	return resultsWhere(runs, func(r Result) bool { return j.carries(c.ID, r.Commit) })
}

// countingFaults is the failing examples of c that count at the subject (step 7).
func (j *checkJudge) countingFaults(c Check, faults []Result) []Result {
	switch {
	case !readsProduction(c):
		return resultsWhere(faults, func(r Result) bool { return j.carries(c.ID, r.Commit) })
	case c.Procedure == ProcedureProgram:
		return resultsWhere(faults, func(r Result) bool { return j.carries(c.ID, r.TestCommit) })
	}
	return faults
}

// carries is whether a result at commit counts at the subject's commit for the check id (R8): the
// two are one commit, or the check's tie is known and no file of it differs between their trees.
func (j *checkJudge) carries(check, commit string) bool {
	switch {
	case commit == "":
		return false
	case commit == j.in.Subject.Commit:
		return true
	}
	tie := j.in.Ties[check]
	if !tie.Known || j.in.History == nil {
		return false
	}
	changed, read := j.changed[commit]
	if !read {
		changed.paths, changed.ok = j.in.History.Changed(commit, j.in.Subject.Commit)
		j.changed[commit] = changed
	}
	return changed.ok && !slices.ContainsFunc(changed.paths, tie.Covers)
}

// jobEnded is whether a run of job, in any leg, ended at the subject's commit.
func (j *checkJudge) jobEnded(job string) bool {
	return slices.ContainsFunc(j.in.JobRuns, func(r JobRun) bool {
		return r.Job == job && r.Commit == j.in.Subject.Commit && r.Ended
	})
}

// silentLeg is the first leg, by name, of a run of job that ended at the subject's commit with no
// run of the check that counts.
func (j *checkJudge) silentLeg(job string, newest map[string]Result) (string, bool) {
	var silent []string
	for _, r := range j.in.JobRuns {
		if _, reported := newest[r.Leg]; r.Job == job && r.Commit == j.in.Subject.Commit && r.Ended && !reported {
			silent = append(silent, r.Leg)
		}
	}
	if len(silent) == 0 {
		return "", false
	}
	return slices.Min(silent), true
}

// runsDisagree is whether the runs that count give two answers (step 5): a pass and a failure at
// one commit, a pass recorded after a failure, or newest runs of two legs of which one passed and
// one failed. All of them count at the subject, so they read the same code of the check's tie, and
// only a failure recorded after every pass is a later answer, which cancels them (R7).
func runsDisagree(counting []Result, newest map[string]Result) bool {
	type answers struct{ passed, failed bool }
	byCommit := map[string]answers{}
	failed := false
	for _, r := range slices.SortedFunc(slices.Values(counting), byRecord) {
		a := byCommit[r.Commit]
		switch r.Outcome {
		case OutcomePass:
			if failed {
				return true
			}
			a.passed = true
		case OutcomeFail:
			failed = true
			a.failed = true
		}
		if a.passed && a.failed {
			return true
		}
		byCommit[r.Commit] = a
	}
	var legs answers
	for _, r := range newest {
		legs.passed = legs.passed || r.Outcome == OutcomePass
		legs.failed = legs.failed || r.Outcome == OutcomeFail
	}
	return legs.passed && legs.failed
}

// readsProduction is whether c reads production or the graph rather than the code.
func readsProduction(c Check) bool {
	return c.Subject == SubjectProduction || c.Subject == SubjectGraph
}

// newestOfEachLeg is the newest of results in each leg, by leg.
func newestOfEachLeg(results []Result) map[string]Result {
	newest := map[string]Result{}
	for _, r := range results {
		if old, held := newest[r.Leg]; !held || byRecord(r, old) > 0 {
			newest[r.Leg] = r
		}
	}
	return newest
}

// newestResult is the newest of results by (At, Seq), and false when there is none.
func newestResult(results []Result) (Result, bool) {
	if len(results) == 0 {
		return Result{}, false
	}
	return slices.MaxFunc(results, byRecord), true
}

// byRecord orders results by Dispatch's record time, then its order of record.
func byRecord(a, b Result) int {
	return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.Seq, b.Seq))
}

// resultsWhere is the results that keep accepts, in their order.
func resultsWhere(results []Result, keep func(Result) bool) []Result {
	var kept []Result
	for _, r := range results {
		if keep(r) {
			kept = append(kept, r)
		}
	}
	return kept
}

// unknownCheck is the unknown state of a check with reason and detail.
func unknownCheck(reason, detail string) CheckState {
	return CheckState{State: checkUnknown, Reason: reason, Detail: detail}
}
