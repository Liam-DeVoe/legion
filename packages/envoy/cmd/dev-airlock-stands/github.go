// packages/envoy/cmd/dev-airlock-stands/github.go
//
// The GitHub stand as this command serves it: the stand-in package's GitHub
// (airlocktest.GitHubStand), which mints, gives back and answers a repository read by what each
// token reaches, with what each installation holds set by --installation-permissions, and three
// routes of this command's own that answer by the permissions a token holds. A GitHub profile's
// token is cut to what the installation holds, so what a check reads at these routes is whether
// the cut left a permission in the token or out of it.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/sjawhar/envoy/internal/broker/airlock/airlocktest"
)

// standInCommitSHA is the one commit every repository of the GitHub stand holds, which the report
// names (github_commit_sha): a commit status for it is written, and one for any other commit is
// refused 422, as GitHub refuses a status for a commit the repository does not hold.
const standInCommitSHA = "5fd1c0a5a4e3b2c1d0e9f8a7b6c5d4e3f2a1b0c9"

// permissionName is what a GitHub App permission's name is: lowercase letters and underscores
// (contents, pull_requests, organization_administration).
var permissionName = regexp.MustCompile(`^[a-z_]+$`)

// levelRank orders the levels GitHub gives a permission, so a route asks whether a token holds
// the level it needs or one above it. A level it does not know ranks 0, which holds nothing.
var levelRank = map[string]int{"read": 1, "write": 2, "admin": 3}

// permissionFlags is --installation-permissions: what the App's installation on each owner holds,
// by owner, which replaces the stand-in package's own map (standInGitHubPermissions) for that
// owner alone. Each value is <owner>=<permission>:<level>[,<permission>:<level>...], and an owner
// followed by nothing holds no permission at all.
type permissionFlags struct {
	held map[string]map[string]string
}

func (f permissionFlags) String() string { return "" }

// Set parses one owner's value, refusing an owner named twice, a permission named twice, a name
// that is no permission's and a level GitHub does not give, each by what it refuses: a stand that
// quietly held a misspelt map would answer a check's cut with the wrong installation.
func (f permissionFlags) Set(value string) error {
	owner, list, ok := strings.Cut(value, "=")
	if !ok || owner == "" {
		return errors.New("names no owner: want <owner>=<permission>:<level>[,<permission>:<level>...]")
	}
	if _, twice := f.held[owner]; twice {
		return fmt.Errorf("--installation-permissions names %s twice", owner)
	}
	held := map[string]string{}
	if list != "" {
		for _, entry := range strings.Split(list, ",") {
			permission, level, leveled := strings.Cut(entry, ":")
			switch {
			case !permissionName.MatchString(permission):
				return fmt.Errorf("%q is no permission's name: a permission is lowercase letters and underscores", permission)
			case !leveled:
				return fmt.Errorf("%q names no level: want <permission>:<level>", permission)
			case levelRank[level] == 0:
				return fmt.Errorf("the level %q is none of read, write and admin", level)
			}
			if _, twice := held[permission]; twice {
				return fmt.Errorf("%s names %s twice", owner, permission)
			}
			held[permission] = level
		}
	}
	f.held[owner] = held
	return nil
}

// githubRoutes answers the GitHub stand's requests: the three routes that read what a token
// holds, and every other request as the stand-in package's stand answers it. It is installed on
// two services, the plain github stand the broker's minter reaches and the github-tls stand a
// command behind the proxy reaches, so a token minted at one is spent at the other, as a token
// GitHub minted at its API is spent at that same API.
//
// The stand-in package's stand remembers what each token reaches but not what it holds, so this
// remembers that from the token route's own answer, which names what the token holds, and forgets
// it at the revoke, as the stand forgets the token.
type githubRoutes struct {
	stand *airlocktest.GitHubStand
	// installations is the installation id the App has on each owner, which is how a token
	// request's path names its owner.
	installations map[string]int64

	mu   sync.Mutex
	held map[string]heldToken
}

// heldToken is what one minted token holds: the account it was minted for, the repositories it
// names (none is every repository of the installation) and its permissions.
type heldToken struct {
	owner        string
	repositories []string
	permissions  map[string]string
}

// reaches reports whether the token reaches a repository of an account, without regard to case,
// as GitHub compares logins and names.
func (h heldToken) reaches(owner, repository string) bool {
	if !strings.EqualFold(h.owner, owner) {
		return false
	}
	return repository == "" || len(h.repositories) == 0 ||
		slices.ContainsFunc(h.repositories, func(name string) bool { return strings.EqualFold(name, repository) })
}

func newGitHubRoutes(stand *airlocktest.GitHubStand, installations map[string]int64) *githubRoutes {
	return &githubRoutes{stand: stand, installations: installations, held: map[string]heldToken{}}
}

// A route of this command's own: its method, the path's shape, and the permission and the level
// a token needs to be answered there.
type heldRoute struct {
	name              string
	permission, level string
	// owner, repository and sha are what the path names, the last two empty where it names none.
	owner, repository, sha string
}

// routeOf reads which of the three routes a request is, and false for every other request:
// GET /repos/{owner}/{repo}/keys, a repository's deploy keys, which needs administration; GET
// /orgs/{org}/installations, an organization's installations, which needs
// organization_administration; and POST /repos/{owner}/{repo}/statuses/{sha}, a commit status,
// which needs statuses at write. These are the permissions of GitHub's own REST description for
// each route.
func routeOf(seen airlocktest.Request) (heldRoute, bool) {
	parts := strings.Split(strings.TrimPrefix(seen.Path, "/"), "/")
	switch {
	case seen.Method == http.MethodGet && len(parts) == 4 && parts[0] == "repos" && parts[3] == "keys" && parts[1] != "" && parts[2] != "":
		return heldRoute{name: "keys", permission: "administration", level: "read", owner: parts[1], repository: parts[2]}, true
	case seen.Method == http.MethodGet && len(parts) == 3 && parts[0] == "orgs" && parts[2] == "installations" && parts[1] != "":
		return heldRoute{name: "installations", permission: "organization_administration", level: "read", owner: parts[1]}, true
	case seen.Method == http.MethodPost && len(parts) == 5 && parts[0] == "repos" && parts[3] == "statuses" && parts[1] != "" && parts[2] != "" && parts[4] != "":
		return heldRoute{name: "statuses", permission: "statuses", level: "write", owner: parts[1], repository: parts[2], sha: parts[4]}, true
	}
	return heldRoute{}, false
}

// Answer is the stand's answer to every request: one of the three routes from what the token
// holds, a token request or a revoke as the stand-in package's stand answers it with what the
// answer holds remembered, and anything else as that stand answers it.
func (g *githubRoutes) Answer(w http.ResponseWriter, r *http.Request, seen airlocktest.Request) bool {
	if route, ok := routeOf(seen); ok {
		g.answerHeld(w, seen, route)
		return true
	}
	owner, minting := g.tokenRequestOwner(seen)
	revoking := seen.Method == http.MethodDelete && seen.Path == "/installation/token"
	if !minting && !revoking {
		return g.stand.Answer(w, r, seen)
	}
	answer := &capturedAnswer{header: http.Header{}, status: http.StatusOK}
	g.stand.Answer(answer, r, seen)
	switch {
	case minting && answer.status == http.StatusCreated:
		g.remember(owner, answer.body.Bytes())
	case revoking && answer.status == http.StatusNoContent:
		g.forget(credentialOf(seen))
	}
	answer.writeTo(w)
	return true
}

// tokenRequestOwner is the owner whose installation a token request names
// (POST /app/installations/{id}/access_tokens), and false for any other request or an id no
// owner holds, which the stand answers 404.
func (g *githubRoutes) tokenRequestOwner(seen airlocktest.Request) (string, bool) {
	rest, isToken := strings.CutSuffix(strings.TrimPrefix(seen.Path, "/app/installations/"), "/access_tokens")
	if seen.Method != http.MethodPost || !isToken {
		return "", false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return "", false
	}
	for owner, installation := range g.installations {
		if installation == id {
			return owner, true
		}
	}
	return "", false
}

// remember records what the token a token request was just answered holds: the permissions the
// answer names, and the repositories it names, none where it reaches every one.
func (g *githubRoutes) remember(owner string, body []byte) {
	var minted struct {
		Token        string            `json:"token"`
		Permissions  map[string]string `json:"permissions"`
		Repositories []struct {
			Name string `json:"name"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(body, &minted); err != nil || minted.Token == "" {
		return
	}
	held := heldToken{owner: owner, permissions: minted.Permissions}
	for _, repository := range minted.Repositories {
		held.repositories = append(held.repositories, repository.Name)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.held[minted.Token] = held
}

// forget drops what a given-back token holds.
func (g *githubRoutes) forget(token string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.held, token)
}

// holding is what a credential holds, and false for a credential the stand never minted or one
// the minter gave back.
func (g *githubRoutes) holding(credential string) (heldToken, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	held, minted := g.held[credential]
	return held, minted
}

// answerHeld answers one of the three routes as GitHub does: 401 to a credential it never minted
// or one given back; 404 to a token of another account, or of other repositories, since GitHub
// shows a token nothing it cannot reach; 403 to a token that does not hold the permission at the
// level the route needs; and then the route's own answer, which for a commit status is 422 for a
// commit other than the one every repository holds.
func (g *githubRoutes) answerHeld(w http.ResponseWriter, seen airlocktest.Request, route heldRoute) {
	w.Header().Set("Content-Type", "application/json")
	held, minted := g.holding(credentialOf(seen))
	switch {
	case !minted:
		writeGitHubJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	case !held.reaches(route.owner, route.repository):
		writeGitHubJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	case levelRank[held.permissions[route.permission]] < levelRank[route.level]:
		writeGitHubJSON(w, http.StatusForbidden, map[string]string{
			"message": "Resource not accessible by integration",
			"needs":   route.permission + ":" + route.level,
		})
		return
	}
	switch route.name {
	case "keys":
		writeGitHubJSON(w, http.StatusOK, []any{})
	case "installations":
		var installations []map[string]any
		for owner, id := range g.installations {
			if strings.EqualFold(owner, route.owner) {
				installations = append(installations, map[string]any{"id": id, "account": map[string]string{"login": owner}})
			}
		}
		writeGitHubJSON(w, http.StatusOK, map[string]any{"total_count": len(installations), "installations": installations})
	case "statuses":
		if route.sha != standInCommitSHA {
			writeGitHubJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "No commit found for SHA: " + route.sha})
			return
		}
		var asked struct {
			State   string `json:"state"`
			Context string `json:"context"`
		}
		_ = json.Unmarshal([]byte(seen.Body), &asked)
		writeGitHubJSON(w, http.StatusCreated, map[string]string{"state": asked.State, "context": asked.Context, "sha": route.sha})
	}
}

// writeGitHubJSON writes one answer of the GitHub stand.
func writeGitHubJSON(w http.ResponseWriter, status int, body any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// credentialOf is the credential a request presented in its Authorization header, "Bearer
// <token>" or "token <token>", the scheme read without regard to case, as GitHub reads it; ""
// for none, which no token of the stand's is.
func credentialOf(seen airlocktest.Request) string {
	header := seen.Header.Get("Authorization")
	for _, scheme := range []string{"Bearer ", "token "} {
		if len(header) >= len(scheme) && strings.EqualFold(header[:len(scheme)], scheme) {
			return strings.TrimSpace(header[len(scheme):])
		}
	}
	return ""
}

// capturedAnswer is what the stand-in package's stand answered one request with, held so that
// what it holds is read before it is written on: its header, its status and its body.
type capturedAnswer struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (a *capturedAnswer) Header() http.Header { return a.header }

func (a *capturedAnswer) Write(p []byte) (int, error) { return a.body.Write(p) }

func (a *capturedAnswer) WriteHeader(status int) { a.status = status }

// writeTo writes the answer held on w, as the stand wrote it.
func (a *capturedAnswer) writeTo(w http.ResponseWriter) {
	for name, values := range a.header {
		w.Header()[name] = values
	}
	w.WriteHeader(a.status)
	_, _ = w.Write(a.body.Bytes())
}
