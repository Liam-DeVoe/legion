package prompts

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// The daemon writes the required check legion-verdict from the verdicts of the task's review steps,
// the reviewer's among them, so the check stands `success` at a head only once the reviewer has
// sent `approve` there. A reviewer that waited for every check the base requires before it sent
// `approve` would wait for that check, and the check would wait for the reviewer. So the reviewer's
// Go daemon part reads the required set from both places GitHub keeps it, the rulesets and branch
// protection, with legion-verdict filtered out of each read, and names legion-verdict as the one
// required check the reviewer does not wait for; and the composed prompt still holds the reviewer to
// every other required check: present, completed and success, and a wait while one runs. A part
// that put legion-verdict back among the checks the reviewer reads and waits for - a read without
// the filter, or the sentence gone - fails here. This shows the text is loaded, never that a
// reviewer follows it.
func TestReviewerPromptLeavesLegionVerdictOutOfTheChecksItWaitsFor(t *testing.T) {
	const (
		leftOut        = "`legion-verdict` is the one required check you do not wait for: the daemon writes it from the verdicts of the task's review steps, yours among them, so it stands `success` at a head only after you send `approve` there."
		rulesetsRead   = `gh api --paginate repos/<o>/<r>/rules/branches/<base> --jq '.[] | select(.type=="required_status_checks") | .parameters.required_status_checks[].context | select(. != "legion-verdict")'`
		protectionRead = `gh api repos/<o>/<r>/branches/<base> --jq '.protection.required_status_checks.checks[].context | select(. != "legion-verdict")'`
	)
	part, err := goParts.ReadFile("go/reviewer.md")
	if err != nil {
		t.Fatalf("read go/reviewer.md: %v", err)
	}
	reviewerPart := foldWhitespace(string(part))
	for _, phrase := range []string{leftOut, rulesetsRead, protectionRead} {
		if !strings.Contains(reviewerPart, phrase) {
			t.Errorf("go/reviewer.md omits %q", phrase)
		}
	}
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	composed := foldWhitespace(composedText(t, composer, claim.RoleReviewer, false))
	for _, phrase := range []string{
		leftOut, rulesetsRead, protectionRead,
		"every required name is present, `completed` and `success`",
		"One `queued` or `in_progress`: wait.",
	} {
		if !strings.Contains(composed, phrase) {
			t.Errorf("the composed reviewer prompt omits %q", phrase)
		}
	}
}
