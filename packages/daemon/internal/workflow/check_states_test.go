package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/checkstatetest"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// applyStates applies one check.state_changed event of issue at head of repo for each of states,
// the first at serial and each next one at the serial after, through intake.ApplyFact and engine,
// as the live consumer applies a fact (checkstatetest.Apply), never a write to the store.
func applyStates(t *testing.T, pool *pgxpool.Pool, engine *Engine, issue, repo, head string, serial int64, states ...intake.DispatchCheckState) {
	t.Helper()
	checkstatetest.Apply(t, pool, issue, repo, head, serial, states, engine, admissionStub{})
}

// testedAs applies, ahead of a tester's completion of issue's testing that names no serial, the
// state event that decides that completion's move (testedFact), through intake.ApplyFact and
// engine.
func testedAs(t *testing.T, pool *pgxpool.Pool, engine *Engine, issue, head string, passes bool) {
	t.Helper()
	fact, id := testedFact(t, pool, issue, head, passes)
	if result, err := intake.ApplyFact(context.Background(), pool, "dispatch", id, fact, engine, admissionStub{}); err != nil || result.Refusal != nil {
		t.Fatalf("apply the state event %s: %+v, %v", id, result.Refusal, err)
	}
}

// testedFact is the state event that decides a tester's completion of issue's testing, decoded,
// and its event id (checkstatetest.Tested): k1, the head's one check, yes from the tester where
// passes, and no for a fail where not, at head, on statesRepo where issue has no pull request. A
// test that applies its facts through a function of its own applies this one there too.
func testedFact(t *testing.T, pool *pgxpool.Pool, issue, head string, passes bool) (intake.DispatchCheckState, string) {
	t.Helper()
	return checkstatetest.Tested(t, pool, issue, statesRepo, head, passes)
}

// stateOf is check's state with reason, from roles.
func stateOf(check, state, reason string, roles ...string) intake.DispatchCheckState {
	return intake.DispatchCheckState{Check: check, State: state, Reason: reason, Roles: roles}
}

// passOf is check's state yes, from roles.
func passOf(check string, roles ...string) intake.DispatchCheckState {
	return stateOf(check, "yes", "", roles...)
}

// ofSet is state carrying checks as its head's set.
func ofSet(state intake.DispatchCheckState, checks ...string) intake.DispatchCheckState {
	state.Checks = checks
	return state
}

// inCI is state of a check CI proves, with its run's case.
func inCI(state intake.DispatchCheckState, ciCase string) intake.DispatchCheckState {
	state.CICase = ciCase
	return state
}

// statesRepo is the repository LEGION-208's pull request #42 is on.
const statesRepo = "sjawhar/legion"

// statesRig is LEGION-208 at a phase, with its pull request #42 open at headOne and the tester's
// claim on its row, and the engine every fact of the test applies through.
type statesRig struct {
	t      *testing.T
	pool   *pgxpool.Pool
	engine *Engine
}

func newStatesRig(t *testing.T, at phase.Phase) *statesRig {
	t.Helper()
	pool := migratedPool(t)
	seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: statesRepo, DefaultBranch: "main", GitHubRepo: statesRepo, Tree: "LEGION-208", Project: "LEGION",
		Title: "root", Phase: at, Generation: 1, Status: PhaseStatus(at), Rank: "U"})
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: statesRepo, Number: 42, Branch: "legion/LEGION-208",
		HeadSHA: headOne, CheckedHead: headOne, Failing: []string{}})
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleTester, Claim: "test-claim"})
	return &statesRig{t: t, pool: pool, engine: testEngine(config.DesignGateRootIssues, nil)}
}

// states applies states of LEGION-208 at head from serial on (applyStates).
func (r *statesRig) states(head string, serial int64, states ...intake.DispatchCheckState) {
	r.t.Helper()
	applyStates(r.t, r.pool, r.engine, "LEGION-208", statesRepo, head, serial, states...)
}

// complete is the tester's completion of testing at head, its test record carrying serial as the
// serial of the check states it was written after, and no verdict.
func (r *statesRig) complete(head string, serial int64) {
	r.t.Helper()
	result, err := intake.ApplyFact(context.Background(), r.pool, "api", fmt.Sprintf("test-%s-%d", head, serial), intake.HandoffComplete{
		Generation: 1, Issue: "LEGION-208", Role: claim.RoleTester, Claim: "test-claim", Summary: "Every check passes.",
		Commit: head, HandoffRecord: "test-1", HandoffVersion: 1, StatesSerial: serial,
	}, r.engine, admissionStub{})
	if err != nil || result.Refusal != nil {
		r.t.Fatalf("apply the tester's completion: %+v, %v", result.Refusal, err)
	}
}

// held is what the store holds of LEGION-208's checks at head.
func (r *statesRig) held(head string) record.HeadStates {
	r.t.Helper()
	var held record.HeadStates
	seedRecord(r.t, r.pool, func(tx pgx.Tx) error {
		var err error
		held, err = record.NewStore().HeadStates(context.Background(), tx, "LEGION-208", statesRepo, head)
		return err
	})
	return held
}

// finished is every phase-finished notice of LEGION-208, in the order written, each as its
// stored payload.
func (r *statesRig) finished() []map[string]any {
	r.t.Helper()
	var notices []map[string]any
	for _, row := range issueOutboxOf(r.t, r.pool, "LEGION-208", "notice") {
		var notice map[string]any
		if err := json.Unmarshal(row.payload, &notice); err != nil {
			r.t.Fatalf("decode notice %s: %v", row.payload, err)
		}
		if notice["kind"] == "phase-finished" {
			notices = append(notices, notice)
		}
	}
	return notices
}

// starts is the number of worker starts LEGION-208's outbox holds.
func (r *statesRig) starts() int {
	r.t.Helper()
	var starts int
	if err := r.pool.QueryRow(context.Background(), `select count(*) from outbox where kind = 'supervise' and payload->>'op' = 'start'`).Scan(&starts); err != nil {
		r.t.Fatalf("count the starts: %v", err)
	}
	return starts
}

// waitsSaying fails unless LEGION-208 is still in testing, with no worker started and the one
// phase-finished notice of the tester saying reason.
func (r *statesRig) waitsSaying(reason string) {
	r.t.Helper()
	assertPhase(r.t, r.pool, phase.Testing)
	if starts := r.starts(); starts != 0 {
		r.t.Fatalf("the waiting move started %d workers, want none", starts)
	}
	notices := r.finished()
	if len(notices) != 1 || notices[0]["role"] != "tester" || notices[0]["phase"] != "testing" || notices[0]["reason"] != reason ||
		notices[0]["summary"] != "Every check passes." {
		r.t.Fatalf("phase-finished notices = %v, want one of the tester's testing, its summary, saying %q", notices, reason)
	}
}

// movedBackNaming fails unless LEGION-208 went back to implementing with an implementer's task
// that names each of named, and none of unnamed.
func (r *statesRig) movedBackNaming(named []string, unnamed ...string) {
	r.t.Helper()
	assertPhase(r.t, r.pool, phase.Implementing)
	task := startTask(r.t, r.pool, claim.RoleImplementer)
	for _, want := range named {
		if !strings.Contains(task, want) {
			r.t.Fatalf("the implementer's task %q does not name %q", task, want)
		}
	}
	for _, not := range unnamed {
		if strings.Contains(task, not) {
			r.t.Fatalf("the implementer's task %q names %q", task, not)
		}
	}
}

// The rule of a passing check is judgeStates' alone: a check passes when it reads yes at the tested
// head, whatever roles its results came from. A check of the head's set with no state, and a CI run
// still going with no result, wait, since neither is a state the check ends at; every other state
// is conclusive, and one that is not yes fails and is named with why, a state Dispatch could not
// read among them. A state of a check outside the set is no part of the judgement, and no set held
// is no judgement at all.
func TestJudgeStates(t *testing.T) {
	state := func(check, answer, reason, ciCase string, roles ...string) record.CheckState {
		return record.CheckState{Check: check, State: answer, Reason: reason, CICase: ciCase, Roles: roles}
	}
	held := func(checks []string, states ...record.CheckState) record.HeadStates {
		head := record.HeadStates{Checks: checks, States: map[string]record.CheckState{}}
		for _, s := range states {
			head.States[s.Check] = s
		}
		return head
	}
	for _, tc := range []struct {
		name string
		head record.HeadStates
		want testingJudgement
	}{
		{"all yes from tester", held([]string{"k1", "k2"}, state("k1", "yes", "", "", "tester"), state("k2", "yes", "", "", "implementer", "tester")),
			testingJudgement{}},
		{"yes from implementer only", held([]string{"k1"}, state("k1", "yes", "", "", "implementer")),
			testingJudgement{}},
		{"no for fail", held([]string{"k1"}, state("k1", "no", "fail", "")),
			testingJudgement{Failing: []string{"k1: no (fail)"}}},
		{"unknown for pass_and_fail", held([]string{"k1"}, state("k1", "unknown", "pass_and_fail", "")),
			testingJudgement{Failing: []string{"k1: unknown (pass_and_fail)"}}},
		// Dispatch published a state it could not compute: a held, conclusive state that is not
		// yes, so the check does not pass, named with its reason.
		{"read_failed", held([]string{"k1"}, state("k1", "unknown", "read_failed", "")),
			testingJudgement{Failing: []string{"k1: unknown (read_failed)"}}},
		{"a check of the set with no state", held([]string{"k1", "k2"}, state("k1", "yes", "", "", "tester")),
			testingJudgement{Waiting: []string{"k2"}}},
		{"a state of a check outside the set", held([]string{"k1"}, state("k1", "yes", "", "", "tester"), state("k9", "no", "fail", "")),
			testingJudgement{}},
		{"yes from ci", held([]string{"k1"}, state("k1", "yes", "", "success", "ci")),
			testingJudgement{}},
		{"no for no_result with a CI run going on", held([]string{"k1"}, state("k1", "no", "no_result", "not_completed")),
			testingJudgement{Waiting: []string{"k1"}}},
		{"unknown for pass_and_fail with a CI run going on", held([]string{"k1"}, state("k1", "unknown", "pass_and_fail", "not_completed", "ci")),
			testingJudgement{Failing: []string{"k1: unknown (pass_and_fail)"}}},
		{"no for fail with a CI run going on", held([]string{"k1"}, state("k1", "no", "fail", "not_completed")),
			testingJudgement{Failing: []string{"k1: no (fail)"}}},
		{"a fail beside a state Dispatch could not read", held([]string{"k1", "k2"}, state("k1", "no", "fail", ""), state("k2", "unknown", "read_failed", "")),
			testingJudgement{Failing: []string{"k1: no (fail)", "k2: unknown (read_failed)"}}},
		{"a fail beside a check with no state", held([]string{"k1", "k2"}, state("k1", "no", "fail", "")),
			testingJudgement{Waiting: []string{"k2"}, Failing: []string{"k1: no (fail)"}}},
		{"a fail beside a CI run that goes on with no result", held([]string{"k1", "k2"}, state("k1", "no", "fail", ""), state("k2", "no", "no_result", "not_completed")),
			testingJudgement{Waiting: []string{"k2"}, Failing: []string{"k1: no (fail)"}}},
		{"a head with no check", held([]string{}), testingJudgement{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, judged := judgeStates(tc.head)
			if !judged || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("judgeStates = %+v, %t; want %+v, judged", got, judged, tc.want)
			}
		})
	}
	t.Run("no set held", func(t *testing.T) {
		if got, judged := judgeStates(record.HeadStates{States: map[string]record.CheckState{"k1": state("k1", "yes", "", "", "tester")}}); judged {
			t.Fatalf("judgeStates of a head with no set = %+v, judged; want no judgement", got)
		}
	})
}

// Only a check.state_changed event moves the tester: a result.recorded event, here a fail of k1
// at the head where k1 reads yes from the tester, is no state, and changes nothing the daemon
// holds. The tester's completion then moves the issue to reviewing on the states it holds.
func TestOnlyTheStateEventMovesTheTester(t *testing.T) {
	r := newStatesRig(t, phase.Testing)
	r.states(headOne, 1, passOf("k1", "tester"), passOf("k2", "tester"))

	fixture := checkstatetest.ReadFixture(t)
	recorded, err := json.Marshal(map[string]any{
		"id": 9001, "issue_key": "LEGION-208", "seq": 1500, "type": "result.recorded", "notify": false,
		"created_at": "2026-11-21T06:00:00Z", "actor": fixture.Actor, "payload": map[string]any{
			"result_id": "7d1c2b3a-4e5f-4a6b-8c7d-9e0f1a2b3c4d", "issue": "LEGION-208", "check_issue": "LEGION-208",
			"check_artifact_id": "0a3f6c1e-5b2d-4e8a-9c7f-1d2e3f4a5b6c", "check_block_id": "k1", "check_version": 1,
			"when": "before_merge", "verdict": "fail", "role": "tester", "commit_sha": headOne,
		},
	})
	if err != nil {
		t.Fatalf("encode the result.recorded event: %v", err)
	}
	fact, _, err := intake.DecodeDispatchEvent("LEGION", "LEGION-208", recorded)
	if err != nil {
		t.Fatalf("decode the result.recorded event: %v", err)
	}
	if fact != nil {
		if result, err := intake.ApplyFact(context.Background(), r.pool, "dispatch", "dispatch-event:LEGION-208:1500", fact, r.engine, admissionStub{}); err != nil || result.Refusal != nil {
			t.Fatalf("apply the result.recorded event: %+v, %v", result.Refusal, err)
		}
	}
	if k1 := r.held(headOne).States["k1"]; k1.State != "yes" || k1.Seq != checkstatetest.Seq(1) {
		t.Fatalf("after the recorded fail the daemon holds k1 at %s as %+v, want yes at the sequence of its own event, %d", headOne, k1, checkstatetest.Seq(1))
	}
	assertPhase(t, r.pool, phase.Testing)

	r.complete(headOne, 2)
	assertPhase(t, r.pool, phase.Reviewing)
}

// The move reads the states at the head the tester's record names, not at the head of the pull
// request on record: k1 and k2 read yes from the tester at H1, and k2 reads no at H2, where the
// pull request now stands. A record of H1 goes to reviewing, and one of H2 goes back to
// implementing naming k2.
func TestTheMoveReadsTheStatesAtTheTestedHead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tested string
		want   phase.Phase
	}{
		{"the test record names H1", headOne, phase.Reviewing},
		{"the test record names H2", headTwo, phase.Implementing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newStatesRig(t, phase.Testing)
			seedPR(t, r.pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: statesRepo, Number: 42, Branch: "legion/LEGION-208",
				HeadSHA: headTwo, CheckedHead: headTwo, Failing: []string{}})
			r.states(headOne, 1, passOf("k1", "tester"), passOf("k2", "tester"))
			r.states(headTwo, 3, passOf("k1", "tester"), ofSet(stateOf("k2", "no", "fail"), "k1", "k2"))
			r.complete(tc.tested, 4)
			assertPhase(t, r.pool, tc.want)
			if tc.want == phase.Implementing {
				r.movedBackNaming([]string{"k2: no (fail)", headTwo}, "k1:")
			}
		})
	}
}

// noHeadReason is what the architect's notice says of a tester's completion whose test record
// named no head.
const noHeadReason = "the move waits for a tested head: the tester's test record named none, so no check state can decide it; " +
	"the tester writes a new test handoff naming under head the commit it tested and completes again"

// A tester's completion whose test record named no head holds the move and reads no state: the
// head the tester tested is the record's alone, and the pull request's head, where every check
// here reads yes, can be one the tester never tested. The completion tells the architect so in its
// one notice, and a later state event at the pull request's head moves nothing either. The
// tester's completion again, naming a new record of the head it tested, decides the move, as the
// notice says. (The completion route refuses a completion naming no commit, so only a row the
// engine was handed directly, never one a plugin sent, can name none.)
func TestATestRecordNamingNoHeadHoldsTheMove(t *testing.T) {
	r := newStatesRig(t, phase.Testing)
	r.states(headOne, 1, passOf("k1", "tester"), passOf("k2", "tester"))
	r.complete("", 2)
	r.waitsSaying(noHeadReason)
	r.states(headOne, 3, ofSet(passOf("k1", "tester", "implementer"), "k1", "k2"))
	r.waitsSaying(noHeadReason)

	if result, err := intake.ApplyFact(context.Background(), r.pool, "api", "test-again", intake.HandoffComplete{
		Generation: 1, Issue: "LEGION-208", Role: claim.RoleTester, Claim: "test-claim", Summary: "Every check passes.",
		Commit: headOne, HandoffRecord: "test-2", HandoffVersion: 2, StatesSerial: 3,
	}, r.engine, admissionStub{}); err != nil || result.Refusal != nil {
		t.Fatalf("apply the tester's completion naming %s: %+v, %v", headOne, result.Refusal, err)
	}
	assertPhase(t, r.pool, phase.Reviewing)
}

// Neither the reviewer the pass starts nor the architect told of either move reads a verdict of
// the tester: the reviewer's task names the tester's record and no verdict, and the architect's
// phase-finished notice carries the tester's summary and no verdict key. On a fail the notice's
// reason, and the implementer's task, name the check that does not pass.
func TestTheReviewerAndTheArchitectGetNoTesterVerdict(t *testing.T) {
	t.Run("a pass", func(t *testing.T) {
		r := newStatesRig(t, phase.Testing)
		r.states(headOne, 1, passOf("k1", "tester"))
		r.complete(headOne, 1)
		assertPhase(t, r.pool, phase.Reviewing)
		task := startTask(t, r.pool, claim.RoleReviewer)
		if want := "Handoff: test version 1 (dispatch handoff-read --issue LEGION-208 --phase test --version 1). Handoff commit: " + headOne + ". Pull request: #42."; !strings.Contains(task, want) ||
			strings.Contains(strings.ToLower(task), "verdict") {
			t.Fatalf("the reviewer's task %q, want it to name the tester's record (%q) and no verdict", task, want)
		}
		notices := r.finished()
		if len(notices) != 1 || notices[0]["summary"] != "Every check passes." {
			t.Fatalf("phase-finished notices = %v, want the tester's one with its summary", notices)
		}
		if _, carries := notices[0]["verdict"]; carries {
			t.Fatalf("the tester's phase-finished notice %v carries a verdict", notices[0])
		}
	})
	t.Run("a fail", func(t *testing.T) {
		r := newStatesRig(t, phase.Testing)
		r.states(headOne, 1, passOf("k1", "tester"), stateOf("k2", "no", "fail"))
		r.complete(headOne, 2)
		r.movedBackNaming([]string{"k2: no (fail)"}, "k1:", "erdict")
		notices := r.finished()
		want := "these checks do not pass at " + headOne + ": k2: no (fail)"
		if len(notices) != 1 || notices[0]["reason"] != want || notices[0]["summary"] != "Every check passes." {
			t.Fatalf("phase-finished notices = %v, want the tester's one with its summary and the reason %q", notices, want)
		}
		if _, carries := notices[0]["verdict"]; carries {
			t.Fatalf("the tester's phase-finished notice %v carries a verdict", notices[0])
		}
	})
}

// A fail state that arrives while the tester has not completed is held and starts nothing: the
// issue stays in testing, and no implementer is started.
func TestAFailStateBeforeTheTesterCompletesStartsNothing(t *testing.T) {
	r := newStatesRig(t, phase.Testing)
	r.states(headOne, 1, stateOf("k1", "no", "fail"))
	if k1 := r.held(headOne).States["k1"]; k1.State != "no" || k1.Reason != "fail" {
		t.Fatalf("the daemon holds k1 at %s as %+v, want no for fail", headOne, k1)
	}
	assertPhase(t, r.pool, phase.Testing)
	if task := startTask(t, r.pool, claim.RoleImplementer); task != "" || r.starts() != 0 {
		t.Fatalf("the early fail started a worker (the implementer's task %q), want none", task)
	}
	if notices := r.finished(); len(notices) != 0 {
		t.Fatalf("the early fail wrote %v, want no notice", notices)
	}
}

// Each step of the type testing among the steps the issue runs passes when each check reads yes
// at its head, and its run ends passed; a check that reads no ends it failed, returning the work
// to the step a failed step returns to. Today every issue runs the default steps.
func TestATestingStepPassesWhenItsChecksPass(t *testing.T) {
	steps := DefaultSteps()
	implementing, _, _, _ := stepIn(steps, phase.Implementing)
	tested := 0
	for _, p := range phase.All {
		if _, typ, ok := stepWork(p); !ok || typ != "testing" {
			continue
		}
		place, _, _, ok := stepIn(steps, p)
		if !ok {
			continue
		}
		tested++
		for _, tc := range []struct {
			name   string
			states []intake.DispatchCheckState
			want   phase.Phase
			result string
			back   *int
		}{
			{"each check reads yes", []intake.DispatchCheckState{passOf("k1", "tester"), passOf("k2", "tester")}, phase.Reviewing, string(ResultPassed), nil},
			{"a check reads no", []intake.DispatchCheckState{passOf("k1", "tester"), stateOf("k2", "no", "fail")}, phase.Implementing, string(ResultFailed), &implementing.Place},
		} {
			t.Run(fmt.Sprintf("step %d (%s): %s", place.Place, p, tc.name), func(t *testing.T) {
				pool := migratedPool(t)
				engine := stepEngine(nil)
				stepRoot(t, pool, phase.Implementing)
				move(t, pool, engine, "LEGION-208", TriggerImplementationReady, "", record.PhaseRow{})
				assertPhase(t, pool, p)
				applyStates(t, pool, engine, "LEGION-208", statesRepo, headOne, 1, tc.states...)
				if result, err := intake.ApplyFact(context.Background(), pool, "api", "tested", intake.HandoffComplete{
					Generation: 1, Issue: "LEGION-208", Role: claim.RoleTester, Claim: "test-claim", Summary: "tested",
					Commit: headOne, HandoffRecord: "test-1", HandoffVersion: 1, StatesSerial: int64(len(tc.states)),
				}, engine, admissionStub{}); err != nil || result.Refusal != nil {
					t.Fatalf("apply the tester's completion: %+v, %v", result.Refusal, err)
				}
				assertPhase(t, pool, tc.want)
				_, end := runOf(stepWrites(t, pool, "LEGION-208"), place.Kind, place.Place)
				assertEnd(t, end, place.Kind, place.Place, 1, tc.result, tc.back)
			})
		}
	}
	if tested == 0 {
		t.Fatalf("the steps %v hold no step of the type testing, so nothing was tested", steps)
	}
}

// An issue with no before_merge check goes to reviewing when its tester completes: the first
// event of its head is the set form, with no check.
func TestAnIssueWithNoCheckGoesToReview(t *testing.T) {
	r := newStatesRig(t, phase.Testing)
	r.states(headOne, 1)
	if held := r.held(headOne); held.Checks == nil || len(held.Checks) != 0 {
		t.Fatalf("the daemon holds the set %v at %s, want the empty set", held.Checks, headOne)
	}
	r.complete(headOne, 1)
	assertPhase(t, r.pool, phase.Reviewing)
}

// The first events at a new head can be check events alone, with no set form: each carries the
// head's whole set, so the daemon holds the set from them, and the tester's completion moves on
// them.
func TestTheFirstEventsOfAHeadGiveItsSet(t *testing.T) {
	r := newStatesRig(t, phase.Testing)
	r.states(headOne, 1, passOf("k1", "tester"), passOf("k2", "tester"))
	if held := r.held(headOne); !reflect.DeepEqual(held.Checks, []string{"k1", "k2"}) {
		t.Fatalf("the daemon holds the set %v at %s, want [k1 k2] from the check events", held.Checks, headOne)
	}
	r.complete(headOne, 2)
	assertPhase(t, r.pool, phase.Reviewing)
}

// Each pair of a state of the issue and an event the testing move meets, each its own row. A
// completion that waits tells the architect once, in one phase-finished notice naming what it
// waits for; a later event that still leaves it waiting tells nothing more. The move decides only
// once every check of the set holds a conclusive state, so a fail waits while another check has no
// state or a CI run with no result goes on, and the task of a move back names every check that does
// not pass, a state Dispatch could not read among them.
func TestTheTestingMoveStateTable(t *testing.T) {
	fenceReason := "the move waits for Dispatch's check states through serial 7 at " + headOne
	for _, tc := range []struct {
		name  string
		at    phase.Phase
		given func(r *statesRig)
		event func(r *statesRig)
		then  func(r *statesRig)
	}{
		{
			name: "testing, tester not completed | a state of k1 at the tested head | held, nothing moves", at: phase.Testing,
			event: func(r *statesRig) { r.states(headOne, 1, passOf("k1", "tester")) },
			then: func(r *statesRig) {
				if k1 := r.held(headOne).States["k1"]; k1.State != "yes" {
					r.t.Fatalf("the daemon holds k1 as %+v, want yes", k1)
				}
				assertPhase(r.t, r.pool, phase.Testing)
				if r.starts() != 0 || len(r.finished()) != 0 {
					r.t.Fatalf("a state before the completion started %d workers and wrote %v", r.starts(), r.finished())
				}
			},
		},
		{
			name: "testing, tester completed with serial 7, the daemon holds serials through 6 | the completion | waits for the fence, one notice", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 1, passOf("k1", "tester"), passOf("k2", "tester"), passOf("k1", "tester", "implementer"),
					passOf("k2", "tester", "implementer"), passOf("k1", "tester"), passOf("k2", "tester"))
			},
			event: func(r *statesRig) { r.complete(headOne, 7) },
			then:  func(r *statesRig) { r.waitsSaying(fenceReason) },
		},
		{
			name: "testing, tester completed with serial 7, all states yes, serial 7 not held | the event of serial 7, yes from tester | reviewing", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 1, passOf("k1", "tester"), passOf("k2", "tester"), passOf("k1", "tester", "implementer"),
					passOf("k2", "tester", "implementer"), passOf("k1", "tester"), passOf("k2", "tester"))
				r.complete(headOne, 7)
			},
			event: func(r *statesRig) { r.states(headOne, 7, ofSet(passOf("k1", "tester"), "k1", "k2")) },
			then:  func(r *statesRig) { assertPhase(r.t, r.pool, phase.Reviewing) },
		},
		{
			name: "testing, tester completed with serial 7, serials 5 and 7 held, 6 not | the completion | waits for the fence", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 5, passOf("k1", "tester"))
				r.states(headOne, 7, ofSet(passOf("k2", "tester"), "k1", "k2"))
			},
			event: func(r *statesRig) { r.complete(headOne, 7) },
			then:  func(r *statesRig) { r.waitsSaying(fenceReason) },
		},
		{
			name: "testing, tester completed, no set held | the completion | waits, one notice", at: phase.Testing,
			given: func(r *statesRig) { r.states(headTwo, 1, passOf("k1", "tester")) },
			event: func(r *statesRig) { r.complete(headOne, 1) },
			then:  func(r *statesRig) { r.waitsSaying("the move waits for Dispatch's states at " + headOne) },
		},
		{
			name: "testing, tester completed, k1 no for no_result, ci_case not_completed | the completion | waits", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 1, inCI(stateOf("k1", "no", "no_result"), "not_completed"), passOf("k2", "tester"))
			},
			event: func(r *statesRig) { r.complete(headOne, 2) },
			then:  func(r *statesRig) { r.waitsSaying("the move waits for the states of k1 at " + headOne) },
		},
		{
			name: "testing, tester completed, k1 no for no_result, ci_case not_completed | the state of k1, yes from ci | reviewing", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 1, inCI(stateOf("k1", "no", "no_result"), "not_completed"), passOf("k2", "tester"))
				r.complete(headOne, 2)
			},
			event: func(r *statesRig) { r.states(headOne, 3, ofSet(inCI(passOf("k1", "ci"), "success"), "k1", "k2")) },
			then:  func(r *statesRig) { assertPhase(r.t, r.pool, phase.Reviewing) },
		},
		{
			name: "testing, tester completed, k1 unknown for pass_and_fail, ci_case not_completed | the completion | implementing, the task names k1", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 1, inCI(stateOf("k1", "unknown", "pass_and_fail", "ci"), "not_completed"), passOf("k2", "tester"))
			},
			event: func(r *statesRig) { r.complete(headOne, 2) },
			then:  func(r *statesRig) { r.movedBackNaming([]string{"k1: unknown (pass_and_fail)"}, "k2:") },
		},
		{
			name: "testing, tester completed, k1 no for fail, ci_case not_completed | the completion | implementing, the task names k1", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 1, inCI(stateOf("k1", "no", "fail"), "not_completed"), passOf("k2", "tester"))
			},
			event: func(r *statesRig) { r.complete(headOne, 2) },
			then:  func(r *statesRig) { r.movedBackNaming([]string{"k1: no (fail)"}, "k2:") },
		},
		{
			name: "testing, tester completed, k2 has no state | a state of k1 | waits, no second notice", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 1, ofSet(intake.DispatchCheckState{}, "k1", "k2"))
				r.complete(headOne, 1)
			},
			event: func(r *statesRig) { r.states(headOne, 2, ofSet(passOf("k1", "tester"), "k1", "k2")) },
			then:  func(r *statesRig) { r.waitsSaying("the move waits for the states of k1, k2 at " + headOne) },
		},
		{
			name: "testing, tester completed, k2 has no state | the state of k2, yes from tester | reviewing", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 1, ofSet(passOf("k1", "tester"), "k1", "k2"))
				r.complete(headOne, 1)
			},
			event: func(r *statesRig) { r.states(headOne, 2, ofSet(passOf("k2", "tester"), "k1", "k2")) },
			then:  func(r *statesRig) { assertPhase(r.t, r.pool, phase.Reviewing) },
		},
		// Dispatch could not compute k1's state and published that: a held, conclusive state that is
		// not yes, so the issue goes back naming k1 with its reason, and waits for nothing.
		{
			name: "testing, tester completed, k1 unknown for read_failed | the completion | implementing, the task names k1 with read_failed", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 1, stateOf("k1", "unknown", "read_failed"), passOf("k2", "tester"))
			},
			event: func(r *statesRig) { r.complete(headOne, 2) },
			then:  func(r *statesRig) { r.movedBackNaming([]string{"k1: unknown (read_failed)"}, "k2:") },
		},
		{
			name: "testing, tester completed, k1 no for fail, k2 has no state | the completion | waits, the notice names k2", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 1, ofSet(stateOf("k1", "no", "fail"), "k1", "k2"))
			},
			event: func(r *statesRig) { r.complete(headOne, 1) },
			then:  func(r *statesRig) { r.waitsSaying("the move waits for the states of k2 at " + headOne) },
		},
		{
			name: "testing, tester completed, k1 no for fail, k2 has no state | the state of k2, yes from tester | implementing, the task names k1 alone", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 1, ofSet(stateOf("k1", "no", "fail"), "k1", "k2"))
				r.complete(headOne, 1)
			},
			event: func(r *statesRig) { r.states(headOne, 2, ofSet(passOf("k2", "tester"), "k1", "k2")) },
			then:  func(r *statesRig) { r.movedBackNaming([]string{"k1: no (fail)"}, "k2:") },
		},
		{
			name: "testing, tester completed, k1 no for fail, k2 no for no_result, ci_case not_completed | the completion | waits", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 1, stateOf("k1", "no", "fail"), inCI(stateOf("k2", "no", "no_result"), "not_completed"))
			},
			event: func(r *statesRig) { r.complete(headOne, 2) },
			then:  func(r *statesRig) { r.waitsSaying("the move waits for the states of k2 at " + headOne) },
		},
		{
			name: "testing, tester completed | a state of k1, no for fail | implementing, the task names k1", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 1, ofSet(passOf("k2", "tester"), "k1", "k2"))
				r.complete(headOne, 1)
			},
			event: func(r *statesRig) { r.states(headOne, 2, ofSet(stateOf("k1", "no", "fail"), "k1", "k2")) },
			then:  func(r *statesRig) { r.movedBackNaming([]string{"k1: no (fail)"}, "k2:") },
		},
		{
			name: "testing, tester completed | a state at another head | held, nothing moves", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 1, ofSet(passOf("k1", "tester"), "k1", "k2"))
				r.complete(headOne, 1)
			},
			event: func(r *statesRig) { r.states(headTwo, 2, ofSet(passOf("k2", "tester"), "k1", "k2")) },
			then: func(r *statesRig) {
				if k2 := r.held(headTwo).States["k2"]; k2.State != "yes" {
					r.t.Fatalf("the daemon holds k2 at %s as %+v, want yes", headTwo, k2)
				}
				r.waitsSaying("the move waits for the states of k2 at " + headOne)
			},
		},
		{
			name: "reviewing | a state of k1, no for fail | held, nothing moves", at: phase.Reviewing,
			given: func(r *statesRig) {
				seedPhase(r.t, r.pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleTester, Claim: "test-claim", HandoffCommit: headOne,
					HandoffRecord: "test-1", HandoffVersion: 1, LastHandoff: "test-1", Summary: "Every check passes."})
			},
			event: func(r *statesRig) { r.states(headOne, 1, stateOf("k1", "no", "fail")) },
			then: func(r *statesRig) {
				if k1 := r.held(headOne).States["k1"]; k1.State != "no" {
					r.t.Fatalf("the daemon holds k1 as %+v, want no", k1)
				}
				assertPhase(r.t, r.pool, phase.Reviewing)
				if r.starts() != 0 || len(r.finished()) != 0 {
					r.t.Fatalf("a state in reviewing started %d workers and wrote %v", r.starts(), r.finished())
				}
			},
		},
		{
			name: "testing, tester completed | a state with a sequence below the one held | nothing changes", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 2, ofSet(passOf("k1", "tester"), "k1", "k2"))
				r.complete(headOne, 2)
			},
			event: func(r *statesRig) { r.states(headOne, 1, ofSet(stateOf("k1", "no", "fail"), "k1", "k2")) },
			then: func(r *statesRig) {
				if k1 := r.held(headOne).States["k1"]; k1.State != "yes" || k1.Seq != checkstatetest.Seq(2) {
					r.t.Fatalf("the daemon holds k1 as %+v, want yes at sequence %d", k1, checkstatetest.Seq(2))
				}
				r.waitsSaying("the move waits for the states of k2 at " + headOne)
			},
		},
		{
			name: "testing, tester completed, k1 no for fail, k2 unknown for read_failed | the completion | implementing, the task names k1 and k2", at: phase.Testing,
			given: func(r *statesRig) {
				r.states(headOne, 1, stateOf("k1", "no", "fail"), stateOf("k2", "unknown", "read_failed"))
			},
			event: func(r *statesRig) { r.complete(headOne, 2) },
			then:  func(r *statesRig) { r.movedBackNaming([]string{"k1: no (fail)", "k2: unknown (read_failed)"}) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newStatesRig(t, tc.at)
			if tc.given != nil {
				tc.given(r)
			}
			tc.event(r)
			tc.then(r)
		})
	}
}

// stateEventFails is a handler that fails every fact after the workflow applied it, as any later
// handler or the commit can.
type stateEventFails struct{}

func (stateEventFails) Apply(context.Context, pgx.Tx, intake.Fact) (intake.Result, error) {
	return intake.Result{}, errors.New("a later handler failed")
}

// A state event's state, its head's set and its serial are written in the fact's own transaction,
// so they commit with the event's record as processed and never apart from it: a failure after
// the workflow's handler leaves none of them and no processed record, and the event applied again
// leaves all four.
func TestAStateEventIsStoredInTheFactsOwnTransaction(t *testing.T) {
	r := newStatesRig(t, phase.Testing)
	state := intake.DispatchCheckState{Check: "k1", State: "no", Reason: "fail", Roles: []string{"implementer"}, CICase: "not_completed",
		Checks: []string{"k1", "k2"}, Serial: 3}
	fact, _, err := intake.DecodeDispatchEvent("LEGION", "LEGION-208", checkstatetest.Event(t, "LEGION-208", statesRepo, headOne, checkstatetest.Seq(3), state))
	if err != nil {
		t.Fatalf("decode the state event: %v", err)
	}
	const eventID = "dispatch-event:LEGION-208:1003"
	stored := func() (record.HeadStates, bool, int) {
		var held record.HeadStates
		var reached bool
		var processed int
		seedRecord(t, r.pool, func(tx pgx.Tx) error {
			records := record.NewStore()
			var err error
			if held, err = records.HeadStates(context.Background(), tx, "LEGION-208", statesRepo, headOne); err != nil {
				return err
			}
			if reached, err = records.StatesFenceReached(context.Background(), tx, "LEGION-208", 3); err != nil {
				return err
			}
			return tx.QueryRow(context.Background(), `select count(*) from processed_events where source = 'dispatch' and event_id = $1`, eventID).Scan(&processed)
		})
		return held, reached, processed
	}

	if _, err := intake.ApplyFact(context.Background(), r.pool, "dispatch", eventID, fact, r.engine, stateEventFails{}); err == nil {
		t.Fatal("the state event applied with a failing handler after the workflow's, want the failure")
	}
	if held, reached, processed := stored(); held.Checks != nil || len(held.States) != 0 || reached || processed != 0 {
		t.Fatalf("after the failed apply the store holds the set %v, states %v, serial 3 %t and %d processed records; want none of them", held.Checks, held.States, reached, processed)
	}

	if result, err := intake.ApplyFact(context.Background(), r.pool, "dispatch", eventID, fact, r.engine); err != nil || result.Refusal != nil {
		t.Fatalf("apply the state event: %+v, %v", result.Refusal, err)
	}
	held, reached, processed := stored()
	want := record.CheckState{Issue: "LEGION-208", Repo: statesRepo, Head: headOne, Check: "k1", State: "no", Reason: "fail", CICase: "not_completed",
		Roles: []string{"implementer"}, Seq: checkstatetest.Seq(3)}
	if !reflect.DeepEqual(held.Checks, []string{"k1", "k2"}) || !reflect.DeepEqual(held.States, map[string]record.CheckState{"k1": want}) || !reached || processed != 1 {
		t.Fatalf("after the apply the store holds the set %v, states %+v, serial 3 %t and %d processed records; want [k1 k2], k1 %+v, the serial and the record", held.Checks, held.States, reached, processed, want)
	}
}
