package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/githubtest"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/testwait"
	"github.com/sjawhar/legion/daemon/internal/verdict"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// verdictAppLogin is the one App's bot login in these rigs, and verdictAppSlug the slug GitHub
// names that App by on each check run it made.
const (
	verdictAppLogin = "legion-app[bot]"
	verdictAppSlug  = "legion-app"
)

// ciReviewer is the bot login acme/widgets' verdict entry names as its ci_reviewer.
const ciReviewer = "example-ci-review[bot]"

// widgetsEntry is acme/widgets as a verdict repository whose entry names its ci_reviewer.
var widgetsEntry = config.VerdictRepository{Repository: ghrepo.MustParse("acme/widgets"), CIReviewer: ciReviewer}

// verdictBearer is the Authorization a write of legion-verdict on acme/widgets carries: the
// daemon's own verdict token, as roleTokens mints it.
func verdictBearer() string { return "Bearer " + setToken(appauth.SetVerdict, "acme") }

// fakeRun is one check run GitHub holds at a head: its id, name, conclusion and the slug of the
// App that made it.
type fakeRun struct {
	ID         int64
	Head       string
	Name       string
	Conclusion string
	App        string
}

// fakeStatus is one commit status GitHub holds at a head, newest last.
type fakeStatus struct {
	ID      int64
	Head    string
	Context string
	State   string
	Creator string
}

// fakeWrite is one write of a check run or a commit status the daemon made: the method, the path
// under the repository, the bearer and the body.
type fakeWrite struct {
	Method, Path, Bearer string
	Body                 map[string]any
}

// verdictGitHub is GitHub's REST API for acme/widgets as the writer of legion-verdict meets it: the
// check runs and the commit statuses of each head, created and updated by the daemon's writes; pull
// request 86's merge state, `blocked` while its head has no legion-verdict run concluding success
// and `clean` once it has, which is what a ruleset that requires the check answers; and any other
// pull request named in pulls, whose reads it counts in pullReads. Each write and each read of pull
// request 86 is recorded in order. failCreates is how many of the next creates of a check run
// GitHub answers 502, as it does when it fails a request; staleReads is how many reads of pull
// request 86, made once its head has a success run, still answer `blocked`, as GitHub does while it
// has not yet counted a new run.
type verdictGitHub struct {
	mu          sync.Mutex
	nextID      int64
	head86      string
	runs        []fakeRun
	statuses    []fakeStatus
	writes      []fakeWrite
	events      []string
	pulls       map[int]string
	pullReads   map[int]int
	failCreates int
	staleReads  int
}

func newVerdictGitHub() *verdictGitHub {
	return &verdictGitHub{nextID: 1000, head86: "code", pulls: map[int]string{}, pullReads: map[int]int{}}
}

func (g *verdictGitHub) serve(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		path, ok := strings.CutPrefix(r.URL.Path, "/repos/acme/widgets")
		if !ok {
			githubtest.Unexpected(t, w, r)
			return
		}
		parts := strings.Split(strings.Trim(path, "/"), "/")
		answer := func(v any) {
			if err := json.NewEncoder(w).Encode(v); err != nil {
				t.Errorf("encode the fake's answer: %v", err)
			}
		}
		body := func() map[string]any {
			var sent map[string]any
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Errorf("decode %s %s: %v", r.Method, r.URL.Path, err)
			}
			g.writes = append(g.writes, fakeWrite{Method: r.Method, Path: path, Bearer: r.Header.Get("Authorization"), Body: sent})
			return sent
		}
		switch {
		case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "pulls" && parts[1] == "86":
			state := "blocked"
			if slices.ContainsFunc(g.runs, func(run fakeRun) bool {
				return run.Head == g.head86 && run.Name == verdict.CheckName && run.App == verdictAppSlug && run.Conclusion == "success"
			}) {
				state = "clean"
				if g.staleReads > 0 {
					g.staleReads--
					state = "blocked"
				}
			}
			g.events = append(g.events, "read pull request 86: "+state)
			answer(map[string]any{"number": 86, "state": "open", "base": map[string]any{"ref": "main"},
				"head": map[string]any{"ref": "legion/CAPTURE-1", "sha": g.head86}, "mergeable": true, "mergeable_state": state})
		case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "pulls":
			number, _ := strconv.Atoi(parts[1])
			pull, known := g.pulls[number]
			g.pullReads[number]++
			if !known {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(pull))
		case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "collaborators" && parts[2] == "permission":
			// a-writer has write access, a-reader read access; every bot is an App, which GitHub
			// may give write access and the daemon never reads as a person.
			login := parts[1]
			switch {
			case login == "a-writer":
				answer(map[string]any{"permission": "write", "user": map[string]any{"login": login, "type": "User"}})
			case login == "a-reader":
				answer(map[string]any{"permission": "read", "user": map[string]any{"login": login, "type": "User"}})
			case strings.HasSuffix(login, "[bot]"):
				answer(map[string]any{"permission": "write", "user": map[string]any{"login": login, "type": "Bot"}})
			default:
				http.NotFound(w, r)
			}
		case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "commits" && parts[2] == "check-runs":
			if r.Header.Get("Authorization") != verdictBearer() {
				t.Errorf("the check runs of %s were listed with %q, want the verdict token", parts[1], r.Header.Get("Authorization"))
			}
			listed := []map[string]any{}
			for _, run := range g.runs {
				if run.Head == parts[1] && run.Name == r.URL.Query().Get("check_name") {
					listed = append(listed, map[string]any{"id": run.ID, "head_sha": run.Head, "name": run.Name,
						"status": "completed", "conclusion": run.Conclusion, "app": map[string]any{"slug": run.App}})
				}
			}
			answer(map[string]any{"total_count": len(listed), "check_runs": listed})
		case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "commits" && parts[2] == "statuses":
			listed := []map[string]any{}
			for i := len(g.statuses) - 1; i >= 0; i-- {
				if status := g.statuses[i]; status.Head == parts[1] {
					listed = append(listed, map[string]any{"id": status.ID, "context": status.Context, "state": status.State,
						"creator": map[string]any{"login": status.Creator}})
				}
			}
			answer(listed)
		case r.Method == http.MethodPost && path == "/check-runs" && g.failCreates > 0:
			body()
			g.failCreates--
			g.events = append(g.events, "refused a create with 502")
			http.Error(w, `{"message":"Server Error"}`, http.StatusBadGateway)
		case r.Method == http.MethodPost && path == "/check-runs":
			sent := body()
			g.nextID++
			run := fakeRun{ID: g.nextID, Head: fmt.Sprint(sent["head_sha"]), Name: fmt.Sprint(sent["name"]), Conclusion: fmt.Sprint(sent["conclusion"]), App: verdictAppSlug}
			g.runs = append(g.runs, run)
			g.events = append(g.events, fmt.Sprintf("created run %d on %s: %s", run.ID, run.Head, run.Conclusion))
			w.WriteHeader(http.StatusCreated)
			answer(map[string]any{"id": run.ID, "head_sha": run.Head, "conclusion": run.Conclusion})
		case r.Method == http.MethodPatch && len(parts) == 2 && parts[0] == "check-runs":
			sent := body()
			id, _ := strconv.ParseInt(parts[1], 10, 64)
			at := slices.IndexFunc(g.runs, func(run fakeRun) bool { return run.ID == id })
			if at < 0 {
				http.NotFound(w, r)
				return
			}
			g.runs[at].Conclusion = fmt.Sprint(sent["conclusion"])
			g.events = append(g.events, fmt.Sprintf("updated run %d on %s: %s", id, g.runs[at].Head, g.runs[at].Conclusion))
			answer(map[string]any{"id": id, "conclusion": g.runs[at].Conclusion})
		case r.Method == http.MethodPost && len(parts) == 2 && parts[0] == "statuses":
			sent := body()
			g.nextID++
			status := fakeStatus{ID: g.nextID, Head: parts[1], Context: fmt.Sprint(sent["context"]), State: fmt.Sprint(sent["state"]), Creator: verdictAppLogin}
			g.statuses = append(g.statuses, status)
			g.events = append(g.events, fmt.Sprintf("created status %d on %s: %s", status.ID, status.Head, status.State))
			w.WriteHeader(http.StatusCreated)
			answer(map[string]any{"id": status.ID, "state": status.State})
		case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "compare":
			// The writer makes no compare call: the carry of a tester's pass is the reviewer's
			// tested commit, never a diff (spec R4). The fake answers one as GitHub answers two
			// identical trees and records it, so a writer that carried a pass on that answer shows.
			g.events = append(g.events, "compared "+parts[1])
			answer(map[string]any{"status": "identical", "ahead_by": 0, "behind_by": 0, "files": []any{}})
		default:
			githubtest.Unexpected(t, w, r)
		}
	}
}

// legionRuns is every legion-verdict check run GitHub holds at head.
func (g *verdictGitHub) legionRuns(head string) []fakeRun {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(g.runs), func(run fakeRun) bool { return run.Head != head || run.Name != verdict.CheckName })
}

// recorded is every write the daemon made, and every event, in order.
func (g *verdictGitHub) recorded() ([]fakeWrite, []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.writes), slices.Clone(g.events)
}

// verdictRig is CAPTURE-1 on acme/widgets' pull request 86 at head code, the workflow runtime
// that writes legion-verdict on acme/widgets as the verdict repository entries say, its writer and
// its merge-state reader running, its log kept, the fake GitHub it writes on, and the head of each
// write the workflow asked for (workflow.Config.WriteVerdict), in order.
type verdictRig struct {
	pool   *store.Pool
	w      *workflowRuntime
	github *verdictGitHub
	logs   *syncBuffer
	n      int

	mu    sync.Mutex
	asked []string
}

// newVerdictRig builds a verdictRig over the store seed fills. wrap, where given, builds the fake's
// handler around the rig's verdictGitHub and its own handler, for a test that serves more of GitHub.
func newVerdictRig(t *testing.T, tokens appauth.Tokens, entries []config.VerdictRepository, seed func(*testing.T, *store.Pool),
	wrap ...func(*verdictGitHub, http.HandlerFunc) http.HandlerFunc) *verdictRig {
	t.Helper()
	rig := &verdictRig{pool: isolatedOutboxPool(t), github: newVerdictGitHub(), logs: &syncBuffer{}}
	seed(t, rig.pool)
	handle := rig.github.serve(t)
	for _, around := range wrap {
		handle = around(rig.github, handle)
	}
	api := githubtest.Server(t, githubtest.Spec{Handle: handle})
	log := slog.New(slog.NewJSONHandler(rig.logs, nil))
	w := &workflowRuntime{
		pool: rig.pool, records: projectRecords{Store: record.NewStore(), project: "CAPTURE"}, repositories: servedAcme(),
		tokens: tokens, dispatchProject: "CAPTURE", bootID: "test-boot", log: log, githubAPI: api, appLogin: verdictAppLogin,
		mergeStates: newWakeSet[string](), mergeStateRunning: map[string]*mergeStateRun{}, mergeStateRetry: readRigWait,
		verdicts: newVerdictWriter(entries),
	}
	w.engine = workflow.New(record.NewStore(), workflow.Config{Project: "CAPTURE", Repositories: testRepositories, AppLogin: verdictAppLogin,
		ReadMergeState: w.queueMergeState, WriteVerdict: func(issue, repo string, number int, head string) {
			rig.mu.Lock()
			rig.asked = append(rig.asked, head)
			rig.mu.Unlock()
			w.queueVerdictWrite(issue, repo, number, head)
		}}, log)
	w.handlers = []intake.Handler{w.engine, w.verdictFacts()}
	rig.w = w
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.readMergeStates(ctx)
	go w.writeVerdicts(ctx)
	return rig
}

// apply applies fact through the rig's handlers under an event id of its own.
func (rig *verdictRig) apply(t *testing.T, fact intake.Fact) {
	t.Helper()
	rig.n++
	applyFact(t, rig.pool, "test", fmt.Sprintf("verdict-%d", rig.n), fact, rig.w.handlers...)
}

// askedHeads is the head of each write the workflow asked for so far.
func (rig *verdictRig) askedHeads() []string {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	return slices.Clone(rig.asked)
}

// review applies fact, a webhook's review, as intake delivers it: with its author's write access
// read from GitHub by the daemon's own reader (reviewerCanWrite, which the fake answers) for the
// reviews intake reads it for - a request for changes of a recorded pull request, and an approval
// or a request for changes of an outside one (intake's resolveReviewPermission, which
// TestADecidingReviewCarriesItsAuthorsWriteAccessBeforeItIsApplied and
// TestLegionVerdictAnOutsidePullRequestIsAFactOnAVerdictRepositoryAlone hold) - and with none
// for every other, as intake leaves it.
func (rig *verdictRig) review(t *testing.T, fact intake.Fact) {
	t.Helper()
	canWrite := func(review intake.PullRequestReview) bool {
		t.Helper()
		ok, err := rig.w.reviewerCanWrite(context.Background(), review)
		if err != nil {
			t.Fatalf("read %s's permission: %v", review.Author, err)
		}
		return ok
	}
	switch review := fact.(type) {
	case intake.PullRequestReview:
		if review.Decides() {
			review.AuthorCanWrite = canWrite(review)
		}
		fact = review
	case intake.OutsideReview:
		if review.Deciding() {
			review.AuthorCanWrite = canWrite(review.PullRequestReview)
		}
		fact = review
	default:
		t.Fatalf("%T is no review", fact)
	}
	rig.apply(t, fact)
}

// written waits until the writer holds no write queued or running.
func (rig *verdictRig) written(t *testing.T) {
	t.Helper()
	testwait.Eventually(t, "the writer of legion-verdict to hold no write", func() bool { return rig.w.verdicts.idle() })
}

// writeLines is every legion_verdict_write line the rig logged.
func (rig *verdictRig) writeLines(t *testing.T) []map[string]any {
	t.Helper()
	return loggedMessages(t, rig.logs.String(), "legion_verdict_write")
}

// verdictWrites is every write of legion-verdict the store records on acme/widgets at head.
func verdictWrites(t *testing.T, pool *store.Pool, head string) []store.VerdictWrite {
	t.Helper()
	var writes []store.VerdictWrite
	if err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
		var err error
		writes, err = store.VerdictWritesTx(context.Background(), tx, ghrepo.MustParse("acme/widgets"), head)
		return err
	}); err != nil {
		t.Fatalf("read the writes of legion-verdict at %s: %v", head, err)
	}
	return writes
}

// verdictReviews is every review the store records of acme/widgets' pull request number.
func verdictReviews(t *testing.T, pool *store.Pool, number int) []store.VerdictReview {
	t.Helper()
	var reviews []store.VerdictReview
	if err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
		var err error
		reviews, err = store.VerdictReviewsTx(context.Background(), tx, ghrepo.MustParse("acme/widgets"), number)
		return err
	}); err != nil {
		t.Fatalf("read the reviews of pull request %d: %v", number, err)
	}
	return reviews
}

// The rows of CAPTURE-1's phases these rigs seed: the tester's pass at code, and a reviewer that
// has given no verdict yet.
var (
	testerPassAtCode = record.PhaseRow{Issue: "CAPTURE-1", Role: claim.RoleTester, Claim: "test-claim", HandoffCommit: "code"}
	reviewerWithout  = record.PhaseRow{Issue: "CAPTURE-1", Role: claim.RoleReviewer, Claim: "review-claim"}
)

// reviewingWithPass seeds CAPTURE-1 in reviewing with the tester's pass at code and no merge state
// read for the head, so an approval waits on GitHub's answer (workflow's reviewRound).
func reviewingWithPass(t *testing.T, pool *store.Pool) {
	t.Helper()
	seedCapture(t, pool, phase.Reviewing, "needs_review", testerPassAtCode, reviewerWithout)
	clearCaptureMergeState(t, pool)
}

// captureReviewer is CAPTURE-1's reviewer claim, the agent its reviewer's step records (the start
// writes the phase worker's claim), which alone the daemon takes that step's verdict from.
const captureReviewer claim.Token = "legion-capture-capture-1-reviewer"

// reviewerVerdict is the reviewer's completion of reviewing with verdict at commit.
func reviewerVerdict(verdict, commit string, n int) intake.HandoffComplete {
	return intake.HandoffComplete{Generation: 1, Issue: "CAPTURE-1", Role: claim.RoleReviewer, Claim: captureReviewer, Summary: "reviewed",
		Verdict: verdict, Commit: commit, HandoffRecord: fmt.Sprintf("review-%d", n), HandoffVersion: n}
}

// personsReview is a webhook's review of pull request 86 at code by a-writer, a person with write
// access, as GitHub delivers it: its write access is the daemon's to read (verdictRig.review).
func personsReview(id int64, state string) intake.PullRequestReview {
	return intake.PullRequestReview{Repo: "acme/widgets", Number: 86, ID: id, State: state, CommitID: "code", HeadSHA: "code",
		Author: "a-writer", SubmittedAt: time.Date(2026, 11, 21, 6, 0, int(id), 0, time.UTC)}
}

// approvedAndWritten applies the reviewer's approve at code to a rig reviewingWithPass seeded, and
// waits for the success it writes and the move to accepting that GitHub's clean answer then makes.
func (rig *verdictRig) approvedAndWritten(t *testing.T) fakeRun {
	t.Helper()
	rig.apply(t, reviewerVerdict("approve", "code", 1))
	testwait.Eventually(t, "the approve to write success on code and the round to settle as approved", func() bool {
		got, _ := capturePhase(t, rig.pool)
		return got == phase.Accepting
	})
	rig.written(t)
	runs := rig.github.legionRuns("code")
	if len(runs) != 1 || runs[0].Conclusion != "success" {
		t.Fatalf("legion-verdict runs on code = %+v, want one concluding success", runs)
	}
	return runs[0]
}

// C2: a tester's pass and a reviewer's approve at head H write legion-verdict success on H with the
// verdict token, in the pass that records the approve and before the merge-state read; GitHub
// then answers a clean merge state, and the round settles as approved. The write is recorded in
// the store and in one legion_verdict_write line.
func TestLegionVerdictApproveWritesSuccessBeforeTheMergeStateRead(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
	run := rig.approvedAndWritten(t)
	writes, events := rig.github.recorded()
	if len(writes) != 1 || writes[0].Method != http.MethodPost || writes[0].Bearer != verdictBearer() ||
		writes[0].Body["head_sha"] != "code" || writes[0].Body["conclusion"] != "success" || writes[0].Body["name"] != verdict.CheckName ||
		writes[0].Body["status"] != "completed" {
		t.Fatalf("writes = %+v, want one POST of a completed legion-verdict concluding success on code with the verdict token", writes)
	}
	if first := slices.IndexFunc(events, func(e string) bool { return strings.HasPrefix(e, "read pull request 86") }); first < 0 ||
		events[first] != "read pull request 86: clean" || !slices.Contains(events[:first], fmt.Sprintf("created run %d on code: success", run.ID)) {
		t.Fatalf("GitHub saw %v, want the run written before the first read of the merge state, which answers clean", events)
	}
	if recorded := verdictWrites(t, rig.pool, "code"); len(recorded) != 1 || recorded[0].GitHubID != run.ID ||
		recorded[0].Kind != store.VerdictWriteCheckRun || recorded[0].Conclusion != store.VerdictConclusionSuccess || recorded[0].ForeignID != 0 {
		t.Fatalf("the store records %+v, want the one check run %d concluding success", recorded, run.ID)
	}
	lines := rig.writeLines(t)
	if len(lines) != 1 || lines[0]["head"] != "code" || lines[0]["conclusion"] != "success" || lines[0]["kind"] != "check_run" ||
		lines[0]["id"] != float64(run.ID) || lines[0]["foreign_id"] != float64(0) || lines[0]["repository"] != "acme/widgets" {
		t.Fatalf("legion_verdict_write lines = %v, want one naming the run, its head, its kind and its conclusion", lines)
	}
}

// GitHub can answer the first read after the success run is written with `blocked`, as it does
// while it has not yet counted a new run. That answer ends the climb, and no other fact asks
// again: Envoy leaves the one App's runs out of the CI record, so the write brings no checks fact.
// One more read of the issue after a write that produced success still settles the approve.
func TestLegionVerdictAStaleBlockedAfterTheWriteStillSettlesTheApprove(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
	rig.github.mu.Lock()
	rig.github.staleReads = 1
	rig.github.mu.Unlock()
	rig.approvedAndWritten(t)
	_, events := rig.github.recorded()
	if first := slices.Index(events, "read pull request 86: blocked"); first < 0 || !slices.Contains(events[first:], "read pull request 86: clean") {
		t.Fatalf("GitHub saw %v, want a stale blocked after the write and then a clean read", events)
	}
}

// C2: a push of a new head writes nothing on that head: a check run belongs to one commit, and no
// verdict names the new one. The new head holds no verdict, so a write asked for there would write
// nothing either: what the test holds is that the push asks for none.
func TestLegionVerdictAPushWritesNothingOnTheNewHead(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
	rig.approvedAndWritten(t)
	rig.apply(t, intake.PullRequestSynchronized{Repo: "acme/widgets", Number: 86, Branch: "legion/CAPTURE-1", Before: "code", HeadSHA: "code-2"})
	rig.written(t)
	if runs := rig.github.legionRuns("code-2"); len(runs) != 0 {
		t.Fatalf("legion-verdict runs on the pushed head = %+v, want none", runs)
	}
	if asked := rig.askedHeads(); !slices.Equal(asked, []string{"code"}) {
		t.Fatalf("the workflow asked for writes on %v, want only the approve's on code", asked)
	}
	if writes, _ := rig.github.recorded(); len(writes) != 1 {
		t.Fatalf("writes = %+v, want only the approve's", writes)
	}
}

// C2: a reviewer's changes at H writes failure on H.
func TestLegionVerdictReviewerChangesWritesFailure(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
	rig.apply(t, reviewerVerdict("changes", "code", 1))
	rig.written(t)
	if runs := rig.github.legionRuns("code"); len(runs) != 1 || runs[0].Conclusion != "failure" {
		t.Fatalf("legion-verdict runs on code = %+v, want one concluding failure", runs)
	}
}

// A write GitHub fails is logged naming what repairs it, the writers' sweep at boot and every 10
// minutes, and leaves the head with no run: the writer makes no second attempt of its own.
func TestLegionVerdictAFailedWriteIsLoggedForTheWritersSweep(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
	rig.github.mu.Lock()
	rig.github.failCreates = 1
	rig.github.mu.Unlock()
	rig.apply(t, reviewerVerdict("changes", "code", 1))
	rig.written(t)
	if runs := rig.github.legionRuns("code"); len(runs) != 0 {
		t.Fatalf("legion-verdict runs on code = %+v, want none after GitHub failed the create", runs)
	}
	lines := loggedMessages(t, rig.logs.String(), "verdict: a write of legion-verdict failed; the writers' sweep at boot and every 10 minutes")
	if len(lines) != 1 || lines[0]["head"] != "code" || lines[0]["issue"] != "CAPTURE-1" {
		t.Fatalf("failed-write lines = %v, want one naming code and CAPTURE-1, and the sweep that repairs it", lines)
	}
}

// Review Focus 1: an approve at H1 after a push made H2 writes on H1 only, and H2 has no
// legion-verdict: no write is asked for on H2, which holds no verdict.
func TestLegionVerdictAtAnOldHeadWritesNothingOnTheNewHead(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
	rig.apply(t, intake.PullRequestSynchronized{Repo: "acme/widgets", Number: 86, Branch: "legion/CAPTURE-1", Before: "code", HeadSHA: "code-2"})
	rig.apply(t, reviewerVerdict("approve", "code", 1))
	rig.written(t)
	if runs := rig.github.legionRuns("code"); len(runs) != 1 || runs[0].Conclusion != "success" {
		t.Fatalf("legion-verdict runs on the approved head = %+v, want one concluding success", runs)
	}
	if runs := rig.github.legionRuns("code-2"); len(runs) != 0 {
		t.Fatalf("legion-verdict runs on the new head = %+v, want none", runs)
	}
	if asked := rig.askedHeads(); !slices.Equal(asked, []string{"code"}) {
		t.Fatalf("the workflow asked for writes on %v, want only the approve's on code", asked)
	}
}

// Review Focus 2: a person's request for changes after the writer's success turns the writer's own
// run to failure: the same run, updated, and no second run.
func TestLegionVerdictPersonsLateRefusalTurnsSuccessToFailure(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
	run := rig.approvedAndWritten(t)
	rig.review(t, personsReview(1, "changes_requested"))
	rig.written(t)
	runs := rig.github.legionRuns("code")
	if len(runs) != 1 || runs[0].ID != run.ID || runs[0].Conclusion != "failure" {
		t.Fatalf("legion-verdict runs on code = %+v, want run %d alone, updated to failure", runs, run.ID)
	}
	writes, _ := rig.github.recorded()
	if last := writes[len(writes)-1]; last.Method != http.MethodPatch || last.Path != fmt.Sprintf("/check-runs/%d", run.ID) || last.Bearer != verdictBearer() {
		t.Fatalf("the refusal's write = %+v, want a PATCH of run %d with the verdict token", last, run.ID)
	}
}

// A success the writer wrote does not stand once the verdict at that head no longer does: when
// the writer decides the head again and the record holds no verdict there, it writes failure over
// its own run, so the head cannot merge on a success nothing backs (R4: a commit with no verdict
// cannot merge). Here the tester's next run clears its pass at code (workflow's clearHandoff), and
// a person's approval, which is no verdict on a Legion head, has the writer decide code again.
func TestLegionVerdictASuccessWhoseVerdictNoLongerStandsTurnsToFailure(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
	run := rig.approvedAndWritten(t)
	if _, err := rig.pool.Exec(context.Background(), `update phases set verdict = '', handoff_commit = '' where issue = 'CAPTURE-1' and role = 'tester'`); err != nil {
		t.Fatalf("clear the tester's pass: %v", err)
	}
	rig.review(t, personsReview(1, "approved"))
	rig.written(t)
	if runs := rig.github.legionRuns("code"); len(runs) != 1 || runs[0].ID != run.ID || runs[0].Conclusion != "failure" {
		t.Fatalf("legion-verdict runs on code = %+v, want run %d turned to failure", runs, run.ID)
	}
	if recorded := verdictWrites(t, rig.pool, "code"); len(recorded) != 2 || recorded[1].Conclusion != store.VerdictConclusionFailure {
		t.Fatalf("the store records %+v, want the success and then the failure over it", recorded)
	}
}

// C2: a person's request for changes at H refuses a Legion head the reviewer approved, and a
// later approval of H by the same person lifts that refusal, so the reviewer's approve writes
// success again.
func TestLegionVerdictPersonsApprovalLiftsTheirRefusal(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
	run := rig.approvedAndWritten(t)
	rig.review(t, personsReview(1, "changes_requested"))
	rig.written(t)
	rig.review(t, personsReview(2, "approved"))
	rig.written(t)
	if runs := rig.github.legionRuns("code"); len(runs) != 1 || runs[0].ID != run.ID || runs[0].Conclusion != "success" {
		t.Fatalf("legion-verdict runs on code = %+v, want run %d back at success", runs, run.ID)
	}
	if _, events := rig.github.recorded(); !slices.Contains(events, fmt.Sprintf("updated run %d on code: failure", run.ID)) {
		t.Fatalf("GitHub saw %v, want the refusal's failure before the success", events)
	}
}

// A person's approval delivered after their later request for changes lifts nothing: intake can
// apply a review late (a nak, a redelivery), so each author's last review is the one GitHub
// submitted last (record.ReviewOrder), not the one the daemon recorded last, and the run the
// refusal turned to failure stays at failure.
func TestLegionVerdictAnApprovalDeliveredLateLiftsNoLaterRefusal(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
	run := rig.approvedAndWritten(t)
	rig.review(t, personsReview(2, "changes_requested"))
	rig.written(t)
	rig.review(t, personsReview(1, "approved"))
	rig.written(t)
	if runs := rig.github.legionRuns("code"); len(runs) != 1 || runs[0].ID != run.ID || runs[0].Conclusion != "failure" {
		t.Fatalf("legion-verdict runs on code = %+v, want run %d left at failure", runs, run.ID)
	}
}

// C2: on a Legion head with the tester's pass and no reviewer verdict, a person's approval at H
// writes nothing: only the reviewer's verdict approves a head a Legion issue owns.
func TestLegionVerdictPersonsApprovalWritesNothingOnALegionHead(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
	rig.review(t, personsReview(1, "approved"))
	rig.written(t)
	if writes, _ := rig.github.recorded(); len(writes) != 0 {
		t.Fatalf("writes = %+v, want none", writes)
	}
}

// The outside pull request 90 of acme/widgets, on a branch no issue owns, at head out-1.
var outsideOpened = intake.OutsidePullRequest{Repo: "acme/widgets", Number: 90, Branch: "feature/faster-builds", HeadSHA: "out-1"}

// outsideReview is a webhook's review of pull request 90 at out-1, as GitHub delivers it: its
// author's write access is the daemon's to read (verdictRig.review).
func outsideReview(id int64, author, state string) intake.OutsideReview {
	return intake.OutsideReview{PullRequestReview: intake.PullRequestReview{Repo: "acme/widgets", Number: 90, ID: id, State: state,
		CommitID: "out-1", HeadSHA: "out-1", Author: author, SubmittedAt: time.Date(2026, 11, 21, 6, 0, int(id), 0, time.UTC)}}
}

// nothingSeeded seeds no issue.
func nothingSeeded(*testing.T, *store.Pool) {}

// C2: on a head no Legion issue owns, of a verdict repository whose entry names a ci_reviewer, the
// CI reviewer's APPROVED at H writes success and its CHANGES_REQUESTED failure, and a person's
// CHANGES_REQUESTED after the CI reviewer's approval writes failure. A review by an account of type
// Bot that is not the CI reviewer writes nothing, and an approval by an account of type User with
// write access writes success.
func TestLegionVerdictOnAnOutsideHead(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reviews []intake.OutsideReview
		want    []string
	}{
		{"the CI reviewer approves", []intake.OutsideReview{outsideReview(1, ciReviewer, "approved")}, []string{"success"}},
		{"the CI reviewer asks for changes", []intake.OutsideReview{outsideReview(1, ciReviewer, "changes_requested")}, []string{"failure"}},
		{"a person asks for changes after the CI reviewer's approval", []intake.OutsideReview{
			outsideReview(1, ciReviewer, "approved"), outsideReview(2, "a-writer", "changes_requested")}, []string{"success", "failure"}},
		{"a bot that is not the CI reviewer", []intake.OutsideReview{outsideReview(1, "another-bot[bot]", "approved")}, nil},
		{"a person approves", []intake.OutsideReview{outsideReview(1, "a-writer", "approved")}, []string{"success"}},
		{"an account with no write access approves", []intake.OutsideReview{outsideReview(1, "a-reader", "approved")}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, nothingSeeded)
			rig.apply(t, outsideOpened)
			var concluded []string
			for _, review := range tc.reviews {
				rig.review(t, review)
				rig.written(t)
				if runs := rig.github.legionRuns("out-1"); len(runs) == 1 {
					concluded = append(concluded, runs[0].Conclusion)
				} else if len(runs) > 1 {
					t.Fatalf("legion-verdict runs on out-1 = %+v, want one at most", runs)
				}
			}
			if !slices.Equal(slices.Compact(concluded), tc.want) {
				t.Fatalf("legion-verdict on out-1 concluded %v after each review, want %v", concluded, tc.want)
			}
		})
	}
}

// C2: a pull request on a branch that is no issue's branch is recorded with its head and its
// reviews.
func TestLegionVerdictRecordsAnOutsidePullRequestWithItsHeadAndReviews(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, nothingSeeded)
	rig.apply(t, outsideOpened)
	rig.review(t, outsideReview(1, "a-writer", "approved"))
	rig.written(t)
	widgets := ghrepo.MustParse("acme/widgets")
	if err := pgx.BeginFunc(context.Background(), rig.pool, func(tx pgx.Tx) error {
		pr, found, err := store.OutsidePullRequestTx(context.Background(), tx, widgets, 90)
		if err != nil {
			return err
		}
		if !found || pr.Head != "out-1" {
			t.Errorf("the outside pull request 90 reads %+v (found %v), want it at out-1", pr, found)
		}
		reviews, err := store.VerdictReviewsTx(context.Background(), tx, widgets, 90)
		if err != nil {
			return err
		}
		if len(reviews) != 1 || reviews[0].Head != "out-1" || reviews[0].Author != "a-writer" || !reviews[0].AuthorCanWrite ||
			reviews[0].State != "approved" || reviews[0].GitHubID != 1 {
			t.Errorf("the reviews of pull request 90 read %+v, want the person's approval at out-1", reviews)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// otherProjectsLine is what a daemon logs, once per pull request, of a legion/<KEY> head of
// another project on one of its verdict repositories (Review Focus 5).
const otherProjectsLine = "verdict: no daemon writes legion-verdict here: the head is another project's issue branch"

// Review Focus 5 and C2: a legion/<KEY> head of another project on a verdict repository gets no
// write from this daemon, whose project is not KEY's, and one line once per pull request says why:
// the daemon of KEY's project writes it, on a repository that daemon names. A review of that
// pull request writes nothing either, and is not recorded: it is that project's daemon's to read.
func TestLegionVerdictAnotherProjectsIssueHeadIsLoggedNotWritten(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, nothingSeeded)
	rig.apply(t, intake.PullRequestOpened{Repo: "acme/widgets", Number: 91, Branch: "legion/OTHER-1", HeadSHA: "other-1", Base: "main"})
	rig.apply(t, intake.PullRequestSynchronized{Repo: "acme/widgets", Number: 91, Branch: "legion/OTHER-1", Before: "other-1", HeadSHA: "other-2"})
	review := intake.OutsideReview{PullRequestReview: intake.PullRequestReview{Repo: "acme/widgets", Number: 91, ID: 5, State: "approved",
		CommitID: "other-2", HeadSHA: "other-2", Author: "a-writer"}}
	rig.review(t, review)
	rig.written(t)
	if writes, _ := rig.github.recorded(); len(writes) != 0 {
		t.Fatalf("writes = %+v, want none on another project's issue branch", writes)
	}
	if reviews := verdictReviews(t, rig.pool, 91); len(reviews) != 0 {
		t.Fatalf("the reviews recorded of pull request 91 = %+v, want none of another project's issue branch", reviews)
	}
	lines := loggedMessages(t, rig.logs.String(), otherProjectsLine)
	if len(lines) != 1 || lines[0]["repository"] != "acme/widgets" || lines[0]["pull_request"] != float64(91) || lines[0]["issue"] != "OTHER-1" {
		t.Fatalf("lines = %v, want one naming the pull request and the other project's issue", lines)
	}
}

// C2: a daemon writes on no repository its verdict_repositories does not name: a Legion head's
// approve there settles its round as before, and nothing is written.
func TestLegionVerdictWritesOnNoRepositoryItsEntriesDoNotName(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, nil, func(t *testing.T, pool *store.Pool) {
		seedCapture(t, pool, phase.Reviewing, "needs_review", testerPassAtCode, reviewerWithout)
	})
	rig.apply(t, reviewerVerdict("approve", "code", 1))
	rig.apply(t, outsideOpened)
	rig.review(t, outsideReview(1, "a-writer", "approved"))
	rig.written(t)
	if got, _ := capturePhase(t, rig.pool); got != phase.Accepting {
		t.Fatalf("the issue is in %s, want accepting on the clean merge state its record holds", got)
	}
	if writes, _ := rig.github.recorded(); len(writes) != 0 {
		t.Fatalf("writes = %+v, want none on a repository no entry names", writes)
	}
}

// tokenModeTokens mints as roleTokens does, and answers the verdict token as a daemon in token
// mode does (appauth.ErrNoVerdictToken).
type tokenModeTokens struct{ roleTokens }

func (tokenModeTokens) VerdictToken(context.Context, string) (appauth.Lease, error) {
	return appauth.Lease{}, appauth.ErrNoVerdictToken
}

// tokenModeLine is what the writer logs, once, when the App is in token mode.
const tokenModeLine = "verdict: legion-verdict is not written: the App is in token mode, which mints no verdict token"

// A daemon in token mode mints no verdict token (appauth.ErrNoVerdictToken), so it writes nothing
// and says why once, however many verdicts it holds.
func TestLegionVerdictTokenModeWritesNothingAndSaysWhyOnce(t *testing.T) {
	rig := newVerdictRig(t, tokenModeTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
	rig.apply(t, reviewerVerdict("changes", "code", 1))
	rig.written(t)
	rig.apply(t, outsideOpened)
	rig.review(t, outsideReview(1, ciReviewer, "approved"))
	rig.written(t)
	if writes, _ := rig.github.recorded(); len(writes) != 0 {
		t.Fatalf("writes = %+v, want none in token mode", writes)
	}
	if lines := loggedMessages(t, rig.logs.String(), tokenModeLine); len(lines) != 1 {
		t.Fatalf("token-mode lines = %v, want one", lines)
	}
}

// R19: a legion-verdict check run and a commit status of the one App at a head, which the daemon's
// record does not hold, get the daemon's own conclusion over them: the run updated to it, and a
// status of the daemon's own with the same state set over the status. Each write is in the record
// and in a legion_verdict_write line naming the foreign id. A run of another App is left alone.
func TestLegionVerdictWritesItsConclusionOverAForeignRunAndStatus(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
	rig.github.mu.Lock()
	rig.github.runs = append(rig.github.runs,
		fakeRun{ID: 500, Head: "code", Name: verdict.CheckName, Conclusion: "success", App: verdictAppSlug},
		fakeRun{ID: 501, Head: "code", Name: verdict.CheckName, Conclusion: "success", App: "github-actions"})
	rig.github.statuses = append(rig.github.statuses, fakeStatus{ID: 600, Head: "code", Context: verdict.CheckName, State: "success", Creator: verdictAppLogin})
	rig.github.mu.Unlock()
	rig.apply(t, reviewerVerdict("changes", "code", 1))
	rig.written(t)
	runs := rig.github.legionRuns("code")
	if len(runs) != 2 || runs[0].ID != 500 || runs[0].Conclusion != "failure" || runs[1].ID != 501 || runs[1].Conclusion != "success" {
		t.Fatalf("legion-verdict runs on code = %+v, want the App's foreign run 500 at failure, another App's left alone, and no new run", runs)
	}
	rig.github.mu.Lock()
	statuses := slices.Clone(rig.github.statuses)
	rig.github.mu.Unlock()
	if len(statuses) != 2 || statuses[1].State != "failure" || statuses[1].Context != verdict.CheckName {
		t.Fatalf("statuses on code = %+v, want a newer legion-verdict status of the daemon's own concluding failure", statuses)
	}
	recorded := verdictWrites(t, rig.pool, "code")
	if len(recorded) != 2 || recorded[0].Kind != store.VerdictWriteCheckRun || recorded[0].GitHubID != 500 || recorded[0].ForeignID != 500 ||
		recorded[1].Kind != store.VerdictWriteStatus || recorded[1].GitHubID != statuses[1].ID || recorded[1].ForeignID != 600 {
		t.Fatalf("the store records %+v, want the run written over 500 and the status written over 600", recorded)
	}
	lines := rig.writeLines(t)
	if len(lines) != 2 || lines[0]["foreign_id"] != float64(500) || lines[1]["foreign_id"] != float64(600) || lines[1]["kind"] != "status" {
		t.Fatalf("legion_verdict_write lines = %v, want one per write, each naming its foreign id", lines)
	}
}

// C2's fake answers a merge-state read that waits on the write: queueVerdictWrite holds the
// issue's reads until the write ends (verdictWriter.await), so a read asked for on the same commit
// as the write is made after it.
func TestLegionVerdictHoldsTheIssuesMergeStateReadUntilItsWriteEnds(t *testing.T) {
	writer := newVerdictWriter([]config.VerdictRepository{widgetsEntry})
	w := &workflowRuntime{verdicts: writer, log: slog.New(slog.DiscardHandler)}
	w.queueVerdictWrite("CAPTURE-1", "acme/widgets", 86, "code")
	read := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	go func() {
		ended, err := writer.await(ctx, "CAPTURE-1")
		if err == nil && !ended {
			err = errors.New("the bound passed")
		}
		read <- err
	}()
	select {
	case err := <-read:
		t.Fatalf("the read went on (%v) while the issue's write was pending", err)
	case <-time.After(50 * time.Millisecond):
	}
	if ended, err := writer.await(ctx, "CAPTURE-2"); err != nil || !ended {
		t.Fatalf("another issue's read waited: %v, %v", ended, err)
	}
	target, ok := writer.next()
	if !ok || target.head != "code" || target.number != 86 {
		t.Fatalf("the writer took %+v, %v; want the queued write of code", target, ok)
	}
	writer.done(target)
	select {
	case err := <-read:
		if err != nil {
			t.Fatalf("the read after the write ended: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the read still waits after the write ended")
	}
	if ended, err := writer.await(canceledContext(), "CAPTURE-1"); err != nil || !ended {
		t.Fatalf("a read of an issue with no write pending waited: %v, %v", ended, err)
	}
}

// A merge-state read waits on its issue's write of legion-verdict for one bound at most (the
// writer's bound, one issueRead): a write held up by a slow GitHub or a long queue holds the
// issue's reads no longer than that, and the read then goes on and says so once.
func TestLegionVerdictAMergeStateReadWaitsOnItsWriteForOneBoundAtMost(t *testing.T) {
	writer := newVerdictWriter([]config.VerdictRepository{widgetsEntry})
	writer.bound = 50 * time.Millisecond
	logs := &syncBuffer{}
	w := &workflowRuntime{pool: isolatedOutboxPool(t), records: projectRecords{Store: record.NewStore(), project: "CAPTURE"},
		verdicts: writer, log: slog.New(slog.NewJSONHandler(logs, nil))}
	// The write is queued and never taken, as a write that never ends.
	w.queueVerdictWrite("CAPTURE-1", "acme/widgets", 86, "code")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := w.readMergeState(ctx, "CAPTURE-1", ""); err != nil {
		t.Fatalf("the read behind a write that never ends: %v, want it to go on after the bound", err)
	}
	lines := loggedMessages(t, logs.String(), verdictReadWentOn)
	if len(lines) != 1 || lines[0]["issue"] != "CAPTURE-1" {
		t.Fatalf("%q lines = %v, want one naming CAPTURE-1", verdictReadWentOn, lines)
	}
}

// A write asked for on a repository no verdict entry names, under the name a record keeps or the
// one GitHub serves it under now, is queued for nothing: it would write nothing, and the issue's
// merge-state reads would wait on it. One the entries name under its serving name is queued.
func TestLegionVerdictQueuesNoWriteOnARepositoryItsEntriesDoNotName(t *testing.T) {
	served := newRepositories("CAPTURE", transferredCapture)
	served.fill(ghrepo.MustParse("cybermonklabs/widgets"))
	writer := newVerdictWriter([]config.VerdictRepository{{Repository: ghrepo.MustParse("cybermonklabs/widgets")}})
	w := &workflowRuntime{verdicts: writer, repositories: served, log: slog.New(slog.DiscardHandler)}
	w.queueVerdictWrite("CAPTURE-1", "acme/gadgets", 86, "code")
	if !writer.idle() {
		t.Fatal("a write on acme/gadgets, which no entry names, is queued; want none")
	}
	if ended, err := writer.await(canceledContext(), "CAPTURE-1"); err != nil || !ended {
		t.Fatalf("CAPTURE-1's read waits (%v, %v) on a write on a repository no entry names", ended, err)
	}
	w.queueVerdictWrite("CAPTURE-1", "acme/widgets", 86, "code")
	if target, ok := writer.next(); !ok || target.repository.String() != "acme/widgets" || target.head != "code" {
		t.Fatalf("the writer took %+v, %v; want the write on acme/widgets, which GitHub serves as the entry's cybermonklabs/widgets", target, ok)
	}
}

// A write asked for on a pull request of a verdict repository that no record holds - one opened
// before the daemon heard its repository, whose first event here is a review - reads that pull
// request from GitHub once (GET /pulls/{number}, decideVerdict). On a branch that is no issue's it
// is recorded as an outside pull request at the head GitHub shows and decided as one; on another
// project's issue branch it is that project's, logged once and not written; on this project's
// issue branch that no record holds it has no verdict here; and one GitHub shows closed merges
// nothing, so it is neither recorded nor written on, as a closed Legion pull request is not.
func TestLegionVerdictReadsAPullRequestNoRecordHoldsFromGitHub(t *testing.T) {
	pull := func(state, ref string) string {
		return fmt.Sprintf(`{"number":92,"state":%q,"base":{"ref":"main"},"head":{"ref":%q,"sha":"out-9"}}`, state, ref)
	}
	for _, tc := range []struct {
		name, pull string
		want       []string
		recorded   bool
		logged     bool
	}{
		{name: "a branch that is no issue's", pull: pull("open", "feature/faster-tests"), want: []string{"success"}, recorded: true},
		{name: "another project's issue branch", pull: pull("open", "legion/OTHER-2"), logged: true},
		{name: "this project's issue branch, which no record holds", pull: pull("open", "legion/CAPTURE-9")},
		{name: "a pull request GitHub shows closed", pull: pull("closed", "feature/faster-tests")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, nothingSeeded)
			rig.github.mu.Lock()
			rig.github.pulls[92] = tc.pull
			rig.github.mu.Unlock()
			rig.review(t, intake.OutsideReview{PullRequestReview: intake.PullRequestReview{Repo: "acme/widgets", Number: 92, ID: 9, State: "approved",
				CommitID: "out-9", HeadSHA: "out-9", Author: "a-writer", SubmittedAt: time.Date(2026, 11, 21, 6, 0, 9, 0, time.UTC)}})
			rig.written(t)
			var concluded []string
			for _, run := range rig.github.legionRuns("out-9") {
				concluded = append(concluded, run.Conclusion)
			}
			if !slices.Equal(concluded, tc.want) {
				t.Fatalf("legion-verdict on out-9 concluded %v, want %v", concluded, tc.want)
			}
			var sighted store.OutsidePullRequest
			var found bool
			if err := pgx.BeginFunc(context.Background(), rig.pool, func(tx pgx.Tx) error {
				var err error
				sighted, found, err = store.OutsidePullRequestTx(context.Background(), tx, ghrepo.MustParse("acme/widgets"), 92)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if found != tc.recorded || found && sighted.Head != "out-9" {
				t.Fatalf("the outside pull request 92 reads %+v (found %v), want found %v at out-9", sighted, found, tc.recorded)
			}
			if lines := loggedMessages(t, rig.logs.String(), otherProjectsLine); tc.logged != (len(lines) == 1 && lines[0]["issue"] == "OTHER-2") || len(lines) > 1 {
				t.Fatalf("lines = %v, want one naming OTHER-2: %v", lines, tc.logged)
			}
		})
	}
}

// C2's tester half: the tester's verdict changes a Legion head's verdict, so its completion asks
// for a write at the commit it tested (workflow's askMergeState), and a fail there writes failure.
func TestLegionVerdictTheTestersFailWritesFailureOnTheCommitItTested(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, func(t *testing.T, pool *store.Pool) {
		seedCapture(t, pool, phase.Testing, "testing", record.PhaseRow{Issue: "CAPTURE-1", Role: claim.RoleTester, Claim: "test-claim"})
	})
	rig.apply(t, intake.HandoffComplete{Generation: 1, Issue: "CAPTURE-1", Role: claim.RoleTester, Claim: "test-claim", Summary: "tested",
		Verdict: "fail", Commit: "code", HandoffRecord: "test-1", HandoffVersion: 1})
	rig.written(t)
	if asked := rig.askedHeads(); !slices.Equal(asked, []string{"code"}) {
		t.Fatalf("the workflow asked for writes on %v, want one on code, the commit the tester tested", asked)
	}
	if runs := rig.github.legionRuns("code"); len(runs) != 1 || runs[0].Conclusion != "failure" {
		t.Fatalf("legion-verdict runs on code = %+v, want one concluding failure", runs)
	}
}

// R19's status half, over a status the daemon holds: once the daemon's own status stands at a head,
// a verdict that turns the head's conclusion sets a status of the daemon's own at the new state over
// it, since GitHub reads the newest status of each context, with no foreign id.
func TestLegionVerdictSetsItsOwnStatusAgainWhenTheConclusionTurns(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
	rig.github.mu.Lock()
	rig.github.statuses = append(rig.github.statuses, fakeStatus{ID: 600, Head: "code", Context: verdict.CheckName, State: "failure", Creator: verdictAppLogin})
	rig.github.mu.Unlock()
	rig.approvedAndWritten(t)
	rig.review(t, personsReview(1, "changes_requested"))
	rig.written(t)
	rig.github.mu.Lock()
	statuses := slices.Clone(rig.github.statuses)
	rig.github.mu.Unlock()
	var states []string
	for _, status := range statuses {
		states = append(states, status.State)
	}
	if !slices.Equal(states, []string{"failure", "success", "failure"}) {
		t.Fatalf("the legion-verdict statuses on code, oldest first, are %v; want the foreign failure, the daemon's success over it, then the daemon's failure", states)
	}
	lines := rig.writeLines(t)
	if last := lines[len(lines)-1]; last["kind"] != "status" || last["conclusion"] != "failure" || last["foreign_id"] != float64(0) {
		t.Fatalf("the last legion_verdict_write line = %v, want the daemon's own status at failure, over no foreign one", last)
	}
}

// A review names the commit it read (commit_id), and that is the one head it is a verdict at, as
// the workflow's refusal holds it (workflow's review): a review that names none is recorded at no
// head and asks for no write, rather than at the head the pull request stood at when GitHub sent it.
func TestLegionVerdictAReviewNamingNoCommitIsNoVerdict(t *testing.T) {
	rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, nothingSeeded)
	rig.apply(t, outsideOpened)
	review := outsideReview(1, "a-writer", "approved")
	review.CommitID = ""
	rig.review(t, review)
	rig.written(t)
	if runs := rig.github.legionRuns("out-1"); len(runs) != 0 {
		t.Fatalf("legion-verdict runs on out-1 = %+v, want none from a review that names no commit", runs)
	}
	if reviews := verdictReviews(t, rig.pool, 90); len(reviews) != 0 {
		t.Fatalf("the reviews recorded of pull request 90 = %+v, want none", reviews)
	}
}

// canceledContext is a context already ended.
func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// The repositories GitHub intake hears include every verdict repository, so the events of one the
// mapping does not name reach the writer.
func TestLegionVerdictRepositoriesAreHeard(t *testing.T) {
	pool := isolatedOutboxPool(t)
	w := &workflowRuntime{pool: pool, records: projectRecords{Store: record.NewStore(), project: "CAPTURE"}, repositories: servedAcme(),
		verdicts: newVerdictWriter([]config.VerdictRepository{widgetsEntry, {Repository: ghrepo.MustParse("acme/Verdict-Scratch")}})}
	heard, err := w.heardAtBoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, repository := range heard {
		names = append(names, repository.String())
	}
	if want := []string{"acme/Verdict-Scratch", "acme/widgets"}; !slices.Equal(names, want) {
		t.Fatalf("heard = %v, want %v: the served repository and the verdict repository the mapping does not name", names, want)
	}
}
