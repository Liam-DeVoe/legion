package prompts

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// A step of the type review is the reviewer's, or the owner's acceptance at `acceptance:owner`,
// and the daemon takes each step's verdict only from the claim its step record names (workflow's
// reviewStepRefusal and reviewVerdict). Each agent sends its verdict at the head it read: the
// reviewer `approve` or `changes` with its completion's `commit`, and past review with
// `review_verdict` and no `issue`; the owner with `sign_off`, its `approve`, or a move back out of
// `accepting`, its `changes`, at the head the record holds for the pull request
// (recordAcceptance), and past review with `review_verdict` naming `issue`. No completion names a
// tested commit, and an `approve` carries no tester's pass to a later head: the states Dispatch
// publishes at each head decide the testing step there, and the tester's completion carries no
// verdict. legion-verdict is success only where an approving review step is independent, so each
// review step's agent is told what takes its independence away, and the merger names both review
// steps where it says what the check is written from. Each composed prompt that carries a rule
// holds it in one clause, and no composed prompt still teaches the carry, the reviewer as the one
// review step, or the check written from the tester's verdict. This shows the text is loaded,
// never that a role follows it.
func TestEachReviewStepSendsItsVerdictAtTheHeadItRead(t *testing.T) {
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	carry := []string{"testedCommit", "tester's pass", "handoffCommit", "back to `testing` by itself",
		"sends the issue back to testing", "the reviewer whose completion ended the round"}
	owner := [][]string{
		{"your `sign_off` is your `approve` there", "your `request_backward_move` out of `accepting` is your `changes` there",
			"read that head and its evidence before you decide"},
		{"send `review_verdict` with `issue` the issue you accepted", "`commit` the new head"},
		{"a commit you push to the issue's branch makes your acceptance no independent review"},
		{"`sign_off` is then your `approve` at the new head"},
	}
	ownerRetired := append([]string{
		"the daemon also asks the reviewer for its verdict on the new head",
		"that it sent `approve` on the new head and `legion-verdict` at that head reads `success`",
		"which the daemon writes from the reviewer's verdict",
	}, carry...)
	for _, tc := range []struct {
		name    string
		role    claim.Role
		isRoot  bool
		holds   [][]string
		retired []string
	}{
		{"reviewer", claim.RoleReviewer, false, [][]string{
			{"It takes your step's verdict only from the claim your step's record names as its agent", "(`VERDICT_CALLER_FORBIDDEN`)"},
			{"Send it with `op: \"review_verdict\"`", "`commit` that head", "leave `issue` out"},
			{"The daemon takes it only from the agent of a review step of the issue's task", "you for your step"},
			{"at least one of those review steps is independent", "run by an agent whose claim wrote none of the change"},
			{"A claim wrote part of the change when it ran an implementation step of the task", "`Omp-Session` trailer"},
			{"A pass or a verdict at an earlier commit counts at no later head", "your `approve` carries no pass of the testing step"},
			{"past review, the old head the daemon's message names", "the head your last verdict named"},
			{"each push past review asks the agent of each review step that has passed for a verdict on the new head"},
		}, append([]string{"from your verdict there", "a head your `approve` names", "the reviewer is asked as well",
			"the head the push left", "No other phase completion carries a `verdict` (`VERDICT_NOT_TAKEN`)"}, carry...)},
		{"root architect", claim.RoleArchitect, true, owner, ownerRetired},
		{"sub-architect", claim.RoleArchitect, false, owner, ownerRetired},
		{"tester", claim.RoleTester, false, [][]string{
			{"You post no check run, and your completion carries no verdict", "the daemon writes `legion-verdict` from the states Dispatch publishes of the issue's checks at the head"},
		}, append([]string{"from your verdict and the reviewer's"}, carry...)},
		{"merger", claim.RoleMerger, false, [][]string{
			{"The daemon writes the required check `legion-verdict` on each head from the states Dispatch publishes of the issue's checks there",
				"the verdicts there of the task's review steps, the reviewer's and the owner's acceptance"},
			{"each push past review asks the agent of each review step that has passed for a verdict on the new head"},
			{"Where a review step of your task has passed, the daemon also asks its agent for its verdict on that head"},
		}, append([]string{"from the reviewer's and the tester's verdicts", "a head the reviewer's `approve` names",
			"Where your task has a reviewer step, the daemon also asks the reviewer"}, carry...)},
	} {
		text := foldWhitespace(composedText(t, composer, tc.role, tc.isRoot))
		for _, terms := range tc.holds {
			if !clauseHolds(text, terms...) {
				t.Errorf("no clause of the composed %s prompt holds %q, in that order", tc.name, terms)
			}
		}
		for _, phrase := range tc.retired {
			if strings.Contains(text, phrase) {
				t.Errorf("the composed %s prompt still says %q", tc.name, phrase)
			}
		}
	}
}

// When every testing and review step of the task passed at a head and no approving review step
// was run by an agent whose claim wrote none of the change, the daemon writes legion-verdict
// `failure` there with its own title (spec R4), or with the same title where it could not read the
// commits or the sessions that would show one. No worker's change cures that, so the merger
// neither merges nor sends the issue back: it tells the owner of the task, quoting the run's
// summary, and only a person merges that head. The composed merger prompt keys that case on the
// run's title, gives the read that prints it, and says each of those acts. This shows the text is
// loaded, never that a merger follows it.
func TestTheMergerTellsTheOwnerWhenNoReviewIsIndependent(t *testing.T) {
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	text := foldWhitespace(composedText(t, composer, claim.RoleMerger, false))
	const title = "`legion-verdict: failure (no independent review)`"
	for _, terms := range [][]string{
		{"`output.title` reads " + title, "is a head no worker can fix"},
		{"no review step whose verdict there is `approve` was run by an agent whose claim wrote none of the change",
			"or the daemon could not read the commits and sessions that would show one"},
		{"Do not merge, do not enqueue, do not send the issue back, and do not complete"},
		{"Wait, and tell the owner of the task, your architect, with `envoy_publish` to its encoded role token",
			"name the pull request and the head", "quote the run's `output.summary`"},
		{"Only a person can merge that head"},
	} {
		if !clauseHolds(text, terms...) {
			t.Errorf("no clause of the composed merger prompt holds %q, in that order", terms)
		}
	}
	const read = `gh api 'repos/<o>/<r>/commits/<head>/check-runs?check_name=legion-verdict' --jq '[.check_runs[] | select(.app.slug == "<slug>") | {conclusion, title: .output.title, summary: .output.summary}]'`
	if !strings.Contains(text, read) {
		t.Errorf("the composed merger prompt omits the read of the run's title and summary, %q", read)
	}
}
