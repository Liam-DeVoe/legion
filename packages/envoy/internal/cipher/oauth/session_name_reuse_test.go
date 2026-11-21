package oauth_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/exchange"
	"github.com/sjawhar/envoy/internal/cipher/oauth"
	"github.com/sjawhar/envoy/internal/cipher/oauthstore"
	"github.com/sjawhar/envoy/internal/cipher/oauthwire"
)

// The two session accounts a reused session name is tried under: a private project's sessions
// run as privSessionAccount, whose entry names privProject, and public sessions run as
// pubSessionAccount, whose entry names pubProject. Each entry fixes no session, so each pod trades
// under the name its registration gives it.
const (
	privSessionAccount = "system:serviceaccount:priv-sessions:agent-session"
	pubSessionAccount  = "system:serviceaccount:pub-sessions:agent-session"
	privProject        = "PRIV"
	pubProject         = "PUB"
	privSessionPerson  = "priv-owner@example.com"
	pubSessionPerson   = "session-operator@example.com"
	// reusedSession is the session name both accounts' pods are registered for.
	reusedSession = "019a7c3e-reused-session"
	// reuseAccessLifetime is the lifetime of each token these tests mint, which the hold on a
	// session name runs SessionRetention past.
	reuseAccessLifetime = 15 * time.Minute
)

// newSessionAccountsFixture is an exchange whose policy lists the two session accounts above, and
// whose Dispatch says each entry's person sees both projects: so the project check at mint
// (oauth's projectAccess) admits either project for either account, and what decides a token's
// projects in these tests is the entry of the account that traded, alone.
func newSessionAccountsFixture(t *testing.T) *exchangeFixture {
	t.Helper()
	access := newFakeAccess()
	access.set(func(a *fakeAccess) {
		a.held = []string{privProject, pubProject}
		a.sees = map[string][]string{
			privSessionPerson: {privProject, pubProject},
			pubSessionPerson:  {privProject, pubProject},
		}
	})
	return newExchangeFixture(t, func(c *oauth.Config) {
		c.AccessLifetime = reuseAccessLifetime
		c.Exchange.Policy = newPolicy(t, []exchange.AccountEntry{
			{Account: privSessionAccount, Project: privProject, Person: privSessionPerson},
			{Account: pubSessionAccount, Project: pubProject, Person: pubSessionPerson},
		}, nil)
		c.Exchange.Projects = access
	})
}

// TestAReusedSessionNameCarriesOnlyItsPodsProjects: a pod token's projects come from the account
// it was minted for (exchange.Set.Pod reads the account's entry), never from the session name it
// trades under. A session name belongs to the first account that wrote under it for
// oauthstore.SessionRetention past its last token (oauthstore.Store.BindSession), so a pod of
// another account presenting that name is refused while the hold stands; once it has run out, the
// name is the new account's, and its token lists that account's projects and nothing of the
// name's earlier holder.
//
// Control (run by hand, then reverted): make the exchange give a session name the projects of the
// first token minted under it, for example by keeping them in a map keyed by
// session.Extra[oauth.ClaimSession] in oauth.Provider.decide and writing them over claims.Projects
// on every later trade of that name. The last trade then lists PRIV, and this test fails naming
// reusedSession and PRIV.
func TestAReusedSessionNameCarriesOnlyItsPodsProjects(t *testing.T) {
	f := newSessionAccountsFixture(t)
	ctx := context.Background()
	key := newKey(t)

	// 1. The private project's pod writes under the name first, and its token lists PRIV.
	f.registers(t, privSessionAccount, "pod-uid-priv", reusedSession)
	first := f.exchanged(t, exchangeForm(f.podTokenOf(t, privSessionAccount, "pod-uid-priv"), oauthwire.SubjectTokenTypeKubernetes, reusedSession), key)
	if answer := f.introspected(t, first); answer.Session != reusedSession || answer.ProjectScope != oauthwire.ProjectScopeNamed || !slices.Equal(answer.Projects, []string{privProject}) {
		t.Fatalf("the %s pod's token under %s introspects %+v, want a named token of %s listing [%s]", privSessionAccount, reusedSession, answer, reusedSession, privProject)
	}

	// 2. A public session's pod registered for the same name is refused it while the hold stands,
	// and nothing is minted for it.
	f.registers(t, pubSessionAccount, "pod-uid-pub", reusedSession)
	tokens := f.count(t, "oauth_access_tokens")
	status, body := f.exchange(t, exchangeForm(f.podTokenOf(t, pubSessionAccount, "pod-uid-pub"), oauthwire.SubjectTokenTypeKubernetes, reusedSession), key)
	refusedTheName(t, status, body, reusedSession)
	if after := f.count(t, "oauth_access_tokens"); after != tokens {
		t.Fatalf("the refused trade of %s under %s stored %d access tokens, want none", pubSessionAccount, reusedSession, after-tokens)
	}

	// 3. The hold runs out: the store's clock moves SessionRetention past the last token's expiry,
	// and a minute more.
	var holder string
	var lapsed bool
	if err := f.pool.QueryRow(ctx, `update oauth_session_bindings set expires_at = expires_at - make_interval(secs => $2)
		where session = $1 returning principal, expires_at <= now()`,
		reusedSession, (oauthstore.SessionRetention+reuseAccessLifetime+time.Minute).Seconds()).Scan(&holder, &lapsed); err != nil {
		t.Fatalf("move the hold on %s past SessionRetention: %v", reusedSession, err)
	}
	if want := exchange.AccountPrincipal(privSessionAccount); holder != want || !lapsed {
		t.Fatalf("the hold on %s moved was %q's, lapsed %t; want %q's, lapsed", reusedSession, holder, lapsed, want)
	}
	second := f.exchanged(t, exchangeForm(f.podTokenOf(t, pubSessionAccount, "pod-uid-pub"), oauthwire.SubjectTokenTypeKubernetes, reusedSession), key)
	answer := f.introspected(t, second)
	if slices.Contains(answer.Projects, privProject) {
		t.Fatalf("the %s pod's token under %s lists %s, the project of the name's earlier holder %s: a pod token's projects come from its own account, never from the session name (it lists %v)",
			pubSessionAccount, reusedSession, privProject, privSessionAccount, answer.Projects)
	}
	if answer.Session != reusedSession || answer.Person != pubSessionPerson || answer.ProjectScope != oauthwire.ProjectScopeNamed || !slices.Equal(answer.Projects, []string{pubProject}) {
		t.Fatalf("the %s pod's token under %s introspects %+v, want a named token of %s acting for %s and listing [%s] only",
			pubSessionAccount, reusedSession, answer, reusedSession, pubSessionPerson, pubProject)
	}

	// The name is now the public account's: the private project's pod, still registered for it, is
	// refused it in turn.
	status, body = f.exchange(t, exchangeForm(f.podTokenOf(t, privSessionAccount, "pod-uid-priv"), oauthwire.SubjectTokenTypeKubernetes, reusedSession), key)
	refusedTheName(t, status, body, reusedSession)
}

// TestAPodTradeMustNameItsRegisteredSession: an entry that fixes no session leaves the name to the
// pod's request, and the registration its deployment made for that pod uid is what admits the
// name (oauth.Provider.registeredPodSession). A pod registered for one name that names no session
// is refused (exchange.ErrSessionRequired): the broker picks no session for it, its registered one
// included. Naming the name it is registered for, it is admitted. Naming a name another pod of the
// same account is registered for and writes under, it is refused: the claim on the name cannot
// tell two pods of one account apart, and the registration does.
//
// Controls (each run by hand, then reverted): make registeredPodSession answer nil for every
// trade, and the trade naming the other pod's session is admitted; make exchange.resolveSession
// answer the empty name it was given for an entry that fixes none, and the trade naming no session
// is refused only by the registration gate (pod_session_not_registered, naming the empty session),
// not by the rule that a request names its session.
func TestAPodTradeMustNameItsRegisteredSession(t *testing.T) {
	f := newSessionAccountsFixture(t)
	key := newKey(t)
	const session, otherSession = reusedSession, "019a7c3e-another-session"
	f.registers(t, privSessionAccount, "pod-uid-1", session)
	f.registers(t, privSessionAccount, "pod-uid-2", otherSession)
	// The other pod writes under its own name first, so the account holds both names and what
	// refuses the first pod that name below is its registration, not the claim.
	f.exchanged(t, exchangeForm(f.podTokenOf(t, privSessionAccount, "pod-uid-2"), oauthwire.SubjectTokenTypeKubernetes, otherSession), key)
	tokens := f.count(t, "oauth_access_tokens")

	status, body := f.exchange(t, exchangeForm(f.podTokenOf(t, privSessionAccount, "pod-uid-1"), oauthwire.SubjectTokenTypeKubernetes, ""), key)
	if description, _ := body["error_description"].(string); status != http.StatusBadRequest || body["error"] != "invalid_request" || body["access_token"] != nil ||
		!strings.Contains(description, "listed with no session of its own, so the request names one") {
		t.Fatalf("the trade of pod-uid-1, registered for %s, naming no session = %d %v %q; want 400 invalid_request with no access token, saying the request names a session", session, status, body["error"], description)
	}

	answer := f.introspected(t, f.exchanged(t, exchangeForm(f.podTokenOf(t, privSessionAccount, "pod-uid-1"), oauthwire.SubjectTokenTypeKubernetes, session), key))
	if answer.Session != session || !slices.Equal(answer.Projects, []string{privProject}) {
		t.Fatalf("the trade of pod-uid-1 naming its registered session %s introspects %+v, want a token of %s listing [%s]", session, answer, session, privProject)
	}

	status, body = f.exchange(t, exchangeForm(f.podTokenOf(t, privSessionAccount, "pod-uid-1"), oauthwire.SubjectTokenTypeKubernetes, otherSession), key)
	if description, _ := body["error_description"].(string); status != http.StatusBadRequest || body["error"] != "invalid_grant" || body["access_token"] != nil ||
		!strings.HasPrefix(description, oauthwire.PodSessionNotRegistered+": ") || !strings.Contains(description, "'"+otherSession+"'") {
		t.Fatalf("the trade of pod-uid-1 naming %s, which pod-uid-2 is registered for = %d %v %q; want 400 invalid_grant with no access token, beginning %s: and naming %s",
			otherSession, status, body["error"], description, oauthwire.PodSessionNotRegistered, otherSession)
	}
	if after := f.count(t, "oauth_access_tokens"); after != tokens+1 {
		t.Fatalf("the three trades stored %d access tokens, want 1: the one naming the registered session", after-tokens)
	}
}
