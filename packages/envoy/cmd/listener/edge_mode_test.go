package main

// edge_mode_test.go holds the edge listener's rules on a real bus: E1's admission, E2's topics
// with its GitHub rows G1 to G6, E3's roles, E4's sends, E5's reads, E6's start, and K6's record
// of the registering token's projects on each session.
//
// Every row reads the projects its listener serves from ENVOY_EDGE_PROJECTS when the test's own
// environment sets it, and serves PRIV otherwise (edgeTestProjects). So the control every row is
// run under once is the same command with ENVOY_EDGE_PROJECTS set empty: the listener is in no
// edge mode, answers as every listener does today, and each row fails naming the call it expected
// refused.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/brokertoken/brokertokentest"
	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/legionagents"
	"github.com/sjawhar/envoy/internal/logging"
	"github.com/sjawhar/envoy/internal/metrics"
	"github.com/sjawhar/envoy/internal/session"
	"github.com/sjawhar/envoy/internal/store"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
	"github.com/sjawhar/envoy/listenerwire"
)

// edgeTestProjects is the projects the rows' edge listener serves: PRIV, unless the test's own
// environment sets ENVOY_EDGE_PROJECTS, read then as the listener reads it. Set empty, it is the
// edge-off control: a listener that serves no project is in no edge mode.
func edgeTestProjects(t *testing.T) []string {
	t.Helper()
	raw, set := os.LookupEnv("ENVOY_EDGE_PROJECTS")
	if !set {
		return []string{"PRIV"}
	}
	projects, err := oauthwire.ParseProjects(strings.ReplaceAll(raw, ",", " "))
	if err != nil {
		t.Fatalf("ENVOY_EDGE_PROJECTS=%q: %v", raw, err)
	}
	t.Logf("ENVOY_EDGE_PROJECTS=%q: the listener serves %v (edge mode %t)", raw, projects, len(projects) > 0)
	return projects
}

// The repository answer every GitHub row starts from, as K-E5's broker route relays Dispatch's:
// PRIV-1 names acme/pub, PRIV-3 names none and is a child of PRIV-2, which names acme/two, so
// PRIV's issues resolve to acme/pub and acme/two; INFRA-1 names acme/priv, which is INFRA's and
// no repository of PRIV's. Which issue resolves where is Dispatch's one rule (AG-11); the stand-in
// answers its result.
func edgeRepositoryFixture() map[string][]string {
	return map[string][]string{"PRIV": {"acme/pub", "acme/two"}, "INFRA": {"acme/priv"}}
}

// edgeAccountToken is the stand-in broker account the rig's repository reader asks with.
const edgeAccountToken = "listener-account-token"

// repositoryStandIn is the broker's GET /v1/project-repositories (K-E5, AG-12's route) as the
// edge listener reads it: the answer of each project it serves, 403 PROJECT_NOT_LISTED for any
// other, and the status the test sets in place of every answer while it sets one.
type repositoryStandIn struct {
	server  *httptest.Server
	serves  []string
	mu      sync.Mutex
	answers map[string][]string
	status  int
	calls   atomic.Int64
	bearers []string
}

func newRepositoryStandIn(t *testing.T, serves []string, answers map[string][]string) *repositoryStandIn {
	t.Helper()
	stand := &repositoryStandIn{serves: serves, answers: answers}
	stand.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != edgeRepositoriesPath {
			http.NotFound(w, r)
			return
		}
		stand.calls.Add(1)
		stand.mu.Lock()
		stand.bearers = append(stand.bearers, r.Header.Get("Authorization"))
		status, answers := stand.status, stand.answers
		stand.mu.Unlock()
		project := r.URL.Query().Get("project")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case status != 0:
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"code":"REPOSITORIES_UNKNOWN","error":"the stand-in answers %d"}`, status)
		case !slices.Contains(stand.serves, project):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"PROJECT_NOT_LISTED","error":"the caller's edge-listener entry does not list this project"}`))
		default:
			repositories := answers[project]
			if repositories == nil {
				repositories = []string{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"project": project, "repositories": repositories})
		}
	}))
	t.Cleanup(stand.server.Close)
	return stand
}

// answer makes project's answer repositories from the next call on.
func (s *repositoryStandIn) answer(project string, repositories ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	answers := make(map[string][]string, len(s.answers))
	for key, value := range s.answers {
		answers[key] = value
	}
	answers[project] = repositories
	s.answers = answers
}

// failWith makes every later call answer status, 0 for the answers again.
func (s *repositoryStandIn) failWith(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

func (s *repositoryStandIn) lastBearer() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bearers) == 0 {
		return ""
	}
	return s.bearers[len(s.bearers)-1]
}

// edgeClock is the clock the rows' repository reader dates its answers by: the real one, moved
// on by what a row adds, so a row ages an answer past its life without waiting it out.
type edgeClock struct{ ahead atomic.Int64 }

func (c *edgeClock) now() time.Time { return time.Now().Add(time.Duration(c.ahead.Load())) }

func (c *edgeClock) advance(d time.Duration) { c.ahead.Add(int64(d)) }

// edgeRig is a listener's /v1 as an edge deployment runs it, every piece real but the broker: the
// routes and session relays over the in-process server (relayRig), the broker-token credential
// behind apiAuth with this listener's edge projects, the broker's introspection stub, and the
// stand-in of its repository route, read through the listener's own reader. Its refresh loop runs
// on ticks the row sends (tick), so a row orders a refresh against its own publishes.
type edgeRig struct {
	*relayRig
	projects     []string
	handler      http.Handler
	broker       *brokertokentest.StubBroker
	caller       *brokertokentest.Caller
	repositories *repositoryStandIn
	reader       *edgeRepositories
	clock        *edgeClock
	met          *metrics.Registry
	agents       *legionagents.Store
	ticks        chan time.Time
}

func newEdgeRig(t *testing.T) *edgeRig {
	t.Helper()
	projects := edgeTestProjects(t)
	rig := newRelayRigUnowned(t, relayPauseFirst, relayPauseMax)
	met := metrics.New()
	stand := newRepositoryStandIn(t, []string{"PRIV"}, edgeRepositoryFixture())
	clock := &edgeClock{}
	logger := rig.harness.config.logger
	reader := newEdgeRepositories(stand.server.URL, listenerBrokerAccount(edgeAccountToken), projects, newEdgeWithheldCounter(met), logger)
	reader.now = clock.now
	agents, err := legionagents.Open(t.Context(), rig.harness.client.Conn, legionagents.WithReplicas(1))
	if err != nil {
		t.Fatalf("open the legion agent registry: %v", err)
	}
	rig.deps.legionAgents = newLegionAgentRegistry(agents)
	edge := &edgeRig{relayRig: rig, projects: projects, met: met, repositories: stand, reader: reader, clock: clock,
		agents: agents, ticks: make(chan time.Time)}
	if len(projects) > 0 {
		rig.deps.repositories = reader
		rig.relays.repositories = reader
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			runEdgeRepositories(ctx, rig.deps, "test-machine", edge.ticks)
		}()
		t.Cleanup(func() {
			cancel()
			<-done
		})
	}
	rig.relays.setOwned(true)
	edge.broker = brokertokentest.NewStubBroker(t)
	edge.caller = brokertokentest.NewCaller(t)
	edge.handler = apiAuth(listenerAPIAuth{brokers: listenerBrokerTokens(t, edge.broker), edgeProjects: projects, logger: logger}, rig.mux)
	return edge
}

// edgeOn reports whether the rig's listener is in edge mode: false under the control.
func (r *edgeRig) edgeOn() bool { return len(r.projects) > 0 }

// tick runs one pass of the listener's repository refresh and waits for it to finish.
func (r *edgeRig) tick(t *testing.T) {
	t.Helper()
	if !r.edgeOn() {
		return
	}
	before := r.reader.passes.Load()
	select {
	case r.ticks <- time.Now():
	case <-time.After(10 * time.Second):
		t.Fatal("the repository refresh loop took no tick within 10 s")
	}
	testwait.Eventually(t, "the repository refresh pass to finish", func() bool { return r.reader.passes.Load() > before })
}

// personToken is a token the broker answers as session's, acting for a person, of project scope
// person and listing projects: the token an agent's own trade makes.
func (r *edgeRig) personToken(session string, projects ...string) string {
	token := "access-token-of-" + session
	answer := sessionAnswer(session, "outside@example.com", r.caller.JKT())
	answer.Projects = slices.Sorted(slices.Values(projects))
	r.broker.Says(token, answer)
	return token
}

// namedToken is a named project's token for session: an operator's entry's, its one project.
func (r *edgeRig) namedToken(session, project string) string {
	token := "named-token-of-" + session
	answer := sessionAnswer(session, "outside@example.com", r.caller.JKT())
	answer.ProjectScope = brokertoken.ProjectScopeNamed
	answer.Projects = []string{project}
	r.broker.Says(token, answer)
	return token
}

// call sends one request through the whole stack: the credential, then the route.
func (r *edgeRig) call(t *testing.T, method, token, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	r.handler.ServeHTTP(recorder, brokerTokenRequest(t, r.caller, method, path, token, body))
	return recorder
}

// subscribe registers session under token for consumer delivery, following topics.
func (r *edgeRig) subscribe(t *testing.T, token, session string, topics ...string) *httptest.ResponseRecorder {
	t.Helper()
	return r.call(t, http.MethodPost, token, "/v1/interests/subscribe", consumerRegistration(session, "epoch-1", topics...))
}

// placeElsewhere writes session's entry as another listener's registration under a token listing
// projects would, and its interest row, so the edge's reads find a session it did not register.
func (r *edgeRig) placeElsewhere(t *testing.T, sessionID string, projects ...string) {
	t.Helper()
	if projects == nil {
		projects = []string{}
	}
	if err := r.deps.sessions.Put(sessionID, session.SessionEntry{MachineID: "shared-listener", SelfSubscribed: true, Title: sessionID, Projects: projects}); err != nil {
		t.Fatalf("register %s elsewhere: %v", sessionID, err)
	}
	if _, err := r.deps.registry.Upsert(store.Interest{SessionID: sessionID, MachineID: "shared-listener"}, []string{contracts.LegacyAgentSubject(sessionID)}); err != nil {
		t.Fatalf("write %s's interest row: %v", sessionID, err)
	}
}

// withheld is envoy_edge_github_withheld_total{reason=...} as the listener's /metrics renders it,
// -1 for no series.
func (r *edgeRig) withheld(t *testing.T, reason string) int64 {
	t.Helper()
	recorder := httptest.NewRecorder()
	r.met.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return counterValue(t, recorder.Body.String(), `envoy_edge_github_withheld_total{reason="`+reason+`"}`)
}

// edgeAnswer is what a row reads of an edge refusal.
type edgeAnswer struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

// requireEdgeRefusal fails the test unless recorder is status with code, naming what was asked.
func requireEdgeRefusal(t *testing.T, recorder *httptest.ResponseRecorder, status int, code, asked string) {
	t.Helper()
	var answer edgeAnswer
	_ = json.Unmarshal(recorder.Body.Bytes(), &answer)
	if recorder.Code != status || answer.Code != code {
		t.Errorf("%s: status %d code %q (%s), want %d %s", asked, recorder.Code, answer.Code, strings.TrimSpace(recorder.Body.String()), status, code)
	}
}

// requireStatus fails the test unless recorder is status, naming what was asked.
func requireStatus(t *testing.T, recorder *httptest.ResponseRecorder, status int, asked string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("%s: status %d (%s), want %d", asked, recorder.Code, strings.TrimSpace(recorder.Body.String()), status)
	}
}

// E1: an edge listener admits a broker token alone, and only one whose projects are all among
// the ones it serves, of project scope named or person; every other caller is refused before any
// handler runs. The exported admission (EdgeAdmits) is the very rule apiAuth asks, which the relay
// socket asks again of each live answer (K-E2).
func TestEdgeAdmitsOnlyTokensOfItsProjects(t *testing.T) {
	projects := edgeTestProjects(t)
	broker := brokertokentest.NewStubBroker(t)
	caller := brokertokentest.NewCaller(t)
	reached := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		w.WriteHeader(http.StatusNoContent)
	})
	logger := logging.NewWithWriter("test-machine", io.Discard)
	handler := apiAuth(listenerAPIAuth{brokers: listenerBrokerTokens(t, broker), edgeProjects: projects, logger: logger}, next)
	answer := func(session, scope string, projects ...string) brokertoken.Introspection {
		got := sessionAnswer(session, "outside@example.com", caller.JKT())
		got.ProjectScope, got.Projects = scope, projects
		return got
	}
	service := serviceAnswer("ses_service", "example-fleet", caller.JKT())
	for _, tc := range []struct {
		name   string
		answer *brokertoken.Introspection
		header string
		admit  bool
		status int
	}{
		{name: "a named token of the project it serves", answer: new(answer("ses_named", brokertoken.ProjectScopeNamed, "PRIV")), admit: true},
		{name: "a person token listing only that project", answer: new(answer("ses_person", brokertoken.ProjectScopePerson, "PRIV")), admit: true},
		{name: "a person token listing a project it does not serve beside it", answer: new(answer("ses_two", brokertoken.ProjectScopePerson, "INFRA", "PRIV")), status: http.StatusForbidden},
		{name: "a named token of another project", answer: new(answer("ses_other", brokertoken.ProjectScopeNamed, "INFRA")), status: http.StatusForbidden},
		{name: "a person token listing no project", answer: new(answer("ses_none", brokertoken.ProjectScopePerson)), status: http.StatusForbidden},
		{name: "a service's token, which lists none", answer: &service, status: http.StatusForbidden},
		{name: "a read-only token", answer: new(readOnlyAnswer("ses_reader", "example-reader", caller.JKT())), status: http.StatusUnauthorized},
		{name: "no credential at all", status: http.StatusUnauthorized},
		{name: "a shared bearer", header: "Bearer some-shared-token", status: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached = 0
			var request *http.Request
			switch {
			case tc.answer != nil:
				token := "token-" + tc.answer.Session
				broker.Says(token, *tc.answer)
				request = brokerTokenRequest(t, caller, http.MethodGet, "/v1/sessions", token, "")
			default:
				request = httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
				request.Host = "127.0.0.1:9020"
				if tc.header != "" {
					request.Header.Set("Authorization", tc.header)
				}
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if tc.admit {
				if recorder.Code != http.StatusNoContent || reached != 1 {
					t.Fatalf("GET /v1/sessions under %s: status %d, reached the handler %d times (%s), want it admitted", tc.name, recorder.Code, reached, recorder.Body.String())
				}
				return
			}
			if recorder.Code != tc.status || reached != 0 {
				t.Fatalf("GET /v1/sessions under %s: status %d, reached the handler %d times (%s), want %d before any handler", tc.name, recorder.Code, reached, recorder.Body.String(), tc.status)
			}
		})
	}
	// The exported rule, as the relay socket calls it on each live answer.
	for _, tc := range []struct {
		caller brokertoken.Caller
		admit  bool
	}{
		{brokertoken.Caller{Session: "s", Person: "p", ProjectScope: brokertoken.ProjectScopeNamed, Projects: []string{"PRIV"}}, true},
		{brokertoken.Caller{Session: "s", Person: "p", ProjectScope: brokertoken.ProjectScopePerson, Projects: []string{"INFRA", "PRIV"}}, false},
		{brokertoken.Caller{Session: "s", Person: "p", ProjectScope: brokertoken.ProjectScopePerson}, false},
		{brokertoken.Caller{Session: "s", Service: "f", ProjectScope: brokertoken.ProjectScopeNone, ReadOnly: true}, false},
		{brokertoken.Caller{Session: "s", Person: "p", ProjectScope: "elsewhere", Projects: []string{"PRIV"}}, false},
	} {
		err := EdgeAdmits(projects, tc.caller)
		if tc.admit != (err == nil) {
			t.Errorf("EdgeAdmits(%v, %+v) = %v, want admitted %t", projects, tc.caller, err, tc.admit)
		}
		if err != nil && !errors.Is(err, ErrEdgeNotAdmitted) {
			t.Errorf("EdgeAdmits(%v, %+v) = %v, which is not ErrEdgeNotAdmitted", projects, tc.caller, err)
		}
	}
}

// G1: an edge session may follow the GitHub events of each repository its project's issues
// resolve to, read fresh through the broker at the registration with the listener's own account,
// and an event of each reaches it.
func TestAnEdgeRelaysTheGitHubEventsOfItsProjectsRepositories(t *testing.T) {
	rig := newEdgeRig(t)
	const sessionID = "ses_edge_github"
	token := rig.personToken(sessionID, "PRIV")
	standIn := startSessionStandIn(t, rig.url, sessionID, answerDone)
	before := rig.repositories.calls.Load()
	topics := []string{"notifications.github.acme.pub.>", "notifications.github.acme.two.>"}
	answer := rig.subscribe(t, token, sessionID, topics...)
	requireStatus(t, answer, http.StatusOK, fmt.Sprintf("a registration following %v", topics))
	if rig.repositories.calls.Load() == before {
		t.Fatalf("the registration naming %v read no repository answer; K-E5 is read fresh at each one", topics)
	}
	if got := rig.repositories.lastBearer(); got != "Bearer "+edgeAccountToken {
		t.Fatalf("the repository read presented %q, want the listener's own account", got)
	}
	pub, _ := rig.storeEvent(t, "notifications.github.acme.pub.pr.1")
	two, _ := rig.storeEvent(t, "notifications.github.acme.two.issue.2")
	requireFrames(t, standIn.collect(t, 2, 10*time.Second), pub, two)
}

// G2: a GitHub interest in a repository no issue of the project resolves to, or in no single
// repository, is refused 403 repository_not_in_project, and such a repository's event never
// reaches the session.
func TestAnEdgeRefusesAnotherRepositorysEvents(t *testing.T) {
	rig := newEdgeRig(t)
	const sessionID = "ses_edge_other_repository"
	token := rig.personToken(sessionID, "PRIV")
	standIn := startSessionStandIn(t, rig.url, sessionID, answerDone)
	for _, topic := range []string{"notifications.github.acme.priv.>", "notifications.github.acme.>", "notifications.github.>"} {
		requireEdgeRefusal(t, rig.subscribe(t, token, sessionID, topic), http.StatusForbidden, "repository_not_in_project", "an interest in "+topic)
	}
	requireStatus(t, rig.subscribe(t, token, sessionID, "notifications.github.acme.pub.>"), http.StatusOK, "an interest in acme/pub")
	rig.storeEvent(t, "notifications.github.acme.priv.pr.3")
	pub, _ := rig.storeEvent(t, "notifications.github.acme.pub.pr.4")
	frames := standIn.collect(t, 1, 10*time.Second)
	if frames[0].item.Topic != pub.Topic {
		t.Fatalf("the session's first frame is %s, want acme/pub's: the event of acme/priv reached it", frames[0].item.Topic)
	}
	requireFrames(t, frames, pub)
	standIn.none(t, time.Second)
}

// G3: every subject of no project but GitHub's is refused 403 topic_not_in_project: the
// intakes, the shared wake, the listener's own lanes.
func TestAnEdgeRefusesEverySubjectOfNoProjectButGitHub(t *testing.T) {
	rig := newEdgeRig(t)
	const sessionID = "ses_edge_no_project"
	token := rig.personToken(sessionID, "PRIV")
	for _, topic := range []string{
		"notifications.slack.>",
		"notifications.whatsapp.>",
		"notifications.ghostwispr.>",
		"notifications.team.>",
		"notifications.legion.wake",
		"notifications.envoy.kept.role.x",
	} {
		requireEdgeRefusal(t, rig.subscribe(t, token, sessionID, topic), http.StatusForbidden, "topic_not_in_project", "an interest in "+topic)
	}
}

// G4: a repository that leaves the project's answer stops reaching the session at the first
// refresh that reads the new answer, its event acknowledged undelivered and counted, and the
// session's interest in it is gone at the refresh after. The production refresh runs every 30 s
// and an answer lives 60 s, so both happen within 60 s of the change.
func TestARepositoryThatLeavesTheProjectStopsReachingTheEdge(t *testing.T) {
	if edgeRepositoryRefresh != 30*time.Second || edgeRepositoryAnswerLife != 60*time.Second {
		t.Fatalf("the refresh runs every %s and an answer lives %s, want 30 s and 60 s (R15)", edgeRepositoryRefresh, edgeRepositoryAnswerLife)
	}
	rig := newEdgeRig(t)
	const sessionID = "ses_edge_leaving"
	token := rig.personToken(sessionID, "PRIV")
	standIn := startSessionStandIn(t, rig.url, sessionID, answerDone)
	requireStatus(t, rig.subscribe(t, token, sessionID, "notifications.github.acme.pub.>", "notifications.github.acme.two.>"), http.StatusOK, "the registration")
	first, _ := rig.storeEvent(t, "notifications.github.acme.two.pr.1")
	requireFrames(t, standIn.collect(t, 1, 10*time.Second), first)

	rig.repositories.answer("PRIV", "acme/pub")
	rig.tick(t)
	left, _ := rig.storeEvent(t, "notifications.github.acme.two.pr.2")
	stays, _ := rig.storeEvent(t, "notifications.github.acme.pub.pr.3")
	// The frames go in stream order, so the event after it arriving first is the proof the one
	// before it was not relayed.
	frames := standIn.collect(t, 1, 10*time.Second)
	if frames[0].item.EventID == left.EventID {
		t.Fatalf("the acme/two event %s stored after acme/two left the answer reached the session", left.EventID)
	}
	requireFrames(t, frames, stays)
	if got := rig.withheld(t, "not_in_project"); got != 1 {
		t.Fatalf(`envoy_edge_github_withheld_total{reason="not_in_project"} = %d, want 1: the acme/two event withheld`, got)
	}

	rig.tick(t)
	recorder := rig.call(t, http.MethodGet, token, "/v1/registry/"+sessionID, "")
	requireStatus(t, recorder, http.StatusOK, "GET /v1/registry/"+sessionID)
	var info listenerwire.SessionInfo
	if err := json.Unmarshal(recorder.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode %s: %v", recorder.Body.String(), err)
	}
	for _, topic := range info.Topics {
		if strings.HasPrefix(topic, "notifications.github.acme.two") {
			t.Fatalf("the session still follows %s after acme/two left the answer: %v", topic, info.Topics)
		}
	}
	if !slices.Contains(info.Topics, "notifications.github.acme.pub.>") {
		t.Fatalf("the session no longer follows acme/pub, which stayed: %v", info.Topics)
	}
}

// G5: while no repository answer younger than 60 s stands, a registration naming a GitHub topic
// is 503 repositories_unknown, every GitHub frame is acknowledged undelivered with the gap mark
// on the next frame, and every frame that is not GitHub's flows as before.
func TestAnUnknownRepositoryAnswerWithholdsOnlyGitHubFrames(t *testing.T) {
	rig := newEdgeRig(t)
	const sessionID = "ses_edge_unknown"
	token := rig.personToken(sessionID, "PRIV")
	standIn := startSessionStandIn(t, rig.url, sessionID, answerDone)
	requireStatus(t, rig.subscribe(t, token, sessionID, "notifications.github.acme.pub.>"), http.StatusOK, "the registration")

	rig.repositories.failWith(http.StatusServiceUnavailable)
	requireEdgeRefusal(t, rig.subscribe(t, token, sessionID, "notifications.github.acme.pub.>"), http.StatusServiceUnavailable, "repositories_unknown",
		"a registration naming acme/pub while the answer is 503")
	rig.clock.advance(edgeRepositoryAnswerLife + time.Second)
	rig.storeEvent(t, "notifications.github.acme.pub.pr.5")
	agent, _ := rig.storeEvent(t, contracts.LegacyAgentSubject(sessionID))
	frames := standIn.collect(t, 1, 10*time.Second)
	if frames[0].item.EventID != agent.EventID {
		t.Fatalf("the session's next frame is %s on %s, want the agent message: the acme/pub event was delivered with an answer %s old",
			frames[0].item.EventID, frames[0].item.Topic, edgeRepositoryAnswerLife+time.Second)
	}
	if got := frames[0].msg.Header.Get(relayHeaderGap); got != "true" {
		t.Fatalf("the frame after the withheld GitHub event carries %s %q, want true", relayHeaderGap, got)
	}
	if got := rig.withheld(t, "answer_unknown"); got != 1 {
		t.Fatalf(`envoy_edge_github_withheld_total{reason="answer_unknown"} = %d, want 1`, got)
	}
}

// G6: Dispatch answers a repository in lower case and the intake keeps GitHub's case, so the
// edge compares owner and name ignoring case: an interest in Acme/Pub is admitted through an
// answer of acme/pub, and the event the intake publishes for Acme/Pub reaches the session.
func TestAnEdgeComparesRepositoriesIgnoringCase(t *testing.T) {
	rig := newEdgeRig(t)
	const sessionID = "ses_edge_case"
	token := rig.personToken(sessionID, "PRIV")
	standIn := startSessionStandIn(t, rig.url, sessionID, answerDone)
	before := rig.repositories.calls.Load()
	requireStatus(t, rig.subscribe(t, token, sessionID, "notifications.github.Acme.Pub.>"), http.StatusOK, "an interest in Acme/Pub")
	if rig.repositories.calls.Load() == before {
		t.Fatal("the registration naming Acme/Pub read no repository answer; K-E5 is read fresh at each one")
	}
	event, _ := rig.storeEvent(t, contracts.GithubRepositoryPrefix("Acme", "Pub")+".pr.9")
	requireFrames(t, standIn.collect(t, 1, 10*time.Second), event)
}

// E2's publish rule: an edge session publishes only a role subject of its project in the form
// that names the project, and only a role that project's own (a Legion role of it), whose claim
// is keyed by its bare name; anything else is 403 topic_not_published.
func TestEdgePublishesOnlyRoleSubjectsOfItsProjects(t *testing.T) {
	rig := newEdgeRig(t)
	const sessionID = "ses_edge_publisher"
	token := rig.personToken(sessionID, "PRIV")
	for _, topic := range []string{
		"notifications.dispatch.issue.PRIV.PRIV-1.comment.created",
		"notifications.team.priv",
		"notifications.role.legion-priv-priv-1-tester",
		"notifications.role.INFRA.legion-infra-infra-1-tester",
		"notifications.role.PRIV.lead-reviewer",
		"notifications.github.acme.pub.pr.1",
	} {
		body := fmt.Sprintf(`{"topic":%q,"message":"from the edge","source":"agent","source_session":%q}`, topic, sessionID)
		requireEdgeRefusal(t, rig.call(t, http.MethodPost, token, "/v1/messages/publish", body), http.StatusForbidden, "topic_not_published", "a publish to "+topic)
	}
	role := contracts.RoleSubject("PRIV", "legion-priv-priv-1-tester")
	body := fmt.Sprintf(`{"topic":%q,"message":"for the role's holder","source":"agent","source_session":%q}`, role, sessionID)
	recorder := rig.call(t, http.MethodPost, token, "/v1/messages/publish", body)
	if recorder.Code != http.StatusOK && recorder.Code != http.StatusAccepted {
		t.Fatalf("a publish to %s: status %d (%s), want it taken", role, recorder.Code, recorder.Body.String())
	}
}

// E3: an edge session claims and reads only a role of its token's projects, the project
// contracts.ClaimProject gives it, and its claim is keyed by contracts.RoleClaimKey.
func TestEdgeHoldsRolesToTheTokensProjects(t *testing.T) {
	rig := newEdgeRig(t)
	const sessionID = "ses_edge_roles"
	token := rig.personToken(sessionID, "PRIV")
	requireStatus(t, rig.subscribe(t, token, sessionID), http.StatusOK, "the registration")
	claim := func(role string) *httptest.ResponseRecorder {
		return rig.call(t, http.MethodPost, token, "/v1/roles/set", fmt.Sprintf(`{"session_id":%q,"role":%q,"soft":true}`, sessionID, role))
	}
	for _, role := range []string{"legion-infra-infra-1-tester", "INFRA.legion-infra-infra-1-tester", "lead-reviewer", "INFRA.lead-reviewer"} {
		requireEdgeRefusal(t, claim(role), http.StatusForbidden, "role_not_in_project", "a claim of "+role)
	}
	const own = "legion-priv-priv-1-tester"
	requireStatus(t, claim(own), http.StatusOK, "a claim of "+own)
	key := contracts.RoleClaimKey(mustClaimProject(t, own, ""), own)
	held, err := rig.deps.registry.RoleClaim(t.Context(), key)
	if err != nil || held.HolderSessionID != sessionID {
		t.Fatalf("the claim keyed %s holds %+v (%v), want %s", key, held, err, sessionID)
	}
	requireEdgeRefusal(t, rig.call(t, http.MethodGet, token, "/v1/roles/legion-infra-infra-1-tester", ""), http.StatusForbidden, "role_not_in_project", "a read of legion-infra-infra-1-tester")
	requireStatus(t, rig.call(t, http.MethodGet, token, "/v1/roles/"+own, ""), http.StatusOK, "a read of "+own)
}

func mustClaimProject(t *testing.T, role, requested string) string {
	t.Helper()
	project, err := contracts.ClaimProject(role, requested)
	if err != nil {
		t.Fatalf("ClaimProject(%s, %q): %v", role, requested, err)
	}
	return project
}

// E4: an edge session sends only to a session whose token lists one of its token's projects; a
// send to any other is answered as to a missing session, and a publish to a role subject names
// one of its projects.
func TestEdgeSendsOnlyToSessionsOfTheTokensProjects(t *testing.T) {
	rig := newEdgeRig(t)
	const sender, peer = "ses_edge_sender", "ses_edge_peer"
	token := rig.personToken(sender, "PRIV")
	requireStatus(t, rig.subscribe(t, token, sender), http.StatusOK, "the sender's registration")
	requireStatus(t, rig.subscribe(t, rig.namedToken(peer, "PRIV"), peer), http.StatusOK, "the peer's registration")
	rig.placeElsewhere(t, "ses_infra_agent", "INFRA")
	rig.placeElsewhere(t, "ses_no_project")
	send := func(target string) *httptest.ResponseRecorder {
		return rig.call(t, http.MethodPost, token, "/v1/messages/send",
			fmt.Sprintf(`{"target_session":%q,"source_session":%q,"message":"from the edge","source":"agent"}`, target, sender))
	}
	requireStatus(t, send(peer), http.StatusOK, "a send to "+peer)
	for _, target := range []string{"ses_infra_agent", "ses_no_project"} {
		recorder := send(target)
		var refusal listenerwire.NoLiveSessionRefusal
		_ = json.Unmarshal(recorder.Body.Bytes(), &refusal)
		if recorder.Code != http.StatusNotFound || !strings.HasPrefix(refusal.Error, "no live session "+target) || refusal.LastSeen != 0 {
			t.Errorf("a send to %s: status %d (%s), want the 404 a missing session gets, naming no last registration", target, recorder.Code, recorder.Body.String())
		}
	}
	body := fmt.Sprintf(`{"topic":%q,"message":"from the edge","source":"agent","source_session":%q}`, contracts.RoleSubject("INFRA", "legion-infra-infra-1-tester"), sender)
	requireEdgeRefusal(t, rig.call(t, http.MethodPost, token, "/v1/messages/publish", body), http.StatusForbidden, "topic_not_published", "a publish to INFRA's role")
}

// E5: the edge's reads answer only the sessions and roles of the token's projects, and the
// interest list, another session's interests and every delete naming another session are 403.
func TestEdgeReadsListOnlyTheTokensProjects(t *testing.T) {
	rig := newEdgeRig(t)
	const reader, peer = "ses_edge_reader", "ses_edge_peer_read"
	token := rig.personToken(reader, "PRIV")
	requireStatus(t, rig.subscribe(t, token, reader), http.StatusOK, "the reader's registration")
	requireStatus(t, rig.subscribe(t, rig.namedToken(peer, "PRIV"), peer), http.StatusOK, "the peer's registration")
	rig.placeElsewhere(t, "ses_infra_agent", "INFRA")
	rig.placeElsewhere(t, "ses_no_project")
	for role, holder := range map[string]string{"legion-priv-priv-2-tester": peer, "legion-infra-infra-2-tester": peer} {
		if _, err := rig.deps.registry.SetRole(holder, "test-machine", role, false); err != nil {
			t.Fatalf("claim %s for %s: %v", role, holder, err)
		}
	}
	testwait.Eventually(t, "the role claim copy to name both claims", func() bool {
		held, err := rig.deps.registry.RolesHeldBy(peer)
		return err == nil && len(held) == 2
	})
	ctx := t.Context()
	agents := rig.agents
	for _, entry := range []legionagents.Entry{
		{SessionID: peer, LegionProject: "priv", Issue: "PRIV-2", Role: "tester", RoleToken: "legion-priv-priv-2-tester", Title: "Legion tester · PRIV-2", Harness: "omp", StartedAt: "2026-11-20T00:00:00Z"},
		{SessionID: "ses_infra_agent", LegionProject: "infra", Issue: "INFRA-2", Role: "tester", RoleToken: "legion-infra-infra-2-tester", Title: "Legion tester · INFRA-2", Harness: "omp", StartedAt: "2026-11-20T00:00:00Z"},
	} {
		if _, err := agents.Put(ctx, entry); err != nil {
			t.Fatalf("record the legion agent %s: %v", entry.SessionID, err)
		}
	}

	listed := rig.call(t, http.MethodGet, token, "/v1/sessions", "")
	requireStatus(t, listed, http.StatusOK, "GET /v1/sessions")
	var rows []listenerwire.SessionInfo
	if err := json.Unmarshal(listed.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode %s: %v", listed.Body.String(), err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.SessionID)
		if row.SessionID == peer && !slices.Equal(row.Roles, []string{"legion-priv-priv-2-tester"}) {
			t.Errorf("GET /v1/sessions lists %s with roles %v, want only its PRIV role", peer, row.Roles)
		}
	}
	slices.Sort(ids)
	if want := []string{peer, reader}; !slices.Equal(ids, want) {
		t.Errorf("GET /v1/sessions lists %v, want only the sessions of PRIV %v", ids, want)
	}
	for _, other := range []string{"ses_infra_agent", "ses_no_project"} {
		recorder := rig.call(t, http.MethodGet, token, "/v1/registry/"+other, "")
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET /v1/registry/%s: status %d (%s), want the 404 of a missing session", other, recorder.Code, recorder.Body.String())
		}
	}
	requireStatus(t, rig.call(t, http.MethodGet, token, "/v1/registry/"+peer, ""), http.StatusOK, "GET /v1/registry/"+peer)

	legion := func(path string) []string {
		t.Helper()
		recorder := rig.call(t, http.MethodGet, token, path, "")
		requireStatus(t, recorder, http.StatusOK, "GET "+path)
		var list listenerwire.LegionAgentList
		if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
			t.Fatalf("decode %s: %v", recorder.Body.String(), err)
		}
		sessions := make([]string, 0, len(list.Entries))
		for _, entry := range list.Entries {
			sessions = append(sessions, entry.SessionID)
		}
		return sessions
	}
	if got := legion("/v1/legion/agents?issue=INFRA-2"); len(got) != 0 {
		t.Errorf("GET /v1/legion/agents?issue=INFRA-2 answers %v, want none", got)
	}
	if got := legion("/v1/legion/agents?issue=PRIV-2"); !slices.Equal(got, []string{peer}) {
		t.Errorf("GET /v1/legion/agents?issue=PRIV-2 answers %v, want %s", got, peer)
	}
	if got := legion("/v1/legion/agents/roles/legion-infra-infra-2-tester"); len(got) != 0 {
		t.Errorf("GET /v1/legion/agents/roles/legion-infra-infra-2-tester answers %v, want none", got)
	}
	if recorder := rig.call(t, http.MethodGet, token, "/v1/legion/agents/sessions/ses_infra_agent", ""); recorder.Code != http.StatusNotFound {
		t.Errorf("GET /v1/legion/agents/sessions/ses_infra_agent: status %d (%s), want 404", recorder.Code, recorder.Body.String())
	}
	requireStatus(t, rig.call(t, http.MethodGet, token, "/v1/legion/agents/sessions/"+peer, ""), http.StatusOK, "GET /v1/legion/agents/sessions/"+peer)
	requireEdgeRefusal(t, rig.call(t, http.MethodPut, token, "/v1/legion/agents", `{"entries":[]}`), http.StatusForbidden, "legion_registry_not_written", "PUT /v1/legion/agents")

	requireEdgeRefusal(t, rig.call(t, http.MethodGet, token, "/v1/interests/", ""), http.StatusForbidden, "session_not_in_project", "GET /v1/interests/")
	requireEdgeRefusal(t, rig.call(t, http.MethodGet, token, "/v1/interests/ses_infra_agent", ""), http.StatusForbidden, "session_not_in_project", "GET /v1/interests/ses_infra_agent")
	requireStatus(t, rig.call(t, http.MethodGet, token, "/v1/interests/"+reader, ""), http.StatusOK, "GET /v1/interests/"+reader)
	for _, path := range []string{"/v1/interests/ses_infra_agent", "/v1/sessions/ses_infra_agent"} {
		if recorder := rig.call(t, http.MethodDelete, token, path, ""); recorder.Code != http.StatusForbidden {
			t.Errorf("DELETE %s: status %d (%s), want 403", path, recorder.Code, recorder.Body.String())
		}
	}
}

// K6: every listener that verifies broker tokens records the registering token's projects on the
// session's entry, sorted, and [] for a registration that lists none, so the edge's send and read
// rules can read the target's projects. It holds with edge mode off too: the field is K6's for
// every listener, and the API's answers do not carry it.
func TestEdgeSessionEntryRecordsTheRegisteringTokensProjects(t *testing.T) {
	rig := newEdgeRig(t)
	const sessionID = "ses_edge_k6"
	requireStatus(t, rig.subscribe(t, rig.personToken(sessionID, "PRIV"), sessionID), http.StatusOK, "the registration")
	entry, err := rig.deps.sessions.Refresh(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("read %s's entry: %v", sessionID, err)
	}
	if !slices.Equal(entry.Projects, []string{"PRIV"}) {
		t.Fatalf("%s's entry records projects %v, want [PRIV]", sessionID, entry.Projects)
	}
	open := httptest.NewRecorder()
	rig.mux.ServeHTTP(open, httptest.NewRequest(http.MethodPost, "/v1/interests/subscribe", strings.NewReader(consumerRegistration("ses_edge_k6_open", "epoch-1"))))
	requireStatus(t, open, http.StatusOK, "a registration with no broker token")
	stored, err := rig.deps.sessions.Refresh(t.Context(), "ses_edge_k6_open")
	if err != nil {
		t.Fatalf("read ses_edge_k6_open's entry: %v", err)
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Projects == nil || len(stored.Projects) != 0 || !strings.Contains(string(raw), `"projects":[]`) {
		t.Fatalf("a registration with no token records %s, want projects []", raw)
	}
	listed := rig.call(t, http.MethodGet, rig.personToken(sessionID, "PRIV"), "/v1/registry/"+sessionID, "")
	if strings.Contains(listed.Body.String(), `"projects"`) {
		t.Fatalf("GET /v1/registry/%s answers %s, which carries the entry's projects: the API's answers are unchanged", sessionID, listed.Body.String())
	}
}

// E6: an edge listener refuses to start beside any credential that admits a caller by something
// other than a broker token, beside webhooks, and without the whole broker-token group; each
// refusal names the variable. The last row starts the built binary that way.
func TestEdgeRefusesToStartBesideSharedCredentials(t *testing.T) {
	projects := edgeTestProjects(t)
	broker := []string{
		"ENVOY_AGENT_SECRETS_URL=http://broker.example",
		"ENVOY_AGENT_SECRETS_ACCOUNT_TOKEN_FILE=/var/run/secrets/broker/token",
		"ENVOY_PUBLIC_URL=https://edge.example",
	}
	for _, tc := range []struct {
		extra []string
		drop  string
		names string
	}{
		{extra: []string{"ENVOY_API_TOKEN=shared"}, names: "ENVOY_API_TOKEN"},
		{extra: []string{"ENVOY_OIDC_ISSUER=https://issuer.example", "ENVOY_OIDC_AUDIENCE=envoy"}, names: "ENVOY_OIDC_ISSUER"},
		{extra: []string{"ENVOY_OIDC_ACCOUNTS=system:serviceaccount:a:b"}, names: "ENVOY_OIDC_ACCOUNTS"},
		{extra: []string{"ENVOY_OIDC_GOOGLE_AUDIENCE=envoy"}, names: "ENVOY_OIDC_GOOGLE_AUDIENCE"},
		{extra: []string{"ENVOY_WEBHOOKS=github"}, names: "ENVOY_WEBHOOKS"},
		{extra: []string{"ENVOY_GITHUB_WEBHOOK_SECRET=s"}, names: "ENVOY_GITHUB_WEBHOOK_SECRET"},
		{extra: []string{"ENVOY_SLACK_SIGNING_SECRET=s"}, names: "ENVOY_SLACK_SIGNING_SECRET"},
		{extra: []string{"ENVOY_GHOSTWISPR_SIGNING_SECRET=s"}, names: "ENVOY_GHOSTWISPR_SIGNING_SECRET"},
		{drop: "ENVOY_AGENT_SECRETS_URL", names: "ENVOY_AGENT_SECRETS_URL"},
		{drop: "ENVOY_PUBLIC_URL", names: "ENVOY_PUBLIC_URL"},
	} {
		env := slices.DeleteFunc(slices.Clone(broker), func(entry string) bool { return tc.drop != "" && strings.HasPrefix(entry, tc.drop+"=") })
		env = append(env, tc.extra...)
		err := validateEdgeMode(projects, env)
		if err == nil || !strings.Contains(err.Error(), tc.names) {
			t.Errorf("an edge listener of %v with %v: %v, want a refusal naming %s", projects, env, err, tc.names)
		}
	}
	if err := validateEdgeMode(projects, broker); err != nil {
		t.Fatalf("an edge listener with the broker-token group alone: %v, want it to start", err)
	}

	binary := cmdtest.Build(t, "envoy-listener")
	listener := startListenerProcess(t, binary, testnats.URL(t), "edge-refused",
		"ENVOY_EDGE_PROJECTS="+strings.Join(projects, ","))
	select {
	case <-listener.Exited:
	case <-time.After(30 * time.Second):
		t.Fatalf("the listener with ENVOY_EDGE_PROJECTS=%s beside ENVOY_API_TOKEN is still running after 30 s, want a refusal to start:\n%s",
			strings.Join(projects, ","), listener.Output.String())
	}
	if out := listener.Output.String(); listener.Cmd.ProcessState.Success() || !strings.Contains(out, "ENVOY_API_TOKEN") || !strings.Contains(out, "ENVOY_EDGE_PROJECTS") {
		t.Fatalf("the listener with ENVOY_EDGE_PROJECTS beside ENVOY_API_TOKEN exited %v, want a refusal naming both:\n%s", listener.Cmd.ProcessState, out)
	}
}
