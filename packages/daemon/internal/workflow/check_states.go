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
	// reasonReadFailed is a state Dispatch could not compute: a read it needed failed, and its
	// settler tries again.
	reasonReadFailed = "read_failed"
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
	// Waiting is each check with no state, a state Dispatch could not read (read_failed), or no
	// result while its CI run goes on (ci_case not_completed with the reason no_result).
	Waiting []string
	// Failing is each other check that does not pass, as "k2: no (fail)", "k3: unknown
	// (pass_and_fail)" or "k4: yes from implementer, with no pass of the tester".
	Failing []string
}

// judgeStates judges the states the daemon holds of the head's checks, and is the one place the
// rule of a passing check is written: a check passes on the state yes whose roles hold the tester,
// or hold ci for a check CI proves (a state with a ci_case). Every check of the head's set is
// judged in the set's order, and a state of a check outside the set is no part of it, so a check
// removed from the Verification document holds nothing. A check with no state, one whose reason is
// read_failed, and one with no result while its CI run goes on wait; every other check that does
// not pass fails, a pass and a fail or a fail among them whatever its CI run says, since neither
// can end in a pass at that head. false is no set held at the head, which judges nothing.
func judgeStates(head record.HeadStates) (testingJudgement, bool) {
	if head.Checks == nil {
		return testingJudgement{}, false
	}
	var judgement testingJudgement
	for _, check := range head.Checks {
		state, held := head.States[check]
		switch {
		case !held || state.Reason == reasonReadFailed:
			judgement.Waiting = append(judgement.Waiting, check)
		case state.State == stateYes:
			if slices.Contains(state.Roles, string(claim.RoleTester)) || state.CICase != "" && slices.Contains(state.Roles, ciRole) {
				continue
			}
			from := "no role"
			if len(state.Roles) > 0 {
				from = strings.Join(state.Roles, ", ")
			}
			judgement.Failing = append(judgement.Failing, fmt.Sprintf("%s: yes from %s, with no pass of the tester", check, from))
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

// ciRole is the role Dispatch names on a result CI's run derived, the producer of a check CI
// proves.
const ciRole = "ci"

// testedHead is where the testing move reads the states: the commit the tester's completion
// reported, the head its test record names, in the repository of the pull request on record; with
// no commit reported, the pull request's head.
func testedHead(issue record.Issue, row record.PhaseRow, pr *record.PullRequest) (repo, head string) {
	repo, head = issue.Repo, row.HandoffCommit
	if pr != nil {
		repo = pr.Repo
		if head == "" {
			head = pr.HeadSHA
		}
	}
	return repo, head
}

// decideTesting moves issue out of testing by the states the daemon holds of its checks at the
// tested head, the tester's completion row being row. Nothing of Dispatch is read here: the states
// arrive as check.state_changed events (checkState), and the serial of the tester's test record
// came with its completion (api's handoffRecord).
//
// It moves nothing until the daemon holds every state event of the issue from the lowest serial it
// holds through the record's (record.Store.StatesFenceReached), since each event the tester's
// results caused has a serial at or below it: below that fence the states held may be the ones from
// before those results. At the fence, a check that fails sends the issue back to implementing at
// once, the task naming each check that does not pass, even while another check of the set waits,
// since no state of the waiting check makes the round a pass. With nothing failing, a head with no
// set held, or a check the judgement waits for, moves nothing; otherwise the issue goes to
// reviewing. atCompletion is whether the tester's completion is the fact deciding: only that one
// tells the architect of a wait, in one phase-finished notice that names what the move waits for,
// so each later state event that leaves the move waiting tells nothing more.
func (e *Engine) decideTesting(ctx context.Context, tx pgx.Tx, issue record.Issue, row record.PhaseRow, pr *record.PullRequest, atCompletion bool) error {
	repo, head := testedHead(issue, row, pr)
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
	case len(judgement.Failing) > 0:
		return e.transition(ctx, tx, issue, TriggerTesterFailed, "", row, pr,
			"these checks do not pass at "+head+": "+strings.Join(judgement.Failing, "; "))
	case !judged:
		return e.testingWaits(ctx, tx, issue, row, head, atCompletion, "the move waits for Dispatch's states at "+head)
	case len(judgement.Waiting) > 0:
		return e.testingWaits(ctx, tx, issue, row, head, atCompletion,
			"the move waits for the states of "+strings.Join(judgement.Waiting, ", ")+" at "+head)
	}
	return e.transition(ctx, tx, issue, TriggerTesterPassed, "", row, pr, "")
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
