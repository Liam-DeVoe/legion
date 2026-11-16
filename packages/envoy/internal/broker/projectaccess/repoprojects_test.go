package projectaccess_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/projectaccess"
	"github.com/sjawhar/envoy/internal/broker/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestRepoProjectsReadsDispatchsWholeMapping: RepoProjects reads Dispatch's whole repository
// mapping (GET /api/v1/settings/repo-projects) in the shape Dispatch's handler writes it, a list
// of model.RepoProject, presenting the broker's own account token: each row's repository, its
// project and the repository it was renamed to, nil for one never renamed. A 401 or a 403 is
// ErrBrokerRefused, as on the access routes.
func TestRepoProjectsReadsDispatchsWholeMapping(t *testing.T) {
	renamed := "acme/b"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/settings/repo-projects" || r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]model.RepoProject{
			{Repo: "acme/a", Project: "PUB1", RenamedTo: &renamed, CreatedAt: time.Now()},
			{Repo: "acme/b", Project: "PUB2", Default: true, CreatedAt: time.Now()},
		})
	}))
	t.Cleanup(server.Close)

	rows, err := projectaccess.New(server.URL, projectaccesstest.StaticAccount("token"), nil).RepoProjects(t.Context())
	want := []projectaccess.RepoProject{{Repo: "acme/a", Project: "PUB1", RenamedTo: &renamed}, {Repo: "acme/b", Project: "PUB2"}}
	if err != nil || !reflect.DeepEqual(rows, want) {
		t.Fatalf("RepoProjects = %+v, %v; want %+v", rows, err, want)
	}

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for _, body := range projectaccesstest.RefusalBodies {
			client := projectaccess.New(projectaccesstest.NewRefusing(t, status, body), projectaccesstest.StaticAccount("token"), nil)
			if _, err := client.RepoProjects(t.Context()); !errors.Is(err, projectaccess.ErrBrokerRefused) {
				t.Errorf("RepoProjects answered %d with a body %s = %v, want ErrBrokerRefused", status, body, err)
			}
		}
	}
}
