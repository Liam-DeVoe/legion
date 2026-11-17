package specgraph

import (
	"testing"
	"time"
)

// A member of an Input that the caller did not set is its zero value, and the derivation would read
// that value as a real one: a failure with no time as older than every approval, a run with no
// subject, no job or no test commit as one that counts nowhere, a subject with no now as one at
// which no time limit ends, an approved text with no time as one that makes no result suspect, a
// commit of the history with no files and a known tie with no path as no change between two
// commits, and a job run with no job or no commit as no run of the check's job. Each turns a state
// toward true or hides a failure (R7: "A pass and a failure on one such subject and one procedure
// make the state unknown", "a later failure cancels an earlier pass"), so Derive and CheckStates
// give an error and no state, in the words that the scenario reader refuses the same input with.
// Each row: the case of TestCheckState, at c1, the head of branch b, with the row's checks added,
// its setup, and its change of the Input. The three rows that name a test of their own are that
// test's rows, whose input is refused here: they read unknown before.
func TestTheDerivationRefusesAnInputThatIsNotWhole(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name   string
		add    []Check          // checks that the case approves and the files hold before the Input is made
		setup  func(*checkCase) // a change of the case before the Input is made; nil for none
		change func(in *Input)  // a change of the Input; nil for none
		want   string           // the error; "" for none
	}{
		{name: "control: the case as it is", change: func(in *Input) {}},
		{name: "a result with no at", change: func(in *Input) { in.Results[0].At = time.Time{} },
			want: "specgraph: result 1: it sets no at"},
		{name: "a failing example with no at", change: func(in *Input) { in.Results[1].At = time.Time{} },
			want: "specgraph: result 2: it sets no at"},
		{name: "a result with no check", change: func(in *Input) { in.Results[0].Check = "" },
			want: "specgraph: result 1: it sets no check"},
		{name: "a result with no digest", change: func(in *Input) { in.Results[0].CheckDigest = "" },
			want: "specgraph: result 1: it carries no digest of the text of its check"},
		{name: "a result of another kind", change: func(in *Input) { in.Results[0].Kind = "" },
			want: `specgraph: result 1: kind "" is not run or failing_example`},
		{name: "a result of another outcome", change: func(in *Input) { in.Results[0].Outcome = "" },
			want: `specgraph: result 1: outcome "" is not pass, fail or could_not_run`},
		{name: "a result that names two subjects", change: func(in *Input) { in.Results[0].Deploy = "d1" },
			want: "specgraph: result 1: it names the subjects commit and deploy; a result reads one subject"},
		{name: "a run of a check of the code with no commit", change: func(in *Input) { in.Results[0].Commit = "" },
			want: "specgraph: result 1: it sets no commit, the subject that a check of the code reads"},
		{name: "a failing example of a check of the code with no commit", change: func(in *Input) { in.Results[1].Commit = "" },
			want: "specgraph: result 2: it sets no commit, the subject that a check of the code reads"},
		{name: "a run of a program check with no job", change: func(in *Input) { in.Results[0].Job = "" },
			want: "specgraph: result 1: it sets no job, and only the reports of the job of its check count"},
		{name: "a run of a check of production with no deploy", add: []Check{checkStateProduction}, change: func(in *Input) {
			in.Results = append(in.Results, Result{Check: checkStateProduction.ID, CheckDigest: checkStateProduction.Digest(), Kind: ResultRun,
				Outcome: OutcomeFail, At: checkStateRun, Seq: 3})
		}, want: "specgraph: result 3: it sets no deploy, the subject that a check of production reads"},
		{name: "a run of a check of the graph with no graph_tx", add: []Check{checkStateGraph}, change: func(in *Input) {
			in.Results = append(in.Results, Result{Check: checkStateGraph.ID, CheckDigest: checkStateGraph.Digest(), Kind: ResultRun,
				Outcome: OutcomeFail, Deploy: "d1", At: checkStateRun, Seq: 3})
		}, want: "specgraph: result 3: it sets no graph_tx, the subject that a check of the graph reads"},
		{name: "control: a failing example of a query of production names no subject", add: []Check{checkStateProduction}, change: func(in *Input) {
			in.Results = append(in.Results, Result{Check: checkStateProduction.ID, CheckDigest: checkStateProduction.Digest(), Kind: ResultFailingExample,
				Outcome: OutcomeFail, At: checkStateRun, Seq: 3})
		}},
		{name: "a run of a program check of production with no test_commit", add: []Check{checkStateProductionProgram}, change: func(in *Input) {
			q := checkStateProductionProgram
			in.Results = append(in.Results, Result{Check: q.ID, CheckDigest: q.Digest(), Kind: ResultRun, Outcome: OutcomeFail,
				Deploy: "d1", Job: q.Test.Job, At: checkStateRun, Seq: 3})
		}, want: "specgraph: result 3: it sets no test_commit, the commit of the test that ran"},
		{name: "a failing example of a program check of production with no test_commit", add: []Check{checkStateProductionProgram}, change: func(in *Input) {
			q := checkStateProductionProgram
			in.Results = append(in.Results, Result{Check: q.ID, CheckDigest: q.Digest(), Kind: ResultFailingExample, Outcome: OutcomePass,
				At: checkStateRun, Seq: 3, FaultAuthor: "author-b", TestAuthors: []string{"author-a"}})
		}, want: "specgraph: result 3: it sets no test_commit, the commit of the test that ran"},
		{name: "TestCheckState, item 19: the failing example of a program check of production names no commit of its test",
			setup: func(s *checkCase) { s.productionProgram("docs/x.md", "") },
			want:  "specgraph: result 4: it sets no test_commit, the commit of the test that ran"},
		{name: "TestARunOfProductionOrTheGraphCountsForTheCodeThatRanIt: production, the pass names no commit of its test",
			setup: func(s *checkCase) {
				q := checkStateProductionProgram
				s.add(q)
				s.tieLikeC(q.ID)
				s.commit("c2", "c1", "docs/x.md")
				s.Subject = Point{Commit: "c2", Deploy: "d2", GraphTx: "tx1"}
				s.Results = append(s.Results, Result{Check: q.ID, Kind: ResultRun, Outcome: OutcomePass, Deploy: "d2", Job: q.Test.Job, At: checkStateRun.Add(time.Hour)},
					Result{Check: q.ID, Kind: ResultFailingExample, Outcome: OutcomeFail, TestCommit: "c2",
						At: checkStateRun.Add(65 * time.Minute), FaultAuthor: "author-b", TestAuthors: []string{"author-a"}})
				s.remove(checkStateCheck.ID)
			}, want: "specgraph: result 3: it sets no test_commit, the commit of the test that ran"},
		{name: "TestAGraphCheckReadsTheTransactionOfTheSubject: a pass that names no transaction, at a deploy that names none",
			setup: func(s *checkCase) {
				g := checkStateGraph
				s.add(g)
				s.tieLikeC(g.ID)
				s.commit("c2", "c1", "packages/envoy/cmd/listener/restart_test.go")
				s.Subject = Point{Commit: "c2", Deploy: "d2"}
				s.Results = append(s.Results, Result{Check: g.ID, Kind: ResultRun, Outcome: OutcomePass, At: checkStateRun.Add(time.Hour)},
					Result{Check: g.ID, Kind: ResultFailingExample, Outcome: OutcomeFail, TestCommit: "c2",
						At: checkStateRun.Add(5 * time.Minute), FaultAuthor: "author-b", TestAuthors: []string{"author-a"}})
				s.remove(checkStateCheck.ID)
			}, want: "specgraph: result 3: it sets no graph_tx, the subject that a check of the graph reads"},
		{name: "control: a result of an earlier text of its check is judged by the members that each result sets", change: func(in *Input) {
			in.Results[0].CheckDigest, in.Results[0].Commit, in.Results[0].Job = "an-earlier-text", "", ""
		}},
		{name: "two results of a check at one at and one seq", change: func(in *Input) {
			in.Results[1].At, in.Results[1].Seq = in.Results[0].At, in.Results[0].Seq
		}, want: "specgraph: result 2: it has the at and the seq of result 1, so neither of the two is the later"},
		{name: "a subject with no commit", change: func(in *Input) { in.Subject.Commit = "" },
			want: "specgraph: subject: it sets no commit"},
		{name: "a subject with no now", change: func(in *Input) { in.Now = time.Time{} },
			want: "specgraph: subject: it sets no now"},
		{name: "a subject with neither branch nor deploy", change: func(in *Input) { in.Subject.Branch = "" },
			want: "specgraph: subject: it sets neither branch nor deploy; a subject sets exactly one of them"},
		{name: "an approved text with no time", change: func(in *Input) {
			n := in.Approved.Nodes[checkStateLeaf.ID]
			n.Versions = []ApprovedVersion{{Version: 1, Block: n.Versions[0].Block}}
			in.Approved.Nodes[checkStateLeaf.ID] = n
		}, want: `specgraph: approved node "a-stored-message-survives-a-restart" version 1: it has no time`},
		{name: "an approved node with no version", change: func(in *Input) {
			n := in.Approved.Nodes[checkStateCheck.ID]
			n.Versions = nil
			in.Approved.Nodes[checkStateCheck.ID] = n
		}, want: `specgraph: approved node "a-restart-loses-no-message": it has no version`},
		{name: "an approved text with no block", change: func(in *Input) {
			n := in.Approved.Nodes[checkStateCheck.ID]
			n.Versions = []ApprovedVersion{{Version: 1, At: checkStateApproved}}
			in.Approved.Nodes[checkStateCheck.ID] = n
		}, want: `specgraph: approved node "a-restart-loses-no-message" version 1: bad-approved-block: the block holds 0 nodes, not one (line 1 of its block)`},
		{name: "a commit of the history with no files", change: func(in *Input) {
			in.History = &FixtureHistory{Commits: []FixtureCommit{{ID: "c1"}}}
		}, want: `specgraph: history: commit "c1": it sets no files`},
		{name: "a known tie with no path", change: func(in *Input) { in.Ties = CommitTies{"c1": {checkStateCheck.ID: {Known: true}}} },
			want: `specgraph: ties: commit "c1": check "a-restart-loses-no-message": specgraph: a tie holds no path`},
		{name: "control: a tie that is not known has no path", change: func(in *Input) { in.Ties = CommitTies{"c1": {checkStateCheck.ID: {}}} }},
		{name: "a job run with no job", change: func(in *Input) { in.JobRuns = []JobRun{{Commit: "c1", Ended: true}} },
			want: "specgraph: job run 1: it sets no job"},
		{name: "a job run with no commit", change: func(in *Input) { in.JobRuns = []JobRun{{Job: "go-checks", Ended: true}} },
			want: "specgraph: job run 1: it sets no commit"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			s := newCheckCase(t)
			for _, c := range row.add {
				s.add(c)
			}
			if row.setup != nil {
				row.setup(s)
			}
			in := s.input()
			if row.change != nil {
				row.change(&in)
			}
			state, err := Derive(in)
			checks, checksErr := CheckStates(in)
			if row.want == "" {
				if err != nil || checksErr != nil || len(state.Statements) == 0 || len(checks) == 0 {
					t.Fatalf("Derive: %d statements, error %v; CheckStates: %d checks, error %v; want states and no error",
						len(state.Statements), err, len(checks), checksErr)
				}
				return
			}
			if err == nil || err.Error() != row.want {
				t.Errorf("Derive: error %v, state %+v\nwant the error %q", err, state, row.want)
			}
			if checksErr == nil || checksErr.Error() != row.want {
				t.Errorf("CheckStates: error %v, states %+v\nwant the error %q", checksErr, checks, row.want)
			}
			if state.Checks != nil || state.Statements != nil || checks != nil {
				t.Errorf("with the error, Derive gave %+v and CheckStates %+v; want no state", state, checks)
			}
		})
	}
}

// derive is Derive(in), and fails the test when Derive refuses in.
func derive(t *testing.T, in Input) State {
	t.Helper()
	state, err := Derive(in)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	return state
}

// checkStates is CheckStates(in), and fails the test when CheckStates refuses in.
func checkStates(t *testing.T, in Input) []CheckState {
	t.Helper()
	states, err := CheckStates(in)
	if err != nil {
		t.Fatalf("CheckStates: %v", err)
	}
	return states
}
