package specgraph

import "time"

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
	// deploy of production, or a transaction of the graph. One of the three is set.
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
	// TestCommit is the commit of the test that ran on the failing example of a program check
	// whose subject is production or the graph, "" when the result does not name it (R9: "its
	// test, as it is now").
	TestCommit string
}

// JobRun is one run of a CI job at a commit, in one leg of its matrix ("" for none, the form of
// Result.Leg), and whether the run ended (R9: "after a run of its job ends").
type JobRun struct {
	Job    string `json:"job"`
	Leg    string `json:"leg"`
	Commit string `json:"commit"`
	Ended  bool   `json:"ended"`
}
