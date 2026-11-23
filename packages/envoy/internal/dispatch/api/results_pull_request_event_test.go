package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/auth"
	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/githubtest"
	"github.com/sjawhar/envoy/internal/testnats"
)

// headAfterEventBound is how long after GitHub's `pull_request` event the results route may take
// to compare a check with the head that event reports: v-head-no-settings says "within 5 seconds
// of the event".
const headAfterEventBound = 5 * time.Second

// The pull request a `before_merge` check compares its results with is the one GitHub's own
// event reports, in a deployment that holds no delivery settings.
//
// The case is a pull request opened after its issue's results were recorded: not a draft, in the
// issue's own repository, which is not the project's default repository, with a branch and a body
// that name the issue. Nobody links it to the issue by hand.
// Envoy relays GitHub's `pull_request` event for it, as it does for every repository the App's
// webhook covers, and the delivery intake is the one writer that turns that event into the head
// row the results read compares with. A deployment that never configured the delivery timeline
// holds no delivery settings, and the head of every pull request is still the basis of every
// `before_merge` result there, so the intake must write it all the same.
//
// GitHub's commit-to-pull-request answer names nothing here, at the record and at every read,
// which is what the results read's own read-through got for the commit in the deployment this
// issue was found in: so the event is the only way the head can reach Dispatch, and the test
// proves that way and no other. The intake takes from the event which pull request changed and
// reads that pull request from GitHub, so GitHub's single pull request read answers #7 as GitHub
// holds it.
func TestAPullRequestEventGivesTheResultsReadItsHeadWithNoDeliverySettings(t *testing.T) {
	requireTheEventsHeadWithinTheBound(t, 0)
}

// The same within the same 5 seconds, with four envelopes no row can hold published just before
// GitHub's event: numbers 3000000000 and up, which do not fit the integer columns. The intake
// takes at most four envelopes at a time, so before cybermonklabs/legion#4209's round 3 these four
// were handed back for ever and held every slot, and GitHub's event behind them never reached the
// route (acceptance round 3, defect 5: 420 s and no head). Each is now final at its first
// delivery, so the event behind them is taken at once.
func TestAPullRequestEventBehindFourEnvelopesNoRowCanHoldGivesTheResultsReadItsHead(t *testing.T) {
	requireTheEventsHeadWithinTheBound(t, intakeSlots)
}

// intakeSlots is how many envelopes the delivery intake holds at a time
// (delivery.intakeMaxAckPending).
const intakeSlots = 4

// requireTheEventsHeadWithinTheBound publishes poisonBefore envelopes no row can hold, then
// GitHub's own event of example/tools#7, and fails unless the results route compares check k1
// with #7's head within headAfterEventBound of that event's publish.
func requireTheEventsHeadWithinTheBound(t *testing.T, poisonBefore int) {
	t.Helper()
	handler, database, served, fake := readThroughGitHub(t, "example/tools")
	if response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/projects",
		map[string]string{"key": "ACME", "name": "Acme"}, "alice"); response.Code != http.StatusCreated {
		t.Fatalf("create project ACME: status=%d body=%s", response.Code, response.Body.String())
	}
	// The project's default is example/default, so the issue reaches example/tools by its own
	// repository alone: an issue resolves to its own repository, else to its project's default
	// (refs.IssueRepoResolved).
	setDefaultRepository(t, handler, "ACME", "example/default")
	created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues",
		map[string]any{"project": "ACME", "title": "The tools change", "spec": "A spec\n", "repo": "example/tools"}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("create the issue in example/tools: status=%d body=%s", created.Code, created.Body.String())
	}
	issue := decodeBody[model.Issue](t, created)
	if issue.ResolvedRepo == nil || *issue.ResolvedRepo != "example/tools" {
		t.Fatalf("the issue resolves to %v, want its own example/tools, which is not its project's default", issue.ResolvedRepo)
	}
	uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "verification.md", twoChecks)
	recorded := decodeBody[recordedResult](t, recordResult(t, handler, issue.Key, resultBody("k1", "pass", headCommit, nil)))
	if _, err := delivery.GetSettings(context.Background(), database.Pool); err == nil {
		t.Fatal("the deployment holds delivery settings, want none: the case is a deployment that never configured the delivery timeline")
	}

	_, natsURL := testnats.Start(t)
	publisher, err := bus.ConnectOwningStream([]string{natsURL})
	if err != nil {
		t.Fatalf("connect the publisher: %v", err)
	}
	t.Cleanup(publisher.Close)
	callers, err := database.Pool.Leases()
	if err != nil {
		t.Fatalf("open the lease pool: %v", err)
	}
	leases, err := delivery.ReserveIntakeLeases(callers)
	if err != nil {
		t.Fatalf("reserve the intakes' callers of the lease pool: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	stopIntakes, err := delivery.StartIntakes(ctx, func(name string) (*bus.Client, error) {
		return bus.Connect([]string{natsURL}, bus.WithName(name))
	}, database.Pool, leases, served.deps.GitHub, served.deps.Events, nil, nil, served.deps.Attributor)
	if err != nil {
		cancel()
		t.Fatalf("start the intakes: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		stopIntakes()
	})

	updated := time.Now().UTC().Format(time.RFC3339)
	fake.pullAnswers(func(repo, number string) string {
		if repo != "example/tools" || number != "7" {
			return ""
		}
		return fmt.Sprintf(`{"number":7,"title":%q,"body":%q,"state":"open","merged":false,
			"html_url":"https://github.com/example/tools/pull/7","updated_at":%q,
			"head":{"ref":%q,"sha":%q,"repo":{"full_name":"example/tools"}},"base":{"ref":"main"}}`,
			"The tools change ("+issue.Key+")", "Dispatch: "+issue.Key+"\n\nThe change.", updated,
			"legion/"+issue.Key, headCommit)
	})
	payload, err := json.Marshal(map[string]string{
		"kind": "pr", "action": "opened", "repo": "example/tools", "number": "7",
		"title":    "The tools change (" + issue.Key + ")",
		"body":     "Dispatch: " + issue.Key + "\n\nThe change.",
		"url":      "https://github.com/example/tools/pull/7",
		"author":   "implementer[bot]",
		"head_sha": headCommit, "head_ref": "legion/" + issue.Key, "head_repo": "example/tools",
		"base_ref": "main", "merged": "false", "updated_at": updated,
	})
	if err != nil {
		t.Fatalf("encode the pull request's payload: %v", err)
	}
	for i := range poisonBefore {
		number := fmt.Sprint(3_000_000_000 + i)
		poison, err := json.Marshal(map[string]string{
			"kind": "pr", "action": "opened", "repo": "example/tools", "number": number,
			"head_sha": headCommit, "head_ref": "feature/x", "base_ref": "main", "merged": "false", "updated_at": updated,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := publisher.Publish(contracts.Envelope{
			EventID: "example-tools-pr-" + number, Source: "github", SourceEventID: "example-tools-pr-" + number,
			TraceID: "example-tools-pr-" + number, Topic: contracts.GithubResourceSubject("example", "tools", "pr", number),
			Payload: string(poison), IssuedAt: time.Now().Unix(),
		}); err != nil {
			t.Fatalf("publish the envelope of #%s: %v", number, err)
		}
	}
	// The bound is v-head-no-settings' own: the results route compares a check with the head of
	// an open pull request within 5 seconds of GitHub's event, measured from the publish.
	published := time.Now()
	if err := publisher.Publish(contracts.Envelope{
		EventID: "example-tools-pr-7-opened", Source: "github", SourceEventID: "example-tools-pr-7-opened",
		TraceID: "example-tools-pr-7-opened", Topic: contracts.GithubResourceSubject("example", "tools", "pr", "7"),
		Payload: string(payload), IssuedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("publish the pull request's opening: %v", err)
	}

	deadline := published.Add(headAfterEventBound)
	read := readResults(t, handler, issue.Key)
	for len(checkOf(t, read, "k1").ComparedWith) == 0 {
		if time.Now().After(deadline) {
			check := checkOf(t, read, "k1")
			t.Fatalf("%s after the pull request's event, check k1 compares with %+v for the reason %q and "+
				"the delivery durable is %s; want the head of example/tools#7, which the event reported",
				time.Since(published).Round(time.Millisecond), check.ComparedWith, check.ComparedNothing, durableState(publisher))
		}
		time.Sleep(100 * time.Millisecond)
		read = readResults(t, handler, issue.Key)
	}
	// The answer accepted is held to the bound too, and not only the answers that had no basis:
	// a route that answers right but late passes no bound.
	took := time.Since(published)
	if took > headAfterEventBound {
		t.Fatalf("the results route compared k1 with the event's head %s after the publish, past the %s of v-head-no-settings",
			took.Round(time.Millisecond), headAfterEventBound)
	}
	t.Logf("the results route compared k1 with the event's head %s after the publish", took.Round(time.Millisecond))
	stands := standsAt(t, read, "k1", recorded.Result.ID,
		"a result at the head of the pull request the event reported", model.CommitAtBasis, false)
	if stands.Basis == nil || stands.Basis.PullRequest == nil || *stands.Basis.PullRequest != 7 {
		t.Fatalf("the result stands at %+v, want the head of pull request 7", stands.Basis)
	}
	if stood := checkOf(t, read, "k1").PassStands; stood.Answer != model.PassStandsYes {
		t.Fatalf("the check answers pass_stands %+v, want yes", stood)
	}
	if asked := fake.asks("example/tools"); asked > 2 {
		t.Fatalf("GitHub was asked %d times for the commit's pull requests, want the record's read-through and at most one read's: the head came from the event", asked)
	}
}

// durableState says what the NATS server holds of the delivery intake's durable, for the
// failure above: absent, unbound or bound, so a red run names whether the intake ever took the
// event at all.
func durableState(client *bus.Client) string {
	info, err := client.JS().ConsumerInfo(bus.Stream, bus.DispatchDeliveryDurable)
	if err != nil {
		return fmt.Sprintf("not on the server (%v)", err)
	}
	return fmt.Sprintf("bound=%v, delivered through stream sequence %d, acknowledged through %d",
		info.PushBound, info.Delivered.Stream, info.AckFloor.Stream)
}

// A pull request GitHub closed stops being the head a check CI proves is read at, within the same
// 5 seconds of GitHub's `pull_request` event, in a deployment that holds no delivery settings.
//
// The case is an issue in acme/widgets with one check CI proves and two pull requests Dispatch
// stores open: #5, the issue's own, at headCommit, where CI's run 41 passed, and #9, a negative
// control opened after it on a branch that names the issue, at newHeadCommit, where run 42 was
// cancelled. The read of such a check is at the issue's open head of the highest number
// (delivery.Attributor.NewestPullRequestHead), so while #9 is stored open the check is read at
// #9's cancelled run and holds no result. GitHub then closes #9 without merging it, and its
// `closed` event is the one input that tells Dispatch so: GitHub names no pull request for any
// commit here and the reconcile is not running. The intake reads #9 from GitHub and writes it
// closed, a pull request closed unmerged is no head, and the read moves to #5, where the check
// holds the pass derived from run 41.
//
// The deployment repository's #6070 and #6071 were held that way. GitHub closed both at 20:58Z
// on 2026-11-22. The deployment held no delivery settings, so no intake took their `closed`
// events, and Dispatch kept both stored open, and read the check at #6071's cancelled run, until
// the reconcile read them again at about 21:12:37Z.
//
// Red where the intake binds only under delivery settings, as at main before
// cybermonklabs/legion#4209 (Intake.Run waited for the settings cache to hold settings): the
// event is never taken, #9 stays stored open, and when the bound ends c1 still reads run 42's
// cancel at #9.
func TestAPullRequestClosedEventMovesTheCIReadToTheOpenHeadWithNoDeliverySettings(t *testing.T) {
	runs := newCheckRunsFake()
	runs.set(headCommit, completedRun(41, ciCheckRun, "success"))
	runs.set(newHeadCommit, completedRun(42, ciCheckRun, "cancelled"))
	// GitHub's answers name the issue and the moment GitHub closed #9: both are set below, before
	// anything reads a pull request from GitHub.
	var issueKey, branch, closedAt string
	handler, database, served := closedNegativeControlGitHub(t, runs, func(number string) string {
		switch number {
		case "5":
			return fmt.Sprintf(`{"number":5,"title":%q,"body":"","state":"open","merged":false,"merged_at":null,
				"html_url":"https://github.com/acme/widgets/pull/5","updated_at":"2026-11-22T20:30:00Z",
				"head":{"ref":%q,"sha":%q,"repo":{"full_name":"acme/widgets"}},"base":{"ref":"main"}}`,
				"The change ("+issueKey+")", "legion/"+issueKey, headCommit)
		case "9":
			return fmt.Sprintf(`{"number":9,"title":"A negative control","body":"","state":"closed","merged":false,
				"merged_at":null,"closed_at":%q,"html_url":"https://github.com/acme/widgets/pull/9","updated_at":%q,
				"head":{"ref":%q,"sha":%q,"repo":{"full_name":"acme/widgets"}},"base":{"ref":"main"}}`,
				closedAt, closedAt, branch, newHeadCommit)
		}
		return ""
	})
	issue := issueInRepository(t, handler, "RESCE", "acme/widgets", "A negative control closed")
	issueKey = issue.Key
	// The branch names the issue in lower case, as production's negative-control branches did: a
	// key in a branch is read in either case.
	branch = "implementer/" + strings.ToLower(issue.Key) + "-breaks-b"
	uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "verification.md", ciCheckBlock("c1", ciCheckKept))
	if _, err := delivery.GetSettings(context.Background(), database.Pool); err == nil {
		t.Fatal("the deployment holds delivery settings, want none: production held none")
	}
	writeHead(t, database, "acme/widgets", 5, headCommit, "open", issue.Key)
	writeHead(t, database, "acme/widgets", 9, newHeadCommit, "open", issue.Key)
	if _, err := database.Pool.Exec(context.Background(),
		`update pull_request_heads set head_ref = $1 where repo = 'acme/widgets' and number = 9`, branch); err != nil {
		t.Fatalf("store #9 on its branch: %v", err)
	}
	storedState := func() string {
		var state string
		if err := database.Pool.QueryRow(context.Background(),
			`select state from pull_request_heads where repo = 'acme/widgets' and number = 9`).Scan(&state); err != nil {
			return fmt.Sprintf("unread (%v)", err)
		}
		return state
	}

	// Before the event the read is production's wrong one: #9 is stored open and has the higher
	// number, so c1 is read at #9's cancelled run and holds no result.
	before := checkOf(t, readResults(t, handler, issue.Key), "c1")
	assertCI(t, before, ciCheckRun, model.CIStateNoResult, "cancelled", ciUnconcludedLineOf("c1", ciCheckRun, "cancelled"))
	if len(before.Results) != 0 {
		t.Fatalf("before #9's closed event, c1 holds %+v, want no result: run 42 was cancelled", before.Results)
	}

	_, natsURL := testnats.Start(t)
	publisher, err := bus.ConnectOwningStream([]string{natsURL})
	if err != nil {
		t.Fatalf("connect the publisher: %v", err)
	}
	t.Cleanup(publisher.Close)
	callers, err := database.Pool.Leases()
	if err != nil {
		t.Fatalf("open the lease pool: %v", err)
	}
	leases, err := delivery.ReserveIntakeLeases(callers)
	if err != nil {
		t.Fatalf("reserve the intakes' callers of the lease pool: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	stopIntakes, err := delivery.StartIntakes(ctx, func(name string) (*bus.Client, error) {
		return bus.Connect([]string{natsURL}, bus.WithName(name))
	}, database.Pool, leases, served.deps.GitHub, served.deps.Events, nil, nil, served.deps.Attributor)
	if err != nil {
		cancel()
		t.Fatalf("start the intakes: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		stopIntakes()
	})

	closedAt = time.Now().UTC().Format(time.RFC3339)
	payload, err := json.Marshal(map[string]string{
		"kind": "pr", "action": "closed", "repo": "acme/widgets", "number": "9",
		"title": "A negative control", "body": "",
		"url":      "https://github.com/acme/widgets/pull/9",
		"author":   "implementer[bot]",
		"head_sha": newHeadCommit, "head_ref": branch, "head_repo": "acme/widgets",
		"base_ref": "main", "merged": "false", "updated_at": closedAt,
	})
	if err != nil {
		t.Fatalf("encode the pull request's payload: %v", err)
	}
	// The bound and its measure are requireTheEventsHeadWithinTheBound's: v-head-no-settings' 5
	// seconds, from the publish.
	published := time.Now()
	if err := publisher.Publish(contracts.Envelope{
		EventID: "acme-widgets-pr-9-closed", Source: "github", SourceEventID: "acme-widgets-pr-9-closed",
		TraceID: "acme-widgets-pr-9-closed", Topic: contracts.GithubResourceSubject("acme", "widgets", "pr", "9"),
		Payload: string(payload), IssuedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("publish the pull request's close: %v", err)
	}

	deadline := published.Add(headAfterEventBound)
	atHead := ciVerdictLineOf("c1", ciCheckRun, "pass", headCommit)
	check := checkOf(t, readResults(t, handler, issue.Key), "c1")
	for check.CI == nil || check.CI.Line != atHead {
		if time.Now().After(deadline) {
			reads := "no ci reading"
			if check.CI != nil {
				reads = fmt.Sprintf("case %q (%s)", check.CI.Case, check.CI.Detail)
			}
			t.Fatalf("%s after #9's closed event, c1 reads %s and holds %+v, #9 is stored %s and the delivery durable is %s; "+
				"want CI's pass of run 41 at %s, the head of acme/widgets#5: GitHub closed #9 unmerged, so its head is no head",
				time.Since(published).Round(time.Millisecond), reads, check.Results, storedState(), durableState(publisher), headCommit)
		}
		time.Sleep(100 * time.Millisecond)
		check = checkOf(t, readResults(t, handler, issue.Key), "c1")
	}
	// The answer accepted is held to the bound too, as requireTheEventsHeadWithinTheBound holds it.
	took := time.Since(published)
	if took > headAfterEventBound {
		t.Fatalf("the results route read c1 at #5's head %s after #9's closed event, past the %s of v-head-no-settings",
			took.Round(time.Millisecond), headAfterEventBound)
	}
	t.Logf("the results route read c1 at #5's head %s after #9's closed event, #9 now stored %s", took.Round(time.Millisecond), storedState())
	assertCI(t, check, ciCheckRun, model.CIStatePass, "success", atHead)
	result := assertDerived(t, check, 41, ciCheckRun, "success", "pass", headCommit)
	if basis := result.CommitStands.Basis; basis == nil || basis.PullRequest == nil || *basis.PullRequest != 5 {
		t.Fatalf("the derived result stands at %+v, want the head of #5", basis)
	}
	if stood := check.PassStands; stood.Answer != model.PassStandsYes {
		t.Fatalf("c1 answers pass_stands %+v, want yes", stood)
	}
}

// closedNegativeControlGitHub is a Dispatch with no delivery settings whose GitHub serves
// acme/widgets: the App is installed there and nowhere else, GitHub names no pull request for any
// commit (so no read-through writes a head row), each pull request's commits are an empty page,
// runs lists each commit's check runs, and pull answers GET /repos/{owner}/{repo}/pulls/{number}
// of acme/widgets, "" being GitHub's 404. The case above needs the check runs and the read of one
// pull request both: readThroughGitHub serves no check runs, and resultsGitHubCheckRuns no read of
// a pull request and no server for the intakes.
func closedNegativeControlGitHub(t *testing.T, runs *checkRunsFake, pull func(number string) string) (http.Handler, *store.Store, *server) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate fixture key: %v", err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{repo}/installation", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("owner")+"/"+r.PathValue("repo") != "acme/widgets" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		fmt.Fprint(w, `{"id":77,"app_slug":"dispatch-test","permissions":{"contents":"read","pull_requests":"read","checks":"read"}}`)
	})
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"token":"ghs_fake_77","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"full_name":%q,"default_branch":"main"}`, r.PathValue("owner")+"/"+r.PathValue("repo"))
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{sha}/pulls", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[]`)
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/{number}", func(w http.ResponseWriter, r *http.Request) {
		body := ""
		if r.PathValue("owner")+"/"+r.PathValue("repo") == "acme/widgets" {
			body = pull(r.PathValue("number"))
		}
		if body == "" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/{number}/commits", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[]`)
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{sha}/check-runs", runs.serve)
	upstream := githubtest.Server(t, githubtest.Spec{Handle: mux.ServeHTTP})
	handler, database, _, served := newRegisteredTestServer(t, testServerOptions{
		app:           &auth.AppConfig{ClientID: "Iv1.test", PEM: pemText},
		githubAPIBase: upstream,
	})
	return handler, database, served
}
