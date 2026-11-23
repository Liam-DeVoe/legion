package prompts

import (
	"strings"
	"testing"
)

// promptPart is one embedded part, by its path: `go/<name>` under the daemon parts, any other
// path under roles/ (rolePart).
func promptPart(t *testing.T, name string) string {
	t.Helper()
	if strings.HasPrefix(name, "go/") {
		body, err := goParts.ReadFile(name)
		if err != nil {
			t.Fatalf("read daemon prompt part %s: %v", name, err)
		}
		return string(body)
	}
	return rolePart(t, name)
}

// The words fresh readers took two ways, or found false, on a task with no pull request, at the
// fresh agent of acceptance's step and at the owner's own steps. Each pin is a sentence that says
// what the code does:
//   - the `legion` tool sends a step's commit itself, the `head` of its handoff record or `@-`
//     where its step writes none (pi-shared's findHandoffReport), and the daemon refuses a review
//     step's report without one (api's stepReportRefusal, MISSING_FIELD);
//   - a read of an issue with no pull request compares a `before_merge` check with nothing unless
//     it names a commit (`dispatch results --at`), and a `ci_check` there reads `no_head` and can
//     never pass, since no role records it (CHECK_PROVED_BY_CI): a step that reads it holds or
//     fails, never passes;
//   - the fresh agent's pass refused SIGNOFF_CHILD_IN_WORKFLOW is applied only when it is sent
//     again (workflow's handoff leaves it unspent);
//   - an issue with a pull request goes from its retro to its merge whatever its steps (workflow's
//     table, hasPullRequest);
//   - the merger waits for the owner's approval of a merged-forward head.
//
// Each retired sentence is refused too. The match runs over a body whose whitespace is folded,
// since a part hard-wraps and a sentence can cross the break.
func TestTheNoPullRequestAndOwnerStepWordsSayWhatTheCodeDoes(t *testing.T) {
	for _, pin := range []struct{ part, words string }{
		{"reviewer.md", "The `legion` tool sends that handoff's `head` as the completion's `commit`, so you pass no commit yourself."},
		{"reviewer.md", "Read the checks there with `dispatch results --issue <issue> --at <the commit you read>`."},
		{"reviewer.md", "A `ci_check` check reads `no_head` there: no run can prove it without a pull request, so its words are wrong for this task."},
		{"reviewer.md", "Send it to the architect and wait for the rewrite, with a `WAITING` line naming it, as for a check whose `run` is a whole test suite."},
		{"reviewer.md", "Never `pass` while such a check stands."},
		{"core/reviewer.md", "Send it to the architect and give no `pass` while it stands."},
		{"acceptance.md", "and wait for the rewrite with a `WAITING` line naming it. Or end your step with a `fail` that names no `return_to`."},
		{"acceptance.md", "Never `pass` while such a check stands."},
		{"reviewer.md", "or in your handoff's `body` where the issue has no pull request"},
		{"reviewer.md", "on an issue with no pull request also `body`, the text a review body would carry"},
		{"tester.md", "That verdict judges the implementer's recorded results alone: your own `failures`, such as a red test you wrote, never make it `rejected`."},
		{"tester.md", "say in `implementerProof.how` that no implementer step stood before yours, so nothing was judged."},
		{"tester.md", "the pushed commit you tested, which `jj -R \"$LEGION_WORKSPACE\" log -r @- --no-graph -T commit_id` printed before you tested (never `@`, the working-copy commit, which is never pushed)"},
		{"core/tester.md", "read on GitHub at the head you tested, the pull request's head where the issue has one,"},
		{"core/reviewer.md", "What a review body would carry, the `Security:` line among it, goes in your handoff instead."},
		{"core/reviewer.md", "A round's diff runs to it from the commit `Last reviewed commit:` names, or from the default branch where it names none."},
		{"core/common.md", "A correctness finding is fixed in this change, by the step whose work it is. A role that makes no commit, such as the reviewer, sends it back to that step."},
		{"mechanics/headless.md", "`summary` (two sentences for the architect) and each other field your role sends."},
		{"mechanics/headless.md", "Its description names each operation it gives you, and this prompt says which of them your role uses."},
		{"mechanics/headless.md", "Before you complete, record your handoff where your phase ends with one, and push each commit your role makes."},
		{"acceptance.md", "which `dispatch results --issue <issue> --at <head>` shows under the check's own line"},
		{"architect-root.md", "the retro of a task with no step of the type implementation or whose last such step is yours"},
		{"architect-root.md", "or its verdict where the issue has no pull request or that step was your own"},
		{"architect-root.md", "Where its task holds your step `acceptance:owner`, your acceptance is the only gate on it. A task with no acceptance step has no gate on it at all."},
		{"architect.md", "or its verdict where the issue has no pull request or that step was your own"},
		{"architect.md", "Where its task holds your step `acceptance:owner`, your acceptance is the only gate on it. A task with no acceptance step has no gate on it at all."},
		{"go/reviewer.md", "The tool sends your `review` handoff's `head` as the completion's `commit`, and the daemon refuses a completion with none (`MISSING_FIELD`, naming `commit`)."},
		{"go/reviewer.md", "So send `return_to` when the work that must change is another earlier step's, such as a tester step's results."},
		{"go/acceptance.md", "A `fail` may name the earlier step whose work must change with `return_to`"},
		{"go/acceptance.md", "Send the same report again once each has left the workflow."},
		{"go/acceptance.md", "Send the work back with your `fail`, never with `request_backward_move`."},
		{"go/worker-common.md", "Retro, the merger, the step acceptance and a step of a role of the deployment or of the plan write no handoff."},
		{"go/architect-common.md", "An issue that has a pull request goes on from its retro to its merge, whatever its task's steps."},
		{"go/architect-common.md", "You pass no commit: the tool reads the commit and the handoff record as `handoff_complete` does."},
		{"go/architect-common.md", "Where your pod's launcher cannot say what those files hold, a step of your own App is told at once"},
		{"go/architect-common.md", "Write its `review` handoff first, its `decision` `approved` for `pass` and `changes_requested` for `fail`"},
		{"go/architect-common.md", "A merger that enters `merging` while that approval is owed waits for it."},
	} {
		if !strings.Contains(foldWhitespace(promptPart(t, pin.part)), pin.words) {
			t.Errorf("%s does not say %q", pin.part, pin.words)
		}
	}
	for _, retired := range []struct{ part, words string }{
		{"reviewer.md", "and that `commit`, as the daemon part below says"},
		{"reviewer.md", "Wait for no run of it, fail nothing for it"},
		{"core/reviewer.md", "so wait for none: send it to the architect"},
		{"acceptance.md", "Wait for none, and send it to your architect as a finding"},
		{"tester.md", "the commit of the working copy you tested"},
		{"tester.md", "the one your completion gives: `verified` with `pass`, `rejected` with `fail`"},
		{"go/reviewer.md", "and the `commit` you read, which the daemon requires there"},
		{"go/acceptance.md", "Legion applies that same report once they have left"},
		{"go/worker-common.md", "Retro and the merger write no handoff"},
		{"go/architect-common.md", "Legion runs the retro itself"},
		{"go/architect-common.md", "and Legion reads nothing of it"},
		{"go/architect-common.md", "the commit where it sends one"},
		{"go/reviewer.md", "whichever of the two is second"},
		{"mechanics/headless.md", "`handoff_complete`, `read_record` (your issue's record), and `request_backward_move`"},
	} {
		if strings.Contains(foldWhitespace(promptPart(t, retired.part)), retired.words) {
			t.Errorf("%s still says %q", retired.part, retired.words)
		}
	}
}
