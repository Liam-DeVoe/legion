package secrets_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sjawhar/envoy/internal/broker/airlock/airlocktest"
	"github.com/sjawhar/envoy/internal/broker/policy"
	"github.com/sjawhar/envoy/internal/broker/secrets"
	"github.com/sjawhar/envoy/internal/githubtest"
	"github.com/sjawhar/envoy/internal/logtest"
)

// tokenRequest is one POST .../access_tokens the stub GitHub received: the installation it named,
// the body it carried ("" for none) and its Content-Type.
type tokenRequest struct {
	installation string
	body         string
	contentType  string
}

// profileGitHub is a stub GitHub with the App installed on Acme-Org (1), ada (2) and outsider (3).
// It lists each installation with what it holds, everyGrant unless a test sets another (setHeld),
// and answers a token request naming a permission its installation does not hold at the level
// asked or above HTTP 422, minting nothing, as GitHub does. It records every request it receives
// and every token request's body, and answers every other token request with answer,
// asGitHubAnswers unless a test sets another, and with headers. It records the bearer of every
// DELETE /installation/token and answers it revokeStatus, 204 unless a test sets another.
type profileGitHub struct {
	server *httptest.Server

	mu           sync.Mutex
	requests     int
	tokens       []tokenRequest
	revoked      []string
	answer       func(installation string, body []byte) (int, any)
	headers      http.Header
	revokeStatus int
	held         map[string]map[string]string
}

// everyGrant is what each installation of the stub holds unless a test sets another: the
// repository permissions a Legion App's installation holds, most of them at write.
var everyGrant = map[string]string{
	"actions": "write", "checks": "write", "contents": "write", "issues": "write", "metadata": "read", "pull_requests": "write", "statuses": "write",
}

// levelRank orders the levels GitHub gives a permission, so the stub refuses a level past the one
// an installation holds.
var levelRank = map[string]int{"read": 1, "write": 2, "admin": 3}

func newProfileGitHub(t *testing.T) *profileGitHub {
	t.Helper()
	g := &profileGitHub{answer: asGitHubAnswers, revokeStatus: http.StatusNoContent}
	g.server = githubtest.Serve(t, githubtest.Spec{Handle: func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.requests++
		answer, headers, revokeStatus, held := g.answer, g.headers, g.revokeStatus, g.held
		g.mu.Unlock()
		holds := func(installation string) map[string]string {
			if grant, set := held[installation]; set {
				return grant
			}
			return everyGrant
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/app/installations":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "account": map[string]string{"login": "Acme-Org"}, "permissions": holds("1")},
				{"id": 2, "account": map[string]string{"login": "ada"}, "permissions": holds("2")},
				{"id": 3, "account": map[string]string{"login": "outsider"}, "permissions": holds("3")},
			})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/app/installations/") && strings.HasSuffix(r.URL.Path, "/access_tokens"):
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read token request body: %v", err)
			}
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/app/installations/"), "/access_tokens")
			g.mu.Lock()
			g.tokens = append(g.tokens, tokenRequest{installation: id, body: string(body), contentType: r.Header.Get("Content-Type")})
			g.mu.Unlock()
			var asked struct {
				Permissions map[string]string `json:"permissions"`
			}
			_ = json.Unmarshal(body, &asked)
			status, reply := http.StatusUnprocessableEntity, any(map[string]string{"message": "The permissions requested are not granted to this installation."})
			if heldAll(holds(id), asked.Permissions) {
				status, reply = answer(id, body)
			}
			for name, values := range headers {
				w.Header()[name] = values
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(reply)
		case r.Method == http.MethodDelete && r.URL.Path == "/installation/token":
			g.mu.Lock()
			g.revoked = append(g.revoked, r.Header.Get("Authorization"))
			g.mu.Unlock()
			w.WriteHeader(revokeStatus)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}})
	return g
}

// heldAll reports whether an installation holding held holds every permission asked names, each
// at the level asked or above.
func heldAll(held, asked map[string]string) bool {
	for permission, level := range asked {
		if levelRank[held[permission]] < levelRank[level] {
			return false
		}
	}
	return true
}

// setHeld makes the stub list installation as holding held, and refuse every later token request
// for it that names a permission held does not hold at that level.
func (g *profileGitHub) setHeld(installation string, held map[string]string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held == nil {
		g.held = map[string]map[string]string{}
	}
	g.held[installation] = held
}

// setAnswer makes the stub answer every later token request with answer.
func (g *profileGitHub) setAnswer(answer func(installation string, body []byte) (int, any)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.answer = answer
}

// setHeaders makes the stub send headers with every later token request's answer.
func (g *profileGitHub) setHeaders(headers http.Header) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.headers = headers
}

// setRevokeStatus makes the stub answer every later revoke with status.
func (g *profileGitHub) setRevokeStatus(status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.revokeStatus = status
}

// takeRevoked returns the Authorization header of every revoke since the last call, and forgets
// them.
func (g *profileGitHub) takeRevoked() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	revoked := g.revoked
	g.revoked = nil
	return revoked
}

// take returns how many requests the stub received and the token requests among them since the
// last take, and forgets them.
func (g *profileGitHub) take() (int, []tokenRequest) {
	g.mu.Lock()
	defer g.mu.Unlock()
	requests, tokens := g.requests, g.tokens
	g.requests, g.tokens = 0, nil
	return requests, tokens
}

// asGitHubAnswers is the 201 GitHub gives a token request with body: the permissions the body
// names, or every permission the App holds when it names none, and, when it names repositories,
// those alone with the selection "selected"; with none, the installation's own selection, "all".
func asGitHubAnswers(installation string, body []byte) (int, any) {
	var asked struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	_ = json.Unmarshal(body, &asked)
	answer := map[string]any{
		"token": "token-" + installation, "expires_at": "2026-10-23T13:00:00Z", "repository_selection": "all",
		"permissions": map[string]string{"administration": "write", "contents": "write", "metadata": "read"},
	}
	if asked.Permissions != nil {
		answer["permissions"] = asked.Permissions
	}
	if len(asked.Repositories) > 0 {
		var repositories []map[string]string
		for _, name := range asked.Repositories {
			repositories = append(repositories, map[string]string{"name": name, "full_name": "owner/" + name})
		}
		answer["repository_selection"] = "selected"
		answer["repositories"] = repositories
	}
	return http.StatusCreated, answer
}

// readScope is a profile's scope of contents at read on accounts, every repository the
// installation reaches, released as today's JSON of installations.
func readScope(accounts ...string) *secrets.GitHubScope {
	return &secrets.GitHubScope{Accounts: accounts, Permissions: map[string]string{"contents": "read"}, Value: policy.ValueInstallations}
}

// TestAProfileMintAsksForItsPermissionsCutToTheInstallation: a profile's token is asked for with
// the profile's permissions cut to what the installation holds, as its listing says: a
// permission the installation holds at a lower level is asked for at that level, and one it
// holds not at all is left out. The release names each entry the cut left out, for each account:
// "<permission>:<level>" for a level left out, "<permission>" for a permission left out whole.
// Every token request names its permissions, so GitHub never grants one the request did not
// name, and it names the scope's repositories, which reach the token request as they are given.
func TestAProfileMintAsksForItsPermissionsCutToTheInstallation(t *testing.T) {
	key, err := testAppKey()
	if err != nil {
		t.Fatal(err)
	}
	github := newProfileGitHub(t)
	github.setHeld("2", map[string]string{"contents": "read", "issues": "write"})
	app := secrets.GitHubApp{Base: github.server.URL, HTTP: github.server.Client()}
	value := githubAppValue(t, "123456", key)
	profile := map[string]string{"contents": "write", "issues": "read", "members": "read"}

	for _, tc := range []struct {
		name         string
		repositories []string
		body         map[string]any
	}{
		{"every repository the installation reaches", nil, map[string]any{"permissions": map[string]any{"contents": "read", "issues": "read"}}},
		{"named repositories", []string{"lean", "proofs"}, map[string]any{"permissions": map[string]any{"contents": "read", "issues": "read"}, "repositories": []any{"lean", "proofs"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			github.take()
			minted, err := app.Mint(t.Context(), secrets.MintRequest{Value: value, GitHub: &secrets.GitHubScope{
				Accounts: []string{"ada"}, Repositories: tc.repositories, Permissions: profile, Value: policy.ValueInstallations,
			}})
			if err != nil {
				t.Fatalf("Mint: %v", err)
			}
			_, tokens := github.take()
			if len(tokens) != 1 || tokens[0].installation != "2" || tokens[0].contentType != "application/json" {
				t.Fatalf("token requests = %+v, want one JSON request for ada's installation (2)", tokens)
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(tokens[0].body), &body); err != nil {
				t.Fatalf("token request body %q: %v", tokens[0].body, err)
			}
			if !reflect.DeepEqual(body, tc.body) {
				t.Fatalf("token request body = %v, want %v", body, tc.body)
			}
			if want := map[string][]string{"ada": {"contents:write", "members"}}; !reflect.DeepEqual(minted.LeftOut, want) {
				t.Fatalf("LeftOut = %v, want %v", minted.LeftOut, want)
			}
			var got mintedGitHubApp
			if err := json.Unmarshal([]byte(minted.Value), &got); err != nil || got.Installations["ada"].Token != "token-2" || len(got.Installations) != 1 {
				t.Fatalf("Mint value = %q (%v), want ada's installation alone", minted.Value, err)
			}
		})
	}

	github.take()
	whole, err := app.Mint(t.Context(), secrets.MintRequest{Value: value, GitHub: &secrets.GitHubScope{
		Accounts: []string{"acme-org"}, Permissions: map[string]string{"contents": "write", "issues": "read"}, Value: policy.ValueInstallations,
	}})
	if err != nil || whole.LeftOut != nil {
		t.Fatalf("Mint of a profile the installation holds whole = LeftOut %v, %v; want nothing left out", whole.LeftOut, err)
	}
	if _, tokens := github.take(); len(tokens) != 1 || tokens[0].body != `{"permissions":{"contents":"write","issues":"read"}}` {
		t.Fatalf("token requests = %+v, want one naming the profile's permissions as they are", tokens)
	}
}

// TestAnEmptyCutRefusesWithNoRequestToGitHub: a profile whose permissions the installation holds
// none of is refused as the caller's scope (ErrMintNarrowingRefused) before any token request,
// since a request naming no permissions is one GitHub answers with every permission the
// installation holds. Where the scope names two accounts and one of them leaves an empty cut, no
// token is asked for either, so nothing is minted that the release would then hand to nobody.
func TestAnEmptyCutRefusesWithNoRequestToGitHub(t *testing.T) {
	key, err := testAppKey()
	if err != nil {
		t.Fatal(err)
	}
	github := newProfileGitHub(t)
	github.setHeld("2", map[string]string{"issues": "write"})
	app := secrets.GitHubApp{Base: github.server.URL, HTTP: github.server.Client()}
	value := githubAppValue(t, "123456", key)

	for _, tc := range []struct {
		name     string
		accounts []string
	}{
		{"one account", []string{"ada"}},
		{"two accounts, the second's cut empty", []string{"acme-org", "ada"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			github.take()
			minted, err := app.Mint(t.Context(), secrets.MintRequest{Value: value, GitHub: readScope(tc.accounts...)})
			if minted.Value != "" || !errors.Is(err, secrets.ErrMintNarrowingRefused) || !strings.Contains(err.Error(), "ada") || !strings.Contains(err.Error(), "contents") {
				t.Fatalf("Mint with an empty cut = %q, %v; want ErrMintNarrowingRefused naming the account and the permissions it asked for", minted.Value, err)
			}
			if _, tokens := github.take(); len(tokens) != 0 {
				t.Fatalf("token requests %+v for an empty cut, want none", tokens)
			}
			if revoked := github.takeRevoked(); len(revoked) != 0 {
				t.Fatalf("revokes %q for an empty cut, want none", revoked)
			}
		})
	}
}

// TestATokenPastTheRequestIsRefusedAndRevokedForEveryProfile: Mint releases a token only when
// GitHub's own answer describes no more than the request asked: no permission it did not name,
// none at a level above the one it named, and not none named; for repositories, a selection of
// those alone, named. Anything else (a body something between dropped or widened) is refused as
// GitHub's fault (class upstream), for a read profile and a write profile alike, and the token
// GitHub minted is revoked at GitHub with DELETE /installation/token, as its own bearer, before
// Mint returns. The error names which rule failed and never a value GitHub's answer holds, nor the
// token. metadata at read is the one grant past the request it accepts: GitHub grants it with any
// repository permission. A repository GitHub names in another case than asked is the same
// repository.
func TestATokenPastTheRequestIsRefusedAndRevokedForEveryProfile(t *testing.T) {
	key, err := testAppKey()
	if err != nil {
		t.Fatal(err)
	}
	value := githubAppValue(t, "123456", key)
	read := map[string]string{"contents": "read", "pull_requests": "read"}
	write := map[string]string{"contents": "write", "pull_requests": "write"}

	t.Run("the stand-in's widened token", func(t *testing.T) {
		service, stand := gitHubCheckStand(t)
		stand.Answers(airlocktest.GitHubAnswer{Mode: airlocktest.GitHubTokensWider})
		app := secrets.GitHubApp{Base: service.URL(), HTTP: http.DefaultClient}
		for _, profile := range []struct {
			name        string
			permissions map[string]string
		}{{"a read profile", read}, {"a write profile", write}} {
			t.Run(profile.name, func(t *testing.T) {
				seen := len(service.Requests())
				minted, err := app.Mint(t.Context(), secrets.MintRequest{Value: value, GitHub: &secrets.GitHubScope{
					Accounts: []string{"ada"}, Repositories: []string{"lean"}, Permissions: profile.permissions, Value: policy.ValueInstallations,
				}})
				if minted.Value != "" || secrets.MintErrorClass(err) != "upstream" {
					t.Fatalf("Mint of a widened token = %q, %v (class %s), want nothing released and class upstream", minted.Value, err, secrets.MintErrorClass(err))
				}
				var revokes int
				for _, request := range service.Requests()[seen:] {
					if request.Method == http.MethodDelete && request.Path == "/installation/token" {
						revokes++
					}
				}
				if revokes != 1 {
					t.Fatalf("Mint sent %d revokes of the widened token, want one", revokes)
				}
			})
		}
	})

	github := newProfileGitHub(t)
	app := secrets.GitHubApp{Base: github.server.URL, HTTP: github.server.Client()}
	answering := func(change func(map[string]any)) func(string, []byte) (int, any) {
		return func(installation string, body []byte) (int, any) {
			status, answer := asGitHubAnswers(installation, body)
			change(answer.(map[string]any))
			return status, answer
		}
	}
	scope := func(permissions map[string]string, repositories ...string) *secrets.GitHubScope {
		return &secrets.GitHubScope{Accounts: []string{"ada"}, Repositories: repositories, Permissions: permissions, Value: policy.ValueInstallations}
	}
	// Every value a wider answer below holds that the request did not, so that no error quotes one.
	gitHubValues := []string{"administration", "secrets", `"all"`, "SECRETS", "admin"}

	for _, tc := range []struct {
		name     string
		scope    *secrets.GitHubScope
		change   func(map[string]any)
		mentions string
	}{
		{"write contents, to a read profile", scope(read), func(a map[string]any) {
			a["permissions"] = map[string]string{"contents": "write", "pull_requests": "read"}
		}, "a level past the request"},
		{"admin contents, to a write profile", scope(write), func(a map[string]any) {
			a["permissions"] = map[string]string{"contents": "admin", "pull_requests": "write"}
		}, "a level past the request"},
		{"another permission, to a read profile", scope(read), func(a map[string]any) {
			a["permissions"] = map[string]string{"administration": "read", "contents": "read", "pull_requests": "read"}
		}, "a permission the request did not name"},
		{"another permission, to a write profile", scope(write), func(a map[string]any) {
			a["permissions"] = map[string]string{"administration": "write", "contents": "write", "pull_requests": "write"}
		}, "a permission the request did not name"},
		{"metadata at write", scope(read), func(a map[string]any) {
			a["permissions"] = map[string]string{"contents": "read", "metadata": "write"}
		}, "a permission the request did not name"},
		{"no permissions named", scope(write), func(a map[string]any) { delete(a, "permissions") }, "names no permissions"},
		{"every repository", scope(read, "proofs"), func(a map[string]any) { a["repository_selection"] = "all"; delete(a, "repositories") }, "repository selection"},
		{"another repository", scope(write, "proofs"), func(a map[string]any) {
			a["repositories"] = []map[string]string{{"name": "proofs"}, {"name": "SECRETS"}}
		}, "a repository the request does not name"},
		{"no repositories named", scope(read, "proofs"), func(a map[string]any) { delete(a, "repositories") }, "names no repositories"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			github.setAnswer(answering(tc.change))
			release, err := app.Mint(t.Context(), secrets.MintRequest{Value: value, GitHub: tc.scope})
			if release.Value != "" || secrets.MintErrorClass(err) != "upstream" || !strings.Contains(err.Error(), tc.mentions) || strings.Contains(err.Error(), "token-") {
				t.Fatalf("Mint = %q, %v (class %s), want nothing released, class upstream naming %q and no token", release.Value, err, secrets.MintErrorClass(err), tc.mentions)
			}
			for _, quoted := range gitHubValues {
				if strings.Contains(err.Error(), quoted) {
					t.Fatalf("Mint = %v quotes %q from GitHub's answer", err, quoted)
				}
			}
			if revoked := github.takeRevoked(); !reflect.DeepEqual(revoked, []string{"Bearer token-2"}) {
				t.Fatalf("revokes at GitHub = %q, want one DELETE /installation/token as the refused token", revoked)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		scope  *secrets.GitHubScope
		change func(map[string]any)
	}{
		{"the repository in GitHub's own case", scope(read, "proofs"), func(a map[string]any) { a["repositories"] = []map[string]string{{"name": "Proofs"}} }},
		{"metadata at read beside the request", scope(read), func(a map[string]any) {
			a["permissions"] = map[string]string{"contents": "read", "metadata": "read", "pull_requests": "read"}
		}},
		{"less than the request", scope(write), func(a map[string]any) { a["permissions"] = map[string]string{"contents": "read"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			github.setAnswer(answering(tc.change))
			if release, err := app.Mint(t.Context(), secrets.MintRequest{Value: value, GitHub: tc.scope}); err != nil || !strings.Contains(release.Value, "token-2") {
				t.Fatalf("Mint = %q, %v; want ada's token", release.Value, err)
			}
			if revoked := github.takeRevoked(); len(revoked) != 0 {
				t.Fatalf("revokes of a released token = %q, want none", revoked)
			}
		})
	}
}

// TestAValueTokenProfileReleasesTheBareToken: a profile whose value is policy.ValueToken releases
// the one account's token itself, not today's JSON of installations, with that token's expiry.
func TestAValueTokenProfileReleasesTheBareToken(t *testing.T) {
	key, err := testAppKey()
	if err != nil {
		t.Fatal(err)
	}
	github := newProfileGitHub(t)
	app := secrets.GitHubApp{Base: github.server.URL, HTTP: github.server.Client()}
	minted, err := app.Mint(t.Context(), secrets.MintRequest{Value: githubAppValue(t, "123456", key), GitHub: &secrets.GitHubScope{
		Accounts: []string{"ada"}, Permissions: map[string]string{"contents": "read"}, Value: policy.ValueToken,
	}})
	if err != nil || minted.Value != "token-2" {
		t.Fatalf("Mint of a value-token profile = %q, %v; want ada's token alone", minted.Value, err)
	}
	if want := "2026-10-23T13:00:00Z"; minted.Expires.UTC().Format("2006-01-02T15:04:05Z") != want {
		t.Fatalf("Mint expires = %v, want the token's own, %s", minted.Expires, want)
	}
}

// TestAValueTokenProfileWithTwoAccountsIsRefused: the bare token is one account's, so a
// value-token scope of more than one account is refused as the caller's scope
// (ErrMintNarrowingRefused), naming the accounts, before GitHub is asked anything.
func TestAValueTokenProfileWithTwoAccountsIsRefused(t *testing.T) {
	key, err := testAppKey()
	if err != nil {
		t.Fatal(err)
	}
	github := newProfileGitHub(t)
	app := secrets.GitHubApp{Base: github.server.URL, HTTP: github.server.Client()}
	minted, err := app.Mint(t.Context(), secrets.MintRequest{Value: githubAppValue(t, "123456", key), GitHub: &secrets.GitHubScope{
		Accounts: []string{"acme-org", "ada"}, Permissions: map[string]string{"contents": "read"}, Value: policy.ValueToken,
	}})
	if minted.Value != "" || !errors.Is(err, secrets.ErrMintNarrowingRefused) || !strings.Contains(err.Error(), "acme-org") || !strings.Contains(err.Error(), "ada") {
		t.Fatalf("Mint of a value-token profile of two accounts = %q, %v; want ErrMintNarrowingRefused naming both accounts", minted.Value, err)
	}
	if requests, _ := github.take(); requests != 0 {
		t.Fatalf("GitHub received %d requests, want none", requests)
	}
}

// TestAMintWithNoProfileScopeIsRefused: every GitHub App token the broker mints is a profile's
// (policy.ReasonGitHubAppNoProfile), so a request that carries no scope is a programming error
// in the broker, refused (class refused) before GitHub is asked anything, for a release and a
// check alike.
func TestAMintWithNoProfileScopeIsRefused(t *testing.T) {
	key, err := testAppKey()
	if err != nil {
		t.Fatal(err)
	}
	github := newProfileGitHub(t)
	app := secrets.GitHubApp{Base: github.server.URL, HTTP: github.server.Client()}
	for _, check := range []bool{false, true} {
		minted, err := app.Mint(t.Context(), secrets.MintRequest{Value: githubAppValue(t, "123456", key), Check: check})
		if minted.Value != "" || secrets.MintErrorClass(err) != "refused" || !strings.Contains(err.Error(), "profile") {
			t.Fatalf("Mint (check %t) with no scope = %q, %v (class %s); want class refused naming the profile", check, minted.Value, err, secrets.MintErrorClass(err))
		}
	}
	if requests, _ := github.take(); requests != 0 {
		t.Fatalf("GitHub received %d requests, want none", requests)
	}
}

// TestAProfileScopeOfRepositoriesNamesOneAccount: a scope's repositories are names without their
// account, so a scope naming repositories mints for one account; one naming more is refused as
// the caller's scope before GitHub is asked anything, rather than asking each account for names
// another account holds.
func TestAProfileScopeOfRepositoriesNamesOneAccount(t *testing.T) {
	key, err := testAppKey()
	if err != nil {
		t.Fatal(err)
	}
	github := newProfileGitHub(t)
	app := secrets.GitHubApp{Base: github.server.URL, HTTP: github.server.Client()}
	minted, err := app.Mint(t.Context(), secrets.MintRequest{Value: githubAppValue(t, "123456", key), GitHub: &secrets.GitHubScope{
		Accounts: []string{"acme-org", "ada"}, Repositories: []string{"lean"}, Permissions: map[string]string{"contents": "read"}, Value: policy.ValueInstallations,
	}})
	if minted.Value != "" || !errors.Is(err, secrets.ErrMintNarrowingRefused) || !strings.Contains(err.Error(), "--target") {
		t.Fatalf("Mint of repositories on two accounts = %q, %v; want ErrMintNarrowingRefused naming --target", minted.Value, err)
	}
	if requests, _ := github.take(); requests != 0 {
		t.Fatalf("GitHub received %d requests, want none", requests)
	}
}

// TestAProfileAccountWithNoInstallationMintsNothing: an account the scope names that the App is
// not installed on gets no token request, and a scope none of whose accounts has an installation
// is refused as the App with no installation there (ErrMintNoInstallation, class refused).
func TestAProfileAccountWithNoInstallationMintsNothing(t *testing.T) {
	key, err := testAppKey()
	if err != nil {
		t.Fatal(err)
	}
	github := newProfileGitHub(t)
	app := secrets.GitHubApp{Base: github.server.URL, HTTP: github.server.Client()}
	value := githubAppValue(t, "123456", key)

	_, err = app.Mint(t.Context(), secrets.MintRequest{Value: value, GitHub: readScope("grace")})
	if !errors.Is(err, secrets.ErrMintNoInstallation) || secrets.MintErrorClass(err) != "refused" || !strings.Contains(err.Error(), "grace") || !strings.Contains(err.Error(), "has no installation on") {
		t.Fatalf("Mint for an account with no installation = %v (class %s); want ErrMintNoInstallation naming the account", err, secrets.MintErrorClass(err))
	}
	if _, tokens := github.take(); len(tokens) != 0 {
		t.Fatalf("token requests %v for an account with no installation, want none", tokens)
	}

	minted, err := app.Mint(t.Context(), secrets.MintRequest{Value: value, GitHub: readScope("ada", "grace")})
	if err != nil || !strings.Contains(minted.Value, "token-2") {
		t.Fatalf("Mint for ada and grace = %q, %v; want ada's token", minted.Value, err)
	}
	if _, tokens := github.take(); len(tokens) != 1 || tokens[0].installation != "2" {
		t.Fatalf("token requests = %+v, want ada's alone", tokens)
	}
}

// TestGitHubsRefusalOfAProfilesTokenNamesTheLikelyCause: GitHub's 422 to a profile's token
// request is the caller's scope (class narrowing), and the error names what it likely got wrong:
// the repositories it names, or the permissions it asks for. No error quotes GitHub's own text.
func TestGitHubsRefusalOfAProfilesTokenNamesTheLikelyCause(t *testing.T) {
	key, err := testAppKey()
	if err != nil {
		t.Fatal(err)
	}
	github := newProfileGitHub(t)
	github.setAnswer(func(string, []byte) (int, any) {
		return http.StatusUnprocessableEntity, map[string]string{"message": "There is at least one repository that does not exist or is not accessible to the parent installation."}
	})
	app := secrets.GitHubApp{Base: github.server.URL, HTTP: github.server.Client()}
	value := githubAppValue(t, "123456", key)

	for _, tc := range []struct {
		name     string
		scope    *secrets.GitHubScope
		mentions []string
	}{
		{"repositories", &secrets.GitHubScope{Accounts: []string{"ada"}, Repositories: []string{"proofs", "widgets"}, Permissions: map[string]string{"contents": "read"}, Value: policy.ValueInstallations},
			[]string{"422", "ada", "proofs, widgets"}},
		{"every repository", readScope("ada"), []string{"422", "ada", "contents: read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := app.Mint(t.Context(), secrets.MintRequest{Value: value, GitHub: tc.scope})
			if secrets.MintErrorClass(err) != "narrowing" || !errors.Is(err, secrets.ErrMintNarrowingRefused) {
				t.Fatalf("Mint = %v (class %s), want class narrowing", err, secrets.MintErrorClass(err))
			}
			for _, want := range tc.mentions {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Mint = %v, want it to name %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "parent installation") {
				t.Fatalf("Mint = %v quotes GitHub's own text", err)
			}
			if errors.Is(err, secrets.ErrMintEmptyCut) {
				t.Fatalf("Mint = %v reads as an empty cut; GitHub refused a request that named permissions", err)
			}
		})
	}
}

// TestGitHubAppMintKeepsARateLimited422Upstream: a 422 GitHub answers because the App is rate
// limited (X-RateLimit-Remaining: 0, or a secondary-rate-limit message) is no verdict on the
// scope, so it stays the upstream class an operator watches, not the caller's scope, and its
// error names neither the repositories as a likely cause nor GitHub's own text. Neither names a
// wait, which counts as a minute, so the Retry ends the mint at its one token request.
func TestGitHubAppMintKeepsARateLimited422Upstream(t *testing.T) {
	key, err := testAppKey()
	if err != nil {
		t.Fatal(err)
	}
	github := newProfileGitHub(t)
	retry, clock, _ := testRetry(atTheTop)
	app := secrets.GitHubApp{Base: github.server.URL, HTTP: github.server.Client(), Retry: retry}
	value := githubAppValue(t, "123456", key)
	scope := &secrets.GitHubScope{Accounts: []string{"ada"}, Repositories: []string{"proofs"}, Permissions: map[string]string{"contents": "read"}, Value: policy.ValueInstallations}

	for _, tc := range []struct {
		name    string
		headers http.Header
		message string
	}{
		{"no requests left", http.Header{"X-Ratelimit-Remaining": {"0"}}, "Validation Failed"},
		{"a secondary rate limit", nil, "You have exceeded a secondary rate limit. Please wait a few minutes before you try again."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			github.setHeaders(tc.headers)
			github.setAnswer(func(string, []byte) (int, any) {
				return http.StatusUnprocessableEntity, map[string]string{"message": tc.message}
			})
			github.take()
			_, err := app.Mint(t.Context(), secrets.MintRequest{Value: value, GitHub: scope})
			if secrets.MintErrorClass(err) != "upstream" || !strings.Contains(err.Error(), "rate limit") || strings.Contains(err.Error(), "proofs") || strings.Contains(err.Error(), "few minutes") {
				t.Fatalf("Mint = %v (class %s), want class upstream naming the rate limit, not the scope or GitHub's text", err, secrets.MintErrorClass(err))
			}
			if _, tokens := github.take(); len(tokens) != 1 || secrets.MintAttempts(err) != 1 {
				t.Fatalf("Mint made %d token requests (%d attempts); want one, since a rate limit naming no wait ends the mint", len(tokens), secrets.MintAttempts(err))
			}
		})
	}
	if waits := clock.slept(); len(waits) != 0 {
		t.Fatalf("the mints waited %v; want no wait", waits)
	}
}

// TestGitHubAppMintStillRefusesWhenGitHubDoesNotRevoke: a revoke GitHub does not confirm (here a
// 500, which the Retry asks three times, all of them the token itself) is logged at WARN, never
// naming the token, and the wider token is still refused.
func TestGitHubAppMintStillRefusesWhenGitHubDoesNotRevoke(t *testing.T) {
	var logged bytes.Buffer
	logtest.SetDefault(t, slog.NewTextHandler(&logged, nil))
	key, err := testAppKey()
	if err != nil {
		t.Fatal(err)
	}
	github := newProfileGitHub(t)
	github.setRevokeStatus(http.StatusInternalServerError)
	github.setAnswer(func(installation string, body []byte) (int, any) {
		status, answer := asGitHubAnswers(installation, body)
		answer.(map[string]any)["permissions"] = map[string]string{"contents": "write", "metadata": "read"}
		return status, answer
	})
	retry, _, _ := testRetry(atTheTop)
	app := secrets.GitHubApp{Base: github.server.URL, HTTP: github.server.Client(), Retry: retry}

	release, err := app.Mint(t.Context(), secrets.MintRequest{Value: githubAppValue(t, "123456", key), GitHub: readScope("ada")})
	if release.Value != "" || secrets.MintErrorClass(err) != "upstream" {
		t.Fatalf("Mint = %q, %v (class %s), want nothing released and class upstream", release.Value, err, secrets.MintErrorClass(err))
	}
	if revoked := github.takeRevoked(); !reflect.DeepEqual(revoked, []string{"Bearer token-2", "Bearer token-2", "Bearer token-2"}) {
		t.Fatalf("revokes at GitHub = %q, want three, each as the refused token", revoked)
	}
	if line := logged.String(); !strings.Contains(line, "level=WARN") || !strings.Contains(line, "did not revoke") || strings.Contains(line, "token-2") {
		t.Fatalf("log = %q, want a WARN line that GitHub did not revoke the token, without the token", line)
	}
}

// TestGitHubAppMintReadsARetriedRevokesRefusalAsRevoked: GitHub revokes the token at the first
// DELETE /installation/token and that answer is lost (the connection drops before the 204), so the
// Retry makes the DELETE again, as the token GitHub has just revoked, and GitHub answers it 401, or
// 404 for a token it no longer knows. That is the revoke done: the wider token is still refused,
// and Mint logs no line saying GitHub did not revoke it. A 401 or a 404 to the first DELETE keeps
// its meaning, a revoke GitHub did not confirm, and is logged as one.
func TestGitHubAppMintReadsARetriedRevokesRefusalAsRevoked(t *testing.T) {
	wider := reply{status: http.StatusCreated, body: `{"token":"token-7","expires_at":"2026-11-14T13:00:00Z","permissions":{"contents":"write","metadata":"read"}}`}
	revokedAndLost := reply{lost: true}
	badCredentials := reply{status: http.StatusUnauthorized, body: `{"message":"Bad credentials"}`}
	notFound := reply{status: http.StatusNotFound, body: `{"message":"Not Found"}`}
	for _, tc := range []struct {
		name    string
		revokes []reply
		warned  bool
	}{
		{"the first answer lost, then GitHub's 401", []reply{revokedAndLost, badCredentials}, false},
		{"the first answer lost, then a 404", []reply{revokedAndLost, notFound}, false},
		{"a 401 to the first DELETE", []reply{badCredentials}, true},
		{"a 404 to the first DELETE", []reply{notFound}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logged bytes.Buffer
			logtest.SetDefault(t, slog.NewTextHandler(&logged, nil))
			retry, _, _ := testRetry(atTheBottom)
			up := newScripted(t, append([]reply{installed, wider}, tc.revokes...)...)
			release, err := mintGitHub(t.Context(), t, up, retry, nil)
			if release.Value != "" || secrets.MintErrorClass(err) != "upstream" || !strings.Contains(err.Error(), "a level past the request") {
				t.Fatalf("Mint = %q, %v (class %s); want nothing released and class upstream naming the level", release.Value, err, secrets.MintErrorClass(err))
			}
			var deletes int
			for _, request := range up.requests() {
				if request.method == http.MethodDelete && request.path == "/installation/token" {
					deletes++
				}
			}
			if deletes != len(tc.revokes) {
				t.Fatalf("Mint sent %d revokes; want %d", deletes, len(tc.revokes))
			}
			if warned := strings.Contains(logged.String(), "did not revoke"); warned != tc.warned {
				t.Fatalf("log = %q; a line saying GitHub did not revoke the token: %t, want %t", logged.String(), warned, tc.warned)
			}
		})
	}
}

// TestGitHubAppMintReadsTheAnswerToAScopeAtTheMaximum: GitHub's answer to a token naming
// repositories carries a full repository object for each repository, about 5 KB, and GitHub has
// minted the token by the time it answers, so an answer Mint could not read whole would leave a
// token neither released nor revoked. At secrets.MaxNarrowedRepositories, with each object eight
// times GitHub's size (an answer over 4 MiB, past the 1 MiB other calls read to), Mint reads it,
// checks it and releases the token.
func TestGitHubAppMintReadsTheAnswerToAScopeAtTheMaximum(t *testing.T) {
	key, err := testAppKey()
	if err != nil {
		t.Fatal(err)
	}
	github := newProfileGitHub(t)
	github.setAnswer(func(installation string, body []byte) (int, any) {
		status, answer := asGitHubAnswers(installation, body)
		repositories := answer.(map[string]any)["repositories"].([]map[string]string)
		for _, repository := range repositories {
			repository["description"] = strings.Repeat("x", 40<<10)
		}
		return status, answer
	})
	app := secrets.GitHubApp{Base: github.server.URL, HTTP: github.server.Client()}
	names := make([]string, secrets.MaxNarrowedRepositories)
	for i := range names {
		names[i] = fmt.Sprintf("repo-%03d", i)
	}

	release, err := app.Mint(t.Context(), secrets.MintRequest{Value: githubAppValue(t, "123456", key), GitHub: &secrets.GitHubScope{
		Accounts: []string{"ada"}, Repositories: names, Permissions: map[string]string{"contents": "read"}, Value: policy.ValueInstallations,
	}})
	if err != nil || !strings.Contains(release.Value, "token-2") {
		t.Fatalf("Mint naming %d repositories = %q, %v; want ada's token", len(names), release.Value, err)
	}
	if revoked := github.takeRevoked(); len(revoked) != 0 {
		t.Fatalf("revokes at GitHub = %q, want none", revoked)
	}
}
