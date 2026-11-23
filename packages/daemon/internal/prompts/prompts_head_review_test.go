package prompts

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// A push moves the branch, but the pull request can keep naming the old commit, so the new
// commit gets no checks and nothing reports it (dispatch://INFRA-1318). Every pushing role's
// composed prompt therefore carries the read of the pull request's own head after its own push,
// escalation sentence included, and neither architect shape does: a later edit that moves the
// step into a part both shapes compose fails here rather than telling an architect to push. The
// implementer's part carries the carve-out that names the step's spaced reads as the one sleep
// its "never sleep in a loop for anything" rule allows. This shows the text is loaded, never
// that a role follows it.
func TestTheComposedPushingPromptsPinTheReadOfThePullRequestHead(t *testing.T) {
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const headRead = "gh pr view <n> --repo <the issue's repository> --json headRefOid --jq .headRefOid"
	step := []string{
		headRead,
		"about ten seconds after your push, again about ten seconds later, and a last time about thirty seconds after that, with a read that gives the pushed commit ending the step there and only the last read deciding a mismatch",
		"once the branch has an open pull request",
		"an empty one when your work is complete",
		"tell your architect the commit you pushed, the head each read gave and the time of each, and end the turn with a WAITING line naming that answer rather than completing: a phase completed here hands the next role a commit the pull request does not name.",
		"The merger pushes only a merge of the base forward, and makes this read after that push as any role does",
	}
	for _, role := range []claim.Role{claim.RolePlanner, claim.RoleImplementer, claim.RoleTester,
		claim.RoleReviewer, claim.RoleMerger} {
		text := foldWhitespace(composedText(t, composer, role, false))
		for _, phrase := range step {
			if !strings.Contains(text, phrase) {
				t.Errorf("the composed %s prompt omits %q", role, phrase)
			}
		}
	}
	const carveOut = "the step's spaced reads of the pull request's head after a push are the one sleep this rule allows, and they read no check"
	if !strings.Contains(foldWhitespace(composedText(t, composer, claim.RoleImplementer, false)), carveOut) {
		t.Errorf("the composed %s prompt omits %q, so it holds \"never sleep in a loop for anything\" beside a step whose reads are spaced by sleeps, with nothing saying that rule allows them",
			claim.RoleImplementer, carveOut)
	}
	for _, isRoot := range []bool{true, false} {
		text := foldWhitespace(composedText(t, composer, claim.RoleArchitect, isRoot))
		if strings.Contains(text, headRead) {
			t.Errorf("the composed %s prompt (root %t) says %q", claim.RoleArchitect, isRoot, headRead)
		}
	}
}

// The reviewer's verdict reaches the daemon with its completion of reviewing, whose `commit` the
// `legion` tool takes from the `head` of the reviewer's newest `review` handoff, and the daemon
// records the verdict as the round's decision at that commit (workflow's decisionOf), so a
// reviewer whose prompt left the head out of that handoff, or let it name a head it did not read,
// sends a verdict the daemon refuses (`VERDICT_COMMIT_REQUIRED`) or one of code it never read. The
// composed reviewer prompt therefore says where the verdict's head comes from, which head that is
// - the one whose diff and checks the reviewer read, never the head GitHub happens to show when it
// completes - and the read that tells it a push landed under it (`--json headRefOid`); that its
// completion carries `approve` or `changes` and what the daemon refuses
// (`REVIEWER_VERDICT_REQUIRED`); and that the review it posts on GitHub is a `COMMENT`, with the
// Legion footer every role's pull-request comment carries, its session and its phase. The daemon
// reads no review footer any more, so the footer's head and entry, and the rules that read them,
// are gone. This shows the text is loaded, never that a reviewer follows it.
func TestTheComposedReviewerPromptSendsItsVerdictOnTheHeadItReviewed(t *testing.T) {
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	text := foldWhitespace(composedText(t, composer, claim.RoleReviewer, false))
	for _, phrase := range []string{
		"`op: \"handoff_complete\"`, a `summary` and your `verdict`, `approve` or `changes`",
		"(`REVIEWER_VERDICT_REQUIRED`)",
		"(`VERDICT_COMMIT_REQUIRED`): the tool sends the `head` of your newest `review` handoff as the completion's `commit`",
		"gh pr view <n> --repo <owner>/<repo> --json headRefOid --jq .headRefOid",
		"the head whose diff and checks you read",
		"rather than naming a head you have not read",
		"`event` (`COMMENT`)",
		`"phase":"review"} -->`,
		`\"phase\":\"review\"} -->`,
	} {
		if !strings.Contains(text, phrase) {
			t.Errorf("the composed reviewer prompt omits %q", phrase)
		}
	}
	for _, retired := range []string{
		`"head":"<the head you reviewed>"`,
		`\"head\":\"<head-sha>\"`,
		`"entry":"<entry>"`,
		"`APPROVE`", "`REQUEST_CHANGES`",
	} {
		if strings.Contains(text, retired) {
			t.Errorf("the composed reviewer prompt still says %q, which the daemon no longer reads", retired)
		}
	}
}

// The task of a later review round names the commit the reviewer's own newest verdict of its round
// named (` Last reviewed commit: <sha>.`, which workflow's namingTheReviewRound writes from
// record.PhaseRow.ReviewedHead), so the reviewer can read the difference from that commit rather
// than the whole change again. The composed reviewer prompt therefore names the field, the
// function that writes it into the task, and what sets it: the verdict the reviewer sends with its
// completion of reviewing while its row holds the task of the entry that stands (` Review entry:
// <n>.`, the task's last sentence, record.PhaseRow.Entry, which the task of a wake ends with too),
// to which the daemon holds that completion (`HANDOFF_STALE_ENTRY`); a `COMMENT`, a person's
// review and a verdict past review leave it where it stands. The core part names the commit in the
// daemon part's words. It says to read the difference from that commit and each thread, takes that
// commit as the earlier head the review sizes itself to, names in each task of the thermonuclear
// pair the diff it reviews, keeps every defect seen outside it a finding, and reads the whole
// change where the commit is no ancestor of the head. The pair's old sentence, which ran it over
// the whole change in every round, is gone, and so is the unchanged-diff fingerprint, which
// dispatch://INFRA-4406 R1 deleted from the role prompts. This shows the text is loaded, never
// that a reviewer follows it.
func TestTheComposedReviewerPromptReadsALaterRoundFromTheCommitItsTaskNames(t *testing.T) {
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	text := foldWhitespace(composedText(t, composer, claim.RoleReviewer, false))
	for _, phrase := range []string{
		"Last reviewed commit: <sha>",
		"no entry into a phase clears it",
		"`namingTheReviewRound` in `packages/daemon/internal/workflow/review_round.go` writes it into the task",
		"Your task names the entry of your round as its last sentence, `Review entry: <n>.`: the number of your entry into the round, which the daemon keeps on your phase row (`PhaseRow.Entry` in `packages/daemon/internal/record/record.go`) and raises at each new entry.",
		"The task a wake gives you once your pod was released and brought back ends with it too, as each task of the round does.",
		"Your completion is held to the entry your claim serves: one of an entry the daemon has raised since is taken only at the head the record holds and only where no move back raised the entry; any other is refused (`HANDOFF_STALE_ENTRY`)",
		"When the task of a round names `Last reviewed commit: <commit>`, the head your own newest verdict of the round named (the daemon part below says which verdict that is), read the difference from that commit to the head (`git diff <commit> <head>`), and each thread",
		"the head your newest verdict named, sent with your completion of reviewing while your row held that entry's task, which a new generation's clear empties until the daemon enters you again.",
		"Your `COMMENT` reviews, a person's review and a verdict you send past review leave it where it stands.",
		"the head your newest verdict of the round named; the daemon part below gives the whole rule and where the commit comes from.",
		"read the difference from that commit to the head (`git diff <commit> <head>`), and each thread",
		"over the diff that round reads, when that diff touches runtime code, and name that diff in each agent's task",
		"The commit limits what you must read, never what you report: a defect you see outside the difference is a finding like any other",
		"On a new head, that commit is your earlier head, unless it is not an ancestor of the head (`git merge-base --is-ancestor <commit> <head>` does not exit 0): then you read the whole change, as for `none`.",
		"Size your review to the change you read: review again only what it can affect, and confirm the rest by naming the earlier head.",
	} {
		if !strings.Contains(text, phrase) {
			t.Errorf("the composed reviewer prompt omits %q", phrase)
		}
	}
	for _, retired := range []string{
		"Run the thermonuclear pair once at the head the round reviews when the diff touches runtime code",
		"fingerprint",
	} {
		if strings.Contains(text, retired) {
			t.Errorf("the composed reviewer prompt still says %q", retired)
		}
	}
}
