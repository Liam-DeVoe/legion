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
