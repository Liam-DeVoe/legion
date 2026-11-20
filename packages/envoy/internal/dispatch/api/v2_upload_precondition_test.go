package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The /api/v2 uploads take the base an upload over an existing document names as /api/v1's do
// (#3000): `precondition` is a member of the body the v2 decoder reads, JSON or multipart
// (jsonArtifactUpload), and the refusals are the ones /api/v1 gives, in the v2 shape. A
// replacement that names no token is 400 PRECONDITION_REQUIRED, a token the document has moved
// past is 409 PRECONDITION_FAILED naming the live token and version, a token naming blocks is 400
// INVALID_PRECONDITION, and the live token is taken.
func TestV2UploadReplacingADocumentNamesItsBase(t *testing.T) {
	handler, database := uploadBaseHandler(t)
	issue := createArtifactIssue(t, handler)
	artifactID := seedUploadedDocument(t, handler, issue.Key, "notes.md", "# Notes\n\nbefore\n")
	target := "/api/v2/issues/" + issue.Key + "/artifacts"
	upload := func(body map[string]any) (int, string, string) {
		t.Helper()
		response := dispatchRequest(t, handler, http.MethodPost, target, body, "alice")
		if response.Code == http.StatusCreated {
			return response.Code, "", response.Body.String()
		}
		code, _ := refusalOf(t, response)
		return response.Code, code, response.Body.String()
	}
	versionsBefore := uploadedVersionCount(t, database, artifactID)
	base := uploadBase(t, handler, artifactID)

	if status, code, body := upload(map[string]any{"name": "notes.md", "content": "# Notes\n\nafter\n"}); status != http.StatusBadRequest || code != "PRECONDITION_REQUIRED" {
		t.Fatalf("a v2 replacement naming no token = %d %s, want 400 PRECONDITION_REQUIRED: %s", status, code, body)
	}
	blocks := readDocumentPreconditionBlocks(t, handler, artifactID)
	if status, code, body := upload(map[string]any{
		"name": "notes.md", "content": "# Notes\n\nafter\n",
		"precondition": map[string]any{"blocks": []map[string]string{{"id": blocks[0].ID, "token": blocks[0].Token}}},
	}); status != http.StatusBadRequest || code != "INVALID_PRECONDITION" {
		t.Fatalf("a v2 replacement naming blocks as its base = %d %s, want 400 INVALID_PRECONDITION: %s", status, code, body)
	}
	if after := uploadedVersionCount(t, database, artifactID); after != versionsBefore {
		t.Fatalf("the refused v2 uploads wrote: %d versions, want the %d standing before them", after, versionsBefore)
	}

	taken := dispatchRequest(t, handler, http.MethodPost, target, map[string]any{
		"name": "notes.md", "content": "# Notes\n\nafter\n", "precondition": base,
	}, "alice")
	if taken.Code != http.StatusCreated {
		t.Fatalf("a v2 replacement naming the live token = %d, want 201: %s", taken.Code, taken.Body.String())
	}
	if version := decodeBody[struct {
		Version model.Version `json:"version"`
	}](t, taken).Version; version.Number != versionsBefore+1 {
		t.Fatalf("the v2 replacement wrote version %d, want %d", version.Number, versionsBefore+1)
	}
	live := readDocumentPrecondition(t, handler, artifactID)

	behind := dispatchRequest(t, handler, http.MethodPost, target, map[string]any{
		"name": "notes.md", "content": "# Notes\n\nagain\n", "precondition": base,
	}, "alice")
	if behind.Code != http.StatusConflict {
		t.Fatalf("a v2 replacement naming a token behind the document = %d, want 409: %s", behind.Code, behind.Body.String())
	}
	conflict := decodeBody[uploadPreconditionConflict](t, behind)
	if conflict.Code != "PRECONDITION_FAILED" || conflict.Current.Document != live.Token ||
		conflict.Current.Version == nil || *conflict.Current.Version != versionsBefore+1 {
		t.Fatalf("the v2 refusal of a stale token = %s, want PRECONDITION_FAILED naming token %s at version %d",
			behind.Body.String(), live.Token, versionsBefore+1)
	}
	if !strings.Contains(conflict.Error, "--precondition-json") {
		t.Fatalf("the v2 refusal = %q, want it to say how to upload again naming the live token", conflict.Error)
	}
	if after := uploadedVersionCount(t, database, artifactID); after != versionsBefore+1 {
		t.Fatalf("the refused v2 upload wrote: %d versions, want %d", after, versionsBefore+1)
	}
}

// A v2 multipart upload names its base in the form field `precondition`, the same JSON object a
// JSON body's member holds: the live token is taken, and a field that holds no JSON value is
// refused by the v2 decoder and writes nothing. A first upload of a name, which replaces no
// version, is refused a token as /api/v1 refuses it.
func TestV2MultipartUploadNamesItsBaseInItsForm(t *testing.T) {
	handler, database := uploadBaseHandler(t)
	issue := createArtifactIssue(t, handler)
	artifactID := seedUploadedDocument(t, handler, issue.Key, "notes.md", "# Notes\n\nbefore\n")
	target := "/api/v2/issues/" + issue.Key + "/artifacts"
	versionsBefore := uploadedVersionCount(t, database, artifactID)

	malformed := multipartRequest(t, handler, target, map[string]string{"name": "notes.md", "precondition": "not json"},
		"notes.md", "text/markdown", []byte("# Notes\n\nafter\n"), "alice")
	if code, text := refusalOf(t, malformed); malformed.Code != http.StatusBadRequest || !strings.Contains(text, "precondition") {
		t.Fatalf("a v2 multipart upload whose base is no JSON = %d %s %q, want 400 naming the precondition field", malformed.Code, code, text)
	}
	if after := uploadedVersionCount(t, database, artifactID); after != versionsBefore {
		t.Fatalf("the refused v2 multipart upload wrote: %d versions, want %d", after, versionsBefore)
	}

	replaced := multipartRequest(t, handler, target,
		map[string]string{"name": "notes.md", "precondition": uploadBaseField(t, handler, artifactID)},
		"notes.md", "text/markdown", []byte("# Notes\n\nafter\n"), "alice")
	if replaced.Code != http.StatusCreated {
		t.Fatalf("a v2 multipart upload naming the live token = %d, want 201: %s", replaced.Code, replaced.Body.String())
	}
	if after := uploadedVersionCount(t, database, artifactID); after != versionsBefore+1 {
		t.Fatalf("the v2 multipart upload left %d versions, want %d", after, versionsBefore+1)
	}

	first := multipartRequest(t, handler, target,
		map[string]string{"name": "fresh.md", "precondition": encodeUploadBase(t, map[string]string{"document": "sha256:nothing"})},
		"fresh.md", "text/markdown", []byte("# Fresh\n"), "alice")
	if code, _ := refusalOf(t, first); first.Code != http.StatusConflict || code != "PRECONDITION_FAILED" {
		t.Fatalf("a v2 first upload naming a token = %d %s, want 409 PRECONDITION_FAILED: %s", first.Code, code, first.Body.String())
	}
}

// A question opened through /api/v2 on an issue whose specification carries a person's review of
// its newest version names that review on the write's advice (standing_review), as the /api/v1
// create does: both open the ask through openAsk.
func TestV2AskAdviceNamesThePersonsStandingReview(t *testing.T) {
	handler := newTestHandler(t)
	createAdviceProject(t, handler, "STANDV2")
	spec := "# Transport\n\nThe design, as read.\n"
	issue := createAdviceIssue(t, handler, "STANDV2", "Reviewed spec", &spec)
	written := reviewDocument(t, handler, issue.PrimaryArtifactID, "alice", "approved", standingReviewReason)

	asked := sessionRequest(t, handler, http.MethodPost, "/api/v2/issues/"+issue.Key+"/asks", map[string]any{
		"question": standingReviewQuestion, "actor": adviceSessionActor("session-asking"),
	})
	review := adviceFromResponse(t, asked.Code, asked.Body.String()).StandingReview
	if review == nil {
		t.Fatalf("the v2 question's advice named no review; want alice's of version 1: %s", asked.Body.String())
	}
	if review.By != "alice" || review.State != "approved" || review.Version != 1 || review.Reason != standingReviewReason ||
		!review.At.Equal(written.CreatedAt) {
		t.Fatalf("the v2 question's standing review = %#v, want alice's approval of version 1 with its reason", review)
	}
}

// GET /api/v2/issues/{key}/references walks the closure GET /api/v1/issues/{key}/references
// walks, under the same bound (issueClosure): a walk past referencesClosureBound answers 503
// REFERENCES_TIMEOUT at about that bound, as /api/v1 does. refs is locked in another connection,
// so the walk waits however fast it would run; the read before the lock answers 200.
func TestV2IssueReferencesRefuseAtTheClosureBound(t *testing.T) {
	handler, database := newTestHandlerWithStore(t)
	createReferenceAPIProject(t, handler, "CORE")
	issue := createReferenceAPIIssue(t, handler, "CORE")
	target := "/api/v2/issues/" + issue.Key + "/references"
	if response := dispatchRequest(t, handler, http.MethodGet, target, nil, "alice"); response.Code != http.StatusOK {
		t.Fatalf("v2 references before the lock: status=%d body=%s, want 200", response.Code, response.Body.String())
	}
	holdLock(t, database, "lock table refs in access exclusive mode")
	started := time.Now()
	response := dispatchRequest(t, handler, http.MethodGet, target, nil, "alice")
	took := time.Since(started)
	if code, text := refusalOf(t, response); response.Code != http.StatusServiceUnavailable || code != "REFERENCES_TIMEOUT" ||
		!strings.Contains(text, referencesClosureBound.String()) {
		t.Fatalf("v2 references past the bound = %d %s %q, want 503 REFERENCES_TIMEOUT naming the %s bound", response.Code, code, text, referencesClosureBound)
	}
	if took < referencesClosureBound || took > referencesClosureBound+3*time.Second {
		t.Errorf("v2 references past the bound answered after %s, want about its %s bound", took, referencesClosureBound)
	}
}
