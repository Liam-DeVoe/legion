package prompts

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// C4 (R6): the daemon writes legion-verdict on each merge group's commit, and no check is written
// while it is down, so a queue whose check times out removes the entry as `failed_checks`. That
// removal is no work to fix. The composed merger prompt therefore finds the group commit of the
// removal (its beforeCommit, or where that is empty the newest gh-readonly-queue/ branch the queue
// made for the pull request, from the repository's activity) and counts its legion-verdict runs of
// the merger's own App, by the slug its next read filters on too: a run of another App beside it
// is no run of the daemon's. With none, it enqueues the same head again once the head's own
// legion-verdict stands, records that enqueue, and sends nothing back; with any, the group's checks
// failed and it sends the issue back as before. The unconditional send-back it held for every
// `failed_checks` removal is gone. Each head gets that requeue once, a choice the prompt states
// with its reason: a second such removal of the same head, which its own recorded enqueue shows,
// goes to the architect, and nothing is enqueued, so a writer that keeps failing on group commits
// does not requeue the head every time the check times out. Both the first removal's requeue and
// the second removal's report sit in the one paragraph that names the missing run. This shows the
// text is loaded, never that a merger follows it.
func TestMergerPromptRequeuesAMissingLegionVerdict(t *testing.T) {
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw := composedText(t, composer, claim.RoleMerger, false)
	text := foldWhitespace(raw)
	const missing = "no `legion-verdict` run of your App reached the group commit"
	for _, phrase := range []string{
		"First find that group commit. It is the `beforeCommit` of the removal.",
		"gh api --paginate 'repos/<o>/<r>/activity?activity_type=branch_creation&time_period=day&per_page=100' --jq '.[] | select(.ref | test(\"^refs/heads/gh-readonly-queue/.+/pr-<n>-[0-9a-f]+$\")) | [.timestamp, .after] | @tsv'",
		"Otherwise count your App's `legion-verdict` runs on the group commit, where `<slug>` is your App's login with `[bot]` taken off:",
		"gh api 'repos/<o>/<r>/commits/<group commit>/check-runs?check_name=legion-verdict' --jq '[.check_runs[] | select(.app.slug == \"<slug>\")] | length'",
		"gh api 'repos/<o>/<r>/commits/<head>/check-runs?check_name=legion-verdict' --jq '[.check_runs[] | select(.app.slug == \"<slug>\") | .conclusion]'",
		missing,
		"When it reads `success`, enqueue the same head again (step 4), and record that enqueue in a `dispatch comment` on the issue that names the group commit.",
		"Any other count: the queue's own `merge_group` checks failed. That is work to fix. Send the issue back with `request_backward_move` (step 6).",
		"If that command lists no branch, tell the architect with `envoy_publish` to its encoded role token, and quote its output.",
	} {
		if !strings.Contains(text, phrase) {
			t.Errorf("the composed merger prompt omits %q", phrase)
		}
	}
	const once = "Each head gets one such enqueue, so that a daemon whose writes keep failing does not requeue it at every timeout."
	const second = "If one already records such an enqueue of this head, enqueue nothing: tell the architect with `envoy_publish` to its encoded role token, name the head and both group commits, and quote the two reads above."
	paragraphs := ruleParagraphs(raw, missing)
	if len(paragraphs) != 1 {
		t.Fatalf("the composed merger prompt's paragraphs naming %q are %q; want one", missing, paragraphs)
	}
	for _, want := range []string{"send nothing back", once, "Read your own `dispatch comment`s on the issue first.", second} {
		if !strings.Contains(paragraphs[0], want) {
			t.Errorf("the composed merger prompt's paragraph naming %q omits %q: %q", missing, want, paragraphs[0])
		}
	}
	if retired := "`failed_checks`: the queue's own `merge_group` checks failed. That is work to fix."; strings.Contains(text, retired) {
		t.Errorf("the composed merger prompt still sends every `failed_checks` removal back: %q", retired)
	}
	if retired := "--jq .total_count"; strings.Contains(text, retired) {
		t.Errorf("the composed merger prompt still counts the group commit's `legion-verdict` runs of every App (%q), where its next read counts only its own App's", retired)
	}
}
