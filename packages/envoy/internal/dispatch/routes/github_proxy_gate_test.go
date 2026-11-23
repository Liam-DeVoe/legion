package routes

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

	"github.com/sjawhar/envoy/internal/ciphertoken"
	"github.com/sjawhar/envoy/internal/ciphertoken/ciphertokentest"
	"github.com/sjawhar/envoy/internal/dispatch/access"
	"github.com/sjawhar/envoy/internal/dispatch/auth"
	"github.com/sjawhar/envoy/internal/dispatch/githubapp/githubapptest"
	"github.com/sjawhar/envoy/internal/dispatch/identity"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/githubtest"
	"github.com/sjawhar/envoy/internal/oidc"
)

// gateRead is the GitHub proxy's read the no-database test makes: one pull request of acme/web.
const gateRead = "/api/github/rest/repos/acme/web/pulls/1"

// A router with no database has no viewer to resolve for the cookie's person, so its proxy gate
// fails closed: a repository the App is installed on answers as one it is not, and GitHub is not
// read.
func TestTheGitHubProxyOfARouterWithNoDatabaseHidesEveryRepository(t *testing.T) {
	fake := &gateGitHub{installed: []string{"acme/web"}}
	_, pem := githubapptest.Key(t)
	people := newMemoryPeople()
	ctx, err := BuildAppContext(AppContextOptions{
		SigningKey:    testSigningKey,
		People:        people,
		Identity:      identity.HeaderIdentity{Header: "X-Dispatch-User", People: people},
		App:           &auth.AppConfig{ClientID: "Iv1.gate", PEM: pem},
		GitHubAPIBase: githubtest.Server(t, githubtest.Spec{Handle: fake.serve}),
		Lifetime:      t.Context(),
	})
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	handler, _ := New(ctx)
	request := httptest.NewRequest(http.MethodGet, "http://dispatch.test"+gateRead, nil)
	request.Header.Set("X-Dispatch-User", "alice@example.com")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "not installed on acme/web") {
		t.Fatalf("a router with no database answered %s as %d %s, want 503 not installed", gateRead, response.Code, response.Body.String())
	}
	if reads := fake.repositoryReads(); len(reads) != 0 {
		t.Fatalf("GitHub was read through a router with no database: %q", reads)
	}
}

// proxiedRepositories are the nine repositories of the proxy gate's seed (seedProxyGate), each one
// way an issue resolves to a repository or none does. The App is installed on every one of them.
var proxiedRepositories = []string{
	"acme/pub", "acme/priv", "acme/privdone", "acme/privparent", "acme/privdefault", "acme/none", "acme/both",
	"acme/oldname", "acme/newname",
}

// uninstalledRepository is a repository the App is not installed on, whose answer every hidden
// repository's answer must equal.
const uninstalledRepository = "acme/uninstalled"

// TestTheGitHubProxyShowsARepositoryByItsIssues (c-proxy-by-issue): the proxy shows a full member a
// repository that an issue of a project the person sees resolves to, and an open viewer also one
// that no project the person does not see owns; it answers every other repository, in status and
// bytes, as one the App is not installed on, reading nothing of it from GitHub. An issue resolves
// through its own repository, its parent's and its project's default, a done issue counts as an
// open one does, and no rename a mapping records is read. A restricted member reads nothing
// through the proxy (R9's exception), so the same rule is read for bob and dave through the
// App-installation route, which holds every caller to the proxy's gate (repositoriesHidden).
//
// alice, a full member no private list names, reads acme/pub, acme/none, acme/both, which PUB-2
// names beside PRIV-5, and acme/newname, which no issue names; carol, on PRIV's and PRIV2's lists,
// reads all nine. bob, a restricted member PRIV's list names, is 403 RESTRICTED_MEMBER on every
// proxy read, GitHub asked nothing, and the App-installation route shows him every acme/priv*
// repository, acme/privparent among them through PRIV-3's parent, and acme/both and acme/oldname,
// and answers every other one not installed, GitHub asked nothing. With every project public, dave,
// a restricted member no list names, is refused alike and shown none of them (R2/R3 in every
// state). The rename row shows the rule reads no rename: alice reads acme/newname and not
// acme/oldname, and bob is shown acme/oldname and not acme/newname. Each proxy read is made through
// a pull request path and a check path.
//
// Four controls, each run on its own, and each time the test fails naming the repository: show
// every repository to an open viewer (alice reads acme/priv); count open issues only (alice reads
// acme/privdone); resolve an issue's own repository only (alice reads acme/privdefault and bob loses
// acme/privparent); compare names through refs.CanonicalRepoSQL (bob is shown acme/newname and alice
// loses it).
func TestTheGitHubProxyShowsARepositoryByItsIssues(t *testing.T) {
	database := seedProxyGate(t)
	fake := &gateGitHub{installed: proxiedRepositories}
	handler := gateRouter(t, database, fake)
	for _, caller := range []struct {
		login string
		reads []string
	}{
		{"alice@example.com", []string{"acme/pub", "acme/none", "acme/both", "acme/newname"}},
		{"carol@example.com", proxiedRepositories},
	} {
		for _, path := range []struct{ name, suffix string }{
			{"pull request", "pulls/1"},
			{"check", "commits/0123abcd/check-runs"},
		} {
			uninstalled := proxyAnswer(t, handler, caller.login, uninstalledRepository, path.suffix)
			if !strings.HasPrefix(uninstalled, `503 "" `) || !strings.Contains(uninstalled, "is not installed on <repo>") {
				t.Fatalf("%s's %s read of %s, which the App is not installed on, answered %s: the comparison proves nothing",
					caller.login, path.name, uninstalledRepository, uninstalled)
			}
			for _, repository := range proxiedRepositories {
				before := len(fake.repositoryReads())
				got := proxyAnswer(t, handler, caller.login, repository, path.suffix)
				read := len(fake.repositoryReads()) > before
				switch {
				case slices.Contains(caller.reads, repository):
					if want := `200 "" {"repository":"<repo>"}`; got != want || !read {
						t.Errorf("%s reads %s through its %s path as %s (GitHub read: %t), want GitHub's answer %s",
							caller.login, repository, path.name, got, read, want)
					}
				case got != uninstalled || read:
					t.Errorf("%s reads %s, which the person may not see, through its %s path as\n\t%s (GitHub read: %t)\nwant what %s, which the App is not installed on, answers:\n\t%s",
						caller.login, repository, path.name, got, read, uninstalledRepository, uninstalled)
				}
			}
		}
	}
	restrictedReads(t, handler, fake, "bob@example.com", []string{"acme/priv", "acme/privdone", "acme/privparent", "acme/privdefault", "acme/both", "acme/oldname"})

	// With every project public, a restricted member still sees only the repositories an issue of
	// a project whose list names them resolves to: dave, on no list, is shown none of the nine.
	if _, err := database.Pool.Exec(context.Background(), `update projects set visibility = 'public'`); err != nil {
		t.Fatalf("make every project public: %v", err)
	}
	restrictedReads(t, handler, fake, "dave@example.com", nil)
}

// restrictedReads holds login, a restricted member, to the proxy's refusal and to the proxy's gate:
// every proxy read of the nine repositories is 403 RESTRICTED_MEMBER with GitHub asked nothing, and
// the App-installation route, which holds the caller to that gate, answers each of shown as
// installed and every other one not installed with GitHub asked nothing.
func restrictedReads(t *testing.T, handler http.Handler, fake *gateGitHub, login string, shown []string) {
	t.Helper()
	for _, repository := range proxiedRepositories {
		before := len(fake.requests())
		if got := proxyAnswer(t, handler, login, repository, "pulls/1"); !strings.HasPrefix(got, `403 "" `) ||
			!strings.Contains(got, `"code":"RESTRICTED_MEMBER"`) || len(fake.requests()) != before {
			t.Errorf("%s, a restricted member, reads %s through the proxy as %s (GitHub asked %q), want 403 RESTRICTED_MEMBER with GitHub asked nothing",
				login, repository, got, fake.requests()[before:])
		}
		before = len(fake.requests())
		got := installationAnswer(t, handler, login, repository)
		asked := len(fake.requests()) > before
		switch {
		case slices.Contains(shown, repository):
			if got != `200 {"installed":true}` || !asked {
				t.Errorf("%s asks whether the App is installed on %s and is answered %s (GitHub asked: %t), want it installed, read from GitHub", login, repository, got, asked)
			}
		case got != `200 {"installed":false}` || asked:
			t.Errorf("%s asks whether the App is installed on %s, which the person may not see, and is answered %s (GitHub asked: %t), want not installed with GitHub asked nothing",
				login, repository, got, asked)
		}
	}
}

// installationAnswer is GET /api/v1/github/app-installation/<repository> as login at the browser,
// answered as "<status> <body>".
func installationAnswer(t *testing.T, handler http.Handler, login, repository string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://dispatch.test/api/v1/github/app-installation/"+repository, nil)
	request.Header.Set("X-Dispatch-User", login)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return fmt.Sprintf("%d %s", response.Code, strings.TrimSpace(response.Body.String()))
}

// seedProxyGate writes the people, projects and issues the proxy gate test reads through: alice and
// carol, full members, bob, a restricted member, and olga, every project's owner; PUB, public;
// PRIV, private, whose list names bob and carol; and PRIV2, private, whose list names carol and not
// bob. PUB-1 names acme/pub; PRIV-1 names acme/priv; PRIV-2 names acme/privdone and is done; PRIV-3
// names none and is a child of PRIV2-1, which names acme/privparent, so only the parent walk gives
// bob that repository; PRIV-4 names none and resolves to PRIV's default repository,
// acme/privdefault; PUB-2 and PRIV-5 both name acme/both; PRIV-6 names acme/oldname. No issue names
// acme/none or acme/newname. seedRepositoryRecords stores the default and the rename.
func seedProxyGate(t *testing.T) *store.Store {
	t.Helper()
	database := storetest.Open(t)
	if _, err := database.Pool.Exec(context.Background(), `
		insert into people (email, member_kind) values
			('alice@example.com', 'full'), ('bob@example.com', 'restricted'), ('carol@example.com', 'full'), ('olga@example.com', 'full'),
			('dave@example.com', 'restricted');
		insert into projects (key, name, default_assignee, visibility) values
			('PUB', 'Public', 'olga@example.com', 'public'),
			('PRIV', 'Private', 'olga@example.com', 'private'),
			('PRIV2', 'Private two', 'olga@example.com', 'private');
		insert into project_access (project_key, principal_kind, principal, added_by) values
			('PRIV', 'person', 'bob@example.com', '{"kind":"user","id":"olga@example.com"}'),
			('PRIV', 'person', 'carol@example.com', '{"kind":"user","id":"olga@example.com"}'),
			('PRIV2', 'person', 'carol@example.com', '{"kind":"user","id":"olga@example.com"}');
		insert into issues (key, project_key, number, title, created_by, rank, parent_key, repo, status) values
			('PUB-1', 'PUB', 1, 'Names acme/pub', '{"kind":"user","id":"olga@example.com"}', 'a', null, 'acme/pub', 'triage'),
			('PUB-2', 'PUB', 2, 'Names acme/both', '{"kind":"user","id":"olga@example.com"}', 'b', null, 'acme/both', 'triage'),
			('PRIV2-1', 'PRIV2', 1, 'Names acme/privparent', '{"kind":"user","id":"olga@example.com"}', 'a', null, 'acme/privparent', 'triage'),
			('PRIV-1', 'PRIV', 1, 'Names acme/priv', '{"kind":"user","id":"olga@example.com"}', 'a', null, 'acme/priv', 'triage'),
			('PRIV-2', 'PRIV', 2, 'Names acme/privdone', '{"kind":"user","id":"olga@example.com"}', 'b', null, 'acme/privdone', 'done'),
			('PRIV-3', 'PRIV', 3, 'Under PRIV2-1', '{"kind":"user","id":"olga@example.com"}', 'c', 'PRIV2-1', null, 'triage'),
			('PRIV-4', 'PRIV', 4, 'Names none', '{"kind":"user","id":"olga@example.com"}', 'd', null, null, 'triage'),
			('PRIV-5', 'PRIV', 5, 'Names acme/both', '{"kind":"user","id":"olga@example.com"}', 'e', null, 'acme/both', 'triage'),
			('PRIV-6', 'PRIV', 6, 'Names acme/oldname', '{"kind":"user","id":"olga@example.com"}', 'f', null, 'acme/oldname', 'triage');
	`); err != nil {
		t.Fatalf("seed the proxy gate: %v", err)
	}
	seedRepositoryRecords(t, database)
	return database
}

// seedRepositoryRecords records PRIV's default repository, acme/privdefault, and a rename of
// acme/oldname to acme/newname, as this build stores both: the default on the project
// (projects.default_repo, since #4457) and the rename a PRIV row of repo_projects with renamed_to.
// A build that stores either elsewhere changes this function alone.
func seedRepositoryRecords(t *testing.T, database *store.Store) {
	t.Helper()
	if _, err := database.Pool.Exec(context.Background(), `
		update projects set default_repo = 'acme/privdefault' where key = 'PRIV';
		insert into repo_projects (repo, project, is_default, renamed_to, created_by) values
			('acme/oldname', 'PRIV', false, 'acme/newname', '{"kind":"user","id":"olga@example.com"}');
	`); err != nil {
		t.Fatalf("record PRIV's default repository and the rename: %v", err)
	}
}

// gateRouter is a router over database whose GitHub is fake, with the App's key, its callers
// named by the X-Dispatch-User header as a person at the browser.
func gateRouter(t *testing.T, database *store.Store, fake *gateGitHub) http.Handler {
	t.Helper()
	_, pem := githubapptest.Key(t)
	people := store.NewPgPeopleStore(database.Pool, testSigningKey, nil)
	ctx, err := BuildAppContext(AppContextOptions{
		SigningKey:    testSigningKey,
		People:        people,
		Identity:      identity.HeaderIdentity{Header: "X-Dispatch-User", People: people},
		Store:         database,
		App:           &auth.AppConfig{ClientID: "Iv1.gate", PEM: pem},
		GitHubAPIBase: githubtest.Server(t, githubtest.Spec{Handle: fake.serve}),
		Lifetime:      t.Context(),
	})
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	handler, background := New(ctx)
	t.Cleanup(background.Wait)
	return handler
}

// proxyAnswer reads repos/<repository>/<suffix> through the proxy as login, and answers it as
// "<status> <Retry-After> <body>", with the repository's own name written as <repo>, the one byte
// of an answer two repositories may differ in.
func proxyAnswer(t *testing.T, handler http.Handler, login, repository, suffix string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://dispatch.test/api/github/rest/repos/"+repository+"/"+suffix, nil)
	request.Header.Set("X-Dispatch-User", login)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return strings.ReplaceAll(fmt.Sprintf("%d %q %s", response.Code, response.Header().Get("Retry-After"),
		strings.TrimSpace(response.Body.String())), repository, "<repo>")
}

// gateGitHub is GitHub for the repositories it names installed: the App's installation on each of
// them, a token for that installation, and every read of a repository, answered with the
// repository's name and recorded. Every request it is sent, the installation lookups and token
// mints included, is recorded apart.
type gateGitHub struct {
	installed []string
	mu        sync.Mutex
	reads     []string
	every     []string
}

func (g *gateGitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.every = append(g.every, r.Method+" "+r.URL.RequestURI())
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodPost && r.URL.Path == "/app/installations/11/access_tokens" {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"token":"ghs_gate","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
		return
	}
	owner, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
	name, path, _ := strings.Cut(rest, "/")
	repository := owner + "/" + name
	if path == "installation" {
		if !slices.Contains(g.installed, repository) {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `{"id":11,"app_slug":"envoy","permissions":{"pull_requests":"read","checks":"read"}}`)
		return
	}
	g.mu.Lock()
	g.reads = append(g.reads, r.Method+" "+r.URL.RequestURI())
	g.mu.Unlock()
	fmt.Fprintf(w, `{"repository":%q}`, repository)
}

// repositoryReads is every read of a repository beyond the installation lookup and the token mint.
func (g *gateGitHub) repositoryReads() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.reads...)
}

// requests is every request GitHub was sent, the installation lookups and token mints included.
func (g *gateGitHub) requests() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.every...)
}

// proxyOrigin is the origin the credential rig's requests are sent to, and the one its broker
// tokens' proofs name.
const proxyOrigin = "http://dispatch.test"

// proxyHeadSHA is the full commit sha the check-runs and workflow-runs reads name.
const proxyHeadSHA = "0123456789abcdef0123456789abcdef01234567"

// proxyShapes are the four reads the proxy forwards (githubapi's readable), each after
// repos/<repository>/.
var proxyShapes = []string{"pulls/1", "issues/1", "commits/" + proxyHeadSHA + "/check-runs", "actions/runs?head_sha=" + proxyHeadSHA}

// sharedProxyToken is the deployment's shared agent token in the credential rig.
const sharedProxyToken = "agent-token"

// credential is one way a request proves its caller, put on the request before it is sent.
type credential func(t *testing.T, request *http.Request)

// cookieOf is login at the browser, as the identity header names a person.
func cookieOf(login string) credential {
	return func(_ *testing.T, request *http.Request) { request.Header.Set("X-Dispatch-User", login) }
}

// bearer is token sent as a bearer: a personal token, the shared agent token or a service
// account's.
func bearer(token string) credential {
	return func(_ *testing.T, request *http.Request) { request.Header.Set("Authorization", "Bearer "+token) }
}

// podVerifier is the service-account token check of a deployment that runs pods: it verifies
// every JWT-shaped bearer as one pod's projected token.
type podVerifier struct{}

func (podVerifier) Verify(context.Context, string) (oidc.Claims, error) {
	return oidc.Claims{Subject: "system:serviceaccount:legion:worker"}, nil
}

// credentialRig is the proxy gate's seed (seedProxyGate) behind a router that resolves every
// credential a deployment takes: a person at the browser (the identity header), a personal token,
// the shared agent token, a pod's service-account token, and a token the secrets broker issued,
// which a stub broker answers. Its sign-in rule admits a restricted member from outside
// workspace.example while a project names them, so a token of bob's is judged by what it holds,
// never refused for whose it is.
type credentialRig struct {
	handler  http.Handler
	database *store.Store
	fake     *gateGitHub
	broker   *ciphertokentest.StubBroker
	caller   *ciphertokentest.Caller
	made     int
}

func newCredentialRig(t *testing.T) *credentialRig {
	t.Helper()
	database := seedProxyGate(t)
	fake := &gateGitHub{installed: proxiedRepositories}
	broker := ciphertokentest.NewStubBroker(t)
	account := filepath.Join(t.TempDir(), "account-token")
	if err := os.WriteFile(account, []byte("dispatch-account"), 0o600); err != nil {
		t.Fatalf("write the account token: %v", err)
	}
	verifier, err := ciphertoken.New(ciphertoken.Options{
		BrokerURL: broker.Server.URL, Account: ciphertoken.NewAccountToken(account), Origins: []string{proxyOrigin},
		Replay: store.NewDPoPProofs(database.Pool), Address: auth.ForwardedAddress, ServesReadOnly: true,
	})
	if err != nil {
		t.Fatalf("build the broker token verifier: %v", err)
	}
	_, pem := githubapptest.Key(t)
	people := store.NewPgPeopleStore(database.Pool, testSigningKey, nil)
	ctx, err := BuildAppContext(AppContextOptions{
		SigningKey: testSigningKey,
		People:     people,
		Identity:   identity.HeaderIdentity{Header: "X-Dispatch-User", People: people},
		Store:      database,
		SignInRule: identity.OutsideRule{
			DomainRule: identity.DomainRule{Domain: "workspace.example"},
			People: identity.ListerFunc(func(ctx context.Context, email string) (bool, error) {
				return access.Listed(ctx, database.Pool, email)
			}),
		},
		AgentTokens:   sharedAgentTokens(t, sharedProxyToken),
		OIDC:          podVerifier{},
		BrokerTokens:  verifier,
		App:           &auth.AppConfig{ClientID: "Iv1.gate", PEM: pem},
		GitHubAPIBase: githubtest.Server(t, githubtest.Spec{Handle: fake.serve}),
		Lifetime:      t.Context(),
	})
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	handler, background := New(ctx)
	t.Cleanup(background.Wait)
	return &credentialRig{handler: handler, database: database, fake: fake, broker: broker, caller: ciphertokentest.NewCaller(t)}
}

// personalToken stores a dsp_ token of owner holding scopes and listing projects, and answers it
// as a bearer.
func (rig *credentialRig) personalToken(t *testing.T, owner string, scopes, projects []string) credential {
	t.Helper()
	rig.made++
	plaintext := "dsp_proxy" + strconv.Itoa(rig.made)
	hash := sha256.Sum256([]byte(plaintext))
	if _, err := rig.database.Pool.Exec(context.Background(),
		`insert into agent_tokens (owner, name, token_hash, prefix, scopes, projects) values ($1, $2, $3, 'dsp_prox', $4, $5)`,
		owner, plaintext, hash[:], append([]string{}, scopes...), append([]string{}, projects...)); err != nil {
		t.Fatalf("store a personal token of %s: %v", owner, err)
	}
	return bearer(plaintext)
}

// brokerToken is a token the stub broker answers as answer, bound to the rig's caller's key, sent
// under the DPoP scheme with a fresh proof for each request.
func (rig *credentialRig) brokerToken(answer ciphertoken.Introspection) credential {
	rig.made++
	token := "broker-token-" + strconv.Itoa(rig.made)
	answer.Active, answer.ClientID, answer.Thumbprint = true, ciphertoken.ExchangeClientID, rig.caller.JKT()
	answer.Session = fmt.Sprintf("ses_%012x", rig.made)
	answer.ExpiresAt = time.Now().Add(time.Hour).Unix()
	rig.broker.Says(token, answer)
	return func(t *testing.T, request *http.Request) {
		signed := rig.caller.Signed(t, request.Method, request.URL.String(), token)
		request.Header.Set("Authorization", signed.Header.Get("Authorization"))
		request.Header.Set("DPoP", signed.Header.Get("DPoP"))
	}
}

// personScoped is the broker's answer for a token acting as person, the one a person's sign-in
// and their machine login trade for.
func personScoped(person string) ciphertoken.Introspection {
	return ciphertoken.Introspection{Person: person, ProjectScope: ciphertoken.ProjectScopePerson, Scope: "dispatch.write"}
}

// proxyRead is one answer of the proxy, or of the App-installation route, to one credential: its
// status, error code and WWW-Authenticate challenge, its body with the repository's own name
// written as <repo>, and how many requests GitHub was sent while it was answered.
type proxyRead struct {
	status          int
	code, challenge string
	body            string
	asked           int
}

// read answers GET path, a read of repository, for who.
func (rig *credentialRig) read(t *testing.T, who credential, path, repository string) proxyRead {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, proxyOrigin+path, nil)
	who(t, request)
	before := len(rig.fake.requests())
	response := httptest.NewRecorder()
	rig.handler.ServeHTTP(response, request)
	var refusal struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &refusal)
	return proxyRead{
		status: response.Code, code: refusal.Code, challenge: response.Header().Get("WWW-Authenticate"),
		body:  strings.ReplaceAll(strings.TrimSpace(response.Body.String()), repository, "<repo>"),
		asked: len(rig.fake.requests()) - before,
	}
}

// proxyPath is the proxy's path for shape of repository.
func proxyPath(repository, shape string) string {
	return "/api/github/rest/repos/" + repository + "/" + shape
}

// TestTheGitHubProxyReadsForACredentialThatHoldsThePersonScope: the proxy resolves the request's
// own credential as every API route does (api.Background's GitHubGate) and reads GitHub for one
// that holds the person scope, under the gate the App-installation route applies to any
// credential. alice, a full member, reads acme/pub, which PUB-1 names, in each of the proxy's four
// shapes, from GitHub, at her browser, with her personal token holding person and with her broker
// token acting as her; acme/priv, which only PRIV's issues name, answers each of the three what
// acme/uninstalled answers, GitHub asked nothing, and the App-installation route answers the same
// credential that the App is installed on acme/pub and not on acme/priv. A token is held to its
// own list: carol's token listing PRIV reads acme/priv, and her token listing nothing reads it as
// not installed, though carol at her browser sees PRIV.
//
// Every other credential is refused before GitHub is asked anything, reading acme/pub, which alice
// reads: no credential 401 NO_IDENTITY; 403 OAUTH_SCOPE_REQUIRED naming person for alice's token
// holding no scope, the shared agent token, a pod's service-account token, a token an operator's
// entry mapped to PUB, bob's personal token holding no scope and bob's broker token acting as him
// (brokerTokenActor gives a restricted member's none); and 403 READ_ONLY_TOKEN for a read-only
// token.
//
// A restricted member's credential of any kind is 403 RESTRICTED_MEMBER, whatever scopes it holds,
// reading acme/priv, which PRIV's list gives bob's viewer: bob mints a token at his browser (201,
// person and approve, the mint's default), and that token is refused, and so is bob at his browser.
//
// Four controls, each run on its own: drop the gate's scope check, and the shared token reads
// acme/pub; hand the proxy a gate that hides nothing, and alice's token reads acme/priv; read the
// cookie alone, as the proxy did before, and alice's tokens are 401 NO_IDENTITY; drop the
// restricted member's refusal, and bob's token and bob at his browser read acme/priv from GitHub.
func TestTheGitHubProxyReadsForACredentialThatHoldsThePersonScope(t *testing.T) {
	rig := newCredentialRig(t)
	const alice, bob, carol = "alice@example.com", "bob@example.com", "carol@example.com"
	readers := []struct {
		name string
		who  credential
	}{
		{"alice at her browser", cookieOf(alice)},
		{"alice's personal token holding person", rig.personalToken(t, alice, []string{"person"}, nil)},
		{"alice's broker token acting as her", rig.brokerToken(personScoped(alice))},
	}
	for _, reader := range readers {
		uninstalled := rig.read(t, reader.who, proxyPath(uninstalledRepository, "pulls/1"), uninstalledRepository)
		if uninstalled.status != http.StatusServiceUnavailable || uninstalled.code != "GITHUB_TOKEN_UNAVAILABLE" {
			t.Fatalf("%s reads %s, which the App is not installed on, as %+v: the comparison proves nothing", reader.name, uninstalledRepository, uninstalled)
		}
		for _, shape := range proxyShapes {
			want := `{"repository":"<repo>"}`
			if strings.HasPrefix(shape, "actions/runs") {
				want = `{"total_count":0,"workflow_runs":null}`
			}
			if got := rig.read(t, reader.who, proxyPath("acme/pub", shape), "acme/pub"); got.status != http.StatusOK || got.body != want || got.asked == 0 {
				t.Errorf("%s reads acme/pub's %s as %+v, want 200 %s read from GitHub", reader.name, shape, got, want)
			}
		}
		if got := rig.read(t, reader.who, proxyPath("acme/priv", "pulls/1"), "acme/priv"); got.status != uninstalled.status || got.body != uninstalled.body || got.asked != 0 {
			t.Errorf("%s reads acme/priv, which only PRIV's issues name, as %+v, want what %s answers, %d %s, with GitHub asked nothing",
				reader.name, got, uninstalledRepository, uninstalled.status, uninstalled.body)
		}
		for _, installation := range []struct{ repository, want string }{
			{"acme/pub", `{"installed":true}`}, {"acme/priv", `{"installed":false}`},
		} {
			if got := rig.read(t, reader.who, "/api/v1/github/app-installation/"+installation.repository, installation.repository); got.status != http.StatusOK || got.body != installation.want {
				t.Errorf("%s asks the App-installation route about %s and is answered %+v, want 200 %s", reader.name, installation.repository, got, installation.want)
			}
		}
	}

	if got := rig.read(t, rig.personalToken(t, carol, []string{"person"}, []string{"PRIV"}), proxyPath("acme/priv", "pulls/1"), "acme/priv"); got.status != http.StatusOK || got.asked == 0 {
		t.Errorf("carol's token listing PRIV reads acme/priv as %+v, want GitHub's 200", got)
	}
	if got := rig.read(t, rig.personalToken(t, carol, []string{"person"}, nil), proxyPath("acme/priv", "pulls/1"), "acme/priv"); got.code != "GITHUB_TOKEN_UNAVAILABLE" || got.asked != 0 {
		t.Errorf("carol's token listing nothing reads acme/priv as %+v, want it not installed, GitHub asked nothing: a token's list decides which private projects it reads", got)
	}

	const scopeChallenge = `Bearer error="insufficient_scope", scope="person"`
	for _, refused := range []struct {
		name      string
		who       credential
		status    int
		code      string
		challenge string
	}{
		{"no credential", func(*testing.T, *http.Request) {}, http.StatusUnauthorized, "NO_IDENTITY", ""},
		{"alice's personal token holding no scope", rig.personalToken(t, alice, nil, nil), http.StatusForbidden, "OAUTH_SCOPE_REQUIRED", scopeChallenge},
		{"the deployment's shared agent token", bearer(sharedProxyToken), http.StatusForbidden, "OAUTH_SCOPE_REQUIRED", scopeChallenge},
		{"a pod's service-account token", bearer("eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJwb2QifQ.signature"), http.StatusForbidden, "OAUTH_SCOPE_REQUIRED", scopeChallenge},
		{"a token an operator's entry mapped to PUB", rig.brokerToken(ciphertoken.Introspection{
			Person: alice, ProjectScope: ciphertoken.ProjectScopeNamed, Projects: []string{"PUB"}, Scope: "dispatch.write",
		}), http.StatusForbidden, "OAUTH_SCOPE_REQUIRED", scopeChallenge},
		{"a read-only token", rig.brokerToken(ciphertoken.Introspection{
			Service: "example-reader", ProjectScope: ciphertoken.ProjectScopeNone, Scope: ciphertoken.ScopeRead,
		}), http.StatusForbidden, "READ_ONLY_TOKEN", ""},
		{"bob's personal token holding no scope", rig.personalToken(t, bob, nil, nil), http.StatusForbidden, "OAUTH_SCOPE_REQUIRED", scopeChallenge},
		{"bob's broker token acting as him", rig.brokerToken(personScoped(bob)), http.StatusForbidden, "OAUTH_SCOPE_REQUIRED", scopeChallenge},
	} {
		got := rig.read(t, refused.who, proxyPath("acme/pub", "pulls/1"), "acme/pub")
		if got.status != refused.status || got.code != refused.code || got.challenge != refused.challenge || got.asked != 0 {
			t.Errorf("%s reads acme/pub as %+v, want %d %s (challenge %q) with GitHub asked nothing", refused.name, got, refused.status, refused.code, refused.challenge)
		}
	}

	mint := httptest.NewRequest(http.MethodPost, proxyOrigin+"/api/v1/me/agent-tokens", strings.NewReader(`{"name":"bob's agent","projects":["PRIV"]}`))
	mint.Header.Set("Content-Type", "application/json")
	mint.Header.Set("X-Dispatch-User", bob)
	minted := httptest.NewRecorder()
	rig.handler.ServeHTTP(minted, mint)
	var token struct {
		Token  string   `json:"token"`
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal(minted.Body.Bytes(), &token); err != nil || minted.Code != http.StatusCreated || token.Token == "" ||
		!slices.Contains(token.Scopes, "person") {
		t.Fatalf("bob's mint at his browser answered %d %s, want 201 and a token holding person: the rows below would prove nothing",
			minted.Code, minted.Body.String())
	}
	for _, restricted := range []struct {
		name string
		who  credential
	}{
		{fmt.Sprintf("bob's personal token minted at his browser, holding %v", token.Scopes), bearer(token.Token)},
		{"bob at his browser", cookieOf(bob)},
	} {
		got := rig.read(t, restricted.who, proxyPath("acme/priv", "pulls/1"), "acme/priv")
		if got.status != http.StatusForbidden || got.code != "RESTRICTED_MEMBER" || got.challenge != "" || got.asked != 0 {
			t.Errorf("%s reads acme/priv, which PRIV's list gives his viewer, as %+v, want 403 RESTRICTED_MEMBER with GitHub asked nothing",
				restricted.name, got)
		}
	}
}
