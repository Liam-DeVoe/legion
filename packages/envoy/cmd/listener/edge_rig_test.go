package main

// edge_rig_test.go is the edge listener's test rig: the listener as an edge deployment runs it on
// a real bus, with a stand-in for the broker's repository answer, and the helpers the edge rows
// share (edge_mode_test.go, edge_repositories_test.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/oauthwire"
	"github.com/sjawhar/envoy/internal/ciphertoken"
	"github.com/sjawhar/envoy/internal/ciphertoken/ciphertokentest"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/id"
	"github.com/sjawhar/envoy/internal/legionagents"
	"github.com/sjawhar/envoy/internal/metrics"
	"github.com/sjawhar/envoy/internal/session"
	"github.com/sjawhar/envoy/internal/store"
	"github.com/sjawhar/envoy/internal/testwait"
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

// The repository answer every GitHub row starts from, as the broker's route relays Dispatch's:
// PRIV-1 names acme/pub, PRIV-3 names none and is a child of PRIV-2, which names acme/two, so
// PRIV's issues resolve to acme/pub and acme/two; INFRA-1 names acme/priv, which is INFRA's and
// no repository of PRIV's. Which issue resolves where is Dispatch's one rule; the stand-in
// answers its result.
func edgeRepositoryFixture() map[string][]string {
	return map[string][]string{"PRIV": {"acme/pub", "acme/two"}, "INFRA": {"acme/priv"}}
}

// edgeAccountToken is the stand-in broker account the rig's repository reader asks with.
const edgeAccountToken = "listener-account-token"

// repositoryStandIn is the broker's GET /v1/project-repositories as the edge listener reads it:
// the answer of each project it serves, 403 PROJECT_NOT_LISTED for any other, and the status the
// test sets in place of every answer while it sets one.
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
	broker       *ciphertokentest.StubBroker
	caller       *ciphertokentest.Caller
	repositories *repositoryStandIn
	reader       *edgeRepositories
	clock        *edgeClock
	met          *metrics.Registry
	agents       *legionagents.Store
	ticks        chan time.Time
}

func newEdgeRig(t *testing.T) *edgeRig {
	t.Helper()
	return newEdgeRigServing(t, edgeTestProjects(t))
}

// newEdgeRigServing is the rig of a listener serving projects, whatever the test's environment
// says: none is a listener in no edge mode, which the rows that hold every listener use.
func newEdgeRigServing(t *testing.T, projects []string) *edgeRig {
	t.Helper()
	rig := newRelayRigUnowned(t, relayPauseFirst, relayPauseMax)
	// The publish route forwards a role message to its holder through the delivery config main
	// wires and records each confirmation, so the rows that publish to a held role need both.
	withPublishRoute(t, rig.deps)
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
	edge.broker = ciphertokentest.NewStubBroker(t)
	edge.caller = ciphertokentest.NewCaller(t)
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
	answer.ProjectScope = ciphertoken.ProjectScopeNamed
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

// placeElsewhereFollowing is placeElsewhere of a session whose interest row another listener
// stored with topics beside its agent subject.
func (r *edgeRig) placeElsewhereFollowing(t *testing.T, sessionID string, projects []string, topics ...string) {
	t.Helper()
	r.placeElsewhere(t, sessionID, projects...)
	if _, err := r.deps.registry.Upsert(store.Interest{SessionID: sessionID, MachineID: "shared-listener"}, topics); err != nil {
		t.Fatalf("write %s's interest row with %v: %v", sessionID, topics, err)
	}
}

// storeGithubEvent stores a GitHub event of repository, owner/name as GitHub writes it, on the
// subject the intake publishes it on with kind after the repository, its payload naming the
// repository as every GitHub event's payload does (contracts' githubPayload), and returns it.
func (r *edgeRig) storeGithubEvent(t *testing.T, repository, kind string) contracts.Envelope {
	t.Helper()
	owner, name, _ := strings.Cut(repository, "/")
	subject := contracts.GithubRepositoryPrefix(owner, name) + "." + kind
	item := listenerTestEnvelope(subject, "edge-github-"+id.New())
	item.Source = "github"
	item.Payload = fmt.Sprintf(`{"kind":"pr","action":"opened","repo":%q}`, repository)
	if _, err := r.harness.client.JS().Publish(subject, marshalListenerEnvelope(t, item)); err != nil {
		t.Fatalf("store a GitHub event of %s on %s: %v", repository, subject, err)
	}
	return item
}

// withheld is envoy_edge_github_withheld_total{reason=...} as the listener's /metrics renders it,
// -1 for no series.
func (r *edgeRig) withheld(t *testing.T, reason string) int64 {
	t.Helper()
	recorder := httptest.NewRecorder()
	r.met.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return counterValue(t, recorder.Body.String(), `envoy_edge_github_withheld_total{reason="`+reason+`"}`)
}

// edgeAnswer is what a row reads of an edge refusal (listenerwire.EdgeRefusal).
type edgeAnswer struct {
	Reason  string   `json:"reason"`
	Error   string   `json:"error"`
	Refused []string `json:"refused"`
}

// requireEdgeRefusal fails the test unless recorder is status with reason, naming what was asked,
// and returns the refusal.
func requireEdgeRefusal(t *testing.T, recorder *httptest.ResponseRecorder, status int, reason, asked string) edgeAnswer {
	t.Helper()
	var answer edgeAnswer
	_ = json.Unmarshal(recorder.Body.Bytes(), &answer)
	if recorder.Code != status || answer.Reason != reason {
		t.Errorf("%s: status %d reason %q (%s), want %d %s", asked, recorder.Code, answer.Reason, strings.TrimSpace(recorder.Body.String()), status, reason)
	}
	return answer
}

// requireTopicRefusal is requireEdgeRefusal of a registration, whose refusal names exactly the
// refused topics.
func requireTopicRefusal(t *testing.T, recorder *httptest.ResponseRecorder, reason string, refused ...string) {
	t.Helper()
	answer := requireEdgeRefusal(t, recorder, http.StatusForbidden, reason, fmt.Sprintf("a registration refused for %v", refused))
	if recorder.Code == http.StatusForbidden && !slices.Equal(answer.Refused, refused) {
		t.Errorf("the refusal names refused %v (%s), want exactly %v", answer.Refused, strings.TrimSpace(recorder.Body.String()), refused)
	}
}

// requireStatus fails the test unless recorder is status, naming what was asked.
func requireStatus(t *testing.T, recorder *httptest.ResponseRecorder, status int, asked string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("%s: status %d (%s), want %d", asked, recorder.Code, strings.TrimSpace(recorder.Body.String()), status)
	}
}
