package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/metrics"
)

// The /api/v2 uploads take the base an upload over an existing document names as /api/v1's do
// (#3000): `precondition` is a member of the body the v2 decoder reads, JSON or multipart
// (jsonArtifactUpload), and the answers are the ones /api/v1 gives, in the v2 shape. A
// replacement that names no token is taken unchecked and counted, as /api/v1 takes it, until
// every deployed client sends one (INFRA-4980); a token naming blocks is 400 INVALID_PRECONDITION;
// the live token is taken and not counted; and a token the document has moved past is still 409
// PRECONDITION_FAILED naming the live token and version, and the field the token goes in.
func TestV2UploadReplacingADocumentChecksTheBaseItNames(t *testing.T) {
	registry := metrics.New()
	handler, database, _ := newTestServer(t, testServerOptions{settle: time.Hour, metrics: registry})
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

	unchecked := "# Notes\n\nunchecked\n"
	if status, code, body := upload(map[string]any{"name": "notes.md", "content": unchecked}); status != http.StatusCreated {
		t.Fatalf("a v2 replacement naming no token = %d %s, want 201: %s", status, code, body)
	}
	if after := uploadedVersionCount(t, database, artifactID); after != versionsBefore+1 {
		t.Fatalf("the v2 replacement naming no token left %d versions, want %d", after, versionsBefore+1)
	}
	if read := readDocumentPrecondition(t, handler, artifactID); read.Markdown != unchecked {
		t.Fatalf("the document after the v2 replacement naming no token = %q, want %q", read.Markdown, unchecked)
	}
	if sample, want := unguardedReplacementSample(t, registry), unguardedReplacementsMetric+" 1"; sample != want {
		t.Fatalf("after the v2 replacement naming no token the counter reads %q, want %q", sample, want)
	}
	versionsBefore++
	base := uploadBase(t, handler, artifactID)

	blocks := readDocumentPreconditionBlocks(t, handler, artifactID)
	if status, code, body := upload(map[string]any{
		"name": "notes.md", "content": "# Notes\n\nafter\n",
		"precondition": map[string]any{"blocks": []map[string]string{{"id": blocks[0].ID, "token": blocks[0].Token}}},
	}); status != http.StatusBadRequest || code != "INVALID_PRECONDITION" {
		t.Fatalf("a v2 replacement naming blocks as its base = %d %s, want 400 INVALID_PRECONDITION: %s", status, code, body)
	}
	if after := uploadedVersionCount(t, database, artifactID); after != versionsBefore {
		t.Fatalf("the refused v2 upload wrote: %d versions, want the %d standing before it", after, versionsBefore)
	}

	taken := dispatchRequest(t, handler, http.MethodPost, target, map[string]any{
		"name": "notes.md", "content": "# Notes\n\nafter\n", "precondition": base,
	}, "alice")
	if taken.Code != http.StatusCreated {
		t.Fatalf("a v2 replacement naming the live token = %d, want 201: %s", taken.Code, taken.Body.String())
	}
	if version := decodeBody[uploadedArtifact](t, taken).Version; version.Number != versionsBefore+1 {
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
	if !strings.Contains(conflict.Error, "dispatch doc-read") || !strings.Contains(conflict.Error, "precondition") {
		t.Fatalf("the v2 refusal = %q, want it to say the live token comes from `dispatch doc-read` and to upload again naming it as the precondition", conflict.Error)
	}
	if strings.Contains(conflict.Error, "--precondition-json") {
		t.Fatalf("the v2 refusal = %q, want it to name no flag of a command", conflict.Error)
	}
	if after := uploadedVersionCount(t, database, artifactID); after != versionsBefore+1 {
		t.Fatalf("the refused v2 upload wrote: %d versions, want %d", after, versionsBefore+1)
	}
	if sample, want := unguardedReplacementSample(t, registry), unguardedReplacementsMetric+" 1"; sample != want {
		t.Fatalf("the counter reads %q, want %q: the one replacement naming no token, and none of the uploads naming a token", sample, want)
	}
}

// A v2 multipart upload names its base in the form field `precondition`, the same JSON object a
// JSON body's member holds: the live token is taken, and a field that holds no JSON value is
// refused by the v2 decoder and writes nothing. A form that names no token is taken unchecked and
// counted, as /api/v1 takes it (INFRA-4980). A first upload of a name, which replaces no version,
// is refused a token as /api/v1 refuses it.
func TestV2MultipartUploadNamesItsBaseInItsForm(t *testing.T) {
	registry := metrics.New()
	handler, database, _ := newTestServer(t, testServerOptions{settle: time.Hour, metrics: registry})
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

	unchecked := multipartRequest(t, handler, target, map[string]string{"name": "notes.md"},
		"notes.md", "text/markdown", []byte("# Notes\n\nunchecked\n"), "alice")
	if unchecked.Code != http.StatusCreated {
		t.Fatalf("a v2 multipart upload naming no token = %d, want 201: %s", unchecked.Code, unchecked.Body.String())
	}
	if after := uploadedVersionCount(t, database, artifactID); after != versionsBefore+2 {
		t.Fatalf("the v2 multipart upload naming no token left %d versions, want %d", after, versionsBefore+2)
	}

	first := multipartRequest(t, handler, target,
		map[string]string{"name": "fresh.md", "precondition": encodeUploadBase(t, map[string]string{"document": "sha256:nothing"})},
		"fresh.md", "text/markdown", []byte("# Fresh\n"), "alice")
	if code, _ := refusalOf(t, first); first.Code != http.StatusConflict || code != "PRECONDITION_FAILED" {
		t.Fatalf("a v2 first upload naming a token = %d %s, want 409 PRECONDITION_FAILED: %s", first.Code, code, first.Body.String())
	}
	if sample, want := unguardedReplacementSample(t, registry), unguardedReplacementsMetric+" 1"; sample != want {
		t.Fatalf("the counter reads %q, want %q: the one multipart replacement naming no token", sample, want)
	}
}
