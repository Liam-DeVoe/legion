// going_writes_test.go holds the writes a command-bar run makes to what they run through: every
// row of the catalogue executes through GoingCaller.Do against the server's own route table, as
// its caller (c8), and the writes route performs a proposed write as its caller, refusing what
// its route refuses exactly as that route does, with the person's own
// confirmation setting held on the server.
package api

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/going"
	"github.com/sjawhar/envoy/internal/dispatch/auth"
	"github.com/sjawhar/envoy/internal/dispatch/identity"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// goingCookieOrHeader names the person a session cookie names, as a browser's request is named,
// and otherwise the person X-Dispatch-User names, which is how the fixtures below write.
type goingCookieOrHeader struct {
	cookie identity.CookieIdentity
	header identity.HeaderIdentity
}

func (i goingCookieOrHeader) Login(r *http.Request) (string, error) {
	if auth.HasSessionCookie(r) {
		return i.cookie.Login(r)
	}
	return i.header.Login(r)
}

// goingWritesRig is a server whose people sign in with a session cookie, beside the header the
// fixtures write with, and whose stand-in listener holds one live session, s1, titled planner,
// and records every send to it in sent (newGoingWritesRigOn reaches another listener instead).
// mux is the route table as register mounts it; network is the same behind the cross-site origin
// check routes.New puts in front of the whole server, which is what a browser reaches.
type goingWritesRig struct {
	mux      http.Handler
	network  http.Handler
	served   *server
	database *store.Store
	sessions *store.PgSessionStore
	sent     *[]map[string]any
}

// goingPlannerTitle is the stand-in listener's one session's title, and goingPlanner its id.
const (
	goingPlannerTitle = "planner"
	goingPlanner      = "s1"
)

func newGoingWritesRig(t *testing.T) goingWritesRig {
	t.Helper()
	return newGoingWritesRigServing(t, func(database *store.Store) *store.Store { return database })
}

// newGoingWritesRigServing is the rig whose server runs on the store serving makes of the test's
// database - a pool of a smaller size, say - while the fixtures and the sign-ins use the database's
// own.
func newGoingWritesRigServing(t *testing.T, serving func(*store.Store) *store.Store) goingWritesRig {
	t.Helper()
	live := true
	sent := []map[string]any{}
	listener := sessionListener(t, &live, &sent)
	t.Cleanup(listener.Close)
	rig := newGoingWritesRigOn(t, listener.URL, serving)
	rig.sent = &sent
	return rig
}

// newGoingWritesRigOn is the rig whose server reaches the Envoy listener at envoyURL rather than
// the stand-in holding planner, and keeps no record of the sends (sent is nil).
func newGoingWritesRigOn(t *testing.T, envoyURL string, serving func(*store.Store) *store.Store) goingWritesRig {
	t.Helper()
	database := storetest.Open(t)
	sessions := store.NewPgSessionStore(database.Pool)
	people := goingCookieOrHeader{
		cookie: identity.CookieIdentity{SigningKey: testSigningKey, Sessions: sessions},
		header: headerIdentity(database),
	}
	app, githubAPIBase := goingFixtureArchitectureGitHub(t)
	mux, _, _, served := newRegisteredTestServer(t, testServerOptions{store: serving(database), identity: people, envoyURL: envoyURL, app: app, githubAPIBase: githubAPIBase})
	return goingWritesRig{
		mux: mux, network: EnforceCookieOrigin(testServerURL, mux), served: served,
		database: database, sessions: sessions,
	}
}

// cookie is login's session cookie, signed in now.
func (rig goingWritesRig) cookie(t *testing.T, login string) *http.Cookie {
	t.Helper()
	generation, err := rig.sessions.EnsureSession(context.Background(), login)
	if err != nil {
		t.Fatalf("sign %s in: %v", login, err)
	}
	cookie, err := http.ParseSetCookie(auth.IssueSessionCookie(login, generation, testSigningKey, true))
	if err != nil {
		t.Fatalf("parse %s's session cookie: %v", login, err)
	}
	return cookie
}

// request is one request to handler, with headers set on it.
func goingRequestTo(t *testing.T, handler http.Handler, method, target string, body any, headers map[string]string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode the request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, target, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// pageOrigin is the header a page of this deployment's own sends.
var pageOrigin = map[string]string{"Origin": testServerURL}

// browser is one request of login's browser on this deployment's own page, through the origin
// check, as a browser's reaches the server.
func (rig goingWritesRig) browser(t *testing.T, login, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return goingRequestTo(t, rig.network, method, target, body, pageOrigin, rig.cookie(t, login))
}

// fixture is one write of the fixtures, as login through the header, which must succeed.
func (rig goingWritesRig) fixture(t *testing.T, login, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	response := dispatchRequest(t, rig.mux, method, target, body, login)
	if response.Code >= http.StatusMultipleChoices {
		t.Fatalf("fixture %s %s: status=%d body=%s", method, target, response.Code, response.Body.String())
	}
	return response
}

// issue makes one more issue of DEMO, which the first fixture issue's project already holds.
func (rig goingWritesRig) issue(t *testing.T, title string) string {
	t.Helper()
	return decodeBody[model.Issue](t, rig.fixture(t, "alice", http.MethodPost, "/api/v1/issues", map[string]any{
		"project": "DEMO", "title": title, "force": true,
	})).Key
}

// ask opens one question ask on issue.
func (rig goingWritesRig) ask(t *testing.T, issue, question string) string {
	t.Helper()
	return decodeBody[model.Ask](t, rig.fixture(t, "alice", http.MethodPost, "/api/v1/issues/"+issue+"/asks", map[string]any{
		"question": question,
	})).ID
}

// goingBrowserStart is the run of a person at a browser: every action of the catalogue whose rows a
// cookie may call, and no session of their own.
func goingBrowserStart(t *testing.T) goingRunStart {
	t.Helper()
	catalogue, err := BuildGoingCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	return goingRunStart{actions: catalogue.runnableBy(goingCredential{cookie: true, credential: personCredential})}
}

// goingFakeProposal is what a run proposes when a fake decision chose action, about the items the
// words name, and a fake answer call wrote arguments for it: one invocation per element. It reads
// nothing of the store, so a hand-over or an upload's name runs at once only where the words name
// it.
func goingFakeProposal(t *testing.T, start goingRunStart, action string, arguments ...map[string]any) goingProposal {
	t.Helper()
	written, err := json.Marshal(map[string][]map[string]any{action: arguments})
	if err != nil {
		t.Fatal(err)
	}
	return goingPropose(goingDecision{Context: going.ContextNamed, offered: []string{action}},
		&going.Final{Answer: "Done.", Arguments: string(written)}, start, goingHolds{})
}

// proposed records a finished run of login's whose decision chose action, at or above the action
// threshold where act, and whose proposal is the fake answer's arguments for it, and answers the
// run's id. The run's words name every item, every hand-over and every upload's name the
// arguments name, as a person's words do when they ask for a write on those items, so each
// invocation may run at once (goingAutomatic).
func (rig goingWritesRig) proposed(t *testing.T, login string, act bool, action string, arguments ...map[string]any) string {
	t.Helper()
	start := goingBrowserStart(t)
	start.words = "the words of " + action
	for _, call := range arguments {
		for argument, value := range call {
			if goingItemArguments[argument] == "" && !goingHeldArguments[argument] && goingUploadNames[action] != argument {
				continue
			}
			values := []any{value}
			if list, isList := value.([]any); isList {
				values = list
			}
			for _, element := range values {
				if text, isText := element.(string); isText {
					start.words += " " + text
				}
			}
		}
	}
	return rig.recorded(t, login, act, action, start.words, goingFakeProposal(t, start, action, arguments...))
}

// recorded records a finished run of login's, typed as words, whose decision chose action, at or
// above the action threshold where act, and which proposed proposal, and answers the run's id.
func (rig goingWritesRig) recorded(t *testing.T, login string, act bool, action, words string, proposal goingProposal) string {
	t.Helper()
	confidence := 0.99
	decision := goingDecision{
		Actions: []goingDecidedAction{{Action: action, Confidence: &confidence}}, Act: act,
		Context: going.ContextNamed, Display: goingDecidedDisplay{Choice: going.DisplayProse},
	}
	id := uuid.NewString()
	ctx := context.Background()
	if err := rig.served.recordGoingRun(ctx, id, model.CanonicalLogin(login), words, nil); err != nil {
		t.Fatalf("record the run: %v", err)
	}
	if err := rig.served.fillGoingRun(ctx, id, goingRunParts{Decision: &decision, Proposal: &proposal, Finished: true}); err != nil {
		t.Fatalf("fill the run: %v", err)
	}
	return id
}

// goingWriteLine is what a write the tests make answers of it: the item it changed, the fields of
// that item it changed, the value before and the value now, how it went, and for a send to an
// agent the message's last delivery, as its record on the run keeps them.
type goingWriteLine struct {
	WriteID  string            `json:"write_id"`
	Action   string            `json:"action"`
	Item     string            `json:"item"`
	Fields   []string          `json:"fields"`
	Before   json.RawMessage   `json:"before"`
	Now      json.RawMessage   `json:"now"`
	Outcome  goingWriteOutcome `json:"outcome"`
	Delivery *goingDelivery    `json:"delivery,omitempty"`
}

// wrote makes, as login, the write of the invocation body names of run's proposal - its offer, its
// invocation and, for one missing the session it acts for, that session's title (fill) - through
// the server's route table in-process, as the writes route made one: under a claim of the write on
// the run, each request recording its landing in its route's own transaction, so a follow that
// changed nothing is recorded as one; an upload with force over the document its read before
// found writes that document's next version, naming the token of the version it replaces. The
// write is recorded on the run in the place of its claim as a write that went through, with the
// item, the fields, the value before and after as its route answered them, how each request went
// and the undo its row states: the record the run's undo reads. It answers the write's line.
func (rig goingWritesRig) wrote(t *testing.T, login, run string, body map[string]any) goingWriteLine {
	t.Helper()
	ctx := context.Background()
	login = model.CanonicalLogin(login)
	offer, _ := body["offer"].(string)
	index, _ := body["invocation"].(int)
	var invocation *goingInvocation
	if proposal := rig.record(t, login, run).Proposal; proposal != nil {
		for _, proposed := range proposal.Offers {
			if proposed.Action == offer && index < len(proposed.Invocations) {
				invocation = &proposed.Invocations[index]
			}
		}
	}
	if invocation == nil {
		t.Fatalf("run %s proposed no invocation %d of %s", run, index, offer)
	}
	person := model.Actor{Kind: "user", ID: login, Credential: personCredential, Proven: true}
	caller := GoingCaller{Catalogue: rig.served.going, Doer: rig.served.goingDoer(goingBarCaller(person, true, run), testServerURL),
		knownSession: rig.served.goingKnownSession}
	if fill, named := body["fill"].(string); named {
		session, err := caller.reader().session(ctx, fill)
		if err != nil {
			t.Fatalf("resolve the session %q: %v", fill, err)
		}
		caller.Session = session
	}
	prepared, err := caller.prepare(ctx, offer, maps.Clone(invocation.Arguments))
	if err != nil {
		t.Fatalf("prepare %s: %v", offer, err)
	}
	specs, err := prepared.call.requests(prepared.route)
	if err != nil {
		t.Fatalf("encode %s: %v", offer, err)
	}
	write := goingRunWrite{ID: uuid.NewString(), Action: offer, Invocation: index, At: time.Now().UTC()}
	if err := rig.served.claimGoingRunWrite(ctx, run, login, write, ""); err != nil {
		t.Fatalf("claim %s on the run: %v", offer, err)
	}
	caller.landings = &goingLandings{s: rig.served, run: run, login: login, write: write.ID}
	// landed reads whether the write's last request committed a change in its route's own
	// transaction, as its landing records it there, for a route whose answer does not say (a
	// follow).
	observer := goingObserver{read: caller.reader(), s: rig.served, landed: func(ctx context.Context) (bool, error) {
		return caller.landings.landed(ctx, len(specs)-1, specs[len(specs)-1])
	}}
	before, err := observer.observe(ctx, prepared, nil, 0, goingObservation{})
	if err != nil {
		t.Fatalf("read before %s: %v", offer, err)
	}
	if _, uploads := goingUploadNames[prepared.action]; uploads && prepared.arguments["force"] == true && before.held != nil {
		// An upload with force over the document its read before found sheds the create alone its
		// translation carries and names the token of the version it replaces.
		prepared.call.condition = nil
		base, err := observer.uploadBase(ctx, before.held)
		if err != nil {
			t.Fatalf("read the token %s writes over: %v", offer, err)
		}
		if base != "" {
			prepared.call.body["precondition"] = map[string]any{"document": base}
		}
		if specs, err = prepared.call.requests(prepared.route); err != nil {
			t.Fatalf("encode %s: %v", offer, err)
		}
	}
	response, requests, err := caller.send(ctx, prepared, nil)
	if err != nil || response == nil {
		t.Fatalf("send %s: %v", offer, err)
	}
	answer, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode >= http.StatusMultipleChoices {
		t.Fatalf("%s answered %d %s (%v)", offer, response.StatusCode, answer, err)
	}
	// A write whose value after its answer does not say is recorded landed with that value unread,
	// as the run's record keeps such a write, so its undo puts nothing back.
	after, err := observer.observe(ctx, prepared, answer, response.StatusCode, before)
	afterUnread := err != nil
	message := after.note
	if afterUnread {
		after = goingObservation{target: before.target, fields: before.fields, issue: before.issue, beforeUnread: after.beforeUnread}
		message = "the write landed and the value after it could not be read (" + goingFailed(err).Message + ")"
	}
	write.Target, write.Fields = cmp.Or(after.target, before.target), after.fields
	write.Before, write.After = goingJSONOrNull(before.value), goingJSONOrNull(after.value)
	write.Outcome = goingWriteOutcome{OK: true, Status: response.StatusCode, Message: message}
	write.Approved, write.Requests, write.Sends, write.Undo = "person", requests, len(specs), prepared.route.Undo.Kind
	write.Delivery, write.Unchanged, write.BeforeToken, write.BeforeUnread = after.delivery, after.unchanged, before.token, after.beforeUnread
	write.AfterUnread = afterUnread
	if len(write.Fields) == 0 {
		write.Fields = before.fields
	}
	if after.stood != nil {
		write.Before = after.stood
	}
	if err := rig.served.recordGoingRunWrite(ctx, run, login, write, cmp.Or(after.issue, before.issue)); err != nil {
		t.Fatalf("record %s on the run: %v", offer, err)
	}
	return goingWriteLine{WriteID: write.ID, Action: write.Action, Item: write.Target, Fields: write.Fields, Before: write.Before,
		Now: write.After, Outcome: write.Outcome, Delivery: write.Delivery}
}

// TestGoingSettingsAreThePersonsOwnAndReadAskUntilSet: the mode of the bar is held for the
// person, read ask until they set it, read back as they set it, refused when it is neither mode,
// and nobody else's.
func TestGoingSettingsAreThePersonsOwnAndReadAskUntilSet(t *testing.T) {
	rig := newGoingWritesRig(t)
	read := func(login string) goingSettings {
		t.Helper()
		response := rig.browser(t, login, http.MethodGet, "/api/v1/me/going/settings", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET settings as %s answered %d %s", login, response.Code, response.Body.String())
		}
		return decodeBody[goingSettings](t, response)
	}
	if settings := read("alice"); settings.Mode != goingModeAsk {
		t.Fatalf("a new person's mode is %q, want ask", settings.Mode)
	}
	if response := rig.browser(t, "alice", http.MethodPut, "/api/v1/me/going/settings", map[string]any{"mode": "auto"}); response.Code != http.StatusBadRequest {
		t.Fatalf("a mode of auto answered %d, want 400", response.Code)
	}
	if response := rig.browser(t, "alice", http.MethodPut, "/api/v1/me/going/settings", map[string]any{"mode": goingModeAutoReview}); response.Code != http.StatusOK {
		t.Fatalf("PUT auto_review answered %d %s", response.Code, response.Body.String())
	}
	if settings := read("alice"); settings.Mode != goingModeAutoReview {
		t.Fatalf("alice's mode reads %q after she set auto_review", settings.Mode)
	}
	if settings := read("bob"); settings.Mode != goingModeAsk {
		t.Fatalf("bob's mode reads %q after alice set hers, want his own ask", settings.Mode)
	}
}

// record is login's run as the record keeps it.
func (rig goingWritesRig) record(t *testing.T, login, run string) goingRun {
	t.Helper()
	read, err := rig.served.readGoingRun(context.Background(), run, model.CanonicalLogin(login))
	if err != nil {
		t.Fatalf("read the run: %v", err)
	}
	return read
}

// goingRecordingDoer is a Doer that records each request it sends, by method and path.
type goingRecordingDoer struct {
	doer GoingDoer
	sent []string
}

func (d *goingRecordingDoer) Do(request *http.Request) (*http.Response, error) {
	d.sent = append(d.sent, request.Method+" "+request.URL.Path)
	return d.doer.Do(request)
}

// goingPathMatches reports whether path is a path of the route pattern.
func goingPathMatches(pattern, path string) bool {
	want, got := strings.Split(pattern, "/"), strings.Split(path, "/")
	if len(want) != len(got) {
		return false
	}
	for index := range want {
		if strings.HasPrefix(want[index], "{") {
			if got[index] == "" {
				return false
			}
			continue
		}
		if want[index] != got[index] {
			return false
		}
	}
	return true
}

// goingTestRun is the run the command bar's act names on a call a test makes through the route
// table in-process outside any run's write.
const goingTestRun = "0190c7d2-1111-7222-8333-444444444444"

// goingCallerOf is the caller a write is performed as, resolved as the writes route resolves its
// own from a request standing for it - a person's browser on this deployment's page, or a
// session's bearer - with the command bar's act beside it.
func goingCallerOf(t *testing.T, rig goingWritesRig, bearer bool) resolvedCaller {
	t.Helper()
	caller := httptest.NewRequest(http.MethodPost, "/api/v1/going/runs/caller/writes", nil)
	if bearer {
		caller.Header.Set("Authorization", "Bearer agent-token")
	} else {
		caller.Header.Set("Origin", testServerURL)
		caller.AddCookie(rig.cookie(t, "alice"))
	}
	actor, human, err := rig.served.optionalActor(caller)
	if err != nil {
		t.Fatalf("resolve the caller: %v", err)
	}
	return goingBarCaller(actor, human, goingTestRun)
}

// goingRowCase is the call one row of the catalogue is executed with: the arguments the fake
// answer writes, the title of the session the caller names where the row addresses one, and
// whether the row's route takes a session's bearer alone.
type goingRowCase struct {
	arguments map[string]any
	session   string
	bearer    bool
}

// goingRowFixtures are the items the rows' calls name, made before any row runs.
type goingRowFixtures struct {
	issue, released, claimable                  string
	notesSlug, notesID, guideSlug, planSlug     string
	planVersion                                 int
	editAsk, resolveAsk, unfollowAsk, followAsk string
	comment, result, direct                     string
}

// newGoingRowFixtures makes the items the rows' calls name, in the project DEMO, which it makes
// where the rig has not made it already (an agent rig has).
func newGoingRowFixtures(t *testing.T, rig goingWritesRig) goingRowFixtures {
	t.Helper()
	var f goingRowFixtures
	var demo bool
	if err := rig.database.Pool.QueryRow(context.Background(), `select exists (select 1 from projects where key = 'DEMO')`).Scan(&demo); err != nil {
		t.Fatalf("read whether DEMO is made: %v", err)
	}
	if demo {
		f.issue = decodeBody[model.Issue](t, rig.fixture(t, "alice", http.MethodPost, "/api/v1/issues", map[string]any{
			"project": "DEMO", "title": "The first issue", "spec": "The spec says hello to everyone.\n", "force": true,
		})).Key
	} else {
		f.issue = createInteractionIssue(t, rig.mux, "DEMO", "The first issue", "The spec says hello to everyone.\n").Key
	}
	// One architecture source the project's sync imports. The rig wires a GitHub stand-in where
	// acme/arch is readable (goingFixtureArchitectureGitHub), so both the every-source and the
	// one-source sync answer 200.
	if _, err := rig.database.Pool.Exec(context.Background(), `
		insert into architecture_sources (project_key, id, repo, branch, installation_id, created_by)
		values ('DEMO', 1, 'acme/arch', 'main', 1, '{"kind":"system","id":"test"}')`); err != nil {
		t.Fatalf("add an architecture source: %v", err)
	}
	f.released = rig.issue(t, "An issue alice holds")
	f.claimable = rig.issue(t, "An issue nobody holds")
	owner := "/api/v1/issues/" + f.issue
	notes := uploadDocument(t, rig.mux, owner, "notes.md", "# Notes\n\nThe first line.\n")
	f.notesSlug, f.notesID = notes.Artifact.Slug, notes.Artifact.ID
	f.guideSlug = uploadDocument(t, rig.mux, "/api/v1/projects/DEMO", "guide.md", "# Guide\n\nRead this first.\n").Artifact.Slug
	plan := uploadDocument(t, rig.mux, owner, "plan.md", "# Plan\n\nDo the work.\n")
	f.planSlug, f.planVersion = plan.Artifact.Slug, plan.Version.Number
	uploadDocument(t, rig.mux, owner, "verification.md", twoChecks)
	f.editAsk = rig.ask(t, f.issue, "Ship it on Friday?")
	f.resolveAsk = rig.ask(t, f.issue, "Is the old flag still read?")
	f.unfollowAsk = rig.ask(t, f.issue, "Which region first?")
	f.followAsk = rig.ask(t, f.issue, "Who reviews the change?")
	rig.fixture(t, "alice", http.MethodPut, "/api/v1/asks/"+f.unfollowAsk+"/followers/"+goingPlanner, nil)
	f.comment = decodeBody[model.Comment](t, rig.fixture(t, "alice", http.MethodPost, owner+"/comments", map[string]any{"body": "A thread to resolve."})).ID
	f.result = decodeBody[recordedResult](t, rig.fixture(t, "alice", http.MethodPost, owner+"/results", resultBody("k1", "pass", headCommit, nil))).Result.ID
	rig.fixture(t, "alice", http.MethodPost, "/api/v1/issues/"+f.released+"/claim", map[string]any{})
	f.direct = decodeBody[model.Message](t, rig.fixture(t, "alice", http.MethodPost, "/api/v1/agents/"+goingPlanner+"/messages", map[string]any{
		"body": "Where is the dashboard?", "delivery": "aside",
	})).ID
	return f
}

// goingRowCases is the call each row of the catalogue is executed with, by action and row.
func goingRowCases(f goingRowFixtures) map[string]goingRowCase {
	return map[string]goingRowCase{
		"dispatch_issue POST /api/v1/issues": {arguments: map[string]any{"project": "DEMO", "title": "Made by the bar", "force": true}},
		"dispatch_issue_update PATCH /api/v1/issues/{key}": {arguments: map[string]any{
			"issue": f.issue, "status": "in_progress", "labels": []any{"bar"}, "external_links": []any{"https://example.com/pull/1"},
		}},
		"dispatch_claim DELETE /api/v1/issues/{key}/claim": {arguments: map[string]any{"issue": f.released, "release": true}},
		"dispatch_claim POST /api/v1/issues/{key}/claim":   {arguments: map[string]any{"issue": f.claimable}},
		"dispatch_ask POST /api/v1/issues/{key}/asks": {arguments: map[string]any{
			"issue": f.issue, "question": "Ship the notes as they stand?",
			"anchor": map[string]any{"artifact": f.notesSlug, "quote": "The first line."},
		}},
		"dispatch_ask POST /api/v1/artifacts/{id}/asks": {arguments: map[string]any{
			"project": "DEMO", "artifact": f.guideSlug, "question": "Is the guide right?",
		}},
		"dispatch_edit_ask PATCH /api/v1/asks/{id}":                   {arguments: map[string]any{"ask": f.editAsk, "question": "Ship it on Monday?"}},
		"dispatch_resolve_ask POST /api/v1/asks/{id}/resolve":         {arguments: map[string]any{"ask": f.resolveAsk, "kind": "resolved", "reason": "Settled elsewhere."}},
		"dispatch_resolve_comment POST /api/v1/comments/{id}/resolve": {arguments: map[string]any{"comment": f.comment}},
		"dispatch_follow DELETE /api/v1/asks/{id}/followers/{session_id}": {
			arguments: map[string]any{"ask": f.unfollowAsk, "action": "unfollow"}, session: goingPlannerTitle,
		},
		"dispatch_follow PUT /api/v1/asks/{id}/followers/{session_id}": {
			arguments: map[string]any{"ask": f.followAsk, "action": "follow"}, session: goingPlannerTitle,
		},
		"dispatch_comment POST /api/v1/issues/{key}/comments": {arguments: map[string]any{
			"issue": f.issue, "body": "Reads well.", "artifact": f.notesSlug, "quote": "The first line.",
		}},
		"dispatch_comment POST /api/v1/artifacts/{id}/comments": {arguments: map[string]any{
			"project": "DEMO", "artifact": f.guideSlug, "body": "Clear.", "quote": "Read this first.",
		}},
		"dispatch_suggest POST /api/v1/issues/{key}/comments": {arguments: map[string]any{
			"issue": f.issue, "quote": "says hello", "replace_with": "says hi",
		}},
		"dispatch_suggest POST /api/v1/artifacts/{id}/comments": {arguments: map[string]any{
			"project": "DEMO", "artifact": f.guideSlug, "quote": "Read this", "replace_with": "Read that",
		}},
		"dispatch_message POST /api/v1/issues/{key}/messages": {arguments: map[string]any{"issue": f.issue, "body": "Where are we?"}},
		"dispatch_message POST /api/v1/messages/{id}/reply": {
			arguments: map[string]any{"in_reply_to": f.direct, "body": "Working on it."}, bearer: true,
		},
		"dispatch_send_to_agent POST /api/v1/agents/{session_id}/messages": {arguments: map[string]any{
			"session": goingPlannerTitle, "body": "Have a look at the notes.", "delivery": "btw",
		}},
		"dispatch_doc_edit POST /api/v1/issues/{key}/artifacts/{slug}/edits": {arguments: map[string]any{
			"issue": f.issue, "artifact": f.notesSlug, "ops": []any{map[string]any{"op": "replace", "find": "first line", "with": "opening line"}},
		}},
		"dispatch_doc_edit POST /api/v1/projects/{key}/artifacts/{slug}/edits": {arguments: map[string]any{
			"project": "DEMO", "artifact": f.guideSlug, "ops": []any{map[string]any{"op": "replace", "find": "first", "with": "before anything"}},
		}},
		"dispatch_doc_edit POST /api/v1/artifacts/{id}/edits": {arguments: map[string]any{
			"artifact": f.notesID, "ops": []any{map[string]any{"op": "replace", "find": "Notes", "with": "Jottings"}},
		}},
		"dispatch_request_approval POST /api/v1/artifacts/{id}/approval-requests": {arguments: map[string]any{
			"issue": f.issue, "artifact": "spec", "summary": "Ready for review.",
		}},
		"dispatch_plan_approve POST /api/v1/artifacts/{id}/plan-reviews": {arguments: map[string]any{
			"issue": f.issue, "artifact": f.planSlug, "version": f.planVersion,
		}},
		"dispatch_result POST /api/v1/results/{id}/retract": {arguments: map[string]any{"retract": f.result, "reason": "Recorded at the wrong commit."}},
		"dispatch_result POST /api/v1/issues/{key}/results": {arguments: map[string]any{
			"issue": f.issue, "check": "k1", "role": "implementer", "verdict": "pass", "commit": headCommit,
		}},
		"dispatch_artifact POST /api/v1/issues/{key}/artifacts": {arguments: map[string]any{
			"issue": f.issue, "name": "extra.md", "content": "# Extra\n\nMore words.\n",
		}},
		"dispatch_artifact POST /api/v1/projects/{key}/artifacts": {arguments: map[string]any{
			"project": "DEMO", "name": "handbook.md", "content": "# Handbook\n",
		}},
		"dispatch_artifact POST /api/v1/agents/{session_id}/artifacts": {
			arguments: map[string]any{"name": "notes.txt", "content": "Plain words for the planner."}, session: goingPlannerTitle,
		},
		"dispatch_project POST /api/v1/projects":                                           {arguments: map[string]any{"key": "NEWP", "name": "A new project"}},
		"dispatch_architecture_sync POST /api/v1/projects/{key}/architecture-sources/sync": {arguments: map[string]any{"project": "DEMO"}},
		"dispatch_architecture_sync POST /api/v1/projects/{key}/architecture-sources/{owner}/{repo}/sync": {
			arguments: map[string]any{"project": "DEMO", "repo": "acme/arch"},
		},
		"dispatch_handoff_write POST /api/v1/issues/{key}/handoffs/{phase}": {
			arguments: map[string]any{"issue": f.issue, "phase": "plan", "body": `{"summary":"planned"}`}, bearer: true,
		},
	}
}

// TestEveryGoingRowExecutesThroughTheCallerAgainstTheServedTable is c8: every row of the catalogue
// BuildGoingCatalogue holds, counted against that builder, executes through GoingCaller.Do against
// the server's own route table called in-process, under the credential kind its route accepts,
// with the arguments a fake answer call wrote for the action a fake decision chose, translated
// into what the route takes; and each call reaches the row the catalogue names and is taken by it.
func TestEveryGoingRowExecutesThroughTheCallerAgainstTheServedTable(t *testing.T) {
	rig := newGoingWritesRig(t)
	cases := goingRowCases(newGoingRowFixtures(t, rig))
	catalogue, err := BuildGoingCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	for _, action := range catalogue.Actions() {
		rows += len(action.Routes)
	}
	if len(cases) != rows {
		t.Fatalf("%d row cases for the %d rows the builder holds: every row is executed, and only those", len(cases), rows)
	}
	browser := goingBrowserStart(t)
	bearer := goingRunStart{actions: catalogue.runnableBy(goingCredential{credential: scopelessCredential})}
	for _, action := range catalogue.Actions() {
		for _, route := range action.Routes {
			name := action.Action + " " + route.Method + " " + route.Pattern
			t.Run(name, func(t *testing.T) {
				row, held := cases[name]
				if !held {
					t.Fatalf("no case executes %s", name)
				}
				start, session := browser, ""
				if row.bearer {
					// The session a bearer names itself as, as `dispatch` names it in actor.
					start, session = bearer, goingPlanner
				}
				proposal := goingFakeProposal(t, start, action.Action, row.arguments)
				if len(proposal.Offers) != 1 || !proposal.Offers[0].Offered || len(proposal.Offers[0].Invocations) != 1 {
					t.Fatalf("the proposal is %+v, want %s offered once", proposal, action.Action)
				}
				invocation := proposal.Offers[0].Invocations[0]
				doer := &goingRecordingDoer{doer: rig.served.goingDoer(goingCallerOf(t, rig, row.bearer), testServerURL)}
				if row.session != "" {
					// The session a person names by its title, resolved as the person
					// may read the roster.
					resolved, err := (goingReader{doer: doer}).session(context.Background(), row.session)
					if err != nil {
						t.Fatalf("resolve the session %q: %v", row.session, err)
					}
					session = resolved
				} else if invocation.Missing != nil {
					t.Fatalf("the invocation still misses %+v", invocation.Missing)
				}
				caller := GoingCaller{Catalogue: catalogue, Session: session, Doer: doer}
				response, err := caller.Do(context.Background(), action.Action, invocation.Arguments)
				if err != nil {
					t.Fatalf("perform %s: %v", name, err)
				}
				answer := new(bytes.Buffer)
				_, _ = answer.ReadFrom(response.Body)
				if response.StatusCode >= http.StatusMultipleChoices {
					t.Fatalf("%s refused the call: status=%d body=%s (sent %v)", name, response.StatusCode, answer.String(), doer.sent)
				}
				var writes []string
				for _, sent := range doer.sent {
					if !strings.HasPrefix(sent, http.MethodGet+" ") {
						writes = append(writes, sent)
					}
				}
				last := writes[len(writes)-1]
				method, path, _ := strings.Cut(last, " ")
				if method != route.Method || !goingPathMatches(route.Pattern, path) {
					t.Fatalf("the call's write was %s, want the row %s %s", last, route.Method, route.Pattern)
				}
			})
		}
	}
}

// TestEveryGoingRowRefusesAnActionTheCatalogueDoesNotHoldWithNoRequest: an action the catalogue
// does not hold is refused with its own error and nothing is sent, and the proposal never offers
// it, whatever the answer wrote.
func TestEveryGoingRowRefusesAnActionTheCatalogueDoesNotHoldWithNoRequest(t *testing.T) {
	catalogue, err := BuildGoingCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	doer := &goingRecordingDoer{doer: &countingDoer{}}
	_, err = GoingCaller{Catalogue: catalogue, Doer: doer}.Do(context.Background(), "dispatch_paint_widget", map[string]any{"widget": "7"})
	var unknown GoingUnknownActionError
	if !errors.As(err, &unknown) || unknown.Action != "dispatch_paint_widget" || len(doer.sent) != 0 {
		t.Fatalf("an action outside the catalogue answered %v and sent %v, want its own refusal and nothing sent", err, doer.sent)
	}
	start := goingBrowserStart(t)
	proposal := goingPropose(goingDecision{Context: going.ContextNamed, offered: []string{"dispatch_paint_widget"}},
		&going.Final{Arguments: `{"dispatch_paint_widget":[{"widget":"7"}]}`}, start, goingHolds{})
	if len(proposal.Offers) != 0 {
		t.Fatalf("the proposal offers %+v, want nothing for an action the catalogue does not hold", proposal.Offers)
	}
}

// TestEveryGoingRowWithAnUnfillableArgumentIsNotOfferedUntilTheWordsSupplyIt: an action whose
// required arguments neither the page, the words nor one field of the bar can fill is not
// offered, and the same action is offered once the words supply them.
func TestEveryGoingRowWithAnUnfillableArgumentIsNotOfferedUntilTheWordsSupplyIt(t *testing.T) {
	start := goingBrowserStart(t)
	start.chip = &goingContext{Path: "/issues/DEMO-1", Project: "DEMO", Issue: "DEMO-1"}
	unfilled := goingFakeProposal(t, start, "dispatch_resolve_ask", map[string]any{})
	if unfilled.Offers[0].Offered {
		t.Fatalf("an ask resolve with its ask, kind and reason all missing is offered as %+v", unfilled.Offers[0])
	}
	filled := goingFakeProposal(t, start, "dispatch_resolve_ask",
		map[string]any{"ask": "0199e2a0-2222-7000-8000-000000000002", "kind": "resolved", "reason": "Settled."})
	if !filled.Offers[0].Offered || filled.Offers[0].Invocations[0].Missing != nil {
		t.Fatalf("the same resolve, its arguments in the words, is offered as %+v, want it offered whole", filled.Offers[0])
	}
}

// TestGoingOffersNoActionToACallerWhoseTokenNeedsAProof: a caller presenting a broker token under
// DPoP is offered no action, since the bar acts with a person's own credential and a broker token
// is none; the same credential presented as a bearer is offered the catalogue.
func TestGoingOffersNoActionToACallerWhoseTokenNeedsAProof(t *testing.T) {
	catalogue, err := BuildGoingCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	caller := model.Actor{Credential: personCredential}
	presented := func(authorization string) []GoingAction {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/going/requests", nil)
		request.Header.Set("Authorization", authorization)
		return catalogue.runnableBy(goingCredentialOf(request, caller, false))
	}
	if offered := presented("DPoP a-broker-token"); len(offered) != 0 {
		t.Errorf("a caller whose token needs a DPoP proof is offered %d actions, want none", len(offered))
	}
	if offered := presented("Bearer a-personal-token"); len(offered) == 0 {
		t.Error("the same credential presented as a bearer is offered no action, want the catalogue")
	}
}

// The route a send to an agent goes through answers 201 even when the delivery failed, so the
// write's line carries the state and the error of the message's last delivery: a failed send is
// told apart from one that reached the session.
func TestGoingWriteOfASendToAgentRecordsItsLastDeliverysState(t *testing.T) {
	prepared := goingPrepared{action: "dispatch_send_to_agent", call: goingCall{path: map[string]string{"session_id": goingPlanner}}}
	answer := []byte(`{"id":"msg-1","deliveries":[
		{"attempt":1,"state":"sent","error":null},
		{"attempt":2,"state":"failed","error":"no live session s1"}
	]}`)
	after, err := (goingObserver{}).observe(t.Context(), prepared, answer, http.StatusCreated, goingObservation{})
	if err != nil {
		t.Fatalf("observe the send: %v", err)
	}
	if after.target != "msg-1" || after.delivery == nil || after.delivery.State != "failed" ||
		after.delivery.Error == nil || *after.delivery.Error != "no live session s1" {
		t.Errorf("the send's delivery after = %+v (target %q), want msg-1 with its last delivery failed and why", after.delivery, after.target)
	}
}
