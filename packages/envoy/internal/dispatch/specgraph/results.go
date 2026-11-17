package specgraph

import (
	"fmt"
	"strings"
	"time"
)

// Outcome is what one result of a check found: its test or query passed, failed, or could not
// run.
type Outcome string

// The three outcomes of a result.
const (
	OutcomePass        Outcome = "pass"
	OutcomeFail        Outcome = "fail"
	OutcomeCouldNotRun Outcome = "could_not_run"
)

// ResultKind is what a result reports: a run of the check on its subject, or a run on its failing
// example (R9), where OutcomeFail means that the check failed on the fault, as it must.
type ResultKind string

// The two kinds of result.
const (
	ResultRun            ResultKind = "run"
	ResultFailingExample ResultKind = "failing_example"
)

// Result is one result of a check as Dispatch stores it (R7, R12): appended and never edited.
type Result struct {
	// Check is the id of the check, and CheckDigest the Digest of the check's text that ran: the
	// procedure of the result (R7).
	Check, CheckDigest string
	Kind               ResultKind
	Outcome            Outcome
	// Commit, Deploy and GraphTx are the subject that the result read: a commit of the code, a
	// deploy of production, or a transaction of the graph; at most one of the three is set. A result
	// counts only at the subject of its kind that the derivation is given (R7: "A result counts only
	// for the procedure that ran it and for the subject that it read").
	Commit, Deploy, GraphTx string
	// Job and Leg are the job that reported the result and its matrix values, "" for none (R12).
	Job, Leg string
	Attempt  int
	// At is Dispatch's record time and Seq its order of record: (At, Seq) orders results.
	At  time.Time
	Seq int64
	// FaultAuthor wrote the fault of a failing example, and TestAuthors the test that ran on it
	// (R9: "An agent that did not write the test writes the fault"). The failing example of a
	// program check counts only when both are named and FaultAuthor is none of TestAuthors.
	FaultAuthor string
	TestAuthors []string
	// TestCommit is the commit of the test that ran, for a run or a failing example of a program
	// check whose subject is production or the graph, and "" for a result of any other check. Such a
	// result counts only while no file of the check's tie differs between TestCommit and the
	// subject's commit (R7: the procedure is "the code that runs it" too; R9: "its test, as it is
	// now").
	TestCommit string
}

// fault is the first member of r that the derivation reads and that r does not set, or does not
// set to a value of the form, as a refusal names it, and "" when r is whole. The one reader of
// results.json and the entry of the derivation (Derive and CheckStates) both refuse a result by it,
// since the zero value of such a member would read as a real one and hide a failure (R7: "A pass
// and a failure on one such subject and one procedure make the state unknown"; "a later failure
// cancels an earlier pass"): a result with no at would be older than every approval, and one with
// no subject, no job or no test commit would count nowhere. Every result sets its check, the
// digest of the text that ran, a kind and an outcome of the format, at most one subject, and at.
// procedure is the text of the check that ran, when the caller holds it, and nil when not; with it,
// r also sets what that text makes the judge read: a run, and a failing example of a check of the
// code, the subject of the check's kind (commit, deploy or graph_tx); a run of a program check that
// names a job, the job; and a result of a program check of production or of the graph, test_commit.
// A failing example of a query or an agent of production or of the graph names no subject, since
// each one counts (CheckStates, step 7).
func (r Result) fault(procedure *Check) string {
	switch named := namedSubjects(r); {
	case r.Check == "":
		return "it sets no check"
	case r.CheckDigest == "":
		return "it carries no digest of the text of its check"
	case r.Kind != ResultRun && r.Kind != ResultFailingExample:
		return fmt.Sprintf("kind %q is not run or failing_example", r.Kind)
	case r.Outcome != OutcomePass && r.Outcome != OutcomeFail && r.Outcome != OutcomeCouldNotRun:
		return fmt.Sprintf("outcome %q is not pass, fail or could_not_run", r.Outcome)
	case len(named) > 1:
		return fmt.Sprintf("it names the subjects %s; a result reads one subject", strings.Join(named, " and "))
	case r.At.IsZero():
		return "it sets no at"
	case procedure == nil:
		return ""
	}
	c, run := *procedure, r.Kind == ResultRun
	switch {
	case (run || c.Subject == SubjectCode) && readOf(c, r.Commit, r.Deploy, r.GraphTx) == "":
		return fmt.Sprintf("it sets no %s, the subject that a check of %s reads",
			readOf(c, "commit", "deploy", "graph_tx"), readOf(c, "the code", "production", "the graph"))
	case run && c.Procedure == ProcedureProgram && checkJob(c) != "" && r.Job == "":
		return "it sets no job, and only the reports of the job of its check count"
	case c.Procedure == ProcedureProgram && readsProduction(c) && r.TestCommit == "":
		return "it sets no test_commit, the commit of the test that ran"
	}
	return ""
}

// namedSubjects is the name of each subject that the result r names, of commit, deploy and
// graph_tx, in that order.
func namedSubjects(r Result) []string {
	var named []string
	for _, s := range []struct{ name, value string }{{"commit", r.Commit}, {"deploy", r.Deploy}, {"graph_tx", r.GraphTx}} {
		if s.value != "" {
			named = append(named, s.name)
		}
	}
	return named
}

// sameRecord is the first pair of results, i before j, of one check at one At and one Seq, which
// the order of record (byRecord) cannot order, and false when there is none: of such a pass and
// such a failure the judge would read the one listed first as the later, so a failure could be
// hidden. The reader of results.json and the entry of the derivation refuse such a pair.
func sameRecord(results []Result) (i, j int, found bool) {
	type record struct {
		check     string
		sec, nsec int64
		seq       int64
	}
	first := make(map[record]int, len(results))
	for j, r := range results {
		key := record{r.Check, r.At.Unix(), int64(r.At.Nanosecond()), r.Seq}
		if i, seen := first[key]; seen {
			return i, j, true
		}
		first[key] = j
	}
	return 0, 0, false
}

// sameRecordFault is the refusal of the pair of results i and j that sameRecord gives.
func sameRecordFault(i, j int) string {
	return fmt.Sprintf("result %d: it has the at and the seq of result %d, so neither of the two is the later", j+1, i+1)
}

// JobRun is one run of a CI job at a commit, in one leg of its matrix ("" for none, the form of
// Result.Leg), and whether the run ended (R9: "after a run of its job ends").
type JobRun struct {
	Job    string `json:"job"`
	Leg    string `json:"leg"`
	Commit string `json:"commit"`
	Ended  bool   `json:"ended"`
}

// fault is the first member of r that the derivation reads and that r does not set, as a refusal
// names it, and "" when r sets each: the job and the commit, since a run of no job or at no commit
// would hide a leg of the check's job that ended with no result (R9: "When a check has no result
// that counts after a run of its job ends, it reads unknown"). Leg "" is the leg of no matrix, and
// Ended false a run that has not ended. The reader of jobs.json and the entry of the derivation
// both refuse a job run by it.
func (r JobRun) fault() string {
	switch {
	case r.Job == "":
		return "it sets no job"
	case r.Commit == "":
		return "it sets no commit"
	}
	return ""
}
