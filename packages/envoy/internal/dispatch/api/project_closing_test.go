package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/access"
	"github.com/sjawhar/envoy/internal/dispatch/agentsecrets"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// countingBroker stands in for the secrets broker's count route (GET /v1/admin/machine-tokens),
// answering the count each project holds, to Dispatch's own account token alone, or failing every
// call while down.
type countingBroker struct {
	mu   sync.Mutex
	live map[string]int
	down bool
}

const countingBrokerAccount = "dispatch-account-token"

func (b *countingBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.down:
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"DATABASE_UNAVAILABLE","error":"down"}`))
	case r.Method != http.MethodGet || r.URL.Path != "/v1/admin/machine-tokens":
		w.WriteHeader(http.StatusNotFound)
	case r.Header.Get("Authorization") != "Bearer "+countingBrokerAccount:
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"ACCOUNT_LACKS_POWER","error":"not dispatch"}`))
	default:
		project := r.URL.Query().Get("project")
		answer := map[string]any{"project": project, "live_tokens": b.live[project], "last_expires_at": nil}
		if b.live[project] > 0 {
			answer["last_expires_at"] = time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
		}
		_ = json.NewEncoder(w).Encode(answer)
	}
}

func (b *countingBroker) set(project string, live int, down bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.live[project], b.down = live, down
}

// closingRig is a test server whose deployment provisions listed as private and whose hubs admit
// Dispatch, holding PRIV and PUB, both alice's, with the store's backfills done, and, with a
// broker, the counting stand-in as its secrets broker. Its membership answer is read as the
// listed service account.
type closingRig struct {
	handler  http.Handler
	database *store.Store
	broker   *countingBroker
	// answerBody is the membership answer of a project as its bytes.
	answerBody func(t *testing.T, key string) string
}

// answer is the membership answer of key, decoded.
func (rig closingRig) answer(t *testing.T, key string) model.AccessAnswer {
	t.Helper()
	var answer model.AccessAnswer
	if err := json.Unmarshal([]byte(rig.answerBody(t, key)), &answer); err != nil {
		t.Fatalf("decode the membership answer of %s: %v", key, err)
	}
	return answer
}

func newClosingRig(t *testing.T, listed []string, withBroker bool) closingRig {
	t.Helper()
	issuer := newServiceTokenIssuer(t)
	handler, database, _, served := newRegisteredTestServer(t, testServerOptions{
		store: storetest.Open(t), privateProjects: listed, hubAdmission: acceptingAdmission{},
		oidc: issuer.verifier(t), accessAccounts: []string{listedServiceAccount},
	})
	rig := closingRig{handler: handler, database: database}
	if withBroker {
		rig.broker = &countingBroker{live: map[string]int{}}
		stand := httptest.NewServer(rig.broker)
		t.Cleanup(stand.Close)
		served.deps.AgentSecrets = agentsecrets.New(stand.URL, accountTokenFile(t, countingBrokerAccount))
	}
	for _, key := range []string{"PRIV", "PUB"} {
		if response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/projects", map[string]string{"key": key, "name": key}, "alice"); response.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", key, response.Code, response.Body.String())
		}
	}
	if _, err := database.Pool.Exec(context.Background(), `
		insert into session_projects_filled default values;
		insert into access_events_scrubbed default values;
	`); err != nil {
		t.Fatalf("fill the store's markers: %v", err)
	}
	bearer := issuer.Mint(t, issuer.signing, issuer.Claims(listedServiceAccount, serviceTokenAudience))
	rig.answerBody = func(t *testing.T, key string) string {
		t.Helper()
		response := agentRequest(t, handler, http.MethodGet, "/api/v1/access/projects/"+key, nil, bearer)
		if response.Code != http.StatusOK {
			t.Fatalf("the membership answer of %s = %d %s", key, response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	return rig
}

// switchPrivate is the owner alice's switch of key to private.
func (rig closingRig) switchPrivate(t *testing.T, key string) *httptest.ResponseRecorder {
	t.Helper()
	return dispatchRequest(t, rig.handler, http.MethodPatch, "/api/v1/projects/"+key+"/access", map[string]string{"visibility": "private"}, "alice")
}

// closingSince is key's closing_since, nil while it is in no closing state.
func (rig closingRig) closingSince(t *testing.T, key string) *time.Time {
	t.Helper()
	var since *time.Time
	if err := rig.database.Pool.QueryRow(context.Background(), `select closing_since from projects where key = $1`, key).Scan(&since); err != nil {
		t.Fatalf("read %s's closing_since: %v", key, err)
	}
	return since
}

// mint is alice's personal token listing projects, minted through the route; it answers the
// route's response.
func (rig closingRig) mint(t *testing.T, name string, projects []string) *httptest.ResponseRecorder {
	t.Helper()
	return dispatchRequest(t, rig.handler, http.MethodPost, "/api/v1/me/agent-tokens", mintBody(name, projects), "alice")
}

// whoami is the status of GET /api/v1/whoami presenting token.
func (rig closingRig) whoami(t *testing.T, token string) int {
	t.Helper()
	return agentRequest(t, rig.handler, http.MethodGet, "/api/v1/whoami", nil, token).Code
}

// lowerSettle sets developerBoxSettle to settle until the test ends.
func lowerSettle(t *testing.T, settle time.Duration) {
	t.Helper()
	previous := developerBoxSettle
	developerBoxSettle = settle
	t.Cleanup(func() { developerBoxSettle = previous })
}

// refusedWith fails the test unless response is status with code and a message naming each of
// naming.
func refusedWith(t *testing.T, what string, response *httptest.ResponseRecorder, status int, code string, naming ...string) {
	t.Helper()
	body := response.Body.String()
	if response.Code != status || !strings.Contains(body, `"code":"`+code+`"`) {
		t.Fatalf("%s = %d %s, want %d %s", what, response.Code, body, status, code)
	}
	for _, name := range naming {
		if !strings.Contains(body, name) {
			t.Fatalf("%s = %s, want a message naming %q", what, body, name)
		}
	}
}

// fullSwitchAt is key's full_switch_at, nil while no full switch is recorded.
func (rig closingRig) fullSwitchAt(t *testing.T, key string) *time.Time {
	t.Helper()
	var at *time.Time
	if err := rig.database.Pool.QueryRow(context.Background(), `select full_switch_at from projects where key = $1`, key).Scan(&at); err != nil {
		t.Fatalf("read %s's full_switch_at: %v", key, err)
	}
	return at
}

// makeDayOnePrivate makes key private as a project is on its first day, before its full switch:
// private, with no full switch recorded and no closing state.
func (rig closingRig) makeDayOnePrivate(t *testing.T, key string) {
	t.Helper()
	if _, err := rig.database.Pool.Exec(context.Background(), `update projects set visibility = 'private' where key = $1`, key); err != nil {
		t.Fatalf("make %s private on its first day: %v", key, err)
	}
}

// TestAProjectBecomesPrivateOnlyWithNoDeveloperBoxTokenLive: the owner's switch of PRIV to private
// starts its closing state and answers 409 DEVELOPER_BOX_TOKENS_LIVE naming the broker's count,
// the closing state committed and the membership answer closed to developer boxes, PRIV still
// public. It answers so while the broker counts any token live, and while the closing state is
// younger than developerBoxSettle with none live; with none live and the state that old, the
// switch completes, records the full switch and ends the closing state, and the answer stays
// closed. A broker that does not answer leaves the switch 503 DEVELOPER_BOX_TOKENS_UNAVAILABLE and
// the closing state in place; the owner's switch to public ends it. A project private on its first
// day, closed to developer boxes once its key is on DISPATCH_PRIVATE_PROJECTS, goes through the same
// closing state at its owner's switch. A deployment with no secrets broker, which no developer-box
// token reads, completes the switch at once.
func TestAProjectBecomesPrivateOnlyWithNoDeveloperBoxTokenLive(t *testing.T) {
	t.Run("with a broker", func(t *testing.T) {
		rig := newClosingRig(t, []string{"PRIV", "PUB"}, true)
		lowerSettle(t, time.Hour)
		rig.broker.set("PRIV", 2, false)
		refusedWith(t, "the first switch of PRIV", rig.switchPrivate(t, "PRIV"), http.StatusConflict, "DEVELOPER_BOX_TOKENS_LIVE",
			"PRIV is closing since", "holds 2 live developer-box tokens")
		if rig.closingSince(t, "PRIV") == nil || visibilityOf(t, rig.database, "PRIV") != model.ProjectPublic || !rig.answer(t, "PRIV").ClosedToDeveloperBoxes {
			t.Fatalf("after the refused switch PRIV is %s with closing_since %v and answer %+v, want public, closing and closed to developer boxes",
				visibilityOf(t, rig.database, "PRIV"), rig.closingSince(t, "PRIV"), rig.answer(t, "PRIV"))
		}
		lowerSettle(t, 0)
		refusedWith(t, "a switch while 2 tokens are live", rig.switchPrivate(t, "PRIV"), http.StatusConflict, "DEVELOPER_BOX_TOKENS_LIVE", "holds 2 live")
		rig.broker.set("PRIV", 0, false)
		lowerSettle(t, time.Hour)
		refusedWith(t, "a switch with none live and the closing state younger than the settle time", rig.switchPrivate(t, "PRIV"),
			http.StatusConflict, "DEVELOPER_BOX_TOKENS_LIVE", "holds 0 live", "no sooner than")
		rig.broker.set("PRIV", 0, true)
		refusedWith(t, "a switch the broker does not count", rig.switchPrivate(t, "PRIV"), http.StatusServiceUnavailable, "DEVELOPER_BOX_TOKENS_UNAVAILABLE", "PRIV stays closing")
		if rig.fullSwitchAt(t, "PRIV") != nil {
			t.Fatalf("a refused switch recorded PRIV's full switch")
		}
		rig.broker.set("PRIV", 0, false)
		lowerSettle(t, 0)
		if made := rig.switchPrivate(t, "PRIV"); made.Code != http.StatusOK || visibilityOf(t, rig.database, "PRIV") != model.ProjectPrivate ||
			rig.closingSince(t, "PRIV") != nil || rig.fullSwitchAt(t, "PRIV") == nil || !rig.answer(t, "PRIV").ClosedToDeveloperBoxes {
			t.Fatalf("the switch with none live and the state settled = %d %s, PRIV %s closing %v full switch %v; want 200, private, the full switch recorded, the closing state ended and the answer closed",
				made.Code, made.Body.String(), visibilityOf(t, rig.database, "PRIV"), rig.closingSince(t, "PRIV"), rig.fullSwitchAt(t, "PRIV"))
		}

		refusedWith(t, "the first switch of PUB", rig.switchPrivate(t, "PUB"), http.StatusConflict, "DEVELOPER_BOX_TOKENS_LIVE")
		if response := dispatchRequest(t, rig.handler, http.MethodPatch, "/api/v1/projects/PUB/access", map[string]string{"visibility": "public"}, "alice"); response.Code != http.StatusOK ||
			rig.closingSince(t, "PUB") != nil || rig.answer(t, "PUB").ClosedToDeveloperBoxes {
			t.Fatalf("the owner's switch of the closing PUB to public = %d %s, closing %v; want 200 and its closing state ended", response.Code, response.Body.String(), rig.closingSince(t, "PUB"))
		}
	})

	t.Run("a project private on its first day", func(t *testing.T) {
		rig := newClosingRig(t, []string{"PRIV"}, true)
		lowerSettle(t, 0)
		rig.makeDayOnePrivate(t, "PRIV")
		if !rig.answer(t, "PRIV").ClosedToDeveloperBoxes {
			t.Fatalf("PRIV, private on its first day with its key on DISPATCH_PRIVATE_PROJECTS, answered open before its owner's switch")
		}
		refusedWith(t, "the owner's switch of the day-one PRIV", rig.switchPrivate(t, "PRIV"), http.StatusConflict, "DEVELOPER_BOX_TOKENS_LIVE", "PRIV is closing since")
		if !rig.answer(t, "PRIV").ClosedToDeveloperBoxes {
			t.Fatalf("PRIV answered open to developer boxes in its closing state")
		}
		if made := rig.switchPrivate(t, "PRIV"); made.Code != http.StatusOK || rig.closingSince(t, "PRIV") != nil || rig.fullSwitchAt(t, "PRIV") == nil {
			t.Fatalf("the owner's next switch = %d %s, closing %v; want 200, the closing state ended and the full switch recorded", made.Code, made.Body.String(), rig.closingSince(t, "PRIV"))
		}
	})

	t.Run("with no broker", func(t *testing.T) {
		rig := newClosingRig(t, []string{"PRIV"}, false)
		lowerSettle(t, time.Hour)
		if made := rig.switchPrivate(t, "PRIV"); made.Code != http.StatusOK || visibilityOf(t, rig.database, "PRIV") != model.ProjectPrivate || rig.closingSince(t, "PRIV") != nil {
			t.Fatalf("the switch with no broker = %d %s; want 200 at once, private, its closing state ended", made.Code, made.Body.String())
		}
	})
}

// TestTheMembershipAnswerClosesAProjectToDeveloperBoxes: closed_to_developer_boxes is absent for a
// public project, and for a project private on its first day whose key DISPATCH_PRIVATE_PROJECTS
// does not name (DAYONE); it is true for a private project whose key the list names, before any
// switch (PRIV), for a project private by its full switch whose key the list does not name
// (DAYONE once its full switch is recorded), and for a project in its closing state (PUB).
func TestTheMembershipAnswerClosesAProjectToDeveloperBoxes(t *testing.T) {
	rig := newClosingRig(t, []string{"PRIV"}, true)
	if response := dispatchRequest(t, rig.handler, http.MethodPost, "/api/v1/projects", map[string]string{"key": "DAYONE", "name": "DAYONE"}, "alice"); response.Code != http.StatusCreated {
		t.Fatalf("create DAYONE: %d %s", response.Code, response.Body.String())
	}
	rig.makeDayOnePrivate(t, "DAYONE")
	for _, key := range []string{"PUB", "PRIV", "DAYONE"} {
		if raw := rig.answerBody(t, key); strings.Contains(raw, "closed_to_developer_boxes") {
			t.Fatalf("the membership answer of %s = %s, want no closed_to_developer_boxes key: it is public, or private with neither its full switch nor its key listed", key, raw)
		}
	}
	rig.makeDayOnePrivate(t, "PRIV")
	if raw := rig.answerBody(t, "PRIV"); !strings.Contains(raw, `"closed_to_developer_boxes":true`) {
		t.Fatalf("the membership answer of PRIV, private with its key on DISPATCH_PRIVATE_PROJECTS, before any switch = %s, want closed_to_developer_boxes true", raw)
	}
	if _, err := rig.database.Pool.Exec(context.Background(), `update projects set full_switch_at = now() where key = 'DAYONE'`); err != nil {
		t.Fatalf("record DAYONE's full switch: %v", err)
	}
	if raw := rig.answerBody(t, "DAYONE"); !strings.Contains(raw, `"closed_to_developer_boxes":true`) {
		t.Fatalf("the membership answer of DAYONE, private by its full switch with its key not listed = %s, want closed_to_developer_boxes true", raw)
	}
	if _, err := rig.database.Pool.Exec(context.Background(), `update projects set closing_since = now() where key = 'PUB'`); err != nil {
		t.Fatalf("put PUB in its closing state: %v", err)
	}
	if raw := rig.answerBody(t, "PUB"); !strings.Contains(raw, `"closed_to_developer_boxes":true`) {
		t.Fatalf("the membership answer of PUB in its closing state = %s, want closed_to_developer_boxes true", raw)
	}
}

// TestNoPersonalTokenListsAPrivateProject: a personal token's mint and its edit are refused 403
// PRIVATE_PROJECT_TOKEN, naming the project, for a project in its closing state, for one private
// by its full switch, and for one private on its first day whose key DISPATCH_PRIVATE_PROJECTS
// names, and the token is not made; a public project, and one private on its first day whose key
// the list does not name, are listed as before.
func TestNoPersonalTokenListsAPrivateProject(t *testing.T) {
	rig := newClosingRig(t, []string{"PRIV", "PUB"}, true)
	lowerSettle(t, time.Hour)
	if made := rig.mint(t, "while public", []string{"PRIV"}); made.Code != http.StatusCreated {
		t.Fatalf("mint naming PRIV while it is public = %d %s, want 201", made.Code, made.Body.String())
	}
	other := decodeBody[agentTokenResponse](t, rig.mint(t, "of PUB", []string{"PUB"}))
	refusedWith(t, "the switch of PRIV", rig.switchPrivate(t, "PRIV"), http.StatusConflict, "DEVELOPER_BOX_TOKENS_LIVE")
	count := func() int {
		var n int
		if err := rig.database.Pool.QueryRow(context.Background(), `select count(*) from agent_tokens`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	refusedWith(t, "a mint naming PRIV while it is closing", rig.mint(t, "while closing", []string{"PRIV"}), http.StatusForbidden, "PRIVATE_PROJECT_TOKEN", "PRIV")
	refusedWith(t, "an edit naming PRIV while it is closing",
		dispatchRequest(t, rig.handler, http.MethodPatch, "/api/v1/me/agent-tokens/"+other.ID, map[string]any{"scopes": []string{model.ScopePerson}, "projects": []string{"PRIV", "PUB"}}, "alice"),
		http.StatusForbidden, "PRIVATE_PROJECT_TOKEN", "PRIV")
	lowerSettle(t, 0)
	rig.broker.set("PRIV", 0, false)
	if made := rig.switchPrivate(t, "PRIV"); made.Code != http.StatusOK || rig.fullSwitchAt(t, "PRIV") == nil {
		t.Fatalf("the switch that completes PRIV's full switch = %d %s", made.Code, made.Body.String())
	}
	refusedWith(t, "a mint naming PRIV after its full switch", rig.mint(t, "while private", []string{"PRIV"}), http.StatusForbidden, "PRIVATE_PROJECT_TOKEN", "PRIV")
	if after := count(); after != before {
		t.Fatalf("personal tokens after the refused mints = %d, want the %d there were", after, before)
	}
	var listed []string
	if err := rig.database.Pool.QueryRow(context.Background(), `select projects from agent_tokens where id = $1`, other.ID).Scan(&listed); err != nil || len(listed) != 1 || listed[0] != "PUB" {
		t.Fatalf("the edited token lists %v (%v), want PUB alone: the refused edit wrote nothing", listed, err)
	}

	dayOne := newClosingRig(t, []string{"PUB"}, true)
	dayOne.makeDayOnePrivate(t, "PRIV")
	if made := dayOne.mint(t, "on its first day", []string{"PRIV"}); made.Code != http.StatusCreated {
		t.Fatalf("mint naming PRIV on its first day, its key not listed = %d %s, want 201", made.Code, made.Body.String())
	}
	listedDayOne := newClosingRig(t, []string{"PRIV"}, true)
	listedDayOne.makeDayOnePrivate(t, "PRIV")
	refusedWith(t, "a mint naming PRIV on its first day, its key listed", listedDayOne.mint(t, "listed", []string{"PRIV"}), http.StatusForbidden, "PRIVATE_PROJECT_TOKEN", "PRIV")
}

// TestTheClosingStateRevokesPersonalTokensOfTheProject: a personal token made while PRIV was public
// and listing it reads as itself until PRIV's switch to private starts its closing state, and
// answers 401 from then on; a token of another project reads on.
func TestTheClosingStateRevokesPersonalTokensOfTheProject(t *testing.T) {
	rig := newClosingRig(t, []string{"PRIV"}, true)
	lowerSettle(t, time.Hour)
	priv := decodeBody[agentTokenResponse](t, rig.mint(t, "of PRIV", []string{"PRIV"}))
	both := decodeBody[agentTokenResponse](t, rig.mint(t, "of PRIV and PUB", []string{"PRIV", "PUB"}))
	pub := decodeBody[agentTokenResponse](t, rig.mint(t, "of PUB", []string{"PUB"}))
	for _, token := range []string{priv.Token, both.Token, pub.Token} {
		if status := rig.whoami(t, token); status != http.StatusOK {
			t.Fatalf("a token before the switch reads whoami %d, want 200", status)
		}
	}
	refusedWith(t, "the switch of PRIV", rig.switchPrivate(t, "PRIV"), http.StatusConflict, "DEVELOPER_BOX_TOKENS_LIVE")
	for name, token := range map[string]string{"the token of PRIV": priv.Token, "the token of PRIV and PUB": both.Token} {
		if status := rig.whoami(t, token); status != http.StatusUnauthorized {
			t.Fatalf("%s reads whoami %d after PRIV's closing state began, want 401", name, status)
		}
	}
	if status := rig.whoami(t, pub.Token); status != http.StatusOK {
		t.Fatalf("the token of PUB reads whoami %d after PRIV's closing state began, want 200", status)
	}
}

// TestAKeyThatJoinsTheListRevokesThePersonalTokensOfAProjectAlreadyPrivate: a personal token made
// while PRIV was private on its first day, its key not yet on DISPATCH_PRIVATE_PROJECTS, reads on
// until the boot that names the key there runs access.RevokeClosedProjectTokens, as cmd/dispatch's
// boot does, and answers 401 from then on, its owner's switch or not; a token of a public project
// is left. The pass revokes nothing while the list does not name PRIV.
func TestAKeyThatJoinsTheListRevokesThePersonalTokensOfAProjectAlreadyPrivate(t *testing.T) {
	rig := newClosingRig(t, nil, false)
	rig.makeDayOnePrivate(t, "PRIV")
	priv := decodeBody[agentTokenResponse](t, rig.mint(t, "of PRIV", []string{"PRIV"}))
	pub := decodeBody[agentTokenResponse](t, rig.mint(t, "of PUB", []string{"PUB"}))
	if keys, revoked, err := access.RevokeClosedProjectTokens(context.Background(), rig.database.Pool, nil); err != nil || revoked != 0 || len(keys) != 0 {
		t.Fatalf("the boot pass with no key listed = %v, %d, %v; want nothing revoked", keys, revoked, err)
	}
	if status := rig.whoami(t, priv.Token); status != http.StatusOK {
		t.Fatalf("PRIV's token on its first day reads whoami %d, want 200", status)
	}
	keys, revoked, err := access.RevokeClosedProjectTokens(context.Background(), rig.database.Pool, []string{"PRIV"})
	if err != nil || revoked != 1 || len(keys) != 1 || keys[0] != "PRIV" {
		t.Fatalf("the boot pass naming PRIV = %v, %d, %v; want PRIV's one token revoked", keys, revoked, err)
	}
	if status := rig.whoami(t, priv.Token); status != http.StatusUnauthorized {
		t.Fatalf("PRIV's token after the boot that names PRIV reads whoami %d, want 401", status)
	}
	if status := rig.whoami(t, pub.Token); status != http.StatusOK {
		t.Fatalf("PUB's token after that boot reads whoami %d, want 200", status)
	}
}
