package api

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// removedInBatchSpec holds one bullet list between two paragraphs.
const removedInBatchSpec = "Intro.\n\n- old one\n- old two\n\nAfter.\n"

// postEdits posts ops to an artifact's edits as the session, and answers the status, the code
// and the error the route served.
func postEdits(t *testing.T, handler http.Handler, artifactID string, ops []map[string]any) (int, string, string) {
	t.Helper()
	response := sessionRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+artifactID+"/edits", map[string]any{
		"ops":   ops,
		"actor": sessionActor(),
	})
	body := decodeBody[struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}](t, response)
	return response.Code, body.Code, body.Error
}

// listIDOf is the id of the block GET …/blocks lists whose markdown is the spec's one list.
func listIDOf(t *testing.T, handler http.Handler, artifactID string) string {
	t.Helper()
	for markdown, id := range blockIDsByText(t, handler, artifactID) {
		if strings.TrimSpace(markdown) == "- old one\n- old two" {
			return id
		}
	}
	t.Fatalf("no block of %s is the list", artifactID)
	return ""
}

// An operation whose target an earlier operation of the same edit removed is refused 400
// TARGET_REMOVED_IN_BATCH, naming the operation, what it named and the operation that removed
// it, and the document keeps its text and its versions: the replace ahead of both is not written
// either. Each operation reads the document as the operations before it left it, and the block
// the insert names is gone by then (dispatch://INFRA-4517). Nothing settles the spec while the
// row runs, so its versions are the ones the row read.
func TestAnOperationWhoseBlockAnEarlierOperationRemovedIsRefused(t *testing.T) {
	handler, _, _ := newTestServer(t, testServerOptions{settle: time.Hour})
	issue := createInteractionIssue(t, handler, "BATCH", "Removed in the batch", removedInBatchSpec)
	list := listIDOf(t, handler, issue.PrimaryArtifactID)
	before, versions := documentMarkdown(t, handler, issue.PrimaryArtifactID), len(documentVersions(t, handler, issue.PrimaryArtifactID))
	status, code, refusal := postEdits(t, handler, issue.PrimaryArtifactID, []map[string]any{
		{"op": "replace", "find": "Intro.", "with": "Opening."},
		{"op": "delete", "block": list},
		{"op": "insert", "after": "block:" + list, "markdown": "- new"},
	})
	if status != http.StatusBadRequest || code != "TARGET_REMOVED_IN_BATCH" {
		t.Fatalf("insert after a block the batch deleted: status=%d code=%q error=%q, want 400 TARGET_REMOVED_IN_BATCH", status, code, refusal)
	}
	if want := `operation 2: after "block:` + list + `" names a block that operation 1, delete {block:"` + list + `"}, removed earlier in this edit`; !strings.Contains(refusal, want) {
		t.Fatalf("refusal = %q, want it to say %q", refusal, want)
	}
	if after := documentMarkdown(t, handler, issue.PrimaryArtifactID); after != before {
		t.Fatalf("document after the refused batch = %q, want %q unchanged", after, before)
	}
	if after := len(documentVersions(t, handler, issue.PrimaryArtifactID)); after != versions {
		t.Fatalf("versions after the refused batch = %d, want %d", after, versions)
	}
}

// A list inserted after a list's own block, and then that block deleted, in one edit, leaves the
// inserted items where the list stood: the insert joins the list it is anchored on, and the delete
// removes what the list held when the edit began. Version 336 of
// dispatch://INFRA/artifact/superfan-fans-log-md lost both lists (dispatch://INFRA-4517).
func TestAListInsertedAfterAListTheSameEditDeletesIsKept(t *testing.T) {
	handler := newTestHandler(t)
	issue := createInteractionIssue(t, handler, "BATCH", "Kept in the batch", removedInBatchSpec)
	list := listIDOf(t, handler, issue.PrimaryArtifactID)
	if status, code, refusal := postEdits(t, handler, issue.PrimaryArtifactID, []map[string]any{
		{"op": "insert", "after": "block:" + list, "markdown": "- new one\n- new two"},
		{"op": "delete", "block": list},
	}); status != http.StatusOK {
		t.Fatalf("insert after a list, then delete it: status=%d code=%q error=%q, want 200", status, code, refusal)
	}
	if after, want := documentMarkdown(t, handler, issue.PrimaryArtifactID), "Intro.\n\n- new one\n- new two\n\nAfter.\n"; after != want {
		t.Fatalf("document after the batch = %q, want %q", after, want)
	}
}
