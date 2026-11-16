// spec_results_test.go holds POST and GET /api/v1/projects/{key}/results to R12 of the spec:
// a CI job writes the result of a check of the spec files with its own token, Dispatch takes the
// commit, the job and the procedure from the token, from GitHub and from the files at that
// commit, never from the body, and appends each result as a new record.
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/dispatch/auth"
	"github.com/sjawhar/envoy/internal/dispatch/githubapp/githubapptest"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
	"github.com/sjawhar/envoy/internal/githubtest"
)

const (
	// specResultsProject is the project the CI job's results token writes the results of.
	specResultsProject = "ACME"
	// specResultsRepository is the repository of the job's run, which the project maps.
	specResultsRepository = "example-org/example-repo"
	// specResultsCommit is the commit of the job's run, whose files hold the probe checks.
	specResultsCommit = "0123456789abcdef0123456789abcdef01234567"
	// specResultsOtherCommit is a commit the token does not name.
	specResultsOtherCommit = "89abcdef0123456789abcdef0123456789abcdef"
	// specResultsCheckRun is the check run of the job the default token was minted in.
	specResultsCheckRun = 97
)

// specResultsFiles is .dispatch/spec at specResultsCommit: one statement and three program
// checks, p-1 of job go-checks, p-2 of job envoy-go and m-1 of the matrix job m.
var specResultsFiles = map[string]string{
	"probe.md": `:::requirement{#the-probe-holds kind="behavior"}
The probe test passes on each commit.
:::

:::check{#p-1 serves="the-probe-holds" subject="code" procedure="program" runner="go" selector="example.com/m/p TestP" job="go-checks"}
Claim: The probe test passes.
Procedure: The program runs the probe test one time.
Failing example: A probe test that fails.
:::

:::check{#p-2 serves="the-probe-holds" subject="code" procedure="program" runner="go" selector="example.com/m/p TestQ" job="envoy-go"}
Claim: The second probe test passes.
Procedure: The program runs the second probe test one time.
Failing example: A second probe test that fails.
:::

:::check{#m-1 serves="the-probe-holds" subject="code" procedure="program" runner="go" selector="example.com/m/p TestM" job="m"}
Claim: The matrix probe test passes on each leg.
Procedure: The program runs the matrix probe test on each leg.
Failing example: A matrix probe test that fails on one leg.
:::
`,
}

// specCheckDigest is the procedure digest the files give check id, read with the graph logic's
// own parser, as the route must.
func specCheckDigest(t *testing.T, id string) string {
	t.Helper()
	files := map[string][]byte{}
	for name, content := range specResultsFiles {
		files[name] = []byte(content)
	}
	tree, _ := specgraph.Parse(files)
	check, ok := tree.CheckByID(id)
	if !ok {
		t.Fatalf("the fixture files hold no check %s", id)
	}
	return check.Digest()
}

// specCheckRunAnswer is one check run as the stand-in GitHub serves it.
type specCheckRunAnswer struct {
	name, headSHA string
}

// specGitHub is the GitHub the route reads: the installation of the repository, its check runs by
// id, and the tree and blobs of .dispatch/spec at a commit. A status set in checkRunStatus or
// filesStatus answers every read of that kind with it. It counts each read, so a test can see a
// cached read made once.
type specGitHub struct {
	mu             sync.Mutex
	checkRuns      map[int64]specCheckRunAnswer
	files          map[string]map[string]string
	checkRunStatus int
	filesStatus    int
	checkRunReads  int
	treeReads      int
}

func (g *specGitHub) serve(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{repo}/installation", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":77,"app_slug":"dispatch-test","permissions":{"contents":"read","checks":"read"}}`)
	})
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"token":"ghs_fake_77","expires_at":"2099-01-01T00:00:00Z"}`)
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/check-runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.checkRunReads++
		if g.checkRunStatus != 0 {
			w.WriteHeader(g.checkRunStatus)
			fmt.Fprint(w, `{"message":"Server Error"}`)
			return
		}
		var id int64
		fmt.Sscan(r.PathValue("id"), &id)
		run, ok := g.checkRuns[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		fmt.Fprintf(w, `{"id":%d,"name":%q,"head_sha":%q,"status":"in_progress"}`, id, run.name, run.headSHA)
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/git/trees/{ref}", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.treeReads++
		if g.filesStatus != 0 {
			w.WriteHeader(g.filesStatus)
			fmt.Fprint(w, `{"message":"Server Error"}`)
			return
		}
		commit, dir, _ := strings.Cut(r.PathValue("ref"), ":")
		files, ok := g.files[commit]
		if dir != ".dispatch/spec" || !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		var entries []map[string]any
		for name, content := range files {
			entries = append(entries, map[string]any{"path": name, "mode": "100644", "type": "blob", "sha": "blob-" + commit + "-" + name, "size": len(content)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": "tree-" + commit, "truncated": false, "tree": entries})
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/git/blobs/{sha}", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		commit, name, _ := strings.Cut(strings.TrimPrefix(r.PathValue("sha"), "blob-"), "-")
		content, ok := g.files[commit][name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"content": base64.StdEncoding.EncodeToString([]byte(content)), "encoding": "base64"})
	})
	return githubtest.Server(t, githubtest.Spec{Handle: mux.ServeHTTP})
}

// specResultsRig is a Dispatch whose verifier serves results tokens as cmd/dispatch's does, a
// GitHub that holds the run's check run and files, and project ACME mapped to the repository.
type specResultsRig struct {
	*brokerTokenRig
	github *specGitHub
	caller *brokerCaller
	tokens int
}

func newSpecResultsRig(t *testing.T) *specResultsRig {
	t.Helper()
	github := &specGitHub{
		checkRuns: map[int64]specCheckRunAnswer{specResultsCheckRun: {name: "go-checks", headSHA: specResultsCommit}},
		files:     map[string]map[string]string{specResultsCommit: specResultsFiles},
	}
	_, pemText := githubapptest.Key(t)
	rig := newBrokerTokenRigOn(t, testServerOptions{
		app:           &auth.AppConfig{ClientID: "Iv1.test", PEM: pemText},
		githubAPIBase: github.serve(t),
	})
	for _, project := range []string{specResultsProject, "OTHER"} {
		if created := rig.asPerson(t, http.MethodPost, "/api/v1/projects", map[string]any{"key": project, "name": project}); created.Code != http.StatusCreated {
			t.Fatalf("create project %s: status=%d body=%s", project, created.Code, created.Body.String())
		}
	}
	if mapped := rig.asPerson(t, http.MethodPut, "/api/v1/settings/repo-projects/"+specResultsRepository, map[string]any{"project": specResultsProject}); mapped.Code != http.StatusOK {
		t.Fatalf("map %s to %s: status=%d body=%s", specResultsRepository, specResultsProject, mapped.Code, mapped.Body.String())
	}
	return &specResultsRig{brokerTokenRig: rig, github: github, caller: newBrokerCaller(t)}
}

// token has the broker answer a results token of project ACME for the default run, with change
// applied to the answer, and answers the token.
func (r *specResultsRig) token(change func(*brokertoken.Introspection)) string {
	r.tokens++
	token := fmt.Sprintf("a-results-token-%d", r.tokens)
	answer := resultsOnlyToken(r.caller.jkt, specResultsProject)
	if change != nil {
		change(&answer)
	}
	r.broker.Says(token, answer)
	return token
}

// post writes body to project's results route under token.
func (r *specResultsRig) post(t *testing.T, project, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return r.serve(r.caller.presenting(t, http.MethodPost, "/api/v1/projects/"+project+"/results", token, body))
}

// passOf is the body a job writes for check: a pass at the default commit.
func passOf(check string) map[string]any {
	return map[string]any{"commit": specResultsCommit, "check": check, "verdict": "pass", "observed": "ok  \texample.com/m/p\t0.01s"}
}

// read is every stored result of project ACME at commit, of check when it is not empty, as the
// route answers a person.
func (r *specResultsRig) read(t *testing.T, commit, check string) []model.SpecCheckResult {
	t.Helper()
	path := "/api/v1/projects/" + specResultsProject + "/results?commit=" + commit
	if check != "" {
		path += "&check=" + check
	}
	response := r.asPerson(t, http.MethodGet, path, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s: status=%d body=%s", path, response.Code, response.Body.String())
	}
	return decodeBody[struct {
		Results []model.SpecCheckResult `json:"results"`
	}](t, response).Results
}

// stored is how many rows spec_check_results holds.
func (r *specResultsRig) stored(t *testing.T) int {
	t.Helper()
	var count int
	if err := r.database.Pool.QueryRow(context.Background(), `select count(*) from spec_check_results`).Scan(&count); err != nil {
		t.Fatalf("count spec_check_results: %v", err)
	}
	return count
}

// refusedWith fails the test unless response is status with code and nothing was stored.
func (r *specResultsRig) refusedWith(t *testing.T, response *httptest.ResponseRecorder, status int, code string) refusalAnswer {
	t.Helper()
	refusal := decodeBody[refusalAnswer](t, response)
	if response.Code != status || refusal.Code != code {
		t.Fatalf("status=%d body=%s, want %d %s", response.Code, response.Body.String(), status, code)
	}
	if held := r.stored(t); held != 0 {
		t.Fatalf("the refused write left %d results, want none", held)
	}
	return refusal
}

// stored201 decodes a 201 answer, failing the test on any other.
func stored201(t *testing.T, response *httptest.ResponseRecorder) model.SpecCheckResult {
	t.Helper()
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s, want 201", response.Code, response.Body.String())
	}
	return decodeBody[model.SpecCheckResult](t, response)
}

// TestAJobsResultIsStoredAsAnAttestationOfItsRun: the job's result is stored as an in-toto
// statement whose subject is the token's commit, whose job comes from the check run GitHub names
// for the token, whose procedure digest comes from the files at that commit, and whose recorder
// is the session the token proved. The body names none of them. The read answers the same row.
func TestAJobsResultIsStoredAsAnAttestationOfItsRun(t *testing.T) {
	rig := newSpecResultsRig(t)
	result := stored201(t, rig.post(t, specResultsProject, rig.token(nil), passOf("p-1")))

	statement := result.Attestation
	if statement.Type != "https://in-toto.io/Statement/v1" ||
		statement.PredicateType != "https://github.com/cybermonklabs/legion/attestations/spec-check-result/v1" {
		t.Fatalf("the statement is %q of %q, want an in-toto Statement v1 of a spec check result", statement.Type, statement.PredicateType)
	}
	if len(statement.Subject) != 1 || statement.Subject[0].Name != "git+https://github.com/"+specResultsRepository ||
		statement.Subject[0].Digest["gitCommit"] != specResultsCommit {
		t.Fatalf("the subject is %+v, want the repository at the token's commit", statement.Subject)
	}
	predicate := statement.Predicate
	want := model.SpecCheckPredicate{
		Project: specResultsProject, Check: "p-1", ProcedureDigest: specCheckDigest(t, "p-1"),
		Test:    model.SpecCheckTest{Runner: "go", Selector: "example.com/m/p TestP", Job: "go-checks"},
		Verdict: "pass", Observed: "ok  \texample.com/m/p\t0.01s", Job: "go-checks", Leg: "",
		Run: model.SpecCheckRun{
			Repository: specResultsRepository, WorkflowRef: specResultsRepository + "/.github/workflows/ci-results.yaml@refs/heads/topic",
			Ref: "refs/heads/topic", EventName: "push", RunID: "7", Attempt: 1, CheckRunID: "97", RunnerEnvironment: "github-hosted",
		},
		Recorder: model.SpecCheckRecorder{
			Session:   "ci-results:" + specResultsRepository + "/.github/workflows/ci-results.yaml",
			Principal: "workflow-results:" + specResultsRepository + "/.github/workflows/ci-results.yaml",
			Person:    "ada@example.com",
		},
		RecordedAt: predicate.RecordedAt,
	}
	if predicate != want || predicate.RecordedAt.IsZero() {
		t.Fatalf("the predicate is\n%+v\nwant\n%+v", predicate, want)
	}
	if result.Kind != "run" || result.Seq <= 0 || result.ID == "" {
		t.Fatalf("the row is kind %q, seq %d, id %q, want a run with Dispatch's record number and an id", result.Kind, result.Seq, result.ID)
	}
	read := rig.read(t, specResultsCommit, "p-1")
	if len(read) != 1 || read[0].ID != result.ID || read[0].Seq != result.Seq || read[0].Attestation.Predicate != predicate {
		t.Fatalf("the read answered %+v, want the one row the write answered", read)
	}
}

// TestAnotherCommitIsRefusedNamingBoth: a job writes a result only for the commit of its own
// run, and the refusal names both commits.
func TestAnotherCommitIsRefusedNamingBoth(t *testing.T) {
	rig := newSpecResultsRig(t)
	body := passOf("p-1")
	body["commit"] = specResultsOtherCommit
	refusal := rig.refusedWith(t, rig.post(t, specResultsProject, rig.token(nil), body), http.StatusForbidden, "COMMIT_NOT_THE_RUNS")
	if !strings.Contains(refusal.Error, specResultsOtherCommit) || !strings.Contains(refusal.Error, specResultsCommit) {
		t.Fatalf("the refusal %q names not both commits", refusal.Error)
	}
}

// TestAnIdentityFieldInTheBodyIsRefused: the identity on a result is the one the token proved,
// never a field the caller set, so a body naming any field but the four is refused by name.
func TestAnIdentityFieldInTheBodyIsRefused(t *testing.T) {
	rig := newSpecResultsRig(t)
	for _, field := range []string{"actor", "job", "leg", "attempt", "role", "session", "repository", "run_id", "check_run_id", "procedure_digest", "subject"} {
		t.Run(field, func(t *testing.T) {
			body := passOf("p-1")
			body[field] = "chosen-by-the-caller"
			refusal := rig.refusedWith(t, rig.post(t, specResultsProject, rig.token(nil), body), http.StatusBadRequest, "RESULT_INPUT")
			if !strings.Contains(refusal.Error, field) {
				t.Fatalf("the refusal %q does not name %s", refusal.Error, field)
			}
		})
	}
}

// TestACheckTheFilesDoNotHoldIsRefused: a result is for a check of the files at the run's commit.
func TestACheckTheFilesDoNotHoldIsRefused(t *testing.T) {
	rig := newSpecResultsRig(t)
	rig.refusedWith(t, rig.post(t, specResultsProject, rig.token(nil), passOf("p-9")), http.StatusConflict, "CHECK_NOT_IN_FILES")
}

// TestACheckOfAnotherJobIsRefused: the check names the job envoy-go, and the run's check run is
// the job go-checks, which did not run that check.
func TestACheckOfAnotherJobIsRefused(t *testing.T) {
	rig := newSpecResultsRig(t)
	rig.refusedWith(t, rig.post(t, specResultsProject, rig.token(nil), passOf("p-2")), http.StatusConflict, "CHECK_NOT_THIS_JOBS")
}

// TestACheckRunOfAnotherCommitIsRefused: GitHub names another commit for the token's check run,
// so the check run is no job of this run.
func TestACheckRunOfAnotherCommitIsRefused(t *testing.T) {
	rig := newSpecResultsRig(t)
	rig.github.checkRuns[specResultsCheckRun] = specCheckRunAnswer{name: "go-checks", headSHA: specResultsOtherCommit}
	rig.refusedWith(t, rig.post(t, specResultsProject, rig.token(nil), passOf("p-1")), http.StatusForbidden, "CHECK_RUN_NOT_THE_RUNS")
}

// TestAForgedAccessTokenIsRefusedAtTheRoute: a token the broker answers inactive, presented with
// a sound DPoP proof of the caller's own key, writes nothing.
func TestAForgedAccessTokenIsRefusedAtTheRoute(t *testing.T) {
	rig := newSpecResultsRig(t)
	rig.refusedWith(t, rig.post(t, specResultsProject, "a-token-the-broker-never-issued", passOf("p-1")), http.StatusUnauthorized, "BROKER_TOKEN_NOT_ACTIVE")
}

// TestOnlyAResultsTokenWritesTheRoute: a session's own broker token of the project, and a
// person's cookie, are refused the route: only a CI job's results token writes a result there.
func TestOnlyAResultsTokenWritesTheRoute(t *testing.T) {
	rig := newSpecResultsRig(t)
	named := liveNamedToken(rig.caller.jkt)
	named.Project = specResultsProject
	const session = "a-session-token-of-the-project"
	rig.broker.Says(session, named)
	rig.refusedWith(t, rig.post(t, specResultsProject, session, passOf("p-1")), http.StatusForbidden, "RESULTS_SCOPE_REQUIRED")
	rig.refusedWith(t, rig.asPerson(t, http.MethodPost, "/api/v1/projects/"+specResultsProject+"/results", passOf("p-1")), http.StatusForbidden, "RESULTS_SCOPE_REQUIRED")
}

// TestAProjectNotTheTokensIsRefused: a results token writes the results of its own project alone.
func TestAProjectNotTheTokensIsRefused(t *testing.T) {
	rig := newSpecResultsRig(t)
	rig.refusedWith(t, rig.post(t, "OTHER", rig.token(nil), passOf("p-1")), http.StatusForbidden, codeProjectScope)
}

// TestARepositoryTheProjectDoesNotMapIsRefused: the token's run is of a repository the project
// does not map, so its files are no files of the project.
func TestARepositoryTheProjectDoesNotMapIsRefused(t *testing.T) {
	rig := newSpecResultsRig(t)
	token := rig.token(func(answer *brokertoken.Introspection) { answer.CI.Repository = "example-org/another-repo" })
	rig.refusedWith(t, rig.post(t, specResultsProject, token, passOf("p-1")), http.StatusForbidden, "REPOSITORY_NOT_THE_PROJECTS")
}

// TestGitHubUnreadableIsAServiceError: a check run or files GitHub does not answer leave the
// result unjudged, so the route answers 503 and stores nothing, and the job writes it again.
func TestGitHubUnreadableIsAServiceError(t *testing.T) {
	t.Run("the check run", func(t *testing.T) {
		rig := newSpecResultsRig(t)
		rig.github.checkRunStatus = http.StatusBadGateway
		rig.refusedWith(t, rig.post(t, specResultsProject, rig.token(nil), passOf("p-1")), http.StatusServiceUnavailable, "CHECK_RUN_UNREADABLE")
	})
	t.Run("the files", func(t *testing.T) {
		rig := newSpecResultsRig(t)
		rig.github.filesStatus = http.StatusBadGateway
		rig.refusedWith(t, rig.post(t, specResultsProject, rig.token(nil), passOf("p-1")), http.StatusServiceUnavailable, "FILES_UNREADABLE")
	})
}

// storedRow is one row of spec_check_results as Postgres holds it, as text.
func storedRow(t *testing.T, rig *specResultsRig, id string) string {
	t.Helper()
	var row string
	if err := rig.database.Pool.QueryRow(context.Background(), `select row_to_json(r)::text from spec_check_results r where id = $1`, id).Scan(&row); err != nil {
		t.Fatalf("read row %s: %v", id, err)
	}
	return row
}

// TestAResultIsAppendedAndNeverEdited: a second result of one check at one commit is a second
// row with a higher record number, and the first row's bytes stay as they were. GitHub is read
// once for the check run and once for the files: both reads are cached for the process.
func TestAResultIsAppendedAndNeverEdited(t *testing.T) {
	rig := newSpecResultsRig(t)
	first := stored201(t, rig.post(t, specResultsProject, rig.token(nil), passOf("p-1")))
	before := storedRow(t, rig, first.ID)
	failed := passOf("p-1")
	failed["verdict"] = "fail"
	second := stored201(t, rig.post(t, specResultsProject, rig.token(nil), failed))
	if second.ID == first.ID || second.Seq <= first.Seq {
		t.Fatalf("the second result is id %s seq %d beside the first's %s %d, want a new row after it", second.ID, second.Seq, first.ID, first.Seq)
	}
	if after := storedRow(t, rig, first.ID); after != before {
		t.Fatalf("the first row changed:\n%s\nto\n%s", before, after)
	}
	if held := rig.stored(t); held != 2 {
		t.Fatalf("spec_check_results holds %d rows, want 2", held)
	}
	rig.github.mu.Lock()
	defer rig.github.mu.Unlock()
	if rig.github.checkRunReads != 1 || rig.github.treeReads != 1 {
		t.Fatalf("GitHub was read %d times for the check run and %d for the files, want once each", rig.github.checkRunReads, rig.github.treeReads)
	}
}

// TestTheReadIsInRecordOrderNewestFirst: the read answers every row of the commit by Dispatch's
// record number, newest first. Two more rows are written in one transaction, so they carry one
// recorded_at, which a read ordered by time alone would answer in either order.
func TestTheReadIsInRecordOrderNewestFirst(t *testing.T) {
	rig := newSpecResultsRig(t)
	var wrote []int64
	for range 3 {
		wrote = append(wrote, stored201(t, rig.post(t, specResultsProject, rig.token(nil), passOf("p-1"))).Seq)
	}
	ctx := context.Background()
	tx, err := rig.database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	for range 2 {
		row := specCheckRow{
			project: specResultsProject, repository: specResultsRepository, check: "p-1", commit: specResultsCommit,
			procedureDigest: specCheckDigest(t, "p-1"), job: "go-checks", checkRunID: specResultsCheckRun, runID: 7, attempt: 1,
			verdict: "pass", session: "ci-results:" + specResultsRepository + "/.github/workflows/ci-results.yaml",
		}
		stored, err := insertSpecCheckResult(ctx, tx, row)
		if err != nil {
			t.Fatalf("insert in one transaction: %v", err)
		}
		wrote = append(wrote, stored.Seq)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	read := rig.read(t, specResultsCommit, "")
	if len(read) != len(wrote) {
		t.Fatalf("the read answered %d rows, want %d", len(read), len(wrote))
	}
	for i, result := range read {
		if want := wrote[len(wrote)-1-i]; result.Seq != want {
			t.Fatalf("row %d of the read is seq %d, want %d: the rows newest first are %v", i, result.Seq, want, wrote)
		}
	}
}

// TestEveryAttemptAndLegIsKept: each leg of the matrix job m and each attempt of a leg is a row
// of its own, with the job and the leg from its check run's name, and none is marked or dropped.
func TestEveryAttemptAndLegIsKept(t *testing.T) {
	rig := newSpecResultsRig(t)
	runs := []struct {
		id      int64
		name    string
		attempt string
		verdict string
	}{{21, "m (a)", "1", "fail"}, {22, "m (b)", "1", "pass"}, {23, "m (a)", "2", "pass"}}
	for _, run := range runs {
		rig.github.checkRuns[run.id] = specCheckRunAnswer{name: run.name, headSHA: specResultsCommit}
		token := rig.token(func(answer *brokertoken.Introspection) {
			answer.CI.CheckRunID, answer.CI.RunAttempt = fmt.Sprint(run.id), run.attempt
		})
		body := passOf("m-1")
		body["verdict"] = run.verdict
		stored201(t, rig.post(t, specResultsProject, token, body))
	}
	read := rig.read(t, specResultsCommit, "m-1")
	type kept struct {
		job, leg, verdict string
		attempt           int
	}
	var got []kept
	for _, result := range read {
		p := result.Attestation.Predicate
		got = append(got, kept{p.Job, p.Leg, p.Verdict, p.Run.Attempt})
	}
	want := []kept{{"m", "a", "pass", 2}, {"m", "b", "pass", 1}, {"m", "a", "fail", 1}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("the read answered %v, want %v newest first", got, want)
	}
}

// TestTwoLegsOfOneNameInOneAttemptAreTwoRows: two check runs of one name in one attempt are two
// legs, and each is a row.
func TestTwoLegsOfOneNameInOneAttemptAreTwoRows(t *testing.T) {
	rig := newSpecResultsRig(t)
	for _, id := range []int64{11, 12} {
		rig.github.checkRuns[id] = specCheckRunAnswer{name: "go-checks", headSHA: specResultsCommit}
		stored201(t, rig.post(t, specResultsProject, rig.token(func(answer *brokertoken.Introspection) { answer.CI.CheckRunID = fmt.Sprint(id) }), passOf("p-1")))
	}
	read := rig.read(t, specResultsCommit, "p-1")
	if len(read) != 2 || read[0].Attestation.Predicate.Run.CheckRunID != "12" || read[1].Attestation.Predicate.Run.CheckRunID != "11" {
		t.Fatalf("the read answered %+v, want the rows of check runs 12 and 11", read)
	}
}

// TestTwoRunsAtOneCommitAreTwoRows: two workflow runs of one commit each write a row.
func TestTwoRunsAtOneCommitAreTwoRows(t *testing.T) {
	rig := newSpecResultsRig(t)
	for _, run := range []string{"1", "2"} {
		stored201(t, rig.post(t, specResultsProject, rig.token(func(answer *brokertoken.Introspection) { answer.CI.RunID = run }), passOf("p-1")))
	}
	read := rig.read(t, specResultsCommit, "p-1")
	if len(read) != 2 || read[0].Attestation.Predicate.Run.RunID != "2" || read[1].Attestation.Predicate.Run.RunID != "1" {
		t.Fatalf("the read answered %+v, want the rows of runs 2 and 1", read)
	}
}

// TestObservedIsCutTo2000Runes: what the job observed is kept to its first 2000 runes.
func TestObservedIsCutTo2000Runes(t *testing.T) {
	rig := newSpecResultsRig(t)
	body := passOf("p-1")
	body["observed"] = strings.Repeat("é", 2500)
	observed := stored201(t, rig.post(t, specResultsProject, rig.token(nil), body)).Attestation.Predicate.Observed
	if utf8.RuneCountInString(observed) != 2000 || observed != strings.Repeat("é", 2000) {
		t.Fatalf("observed holds %d runes, want the first 2000", utf8.RuneCountInString(observed))
	}
}

// TestACouldNotRunResultIsStored: a job whose test could not run writes that, and it is stored as
// it was written.
func TestACouldNotRunResultIsStored(t *testing.T) {
	rig := newSpecResultsRig(t)
	body := passOf("p-1")
	body["verdict"] = "could_not_run"
	if verdict := stored201(t, rig.post(t, specResultsProject, rig.token(nil), body)).Attestation.Predicate.Verdict; verdict != "could_not_run" {
		t.Fatalf("the stored verdict is %q, want could_not_run", verdict)
	}
}

// TestTheReadIsRefusedToAResultsToken: a results token writes results and reads nothing, the
// results of its own project included.
func TestTheReadIsRefusedToAResultsToken(t *testing.T) {
	rig := newSpecResultsRig(t)
	response := rig.serve(rig.caller.presenting(t, http.MethodGet, "/api/v1/projects/"+specResultsProject+"/results?commit="+specResultsCommit, rig.token(nil), nil))
	if why := notTheLimitedRefusal(response, codeResultsOnlyToken); why != "" {
		t.Fatalf("the read %s, want 403 %s", why, codeResultsOnlyToken)
	}
}
