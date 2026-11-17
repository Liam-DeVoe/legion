package api

import (
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/api/apicode"
)

// TestV2Refusals is c-refusals of dispatch://INFRA-4071 (plan t16 step 3) as far as the api
// package reaches it: a missing issue's comments are 404 ISSUE_NOT_FOUND; the route tour's
// missing-objects subtest covers every route whose path names an object Dispatch stores; a route
// whose path names a session, and the app-installation read, answer as /api/v1 does; and a parent
// loop and a dependency loop are declared with one code. The document's code list equalling the
// declared codes is TestTheV2DocumentListsEveryCodeItCanAnswer (openapi_test.go), which leaves out
// API_V1_ENDED because only /api/v1 answers it; one condition with one code is
// TestOneConditionOneCode; the unknown path, /auth/callback and the origin guard are
// TestAuthAndOriginRefusalsCarryACode in the routes package.
func TestV2Refusals(t *testing.T) {
	tour := newRouteTour(t)

	t.Run("a missing issue's comments", func(t *testing.T) {
		response := dispatchRequest(t, tour.handler, http.MethodGet, "/api/v2/issues/NOPE-9/comments", nil, "alice")
		if code, _ := refusalOf(t, response); response.Code != http.StatusNotFound || code != "ISSUE_NOT_FOUND" {
			t.Fatalf("GET /api/v2/issues/NOPE-9/comments answered %d %s, want 404 ISSUE_NOT_FOUND", response.Code, response.Body.String())
		}
	})

	t.Run("every path object a route names is a missing-objects step", func(t *testing.T) {
		covered := map[string]map[string]bool{}
		for _, step := range tourSteps() {
			for _, missing := range step.Missing {
				if covered[step.route()] == nil {
					covered[step.route()] = map[string]bool{}
				}
				covered[step.route()][missing.Param] = true
			}
		}
		for _, name := range slices.Sorted(maps.Keys(tour.routes)) {
			route := tour.routes[name]
			for _, wildcard := range patternWildcards(route.Pattern) {
				if !namesAStoredObject(route, wildcard) {
					continue
				}
				if !covered[name][wildcard] {
					t.Errorf("%s names {%s}, an object Dispatch stores, and no step of the tour sends it missing", name, wildcard)
				}
			}
		}
	})

	t.Run("a session's route and the app-installation read answer as /api/v1 does", func(t *testing.T) {
		const session = "session-nobody-ever-ran"
		v1 := dispatchRequest(t, tour.handler, http.MethodGet, "/api/v1/agents/"+session+"/messages", nil, "alice")
		v2 := dispatchRequest(t, tour.handler, http.MethodGet, "/api/v2/agents/"+session+"/messages", nil, "alice")
		if listed := decodeBody[page[agentMessageV2]](t, v2); v1.Code != http.StatusOK || v1.Body.String() != "[]\n" ||
			v2.Code != http.StatusOK || listed.Total != 0 || len(listed.Items) != 0 {
			t.Fatalf("a session Dispatch has no row of: /api/v1 %d %s, /api/v2 %d %s, want both 200 and empty",
				v1.Code, v1.Body.String(), v2.Code, v2.Body.String())
		}
		v1 = dispatchRequest(t, tour.handler, http.MethodGet, "/api/v1/github/app-installation/nobody/nothing", nil, "alice")
		v2 = dispatchRequest(t, tour.handler, http.MethodGet, "/api/v2/repositories/nobody/nothing/app-installation", nil, "alice")
		if v1.Code != http.StatusOK || v2.Code != v1.Code || !jsonEqual(t, v2.Body.Bytes(), v1.Body.String()) {
			t.Fatalf("a repository GitHub does not show the App: /api/v1 %d %s, /api/v2 %d %s, want one answer",
				v1.Code, v1.Body.String(), v2.Code, v2.Body.String())
		}
	})

	t.Run("a parent loop and a dependency loop answer one code", func(t *testing.T) {
		declared := map[apicode.Condition]apicode.Code{}
		for _, route := range tour.routes {
			for _, refusal := range route.Refusals {
				if refusal.Condition == apicode.ParentLoop || refusal.Condition == apicode.DependencyLoop {
					declared[refusal.Condition] = refusal.Code
				}
			}
		}
		parent, dependency := declared[apicode.ParentLoop], declared[apicode.DependencyLoop]
		if parent.Name() != "ISSUE_CYCLE" || dependency.Name() != "ISSUE_CYCLE" || parent.Status() != http.StatusConflict || dependency.Status() != http.StatusConflict {
			t.Fatalf("the parent loop is declared %s %d and the dependency loop %s %d, want both 409 ISSUE_CYCLE",
				parent.Name(), parent.Status(), dependency.Name(), dependency.Status())
		}
	})
}

// namesAStoredObject reports whether the wildcard of route's path names a row Dispatch stores,
// whose absence the route answers with that object's 404 (decision 11): every wildcard but a
// {session_id} (ruling A), the {owner}/{repo} of the app-installation read (ruling A) and of a PUT
// that creates the repository row, a mark's {kind}, and the {id} of a deploy run, which is GitHub's
// run id of the deployment's deploy repository: decision 11 names no deploy run among the objects,
// and the read keeps /api/v1's 404 NOT_FOUND for a run Dispatch has not mirrored.
func namesAStoredObject(route apiRoute, wildcard string) bool {
	switch {
	case wildcard == "session_id":
		return false
	case (wildcard == "owner" || wildcard == "repo") && (strings.HasSuffix(route.Pattern, "/app-installation") || route.Method == http.MethodPut):
		return false
	case wildcard == "kind" && strings.Contains(route.Pattern, "/marks/{kind}/{id}"):
		return false
	case wildcard == "id" && strings.HasSuffix(route.Pattern, "/deploy-runs/{id}"):
		return false
	}
	return true
}
