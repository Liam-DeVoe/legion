package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestV2DeclaredAuthority is c-authority of dispatch://INFRA-4071 (plan t4's acceptance): the
// authority each state, field and query value of the /api/v2 routes needs holds at run time,
// declared on the routes' rows and held by their wrapper (gateV2Authorities) before the handler
// runs. A session token that holds no person scope - alice's personal token holding no scope,
// acting for tourSession - resolves a comment and an ask, and changes a project's gated steps, but
// is refused 403 OAUTH_SCOPE_REQUIRED naming person for a comment's accept, an ask's answer, an
// ask's asked_of, waiting_on=me, pinned=true, a claim's force and a project's default assignee; a
// person's token without approve is refused an approval naming approve; a broker token for one
// session cannot change another session's agent mark. Each case runs once its route is mounted, and
// skips naming it until then.
//
// Each refusal of a declared value must be the wrapper's own, whose text names the rule
// (assertGateRefused): the comment's accept and the ask's answer are refused by their handlers
// too, so a code alone would pass a wrapper that skipped them. Its negative control: a scratch
// build whose gateV2Authorities skips the state rules answers those two with the handlers' own
// refusal, which names no rule, and this test fails on both.
func TestV2DeclaredAuthority(t *testing.T) {
	tour := newRouteTour(t)
	scopeless := tourBearer(tour.scopelessToken)
	asSession := url.Values{"session": {tourSession}}

	t.Run("a comment's states", func(t *testing.T) {
		route := tour.requireMounted(t, http.MethodPut, "/comments/{id}/state")
		issue := tour.issue(t)
		// A bearer names its session in actor, as the resolve's own rule for a bearer writes it
		// (resolveCommentStateInput), beside the route map's {state: resolved}.
		resolved := tour.send(t, route, tourRequest{
			Path: map[string]string{"id": tour.comment(t, issue)}, Query: asSession,
			Body: map[string]any{"state": "resolved", "actor": map[string]any{"kind": "session", "id": tourSession}}, As: scopeless,
		}, "")
		if resolved.Code != http.StatusOK {
			t.Errorf("the session token resolving a comment: %d %s, want 200", resolved.Code, resolved.Body.String())
		}
		assertGateRefused(t, "the session token accepting a comment", tour.send(t, route, tourRequest{
			Path: map[string]string{"id": tour.comment(t, issue)}, Query: asSession,
			Body: map[string]any{"state": "accepted"}, As: scopeless,
		}, ""), model.ScopePerson, "state accepted")
	})

	t.Run("an ask's states", func(t *testing.T) {
		route := tour.requireMounted(t, http.MethodPut, "/asks/{id}/state")
		issue := tour.issue(t)
		// A resolve takes its reason, and a bearer names its session in actor, as the route map's
		// {state, reason} body and the resolve's own rule for a bearer write them.
		resolved := tour.send(t, route, tourRequest{
			Path: map[string]string{"id": tour.ask(t, issue)}, Query: asSession,
			Body: map[string]any{
				"state": "resolved", "reason": "Settled on the issue.",
				"actor": map[string]any{"kind": "session", "id": tourSession},
			}, As: scopeless,
		}, "")
		if resolved.Code != http.StatusOK {
			t.Errorf("the session token resolving an ask: %d %s, want 200", resolved.Code, resolved.Body.String())
		}
		assertGateRefused(t, "the session token answering an ask", tour.send(t, route, tourRequest{
			Path: map[string]string{"id": tour.ask(t, issue)}, Query: asSession,
			Body: map[string]any{"state": "answered", "text": "Yes.", "expected_edited_at": nil}, As: scopeless,
		}, ""), model.ScopePerson, "state answered")
	})

	t.Run("an ask's asked_of", func(t *testing.T) {
		route := tour.requireMounted(t, http.MethodPatch, "/asks/{id}")
		assertGateRefused(t, "the session token handing an ask to bob", tour.send(t, route, tourRequest{
			Path: map[string]string{"id": tour.ask(t, tour.issue(t))}, Query: asSession,
			Body: map[string]any{"asked_of": "bob"}, As: scopeless,
		}, ""), model.ScopePerson, "the member asked_of")
	})

	t.Run("the asks waiting on me", func(t *testing.T) {
		route := tour.requireMounted(t, http.MethodGet, "/asks")
		assertGateRefused(t, "the session token reading waiting_on=me", tour.send(t, route, tourRequest{
			Query: url.Values{"waiting_on": {"me"}, "session": {tourSession}}, As: scopeless,
		}, ""), model.ScopePerson, "waiting_on=me")
	})

	t.Run("the issues pinned by me", func(t *testing.T) {
		route := tour.requireMounted(t, http.MethodGet, "/issues")
		assertGateRefused(t, "the session token reading pinned=true", tour.send(t, route, tourRequest{
			Query: url.Values{"pinned": {"true"}, "session": {tourSession}}, As: scopeless,
		}, ""), model.ScopePerson, "pinned=true")
	})

	t.Run("a claim taken by force", func(t *testing.T) {
		// The holder's force is a check on the data the handler makes (plan t4 step 3), and it
		// answers the gate's refusal.
		route := tour.requireMounted(t, http.MethodPut, "/issues/{key}/claim")
		assertScopeRefused(t, "the session token forcing a claim", tour.send(t, route, tourRequest{
			Path: map[string]string{"key": tour.issue(t).Key}, Query: asSession,
			Body: map[string]any{"force": true, "actor": map[string]any{"kind": "session", "id": tourSession}}, As: scopeless,
		}, ""), model.ScopePerson)
	})

	t.Run("a project's fields", func(t *testing.T) {
		route := tour.requireMounted(t, http.MethodPatch, "/projects/{key}")
		project := tour.project(t)
		// A bearer names its session in actor, as the gated steps' own rule for a bearer writes it
		// (patchProjectV2Input, as projectGatedStepsInput), beside the route map's {gated_steps}.
		gated := tour.send(t, route, tourRequest{
			Path: map[string]string{"key": project}, Query: asSession,
			Body: map[string]any{"gated_steps": []string{"spec"}, "actor": map[string]any{"kind": "session", "id": tourSession}}, As: scopeless,
		}, "")
		if gated.Code != http.StatusOK {
			t.Errorf("the session token publishing gated steps: %d %s, want 200", gated.Code, gated.Body.String())
		}
		assertGateRefused(t, "the session token setting the default assignee", tour.send(t, route, tourRequest{
			Path: map[string]string{"key": project}, Query: asSession,
			Body: map[string]any{"default_assignee": "bob"}, As: scopeless,
		}, ""), model.ScopePerson, "the member default_assignee")
	})

	t.Run("an approval without approve", func(t *testing.T) {
		route := tour.requireMounted(t, http.MethodPut, "/credential-requests/{id}/state")
		record := uuid.NewString()
		// Both states of this route need approve, so the route's own scope refuses the request
		// before the per-state column is read (routeScopes), and its text names the route.
		assertScopeRefused(t, "a person's token without approve approving", tour.send(t, route, tourRequest{
			Path: map[string]string{"id": record}, Body: map[string]any{"state": "approved", "code": "ABCD-1234"},
			As: tourBearer(tour.personToken),
		}, ""), model.ScopeApprove)
		if relayed := tour.secrets.callsTo(http.MethodPost, "/v1/credential-requests/"+record+"/approve"); relayed != 0 {
			t.Errorf("the refused approval reached the broker %d times, want never", relayed)
		}
	})

	t.Run("another session's agent mark", func(t *testing.T) {
		route := tour.requireMounted(t, http.MethodPatch, "/me/marks/{kind}/{id}")
		caller := newBrokerCaller(t)
		const token = "a-broker-token-for-one-session"
		tour.brokerTokens.Says(token, liveNamedToken(caller.jkt))
		response := httptest.NewRecorder()
		tour.handler.ServeHTTP(response, caller.presenting(t, route.Method, "/api/v2/me/marks/agent/session-someone-else", token,
			map[string]any{"pinned": true}))
		if code, _ := refusalOf(t, response); response.Code != http.StatusForbidden || (code != "OAUTH_SCOPE_REQUIRED" && code != "SESSION_SCOPE") {
			t.Errorf("a broker token for %s changing another session's agent mark: %d %s, want 403",
				brokerTestSession, response.Code, response.Body.String())
		}
	})
}
