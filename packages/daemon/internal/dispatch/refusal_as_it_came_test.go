package dispatch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestADispatchRefusalIsReadFromItsCodeAsItCame: Dispatch refuses a read with a coded refusal
// whose error names a URL right before the JSON quote that ends it, which the rule cannot read as
// clean, so it prints the whole body as not readable. The refusal is still Dispatch's code: the
// client decodes the code as it came (oauth2call.CodedRefusal, its code where the rule prints it
// unchanged), so the refusal is the permanent one Dispatch made and not a codeless answer from
// whatever sits in front of it, and its message prints as the rule reads it, which names no URL.
func TestADispatchRefusalIsReadFromItsCodeAsItCame(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"code":"INVALID_TRANSITION","error":"read the workflow at https://dispatch.example/docs#transitions"}`))
	}))
	t.Cleanup(server.Close)
	_, err := New(server.URL, dispatchToken).GetIssue(context.Background(), "an-issue")
	var refusal *Error
	if !errors.As(err, &refusal) || refusal.Code != "INVALID_TRANSITION" {
		t.Fatalf("a refusal coded INVALID_TRANSITION beside a URL = %#v, want Dispatch's code", err)
	}
	if _, permanent := PermanentRefusal(err); !permanent {
		t.Errorf("PermanentRefusal(%v) = false, want the coded 422 Dispatch made to be permanent", err)
	}
	if strings.Contains(err.Error(), "#transitions") {
		t.Errorf("the refusal prints %q, want Dispatch's words as the rule reads them", err)
	}
}

// TestADispatchRefusalsSentenceIsReadAsItCame: Dispatch's refusals whose sentence a decision reads
// - the 400 INVALID_JSON naming a field a Dispatch from before it refuses (PermanentRefusal), and a
// router's 404 NOT_FOUND "no route for" (NoRoute) - are decided on the sentence as it came (Error's
// Said), never on the rule's reading of it (Error's Message): a sentence whose URL holds a quote,
// which the rule prints as not readable (url), and one that repeats a run of the token the request
// carried, which the rule cuts from it (token). Each decision still holds, and the error prints
// as the rule reads the sentence, which names neither the URL's query nor the token's run.
func TestADispatchRefusalsSentenceIsReadAsItCame(t *testing.T) {
	for _, c := range []struct {
		name, token, body, hidden string
		status                    int
		decided                   func(error) bool
	}{
		{"newer-field-url", dispatchToken, `{"code":"INVALID_JSON","error":"invalid JSON body: json: unknown field \"labels_add\"; see https://dispatch.example/fields?f=\"labels_add\""}`, `f="labels_add"`,
			http.StatusBadRequest, func(err error) bool { _, permanent := PermanentRefusal(err); return !permanent }},
		{"newer-field-token", "dsp_labels_add_9f3c1e7a", `{"code":"INVALID_JSON","error":"invalid JSON body: json: unknown field \"labels_add\""}`, "labels_add",
			http.StatusBadRequest, func(err error) bool { _, permanent := PermanentRefusal(err); return !permanent }},
		{"no-route-url", dispatchToken, `{"code":"NOT_FOUND","error":"no route for GET /api/v1/issues/an-issue; see https://dispatch.example/routes?r=\"issues\""}`, `r="issues"`,
			http.StatusNotFound, NoRoute},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			t.Cleanup(server.Close)
			_, err := New(server.URL, c.token).GetIssue(context.Background(), "an-issue")
			if !c.decided(err) {
				t.Errorf("Dispatch's %d %s = %v, want it decided on its sentence as it came", c.status, c.body, err)
			}
			if err != nil && strings.Contains(err.Error(), c.hidden) {
				t.Errorf("the refusal prints %q, want its sentence as the rule reads it", err)
			}
		})
	}
}
