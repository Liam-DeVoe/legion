package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/credential"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// sideTokens is roleTokens as the API's token source: the App is installed on acme alone.
type sideTokens struct{ roleTokens }

func (sideTokens) InstalledOwners(context.Context) ([]string, error) { return []string{"acme"}, nil }

// sideAPI is the daemon's API over rig's workflow runtime, its side-review routes wired to the
// writer as serve wires them (api.Options.VerdictEntry and WriteVerdict), and the grant of the
// project's controller session it takes the calls under.
type sideAPI struct {
	handler http.Handler
	grant   string
}

func newSideAPI(t *testing.T, rig *verdictRig) sideAPI {
	t.Helper()
	grants := credential.New(nil)
	grant, err := grants.MintController("ses_controller")
	if err != nil {
		t.Fatalf("mint the controller's grant: %v", err)
	}
	w := rig.w
	server := api.NewServer("127.0.0.1", 0, api.Options{
		Pool: rig.pool, Record: w.records, Grants: grants, Tokens: sideTokens{}, GitHubAPI: w.githubAPI,
		Serving: w.repositories.serving, Log: w.log,
		VerdictEntry: w.verdictEntry,
		WriteVerdict: func(repository string, number int, head string) { w.queueVerdictWrite("", repository, number, head) },
	})
	return sideAPI{handler: server.Handler, grant: grant.ID}
}

// post sends body to route and answers the status and the refusal's code, "" for a success.
func (s sideAPI) post(t *testing.T, route string, body map[string]any) (int, string) {
	t.Helper()
	body["grantId"] = s.grant
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode the body of %s: %v", route, err)
	}
	recorder := httptest.NewRecorder()
	s.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, route, bytes.NewReader(encoded)))
	var refusal struct {
		Code string `json:"code"`
	}
	if recorder.Code != http.StatusOK {
		_ = json.Unmarshal(recorder.Body.Bytes(), &refusal)
	}
	return recorder.Code, refusal.Code
}

// C30 through the real writer: on acme/widgets, whose verdict entry takes side reviews, the
// controller names ses_s the reviewer of pull request 95, which no Legion issue owns, at its head
// side-1. A verdict relayed from ses_t, whom it did not name, is refused and writes no
// legion-verdict; ses_s's approve writes success on side-1 with the daemon's verdict token, and its
// later changes turn that run to failure.
func TestLegionVerdictFromTheControllersRelayOfTheNamedSideReviewer(t *testing.T) {
	entry := config.VerdictRepository{Repository: ghrepo.MustParse("acme/widgets"), SideReviews: true}
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{entry}, nothingSeeded)
	rig.github.mu.Lock()
	rig.github.pulls[95] = `{"number":95,"state":"open","base":{"ref":"main"},"head":{"ref":"fix/parser","sha":"side-1","repo":{"full_name":"acme/widgets"}}}`
	rig.github.mu.Unlock()
	side := newSideAPI(t, rig)
	pullRequest := func(fields map[string]any) map[string]any {
		body := map[string]any{"repository": "acme/widgets", "number": 95, "head": "side-1"}
		for name, value := range fields {
			body[name] = value
		}
		return body
	}

	if status, code := side.post(t, "/legion/v1/pull-request-reviewer", pullRequest(map[string]any{"session": "ses_s"})); status != http.StatusOK {
		t.Fatalf("the controller's naming of ses_s = %d %s, want 200", status, code)
	}
	status, code := side.post(t, "/legion/v1/pull-request-verdict", pullRequest(map[string]any{"verdict": "approve", "session": "ses_t"}))
	if status != http.StatusForbidden || code != "VERDICT_REVIEWER_NOT_NAMED" {
		t.Fatalf("ses_t's verdict = %d %s, want 403 VERDICT_REVIEWER_NOT_NAMED", status, code)
	}
	rig.written(t)
	if runs := rig.github.legionRuns("side-1"); len(runs) != 0 {
		t.Fatalf("legion-verdict runs on side-1 after ses_t's verdict = %+v, want none", runs)
	}

	if status, code := side.post(t, "/legion/v1/pull-request-verdict", pullRequest(map[string]any{"verdict": "approve", "session": "ses_s"})); status != http.StatusOK {
		t.Fatalf("ses_s's approve = %d %s, want 200", status, code)
	}
	rig.written(t)
	runs := rig.github.legionRuns("side-1")
	if len(runs) != 1 || runs[0].Conclusion != "success" || runs[0].App != verdictAppSlug {
		t.Fatalf("legion-verdict runs on side-1 after ses_s's approve = %+v, want one of the App concluding success", runs)
	}
	writes, _ := rig.github.recorded()
	if len(writes) != 1 || writes[0].Bearer != verdictBearer() {
		t.Fatalf("writes after ses_s's approve = %+v, want one, with the daemon's verdict token", writes)
	}

	if status, code := side.post(t, "/legion/v1/pull-request-verdict", pullRequest(map[string]any{"verdict": "changes", "session": "ses_s"})); status != http.StatusOK {
		t.Fatalf("ses_s's changes = %d %s, want 200", status, code)
	}
	rig.written(t)
	if turned := rig.github.legionRuns("side-1"); len(turned) != 1 || turned[0].ID != runs[0].ID || turned[0].Conclusion != "failure" {
		t.Fatalf("legion-verdict runs on side-1 after ses_s's changes = %+v, want run %d alone, turned to failure", turned, runs[0].ID)
	}
}
