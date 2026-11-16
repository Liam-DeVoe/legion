package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// mergeBaseSentences are the sentences two existing rows' descriptions carried at this slice's
// merge base (legion/INFRA-3862-nodes at 1ae82444c2, read from its own GET /api/v1), each written
// whole. c-blob-unchanged (dispatch://INFRA-3862/artifact/verification-md#b-c-blob-unchanged)
// allows a head to add sentences to an existing row's description that document a field or a
// refusal it adds, and fails on any other difference, "a changed or removed sentence included".
var mergeBaseSentences = map[string][]string{
	"POST /api/v1/projects": {
		"Create a project {key, name, default_assignee}: any signed-in person or agent session; a bearer names its session in actor.",
		"Every project names the person its unowned work lands on, the last step of the chain that decides who a question reaches: default_assignee is the email of a person who has signed in, and a call that names none takes the person creating the project, else the person its credential acts for.",
		"A credential that acts for nobody and names no default_assignee is 400 INVALID_PROJECT; a login nobody has signed in with, the one the call names or the one its credential acts for, is 400 ASSIGNEE_NOT_ALLOWED.",
		"The answer is the project with the default_assignee the create stored.",
	},
	"GET /api/v1/whoami": {
		"Who the server takes the caller for:",
	},
}

// TestExistingRouteDescriptionsKeepEverySentenceOfTheMergeBase (R9, written by the A1 tester):
// every sentence a row's description held at the merge base is still in it, whole and unchanged,
// so the head's description differs from the merge base's only by added sentences. At
// 5944573316 POST /api/v1/projects rewrote its first sentence ("any signed-in person" became "any
// signed-in full member ...; a restricted member is 403 RESTRICTED_MEMBER"), which the check
// fails as a changed sentence; the refusal is documented by adding a sentence instead.
func TestExistingRouteDescriptionsKeepEverySentenceOfTheMergeBase(t *testing.T) {
	handler, _, _ := newTestServer(t, testServerOptions{})
	response := dispatchRequest(t, handler, http.MethodGet, "/api/v1", nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/v1: %d %s", response.Code, response.Body.String())
	}
	var index struct {
		Routes []struct {
			Method      string `json:"method"`
			Path        string `json:"path"`
			Description string `json:"description"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &index); err != nil {
		t.Fatalf("decode GET /api/v1: %v", err)
	}
	descriptions := map[string]string{}
	for _, route := range index.Routes {
		descriptions[route.Method+" "+route.Path] = route.Description
	}
	for route, sentences := range mergeBaseSentences {
		description, ok := descriptions[route]
		if !ok {
			t.Errorf("%s is not in the route index", route)
			continue
		}
		for _, sentence := range sentences {
			if !strings.Contains(description, sentence) {
				t.Errorf("%s no longer carries its merge-base sentence %q; c-blob-unchanged allows only added sentences. Its description now reads:\n%s",
					route, sentence, description)
			}
		}
	}
}
