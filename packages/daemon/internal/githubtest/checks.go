package githubtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/legion/daemon/internal/testnats"
)

// Checks is GitHub as a GitHub App's client reads the check runs of a commit: the App's
// installation on any repository, an installation token for it, the repository itself, and the
// check runs the test set on each commit (SetCheckRun), served at
// GET /repos/{owner}/{repo}/commits/{ref}/check-runs. Dispatch reads a check its CI job proves
// there, through its App client (packages/envoy/internal/dispatch/githubapp): it looks the
// installation up with the App's JWT, mints a token, and reads the commit's check runs with it,
// and it reads the repository with that token before it stores its delivery settings. No call
// is checked for a credential.
//
// Every other request is answered 404 and kept (Unanswered): a Dispatch that holds an App calls
// GitHub for more than a test plans, such as its webhook redelivery sweep, so such a request is
// what a test that waited for nothing names, never a failure of its own.
type Checks struct {
	// URL is the server's root, which Dispatch is given as its DISPATCH_GITHUB_API_BASE.
	URL string

	mu         sync.Mutex
	runs       map[string][]CheckRun // by "owner/name@sha"
	unanswered []string
}

// CheckRun is one check run of a commit, with the fields Dispatch reads of each run it lists:
// GitHub's id, the job's name, its status and conclusion, its completion time, its page, its
// output, and the slug of the App that made it. GitHub keeps every attempt of a check, and
// Dispatch takes the attempt of the highest id of each name, so a run of a new id is a new
// attempt, and a run set with the id of one already set replaces it.
type CheckRun struct {
	ID          int64
	Name        string
	Status      string // queued, in_progress or completed
	Conclusion  string // empty until the run has completed
	CompletedAt time.Time
	HTMLURL     string // the run's page
	Output      CheckRunOutput
	AppSlug     string
}

// CheckRunOutput is a check run's output, as its job wrote it.
type CheckRunOutput struct {
	Title, Summary string
}

// checksInstallation is the id of the App's one installation, which covers every repository.
const checksInstallation = 1

// ServeChecks starts Checks with no check run on any commit; it is closed when the test ends.
func ServeChecks(t *testing.T) *Checks {
	t.Helper()
	c := &Checks{runs: map[string][]CheckRun{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{repo}/installation", c.installation)
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", c.token)
	mux.HandleFunc("GET /repos/{owner}/{repo}", c.repository)
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{ref}/check-runs", c.checkRuns)
	mux.HandleFunc("/", c.notFound)
	c.URL = Server(t, Spec{Handle: mux.ServeHTTP})
	return c
}

// SetCheckRun sets run on commit sha of repo, "owner/name", from the next read on.
func (c *Checks) SetCheckRun(repo, sha string, run CheckRun) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := repo + "@" + sha
	runs := c.runs[key]
	if at := slices.IndexFunc(runs, func(held CheckRun) bool { return held.ID == run.ID }); at >= 0 {
		runs[at] = run
		return
	}
	c.runs[key] = append(runs, run)
}

// Unanswered is each request the server answered 404, as "METHOD /path", in the order it came.
func (c *Checks) Unanswered() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.unanswered)
}

// installation answers the App's installation on any repository, with the permissions a read of
// a repository's contents and check runs needs.
func (c *Checks) installation(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"id": checksInstallation, "app_slug": "scratch-dispatch", "permissions": checksPermissions})
}

// token mints an installation token of the one installation, valid for an hour.
func (c *Checks) token(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") != strconv.Itoa(checksInstallation) {
		c.notFound(w, r)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": "ghs_scratch_dispatch",
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "permissions": checksPermissions})
}

// checksPermissions is what the installation and each token it mints may read.
var checksPermissions = map[string]string{"metadata": "read", "contents": "read", "checks": "read", "pull_requests": "read"}

// repository answers any repository, on its default branch main.
func (c *Checks) repository(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("repo")
	writeJSON(w, http.StatusOK, map[string]any{"id": 1, "name": name, "full_name": owner + "/" + name,
		"owner": map[string]string{"login": owner}, "private": false, "default_branch": "main"})
}

// checkRuns answers every run set on the commit, in one page: the listing GitHub gives
// filter=all, each attempt of each check.
func (c *Checks) checkRuns(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	runs := slices.Clone(c.runs[r.PathValue("owner")+"/"+r.PathValue("repo")+"@"+r.PathValue("ref")])
	c.mu.Unlock()
	listed := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		listed = append(listed, run.listed(r.PathValue("ref")))
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(listed), "check_runs": listed})
}

// listed is run as GitHub lists it on commit sha: a field the run does not have yet is null.
func (run CheckRun) listed(sha string) map[string]any {
	listed := map[string]any{
		"id": run.ID, "name": run.Name, "head_sha": sha, "status": run.Status, "conclusion": nil, "completed_at": nil,
		"html_url": run.HTMLURL, "output": map[string]any{"title": nil, "summary": nil}, "app": map[string]any{"slug": run.AppSlug},
	}
	if run.Conclusion != "" {
		listed["conclusion"] = run.Conclusion
	}
	if !run.CompletedAt.IsZero() {
		listed["completed_at"] = run.CompletedAt.UTC().Format(time.RFC3339)
	}
	if run.Output.Title != "" || run.Output.Summary != "" {
		listed["output"] = map[string]any{"title": run.Output.Title, "summary": run.Output.Summary}
	}
	return listed
}

// notFound answers a request no route models 404 and keeps it.
func (c *Checks) notFound(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.unanswered = append(c.unanswered, r.Method+" "+r.URL.Path)
	c.mu.Unlock()
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// ChecksSettlement is what the listener's CI summary loop settles once every check run of a
// commit of a pull request has finished: the repository, "owner/name", the pull request's
// number, the commit, and the newest attempt of each check run there.
type ChecksSettlement struct {
	Repo   string
	Number int
	SHA    string
	Runs   []CheckRun
	// Statuses are the commit statuses the listener's record holds for the commit, the newest of
	// each context.
	Statuses []CommitStatus
	// Generation numbers the listener's settlements of one commit from 1; zero is 1.
	Generation uint64
	// Superseded says the listener settled the commit before.
	Superseded bool
}

// CommitStatus is one commit status of a commit: its context, its state, success, failure or
// error, and when GitHub created it.
type CommitStatus struct {
	Context   string
	State     string
	CreatedAt time.Time
}

// DispatchDeliveryDurable is the durable Dispatch's delivery intake reads GitHub's envelopes
// from, on the notification stream, the settlements PublishChecksSettlement publishes among them
// (Envoy's bus.DispatchDeliveryDurable).
const DispatchDeliveryDurable = "delivery-events"

// PublishChecksSettlement publishes s on the NATS server at natsURL as the listener's CI summary
// loop publishes a settlement (packages/envoy/internal/cistore, settlementEnvelope): an Envoy
// envelope on notifications.github.<owner>.<name>.pr.<number>.checks whose payload is the
// settlement in the shape of cistore.Summary, kind "checks" with its repo, number and sha, each
// run's name, id and conclusion, and the groups of check names the listener's own rule puts each
// run in (cistore's classify). It is stored on the notification stream, ENVOY_NOTIFICATIONS,
// which Dispatch's delivery intake and the daemon's intake both read; the call returns the
// stream sequence of the message once the stream has taken it.
func PublishChecksSettlement(t *testing.T, natsURL string, s ChecksSettlement) uint64 {
	t.Helper()
	published, err := publishedSettlementOf(s)
	if err != nil {
		t.Fatalf("settle the checks of %s#%d at %s: %v", s.Repo, s.Number, s.SHA, err)
	}
	payload, err := json.Marshal(published.payload)
	if err != nil {
		t.Fatalf("encode the settlement of %s#%d at %s: %v", s.Repo, s.Number, s.SHA, err)
	}
	id := fmt.Sprintf("checks-%s-%d-%s-g%d-%d", strings.ReplaceAll(s.Repo, "/", "-"), s.Number, s.SHA, published.payload.Generation,
		time.Now().UnixNano())
	envelope, err := json.Marshal(map[string]any{
		"event_id": id, "source": "github", "source_event_id": id + "-settlement", "topic": published.subject,
		"dedupe_key":      published.dedupeKey,
		"issued_at":       time.Now().UnixMilli(),
		"payload_summary": fmt.Sprintf("checks settled on %s#%d @ %.7s", s.Repo, s.Number, s.SHA),
		"payload":         string(payload), "trace_id": id + "-trace",
	})
	if err != nil {
		t.Fatalf("encode the envelope of the settlement of %s#%d at %s: %v", s.Repo, s.Number, s.SHA, err)
	}
	js, err := jetstream.New(testnats.Connect(t, natsURL))
	if err != nil {
		t.Fatalf("open JetStream on %s: %v", natsURL, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ack, err := js.Publish(ctx, published.subject, envelope)
	if err != nil {
		t.Fatalf("publish the settlement of %s#%d at %s on %s: %v", s.Repo, s.Number, s.SHA, published.subject, err)
	}
	return ack.Sequence
}

// publishedSettlement is what the listener's publish makes of a settlement that its readers read
// (cistore's settlementEnvelope): the subject it is published on, the envelope's dedupe key and the
// payload.
type publishedSettlement struct {
	subject, dedupeKey string
	payload            settlement
}

// publishedSettlementOf is s as the listener publishes it.
func publishedSettlementOf(s ChecksSettlement) (publishedSettlement, error) {
	owner, name, ok := strings.Cut(s.Repo, "/")
	if !ok {
		return publishedSettlement{}, fmt.Errorf("a repository is owner/name, not %q", s.Repo)
	}
	generation := max(s.Generation, 1)
	return publishedSettlement{
		subject:   fmt.Sprintf("notifications.github.%s.%s.pr.%d.checks", subjectSegment(owner), subjectSegment(name), s.Number),
		dedupeKey: fmt.Sprintf("github.checks.%s.pr.%d.%s.g%d", s.Repo, s.Number, s.SHA, generation),
		payload:   settlementOf(s, generation),
	}, nil
}

// settlement is cistore.Summary's JSON, member for member and in its order.
type settlement struct {
	Kind          string             `json:"kind"`
	Repo          string             `json:"repo"`
	Number        string             `json:"number"`
	SHA           string             `json:"sha"`
	CheckRuns     []settledRun       `json:"check_runs"`
	Generation    uint64             `json:"generation"`
	Snapshot      string             `json:"snapshot"`
	Failed        settledGroup       `json:"failed"`
	Running       settledGroup       `json:"running"`
	Passed        settledGroup       `json:"passed"`
	Queued        settledGroup       `json:"queued"`
	Cancelled     settledGroup       `json:"cancelled"`
	Skipped       settledGroup       `json:"skipped"`
	FailingChecks []settledFailedRun `json:"failing_checks"`
}

type settledRun struct {
	Name       string `json:"name"`
	ID         int64  `json:"id"`
	Conclusion string `json:"conclusion,omitempty"`
}

type settledGroup struct {
	Count  int      `json:"count"`
	Checks []string `json:"checks"`
}

type settledFailedRun struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// settlementOf is s as the listener renders it (cistore's renderSummary): its runs sorted by
// name, each group's names sorted, and every group present.
func settlementOf(s ChecksSettlement, generation uint64) settlement {
	runs := slices.Clone(s.Runs)
	slices.SortFunc(runs, func(a, b CheckRun) int { return strings.Compare(a.Name, b.Name) })
	groups := map[string][]string{}
	failing := []settledFailedRun{}
	settled := settlement{Kind: "checks", Repo: s.Repo, Number: strconv.Itoa(s.Number), SHA: s.SHA, CheckRuns: []settledRun{},
		Generation: generation, Snapshot: fmt.Sprintf("%s-g%d", s.SHA, generation)}
	for _, run := range runs {
		settled.CheckRuns = append(settled.CheckRuns, settledRun{Name: run.Name, ID: run.ID, Conclusion: run.Conclusion})
		group := settledGroupOf(run)
		groups[group] = append(groups[group], run.Name)
		if group == "failed" {
			failing = append(failing, settledFailedRun{Name: run.Name, URL: run.HTMLURL})
		}
	}
	named := func(group string) settledGroup {
		names := append([]string{}, groups[group]...)
		slices.Sort(names)
		return settledGroup{Count: len(names), Checks: names}
	}
	settled.Failed, settled.Running, settled.Passed = named("failed"), named("running"), named("passed")
	settled.Queued, settled.Cancelled, settled.Skipped = named("queued"), named("cancelled"), named("skipped")
	settled.FailingChecks = failing
	return settled
}

// settledGroupOf is the group the listener puts run in (cistore's classify): a completed run by
// its conclusion, a conclusion it does not know failed; any other by its status, one it does not
// know queued.
func settledGroupOf(run CheckRun) string {
	if run.Status == "completed" {
		switch run.Conclusion {
		case "cancelled":
			return "cancelled"
		case "skipped":
			return "skipped"
		case "neutral", "success", "":
			return "passed"
		default:
			return "failed"
		}
	}
	if run.Status == "in_progress" {
		return "running"
	}
	return "queued"
}

// subjectSegment is value as one NATS subject segment, as Envoy's contracts write it
// (SanitizeSubjectSegment): a dot, whitespace and the wildcards become underscores.
func subjectSegment(value string) string {
	return subjectSegmentReplacer.Replace(value)
}

var subjectSegmentReplacer = strings.NewReplacer(".", "_", " ", "_", "\t", "_", "\r", "_", "\n", "_", "*", "_", ">", "_")
