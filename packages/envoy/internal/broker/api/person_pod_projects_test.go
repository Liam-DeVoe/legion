// person_pod_projects_test.go drives POST /v1/enrollments for a person's pod that names
// projects, on the real handlers api_test.go mounts. BROKER_POD_OPERATORS maps the pod's account
// to bob, a restricted member, who sees only the projects whose access list names him. The pod
// logs in with its own token and names its projects as bob's own session does, and the broker
// checks them against bob.
package api_test

import (
	"crypto/ecdsa"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/api"
	"github.com/sjawhar/envoy/internal/broker/projectaccess"
	"github.com/sjawhar/envoy/internal/broker/projectaccess/projectaccesstest"
)

// bob's account, the login service BROKER_SERVICE_LOGINS gives it, and bob, whom
// BROKER_POD_OPERATORS maps it to.
const (
	projectsPodAccount = "system:serviceaccount:people:bob"
	projectsPodService = "person-bob"
	projectsPodPerson  = "bob@example.com"
)

// newPersonPodProjectsServer is newTestServer with bob's account mapped to bob and a client of a
// Dispatch stand-in in which bob is a restricted member. PRIV and LATE are private and their
// access lists name bob; a test asks about LATE only while Dispatch is down, so no answer the
// broker's client still holds (projectaccess.CacheTTL) stands in for Dispatch. ELSE is private
// and does not name him. It answers the stand-in, so that a test can take it down.
func newPersonPodProjectsServer(t *testing.T) (*testServer, *projectaccesstest.Server) {
	t.Helper()
	dispatch := projectaccesstest.NewServer(t, map[string]projectaccesstest.Row{
		"PRIV": {Visibility: "private", Owner: testApprover, Listed: []string{projectsPodPerson}},
		"LATE": {Visibility: "private", Owner: testApprover, Listed: []string{projectsPodPerson}},
		"ELSE": {Visibility: "private", Owner: testApprover},
	}, map[string]string{projectsPodPerson: projectaccess.MemberRestricted})
	ts := newTestServerWith(t, func(d *api.Deps) {
		d.Enroll.Projects = projectaccess.New(dispatch.URL, dispatch.Account, nil)
		d.Enroll.ServiceLogins[projectsPodAccount] = projectsPodService
		d.Enroll.PodOperators = map[string]string{projectsPodAccount: projectsPodPerson}
	})
	return ts, dispatch
}

// bobsPod is one pod of bob's account: its pod UID, the login its own token made and that
// login's key, and the key the pod enrols.
type bobsPod struct {
	uid           string
	login         string
	loginKey, key *ecdsa.PrivateKey
}

// logInBobsPod logs pod uid of bob's account in over POST /v1/launcher-credentials/token, with a
// token bound to that pod, and fails t unless the broker answers 200.
func logInBobsPod(t *testing.T, ts *testServer, uid string) bobsPod {
	t.Helper()
	loginKey := newSigningKey(t)
	status, body := ts.tokenLogin(t, ts.accountToken(t, projectsPodAccount, uid, 20*time.Minute),
		signMachineLoginRequest(t, loginKey, ts.URL, "", "people-bob", projectsPodService))
	if status != http.StatusOK {
		t.Fatalf("token login of bob's pod %s = %d %s; want 200", uid, status, body)
	}
	return bobsPod{uid: uid, login: decode[wireTokenLogin](t, body).CredentialID, loginKey: loginKey, key: newSigningKey(t)}
}

// enrol asks POST /v1/enrollments to enrol the pod under its own login, naming projects (no
// projects field at all for nil).
func (p bobsPod) enrol(t *testing.T, ts *testServer, projects []string) (int, []byte) {
	t.Helper()
	body := map[string]any{
		"kind": "pod", "runtime_id": p.uid, "thumbprint": thumbprintOf(t, p.key),
		"pod_token": ts.podToken(t, projectsPodAccount, p.uid),
	}
	if projects != nil {
		body["projects"] = projects
	}
	return ts.launcher(t, p.loginKey, p.login, http.MethodPost, "/v1/enrollments", body)
}

// enrollments counts the enrollments naming the pod, live or not.
func (p bobsPod) enrollments(t *testing.T, ts *testServer) int {
	t.Helper()
	var n int
	if err := ts.Store.Pool.QueryRow(t.Context(), `select count(*) from enrollments where runtime_id=$1`, p.uid).Scan(&n); err != nil {
		t.Fatalf("count the enrollments of pod %s: %v", p.uid, err)
	}
	return n
}

// TestARestrictedMembersPodEnrollsNamingItsProjects: bob's pod naming PRIV, whose access list
// names him, is enrolled: 201, and the same call again answers 200 with the same enrollment, as
// the route answers a key already enrolled. GET /v1/enrollments/self answers bob as its operator
// and PRIV as its projects. Then each refusal by its own code: a pod naming no project 403
// PROJECTS_REQUIRED, saying to set AGENT_SECRETS_PROJECTS and never PROJECTS_NEED_A_PERSON; a pod
// naming ELSE 403 PROJECT_NOT_VISIBLE; and a pod naming LATE, which bob may see, while Dispatch
// is down 503 PROJECTS_UNCHECKED. No refusal enrols anything.
func TestARestrictedMembersPodEnrollsNamingItsProjects(t *testing.T) {
	ts, dispatch := newPersonPodProjectsServer(t)

	pod := logInBobsPod(t, ts, "pod-uid-bob-priv")
	status, body := pod.enrol(t, ts, []string{"PRIV"})
	if status != http.StatusCreated {
		t.Fatalf("POST /v1/enrollments for bob's pod naming PRIV = %d %s; want 201", status, body)
	}
	enrollmentID := decode[wireEnrolled](t, body).EnrollmentID
	status, body = pod.enrol(t, ts, []string{"PRIV"})
	if status != http.StatusOK || decode[wireEnrolled](t, body).EnrollmentID != enrollmentID {
		t.Fatalf("POST /v1/enrollments again for bob's pod naming PRIV = %d %s; want 200 with enrollment %s", status, body, enrollmentID)
	}
	status, body = ts.session(t, pod.key, enrollmentID, http.MethodGet, "/v1/enrollments/self", nil)
	self := decode[struct {
		Kind     string   `json:"kind"`
		Operator *string  `json:"operator"`
		Projects []string `json:"projects"`
	}](t, body)
	if status != http.StatusOK || self.Kind != "pod" || self.Operator == nil || *self.Operator != projectsPodPerson || !slices.Equal(self.Projects, []string{"PRIV"}) {
		t.Fatalf("GET /v1/enrollments/self of bob's pod = %d %s; want a pod of %s naming [PRIV]", status, body, projectsPodPerson)
	}

	for _, c := range []struct {
		name     string
		uid      string
		projects []string
		down     bool
		status   int
		code     string
		says     string
	}{
		{"no project", "pod-uid-bob-none", nil, false, http.StatusForbidden, "PROJECTS_REQUIRED", "AGENT_SECRETS_PROJECTS"},
		{"ELSE, whose list does not name bob", "pod-uid-bob-else", []string{"ELSE"}, false, http.StatusForbidden, "PROJECT_NOT_VISIBLE", "ELSE"},
		{"LATE while Dispatch is down", "pod-uid-bob-down", []string{"LATE"}, true, http.StatusServiceUnavailable, "PROJECTS_UNCHECKED", ""},
	} {
		dispatch.SetDown(c.down)
		refused := logInBobsPod(t, ts, c.uid)
		status, body := refused.enrol(t, ts, c.projects)
		got := decode[wireError](t, body)
		if status != c.status || got.Code != c.code || !strings.Contains(got.Error, c.says) {
			t.Errorf("POST /v1/enrollments for bob's pod naming %s = %d %s; want %d %s saying %q", c.name, status, body, c.status, c.code, c.says)
		}
		if n := refused.enrollments(t, ts); n != 0 {
			t.Errorf("enrollments of bob's pod %s after the refusal = %d, want none", c.uid, n)
		}
	}
}
