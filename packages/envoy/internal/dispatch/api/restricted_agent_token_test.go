package api

import (
	"crypto/ecdsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/sjawhar/envoy/internal/broker/brokertest"
	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/broker/projectaccess"
	"github.com/sjawhar/envoy/internal/broker/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/internal/brokerclient"
	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/brokertoken/brokertokentest"
	"github.com/sjawhar/envoy/internal/dispatch/auth"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// restrictedTokenRig is one deployment end to end: the real broker (brokertest.Rig, on
// BROKER_TEST_DATABASE_URL), whose token exchange asks a stand-in of Dispatch's project-access
// routes what kind of member each person is, behind Dispatch's own routes, which relay the
// credential pages to that broker and decide every broker token by asking it. Dispatch holds ACC,
// which alice owns and whose access list names erin, a restricted member; carol is a full member.
type restrictedTokenRig struct {
	broker   *brokertest.Rig
	client   *brokertest.Client
	dispatch http.Handler
}

func newRestrictedTokenRig(t *testing.T) restrictedTokenRig {
	t.Helper()
	access := projectaccesstest.NewServer(t,
		map[string]projectaccesstest.Row{"ACC": {Visibility: "private", Owner: "alice", Listed: []string{accessErin}}},
		map[string]string{accessErin: projectaccess.MemberRestricted, accessCarol: projectaccess.MemberFull})
	broker := brokertest.NewRig(t, brokertest.WithProjectAccess(projectaccess.New(access.URL, access.Account, nil)))
	client := brokertest.NewClient(t, broker.URL, strings.TrimPrefix(broker.URL, "http://"))
	client.DPoP, client.UIToken = brokerclient.NewDPoPTransport, broker.UIToken
	dispatchAccount := accountTokenFile(t, broker.UIToken)
	served, _ := newAccessServerWith(t, testServerOptions{
		signInOutside: true,
		brokerTokens: func(database *store.Store) *brokertoken.Verifier {
			verifier, err := brokertoken.New(brokertoken.Options{
				BrokerURL: broker.URL, Account: brokertoken.NewAccountToken(dispatchAccount), Origin: testServerURL,
				Replay: store.NewDPoPProofs(database.Pool), Address: auth.ForwardedAddress,
			})
			if err != nil {
				t.Fatalf("build the broker token verifier: %v", err)
			}
			return verifier
		},
		agentSecretsURL: broker.URL, agentSecretsTokenFile: dispatchAccount,
	})
	if listed := dispatchRequest(t, served, http.MethodPut, "/api/v1/projects/ACC/access/people/"+accessErin, nil, "alice"); listed.Code != http.StatusOK {
		t.Fatalf("alice names erin on ACC's list: status=%d body=%s", listed.Code, listed.Body.String())
	}
	return restrictedTokenRig{broker: broker, client: client, dispatch: served}
}

// machineLogin is one machine's pending device login for person: the key it holds and the device
// authorization the broker answered, whose user code the person types at Dispatch.
type machineLogin struct {
	key    *ecdsa.PrivateKey
	device *oauth2.DeviceAuthResponse
}

func (r restrictedTokenRig) startLogin(t *testing.T, person, host string) machineLogin {
	t.Helper()
	key := brokertest.NewSigningKey(t)
	return machineLogin{key: key, device: r.client.DeviceLogin(t, key, person, host)}
}

// lookUp is the machine-lookup of login's code through Dispatch, by login at a browser.
func (r restrictedTokenRig) lookUp(t *testing.T, login string, machine machineLogin) *httptest.ResponseRecorder {
	t.Helper()
	return dispatchRequest(t, r.dispatch, http.MethodPost, "/api/v1/credential-requests/machine-lookup", map[string]any{"code": machine.device.UserCode}, login)
}

// recordOf is the record the broker holds for machine's login, read through Dispatch by its own
// person, which a full member's lookup and a restricted member's of their own both answer.
func (r restrictedTokenRig) recordOf(t *testing.T, person string, machine machineLogin) (id, state string) {
	t.Helper()
	looked := r.lookUp(t, person, machine)
	var record struct {
		RecordID string `json:"record_id"`
		State    string `json:"state"`
	}
	if looked.Code != http.StatusOK || json.Unmarshal(looked.Body.Bytes(), &record) != nil || record.RecordID == "" {
		t.Fatalf("%s's lookup of their own machine login through Dispatch = %d %s, want 200 and its record", person, looked.Code, looked.Body.String())
	}
	return record.RecordID, record.State
}

// approveAtBrowser is login's approve of machine's login through Dispatch, at a browser, with the
// code the machine shows.
func (r restrictedTokenRig) approveAtBrowser(t *testing.T, login, recordID string, machine machineLogin) *httptest.ResponseRecorder {
	t.Helper()
	return dispatchRequest(t, r.dispatch, http.MethodPost, "/api/v1/credential-requests/"+recordID+"/approve", map[string]any{"code": machine.device.UserCode}, login)
}

// trade is the machine's trade of its approved login's token for a session's, naming session and
// asking for projects, with a DPoP proof of its own key: the device-code exchange first, then the
// RFC 8693 exchange of that token at the broker, as a dev box session's client makes it. It
// answers the trade's status and body.
func (r restrictedTokenRig) trade(t *testing.T, machine machineLogin, session, projects string) (int, map[string]any) {
	t.Helper()
	token, err := r.client.DeviceToken(t, machine.key, machine.device)
	if err != nil {
		t.Fatalf("the machine's device-code exchange: %v", err)
	}
	form := url.Values{
		"grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"client_id":            {oauthwire.ExchangeClientID},
		"subject_token":        {token.AccessToken},
		"subject_token_type":   {oauthwire.SubjectTokenTypeAccessToken},
		oauthwire.FormSession:  {session},
		oauthwire.FormProjects: {projects},
	}
	client := &http.Client{Transport: brokerclient.NewDPoPTransport(machine.key, http.DefaultTransport), Timeout: 30 * time.Second}
	response, err := client.PostForm(r.broker.URL+"/v1/oauth/token", form)
	if err != nil {
		t.Fatalf("POST /v1/oauth/token: %v", err)
	}
	defer response.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode the trade's answer: %v", err)
	}
	return response.StatusCode, body
}

// introspect is the broker's own answer about token, asked as Dispatch asks it.
func (r restrictedTokenRig) introspect(t *testing.T, token string) brokertoken.Introspection {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, r.broker.URL+brokertoken.IntrospectPath, strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer "+r.broker.UIToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", brokertoken.IntrospectPath, err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var answer brokertoken.Introspection
	if response.StatusCode != http.StatusOK || json.Unmarshal(raw, &answer) != nil {
		t.Fatalf("POST %s = %d %s, want the broker's answer", brokertoken.IntrospectPath, response.StatusCode, raw)
	}
	return answer
}

// presenting is token presented to Dispatch by the machine that holds machine's key, under the
// DPoP scheme with a fresh proof of that key, on method and path with body.
func (r restrictedTokenRig) presenting(t *testing.T, machine machineLogin, token, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	caller := brokertokentest.NewCallerWith(t, machine.key)
	presented := (&brokerCaller{caller: caller, jkt: caller.JKT()}).presenting(t, method, path, token, body)
	response := httptest.NewRecorder()
	r.dispatch.ServeHTTP(response, presented)
	return response
}

// TestARestrictedMembersAgentTokenIsNamedAndNeitherMintsNorApproves runs an outside person's agent
// on one deployment, end to end. erin, a restricted member, approves her own machine's device
// login at her browser through Dispatch, which reaches the broker; her machine trades its token
// for a session's naming ACC, and the broker answers a token of the named scope listing exactly
// ACC that lives at most five minutes. Presented to Dispatch, that token acts for erin in ACC, and
// it neither mints a personal token (POST /api/v1/me/agent-tokens) nor approves a machine login,
// each 403 OAUTH_SCOPE_REQUIRED, the login staying pending. Her browser approve of carol's pending
// machine login is 403 RESTRICTED_MEMBER and leaves it pending.
//
// The control runs on the same rig: carol, a full member, approves her own login at her browser;
// her machine's trade, naming no project, is the person scope; and with that token the mint
// answers 201 and the approve of carol's second machine login answers 200.
//
// Controls: mint the person scope for a restricted member's trade, and the first row fails naming
// the scope; refuse a restricted member's browser decision as before, and erin's approve of her
// own login fails naming RESTRICTED_MEMBER.
func TestARestrictedMembersAgentTokenIsNamedAndNeitherMintsNorApproves(t *testing.T) {
	r := newRestrictedTokenRig(t)

	erins := r.startLogin(t, accessErin, "erin-laptop")
	erinsRecord, _ := r.recordOf(t, accessErin, erins)
	if approved := r.approveAtBrowser(t, accessErin, erinsRecord, erins); approved.Code != http.StatusOK || !strings.Contains(approved.Body.String(), `"state":"approved"`) {
		t.Fatalf("erin's browser approve of her own machine login through Dispatch = %d %s, want 200 approved", approved.Code, approved.Body.String())
	}
	status, body := r.trade(t, erins, "erin-agent", "ACC")
	if status != http.StatusOK {
		t.Fatalf("erin's machine's trade naming ACC = %d %v, want 200", status, body)
	}
	token, _ := body["access_token"].(string)
	answer := r.introspect(t, token)
	if !answer.Active || answer.ProjectScope != brokertoken.ProjectScopeNamed || !slices.Equal(answer.Projects, []string{"ACC"}) || answer.Person != accessErin {
		t.Fatalf("erin's agent's token introspects as %+v, want a live token of project scope %s listing exactly [ACC], acting for %s",
			answer, brokertoken.ProjectScopeNamed, accessErin)
	}
	if expiresIn, _ := body["expires_in"].(float64); expiresIn <= 0 || expiresIn > 300 {
		t.Fatalf("erin's agent's token expires_in %v, want at most 300 seconds", body["expires_in"])
	}
	if lifetime := time.Until(time.Unix(answer.ExpiresAt, 0)); lifetime > 300*time.Second {
		t.Fatalf("erin's agent's token is live for %s more, want at most 5m0s", lifetime)
	}

	if acts := r.presenting(t, erins, token, http.MethodGet, "/api/v1/whoami", nil); acts.Code != http.StatusOK ||
		!strings.Contains(acts.Body.String(), `"owner":"erin@other.example"`) || !strings.Contains(acts.Body.String(), `"project":"ACC"`) {
		t.Fatalf("whoami under erin's agent's token = %d %s, want it acting for erin in ACC", acts.Code, acts.Body.String())
	}
	if minted := r.presenting(t, erins, token, http.MethodPost, "/api/v1/me/agent-tokens", map[string]any{"name": "minted by erin's agent"}); minted.Code != http.StatusForbidden ||
		!strings.Contains(minted.Body.String(), `"code":"OAUTH_SCOPE_REQUIRED"`) {
		t.Errorf("POST /api/v1/me/agent-tokens under erin's agent's token = %d %s, want 403 OAUTH_SCOPE_REQUIRED", minted.Code, minted.Body.String())
	}
	second := r.startLogin(t, accessErin, "erin-desktop")
	secondRecord, _ := r.recordOf(t, accessErin, second)
	if decided := r.presenting(t, erins, token, http.MethodPost, "/api/v1/credential-requests/"+secondRecord+"/approve", map[string]any{"code": second.device.UserCode}); decided.Code != http.StatusForbidden ||
		!strings.Contains(decided.Body.String(), `"code":"OAUTH_SCOPE_REQUIRED"`) {
		t.Errorf("an approve under erin's agent's token = %d %s, want 403 OAUTH_SCOPE_REQUIRED", decided.Code, decided.Body.String())
	}
	if _, state := r.recordOf(t, accessErin, second); state != "pending" {
		t.Errorf("erin's second machine login after her agent's approve is %s, want pending", state)
	}

	carols := r.startLogin(t, accessCarol, "carol-laptop")
	carolsRecord, _ := r.recordOf(t, accessCarol, carols)
	if refused := r.approveAtBrowser(t, accessErin, carolsRecord, carols); refused.Code != http.StatusForbidden || !strings.Contains(refused.Body.String(), `"code":"RESTRICTED_MEMBER"`) {
		t.Errorf("erin's browser approve of carol's machine login = %d %s, want 403 RESTRICTED_MEMBER", refused.Code, refused.Body.String())
	}
	if _, state := r.recordOf(t, accessCarol, carols); state != "pending" {
		t.Fatalf("carol's machine login after erin's approve is %s, want pending", state)
	}

	// The control, on the same rig: a full member's agent.
	if approved := r.approveAtBrowser(t, accessCarol, carolsRecord, carols); approved.Code != http.StatusOK {
		t.Fatalf("carol's browser approve of her own machine login = %d %s, want 200", approved.Code, approved.Body.String())
	}
	status, body = r.trade(t, carols, "carol-agent", "")
	if status != http.StatusOK {
		t.Fatalf("carol's machine's trade = %d %v, want 200", status, body)
	}
	carolsToken, _ := body["access_token"].(string)
	if answer := r.introspect(t, carolsToken); !answer.Active || answer.ProjectScope != brokertoken.ProjectScopePerson || answer.Projects != nil {
		t.Fatalf("carol's agent's token introspects as %+v, want a live token of project scope %s naming no project", answer, brokertoken.ProjectScopePerson)
	}
	if minted := r.presenting(t, carols, carolsToken, http.MethodPost, "/api/v1/me/agent-tokens", map[string]any{"name": "minted by carol's agent"}); minted.Code != http.StatusCreated {
		t.Errorf("POST /api/v1/me/agent-tokens under carol's agent's token = %d %s, want 201", minted.Code, minted.Body.String())
	}
	carolsSecond := r.startLogin(t, accessCarol, "carol-desktop")
	carolsSecondRecord, _ := r.recordOf(t, accessCarol, carolsSecond)
	if decided := r.presenting(t, carols, carolsToken, http.MethodPost, "/api/v1/credential-requests/"+carolsSecondRecord+"/approve", map[string]any{"code": carolsSecond.device.UserCode}); decided.Code != http.StatusOK {
		t.Errorf("an approve of carol's second machine login under carol's agent's token = %d %s, want 200", decided.Code, decided.Body.String())
	}
}
