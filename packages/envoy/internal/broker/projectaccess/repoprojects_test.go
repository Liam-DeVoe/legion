package projectaccess_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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

// mappingOf serves Dispatch's repository mapping of rows rows, each a repository of its own
// mapped to PUB, until t ends, and answers its base URL.
func mappingOf(t *testing.T, rows int) string {
	t.Helper()
	mapping := make([]model.RepoProject, rows)
	for i := range mapping {
		mapping[i] = model.RepoProject{Repo: fmt.Sprintf("acme/repository-%07d", i), Project: "PUB", CreatedAt: time.Now()}
	}
	body, err := json.Marshal(mapping)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	t.Logf("the mapping of %d rows is %d bytes", rows, len(body))
	return server.URL
}

// TestRepoProjectsReadsAMappingPastOneAnswersBound: the mapping is every row Dispatch holds, so
// it is read to a bound of its own, 8 MiB, and not to the 64 KiB one project's answer is read to.
// A mapping of 2000 rows (about 320 KiB) is read whole; one of 90,000 rows (about 14 MiB) is
// refused naming the bound. The control reads the mapping through get, whose 64 KiB refuses the
// 2000 rows.
func TestRepoProjectsReadsAMappingPastOneAnswersBound(t *testing.T) {
	rows, err := projectaccess.New(mappingOf(t, 2000), projectaccesstest.StaticAccount("token"), nil).RepoProjects(t.Context())
	if err != nil || len(rows) != 2000 || rows[1999].Repo != "acme/repository-0001999" || rows[1999].Project != "PUB" {
		t.Fatalf("RepoProjects of 2000 rows = %d rows, %v; want all 2000", len(rows), err)
	}

	_, err = projectaccess.New(mappingOf(t, 90_000), projectaccesstest.StaticAccount("token"), nil).RepoProjects(t.Context())
	if err == nil || !strings.Contains(err.Error(), "over 8388608 bytes") {
		t.Fatalf("RepoProjects past 8 MiB = %v, want a refusal naming the 8388608-byte bound", err)
	}
}
