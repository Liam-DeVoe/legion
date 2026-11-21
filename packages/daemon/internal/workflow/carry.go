package workflow

import (
	"cmp"
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/verdict"
)

// A head that moves in review (spec R4, R5; Design, "A head that moves in review"). A check run
// belongs to one commit, so a head a push leaves has no legion-verdict until a verdict names it.
// The tester's pass at commit A counts at head B only where the reviewer's verdict at B names A,
// the commit of the tester's recorded pass, as its testedCommit, by which the reviewer states that
// no commit since A changes code that runs (verdict.PassCounts). The daemon compares those two
// recorded commits and reads no diff, so a path a compare would leave out cannot carry a change
// past the tester. An approve that carries no pass sends the issue back to testing, in reviewing
// (handoff) and past review (reviewVerdict) alike, and nothing writes success on its head: the
// move clears the tester's pass. Past review, every move of the head asks the round's recorded
// reviewer for its verdict at the new head (askVerdict, and mergeForwardLanded for a merge
// forward's landing).

// heldVerdictApplied is the line the end of a hold logs when it applies the reviewer's verdict past
// review that the issue took while held (applyHeldVerdict).
const heldVerdictApplied = "workflow: the hold ended, and the reviewer's approve taken while it held carries no tester's pass; the issue goes back to testing"

// reviewVerdict applies the verdict past review (intake.ReviewVerdict, which the review-verdict
// route queues): the round's recorded reviewer's approve or changes at the head the pull request
// moved to while the issue stands past its review (record.PastReview). It writes the round's
// decision at that head on the reviewer's row (decisionOf) and the verdict the writer of
// legion-verdict reads there (recordVerdict), whose write the fact's commit asks for
// (askMergeState). It writes no handoff and makes no completion, and it moves no phase, but where
// an approve carries no tester's pass to the head (uncarried): then the issue goes back to testing
// at the reviewer's request (moveBackToTesting).
//
// The route judged the call against the record before the fact's transaction, and the record can
// move between the two, so the same judgement is made again here under the fact's lock, each
// refusal recording nothing: a claim that is not the one whose completion ended the round
// (VERDICT_CALLER_FORBIDDEN), an issue no longer past its review (VERDICT_NOT_PAST_REVIEW), a
// lingering tree (TREE_LINGERING), and a head the pull request has moved off
// (VERDICT_HEAD_NOT_CURRENT). A held issue moves only when its hold ends, so a verdict taken while
// held is recorded and moves nothing then: the hold's end applies it (applyHeldVerdict), so the
// reviewer sends it once, whatever becomes of its session meanwhile.
func (e *Engine) reviewVerdict(ctx context.Context, tx pgx.Tx, fact intake.ReviewVerdict) (intake.Result, error) {
	issue, err := e.store.Issue(ctx, tx, fact.Issue)
	if err != nil || issue == nil {
		return intake.Result{}, err
	}
	reviewer, err := e.phaseRow(ctx, tx, issue.Key, claim.RoleReviewer)
	if err != nil {
		return intake.Result{}, err
	}
	if reviewer.Claim != fact.Claim {
		return intake.Result{Refusal: &intake.Refusal{Status: 403, Code: "VERDICT_CALLER_FORBIDDEN", Message: fmt.Sprintf(
			"review_verdict is the verdict of the reviewer whose completion ended the review round of %s, claim %q, and this call is claim %s's; nothing was recorded",
			issue.Key, reviewer.Claim, fact.Claim)}}, nil
	}
	if !record.PastReview(*issue) {
		return refused("VERDICT_NOT_PAST_REVIEW", fmt.Sprintf(
			"%s stands in %s: review_verdict is the reviewer's verdict on a head that moved past review, in accepting, retro or merging; nothing was recorded",
			issue.Key, record.StandsAt(*issue))), nil
	}
	if lingers, err := record.TreeLingers(ctx, e.store, tx, issue.Tree); err != nil || lingers {
		return refusedLingering(*issue, "the reviewer's verdict past review"), err
	}
	pr, err := e.store.PullRequest(ctx, tx, issue.Key)
	if err != nil {
		return intake.Result{}, err
	}
	if pr == nil || pr.HeadSHA != fact.Commit {
		current := "the daemon records no pull request for " + issue.Key
		if pr != nil {
			current = fmt.Sprintf("the head of %s's pull request #%d is %s", issue.Key, pr.Number, pr.HeadSHA)
		}
		return refused("VERDICT_HEAD_NOT_CURRENT", fmt.Sprintf(
			"this verdict is on %s, and %s: read that head and send the verdict on it; nothing was recorded", fact.Commit, current)), nil
	}
	decision := decisionOf(intake.HandoffComplete{Verdict: fact.Verdict, Summary: fact.Summary, Commit: fact.Commit, TestedCommit: fact.TestedCommit})
	if decision == nil {
		// The route takes approve and changes alone (api's reviewerVerdictOf).
		return intake.Result{}, nil
	}
	tester, err := e.phaseRow(ctx, tx, issue.Key, claim.RoleTester)
	if err != nil {
		return intake.Result{}, err
	}
	back := uncarried(tester, fact.Verdict, fact.Commit, fact.TestedCommit)
	reviewer.Decision = decision
	if err := e.store.PutPhase(ctx, tx, reviewer); err != nil {
		return intake.Result{}, err
	}
	if err := e.recordVerdict(ctx, tx, issue.Key, pr, fact.Claim, fact.Verdict, fact.Commit, fact.TestedCommit); err != nil {
		return intake.Result{}, err
	}
	// The round's decision on the reviewer's row is the record changing, so the issue moved (the
	// rule in dispatchIssue), as at the completion that wrote the decision before it (handoff).
	if err := e.store.MarkIssueMoved(ctx, tx, issue.Key, e.now()); err != nil {
		return intake.Result{}, err
	}
	e.logOnCommit(ctx, "workflow: recorded the reviewer's verdict past review", "issue", issue.Key, "tree", issue.Tree,
		"phase", record.StandsAt(*issue), "verdict", fact.Verdict, "commit", fact.Commit, "testedCommit", fact.TestedCommit)
	if !back || issue.Phase == phase.Held {
		return intake.Result{}, nil
	}
	return intake.Result{}, e.moveBackToTesting(ctx, tx, *issue, record.PhaseRow{Issue: issue.Key, Role: claim.RoleReviewer}, pr, tester, fact.Commit, fact.TestedCommit)
}

// applyHeldVerdict applies, as issue's hold ends (retryPhase, with issue released to the phase it
// was held from), the verdict past review it took while held (reviewVerdict), and says whether it
// moved the issue: the newest verdict the store records at the head pr stands at (store.Verdict,
// which recordVerdict wrote), where it is this issue's reviewer's approve that carries no tester's
// pass to that head (uncarried), sends the issue back to testing at the reviewer's request, as it
// would have the moment it came, and is logged. A verdict at a head the pull request has moved off
// is of code the issue no longer stands on, and the push that moved it asked the reviewer again
// (askVerdict). An issue in reviewing took no verdict while held: its reviewer's completion is
// what decides that round, and its own approve is acted on as it lands (handoff).
func (e *Engine) applyHeldVerdict(ctx context.Context, tx pgx.Tx, issue record.Issue, pr *record.PullRequest) (bool, error) {
	if !record.PastReview(issue) || pr == nil {
		return false, nil
	}
	repository, err := ghrepo.Parse("the repository of "+issue.Key+"'s pull request", pr.Repo)
	if err != nil {
		return false, err
	}
	given, found, err := store.VerdictTx(ctx, tx, repository, pr.Number, pr.HeadSHA)
	if err != nil || !found || given.Issue != issue.Key {
		return false, err
	}
	tester, err := e.phaseRow(ctx, tx, issue.Key, claim.RoleTester)
	if err != nil || !uncarried(tester, string(given.Verdict), pr.HeadSHA, given.TestedCommit) {
		return false, err
	}
	e.logOnCommit(ctx, heldVerdictApplied, "issue", issue.Key, "tree", issue.Tree, "phase", issue.Phase,
		"commit", pr.HeadSHA, "testedCommit", given.TestedCommit, "reviewer", given.Session)
	return true, e.moveBackToTesting(ctx, tx, issue, record.PhaseRow{Issue: issue.Key, Role: claim.RoleReviewer}, pr, tester, pr.HeadSHA, given.TestedCommit)
}

// uncarried is whether word, a reviewer's verdict at commit carrying the tester's pass at tested,
// is an approve that the tester's recorded pass does not count at (verdict.PassCounts, the rule
// the writer of legion-verdict decides by). Only an issue whose record holds a tester's pass is
// judged: none reaches reviewing without one, and a pass recorded with no commit counts at no
// head, since nothing says which code it tested. A changes is a refusal whatever it carries, and
// ends the round as one.
func uncarried(tester record.PhaseRow, word, commit, tested string) bool {
	return store.VerdictWord(word) == store.VerdictApprove && tester.Verdict == verdict.TesterPass &&
		!verdict.PassCounts(verdict.Tester{Verdict: tester.Verdict, Commit: tester.HandoffCommit}, commit, tested)
}

// testAgain sends issue, in reviewing, back to testing where the reviewer's completion is an
// approve that carries no tester's pass to the commit it names (uncarried), and says whether it
// did: such an approve ends no round, since no pass stands on its head. by is the reviewer's row as
// the completion left it, which the move hands on as the reviewer's request.
func (e *Engine) testAgain(ctx context.Context, tx pgx.Tx, issue record.Issue, by record.PhaseRow, pr *record.PullRequest, word, commit, tested string) (bool, error) {
	tester, err := e.phaseRow(ctx, tx, issue.Key, claim.RoleTester)
	if err != nil || !uncarried(tester, word, commit, tested) {
		return false, err
	}
	return true, e.moveBackToTesting(ctx, tx, issue, by, pr, tester, commit, tested)
}

// moveBackToTesting moves issue back to testing at the reviewer's request (by, whose role is the
// reviewer's), as a backward move does, with the reason the tester's task, the architect's notice
// and the post on the issue give: the approve of commit carries no pass, and commit is the head to
// test. The move clears the tester's pass (clearHandoff), so the writer of legion-verdict decides
// no success on commit until the tester passes it.
func (e *Engine) moveBackToTesting(ctx context.Context, tx pgx.Tx, issue record.Issue, by record.PhaseRow, pr *record.PullRequest, tester record.PhaseRow, commit, tested string) error {
	named := "names no testedCommit"
	if tested != "" {
		named = "names testedCommit " + tested
	}
	passed := "the tester's recorded pass names no commit, so it counts at no head"
	if tester.HandoffCommit != "" {
		passed = fmt.Sprintf("the tester's recorded pass is at %s, so that pass does not count at %s", tester.HandoffCommit, commit)
	}
	reason := fmt.Sprintf("the reviewer's approve of %s %s, and %s: test %s", commit, named, passed, commit)
	e.logOnCommit(ctx, "workflow: the reviewer's approve carries no tester's pass; the issue goes back to testing", "issue", issue.Key,
		"tree", issue.Tree, "phase", record.StandsAt(issue), "commit", commit, "testedCommit", tested, "testerCommit", tester.HandoffCommit)
	return e.transition(ctx, tx, issue, TriggerBackward, phase.Testing, by, pr, reason)
}

// askVerdict asks the round's recorded reviewer for its verdict at pr's head, which a push moved
// there from from while issue stands past its review (spec R5, "The verdict past review"): every
// push in accepting, retro or merging, a retro commit and an implementer's correction among them,
// leaves a head with no legion-verdict, which cannot merge until the reviewer's verdict there is
// written. A merge forward's landing asks in mergeForwardLanded, so this is every other move. The
// ask is the review row a landing writes (record.MergeForwardReview), from the head the push left
// to the head it made, which wakes a released reviewer: a new outbox kind or payload key would be
// one the release before it cannot read (spec R15). Its executor delivers it while the issue stands
// past its review at that head (leftMergeForwardAt). A tree that lingers, where every role is
// suspended, asks nothing.
func (e *Engine) askVerdict(ctx context.Context, tx pgx.Tx, issue record.Issue, pr *record.PullRequest, from string) error {
	if !record.PastReview(issue) || from == "" || from == pr.HeadSHA {
		return nil
	}
	if lingers, err := record.TreeLingers(ctx, e.store, tx, issue.Tree); err != nil || lingers {
		return err
	}
	addressed, err := e.addressRoles(issue.Project, []roleRecipient{{issue: issue.Key, role: claim.RoleReviewer, row: issue.Key}}, nil)
	if err != nil {
		return err
	}
	at := record.StandsAt(issue)
	for _, to := range addressed {
		if err := e.enqueue(ctx, tx, to.issue, record.MergeForwardMessage{Role: claim.RoleReviewer, Step: record.MergeForwardReview,
			Head: from, Landed: pr.HeadSHA, Base: cmp.Or(pr.Base, issue.DefaultBranch), Phase: at, PullRequest: pr.Number}); err != nil {
			return err
		}
	}
	e.logOnCommit(ctx, "workflow: the head moved past review; the reviewer is asked for its verdict", "issue", issue.Key, "tree", issue.Tree,
		"phase", at, "pullRequest", pr.Number, "head", from, "landed", pr.HeadSHA)
	return nil
}
