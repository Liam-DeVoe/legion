// enroll_projects_test.go pins the enrollment route's projects on the real broker api_test.go
// mounts: POST /v1/enrollments takes a session's projects and answers each of the project
// refusals by its own code, and GET /v1/enrollments/self answers the projects the session named.
package api_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/broker/api"
	"github.com/sjawhar/envoy/internal/broker/projectaccess"
	"github.com/sjawhar/envoy/internal/broker/projectaccess/projectaccesstest"
)

// TestSelfAnswersTheProjects: a session enrolled naming PUB and PRIV, which its person may see,
// reads them back sorted from GET /v1/enrollments/self, and a session naming none reads an empty
// list; a session naming a project its person cannot see is refused 403 PROJECT_NOT_VISIBLE, one
// naming a key that is no project key 400 PROJECTS_INVALID, and one naming a project while
// Dispatch is down 503 PROJECTS_UNCHECKED.
func TestSelfAnswersTheProjects(t *testing.T) {
	dispatch := projectaccesstest.NewServer(t,
		map[string]projectaccesstest.Row{
			"PRIV": {Visibility: "private", Owner: testApprover},
			"PUB":  {Visibility: "public", Owner: testApprover},
			"ELSE": {Visibility: "private", Owner: "someone-else@example.com"},
			// LATE is asked about only while Dispatch is down, so no cached answer stands in.
			"LATE": {Visibility: "public", Owner: testApprover},
		}, nil)
	ts := newTestServerWith(t, func(d *api.Deps) {
		d.Enroll.Projects = projectaccess.New(dispatch.URL, dispatch.Account, nil)
	})
	credentialID, machineKey := ts.mintLauncherCredential(t, testApprover, "example-host-devbox")
	enrol := func(runtimeID string, projects any) (int, []byte, string) {
		key := newSigningKey(t)
		body := map[string]any{"kind": "session", "runtime_id": runtimeID, "operator": testApprover, "thumbprint": thumbprintOf(t, key)}
		if projects != nil {
			body["projects"] = projects
		}
		status, raw := ts.launcher(t, machineKey, credentialID, http.MethodPost, "/v1/enrollments", body)
		if status != http.StatusCreated {
			return status, raw, ""
		}
		enrollmentID := decode[wireEnrolled](t, raw).EnrollmentID
		status, raw = ts.session(t, key, enrollmentID, http.MethodGet, "/v1/enrollments/self", nil)
		return status, raw, enrollmentID
	}
	type self struct {
		Projects []string `json:"projects"`
	}
	for _, c := range []struct {
		runtimeID string
		projects  any
		want      []string
	}{
		{"session-both-" + t.Name(), []string{"PUB", "PRIV"}, []string{"PRIV", "PUB"}},
		{"session-none-" + t.Name(), nil, []string{}},
	} {
		status, raw, _ := enrol(c.runtimeID, c.projects)
		got := decode[self](t, raw)
		if status != http.StatusOK || got.Projects == nil || !slices.Equal(got.Projects, c.want) {
			t.Fatalf("GET /v1/enrollments/self of a session naming %v = %d %s; want projects %q", c.projects, status, raw, c.want)
		}
	}
	for _, c := range []struct {
		name     string
		projects []string
		down     bool
		status   int
		code     string
	}{
		{"a project its person cannot see", []string{"ELSE"}, false, http.StatusForbidden, "PROJECT_NOT_VISIBLE"},
		{"a key that is no project key", []string{"pub"}, false, http.StatusBadRequest, "PROJECTS_INVALID"},
		{"a project while Dispatch is down", []string{"LATE"}, true, http.StatusServiceUnavailable, "PROJECTS_UNCHECKED"},
	} {
		dispatch.SetDown(c.down)
		status, raw, _ := enrol("session-refused-"+c.code, c.projects)
		if status != c.status || decode[wireError](t, raw).Code != c.code {
			t.Errorf("POST /v1/enrollments naming %s = %d %s; want %d %s", c.name, status, raw, c.status, c.code)
		}
	}
}
