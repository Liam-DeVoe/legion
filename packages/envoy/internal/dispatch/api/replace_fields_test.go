package api

import (
	"net/http"
	"strings"
	"testing"
)

// A replace whose with is absent, or that carries a field replace does not take, is refused 400
// REPLACE_FIELDS naming its operation and the field, and the document keeps its text and its
// versions. Read as an empty with, such a replace deleted the text its find matched, which a caller
// that sent markdown in place of with met (dispatch://INFRA-1946). Each refused replace stands
// second in its batch, after a replace that would write, so the refusal names operation 1 and the
// batch writes nothing.
func TestAReplaceWithoutItsWithIsRefused(t *testing.T) {
	for _, test := range []struct {
		name  string
		op    map[string]any
		wants []string
	}{
		{
			name:  "a replace with no with",
			op:    map[string]any{"op": "replace", "find": "Planning notes here."},
			wants: []string{"operation 1: replace names no with"},
		},
		{
			name:  "a replace whose with is null",
			op:    map[string]any{"op": "replace", "find": "Planning notes here.", "with": nil},
			wants: []string{"operation 1: replace names no with"},
		},
		{
			name:  "a replace carrying a field replace does not take",
			op:    map[string]any{"op": "replace", "find": "Planning notes here.", "with": "Plan.", "after": "Intro."},
			wants: []string{"operation 1: replace carries after, which replace does not take"},
		},
		{
			name:  "a replace carrying markdown in place of with",
			op:    map[string]any{"op": "replace", "find": "Planning notes here.", "markdown": "Plan."},
			wants: []string{"operation 1: replace names no with", "carries markdown, which replace does not take"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := newTestHandler(t)
			issue := createInteractionIssue(t, handler, "TEST", "Replace fields", "Intro.\n\nPlanning notes here.\n\nOutro.\n")
			before, versions := documentMarkdown(t, handler, issue.PrimaryArtifactID), len(documentVersions(t, handler, issue.PrimaryArtifactID))
			response := sessionRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+issue.PrimaryArtifactID+"/edits", map[string]any{
				"ops":   []map[string]any{{"op": "replace", "find": "Intro.", "with": "Opening."}, test.op},
				"actor": sessionActor(),
			})
			body := decodeBody[struct {
				Code  string `json:"code"`
				Error string `json:"error"`
			}](t, response)
			if response.Code != http.StatusBadRequest || body.Code != "REPLACE_FIELDS" {
				t.Fatalf("replace %v: status=%d code=%q error=%q, want 400 REPLACE_FIELDS", test.op, response.Code, body.Code, body.Error)
			}
			for _, want := range test.wants {
				if !strings.Contains(body.Error, want) {
					t.Fatalf("refusal = %q, want it to say %q", body.Error, want)
				}
			}
			if after := documentMarkdown(t, handler, issue.PrimaryArtifactID); after != before {
				t.Fatalf("document after the refused batch = %q, want %q unchanged", after, before)
			}
			if after := len(documentVersions(t, handler, issue.PrimaryArtifactID)); after != versions {
				t.Fatalf("versions after the refused batch = %d, want %d", after, versions)
			}
		})
	}
}

// An explicit with "" is how a caller deletes a replace's match on purpose, and it still does.
func TestAReplaceWithAnEmptyWithDeletesItsMatch(t *testing.T) {
	handler := newTestHandler(t)
	issue := createInteractionIssue(t, handler, "TEST", "Empty with", "Intro.\n\nPlanning notes here.\n\nOutro.\n")
	response := sessionRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+issue.PrimaryArtifactID+"/edits", map[string]any{
		"ops":   []map[string]any{{"op": "replace", "find": " notes", "with": ""}},
		"actor": sessionActor(),
	})
	if response.Code != http.StatusOK {
		t.Fatalf("replace with an empty with: status=%d body=%s", response.Code, response.Body.String())
	}
	if after, want := documentMarkdown(t, handler, issue.PrimaryArtifactID), "Intro.\n\nPlanning here.\n\nOutro.\n"; after != want {
		t.Fatalf("document after the replace = %q, want %q", after, want)
	}
}
