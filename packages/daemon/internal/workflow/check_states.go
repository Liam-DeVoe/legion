package workflow

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The words of a check state the testing move reads, as Dispatch publishes them in
// check.state_changed (intake.DispatchCheckState).
const (
	stateYes = "yes"
	// reasonNoResult is a check with no result at the head.
	reasonNoResult = "no_result"
	// ciNotCompleted is the case of a check CI proves whose run at the head has not concluded.
	ciNotCompleted = "not_completed"
)

// testingJudgement is what the states the daemon holds at the tested head say of each check of the
// head's set (judgeStates): the checks whose state the move waits for, and each check that does
// not pass with why, as the implementer's task and the architect's notice name it. A judgement
// with neither passes.
type testingJudgement struct {
	// Waiting is each check whose state is not conclusive: one with no state, or with no result
	// while its CI run goes on (ci_case not_completed with the reason no_result).
	Waiting []string
	// Failing is each check with a conclusive state that is not yes, as "k2: no (fail)", "k3:
	// unknown (pass_and_fail)" or "k4: unknown (read_failed)".
	Failing []string
}

// judgeStates judges the states the daemon holds of the head's checks, and is the one place the
// rule of a passing check is written: a check passes when it reads yes at its head, whichever
// results Dispatch counted for it. Every check of the head's set is judged in the set's order, and
// a state of a check outside the set is no part of it, so a check removed from the Verification
// document holds nothing. A check with no state, and one with no result while its CI run goes on,
// wait: neither is a state the check ends at. Every other state is conclusive, and one that is not
// yes fails: a fail, a pass and a fail whatever its CI run says, and a state Dispatch published
// because a read it needed failed (read_failed), which is a state of the check all the same. false
// is no set held at the head, which judges nothing.
func judgeStates(head record.HeadStates) (testingJudgement, bool) {
	if head.Checks == nil {
		return testingJudgement{}, false
	}
	var judgement testingJudgement
	for _, check := range head.Checks {
		state, held := head.States[check]
		switch {
		case !held:
			judgement.Waiting = append(judgement.Waiting, check)
		case state.State == stateYes:
			continue
		case state.Reason == reasonNoResult && state.CICase == ciNotCompleted:
			judgement.Waiting = append(judgement.Waiting, check)
		case state.Reason == "":
			judgement.Failing = append(judgement.Failing, fmt.Sprintf("%s: %s", check, state.State))
		default:
			judgement.Failing = append(judgement.Failing, fmt.Sprintf("%s: %s (%s)", check, state.State, state.Reason))
		}
	}
	return judgement, true
}

// ciState is what the one rule of a check that a CI job runs (ciVerdict) reads of its state at a
// head: what fails there and what passes there.
type ciState int

const (
	// ciUnknownKind is a check of which the daemon holds no state at the head, so whether a CI
	// job runs it is not known yet.
	ciUnknownKind ciState = iota
	// ciNotCI is a check that no CI job runs: its state carries no ci_case.
	ciNotCI
	// ciRunning is a check whose CI job has not ended at the head.
	ciRunning
	// ciPassed is a check whose CI job ended at the head and that reads yes there.
	ciPassed
	// ciFailed is a check whose CI job ended at the head without a pass.
	ciFailed
)

// ciWaitCases are the ci_case words of a job that has not ended at the head, as Dispatch writes
// them: no run yet, a run not concluded, a run not started, no head to read at, and a read of
// GitHub that failed.
var ciWaitCases = []string{"absent", ciNotCompleted, "not_started", "no_head", "github_unreadable"}

// ciVerdict is the one rule of a check that a CI job runs, on state, the state the daemon holds
// of it at a head, held false for none: what fails at a head and what passes there. No state held
// is ciUnknownKind, and a state with no ci_case is ciNotCI. A case of ciWaitCases is ciRunning,
// whatever the state reads, since the job has not ended. Then yes is ciPassed, and every other
// state is ciFailed: the job ended there without a pass, under any word Dispatch writes for its
// end, a word the daemon does not know among them, so no such word waits without end.
func ciVerdict(state record.CheckState, held bool) ciState {
	return ciVerdictWaiting(state, held, ciWaitCases)
}

// ciVerdictWaiting is ciVerdict with waits as the cases of a job that has not ended.
func ciVerdictWaiting(state record.CheckState, held bool, waits []string) ciState {
	switch {
	case !held:
		return ciUnknownKind
	case state.CICase == "":
		return ciNotCI
	case slices.Contains(waits, state.CICase):
		return ciRunning
	case state.State == stateYes:
		return ciPassed
	default:
		return ciFailed
	}
}

// testedHead is where the testing move reads the states: the commit the tester's completion
// reported, the head its test record names, in the repository of the pull request on record. The
// record is the one witness of the head the tester tested: the pull request's head can have moved
// while the tester worked (a merge forward asked of the implementer while the issue stays in
// testing, say), so a completion that reported no commit has no tested head, "", and the move
// reads no state for it (decideTesting). The completion route refuses such a completion
// (TESTED_HEAD_REQUIRED), so only a row the engine was handed directly can name none. Dispatch
// keeps publishing the states at a head the pull request has left while the pull request is open,
// so a result the tester records at the tested head after such a move reaches the move as any
// other state event does.
func testedHead(issue record.Issue, row record.PhaseRow, pr *record.PullRequest) (repo, head string) {
	repo, head = issue.Repo, row.HandoffCommit
	if pr != nil {
		repo = pr.Repo
	}
	return repo, head
}

// decideTesting moves issue out of testing by the states the daemon holds of its checks at the
// tested head, the tester's completion row being row. Nothing of Dispatch is read here: the states
// arrive as check.state_changed events (checkState), and the serial of the tester's test record
// came with its completion (api's handoffRecord).
//
// A completion whose test record named no head (testedHead's "") holds the move and reads no
// state, whatever any head holds: the tester's head is the record's to say, and the tester's
// completing again with a record that names it decides the move then. Otherwise
// it moves nothing until the daemon holds every state event of the issue from the lowest serial it
// holds through the record's (record.Store.StatesFenceReached), since each event the tester's
// results caused has a serial at or below it: below that fence the states held may be the ones from
// before those results. At the fence it decides only once every check of the head's set holds a
// conclusive state there (judgeStates): a head with no set held, or a check the judgement waits
// for, moves nothing, whatever the other checks read, since a later state of the waiting check
// changes what the task must name. Then every check passing moves the issue on to the task's next
// step, and the step the completion named for its task to return to (row.ReturnTo) is not used,
// which the log says once, when the pass moves the issue out of testing. Otherwise the work goes
// back, the task naming each check that does not pass with its state and its reason: to the step
// the completion named (namedReturn), or, where it named none, by the walk's default, the first
// implementation step before the tester (transition's returnOf), implementing for a run with no
// plan rows. atCompletion is whether the tester's completion is the fact deciding: only that one
// tells the architect of a wait, in one phase-finished notice that names what the move waits for,
// so each later state event that leaves the move waiting tells nothing more.
func (e *Engine) decideTesting(ctx context.Context, tx pgx.Tx, issue record.Issue, row record.PhaseRow, pr *record.PullRequest, atCompletion bool) error {
	repo, head := testedHead(issue, row, pr)
	if head == "" {
		return e.testingWaits(ctx, tx, issue, row, head, atCompletion,
			"the move waits for a tested head: the tester's test record named none, so no check state can decide it; "+
				"the tester writes a new test handoff naming under head the commit it tested and completes again")
	}
	reached, err := e.store.StatesFenceReached(ctx, tx, issue.Key, row.StatesSerial)
	if err != nil {
		return err
	}
	if !reached {
		return e.testingWaits(ctx, tx, issue, row, head, atCompletion,
			fmt.Sprintf("the move waits for Dispatch's check states through serial %d at %s", row.StatesSerial, head))
	}
	held, err := e.store.HeadStates(ctx, tx, issue.Key, repo, head)
	if err != nil {
		return err
	}
	judgement, judged := judgeStates(held)
	switch {
	case !judged:
		return e.testingWaits(ctx, tx, issue, row, head, atCompletion, "the move waits for Dispatch's states at "+head)
	case len(judgement.Waiting) > 0:
		return e.testingWaits(ctx, tx, issue, row, head, atCompletion,
			"the move waits for the states of "+strings.Join(judgement.Waiting, ", ")+" at "+head)
	case len(judgement.Failing) > 0:
		target, err := e.namedReturn(ctx, tx, issue, row)
		if err != nil {
			return err
		}
		return e.transition(ctx, tx, issue, TriggerTesterFailed, target, row, pr,
			"these checks do not pass at "+head+": "+strings.Join(judgement.Failing, "; "))
	}
	if err := e.transition(ctx, tx, issue, TriggerTesterPassed, "", row, pr, ""); err != nil || row.ReturnTo <= 0 {
		return err
	}
	// The line that the step named is not used is the pass's move's: it is written once the issue
	// stands out of testing in this transaction, so a pass that moves nothing (a lingering tree, or
	// a line with no testing row) writes none, and a later state event that decides the same pass
	// writes it only with the move it makes. No state event decides a move once the issue has left
	// testing (checkState), so the line is written once.
	moved, err := e.store.Issue(ctx, tx, issue.Key)
	if err != nil {
		return err
	}
	if moved != nil && moved.Phase != phase.Testing {
		e.logOnCommit(ctx, "workflow: the testing step passed, so the step the tester's completion named is not used",
			"issue", issue.Key, "head", head, "returnTo", row.ReturnTo)
	}
	return nil
}

// namedReturn is the phase of the step the tester's completion, row, named for its task to
// return to (row.ReturnTo), which a fail of the check states returns the work to, and "" where
// it named none, which leaves the walk's default (transition's returnOf). The completion's step
// was judged against the task's steps when it was taken (refusedReturn). Should a later version
// of the plan have changed those steps while the move waited, a step they no longer take is
// logged and left, and the default holds.
func (e *Engine) namedReturn(ctx context.Context, tx pgx.Tx, issue record.Issue, row record.PhaseRow) (phase.Phase, error) {
	if row.ReturnTo <= 0 {
		return "", nil
	}
	w, err := e.walk(ctx, tx, issue)
	if err != nil {
		return "", err
	}
	target, why := returnStep(issue, w, issue.Phase, row.ReturnTo)
	if why != "" {
		e.logOnCommit(ctx, "workflow: the step the tester's completion named is no step its task can go back to now, so the walk's default takes the fail",
			"issue", issue.Key, "returnTo", row.ReturnTo, "why", why)
		return "", nil
	}
	return target, nil
}

// testingWaits is the testing move holding issue in testing for reason. At the tester's completion
// it logs the wait and tells the architect, in a phase-finished notice of the tester's carrying
// the tester's summary; at a later state event it does neither, since the architect was told.
func (e *Engine) testingWaits(ctx context.Context, tx pgx.Tx, issue record.Issue, row record.PhaseRow, head string, atCompletion bool, reason string) error {
	if !atCompletion {
		return nil
	}
	e.logOnCommit(ctx, "workflow: the testing move waits for check states", "issue", issue.Key, "head", head,
		"statesSerial", row.StatesSerial, "reason", reason)
	return e.notice(ctx, tx, issue.Key, record.Notice{Kind: "phase-finished", Role: claim.RoleTester, Phase: phase.Testing,
		Summary: row.Summary, Reason: reason})
}

// checkState holds one of Dispatch's check.state_changed events of an issue the workflow records,
// in the fact's own transaction, so the state, the head's set and the serial commit together with
// the event's record as processed, and never apart from it: the head's set of checks every event
// carries (record.Store.PutCheckSet), the state of the check a check event names
// (PutCheckState), each behind the store's guard that a lower Dispatch sequence changes nothing,
// and the event's serial, which the testing move's fence counts (PutCheckStateSerial). An event of
// an issue the workflow does not record is no state of its work and is held nowhere.
//
// While the issue is in testing and its tester has completed, the event decides the testing move
// again (decideTesting), whatever head it names: an event at another head can still be the one the
// fence waits for. In any other phase, and before the tester's completion, it is held and moves
// nothing, so a fail that arrives while the tester works starts no implementer.
func (e *Engine) checkState(ctx context.Context, tx pgx.Tx, fact intake.DispatchCheckState) (intake.Result, error) {
	issue, err := e.store.Issue(ctx, tx, fact.Key)
	if err != nil || issue == nil {
		return intake.Result{}, err
	}
	if _, err := e.store.PutCheckSet(ctx, tx, fact.Key, fact.Repo, fact.Head, fact.Checks, fact.Mark.Seq); err != nil {
		return intake.Result{}, err
	}
	if fact.Check != "" {
		if _, err := e.store.PutCheckState(ctx, tx, record.CheckState{Issue: fact.Key, Repo: fact.Repo, Head: fact.Head, Check: fact.Check,
			State: fact.State, Reason: fact.Reason, CICase: fact.CICase, Roles: fact.Roles, Seq: fact.Mark.Seq}); err != nil {
			return intake.Result{}, err
		}
	}
	if err := e.store.PutCheckStateSerial(ctx, tx, fact.Key, fact.Serial); err != nil {
		return intake.Result{}, err
	}
	e.logOnCommit(ctx, "workflow: a check state is held", "issue", fact.Key, "check", fact.Check, "repo", fact.Repo, "head", fact.Head,
		"state", fact.State, "reason", fact.Reason)
	if issue.Phase != phase.Testing {
		return intake.Result{}, nil
	}
	row, err := e.phaseRow(ctx, tx, issue.Key, claim.RoleTester)
	if err != nil || !row.Completed() {
		return intake.Result{}, err
	}
	pr, err := e.store.PullRequest(ctx, tx, issue.Key)
	if err != nil {
		return intake.Result{}, err
	}
	return intake.Result{}, e.decideTesting(ctx, tx, *issue, row, pr, false)
}
