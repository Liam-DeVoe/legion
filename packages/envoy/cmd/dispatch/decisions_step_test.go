package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/metrics"
)

// /healthz answers what the decisions step's last pass found owed, `decisions_step_owed`, and
// nothing before a pass has run: a step that has not run - as DISPATCH_TEST_HOLD_DECISIONS_STEP
// keeps a server's from running at boot and on its tick - answers no member at all. Once a pass
// has run over a database with nothing owed, the member reads each part and the total zero, and
// once a project stores a list of gated steps that holds `decisions`, the next pass reads that
// list, and the total, as one.
func TestHealthzAnswersWhatTheDecisionsStepOwes(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	step := docs.NewDecisionStep(database, "", nil)
	handler := dispatchHandler(http.NewServeMux(), metrics.New(), database, nil, nil, nil, "", nil, step, issuers{})
	probe := func() map[string]any {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		var health map[string]any
		if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
			t.Fatalf("decode /healthz: %v", err)
		}
		return health
	}
	owed := func(stage string) map[string]any {
		t.Helper()
		health := probe()
		member, ok := health["decisions_step_owed"].(map[string]any)
		if !ok {
			t.Fatalf("%s: /healthz answers decisions_step_owed %#v, want the counts of the step's last pass", stage, health["decisions_step_owed"])
		}
		t.Logf("%s: decisions_step_owed %v", stage, member)
		return member
	}

	if member, present := probe()["decisions_step_owed"]; present {
		t.Fatalf("/healthz answers decisions_step_owed %v before the step has run a pass, want the member absent", member)
	}

	if err := step.Boot(ctx); err != nil {
		t.Fatalf("run the step's boot pass: %v", err)
	}
	empty := owed("after a pass over a database with nothing owed")
	for _, part := range []string{"decisions_documents", "answers_left_spec", "gated_steps_lists", "total"} {
		if empty[part] != float64(0) {
			t.Errorf("after a pass over a database with nothing owed, decisions_step_owed.%s = %v, want 0", part, empty[part])
		}
	}

	if _, err := database.Pool.Exec(ctx, `
		insert into projects (key, name, default_assignee, gated_steps) values ('GATE', 'Gated', 'alice', '{decisions,spec}')
	`); err != nil {
		t.Fatalf("store a list of gated steps holding decisions: %v", err)
	}
	if _, err := step.Tick(ctx); err != nil {
		t.Fatalf("run a tick of the step: %v", err)
	}
	gated := owed("after a pass over one list of gated steps holding decisions")
	if gated["gated_steps_lists"] != float64(1) || gated["total"] != float64(1) {
		t.Errorf("decisions_step_owed = %v, want gated_steps_lists 1 and total 1", gated)
	}
}
