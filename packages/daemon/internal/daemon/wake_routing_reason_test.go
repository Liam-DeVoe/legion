package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// skills/legion-architect/references/wake-routing.md, which the branch leaves as main has it,
// tells the architect what to do on each notice by what main's notice said in its `reason`; the
// branch's notices of these kinds carry no `reason`, and the architect's prompt names other fields
// for the same facts. For pr-blocked the skill names main's sentence by its words ("A `pr-blocked`
// reason reads `<n> rounds counted, <m> conflict rounds not counted`"; main's
// workflow/review.go:recordRound at d46ec4e0: "Issue reached review_round_cap=%d: %d rounds
// counted, %d conflict rounds not counted."), and INFRA-3686's R8 keeps it: "A sentence that a
// prompt or a skill names by its words, such as \"this resume is your design-approved wake\", also
// stays word for word." For the other kinds its R2 holds: "It gives no step that the reader's role
// prompt or a skill gives", so the skill the architect routes by must read what the notice says.
func TestTheArchitectsWakeRoutingReadsOnlyWhatTheNoticesSay(t *testing.T) {
	skill, err := os.ReadFile(filepath.Join(goldenRepositoryRoot, "skills", "legion-architect", "references", "wake-routing.md"))
	if err != nil {
		t.Fatal(err)
	}
	held, err := os.ReadFile(goldenTextsPath)
	if err != nil {
		t.Fatal(err)
	}
	var golden goldenFile
	if err := json.Unmarshal(held, &golden); err != nil {
		t.Fatal(err)
	}
	hasReason := func(p map[string]any, _ string) bool { _, ok := p["reason"]; return ok }
	for _, tc := range []struct {
		kind, skillSays string
		holds           func(payload map[string]any, raw string) bool
		what            string
	}{
		{"pr-blocked", "A `pr-blocked` reason reads `<n> rounds counted, <m> conflict rounds not counted`",
			func(_ map[string]any, raw string) bool {
				return strings.Contains(raw, "rounds counted") && strings.Contains(raw, "conflict rounds not counted")
			}, "the sentence the skill names by its words (R8)"},
		{"review-stuck", "its `reason` names the merge state, each check run with its conclusion and each commit status with its state", hasReason, "a `reason`"},
		{"pull-request-missing", "open the pull request from the branch the reason names", hasReason, "a `reason`"},
		{"child-release-stalled", "release the parent first when the reason says so", hasReason, "a `reason`"},
		{"worker-stalled", "Judge from the reason whether the work needs a decision of yours", hasReason, "a `reason`"},
		{"catch-up", "Accept each issue its `reason` names as waiting for you", hasReason, "a `reason`"},
	} {
		if !strings.Contains(string(skill), tc.skillSays) {
			t.Logf("wake-routing.md no longer says %q", tc.skillSays)
			continue
		}
		for _, sample := range golden.Samples {
			if string(sample.Kind) != tc.kind || sample.Reader != "architect" {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal(sample.Payload, &payload); err != nil {
				t.Fatalf("decode %s: %v", sample.id(), err)
			}
			if !tc.holds(payload, string(sample.Payload)) {
				keys := make([]string, 0, len(payload))
				for k := range payload {
					keys = append(keys, k)
				}
				slices.Sort(keys)
				t.Errorf("wake-routing.md says %q, but %s carries no %s (its keys: %v)", tc.skillSays, sample.id(), tc.what, keys)
				break
			}
		}
	}
}
