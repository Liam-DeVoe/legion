package api

import (
	"net/http"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/api/apicode"
)

func init() { addTourSteps("legion", legionTourSteps) }

// tourDaemonURL is the Legion daemon the tour names: a base URL Dispatch stores and never calls
// within the tour, since the sampler does not run there.
const tourDaemonURL = "http://legion.acme.test:9731"

// legionTourSteps are the route tour's steps of a project's Legion daemon and board: the board of
// a project whose daemon has never answered, the daemon read of a project that names one and of
// one that names none, the naming of a daemon and the naming of the one it already names, and the
// removal.
func legionTourSteps() []tourStep {
	alice := tourPerson("alice")
	projectMissing := []tourMissing{{Param: "key", Value: "NOPE", Code: apicode.ProjectNotFound}}
	onNamed := func(body any) func(*testing.T, *routeTour) tourRequest {
		return func(t *testing.T, tour *routeTour) tourRequest {
			return tourRequest{Path: map[string]string{"key": tour.legionProject(t)}, Body: body, As: alice}
		}
	}
	return []tourStep{
		{
			Method: http.MethodGet, Pattern: "/projects/{key}/legion-board", Status: http.StatusOK,
			Setup:   onNamed(nil),
			Missing: projectMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/projects/{key}/legion-daemon", Status: http.StatusOK,
			Setup:   onNamed(nil),
			Missing: projectMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/projects/{key}/legion-daemon", Name: "a project that names none",
			Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				return tourRequest{Path: map[string]string{"key": tour.project(t)}, As: alice}
			},
			Missing: projectMissing,
		},
		{
			Method: http.MethodPut, Pattern: "/projects/{key}/legion-daemon", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				return tourRequest{
					Path: map[string]string{"key": tour.project(t)},
					Body: map[string]any{"state_url": tourDaemonURL}, As: alice,
				}
			},
			Missing: projectMissing,
		},
		{
			Method: http.MethodPut, Pattern: "/projects/{key}/legion-daemon", Name: "the daemon it already names",
			Status:  http.StatusOK,
			Setup:   onNamed(map[string]any{"state_url": tourDaemonURL}),
			Missing: projectMissing,
		},
		{
			Method: http.MethodDelete, Pattern: "/projects/{key}/legion-daemon", Status: http.StatusNoContent,
			Setup:   onNamed(nil),
			Missing: projectMissing,
		},
	}
}

// legionProject makes a project of the tour's own that names tourDaemonURL its Legion daemon,
// answering its key. The daemon is named through the /api/v1 route, so a step's own route is
// never what set up the state it reads.
func (tour *routeTour) legionProject(t *testing.T) string {
	t.Helper()
	key := tour.project(t)
	named := dispatchRequest(t, tour.handler, http.MethodPut, "/api/v1/projects/"+key+"/legion/daemon",
		map[string]string{"state_url": tourDaemonURL}, "alice")
	if named.Code != http.StatusOK {
		t.Fatalf("name the Legion daemon of %s: %d %s", key, named.Code, named.Body.String())
	}
	return key
}

// TestV2LegionDaemonPutOfTheNamedDaemonWritesNothing: a PUT naming the daemon a project already
// names is the no-op PUT of dispatch://INFRA-4071/spec R8, whoever sends it: it answers the setting
// as it stands, its updated_at kept, while a PUT naming another daemon is a change, which moves it.
func TestV2LegionDaemonPutOfTheNamedDaemonWritesNothing(t *testing.T) {
	tour := newRouteTour(t)
	key := tour.legionProject(t)
	read := func() legionSettingResponse {
		t.Helper()
		answer := dispatchRequest(t, tour.handler, http.MethodGet, "/api/v2/projects/"+key+"/legion-daemon", nil, "alice")
		if answer.Code != http.StatusOK {
			t.Fatalf("read the Legion daemon of %s: %d %s", key, answer.Code, answer.Body.String())
		}
		return decodeBody[legionSettingResponse](t, answer)
	}
	put := func(login, stateURL string) legionSettingResponse {
		t.Helper()
		answer := dispatchRequest(t, tour.handler, http.MethodPut, "/api/v2/projects/"+key+"/legion-daemon",
			map[string]string{"state_url": stateURL}, login)
		if answer.Code != http.StatusOK {
			t.Fatalf("name %s the Legion daemon of %s as %s: %d %s", stateURL, key, login, answer.Code, answer.Body.String())
		}
		return decodeBody[legionSettingResponse](t, answer)
	}
	stored := read()
	if same := put("bob", tourDaemonURL+"/"); same != stored {
		t.Fatalf("a PUT of the daemon %s names answered %+v, want the setting as it stands %+v", key, same, stored)
	}
	if after := read(); after != stored {
		t.Fatalf("after a PUT of the daemon it names, %s reads %+v, want %+v unchanged", key, after, stored)
	}
	moved := put("bob", "http://legion-two.acme.test:9731")
	if moved.StateURL != "http://legion-two.acme.test:9731" || moved.UpdatedAt == stored.UpdatedAt {
		t.Fatalf("a PUT of another daemon answered %+v, want the new state_url and a later updated_at than %s", moved, stored.UpdatedAt)
	}
}
