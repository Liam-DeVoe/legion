package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// R3 of dispatch://INFRA-1523 and the architect's ruling of 2026-11-20 20:55Z (N1): a pending
// insert suggestion's words were the post's own and never document text, so once its author
// deletes it, no read of a stored document version gives them, though the version stores them:
// storage keeps the bytes, and the read masks them. A version named while the insert stood pending
// gave the words after the delete, by its id and by the issue's slug (test round at 8259e87d, in
// chromium: GET /api/v1/artifacts/{id}/versions/2, 6 s and 36 s after the delete).
func TestAStoredVersionGivesNoWordOfAPendingInsertDeletedSince(t *testing.T) {
	f := newAnchorsOnAnInsert(t)
	named := dispatchRequest(t, f.handler, http.MethodPost, "/api/v1/artifacts/"+f.artifactID+"/versions",
		map[string]string{"summary": "With the insert pending"}, "alice")
	if named.Code != http.StatusCreated {
		t.Fatalf("name the version holding the pending insert: status=%d body=%s", named.Code, named.Body.String())
	}
	var version struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(named.Body.Bytes(), &version); err != nil || version.Number < 2 {
		t.Fatalf("the named version = %s (%v), want a version after the first", named.Body.String(), err)
	}
	waitForArtifactVersion(t, f.handler, f.artifactID, version.Number)
	reads := map[string]string{
		"by id":   fmt.Sprintf("/api/v1/artifacts/%s/versions/%d", f.artifactID, version.Number),
		"by slug": fmt.Sprintf("/api/v1/issues/%s/artifacts/spec/versions/%d", f.issueKey, version.Number),
	}
	read := func(path string) string {
		t.Helper()
		response := dispatchRequest(t, f.handler, http.MethodGet, path, nil, "bob")
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s: status=%d body=%s", path, response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	// The positive control: while the insert stands pending, each read of the version gives its words.
	for name, path := range reads {
		if body := read(path); !strings.Contains(body, insertedWords) {
			t.Fatalf("the version read %s before the delete gives no %q, so its check after the delete cannot fail: %s", name, insertedWords, body)
		}
	}

	deleteThreadComment(t, f.handler, f.insert.ID, "alice")
	for name, path := range reads {
		if body := read(path); strings.Contains(body, insertedWords) {
			t.Errorf("the version read %s after the insert's delete gives its words %q: %s", name, insertedWords, body)
		}
	}
}
