package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A text a replacement upload refuses for the text alone is refused by the round trip asked
// without new_document, the most permissive replacement's answer, with that upload's own status,
// code and words. `dispatch artifact --force --path` over a held document takes the file round
// trip's refusal in its dry run and the upload's refusal in its call, so the two print one refusal
// only where the two answers are one. The tester found at 5a574bff, on real servers, that the
// upload of such a replacement refuses block HTML and markdown no reader reads back as
// "INVALID_MARKDOWN: replace live document: markdown is not a Proof document: ...", where the file
// round trip answers "INVALID_MARKDOWN: markdown is not a Proof document: ...", so the dry run and
// the call printed two refusals (INFRA-4224 tester round 16).
func TestARoundTripWithoutNewDocumentRefusesInTheReplacementUploadsOwnWords(t *testing.T) {
	handler := newTestHandler(t)
	issue := createArtifactIssue(t, handler)
	uploads := "/api/v1/issues/" + issue.Key + "/artifacts"
	for index, test := range []struct{ name, text string }{
		{"block HTML", "<details>x</details>"},
		{"markdown whose rendering no reader reads back", "2. [^N]:\n       12. *\n       --\n"},
		{"12,000 list items", strings.Repeat("- a\n", 12_000)},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, kind := range []string{"inline", "file"} {
				name := fmt.Sprintf("held-%d-%s.md", index, kind)
				held := dispatchRequest(t, handler, http.MethodPost, uploads, map[string]string{"name": name, "content": "Held.\n"}, "alice")
				if held.Code != http.StatusCreated {
					t.Fatalf("upload %s: status=%d body=%.300s, want 201", name, held.Code, held.Body.String())
				}
				id := artifactIDNamed(t, handler, uploads, name)
				var replaced *httptest.ResponseRecorder
				if kind == "file" {
					replaced = multipartRequest(t, handler, uploads, map[string]string{"name": name, "precondition": uploadBaseField(t, handler, id)},
						name, "text/markdown", []byte(test.text), "alice")
				} else {
					replaced = dispatchRequest(t, handler, http.MethodPost, uploads, map[string]any{
						"name": name, "content": test.text, "precondition": uploadBase(t, handler, id),
					}, "alice")
				}
				roundTrip := roundTripOf(t, handler, kind, test.text, false)
				code, message := refusalOf(t, replaced)
				tripCode, tripMessage := refusalOf(t, roundTrip)
				if replaced.Code == http.StatusCreated || replaced.Code == http.StatusOK {
					t.Fatalf("the %s replacement of %s was stored (%d), want it refused", kind, name, replaced.Code)
				}
				if roundTrip.Code != replaced.Code || tripCode != code || tripMessage != message {
					t.Errorf("the %s round trip without new_document answers %d %s %.200q, want the replacement upload's %d %s %.200q",
						kind, roundTrip.Code, tripCode, tripMessage, replaced.Code, code, message)
				}
			}
		})
	}
}
