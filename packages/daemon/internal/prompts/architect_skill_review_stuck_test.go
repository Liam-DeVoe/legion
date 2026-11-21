package prompts

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// skills/legion-architect/references/wake-routing.md, which the branch leaves as main has it,
// says of a review-stuck that GitHub will not merge: "its `reason` names the merge state, each
// check run with its conclusion and each commit status with its state". That was main's notice
// (workflow/review.go:stuckMergeStateReason and headChecks at d46ec4e0: "GitHub reported on head
// %s: \"<name>\" <conclusion>, …; commit statuses: \"<ctx>\" <state>"). The branch's review-stuck
// of that cause carries no `reason`: it has merge_state, not_passed and not_passed_statuses, and
// counts the passed ones (passed), as go/architect-common.md says. INFRA-3686's R2: "It gives no
// step that the reader's role prompt or a skill gives"; the skill the architect routes by must not
// contradict the record and the prompt.
func TestTheArchitectSkillReadsTheMergeStateReviewStuckAsTheBranchWritesIt(t *testing.T) {
	skill, err := os.ReadFile("../../../../skills/legion-architect/references/wake-routing.md")
	if err != nil {
		t.Fatal(err)
	}
	var row string
	for _, line := range strings.Split(string(skill), "\n") {
		if strings.HasPrefix(line, "| `review-stuck` |") {
			row = line
		}
	}
	golden, err := os.ReadFile("../daemon/testdata/texts.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Samples []struct {
			Kind    string          `json:"kind"`
			Variant string          `json:"variant"`
			Payload json.RawMessage `json:"payload"`
		} `json:"samples"`
	}
	if err := json.Unmarshal(golden, &file); err != nil {
		t.Fatal(err)
	}
	for _, sample := range file.Samples {
		if sample.Kind != "review-stuck" || sample.Variant != "merge-state" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(sample.Payload, &payload); err != nil {
			t.Fatalf("decode the merge-state review-stuck payload: %v", err)
		}
		if _, has := payload["reason"]; !has && strings.Contains(row, "its `reason` names the merge state, each check run with its conclusion") {
			t.Fatalf("wake-routing.md's review-stuck row says %q, but the branch's merge-state review-stuck has no reason field and counts the passed checks (passed: %v)",
				"its `reason` names the merge state, each check run with its conclusion and each commit status with its state", payload["passed"])
		}
		return
	}
	t.Fatalf("no merge-state review-stuck sample")
}
