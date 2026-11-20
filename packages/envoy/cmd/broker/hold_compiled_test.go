// cmd/broker/hold_compiled_test.go
//
// The hold of refused people (internal/broker/hold) as the broker ships it: the compiled binary,
// its BROKER_PROJECT_ACCESS_URL pointed at a stand-in for Dispatch's person route, read at each
// place cmd/broker/main.go hands the gate to. Every package's own tests hand their package a gate
// themselves, so a line of main that stopped handing it over would pass all of them; this test
// reads what main wires, through the routes a person's credentials reach.
package main

import (
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/sjawhar/envoy/internal/broker/brokertest"
	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/broker/projectaccess"
	"github.com/sjawhar/envoy/internal/broker/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/internal/broker/proof"
	"github.com/sjawhar/envoy/internal/broker/store/storetest"
	"github.com/sjawhar/envoy/internal/brokerclient"
)

// bob is the person the Dispatch stand-in never holds: the control each place serves beside ada
// (approval_page_test.go's), whom it answers held from the second boot on.
const bob = "bob@example.com"

// holdUpstreamBody is what the service behind the airlock answers every request it is carried.
const holdUpstreamBody = "the service behind the airlock answered"

// The three ways a place can answer one person's credential: serve it; refuse it as held, which
// for introspection is reporting the token held; or neither, which no row of this test wants.
const (
	answeredServed  = "served"
	answeredHeld    = "held"
	answeredNeither = "neither"
)

// holdAnswer is what one place did with one person's credential, and the broker's answer as a
// failure prints it.
type holdAnswer struct {
	outcome string
	said    string
}

// holdPlace is one place cmd/broker/main.go hands the hold gate to, as a person's credential
// reaches it through the compiled broker: what a failure names it, the line of main.go that hands
// it the gate, which is what the place's control deletes, and the probe that drives it once for a
// person and answers what it did.
type holdPlace struct {
	name   string
	wiring string
	probe  func(r *holdRig, t *testing.T, p *holdPerson) holdAnswer
}

// holdPlaces is every place main hands the gate to. A wiring line of the hold that main gains is
// one more row: the place, the line, and a probe that reads the place through the binary; a
// setting that line reads goes in holdRig.env.
func holdPlaces() []holdPlace {
	return []holdPlace{
		{name: "a session route (POST /v1/grants/{id}/values)", wiring: "api.Deps's Hold: holdGate", probe: (*holdRig).readGrantValues},
		{name: "a launcher route (POST /v1/enrollments)", wiring: "api.Deps's Hold: holdGate", probe: (*holdRig).enrolUnderTheLogin},
		{name: "a token exchange (the machine login's token traded for a session's)", wiring: "exchangeConfig.Hold = holdGate", probe: (*holdRig).tradeTheMachineToken},
		{name: "a device-code redemption", wiring: "exchangeConfig.Hold = holdGate", probe: (*holdRig).redeemADeviceCode},
		{name: "introspection (POST /v1/oauth/introspect as Dispatch)", wiring: "exchangeConfig.Hold = holdGate", probe: (*holdRig).introspectTheSessionToken},
		{name: "an approval-page decision", wiring: "approval.Config's Hold: holdGate", probe: (*holdRig).decideOnThePage},
		{name: "an airlock request", wiring: "airlockproxy.Options's Hold: holdGate", probe: (*holdRig).askThroughTheAirlock},
	}
}

// holdPerson is one person of this test, their two secrets, and what the first boot leaves them
// holding, each made while Dispatch holds nobody: a machine login (its key, its launcher
// credential and its own access token), a session enrolled under it with the grant of the
// person's airlock secret and that grant's proxy password, the token a trade of the machine
// login's token minted, and a browser signed in to the approval page.
type holdPerson struct {
	login string
	// deploySecret is the person's human-tier secret, whose every request waits on their decision
	// on the approval page; airlockSecret is their agent-tier secret behind the airlock, which
	// their own session is granted at once.
	deploySecret, airlockSecret string

	machineKey    *ecdsa.PrivateKey
	credential    string
	machineToken  string
	enrollment    string
	sessionKey    *ecdsa.PrivateKey
	grant         string
	proxyPassword string
	sessionToken  string
	browser       *brokertest.Browser
}

// local is the part of the person's login before the @, which every name this test makes for them
// starts with.
func (p *holdPerson) local() string { return strings.Split(p.login, "@")[0] }

// tradeSession is the session name each trade of the person's machine token writes under: the same
// at every boot, so a trade the hold serves writes under the name the first boot's trade took.
func (p *holdPerson) tradeSession() string { return p.local() + "-hold-session" }

// holdUpstream is the service behind the airlock: a plain HTTP server on loopback that answers
// holdUpstreamBody, counting each request it is carried.
type holdUpstream struct {
	*httptest.Server
	calls atomic.Int64
}

// holdRig is the broker binary on one database behind one public URL, booted again whenever a row
// needs another setting, with a Dispatch stand-in, a sign-in provider, the service behind the
// airlock, and the clients every probe reaches it through.
type holdRig struct {
	f           approvalFixture
	databaseURL string
	public      *front
	frontAddr   string
	dispatch    *projectaccesstest.Server
	accessFile  string
	airlock     []string
	upstream    *holdUpstream
	client      *brokertest.Client
	pods        brokertest.Pods
	launcher    brokertest.Launcher
	broker      *brokerProcess
	proxyAddr   string
	made        int
}

// newHoldRig writes the four secrets the two people own and the airlock's rules for the two behind
// it, starts the Dispatch stand-in, which answers both people as full members and neither as held
// yet, and the service behind the airlock. It boots nothing.
func newHoldRig(t *testing.T) *holdRig {
	t.Helper()
	r := &holdRig{f: newApprovalFixture(t), databaseURL: storetest.URL(t), public: newFront(t)}
	r.frontAddr = strings.TrimPrefix(r.public.URL, "http://")
	secret := func(slug, owner, tier, value string) string {
		return `{"name": "` + testSecretsPrefix + slug + `", "kms_key_id": "` + testSecretsKMSKeyARN +
			`", "tags": {"owner": "` + owner + `", "tier": "` + tier + `"}, "value": "` + value + `"}`
	}
	r.f.secrets = writeTempFile(t, "fake-secrets.json", []byte(`{"secrets": [`+strings.Join([]string{
		secret("ada-deploy-key", ada, "human", "ada-deploy-value"),
		secret("bob-deploy-key", bob, "human", "bob-deploy-value"),
		secret("ada-airlock", ada, "agent", "ada-airlock-value"),
		secret("bob-airlock", bob, "agent", "bob-airlock-value"),
	}, ",\n")+`]}`))

	r.dispatch = projectaccesstest.NewServer(t, nil, map[string]string{ada: projectaccess.MemberFull, bob: projectaccess.MemberFull})
	r.accessFile = writeTempFile(t, "dispatch-access-token", []byte(r.dispatch.Token+"\n"))

	r.upstream = &holdUpstream{}
	r.upstream.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		r.upstream.calls.Add(1)
		_, _ = io.WriteString(w, holdUpstreamBody)
	}))
	t.Cleanup(r.upstream.Close)
	// The two secrets behind the airlock, each bound to a host of its own that no request of this
	// test names: a grant of either opens connections to the proxy, and the request the probe makes
	// goes to the service on loopback, which no name of the grant is bound to, so the proxy carries
	// it as it comes, after the hold. The proxy dials no loopback address but the one
	// BROKER_AIRLOCK_TEST_DIAL_ALLOW names, the service's.
	rules := airlockRulesFile(t, `{"secrets": [
		{"name": "ADA_AIRLOCK", "hosts": ["ada.hold.airlock.test"], "headers": ["authorization"]},
		{"name": "BOB_AIRLOCK", "hosts": ["bob.hold.airlock.test"], "headers": ["authorization"]}
	]}`)
	r.airlock = append(airlockEnv(t),
		"BROKER_AIRLOCK_RULES_FILE="+rules,
		"BROKER_AIRLOCK_TEST_DIAL_ALLOW="+strings.TrimPrefix(r.upstream.URL, "http://"))

	r.client = brokertest.NewClient(t, r.public.URL, r.frontAddr)
	r.client.DPoP = brokerclient.NewDPoPTransport
	r.pods = r.f.pods(t, r.public.URL)
	return r
}

// env is the broker's configuration at one boot: the approval fixture's, with its sign-in, the
// machine-login page that turns the device grant on, the airlock, a lease and an access token
// that outlive the whole run, and, for a boot with the hold on, the Dispatch stand-in as
// BROKER_PROJECT_ACCESS_URL and the file holding the one bearer it takes.
func (r *holdRig) env(holdOn bool) []string {
	env := append(r.f.baseEnv(r.databaseURL, r.public.URL), r.f.signInEnv()...)
	env = append(env, r.airlock...)
	env = append(env,
		"BROKER_MACHINE_LOGIN_URL="+testMachineLoginURL,
		"BROKER_LEASE_SECONDS=3600",
		"BROKER_ACCESS_TOKEN_SECONDS=3600",
	)
	if holdOn {
		env = append(env, "BROKER_PROJECT_ACCESS_URL="+r.dispatch.URL, "BROKER_PROJECT_ACCESS_TOKEN_FILE="+r.accessFile)
	}
	return env
}

// boot stops the broker serving now, if there is one, and starts the binary again on the same
// database behind the same public URL, so every credential, record and sign-in an earlier boot
// made is one this boot reads. It fails t unless the boot log says whether the hold is on: with
// BROKER_PROJECT_ACCESS_URL unset, the line saying the hold is off and why; with it set, the
// project access check on and no line saying the hold is off.
func (r *holdRig) boot(t *testing.T, holdOn bool) {
	t.Helper()
	if r.broker != nil {
		r.broker.Terminate(t)
		if err := r.broker.AwaitExit(t, stopTimeout); err != nil {
			t.Fatalf("the broker at %s exited %v when stopped:\n%s", r.broker.Addr, err, r.broker.Log())
		}
	}
	r.broker = startBroker(t, brokerOptions{Binary: r.f.binary, Env: r.env(holdOn)})
	r.public.route(t, r.broker)
	r.proxyAddr = airlockProxyAddr(t, r.broker)
	r.client.UIToken = r.f.dispatchToken(t)
	if !holdOn {
		r.broker.AwaitRecord(t, "hold of refused people", map[string]any{"state": "off", "reason": "BROKER_PROJECT_ACCESS_URL is not set"})
		return
	}
	r.broker.AwaitRecord(t, "project access check", map[string]any{"state": "on"})
	// The token exchange's line comes after the hold's in the boot, so a log that holds it holds
	// every line the hold could have written.
	r.broker.AwaitRecord(t, "token exchange", nil)
	if lines := recordsWith(r.broker.Log(), "hold of refused people", nil); len(lines) != 0 {
		t.Fatalf("the broker booted with BROKER_PROJECT_ACCESS_URL logged %v, want no line saying the hold is off", lines)
	}
}

// name is a name no other step of this run made: a machine's host, a session's runtime id, a pod.
func (r *holdRig) name(p *holdPerson, kind string) string {
	r.made++
	return fmt.Sprintf("%s-%s-%d", p.local(), kind, r.made)
}

// equip makes what p holds before any hold, through the broker booted now, failing t at any step
// the broker does not serve: the machine login's device grant, approved as Dispatch relays it, and
// its token pair; a session enrolled under it; the grant of p's airlock secret and the proxy
// password its release carries; a trade of the machine login's token; and p's browser, signed in
// to the approval page through a record that waits on p's decision.
func (r *holdRig) equip(t *testing.T, p *holdPerson) {
	t.Helper()
	p.machineKey = brokertest.NewSigningKey(t)
	authorization := r.client.DeviceLogin(t, p.machineKey, p.login, r.name(p, "box"))
	p.credential = r.client.ApproveMachineLogin(t, p.login, authorization.UserCode)
	token, err := r.client.DeviceToken(t, p.machineKey, authorization)
	if err != nil || token.AccessToken == "" {
		t.Fatalf("redeem %s's device code: %v, want the machine login's token pair", p.login, err)
	}
	p.machineToken = token.AccessToken
	p.enrollment, p.sessionKey = r.client.EnrollSession(t, p.machineKey, p.credential, r.name(p, "session"))
	p.grant = r.client.RequestAgentSecret(t, p.sessionKey, p.enrollment, "the hold of the compiled broker", p.airlockSecret)
	status, body := r.client.Session(t, p.sessionKey, p.enrollment, http.MethodPost, "/v1/grants/"+p.grant+"/values", nil)
	var release struct {
		Airlock *struct {
			ProxyPassword string `json:"proxy_password"`
		} `json:"airlock"`
	}
	if err := json.Unmarshal(body, &release); status != http.StatusOK || err != nil || release.Airlock == nil || release.Airlock.ProxyPassword == "" {
		t.Fatalf("%s's release of %s = %d (%v), want 200 carrying the airlock's proxy password", p.login, p.airlockSecret, status, err)
	}
	p.proxyPassword = release.Airlock.ProxyPassword
	status, answer := newLiveBroker(r.frontAddr, p.machineKey).exchange(t, machineTrade(p.machineToken, p.tradeSession(), ""))
	if p.sessionToken, _ = answer["access_token"].(string); status != http.StatusOK || p.sessionToken == "" {
		t.Fatalf("the trade of %s's machine token = %d %v %v, want 200 and a token", p.login, status, answer["error"], answer["error_description"])
	}
	p.browser = brokertest.NewBrowser(t, r.public.URL, http.DefaultTransport, r.f.issuer)
	p.browser.SignIn(t, r.pendingRecord(t, p), brokertest.Person(p.login))
}

// pendingRecord has a new pod ask for p's human-tier secret and answers the record the request
// waits on, failing t unless it waits on p's decision. A pod's enrollment names no person, so the
// hold asks nothing of a pod: what a probe reads is p's decision alone.
func (r *holdRig) pendingRecord(t *testing.T, p *holdPerson) string {
	t.Helper()
	pod := r.pods.Enroll(r.launcher, r.name(p, "pod"), approvalPodAccount)
	reply := r.pods.Ask(pod, p.deploySecret)
	if reply.State != "pending" || reply.RecordID == nil || string(reply.Approver) != `"`+p.login+`"` {
		t.Fatalf("a pod's request for %s = %+v (approver %s), want pending on a record %s decides", p.deploySecret, reply, reply.Approver, p.login)
	}
	return *reply.RecordID
}

// heldText is the broker's refusal of a held person's credential, naming person: the text of the
// API's 403 MEMBERSHIP_HELD, of the token endpoint's membership_held, of the airlock's 403 and of
// the approval page's refusal, which ends it with a full stop.
func heldText(person string) string {
	return person + "'s credentials are held by Dispatch; they work again when " + person +
		" signs in to Dispatch, or when Dispatch can confirm " + person + " with Google again"
}

// apiAnswer reads an answer of the broker's API to p's call: served at status served, held at 403
// MEMBERSHIP_HELD naming p, and neither otherwise.
func apiAnswer(p *holdPerson, call string, status, served int, body []byte) holdAnswer {
	if status == served {
		return holdAnswer{answeredServed, fmt.Sprintf("%s answered %d", call, status)}
	}
	said := fmt.Sprintf("%s answered %d %s", call, status, strings.TrimSpace(string(body)))
	var refused errorBody
	if json.Unmarshal(body, &refused) == nil && status == http.StatusForbidden && refused.Code == "MEMBERSHIP_HELD" && refused.Error == heldText(p.login) {
		return holdAnswer{answeredHeld, said}
	}
	return holdAnswer{answeredNeither, said}
}

// readGrantValues is p's session reading the values of its grant.
func (r *holdRig) readGrantValues(t *testing.T, p *holdPerson) holdAnswer {
	t.Helper()
	status, body := r.client.Session(t, p.sessionKey, p.enrollment, http.MethodPost, "/v1/grants/"+p.grant+"/values", nil)
	return apiAnswer(p, "POST /v1/grants/"+p.grant+"/values", status, http.StatusOK, body)
}

// enrolUnderTheLogin is p's machine login enrolling a new session.
func (r *holdRig) enrolUnderTheLogin(t *testing.T, p *holdPerson) holdAnswer {
	t.Helper()
	thumbprint, err := proof.Thumbprint(&brokertest.NewSigningKey(t).PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	status, body := r.client.Launcher(t, p.machineKey, p.credential, http.MethodPost, "/v1/enrollments",
		map[string]any{"kind": "session", "runtime_id": r.name(p, "session"), "thumbprint": thumbprint})
	return apiAnswer(p, "POST /v1/enrollments", status, http.StatusCreated, body)
}

// tradeTheMachineToken is p's machine login trading its own token for a session's, as a dev box
// session does.
func (r *holdRig) tradeTheMachineToken(t *testing.T, p *holdPerson) holdAnswer {
	t.Helper()
	status, answer := newLiveBroker(r.frontAddr, p.machineKey).exchange(t, machineTrade(p.machineToken, p.tradeSession(), ""))
	token, _ := answer["access_token"].(string)
	description, _ := answer["error_description"].(string)
	said := fmt.Sprintf("the trade answered %d %v %q", status, answer["error"], description)
	switch {
	case status == http.StatusOK && token != "":
		return holdAnswer{answeredServed, fmt.Sprintf("the trade answered %d and a token", status)}
	case status == http.StatusBadRequest && answer["error"] == oauthwire.MembershipHeld && description == heldText(p.login) && token == "":
		return holdAnswer{answeredHeld, said}
	}
	return holdAnswer{answeredNeither, said}
}

// redeemADeviceCode is a new machine login of p's: its device authorization, p's approval as
// Dispatch relays it, and the redemption of its device code. Neither of the first two asks the
// hold; the redemption does.
func (r *holdRig) redeemADeviceCode(t *testing.T, p *holdPerson) holdAnswer {
	t.Helper()
	key := brokertest.NewSigningKey(t)
	authorization := r.client.DeviceLogin(t, key, p.login, r.name(p, "box"))
	r.client.ApproveMachineLogin(t, p.login, authorization.UserCode)
	token, err := r.client.DeviceToken(t, key, authorization)
	if err == nil && token.AccessToken != "" {
		return holdAnswer{answeredServed, "the redemption answered a token pair"}
	}
	var refused *oauth2.RetrieveError
	if !errors.As(err, &refused) {
		return holdAnswer{answeredNeither, fmt.Sprintf("the redemption answered %v", err)}
	}
	said := fmt.Sprintf("the redemption answered %d %s %q", refused.Response.StatusCode, refused.ErrorCode, refused.ErrorDescription)
	if refused.Response.StatusCode == http.StatusBadRequest && refused.ErrorCode == oauthwire.MembershipHeld && refused.ErrorDescription == heldText(p.login) {
		return holdAnswer{answeredHeld, said}
	}
	return holdAnswer{answeredNeither, said}
}

// introspectTheSessionToken is Dispatch reading back the token the first boot's trade minted for
// p: held when the answer is active and held, served when it is active and not held.
func (r *holdRig) introspectTheSessionToken(t *testing.T, p *holdPerson) holdAnswer {
	t.Helper()
	status, answer := newPlainBroker(r.frontAddr).introspect(t, p.sessionToken, r.f.dispatchToken(t))
	said := fmt.Sprintf("introspection answered %d active=%v held=%v", status, answer["active"], answer["held"])
	switch {
	case status != http.StatusOK || answer["active"] != true:
		return holdAnswer{answeredNeither, said}
	case answer["held"] == true:
		return holdAnswer{answeredHeld, said}
	}
	return holdAnswer{answeredServed, said}
}

// decideOnThePage is p approving, in the browser the first boot signed in, a new pod's request for
// p's human-tier secret.
func (r *holdRig) decideOnThePage(t *testing.T, p *holdPerson) holdAnswer {
	t.Helper()
	record := r.pendingRecord(t, p)
	form := brokertest.DecisionForm(t, p.browser.Page(t, record).Body, record)
	if form == nil {
		t.Fatalf("the page for %s offers %s no decision", record, p.login)
	}
	res := p.browser.FromPage(t, "/approve", brokertest.Decision(form, "approve"))
	if want := "/approve/" + record + "?decided=" + record; res.Status == http.StatusSeeOther && res.Header.Get("Location") == want {
		return holdAnswer{answeredServed, fmt.Sprintf("the decision answered %d to %s", res.Status, want)}
	}
	text := brokertest.PageText(t, res.Body)
	said := fmt.Sprintf("the decision answered %d: %s", res.Status, text)
	if res.Status == http.StatusForbidden && strings.Contains(text, heldText(p.login)) {
		return holdAnswer{answeredHeld, said}
	}
	return holdAnswer{answeredNeither, said}
}

// askThroughTheAirlock is a command of p's session sending a plain request to the service behind
// the airlock through the proxy, with its grant's proxy password: served when the service's answer
// comes back from the one request it was carried, held when the proxy refuses it as held and
// carries nothing.
func (r *holdRig) askThroughTheAirlock(t *testing.T, p *holdPerson) holdAnswer {
	t.Helper()
	proxyURL := &url.URL{Scheme: "http", User: url.UserPassword("agent", p.proxyPassword), Host: r.proxyAddr}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	before := r.upstream.calls.Load()
	res, err := (&http.Client{Transport: transport, Timeout: 30 * time.Second}).Get(r.upstream.URL + "/" + p.local())
	if err != nil {
		return holdAnswer{answeredNeither, fmt.Sprintf("the request through the proxy failed: %v", err)}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return holdAnswer{answeredNeither, fmt.Sprintf("the proxy's answer could not be read: %v", err)}
	}
	carried := r.upstream.calls.Load() - before
	said := fmt.Sprintf("the proxy answered %d %q, and carried %d requests to the service", res.StatusCode, strings.TrimSpace(string(body)), carried)
	switch {
	case res.StatusCode == http.StatusOK && string(body) == holdUpstreamBody && carried == 1:
		return holdAnswer{answeredServed, said}
	case res.StatusCode == http.StatusForbidden && strings.Contains(string(body), heldText(p.login)) && carried == 0:
		return holdAnswer{answeredHeld, said}
	}
	return holdAnswer{answeredNeither, said}
}

// expectAtEachPlace drives every place for p through the broker booted now and fails t, naming the
// place and the line of main that hands it the gate, at each that does not answer want; while says
// what is true of this boot.
func (r *holdRig) expectAtEachPlace(t *testing.T, while string, p *holdPerson, want string) {
	t.Helper()
	for _, place := range holdPlaces() {
		got := place.probe(r, t, p)
		t.Logf("%s, %s at %s: %s", while, p.login, place.name, got.said)
		if got.outcome != want {
			t.Errorf("%s, the compiled broker %s %s at %s, want %s: %s. main.go hands this place the gate in %s",
				while, verbOf(got.outcome), p.login, place.name, want, got.said, place.wiring)
		}
	}
}

// verbOf is what the broker did, as a failure says it.
func verbOf(outcome string) string {
	switch outcome {
	case answeredServed:
		return "served"
	case answeredHeld:
		return "held"
	}
	return "neither served nor held"
}

// TestTheCompiledBrokerHoldsAHeldPerson: the broker built from the tree (buildBroker), against a
// stand-in for Dispatch's person route, refuses a person Dispatch holds at each place main hands
// the gate to, a session route, a launcher route, a token exchange, a device-code redemption, an
// approval-page decision and an airlock request, and its introspection reports her token held,
// while bob is served at each and his token is not held. With BROKER_PROJECT_ACCESS_URL unset, the
// same broker serves her at each, and its boot log says the hold is off.
//
// Three boots of the binary on one database behind one public URL. The first, with the hold on and
// Dispatch holding nobody, makes what ada and bob hold and finds each place serving both, which is
// what makes the second boot's refusals the hold's. Dispatch then holds ada, and the second boot,
// the hold on, reads every place again: ada refused, bob served. The hold is set between boots
// because the broker keeps each person's answer for 30 seconds (projectaccess.CacheTTL), which no
// clock of this test moves in another process; a new process asks Dispatch afresh. The third boot
// has no BROKER_PROJECT_ACCESS_URL: Dispatch still holds ada, and the broker's database keeps the
// held answer the second boot read, yet the broker asks nobody and serves her at each place.
//
// Each place's control deletes the line of main.go that hands it the gate (holdPlaces' wiring):
// the second boot then serves ada there, and this test fails naming that place.
func TestTheCompiledBrokerHoldsAHeldPerson(t *testing.T) {
	r := newHoldRig(t)
	heldAda := &holdPerson{login: ada, deploySecret: approvalSecretName, airlockSecret: "ADA_AIRLOCK"}
	servedBob := &holdPerson{login: bob, deploySecret: "BOB_DEPLOY_KEY", airlockSecret: "BOB_AIRLOCK"}

	r.boot(t, true)
	// The service whose pods ask for each person's human-tier secret, which pendingRecord enrols.
	r.launcher = r.pods.LogIn(approvalLauncherService, approvalLauncherAccount)
	r.equip(t, heldAda)
	r.equip(t, servedBob)
	for _, p := range []*holdPerson{heldAda, servedBob} {
		r.expectAtEachPlace(t, "with the hold on and Dispatch holding nobody", p, answeredServed)
	}
	if t.Failed() {
		t.Fatal("a place refused a person Dispatch does not hold, so a refusal after the hold would not be the hold's")
	}

	r.dispatch.SetHeld(ada, true)
	r.boot(t, true)
	r.expectAtEachPlace(t, "with Dispatch holding ada", heldAda, answeredHeld)
	r.expectAtEachPlace(t, "with Dispatch holding ada", servedBob, answeredServed)

	r.boot(t, false)
	asked := r.dispatch.Calls()
	r.expectAtEachPlace(t, "with BROKER_PROJECT_ACCESS_URL unset and Dispatch still holding ada", heldAda, answeredServed)
	if calls := r.dispatch.Calls() - asked; calls != 0 {
		t.Errorf("the broker with BROKER_PROJECT_ACCESS_URL unset asked the Dispatch stand-in %d times, want none", calls)
	}
}
