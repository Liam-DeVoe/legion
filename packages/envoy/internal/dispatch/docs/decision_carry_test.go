package docs

import (
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// The restore's and the conversion's carry cases (dispatch://INFRA-1150 R7 and R9; restoreCase,
// conversionCase) over the two versions, as carryNewestApproval feeds carryVerdict: the restore's
// answered asks in the spec's `## Decisions` part, in place of its pointer line to the issue's
// decisions document, under the heading the restore adds, and under a new block id, carry the
// approval, and so does such a block the approved version held elsewhere with the same words; a
// restored block whose words moved does not. The conversion's decisions in that part carry it,
// and so do the `shaped` pointers it gives an answered ask that stands in the spec already, beside
// a decision and a reading in one version (spec v42's R9); a decision outside the part does not.
// Each verdict names the cases it carried by, in carryCases' order. Every row is weighed twice:
// as the decisions step's own write, and as another writer's - an agent's upload or edit making
// the same change - which carries nothing by those two cases.
func TestTheRestoreAndTheConversionCarryAnApprovalForTheStepsOwnWriteAlone(t *testing.T) {
	const prose = "# Rollout\n\n:::requirement{#r1}\nThe service ships behind a flag.\n:::\n\n"
	const decisionsDocument = "DOC-1/decisions-md"
	const pointer = "The decisions this spec rests on are in dispatch://DOC-1/artifact/decisions-md.\n"
	ask := func(id, question, extra string) string {
		return `:::ask{#` + id + ` urgency="low" multiple="false"` + extra + "}\n" + question + "\n\n- East: The quiet one.\n- West: The busy one.\n:::\n"
	}
	askWithReading := func(id, extra string) string {
		return `::::ask{#` + id + ` urgency="low" multiple="false"` + extra + "}\nWhich region goes first?\n\n- East: The quiet one.\n- West: The busy one.\n\n" +
			":::reading{#r9}\nThe agent reads it as the east first.\n:::\n::::\n"
	}
	decision := ":::decision{#d1}\nThe census runs before the view.\n:::\n"
	shaped := ` shaped="dispatch://DOC-1/spec#b-r1"`
	part := "## Decisions\n\n"
	restored := restoreCase.change + carriedReasonEnd
	converted := conversionCase.change + carriedReasonEnd
	parse := func(markdown string) *pmdoc.Node {
		t.Helper()
		tree, err := pmdoc.ParseRendering(markdown)
		if err != nil {
			t.Fatalf("parse %q: %v", markdown, err)
		}
		return tree
	}
	for _, test := range []struct {
		name              string
		approved, version string
		decided, answered map[string]bool
		change            string
	}{
		{"an answered ask returned in place of the pointer line", prose + part + pointer, prose + part + ask("q1", "Which region goes first?", ""),
			map[string]bool{"q1": true}, nil, restored},
		{"an answered ask returned under the heading the restore adds", prose, prose + part + ask("q1", "Which region goes first?", ""),
			map[string]bool{"q1": true}, nil, restored},
		{"an answered ask the approved version held elsewhere, returned with its words", prose + ask("q1", "Which region goes first?", "") + "\n" + part + pointer,
			prose + part + ask("q1", "Which region goes first?", ""), map[string]bool{"q1": true}, nil, restored},
		{"an answered ask returned under a new block id", prose + part + pointer, prose + part + ask("q-fresh", "Which region goes first?", ""),
			map[string]bool{"q-fresh": true}, nil, restored},
		{"an answered ask returned with its question edited", prose + ask("q1", "Which region goes first?", "") + "\n" + part + pointer,
			prose + part + ask("q1", "Which region goes last?", ""), map[string]bool{"q1": true}, nil, ""},
		{"an open ask in the Decisions part", prose + part + pointer, prose + part + ask("q1", "Which region goes first?", ""),
			map[string]bool{"q1": false}, nil, ""},
		{"a decision in place of the pointer line", prose + part + pointer, prose + part + decision, nil, nil, converted},
		{"a decision under the heading the conversion adds", prose, prose + part + decision, nil, nil, converted},
		{"a shaped pointer the conversion gives a present answered ask, beside a decision and a reading",
			prose + ask("q1", "Which region goes first?", "") + "\n" + part + pointer,
			prose + askWithReading("q1", shaped) + "\n" + part + decision,
			map[string]bool{"q1": true}, map[string]bool{"q1": true}, readingCase.change + ", and " + conversionCase.change + carriedReasonEnd},
		{"a decision outside the Decisions part", prose + part + pointer, prose + decision + "\n" + part + pointer, nil, nil, ""},
		{"a restored ask and a decision in one version", prose + part + pointer,
			prose + part + ask("q1", "Which region goes first?", "") + "\n" + decision,
			map[string]bool{"q1": true}, nil, restoreCase.change + ", and " + conversionCase.change + carriedReasonEnd},
		{"prose changed beside a decision", prose + part + pointer, strings.Replace(prose, "a flag", "a switch", 1) + part + decision, nil, nil, ""},
	} {
		approvedText, err := pmdoc.Render(parse(test.approved))
		if err != nil {
			t.Fatalf("%s: render the approved version: %v", test.name, err)
		}
		approved, version := parse(approvedText), parse(test.version)
		for _, step := range []bool{true, false} {
			input := carryInput{
				approved: approved, approvedText: approvedText, version: version, agrees: true,
				answered: test.answered, decided: test.decided, decisions: map[string]bool{decisionsDocument: true},
			}
			want := test.change
			if step {
				input.steps = decisionStepCases{restore: true, conversion: true}
			} else if want != "" {
				// Another writer's version making the same change: a route's, an agent's upload.
				want = ""
			}
			input.restored = restoredAskBlocks(approved, version, test.decided)
			change, carried, err := carryVerdict(input)
			if err != nil {
				t.Fatalf("%s: %v", test.name, err)
			}
			if carried != (want != "") || change != want {
				t.Errorf("%s, the step's own write %v: carry = %v with the change %q, want carry %v with %q", test.name, step, carried, change, want != "", want)
			}
		}
	}
}
