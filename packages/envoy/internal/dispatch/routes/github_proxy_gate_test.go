package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/auth"
	"github.com/sjawhar/envoy/internal/dispatch/githubapp/githubapptest"
	"github.com/sjawhar/envoy/internal/dispatch/identity"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/githubtest"
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

// proxiedIDs are the GitHub ids of the seed's repositories, which gateGitHub answers for each name
// and seedProxyGate records beside the name wherever a row names it (recordRepositoryIDs).
var proxiedIDs = map[string]int64{
	"acme/pub": 101, "acme/priv": 102, "acme/privdone": 103, "acme/privparent": 104, "acme/privdefault": 105,
	"acme/none": 106, "acme/both": 107, "acme/oldname": 108, "acme/newname": 109, "acme/privlegacy": 110,
}

// uninstalledRepository is a repository the App is not installed on, whose answer every hidden
// repository's answer must equal.
const uninstalledRepository = "acme/uninstalled"

// TestTheGitHubProxyShowsARepositoryByItsIssues (c-proxy-by-issue): the proxy shows a person a
// repository that an issue of a project the person sees resolves to, and an open viewer also one
// that no project the person does not see owns; it answers every other repository, in status and
// bytes, as one the App is not installed on, reading nothing of it from GitHub but its id. An issue
// resolves through its own repository, its parent's and its project's default, a done issue counts
// as an open one does, and no rename a mapping records is read. Every repository is seeded with its
// GitHub id, in the fake and beside each name a row holds (proxiedIDs).
//
// alice, a full member no private list names, reads acme/pub, acme/none, acme/both, which PUB-2
// names beside PRIV-5, and acme/newname, which no issue names; bob, a restricted member PRIV's list
// names, reads every acme/priv* repository, acme/privparent among them through PRIV-3's parent, and
// acme/both and acme/oldname; carol, on PRIV's and PRIV2's lists, reads all nine. The rename row
// shows the rule reads no rename: alice reads acme/newname and not acme/oldname, and bob reads
// acme/oldname and not acme/newname. Each readable repository is read through a pull request path
// and a check path, and each hidden one through both too.
//
// With every project made public, dave, a restricted member PRIV's list names alone, reads every
// acme/priv* repository and acme/both, and acme/pub, acme/none and acme/newname answer him as not
// installed: the all-public shortcut is an open viewer's alone, so his reads ask GitHub the id. A
// PRIV issue written before Dispatch recorded ids names acme/privlegacy with none, and dave reads
// it by its name. While GitHub does not answer acme/priv's id, dave is answered as not installed
// for it, and the next request, read again, shows it.
//
// Controls, each run on its own, and each time the test fails naming the repository: show every
// repository to an open viewer (alice reads acme/priv); count open issues only (alice reads
// acme/privdone); resolve an issue's own repository only (alice reads acme/privdefault and bob loses
// acme/privparent); compare names through refs.CanonicalRepoSQL (bob reads acme/newname and alice
// loses it); hide a repository whose only issue holds no id (dave loses acme/privlegacy); apply the
// all-public shortcut to every viewer (dave reads acme/pub and acme/none).
func TestTheGitHubProxyShowsARepositoryByItsIssues(t *testing.T) {
	database := seedProxyGate(t)
	fake := &gateGitHub{installed: slices.Concat(proxiedRepositories, []string{"acme/privlegacy"})}
	handler := gateRouter(t, database, fake)
	for _, caller := range []struct {
		login string
		reads []string
	}{
		{"alice@example.com", []string{"acme/pub", "acme/none", "acme/both", "acme/newname"}},
		{"bob@example.com", []string{"acme/priv", "acme/privdone", "acme/privparent", "acme/privdefault", "acme/both", "acme/oldname"}},
		{"carol@example.com", proxiedRepositories},
	} {
		requireReads(t, handler, fake, caller.login, proxiedRepositories, caller.reads)
	}

	// Every project public, and a PRIV issue written before ids were recorded: dave's rows.
	if _, err := database.Pool.Exec(context.Background(), `
		insert into people (email, member_kind) values ('dave@example.com', 'restricted');
		insert into project_access (project_key, principal_kind, principal, added_by) values
			('PRIV', 'person', 'dave@example.com', '{"kind":"user","id":"olga@example.com"}');
		insert into issues (key, project_key, number, title, created_by, rank, parent_key, repo, status) values
			('PRIV-7', 'PRIV', 7, 'Names acme/privlegacy before ids', '{"kind":"user","id":"olga@example.com"}', 'g', null, 'acme/privlegacy', 'triage');
		update projects set visibility = 'public';
	`); err != nil {
		t.Fatalf("seed dave and acme/privlegacy, and make every project public: %v", err)
	}
	everything := slices.Concat(proxiedRepositories, []string{"acme/privlegacy"})
	requireReads(t, handler, fake, "dave@example.com", everything, []string{
		"acme/priv", "acme/privdone", "acme/privparent", "acme/privdefault", "acme/both", "acme/oldname", "acme/privlegacy",
	})
	uninstalled := proxyAnswer(t, handler, "dave@example.com", uninstalledRepository, "pulls/1")
	fake.failID("acme/priv", true)
	if got := proxyAnswer(t, handler, "dave@example.com", "acme/priv", "pulls/1"); got != uninstalled {
		t.Errorf("while GitHub does not answer acme/priv's id, dave reads it as %s, want what %s answers: %s", got, uninstalledRepository, uninstalled)
	}
	fake.failID("acme/priv", false)
	if got := proxyAnswer(t, handler, "dave@example.com", "acme/priv", "pulls/1"); got != `200 "" {"repository":"<repo>"}` {
		t.Errorf("once GitHub answers acme/priv's id again, dave reads it as %s, want GitHub's answer", got)
	}
}

// The repositories of the renamed, transferred and reused rows (seedRenamedGate): each old name is
// one GitHub answers with the repository it moved to, the renamed ones by that repository's id
// (/repositories/<id>) and the transferred ones by its new name (/repos/<owner>/<name>), so the
// proxy's hold is shown to follow whatever location GitHub gives and to predict none.
const (
	idPrivNew      int64 = 9001 // acme/privold, renamed to acme/privnew
	idPrivMoved    int64 = 9002 // acme/privmoved, transferred to other/privmoved
	idPrivNew2     int64 = 9003 // acme/privold2, renamed to acme/privnew2
	idPrivMoved2   int64 = 9004 // acme/privmoved2, transferred to other/privmoved2
	idPrivOld2Took int64 = 9010 // the new repository that takes the name acme/privold2
)

// TestTheGitHubProxyKeepsARenamedOrTransferredPrivateRepositoryHidden (c-proxy-by-repository-id):
// the proxy judges a repository by its GitHub id, the one GitHub answers for the name asked now,
// never by its name, so a private project's repository stays hidden under every name it ever had,
// and a name another repository took later shows that repository.
//
// acme/privold (9001) is renamed to acme/privnew, and acme/privmoved (9002) is transferred to
// other/privmoved; GitHub answers each old name with the repository it moved to, the rename at
// /repositories/9001 and the transfer at /repos/other/privmoved. Only PRIV's issues name them,
// each recorded with its id, and PRIV is private. alice, an open viewer, reads none of the four
// names: each answers her as acme/uninstalled does, in status and bytes. bob and carol read all
// four, each old name through GitHub's redirect to the location the gate's id read reached. With
// GitHub failing acme/privnew's id read, alice is refused it, as a hidden repository is; with PRIV
// public again the same failed read answers her as before any project could be private.
//
// acme/privold2 (9003) is renamed to acme/privnew2, and acme/privmoved2 (9004) is transferred to
// other/privmoved2; a PRIV issue written before Dispatch recorded ids names each by its old name,
// and a PUB issue written then names acme/privnew2. PRIV's owner reads each stored name with
// GitHub's current full name and id (acme/privnew2 9003, other/privmoved2 9004), named_elsewhere 1
// for acme/privold2, and confirms both. Then a new repository (9010) takes the name acme/privold2:
// alice is refused acme/privnew2 and other/privmoved2 and reads acme/privold2, which is no
// project's; bob reads acme/privnew2 and other/privmoved2. The confirm was shared: false, so alice,
// who sees the PUB issue naming acme/privnew2, is refused it. A confirmation of acme/privold2 as
// 9003 now is 409 REPOSITORY_CHANGED. After a PRIV3 issue records 9010, dave, a restricted member
// on PRIV, is refused acme/privold2: his PRIV issue's recorded 9003 does not match 9010. On its
// own seed, the confirm with shared: true records 9003 on the PUB issue too, and alice reads
// acme/privnew2.
//
// Controls, each run on its own, each failing naming the repository: compare names (alice reads
// acme/privnew and other/privmoved); read the stored name's id at request time (after the reuse bob
// loses acme/privnew2 and alice reads it); let shared: true skip the issues of other projects (alice
// is still refused acme/privnew2); match a restricted member by name before the id (dave reads
// acme/privold2); answer a failed id read by the name rule (alice reads acme/privnew); hold a
// redirect to /repositories/<id> alone, the form the hold once predicted (bob and carol lose
// acme/privmoved, which GitHub redirects by name).
func TestTheGitHubProxyKeepsARenamedOrTransferredPrivateRepositoryHidden(t *testing.T) {
	t.Run("renamed and transferred, recorded at their writes", func(t *testing.T) {
		database, fake, handler := seedRenamedGate(t)
		uninstalled := proxyAnswer(t, handler, "alice@example.com", uninstalledRepository, "pulls/1")
		reached := map[string]string{
			"acme/privold": "acme/privnew", "acme/privnew": "acme/privnew",
			"acme/privmoved": "other/privmoved", "other/privmoved": "other/privmoved",
		}
		for _, name := range slices.Sorted(maps.Keys(reached)) {
			if got := proxyAnswer(t, handler, "alice@example.com", name, "pulls/1"); got != uninstalled {
				t.Errorf("alice, an open viewer who does not see PRIV, reads %s as %s, want what %s answers: %s",
					name, got, uninstalledRepository, uninstalled)
			}
			for _, login := range []string{"bob@example.com", "carol@example.com"} {
				requireShown(t, handler, login, name, reached[name])
			}
		}
		fake.failID("acme/privnew", true)
		if got := proxyAnswer(t, handler, "alice@example.com", "acme/privnew", "pulls/1"); got != uninstalled {
			t.Errorf("while GitHub does not answer acme/privnew's id, alice reads it as %s, want it hidden: %s", got, uninstalled)
		}
		// PRIV, and PRIV3 beside it, public again: no project is private, as before any could be.
		setVisibility(t, database, "PRIV", "public")
		setVisibility(t, database, "PRIV3", "public")
		requireShown(t, handler, "alice@example.com", "acme/privnew", "acme/privnew")
	})

	t.Run("renamed, transferred and reused, confirmed by the owner", func(t *testing.T) {
		database, fake, handler := seedRenamedGate(t)
		listed := ownersRepositories(t, handler)
		for _, want := range []struct {
			repo, current string
			id            int64
			named         int
		}{
			{"acme/privold2", "acme/privnew2", idPrivNew2, 1},
			{"acme/privmoved2", "other/privmoved2", idPrivMoved2, 0},
		} {
			row, found := unconfirmedRow(listed, want.repo)
			if !found || row.GitHubID == nil || *row.GitHubID != want.id || row.GitHubFullName == nil || *row.GitHubFullName != want.current ||
				row.NamedElsewhere != want.named {
				t.Fatalf("PRIV's owner reads %s as %+v (found %t), want GitHub's %s, id %d, named_elsewhere %d", want.repo, row, found, want.current, want.id, want.named)
			}
		}
		confirm(t, handler, "acme/privold2", idPrivNew2, false, http.StatusOK)
		confirm(t, handler, "acme/privmoved2", idPrivMoved2, false, http.StatusOK)

		fake.rename("acme/privold2", "", idPrivOld2Took)
		uninstalled := proxyAnswer(t, handler, "alice@example.com", uninstalledRepository, "pulls/1")
		for _, name := range []string{"acme/privnew2", "other/privmoved2"} {
			if got := proxyAnswer(t, handler, "alice@example.com", name, "pulls/1"); got != uninstalled {
				t.Errorf("after the reuse, alice reads %s as %s, want what %s answers: %s", name, got, uninstalledRepository, uninstalled)
			}
			requireShown(t, handler, "bob@example.com", name, name)
		}
		requireShown(t, handler, "alice@example.com", "acme/privold2", "acme/privold2")
		confirm(t, handler, "acme/privold2", idPrivNew2, false, http.StatusConflict)

		if _, err := database.Pool.Exec(context.Background(), `
			insert into issues (key, project_key, number, title, created_by, rank, repo, repo_id, status) values
				('PRIV3-1', 'PRIV3', 1, 'Names the repository that took acme/privold2', '{"kind":"user","id":"olga@example.com"}', 'a', 'acme/privold2', $1, 'triage')
		`, idPrivOld2Took); err != nil {
			t.Fatalf("record 9010 on a PRIV3 issue: %v", err)
		}
		daveUninstalled := proxyAnswer(t, handler, "dave@example.com", uninstalledRepository, "pulls/1")
		if got := proxyAnswer(t, handler, "dave@example.com", "acme/privold2", "pulls/1"); got != daveUninstalled {
			t.Errorf("dave, restricted on PRIV, whose PRIV issue recorded 9003 for acme/privold2, reads the repository that took the name (9010) as %s, want what %s answers: %s",
				got, uninstalledRepository, daveUninstalled)
		}
	})

	t.Run("confirmed as shared", func(t *testing.T) {
		database, _, handler := seedRenamedGate(t)
		confirm(t, handler, "acme/privold2", idPrivNew2, true, http.StatusOK)
		var recorded *int64
		if err := database.Pool.QueryRow(context.Background(), `select repo_id from issues where key = 'PUB-1'`).Scan(&recorded); err != nil {
			t.Fatalf("read PUB-1's id: %v", err)
		}
		if recorded == nil || *recorded != idPrivNew2 {
			t.Errorf("a confirmation of acme/privold2 with shared true left PUB-1, which names acme/privnew2 with no id, holding %v, want %d", recorded, idPrivNew2)
		}
		requireShown(t, handler, "alice@example.com", "acme/privnew2", "acme/privnew2")
	})
}

// seedRenamedGate is the renamed-repository rows' seed and its router: alice and carol, full
// members, bob and dave, restricted members, and olga; PUB, public; PRIV, private, which carol
// owns and whose list names bob, carol and dave; PRIV3, private, whose list names carol alone. PRIV-1
// names acme/privold with 9001 and PRIV-2 acme/privmoved with 9002, each recorded at its write;
// PRIV-3 names acme/privold2 and PRIV-4 acme/privmoved2, and PUB-1 acme/privnew2, each written
// before Dispatch recorded ids. GitHub answers each old name with the repository it moved to.
func seedRenamedGate(t *testing.T) (*store.Store, *gateGitHub, http.Handler) {
	t.Helper()
	database := storetest.Open(t)
	if _, err := database.Pool.Exec(context.Background(), `
		insert into people (email, member_kind) values
			('alice@example.com', 'full'), ('bob@example.com', 'restricted'), ('carol@example.com', 'full'),
			('dave@example.com', 'restricted'), ('olga@example.com', 'full');
		insert into projects (key, name, default_assignee, owner, visibility) values
			('PUB', 'Public', 'olga@example.com', 'olga@example.com', 'public'),
			('PRIV', 'Private', 'olga@example.com', 'carol@example.com', 'private'),
			('PRIV3', 'Private three', 'olga@example.com', 'olga@example.com', 'private');
		insert into project_access (project_key, principal_kind, principal, added_by) values
			('PRIV', 'person', 'bob@example.com', '{"kind":"user","id":"carol@example.com"}'),
			('PRIV', 'person', 'carol@example.com', '{"kind":"user","id":"carol@example.com"}'),
			('PRIV', 'person', 'dave@example.com', '{"kind":"user","id":"carol@example.com"}'),
			('PRIV3', 'person', 'carol@example.com', '{"kind":"user","id":"olga@example.com"}');
		insert into issues (key, project_key, number, title, created_by, rank, repo, repo_id, status) values
			('PUB-1', 'PUB', 1, 'Names acme/privnew2 before ids', '{"kind":"user","id":"olga@example.com"}', 'a', 'acme/privnew2', null, 'triage'),
			('PRIV-1', 'PRIV', 1, 'Names acme/privold', '{"kind":"user","id":"carol@example.com"}', 'a', 'acme/privold', 9001, 'triage'),
			('PRIV-2', 'PRIV', 2, 'Names acme/privmoved', '{"kind":"user","id":"carol@example.com"}', 'b', 'acme/privmoved', 9002, 'triage'),
			('PRIV-3', 'PRIV', 3, 'Names acme/privold2 before ids', '{"kind":"user","id":"carol@example.com"}', 'c', 'acme/privold2', null, 'triage'),
			('PRIV-4', 'PRIV', 4, 'Names acme/privmoved2 before ids', '{"kind":"user","id":"carol@example.com"}', 'd', 'acme/privmoved2', null, 'triage');
	`); err != nil {
		t.Fatalf("seed the renamed repositories: %v", err)
	}
	fake := &gateGitHub{
		installed: []string{"acme/privnew", "other/privmoved", "acme/privnew2", "other/privmoved2"},
		ids: map[string]int64{
			"acme/privnew": idPrivNew, "other/privmoved": idPrivMoved, "acme/privnew2": idPrivNew2, "other/privmoved2": idPrivMoved2,
		},
		moved: map[string]string{
			"acme/privold": "acme/privnew", "acme/privmoved": "other/privmoved",
			"acme/privold2": "acme/privnew2", "acme/privmoved2": "other/privmoved2",
		},
		movedByName: map[string]bool{"acme/privmoved": true, "acme/privmoved2": true},
	}
	return database, fake, gateRouter(t, database, fake)
}

// requireShown holds login's read of a pull request of requested to GitHub's answer, read from
// reached, the repository the name reaches on GitHub now.
func requireShown(t *testing.T, handler http.Handler, login, requested, reached string) {
	t.Helper()
	want := strings.ReplaceAll(fmt.Sprintf(`200 "" {"repository":%q}`, reached), requested, "<repo>")
	if got := proxyAnswer(t, handler, login, requested, "pulls/1"); got != want {
		t.Errorf("%s reads %s as %s, want GitHub's answer for %s: %s", login, requested, got, reached, want)
	}
}

// setVisibility sets project's visibility in its row, as this build's tests seed a private project.
func setVisibility(t *testing.T, database *store.Store, project, visibility string) {
	t.Helper()
	if _, err := database.Pool.Exec(context.Background(), `update projects set visibility = $2 where key = $1`, project, visibility); err != nil {
		t.Fatalf("make %s %s: %v", project, visibility, err)
	}
}

// listedRepository is one row of GET /api/v1/projects/{key}/repositories.
type listedRepository struct {
	Repo            string  `json:"repo"`
	ID              *int64  `json:"id"`
	GitHubID        *int64  `json:"github_id"`
	GitHubFullName  *string `json:"github_full_name"`
	NamedElsewhere  int     `json:"named_elsewhere"`
	SharedElsewhere int     `json:"shared_elsewhere"`
}

// ownersRepositories is PRIV's repositories as its owner carol reads them.
func ownersRepositories(t *testing.T, handler http.Handler) []listedRepository {
	t.Helper()
	response := routerJSON(t, handler, http.MethodGet, "/api/v1/projects/PRIV/repositories", "carol@example.com", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("carol reads PRIV's repositories: %d %s", response.Code, response.Body.String())
	}
	var answer struct {
		Repositories []listedRepository `json:"repositories"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode PRIV's repositories %s: %v", response.Body.String(), err)
	}
	return answer.Repositories
}

// unconfirmedRow is the row of listed that holds repo with no id.
func unconfirmedRow(listed []listedRepository, repo string) (listedRepository, bool) {
	for _, row := range listed {
		if row.Repo == repo && row.ID == nil {
			return row, true
		}
	}
	return listedRepository{}, false
}

// confirm confirms repo as id for PRIV as its owner carol, and holds the answer to status.
func confirm(t *testing.T, handler http.Handler, repo string, id int64, shared bool, status int) {
	t.Helper()
	response := routerJSON(t, handler, http.MethodPost, "/api/v1/projects/PRIV/repositories/confirm", "carol@example.com",
		map[string]any{"repo": repo, "id": id, "shared": shared})
	if response.Code != status {
		t.Fatalf("carol confirms %s as %d (shared %t): %d %s, want %d", repo, id, shared, response.Code, response.Body.String(), status)
	}
}

// routerJSON sends one API request through the router as login at a browser, body encoded as JSON.
func routerJSON(t *testing.T, handler http.Handler, method, target, login string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode %v: %v", body, err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, "http://dispatch.test"+target, reader)
	request.Header.Set("X-Dispatch-User", login)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// requireReads holds login's reads of every repository of repositories, through a pull request
// path and a check path: each of reads answers GitHub's answer, read from GitHub, and every other
// answers what uninstalledRepository answers, with nothing of it read from GitHub but its id.
func requireReads(t *testing.T, handler http.Handler, fake *gateGitHub, login string, repositories, reads []string) {
	t.Helper()
	for _, path := range []struct{ name, suffix string }{
		{"pull request", "pulls/1"},
		{"check", "commits/0123abcd/check-runs"},
	} {
		uninstalled := proxyAnswer(t, handler, login, uninstalledRepository, path.suffix)
		if !strings.HasPrefix(uninstalled, `503 "" `) || !strings.Contains(uninstalled, "is not installed on <repo>") {
			t.Fatalf("%s's %s read of %s, which the App is not installed on, answered %s: the comparison proves nothing",
				login, path.name, uninstalledRepository, uninstalled)
		}
		for _, repository := range repositories {
			before := len(fake.repositoryReads())
			got := proxyAnswer(t, handler, login, repository, path.suffix)
			read := len(fake.repositoryReads()) > before
			switch {
			case slices.Contains(reads, repository):
				if want := `200 "" {"repository":"<repo>"}`; got != want || !read {
					t.Errorf("%s reads %s through its %s path as %s (GitHub read: %t), want GitHub's answer %s",
						login, repository, path.name, got, read, want)
				}
			case got != uninstalled || read:
				t.Errorf("%s reads %s, which the person may not see, through its %s path as\n\t%s (GitHub read: %t)\nwant what %s, which the App is not installed on, answers:\n\t%s",
					login, repository, path.name, got, read, uninstalledRepository, uninstalled)
			}
		}
	}
}

// seedProxyGate writes the people, projects and issues the proxy gate test reads through: alice and
// carol, full members, bob, a restricted member, and olga, every project's owner; PUB, public;
// PRIV, private, whose list names bob and carol; and PRIV2, private, whose list names carol and not
// bob. PUB-1 names acme/pub; PRIV-1 names acme/priv; PRIV-2 names acme/privdone and is done; PRIV-3
// names none and is a child of PRIV2-1, which names acme/privparent, so only the parent walk gives
// bob that repository; PRIV-4 names none and resolves to PRIV's default repository,
// acme/privdefault; PUB-2 and PRIV-5 both name acme/both; PRIV-6 names acme/oldname. No issue names
// acme/none or acme/newname. seedRepositoryRecords stores the default and the rename, and
// recordRepositoryIDs the GitHub id beside each name, as the writes that name them record it.
func seedProxyGate(t *testing.T) *store.Store {
	t.Helper()
	database := storetest.Open(t)
	if _, err := database.Pool.Exec(context.Background(), `
		insert into people (email, member_kind) values
			('alice@example.com', 'full'), ('bob@example.com', 'restricted'), ('carol@example.com', 'full'), ('olga@example.com', 'full');
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
	recordRepositoryIDs(t, database, proxiedIDs)
	return database
}

// seedRepositoryRecords records PRIV's default repository, acme/privdefault, and a rename of
// acme/oldname to acme/newname, as this build stores both: rows of repo_projects, the default
// marked is_default and the rename a PRIV row with renamed_to. A build that stores either
// elsewhere changes this function alone.
func seedRepositoryRecords(t *testing.T, database *store.Store) {
	t.Helper()
	if _, err := database.Pool.Exec(context.Background(), `
		insert into repo_projects (repo, project, is_default, renamed_to, created_by) values
			('acme/privdefault', 'PRIV', true, null, '{"kind":"user","id":"olga@example.com"}'),
			('acme/oldname', 'PRIV', false, 'acme/newname', '{"kind":"user","id":"olga@example.com"}');
	`); err != nil {
		t.Fatalf("record PRIV's default repository and the rename: %v", err)
	}
}

// recordRepositoryIDs records beside each name of ids, wherever an issue's own repository or a
// project's default holds it with no id, that name's GitHub id, as the write that named it records
// it (api's writtenRepository): issues.repo_id and repo_projects.repo_id, as this build stores them.
func recordRepositoryIDs(t *testing.T, database *store.Store, ids map[string]int64) {
	t.Helper()
	for name, id := range ids {
		if _, err := database.Pool.Exec(context.Background(), `
			update issues set repo_id = $2 where lower(repo) = $1 and repo_id is null;
		`, name, id); err != nil {
			t.Fatalf("record %s's id beside the issues that name it: %v", name, err)
		}
		if _, err := database.Pool.Exec(context.Background(), `
			update repo_projects set repo_id = $2 where lower(repo) = $1 and is_default and repo_id is null;
		`, name, id); err != nil {
			t.Fatalf("record %s's id beside the defaults that name it: %v", name, err)
		}
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
// them, a token for that installation, each repository itself with its id and full name, and every
// read of a repository, answered with the repository's name and recorded. A repository's id is
// ids', else proxiedIDs', else 1000 plus its place among installed, compared without case. A name
// moved names is an old one: GitHub answers every request of it with a redirect to the repository
// it moved to, as GitHub answers a renamed or transferred repository's old name: at that
// repository's id, /repositories/<id>, or, for an old name movedByName holds, at its new name,
// /repos/<owner>/<name>. A name failing names answers its id read 502. Every request it is sent,
// the installation lookups, token mints and id reads included, is recorded apart. Its maps may
// change while a test runs, under its lock (rename, failID).
type gateGitHub struct {
	installed   []string
	ids         map[string]int64
	moved       map[string]string
	movedByName map[string]bool
	failing     map[string]bool
	mu          sync.Mutex
	reads       []string
	every       []string
}

// rename makes GitHub answer old with a redirect to the repository now, whose id it holds, or, with
// now empty, makes old a name of its own again whose id is id.
func (g *gateGitHub) rename(old, now string, id int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.moved == nil {
		g.moved = map[string]string{}
	}
	if g.ids == nil {
		g.ids = map[string]int64{}
	}
	delete(g.moved, strings.ToLower(old))
	if now != "" {
		g.moved[strings.ToLower(old)] = strings.ToLower(now)
		return
	}
	g.ids[strings.ToLower(old)] = id
}

// failID makes GitHub answer name's id read 502 (failing true) or as before (false).
func (g *gateGitHub) failID(name string, failing bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failing == nil {
		g.failing = map[string]bool{}
	}
	g.failing[strings.ToLower(name)] = failing
}

// idOf is name's GitHub id, 0 for a repository this GitHub does not hold. g.mu is held.
func (g *gateGitHub) idOf(name string) int64 {
	name = strings.ToLower(name)
	if id, ok := g.ids[name]; ok {
		return id
	}
	installed := slices.IndexFunc(g.installed, func(each string) bool { return strings.EqualFold(each, name) })
	if installed < 0 {
		return 0
	}
	if id, ok := proxiedIDs[name]; ok {
		return id
	}
	return 1000 + int64(installed)
}

// nameOf is the repository whose id is id, lower-cased, "" for none. g.mu is held.
func (g *gateGitHub) nameOf(id int64) string {
	for name, held := range g.ids {
		if held == id {
			return name
		}
	}
	for _, name := range g.installed {
		if g.idOf(name) == id {
			return strings.ToLower(name)
		}
	}
	return ""
}

func (g *gateGitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.every = append(g.every, r.Method+" "+r.URL.RequestURI())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodPost && r.URL.Path == "/app/installations/11/access_tokens" {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"token":"ghs_gate","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
		return
	}
	var repository, path string
	if byID, ok := strings.CutPrefix(r.URL.Path, "/repositories/"); ok {
		idText, rest, _ := strings.Cut(byID, "/")
		id, _ := strconv.ParseInt(idText, 10, 64)
		repository, path = g.nameOf(id), rest
		if repository == "" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
	} else {
		owner, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
		name, rest, _ := strings.Cut(rest, "/")
		repository, path = owner+"/"+name, rest
		if now, ok := g.moved[strings.ToLower(repository)]; ok {
			location := "/repositories/" + strconv.FormatInt(g.idOf(now), 10)
			if g.movedByName[strings.ToLower(repository)] {
				location = "/repos/" + now
			}
			if path != "" {
				location += "/" + path
			}
			if r.URL.RawQuery != "" {
				location += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, location, http.StatusMovedPermanently)
			return
		}
	}
	switch path {
	case "installation":
		if !slices.Contains(g.installed, repository) && g.idOf(repository) == 0 {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `{"id":11,"app_slug":"envoy","permissions":{"pull_requests":"read","checks":"read"}}`)
	case "":
		id := g.idOf(repository)
		switch {
		case g.failing[strings.ToLower(repository)]:
			http.Error(w, `{"message":"Server Error"}`, http.StatusBadGateway)
		case id == 0:
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		default:
			fmt.Fprintf(w, `{"id":%d,"full_name":%q}`, id, strings.ToLower(repository))
		}
	default:
		g.reads = append(g.reads, r.Method+" "+r.URL.RequestURI())
		fmt.Fprintf(w, `{"repository":%q}`, repository)
	}
}

// repositoryReads is every read of a repository beyond the installation lookup, the token mint and
// the read of the repository itself, its id.
func (g *gateGitHub) repositoryReads() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.reads...)
}

// requests is every request GitHub was sent, the installation lookups, token mints and id reads
// included.
func (g *gateGitHub) requests() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.every...)
}
