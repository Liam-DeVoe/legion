package reviewer

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/agentloop"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/modeltest"
)

// rewriteReply is a rewrite's reply holding text.
func rewriteReply(t *testing.T, text Text) string {
	t.Helper()
	return asJSON(t, map[string]any{"body": text.Body, "options": optionsOrEmpty(text), "multiple": text.Multiple})
}

// A rewrite reads the writer's answer when one came, and the record alone when none did, and
// returns the text with the call that wrote it.
func TestARewriteReadsTheWritersAnswerWhenOneCame(t *testing.T) {
	stand := modeltest.New(t, modeltest.Rule{Contains: rewriteMarker, Reply: modeltest.Reply{Text: rewriteReply(t, keptRewrite())}})
	url := servedAs(t, stand, "rewriter", "rewriter-2026-11-01")
	question, err := RewriteQuestion("a-1", askRecord().Text)
	if err != nil {
		t.Fatal(err)
	}
	answer := Answer{Question: question, Text: "The node is the test server. Its first call fails."}

	rewritten, err := Rewrite(t.Context(), chatModel(url, "rewriter"), []RuleSet{RuleNoslop, RuleAskParts}, askRecord(), answer)
	if err != nil {
		t.Fatal(err)
	}
	if want := keptRewrite(); !reflect.DeepEqual(rewritten.Text, want) || rewritten.NotApplied != nil {
		t.Errorf("rewrite = %+v, not applied %v; want %+v applied", rewritten.Text, rewritten.NotApplied, want)
	}
	if len(rewritten.Calls) != 1 || rewritten.Calls[0] != (Call{Purpose: PurposeRewrite, Deployment: "rewriter", Served: "rewriter-2026-11-01", Usage: rewritten.Calls[0].Usage}) {
		t.Errorf("calls = %+v, want the rewrite's call to the rewriter, served by rewriter-2026-11-01", rewritten.Calls)
	}
	request := stand.Requests()[0].Text
	for _, want := range []string{
		"<question_to_writer>\nA person asked Dispatch to write your ask a-1 again",
		"<writer_answer>\nThe node is the test server. Its first call fails.\n</writer_answer>",
		`- "writer_answer": the answer of the writer agent.`,
		"Use only facts that are in the record or in the answer of the writer agent.",
		`### Rule set "ask_parts"`,
	} {
		if !strings.Contains(request, want) {
			t.Errorf("the rewrite's request does not hold %q", want)
		}
	}

	// No answer came: the rewrite reads the record alone (change 4).
	if _, err := Rewrite(t.Context(), chatModel(url, "rewriter"), []RuleSet{RuleNoslop}, askRecord(), Answer{}); err != nil {
		t.Fatal(err)
	}
	request = stand.Requests()[1].Text
	for _, absent := range []string{"<question_to_writer>", "<writer_answer>", "writer_answer", "the answer of the writer agent"} {
		if strings.Contains(request, absent) {
			t.Errorf("a rewrite with no answer holds %q", absent)
		}
	}
	if !strings.Contains(request, "<text kind=\"ask\" choice=\"single\">") || !strings.Contains(request, "<writer_texts>") {
		t.Errorf("a rewrite with no answer does not read the record: %s", request)
	}
}

// A rewrite that breaks a rule of what a rewrite keeps comes back not applied, with its reason.
func TestARewriteThatBreaksARuleIsNotApplied(t *testing.T) {
	broken := keptRewrite()
	broken.Options = []model.AskOption{{ID: "o1", Label: "Ship it", Description: "Release it today"}, broken.Options[1]}
	stand := modeltest.New(t, modeltest.Rule{Reply: modeltest.Reply{Text: rewriteReply(t, broken)}})
	rewritten, err := Rewrite(t.Context(), chatModel(stand.URL, "rewriter"), []RuleSet{RuleNoslop}, askRecord(), Answer{})
	if err != nil {
		t.Fatal(err)
	}
	want := &NotApplied{Rule: KeepOptionLabels, Reason: `option 1 has the label "Ship it" in the rewrite, and "Ship" in the ask`}
	if !reflect.DeepEqual(rewritten.NotApplied, want) {
		t.Errorf("not applied = %+v, want %+v", rewritten.NotApplied, want)
	}
	if !reflect.DeepEqual(rewritten.Text, broken) {
		t.Errorf("rewrite = %+v, want the model's, kept for its reason", rewritten.Text)
	}
}

// An issue's rewrite is its title and its spec.
func TestAnIssuesRewriteIsItsTitleAndItsSpec(t *testing.T) {
	stand := modeltest.New(t, modeltest.Rule{Reply: modeltest.Reply{Text: `{"title":"Review agent texts before a person reads them","body":"The server reviews each agent text."}`}})
	record := Record{Text: Text{Kind: KindIssue, Title: "Land the reviewer", Body: "This names the problem."}}
	rewritten, err := Rewrite(t.Context(), chatModel(stand.URL, "rewriter"), []RuleSet{RulePhrases}, record, Answer{})
	if err != nil {
		t.Fatal(err)
	}
	want := Text{Kind: KindIssue, Title: "Review agent texts before a person reads them", Body: "The server reviews each agent text."}
	if !reflect.DeepEqual(rewritten.Text, want) || rewritten.NotApplied != nil {
		t.Errorf("rewrite = %+v, %v; want %+v", rewritten.Text, rewritten.NotApplied, want)
	}
	if body := string(stand.Requests()[0].Body); !strings.Contains(body, `"required":["title","body"]`) {
		t.Errorf("an issue's rewrite does not ask for a title and a spec: %s", body)
	}
}

// A model that fails gives the rewrite's *ModelError, with its call.
func TestARewriteModelFailureIsAModelError(t *testing.T) {
	stand := modeltest.New(t, modeltest.Rule{Reply: modeltest.Reply{Status: http.StatusTooManyRequests, Text: "slow down"}})
	rewritten, err := Rewrite(t.Context(), chatModel(stand.URL, "rewriter"), []RuleSet{RuleNoslop}, askRecord(), Answer{})
	var failure *ModelError
	if !errors.As(err, &failure) || failure.Purpose != PurposeRewrite || failure.Fault != agentloop.FaultProviderError {
		t.Fatalf("err = %v, want the rewrite's provider error", err)
	}
	if len(rewritten.Calls) != 1 || rewritten.Calls[0].Fault != agentloop.FaultProviderError {
		t.Errorf("calls = %+v, want the failed call", rewritten.Calls)
	}
}
