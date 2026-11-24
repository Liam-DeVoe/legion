package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/credential"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// sideTokens is roleTokens as the API's token source: the App is installed on acme alone.
type sideTokens struct{ roleTokens }

func (sideTokens) InstalledOwners(context.Context) ([]string, error) { return []string{"acme"}, nil }

// sideReviewers is the daemon's claims as the side-review routes read them (api.Supervisor's
// Claims): two running reviewer sessions, ses_s and ses_t. The routes read nothing else of it.
type sideReviewers struct{ api.Supervisor }

func (sideReviewers) Claims(context.Context) ([]supervise.Claim, error) {
	return []supervise.Claim{
		{Token: "capture-capture-capture-301-reviewer", Role: claim.RoleReviewer, Session: "ses_s", State: supervise.StateIdle},
		{Token: "capture-capture-capture-302-reviewer", Role: claim.RoleReviewer, Session: "ses_t", State: supervise.StateWorking},
	}, nil
}

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
	grant, err := grants.MintController("ses_controller", 0)
	if err != nil {
		t.Fatalf("mint the controller's grant: %v", err)
	}
	w := rig.w
	server := api.NewServer("127.0.0.1", 0, api.Options{
		Supervisor: sideReviewers{},
		Pool:       rig.pool, Record: w.records, Grants: grants, Tokens: sideTokens{}, GitHubAPI: w.githubAPI,
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

// A new naming at a head withdraws the reviewer named before: the verdict that reviewer recorded no
// longer stands for the writer. ses_s's approve writes success on side-1; once the controller names
// ses_t at side-1, the run turns to failure, since no verdict of the reviewer named now stands.
func TestLegionVerdictANewSideReviewerWithdrawsTheEarlierVerdict(t *testing.T) {
	entry := config.VerdictRepository{Repository: ghrepo.MustParse("acme/widgets"), SideReviews: true}
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{entry}, nothingSeeded)
	rig.github.mu.Lock()
	rig.github.pulls[95] = `{"number":95,"state":"open","base":{"ref":"main"},"head":{"ref":"fix/parser","sha":"side-1","repo":{"full_name":"acme/widgets"}}}`
	rig.github.mu.Unlock()
	side := newSideAPI(t, rig)
	call := func(route string, body map[string]any) {
		t.Helper()
		body["repository"], body["number"], body["head"] = "acme/widgets", 95, "side-1"
		if status, code := side.post(t, route, body); status != http.StatusOK {
			t.Fatalf("%s %v = %d %s, want 200", route, body, status, code)
		}
		rig.written(t)
	}
	call("/legion/v1/pull-request-reviewer", map[string]any{"session": "ses_s"})
	call("/legion/v1/pull-request-verdict", map[string]any{"verdict": "approve", "session": "ses_s"})
	runs := rig.github.legionRuns("side-1")
	if len(runs) != 1 || runs[0].Conclusion != "success" {
		t.Fatalf("legion-verdict runs on side-1 after ses_s's approve = %+v, want one concluding success", runs)
	}
	call("/legion/v1/pull-request-reviewer", map[string]any{"session": "ses_t"})
	if turned := rig.github.legionRuns("side-1"); len(turned) != 1 || turned[0].ID != runs[0].ID || turned[0].Conclusion != "failure" {
		t.Fatalf("legion-verdict runs on side-1 after ses_t is named = %+v, want run %d alone, turned to failure", turned, runs[0].ID)
	}
}

// A webhook's review of an outside pull request is recorded whichever name of a renamed repository
// it comes under, and whichever name the verdict entry is written under: the entry is looked up as
// the writer looks it up (verdictEntry), under the name given, the one GitHub serves it under, and
// the rename's other name.
func TestLegionVerdictAWebhookReviewOfARenamedRepositoryIsRecorded(t *testing.T) {
	for _, tc := range []struct{ name, entry, webhook string }{
		{"the webhook under the old name, the entry under the new", "acme/widgets", "acme/widgets-old"},
		{"the webhook under the new name, the entry under the old", "acme/widgets-old", "acme/widgets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{{Repository: ghrepo.MustParse(tc.entry), SideReviews: true}}, nothingSeeded)
			renames, err := ghrepo.NewRenames([]ghrepo.Rename{{From: ghrepo.MustParse("acme/widgets-old"), To: ghrepo.MustParse("acme/widgets")}})
			if err != nil {
				t.Fatalf("configure the rename: %v", err)
			}
			served := newRepositories("CAPTURE", renames)
			served.fill(ghrepo.MustParse("acme/widgets"))
			rig.w.repositories = served
			rig.apply(t, intake.OutsidePullRequest{Repo: tc.webhook, Number: 90, Branch: "feature/faster-builds", HeadSHA: "out-1"})
			rig.apply(t, intake.OutsideReview{PullRequestReview: intake.PullRequestReview{Repo: tc.webhook, Number: 90, ID: 1,
				State: "changes_requested", CommitID: "out-1", HeadSHA: "out-1", Author: "a-writer", AuthorCanWrite: true,
				SubmittedAt: time.Date(2026, 11, 21, 6, 0, 1, 0, time.UTC)}})
			rig.written(t)
			var found bool
			var reviews []store.VerdictReview
			if err := store.Transact(context.Background(), rig.pool, func(ctx context.Context, tx pgx.Tx) error {
				_, found, err = store.OutsidePullRequestTx(context.Background(), tx, ghrepo.MustParse(tc.webhook), 90)
				if err != nil {
					return err
				}
				reviews, err = store.VerdictReviewsTx(context.Background(), tx, ghrepo.MustParse(tc.webhook), 90)
				return err
			}); err != nil {
				t.Fatalf("read what was recorded of pull request 90: %v", err)
			}
			if !found || len(reviews) != 1 || !slices.ContainsFunc(reviews, func(r store.VerdictReview) bool { return r.Author == "a-writer" }) {
				t.Fatalf("recorded of %s#90: pull request %t, reviews %+v; want the pull request and a-writer's review", tc.webhook, found, reviews)
			}
		})
	}
}
