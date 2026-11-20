package approval_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/answer"
	"github.com/sjawhar/envoy/internal/broker/api"
	"github.com/sjawhar/envoy/internal/broker/brokertest"
	"github.com/sjawhar/envoy/internal/broker/enroll"
	"github.com/sjawhar/envoy/internal/broker/hold"
	"github.com/sjawhar/envoy/internal/broker/machine"
	"github.com/sjawhar/envoy/internal/broker/oauthstore"
	"github.com/sjawhar/envoy/internal/broker/policy"
	"github.com/sjawhar/envoy/internal/broker/policy/policytest"
	"github.com/sjawhar/envoy/internal/broker/projectaccess"
	"github.com/sjawhar/envoy/internal/broker/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/internal/broker/proof"
	"github.com/sjawhar/envoy/internal/broker/requests"
	"github.com/sjawhar/envoy/internal/broker/secrets"
	"github.com/sjawhar/envoy/internal/broker/store/storetest"
	"github.com/sjawhar/envoy/internal/oidc"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// bob is a person of the domain whom the Dispatch stand-in never holds: the control who decides a
// shared secret's request beside ada, whom it answers held once the test holds her.
const bob = "bob@example.com"

// heldWords is the broker's refusal of a held person's credential, naming person: what the page
// answers a held approver's decision.
func heldWords(person string) string {
	return person + "'s credentials are held by Dispatch; they work again when " + person +
		" signs in to Dispatch, or when Dispatch can confirm " + person + " with Google again"
}

// brokerAccount is the bearer the broker presents to the Dispatch stand-in: the stand-in's own, or,
// once refused is set, one the stand-in answers 403, as a Dispatch that does not list the broker's
// account answers it.
type brokerAccount struct {
	bearer  string
	refused atomic.Bool
}

func (a *brokerAccount) Read() (string, error) {
	if a.refused.Load() {
		return "a bearer Dispatch does not take", nil
	}
	return a.bearer, nil
}

// holdRig is newRig's broker with the hold of refused people wired as cmd/broker wires it where
// BROKER_PROJECT_ACCESS_URL is set: one real hold.Gate over the Dispatch stand-in and the rig's own
// database, given to the approval page and to the API's routes. The gate and its Dispatch client
// read a clock the test moves past the client's cache of an answer.
type holdRig struct {
	*rig
	dispatch *projectaccesstest.Server
	account  *brokerAccount
	ahead    atomic.Int64
}

func (h *holdRig) now() time.Time { return time.Now().Add(time.Duration(h.ahead.Load())) }

// pastCache moves the clock past every answer the gate's client keeps.
func (h *holdRig) pastCache() { h.ahead.Add(int64(projectaccess.CacheTTL + time.Second)) }

// hold has Dispatch answer person held from the gate's next check of them.
func (h *holdRig) hold(person string) {
	h.dispatch.SetHeld(person, true)
	h.pastCache()
}

// refuseAccount has Dispatch refuse the broker's own account from the gate's next check on.
func (h *holdRig) refuseAccount() {
	h.account.refused.Store(true)
	h.pastCache()
}

// newHoldRig is newRig with the hold: the same secrets, sign-in, services and routes.
func newHoldRig(t *testing.T) *holdRig {
	t.Helper()
	h := &holdRig{dispatch: projectaccesstest.NewServer(t, nil, map[string]string{
		ada: projectaccess.MemberFull, ben: projectaccess.MemberFull, bob: projectaccess.MemberFull, carol: projectaccess.MemberFull,
	})}
	h.account = &brokerAccount{bearer: h.dispatch.Token}
	access := projectaccess.New(h.dispatch.URL, h.account, nil)
	access.UseClock(h.now)

	st := storetest.Open(t)
	local := secrets.NewLocal(
		policytest.Secret("TEST_SECRET", ada, policy.TierHuman, "test-v1"),
		policytest.Secret("SHARED_SECRET", policy.OwnerShared, policy.TierHuman, "shared-v1"),
	)
	current := policytest.Current(t, local)
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(answer.Middleware(mux, nil))
	t.Cleanup(srv.Close)
	issuer := oidctest.New(t)
	key := issuer.PublishKey(t, "signing-key")
	accounts, err := oidc.New(context.Background(), issuer.URL(), dispatchAudience)
	if err != nil {
		t.Fatalf("oidc.New: %v", err)
	}
	gate := hold.New(hold.Config{Access: access, Store: st, Now: h.now})
	enr := &enroll.Service{Store: st, Lease: time.Hour}
	enr.Chain = enroll.NewChainVerifier(st, srv.URL, time.Minute)
	reqMachine := &requests.Machine{
		Store: st, Policy: current, Secrets: policytest.Store(local), PendingTTL: 12 * time.Hour,
		Audience: srv.URL, Skew: time.Minute, Replay: enr.Replay,
	}
	reqMachine.Chain = requests.NewChainVerifier(st, srv.URL, time.Minute, reqMachine.Airlock)
	provider := brokertest.NewOAuthProvider(t, st, issuer)
	mach := &machine.Service{
		Store: st, Enroll: enr, Policy: current,
		Audience: srv.URL, Skew: time.Minute, PendingTTL: 15 * time.Minute, CredentialLifetime: 7 * 24 * time.Hour,
		OAuth: provider, Replay: enr.Replay,
	}
	page := brokertest.NewApprovalPage(t, brokertest.SignIn(issuer, key), srv.URL, st, reqMachine, brokertest.WithApprovalHold(gate))
	api.Register(mux, api.Deps{
		PublicURL:     srv.URL,
		APIAccounts:   map[string]api.Account{dispatchAccount: brokertest.Caller(t, "dispatch")},
		AccountTokens: enroll.K8sPodVerifier{Verifier: accounts},
		Enroll:        enr, Machine: reqMachine, MachineLogin: mach,
		OAuth: provider, DeviceLogin: mach.LoginDevice, TokenHooks: mach.TokenHooks(),
		Proof:  &proof.Verifier{Skew: time.Minute, Lookup: enr.Lookup, LookupLauncher: enr.AuthenticateLauncher, Replay: enr.Replay},
		Policy: current, SecretsStore: secrets.StoreAWS, SecretsPrefix: policytest.Prefix, SecretsKMSKey: policytest.KeyARN,
		Approval: page,
		Hold:     gate,
	})
	claims := issuer.Claims(dispatchAccount, dispatchAudience)
	claims["iat"] = time.Now().Add(-5 * time.Second).Unix()
	h.rig = &rig{
		url: srv.URL, srv: srv, st: st, issuer: issuer, machine: reqMachine, page: page, enrollments: enr,
		sweeper:      &requests.Sweeper{Enrollments: enr, Machine: reqMachine, MachineLogins: mach, OAuth: oauthstore.New(st.Pool), Interval: time.Hour},
		accountToken: issuer.Mint(t, key, claims),
	}
	return h
}

// TestHeldApproverDecisionIsRefused: a decision on the approval page by a person Dispatch holds,
// whose session signed in before the hold, is refused with the page's refusal, 403 in the
// broker's words, approve and deny alike, and decides nothing, while the record's own page is
// served to her; bob's decision is made. While Dispatch refuses the broker's own account every
// decision is the page's 503 refusal and decides nothing.
func TestHeldApproverDecisionIsRefused(t *testing.T) {
	r := newHoldRig(t)
	enrollmentID, key := r.benSession(t, "ben-box")
	otherID, otherKey := r.benSession(t, "ben-other-box")
	asked := r.request(t, enrollmentID, key, "deploy", nil, "", "TEST_SECRET")
	shared := r.request(t, enrollmentID, key, "shared deploy", nil, "", "SHARED_SECRET")
	later := r.request(t, otherID, otherKey, "a later shared deploy", nil, "", "SHARED_SECRET")
	adaBrowser := r.browser(t)
	adaBrowser.SignIn(t, asked.recordID, person(ada))
	adaForm := decisionForm(t, adaBrowser.Page(t, asked.recordID).Body, asked.recordID)
	bobBrowser := r.browser(t)
	bobBrowser.SignIn(t, shared.recordID, person(bob))
	bobForm := decisionForm(t, bobBrowser.Page(t, shared.recordID).Body, shared.recordID)
	laterForm := decisionForm(t, bobBrowser.Page(t, later.recordID).Body, later.recordID)
	if adaForm == nil || bobForm == nil || laterForm == nil {
		t.Fatalf("a page offers no decision form for a record this test decides: ada's %v, bob's %v and %v", adaForm != nil, bobForm != nil, laterForm != nil)
	}
	r.hold(ada)

	for _, value := range []string{"approve", "deny"} {
		res := adaBrowser.FromPage(t, "/approve", decision(adaForm, value))
		if res.Status != http.StatusForbidden || !strings.Contains(pageText(t, res.Body), heldWords(ada)) {
			t.Errorf("ada's %s while Dispatch holds her = %d, want 403 saying %q:\n%s", value, res.Status, heldWords(ada), pageText(t, res.Body))
		}
		if id := res.Header.Get("X-Broker-Answer-Id"); id == "" || !strings.Contains(res.Body, id) {
			t.Errorf("the refusal of ada's %s prints no answer id (%q)", value, id)
		}
		r.requirePending(t, asked.recordID)
	}
	if res := adaBrowser.Get(t, "/approve/"+asked.recordID); res.Status != http.StatusOK || !strings.Contains(res.Body, asked.recordID) {
		t.Errorf("the record's page for ada while Dispatch holds her = %d, want it served", res.Status)
	}
	if res := bobBrowser.FromPage(t, "/approve", decision(bobForm, "approve")); res.Status != http.StatusSeeOther {
		t.Errorf("bob's approval = %d, want 303:\n%s", res.Status, pageText(t, res.Body))
	}
	if state := r.state(t, shared.recordID); state != "approved" {
		t.Errorf("the record bob approved is %s, want approved", state)
	}

	r.refuseAccount()
	for _, c := range []struct {
		who      string
		browser  *brokertest.Browser
		form     url.Values
		recordID string
	}{{ada, adaBrowser, adaForm, asked.recordID}, {bob, bobBrowser, laterForm, later.recordID}} {
		says := "Whether Dispatch holds " + c.who + " cannot be read: Dispatch refused the broker's own account"
		res := c.browser.FromPage(t, "/approve", decision(c.form, "approve"))
		if res.Status != http.StatusServiceUnavailable || !strings.Contains(pageText(t, res.Body), says) {
			t.Errorf("%s's approval while Dispatch refuses the broker's account = %d, want 503 saying %q:\n%s", c.who, res.Status, says, pageText(t, res.Body))
		}
		r.requirePending(t, c.recordID)
	}
}
