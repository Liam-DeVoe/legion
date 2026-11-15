package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/writingcheck/plainwords"
)

// The settings read answers the refusal as the deployment set it: off with no rule set where it
// set nothing, and its choice where it set one.
func TestTheWritingRulesSettingsAnswerWhatTheDeploymentSet(t *testing.T) {
	bare := newTestHandler(t)
	read := dispatchRequest(t, bare, http.MethodGet, "/api/v1/settings/writing-rules", nil, "alice")
	if read.Code != http.StatusOK {
		t.Fatalf("read: status=%d body=%s", read.Code, read.Body.String())
	}
	if got := decodeBody[writingRulesSettings](t, read); got.Refusal != "off" || got.Extra == nil || len(got.Extra) != 0 {
		t.Fatalf("a deployment that set nothing = %+v, want refusal off and extra []", got)
	}

	handler, _, _ := newTestServer(t, testServerOptions{writingRules: WritingRulesConfig{
		Refusal: true, Extra: []string{WritingRulesAskParts, WritingRulesReaderWords},
	}})
	got := decodeBody[writingRulesSettings](t, dispatchRequest(t, handler, http.MethodGet, "/api/v1/settings/writing-rules", nil, "alice"))
	if got.Refusal != "on" || !slices.Equal(got.Extra, []string{"ask_parts", "reader_words"}) {
		t.Fatalf("a deployment that set both = %+v, want refusal on and both sets in its order", got)
	}
}

// The rules for a write are the skill's text whole, for the person who reads that write as the
// writing check decides it; the parts of an ask come after them for an ask alone, and no reader
// has asked about a word until R8's records exist.
func TestTheWritingRulesAnswerTheSkillForTheWritesReader(t *testing.T) {
	handler, _, _ := newTestServer(t, testServerOptions{writingRules: WritingRulesConfig{
		Refusal: true, Extra: []string{WritingRulesReaderWords, WritingRulesAskParts},
	}})
	key := createTestIssue(t, handler, "RUL", "The written rules")
	issue := decodeBody[struct {
		Assignee *string `json:"assignee"`
	}](t, dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+key, nil, "alice"))
	if issue.Assignee == nil || *issue.Assignee == "" {
		t.Fatalf("issue %s has no assignee to read the write", key)
	}

	rules := func(route string, body any) writingRulesResponse {
		t.Helper()
		answer := dispatchRequest(t, handler, http.MethodPost, "/api/v1/writing-rules",
			map[string]any{"route": route, "body": body}, "alice")
		if answer.Code != http.StatusOK {
			t.Fatalf("rules for %s: status=%d body=%s", route, answer.Code, answer.Body.String())
		}
		return decodeBody[writingRulesResponse](t, answer)
	}

	comment := rules("POST /api/v1/issues/"+key+"/comments", map[string]any{"body": "The deploy finished."})
	if comment.Reader != *issue.Assignee {
		t.Fatalf("a comment's reader = %q, want the issue's assignee %q", comment.Reader, *issue.Assignee)
	}
	if comment.Text != plainwords.Rules {
		t.Fatalf("a comment's rules are not the skill's text alone: %d characters, want %d", len(comment.Text), len(plainwords.Rules))
	}
	if !strings.Contains(comment.Text, "## The phrases the owner asked to stop") || !strings.Contains(comment.Text, "# Pattern catalog") {
		t.Fatal("the rules lack the six phrases or the pattern catalog")
	}

	ask := rules("POST /api/v1/issues/"+key+"/asks", map[string]any{"question": "Which window?"})
	if !strings.HasPrefix(ask.Text, plainwords.Rules) || !strings.Contains(ask.Text, "## The parts of an ask") {
		t.Fatalf("an ask's rules = %q, want the skill's text and then the parts of an ask", ask.Text[len(plainwords.Rules):])
	}
	if strings.Contains(ask.Text, "## Words that this reader asked about") {
		t.Fatal("the rules name words a reader asked about, and no record of one exists")
	}

	// A new issue's reader comes from its body: the person it names.
	created := rules("POST /api/v1/issues", map[string]any{"project": "RUL", "title": "Next", "assignee": "bob"})
	if want := model.CanonicalLogin("bob"); created.Reader != want {
		t.Fatalf("a new issue's reader = %q, want the person it names, %q", created.Reader, want)
	}

	// A send to an agent's own conversation has no person as its reader.
	if sent := rules("POST /api/v1/agents/ses-1/messages", map[string]any{"body": "Hello."}); sent.Reader != "" {
		t.Fatalf("a send to a session's reader = %q, want none", sent.Reader)
	}

	// The route takes any write a JSON route takes, whose own limit is maxJSONRequestBytes, with
	// the object around it.
	long := strings.Repeat("a", int(maxJSONRequestBytes)-64)
	if full := rules("POST /api/v1/issues/"+key+"/comments", map[string]any{"body": long}); full.Reader != *issue.Assignee {
		t.Fatalf("a comment at the JSON limit: reader %q, want %q", full.Reader, *issue.Assignee)
	}
}

// A route that is no write whose text a person may read, or a body that is not the JSON a write to
// the route sends, is refused, naming what was wrong; the settings off do not change the answer.
func TestTheWritingRulesRefuseAWriteNoReaderRuleServes(t *testing.T) {
	handler := newTestHandler(t)
	for _, route := range []string{
		"GET /api/v1/issues",
		"POST /api/v1/nowhere",
		"POST",
		"POST http://elsewhere.example/api/v1/issues",
		"",
	} {
		answer := dispatchRequest(t, handler, http.MethodPost, "/api/v1/writing-rules",
			map[string]any{"route": route, "body": map[string]any{}}, "alice")
		if answer.Code != http.StatusBadRequest || !strings.Contains(answer.Body.String(), "INVALID_BODY") {
			t.Errorf("route %q: status=%d body=%s, want 400 INVALID_BODY", route, answer.Code, answer.Body.String())
		}
	}
	wrong := dispatchRequest(t, handler, http.MethodPost, "/api/v1/writing-rules",
		map[string]any{"route": "POST /api/v1/issues", "body": map[string]any{"title": 7}}, "alice")
	if wrong.Code != http.StatusBadRequest || !strings.Contains(wrong.Body.String(), "is not the JSON a write to POST /api/v1/issues sends") {
		t.Fatalf("a body of the wrong shape: status=%d body=%s, want 400 naming the route", wrong.Code, wrong.Body.String())
	}
	if answer := dispatchRequest(t, handler, http.MethodPost, "/api/v1/writing-rules",
		map[string]any{"route": "POST /api/v1/issues"}, "alice"); answer.Code != http.StatusBadRequest || !strings.Contains(answer.Body.String(), "body is required") {
		t.Fatalf("no body: status=%d body=%s, want 400 saying the body is required", answer.Code, answer.Body.String())
	}
}
