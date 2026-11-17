package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/metrics"
)

// TestHealthzNamesNoPrivateProjectToACallerWithNoCredential (R2, R6; c-private-unseen):
// /healthz answers a caller with no credential (R6 keeps it open), so it names no private
// project's key, a project the deployment did not provision included: a person from outside, or
// any agent, reads the deployment's health and learns nothing of a project it cannot see. The
// operator's own reading of which project is unprovisioned is the ERROR line the boot and each
// maintenance pass log.
func TestHealthzNamesNoPrivateProjectToACallerWithNoCredential(t *testing.T) {
	database := storetest.Open(t)
	if _, err := database.Pool.Exec(context.Background(),
		`insert into projects (key, name, default_assignee, visibility) values ('SECRET', 'Secret', 'alice@example.com', 'private')`); err != nil {
		t.Fatalf("seed the private project SECRET: %v", err)
	}
	state, err := startPrivateProjectsState(t.Context(), database.Pool, nil)
	if err != nil {
		t.Fatalf("the first read of the private projects: %v", err)
	}
	response := httptest.NewRecorder()
	dispatchHandler(http.NewServeMux(), metrics.New(), database, nil, nil, nil, "", nil, issuers{}, state).
		ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if body := response.Body.String(); strings.Contains(body, "SECRET") {
		t.Fatalf("/healthz with no credential = %d %s: it names the private project SECRET", response.Code, body)
	}
}
