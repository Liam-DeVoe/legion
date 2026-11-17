package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
)

// The engine resolves an issue's repository through the mapping the runtime holds when the issue
// is resolved, not through one bound when the engine was built: an option of the rig replaces the
// runtime's repositories before the boot loads them, and a served repository still resolves
// through the engine. An empty resolution, the project mapping repositories but naming no default,
// resolves to none, as it did; only a project that maps none refuses for that.
func TestTheEngineResolvesThroughTheRuntimesCurrentMapping(t *testing.T) {
	w, _, _, _ := mappingRigWith(t, &installations{}, func(w *workflowRuntime) {
		w.repositories = newRepositories("LEGION", ghrepo.Renames{})
		w.recheckAfter = func(time.Duration) <-chan time.Time { return nil }
	})
	if got, err := w.engine.ResolveRepository("acme/widgets"); err != nil || got.Refusal != "" || got.Repo != "acme/widgets" || got.DefaultBranch != "main" {
		t.Fatalf("the engine resolves acme/widgets to %#v, %v; want it served at main, as the runtime's mapping serves it", got, err)
	}
	if got, err := w.engine.ResolveRepository(""); err != nil || got.Unmapped || got.Refusal != "its repository resolves to none" {
		t.Fatalf("the engine resolves an empty resolution to %#v, %v; want it to resolve to none: the project maps repositories", got, err)
	}
}

// The daemon's own boot (run, through openWorkflow's wiring), with nothing mapped to its project,
// tells an issue Dispatch lists as handed to Legion and todo why it was not admitted: the project
// has no repository mapped, and mapping one is the remedy. The mapping rig builds its own engine,
// so only a boot like this one proves the engine the daemon builds asks the daemon's resolver.
func TestTheBootTellsAListedIssueItsProjectMapsNoRepository(t *testing.T) {
	cfg := workflowConfig(t, workflowNATS(t))
	key := cfg.Project + "-401"
	listed := map[string]any{"key": key, "project": cfg.Project, "title": "handed to Legion", "status": "todo", "parent": nil,
		"rank": "U", "labels": liveDispatchLabels(true), "last_seq": 1, "resolved_repo": ""}
	var mu sync.Mutex
	var notes []string
	posted := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), notes...)
	}
	dispatchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/settings/repo-projects":
			_, _ = io.WriteString(w, `[]`)
		case servedBootCall(t, w, r, cfg.Project):
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/issues":
			_ = json.NewEncoder(w).Encode([]any{listed})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/issues/"+key:
			_ = json.NewEncoder(w).Encode(listed)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/issues/"+key+"/events":
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/issues/"+key+"/messages":
			var message struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
				t.Errorf("decode the message posted on %s: %v", key, err)
			}
			mu.Lock()
			notes = append(notes, message.Body)
			mu.Unlock()
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("Dispatch request = %s %s", r.Method, r.URL.Path)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(dispatchServer.Close)
	cfg.DispatchURL = dispatchServer.URL
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)), overrides{
			listen: heldListen, runtime: fakeRuntime(fake.NewRuntime(), &built{}).runtime, clock: stillClock{},
			workflowTokens: &workflowTokenRecorder{}, githubAPI: repositoriesGitHub(t),
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("run: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	deadline := time.Now().Add(20 * time.Second)
	for len(posted()) == 0 {
		select {
		case err := <-done:
			t.Fatalf("the daemon stopped before it said anything on %s: %v", key, err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon posted nothing on %s within 20 s", key)
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := posted()
	if !strings.Contains(got[0], api.NoRepositoryMapped(cfg.Project)) || !strings.Contains(got[0], "Map the project's repositories") ||
		strings.Contains(got[0], "Set its repository") {
		t.Fatalf("messages on %s = %q, want the first naming the empty mapping and mapping a repository as the remedy", key, got)
	}
}
