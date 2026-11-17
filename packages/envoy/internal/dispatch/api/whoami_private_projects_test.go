package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
)

// TestWhoamiAnswersAListedCredentialsProjectsAndWhichArePrivate (plan t-a-16, KF4 and KF4-F;
// c-a-private): GET /api/v1/whoami answers a credential that lists projects its projects, sorted,
// and private_projects, the keys of that list the caller must treat as private: a private project,
// and one the credential no longer sees whatever its visibility. A credential that lists nothing
// answers neither field (R9).
func TestWhoamiAnswersAListedCredentialsProjectsAndWhichArePrivate(t *testing.T) {
	handler, database, _ := newTestServer(t, testServerOptions{})
	seedCredentialProjects(t, handler, database)
	seedPeople(t, database, accessErin)
	markRestricted(t, database, accessErin)
	makePrivate(t, database, privateProject)

	whoami := func(token string) map[string]json.RawMessage {
		t.Helper()
		response := agentRequest(t, handler, http.MethodGet, "/api/v1/whoami", nil, token)
		if response.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/whoami = %d %s", response.Code, response.Body.String())
		}
		return decodeBody[map[string]json.RawMessage](t, response)
	}
	list := func(answer map[string]json.RawMessage, field string) []string {
		t.Helper()
		raw, present := answer[field]
		if !present {
			return nil
		}
		var keys []string
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatalf("%s is not a list of keys: %s", field, raw)
		}
		return keys
	}

	// carol, PRIV's owner, lists both projects out of order.
	carols := mintListed(t, handler, accessCarol, "carol-both", []string{publicProject, privateProject})
	answer := whoami(carols.Token)
	if got := list(answer, "projects"); !slices.Equal(got, []string{privateProject, publicProject}) {
		t.Errorf("carol's token listing PUB and PRIV: projects = %q, want [PRIV PUB]", got)
	}
	if got := list(answer, "private_projects"); !slices.Equal(got, []string{privateProject}) {
		t.Errorf("carol's token listing PUB and PRIV: private_projects = %q, want [PRIV]: the answer was %s", got, mustJSON(t, answer))
	}

	// erin, a restricted member PRIV's list names, lists PRIV; PRIV then turns public and erin is
	// taken off its list, so erin no longer sees PRIV, and her token's PRIV answers private.
	if response := dispatchRequest(t, handler, http.MethodPut, "/api/v1/projects/PRIV/access/people/"+accessErin, nil, accessCarol); response.Code != http.StatusOK {
		t.Fatalf("carol names erin on PRIV's list: %d %s", response.Code, response.Body.String())
	}
	erins := mintListed(t, handler, accessErin, "erin-priv", []string{privateProject})
	if _, err := database.Pool.Exec(t.Context(), `update projects set visibility = 'public' where key = $1`, privateProject); err != nil {
		t.Fatalf("make PRIV public: %v", err)
	}
	if response := dispatchRequest(t, handler, http.MethodDelete, "/api/v1/projects/PRIV/access/people/"+accessErin, nil, accessCarol); response.Code != http.StatusOK {
		t.Fatalf("carol takes erin off PRIV's list: %d %s", response.Code, response.Body.String())
	}
	answer = whoami(erins.Token)
	if got := list(answer, "projects"); !slices.Equal(got, []string{privateProject}) {
		t.Errorf("erin's token listing PRIV: projects = %q, want [PRIV]", got)
	}
	if got := list(answer, "private_projects"); !slices.Equal(got, []string{privateProject}) {
		t.Errorf("erin's token listing PRIV, which erin no longer sees: private_projects = %q, want [PRIV]: the answer was %s", got, mustJSON(t, answer))
	}

	// R9: a token that lists nothing answers neither field.
	alices := mintListed(t, handler, "alice", "alice-none", nil)
	answer = whoami(alices.Token)
	for _, field := range []string{"projects", "private_projects"} {
		if _, present := answer[field]; present {
			t.Errorf("alice's token that lists nothing answers %s: %s", field, mustJSON(t, answer))
		}
	}
}
