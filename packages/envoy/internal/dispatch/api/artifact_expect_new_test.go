package api

import (
	"context"
	"net/http"
	"testing"
)

// TestUploadExpectNewMakesADocumentAndNeverWritesOverOne (review round 6, finding 3): expect_new
// makes an upload a create alone, decided under the owner's lock: under a name the owner already
// holds it is refused, 409 DOCUMENT_EXISTS, and nothing is written - the document keeps its one
// version - and under a free name it makes the document. It holds for an issue's JSON upload, a
// project's, and a multipart upload, and it cannot be sent with expect_sha256, which asks for the
// opposite.
func TestUploadExpectNewMakesADocumentAndNeverWritesOverOne(t *testing.T) {
	handler, database := newTestHandlerWithStore(t)
	issue := createInteractionIssue(t, handler, "DEMO", "Uploads that only make", "The spec.\n").Key
	versions := func(t *testing.T, owner, name string) int {
		t.Helper()
		var count int
		if err := database.Pool.QueryRow(context.Background(), `
			select count(*) from artifact_versions v join artifacts a on a.id = v.artifact_id
			where a.name = $2 and (a.issue_key = $1 or (a.issue_key is null and a.project_key = $1))`, owner, name).Scan(&count); err != nil {
			t.Fatalf("count the versions of %s's %s: %v", owner, name, err)
		}
		return count
	}
	for _, owner := range []struct{ path, key string }{{"/api/v1/issues/" + issue, issue}, {"/api/v1/projects/DEMO", "DEMO"}} {
		t.Run(owner.path, func(t *testing.T) {
			uploadDocument(t, handler, owner.path, "held.md", "# Held\n\nThe first version.\n")
			over := dispatchRequest(t, handler, http.MethodPost, owner.path+"/artifacts",
				map[string]any{"name": "held.md", "content": "# Held\n\nWritten over.\n", "expect_new": true}, "bob")
			if over.Code != http.StatusConflict || decodeBody[errorBody](t, over).Code != "DOCUMENT_EXISTS" {
				t.Fatalf("an upload expecting a new document over held.md answered %d %s, want 409 DOCUMENT_EXISTS", over.Code, over.Body.String())
			}
			if count := versions(t, owner.key, "held.md"); count != 1 {
				t.Fatalf("held.md holds %d versions after the refused upload, want its one", count)
			}
			made := dispatchRequest(t, handler, http.MethodPost, owner.path+"/artifacts",
				map[string]any{"name": "fresh.md", "content": "# Fresh\n", "expect_new": true}, "bob")
			if made.Code != http.StatusCreated {
				t.Fatalf("an upload expecting a new document under a free name answered %d %s, want 201", made.Code, made.Body.String())
			}
			both := dispatchRequest(t, handler, http.MethodPost, owner.path+"/artifacts",
				map[string]any{"name": "held.md", "content": "# Held\n", "expect_new": true, "expect_sha256": "00"}, "bob")
			if both.Code != http.StatusBadRequest {
				t.Fatalf("an upload expecting both a new document and a held one answered %d %s, want 400", both.Code, both.Body.String())
			}
		})
	}

	t.Run("multipart", func(t *testing.T) {
		uploadDocument(t, handler, "/api/v1/issues/"+issue, "picture-notes.md", "# Notes\n")
		over := multipartRequest(t, handler, "/api/v1/issues/"+issue+"/artifacts",
			map[string]string{"name": "picture-notes.md", "expect_new": "true"}, "picture-notes.md", "text/markdown", []byte("# Written over\n"), "bob")
		if over.Code != http.StatusConflict || decodeBody[errorBody](t, over).Code != "DOCUMENT_EXISTS" {
			t.Fatalf("a multipart upload expecting a new document over a held name answered %d %s, want 409 DOCUMENT_EXISTS", over.Code, over.Body.String())
		}
		if count := versions(t, issue, "picture-notes.md"); count != 1 {
			t.Fatalf("picture-notes.md holds %d versions after the refused upload, want its one", count)
		}
	})
}
