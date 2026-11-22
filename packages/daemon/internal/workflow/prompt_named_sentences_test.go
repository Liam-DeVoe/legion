package workflow

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// everyStringOfRecord is every string value of rec as its payload carries it, at any depth.
func everyStringOfRecord(t *testing.T, rec any) []string {
	t.Helper()
	encoded, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("encode %T: %v", rec, err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode %T: %v", rec, err)
	}
	var found []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			found = append(found, v)
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(decoded)
	return found
}

// Main's merger prompt (prompts/roles/merger.md at d46ec4e0 and c03d3afe, lines 82 and 89) names
// the gate-moved message by its words: "A message reading `The design gate of <tree> closed:
// version <n> of its <step> document (<id>)`" and "The message reading `The design gate of
// <tree> opened: a person approved version <n> of its <step> document (<id>), and every step the
// deployment gates now stands open`", the sentences workflow/gate.go:tellGateMoved wrote ("The
// design gate of %s closed: version %d of its %s document (%s) is with its reviewer, ..." and "The
// design gate of %s opened: a person approved version %d of its %s document (%s), and every step
// the deployment gates now stands open, ..."). INFRA-3686's R8: "A sentence that a prompt or a
// skill names by its words, such as "this resume is your design-approved wake", also stays word
// for word."
func TestTheGateMovedSentencesTheMergersPromptNamesStayWordForWord(t *testing.T) {
	for _, tc := range []struct {
		open  bool
		named string
	}{
		{false, "The design gate of LEGION-208 closed: version 4 of its spec document (artifact-208)"},
		{true, "The design gate of LEGION-208 opened: a person approved version 4 of its spec document (artifact-208), and every step the deployment gates now stands open"},
	} {
		values := everyStringOfRecord(t, GateMovedMessage("LEGION-208", record.DesignStepSpec, "artifact-208", 4, tc.open))
		if !slices.ContainsFunc(values, func(value string) bool { return strings.Contains(value, tc.named) }) {
			t.Errorf("the gate-moved message (open %t) carries %q, and no field holds the sentence the merger's prompt named by its words: %q",
				tc.open, values, tc.named)
		}
	}
}

// Main's architect prompt (prompts/go/architect-common.md, line 31 at d46ec4e0 and line 42 at
// c03d3afe) names the child-status notice of a join by its words: "its reason reads `<KEY>
// joined the tree of <ROOT> from the admission queue`" and "its reason reads `<KEY> joined the
// tree of <ROOT> from the closed tree of <OLD ROOT>`", the reason workflow/adopt.go:joinedTheTree
// wrote ("%s joined the tree of %s from %s", from "the admission queue" in Adopt and "the closed
// tree of "+member.Tree in AdoptMember). INFRA-3686's R8: "A sentence that a prompt or a skill
// names by its words, such as "this resume is your design-approved wake", also stays word for
// word."
func TestTheJoinedSentencesTheArchitectsPromptNamesStayWordForWord(t *testing.T) {
	for _, tc := range []struct {
		from  string
		named string
	}{
		{"", "LEGION-209 joined the tree of LEGION-208 from the admission queue"},
		{"LEGION-207", "LEGION-209 joined the tree of LEGION-208 from the closed tree of LEGION-207"},
	} {
		values := everyStringOfRecord(t, JoinedTheTreeNotice("LEGION-209", "LEGION-208", tc.from))
		if !slices.ContainsFunc(values, func(value string) bool { return strings.Contains(value, tc.named) }) {
			t.Errorf("the joined child-status notice (from %q) carries %q, and no field holds the sentence the architect's prompt named by its words: %q",
				tc.from, values, tc.named)
		}
	}
}
