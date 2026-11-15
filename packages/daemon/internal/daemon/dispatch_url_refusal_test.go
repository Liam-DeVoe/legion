package daemon

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
)

// A daemon that boots with no dispatch_url opens no workflow (openWorkflow's cfg.DispatchURL == ""
// arm), so it builds no Dispatch client, App manager or NATS intake. A change of dispatch_url there
// would name the dispatch-client holder the daemon never built and reach nothing but a log line, so
// the running daemon refuses the key by name — the per-daemon twin of a fixed key (config.Live's
// RefusePath, wired at boot in run) — and goes on running no workflow. Before the fix the route
// answered the change `rebuilt` for a client that does not exist.
func TestADispatchURLChangeIsRefusedOnADaemonThatBootedWithNoWorkflow(t *testing.T) {
	live := liveSettings(t)
	logs := &syncBuffer{}
	d := startDaemonOn(t, live, fakeRuntime(fake.NewRuntime(), &built{}), slog.New(slog.NewJSONHandler(logs, nil)), 10*time.Second)
	if got := len(d.state().Consumers); got != 0 {
		t.Fatalf("a daemon booted with no dispatch_url shows %d consumers, want none: it runs no workflow", got)
	}

	status, answer := settingsRequest(t, d, testOperatorToken, api.SettingsChangeRequest{
		By:      "ana@example.com",
		Changes: map[string]json.RawMessage{"dispatch_url": json.RawMessage(`"https://dispatch.example"`)},
	})

	if status != http.StatusOK {
		t.Fatalf("the settings route = %d", status)
	}
	if got := answer["dispatch_url"]; got.Applied != "" || got.Refused != whyNoWorkflowDispatchURL {
		t.Fatalf("the answer for dispatch_url = %+v, want it refused with the no-workflow reason and applied to nothing", got)
	}
	if got := live.Snapshot().DispatchURL; got != "" {
		t.Errorf("dispatch_url = %q after the refused change, want it still unset: the key reaches nothing until a restart", got)
	}
	if strings.Contains(logs.String(), `"holder":"dispatch-client"`) {
		t.Errorf("the refused dispatch_url change rebuilt the dispatch client on a daemon that has none:\n%s", logs)
	}
	if got := len(d.state().Consumers); got != 0 {
		t.Fatalf("the daemon shows %d consumers after the refused change, want none: it still runs no workflow", got)
	}
}

// A daemon that boots with a dispatch_url opens a workflow and builds the Dispatch client every
// workflow call goes through, so dispatch_url stays rebuild-class: the change is applied, the client
// is rebuilt on the new URL, and the next workflow call goes through the client the rebuild swapped
// in — proved by an operator's own status write reaching the Dispatch server the change named rather
// than the one the daemon booted on.
func TestADispatchURLChangeRebuildsTheClientOnADaemonWithAWorkflow(t *testing.T) {
	natsURL := workflowNATS(t)
	cfg := testConfig(t)
	// Boot reads this process's environment and refuses a gh that is a script (RefuseScriptGH);
	// name a real binary, as workflowConfig does.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEGION_GH_PATH", executable)
	booted, writesBooted := dispatchRecorder(t, cfg.Project)
	renamed, writesRenamed := dispatchRecorder(t, cfg.Project)
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "dispatch-token")
	if err := os.WriteFile(tokenFile, []byte("dispatch-test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "legion.yaml")
	body := fmt.Sprintf(`project: %[1]s
port: %[2]d
bind: 127.0.0.1
postgres_dsn: %[3]s
state_dir: %[4]s
runtime: tmux
daemon_url: http://127.0.0.1:%[2]d
operator_token_file: %[5]s
controller: operator
dispatch_url: %[6]s
dispatch_token_file: %[7]s
nats_urls: [%[8]s]
projects:
  %[1]s: {}
github_apps:
  implement: { app_id: "1", private_key: implement-test-key }
  review: { app_id: "2", private_key: review-test-key }
`, cfg.Project, cfg.Port, cfg.PostgresDSN, cfg.StateDir, cfg.OperatorTokenFile, booted.URL, tokenFile, natsURL)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	live, err := config.NewLive(path, func(string) string { return "" }, nil)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	o := fakeRuntime(fake.NewRuntime(), &built{})
	o.workflowTokens = &workflowTokenRecorder{}
	logs := &syncBuffer{}
	d := startDaemonOn(t, live, o, slog.New(slog.NewJSONHandler(logs, nil)), 30*time.Second)

	status, answer := settingsRequest(t, d, testOperatorToken, api.SettingsChangeRequest{
		By:      "ana@example.com",
		Changes: map[string]json.RawMessage{"dispatch_url": json.RawMessage(strconv.Quote(renamed.URL))},
	})
	if status != http.StatusOK || answer["dispatch_url"].Applied != string(config.OutcomeRebuilt) || answer["dispatch_url"].Refused != "" {
		t.Fatalf("the settings route answered dispatch_url with %d %+v, want it rebuilt and refused by nobody", status, answer["dispatch_url"])
	}
	awaitLog(t, logs, `"holder":"dispatch-client"`, 5*time.Second, "the dispatch client was not rebuilt by the dispatch_url change")

	// The next workflow call uses the rebuilt client: an operator's status write, which the daemon
	// makes synchronously through the Dispatch client (api's setDispatchStatus), reaches the server
	// the change named.
	var grant api.GrantResponse
	if gs, grantBody := d.request(http.MethodPost, "/legion/v1/grants", api.GrantRequest{}, true); gs != http.StatusOK {
		t.Fatalf("mint the operator grant = %d %s", gs, grantBody)
	} else if err := json.Unmarshal(grantBody, &grant); err != nil {
		t.Fatalf("decode the operator grant %s: %v", grantBody, err)
	}
	issue := cfg.Project + "-1"
	d.request(http.MethodPost, "/legion/v1/issues/status", api.IssueStatusRequest{GrantID: grant.GrantID, Issue: issue, Status: "todo"}, false)

	if !slices.Contains(writesRenamed.list(), issue) {
		t.Fatalf("the status write of %s reached the Dispatch server the change named %v, want it there: the next workflow call must use the rebuilt client", issue, writesRenamed.list())
	}
	if slices.Contains(writesBooted.list(), issue) {
		t.Errorf("the status write of %s also reached the Dispatch server the daemon booted on %v, want none after the client was rebuilt", issue, writesBooted.list())
	}
}

// dispatchStatusWrites records the issue keys a Dispatch recorder was asked to PATCH, so a test can tell
// which server a status write reached.
type dispatchStatusWrites struct {
	mu   sync.Mutex
	keys []string
}

func (s *dispatchStatusWrites) record(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
}

func (s *dispatchStatusWrites) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.keys...)
}

// dispatchRecorder is a Dispatch server that answers a daemon's boot calls (servedBootCall) and an
// empty issue listing, records every issue PATCH (a status write) and answers it as Dispatch's PATCH
// does, and answers anything else benignly so a background workflow read after a rename never fails
// the test.
func dispatchRecorder(t *testing.T, project string) (*httptest.Server, *dispatchStatusWrites) {
	t.Helper()
	writes := &dispatchStatusWrites{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/issues/") {
			key := strings.TrimPrefix(r.URL.Path, "/api/v1/issues/")
			writes.record(key)
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"key": key, "status": "todo", "rank": "a", "repo": "acme/widgets", "resolved_repo": "acme/widgets", "sequence": 2,
			}); err != nil {
				t.Errorf("write the status answer for %s: %v", key, err)
			}
			return
		}
		if servedBootCall(t, w, r, project) {
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/issues" {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode([]any{}); err != nil {
				t.Errorf("write the empty issue listing: %v", err)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{}); err != nil {
			t.Errorf("write the benign answer for %s %s: %v", r.Method, r.URL.Path, err)
		}
	}))
	t.Cleanup(server.Close)
	return server, writes
}
