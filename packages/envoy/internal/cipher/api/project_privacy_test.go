// project_privacy_test.go drives GET /v1/project-privacy, the route a listener reads at each privacy
// decision. It answers only an account BROKER_NATS_SERVICES binds to a listener, asks Dispatch afresh
// on every call through the broker's real projectaccess client, answers no list at all when that read
// fails, and holds at most 64 calls at once. Dispatch is a stub of its private-projects route, which
// each test drives.
package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/cipher/api"
	"github.com/sjawhar/envoy/internal/cipher/natscallout"
	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
	"github.com/sjawhar/envoy/internal/cipher/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/internal/oidc"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
	"github.com/sjawhar/envoy/internal/ratelimit"
)

const (
	// privacyListenerAccount is a hub's listener and dispatchListenerAccount Dispatch's listener
	// Deployment, each a ServiceAccount BROKER_NATS_SERVICES binds to a listener.
	privacyListenerAccount  = "system:serviceaccount:example-hub:example-listener"
	dispatchListenerAccount = "system:serviceaccount:example-ns:example-dispatch-listener"
	// privacyStubToken is the bearer the broker presents to the stub, as it presents its own
	// account token to Dispatch.
	privacyStubToken = "dispatch-access-example"
	// privacyPath is the route under test.
	privacyPath = "/v1/project-privacy"
)

// privacyServices is the BROKER_NATS_SERVICES the rig reads, parsed as the broker parses it: the
// hub's listener and Dispatch's listener Deployment in the cluster, a listener on a VM presenting a
// Google identity token, Dispatch's app account and a Legion daemon. Dispatch's listener is a
// listener entry, the kind the auth service answers it with today.
var privacyServices = strings.Join([]string{
	"listener/example-hub/*=" + privacyListenerAccount,
	"listener/example-dispatch/*=" + dispatchListenerAccount,
	"listener/example-vm/*=google:" + listenerGoogleAccount,
	"dispatch/example-dispatch/*=" + dispatchAccount,
	"daemon/-/*=" + daemonAccount,
}, " ")

// privacyStub stands in for Dispatch's GET /api/v1/access/private-projects. Each request is counted,
// then answered by the answer that holds when it arrives, so a test can change Dispatch between two
// calls, hold calls open, or make Dispatch fail.
type privacyStub struct {
	url   string
	calls atomic.Int64

	mu     sync.Mutex
	answer http.HandlerFunc
}

// newPrivacyStub serves answer until t ends.
func newPrivacyStub(t *testing.T, answer http.HandlerFunc) *privacyStub {
	t.Helper()
	stub := &privacyStub{answer: answer}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/access/private-projects" || r.Header.Get("Authorization") != "Bearer "+privacyStubToken {
			t.Errorf("the broker asked Dispatch %s %s with %q, want GET /api/v1/access/private-projects with its own bearer", r.Method, r.URL.Path, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusNotFound)
			return
		}
		stub.calls.Add(1)
		stub.mu.Lock()
		answer := stub.answer
		stub.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(server.Close)
	stub.url = server.URL
	return stub
}

// set makes answer Dispatch's answer to every request from the next one on.
func (s *privacyStub) set(answer http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answer = answer
}

// answerWith answers status with body as Dispatch writes JSON.
func answerWith(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

// newPrivacyServer is the broker's test server reading Dispatch at stub through a projectaccess
// client on httpClient (nil for the client's own default), with privacyServices as its
// BROKER_NATS_SERVICES and a Google issuer of its own for the VM listener's and the operator's
// identity tokens, and each of adjust applied after.
func newPrivacyServer(t *testing.T, stub *privacyStub, httpClient *http.Client, adjust ...func(*api.Deps)) *googleCallers {
	t.Helper()
	services, err := natscallout.ParseServices(privacyServices)
	if err != nil {
		t.Fatalf("parse BROKER_NATS_SERVICES: %v", err)
	}
	return newPrivacyServerWith(t, func(d *api.Deps) {
		d.ServiceKinds = natscallout.ServiceKinds(services)
		d.ProjectPrivacy = projectaccess.New(stub.url, projectaccesstest.StaticAccount(privacyStubToken), httpClient)
		for _, a := range adjust {
			a(d)
		}
	})
}

// newPrivacyServerWith is the broker's test server with its Google issuer, adjusted by adjust.
func newPrivacyServerWith(t *testing.T, adjust func(*api.Deps)) *googleCallers {
	t.Helper()
	google := oidctest.New(t)
	key := google.PublishKey(t, "google-key")
	verifier, err := oidc.New(context.Background(), google.URL(), machineAudience)
	if err != nil {
		t.Fatalf("oidc.New for Google: %v", err)
	}
	ts := newTestServerWith(t, func(d *api.Deps) {
		d.GoogleAccountTokens = api.GoogleTokens{Verifier: verifier}
		adjust(d)
	})
	return &googleCallers{testServer: ts, issuer: google, key: key}
}

// privacyAnswer is one answer of the route: its status, its body and the code a refusal names.
type privacyAnswer struct {
	status int
	body   []byte
	code   string
}

// privacyCall makes one GET /v1/project-privacy presenting bearer. It is safe to call from any
// goroutine: a failure to send is its error, never a failed test.
func privacyCall(url, bearer string) (privacyAnswer, error) {
	request, err := http.NewRequest(http.MethodGet, url+privacyPath, nil)
	if err != nil {
		return privacyAnswer{}, err
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		return privacyAnswer{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return privacyAnswer{}, err
	}
	var refusal struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &refusal)
	return privacyAnswer{status: resp.StatusCode, body: body, code: refusal.Code}, nil
}

// privacy is privacyCall from the test's own goroutine.
func (g *googleCallers) privacy(t *testing.T, bearer string) privacyAnswer {
	t.Helper()
	answer, err := privacyCall(g.URL, bearer)
	if err != nil {
		t.Fatalf("GET %s: %v", privacyPath, err)
	}
	return answer
}

// sameJSON reports whether got and want are the same JSON value.
func sameJSON(t *testing.T, got []byte, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("the test's own JSON does not decode: %v", err)
	}
	return reflect.DeepEqual(g, w)
}

// TestOnlyAListenerReadsProjectPrivacy: an account BROKER_NATS_SERVICES binds to a listener (the
// hub's, Dispatch's listener Deployment's, and a VM's presenting a Google identity token) is
// answered Dispatch's rows unchanged. Every other caller whose platform identity verifies is refused
// 403 not_listener before Dispatch is asked: Dispatch's app account, a session pod's own token, a
// daemon's identity and the operator's Google identity. A listener's token that does not verify,
// and no token at all, are refused 401, Dispatch not asked either.
func TestOnlyAListenerReadsProjectPrivacy(t *testing.T) {
	const body = `{"projects":[` +
		`{"project":"ALPHA","slack_channels":["C0A","C0B"],"slack_channels_changed_at":"2026-11-19T10:00:00Z"},` +
		`{"project":"BETA","slack_channels":[],"slack_channels_changed_at":null}]}`
	stub := newPrivacyStub(t, answerWith(http.StatusOK, body))
	g := newPrivacyServer(t, stub, nil)

	for _, row := range []struct{ name, token string }{
		{"the hub's listener", g.accountToken(t, privacyListenerAccount, "", time.Hour)},
		{"Dispatch's listener", g.accountToken(t, dispatchListenerAccount, "", time.Hour)},
		{"a listener on a VM", g.live(t, listenerGoogleAccount)},
	} {
		got := g.privacy(t, row.token)
		if got.status != http.StatusOK {
			t.Errorf("%s: GET %s = %d %s, want 200", row.name, privacyPath, got.status, got.body)
			continue
		}
		if !sameJSON(t, got.body, body) {
			t.Errorf("%s was answered %s, want the stub's body unchanged, %s", row.name, got.body, body)
		}
	}
	asked := stub.calls.Load()
	if asked != 3 {
		t.Errorf("three listeners' calls asked Dispatch %d times, want 3", asked)
	}

	for _, row := range []struct {
		name, token string
		status      int
		code        string
	}{
		{"Dispatch's app account", g.uiToken, http.StatusForbidden, "not_listener"},
		{"a session token", g.podToken(t, workerAccount, uuid.NewString()), http.StatusForbidden, "not_listener"},
		{"a daemon's identity", g.accountToken(t, daemonAccount, "", time.Hour), http.StatusForbidden, "not_listener"},
		{"the operator's identity", g.live(t, testApprover), http.StatusForbidden, "not_listener"},
		// A listener's account with a token that does not verify is refused as on every route
		// that takes an identity token, before its account is looked up.
		{"the hub's listener's expired token", g.accountToken(t, privacyListenerAccount, "", -time.Minute), http.StatusUnauthorized, "ACCOUNT_TOKEN_EXPIRED"},
		{"no token", "", http.StatusUnauthorized, "ACCOUNT_TOKEN_INVALID"},
	} {
		got := g.privacy(t, row.token)
		if got.status != row.status || got.code != row.code {
			t.Errorf("%s: GET %s = %d %s, want %d %s", row.name, privacyPath, got.status, got.body, row.status, row.code)
		}
	}
	if after := stub.calls.Load(); after != asked {
		t.Errorf("the refused callers asked Dispatch %d times, want none", after-asked)
	}
}

// TestProjectPrivacyIsBounded: with 64 calls held open at the stub, the 65th is answered 503 busy at
// once, while all 64 are still held, and makes no request of the stub; once one held call
// returns, the next call is served.
func TestProjectPrivacyIsBounded(t *testing.T) {
	const body = `{"projects":[]}`
	release := make(chan struct{})
	arrived := make(chan struct{}, 128)
	var holding atomic.Bool
	holding.Store(true)
	stub := newPrivacyStub(t, func(w http.ResponseWriter, r *http.Request) {
		if holding.Load() {
			arrived <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		answerWith(http.StatusOK, body)(w, r)
	})
	g := newPrivacyServer(t, stub, nil)
	var releaseAll sync.Once
	// Registered after both servers, so it runs before either closes and waits on a held call.
	t.Cleanup(func() { releaseAll.Do(func() { close(release) }) })
	token := g.accountToken(t, privacyListenerAccount, "", time.Hour)

	held := make(chan privacyAnswer, 64)
	for range 64 {
		go func() {
			answer, err := privacyCall(g.URL, token)
			if err != nil {
				answer = privacyAnswer{body: []byte(err.Error())}
			}
			held <- answer
		}()
	}
	for i := range 64 {
		select {
		case <-arrived:
		case <-time.After(time.Minute):
			t.Fatalf("only %d of 64 calls reached the stub", i)
		}
	}

	next := make(chan privacyAnswer, 1)
	go func() {
		answer, err := privacyCall(g.URL, token)
		if err != nil {
			answer = privacyAnswer{body: []byte(err.Error())}
		}
		next <- answer
	}()
	// "At once" is "while every one of the 64 is still held": a call that waited for a slot would
	// be answered only after a held call returned, and then it would reach the stub. The wait
	// itself is generous, so a loaded machine fails nothing that is right.
	var got privacyAnswer
	select {
	case got = <-next:
	case <-time.After(30 * time.Second):
	}
	if n := stub.calls.Load(); n != 64 {
		t.Fatalf("with 64 calls held open, the stub counted %d requests, want 64: the 65th call reached it", n)
	}
	if got.status != http.StatusServiceUnavailable || got.code != "busy" {
		t.Fatalf("the 65th call = %d %s, want 503 busy", got.status, got.body)
	}
	if returned := len(held); returned != 0 {
		t.Fatalf("%d held calls had returned when the 65th was answered busy, want none: the broker's read of the stub gives up after 10 s, so this run was too slow to say the busy answer did not wait", returned)
	}

	holding.Store(false)
	select {
	case release <- struct{}{}:
	case <-time.After(30 * time.Second):
		t.Fatal("no held call was left at the stub to release: the held reads ended first")
	}
	select {
	case returned := <-held:
		if returned.status != http.StatusOK {
			t.Fatalf("the released call = %d %s, want 200", returned.status, returned.body)
		}
	case <-time.After(time.Minute):
		t.Fatal("the released call did not return")
	}
	if served := g.privacy(t, token); served.status != http.StatusOK || !sameJSON(t, served.body, body) {
		t.Fatalf("once one held call returned, the next call = %d %s, want 200 %s", served.status, served.body, body)
	}
	if n := stub.calls.Load(); n != 65 {
		t.Fatalf("the stub counted %d requests, want 65: the 64 held and the one served after", n)
	}

	releaseAll.Do(func() { close(release) })
	for range 63 {
		if returned := <-held; returned.status != http.StatusOK {
			t.Errorf("a held call = %d %s once released, want 200", returned.status, returned.body)
		}
	}
}

// TestProjectPrivacyIsAFreshRead: 50 calls against a stub whose answer changes between calls each
// return the stub's answer at that call, and the stub counts 50 requests. Dispatch lists each
// answer's two projects out of key order, and the route answers them in key order.
func TestProjectPrivacyIsAFreshRead(t *testing.T) {
	var at atomic.Int64
	entry := func(i int64, suffix string) string {
		return fmt.Sprintf(`{"project":"P%02d-%s","slack_channels":["C%02d%s"],"slack_channels_changed_at":"2026-11-20T00:00:%02dZ"}`, i, suffix, i, suffix, i)
	}
	stub := newPrivacyStub(t, func(w http.ResponseWriter, r *http.Request) {
		i := at.Load()
		answerWith(http.StatusOK, `{"projects":[`+entry(i, "B")+`,`+entry(i, "A")+`]}`)(w, r)
	})
	g := newPrivacyServer(t, stub, nil)
	token := g.accountToken(t, privacyListenerAccount, "", time.Hour)

	for i := range int64(50) {
		at.Store(i)
		want := `{"projects":[` + entry(i, "A") + `,` + entry(i, "B") + `]}`
		got := g.privacy(t, token)
		if got.status != http.StatusOK {
			t.Fatalf("call %d = %d %s, want 200", i, got.status, got.body)
		}
		if !sameJSON(t, got.body, want) {
			t.Fatalf("call %d was answered %s, want the stub's answer at that call in key order, %s", i, got.body, want)
		}
	}
	if n := stub.calls.Load(); n != 50 {
		t.Fatalf("the stub counted %d requests for 50 calls, want 50", n)
	}
}

// TestProjectPrivacyFailsClosed: whenever the fresh read fails (Dispatch answering 500 or refusing
// the broker's token, timing out, dropping the connection, answering a project without
// slack_channels, no projects key, or no JSON at all) and on a broker with no project-access URL, a
// listener is answered 502 dispatch_unavailable, never 200 and never a list. Each failure is a read
// of the stub of its own, and the next call once Dispatch answers again is served: the route keeps
// no failure, as it keeps no answer.
func TestProjectPrivacyFailsClosed(t *testing.T) {
	healthy := answerWith(http.StatusOK, `{"projects":[]}`)
	failsClosed := func(t *testing.T, name string, got privacyAnswer) {
		t.Helper()
		if got.status != http.StatusBadGateway || got.code != "dispatch_unavailable" || strings.Contains(string(got.body), `"projects"`) {
			t.Errorf("%s: GET %s = %d %s, want 502 dispatch_unavailable", name, privacyPath, got.status, got.body)
		}
	}
	fails := func(t *testing.T, g *googleCallers, stub *privacyStub, token, name string, answer http.HandlerFunc) {
		t.Helper()
		stub.set(answer)
		before := stub.calls.Load()
		failsClosed(t, name, g.privacy(t, token))
		if stub.calls.Load() == before {
			t.Errorf("%s: the call asked Dispatch nothing", name)
		}
		stub.set(healthy)
		if got := g.privacy(t, token); got.status != http.StatusOK {
			t.Errorf("after %s, with Dispatch answering again, GET %s = %d %s, want 200", name, privacyPath, got.status, got.body)
		}
	}

	stub := newPrivacyStub(t, healthy)
	g := newPrivacyServer(t, stub, nil)
	token := g.accountToken(t, privacyListenerAccount, "", time.Hour)
	if got := g.privacy(t, token); got.status != http.StatusOK {
		t.Fatalf("with Dispatch answering, GET %s = %d %s, want 200", privacyPath, got.status, got.body)
	}
	for _, row := range []struct {
		name   string
		answer http.HandlerFunc
	}{
		{"Dispatch answers 500", answerWith(http.StatusInternalServerError, `{"code":"INTERNAL","error":"internal error"}`)},
		{"Dispatch refuses the broker's token", answerWith(http.StatusForbidden, `{"code":"ACCESS_ACCOUNT_REQUIRED","error":"refused"}`)},
		{"Dispatch drops the connection", func(w http.ResponseWriter, _ *http.Request) {
			conn, _, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Errorf("the stub could not take the connection to drop it: %v", err)
				return
			}
			conn.Close()
		}},
		{"Dispatch answers a project without slack_channels", answerWith(http.StatusOK, `{"projects":[{"project":"ALPHA","slack_channels_changed_at":null}]}`)},
		{"Dispatch answers no projects key", answerWith(http.StatusOK, `{}`)},
		{"Dispatch answers no JSON", answerWith(http.StatusOK, `projects`)},
	} {
		fails(t, g, stub, token, row.name, row.answer)
	}

	// The broker's client gives up on Dispatch after a second here rather than its own ten, on a
	// rig of its own, so that no other row can time out instead of failing as it says.
	slowStub := newPrivacyStub(t, healthy)
	slow := newPrivacyServer(t, slowStub, &http.Client{Timeout: time.Second})
	fails(t, slow, slowStub, slow.accountToken(t, privacyListenerAccount, "", time.Hour), "Dispatch times out",
		func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })

	unset := newPrivacyServerWith(t, func(d *api.Deps) {
		services, err := natscallout.ParseServices(privacyServices)
		if err != nil {
			t.Fatalf("parse BROKER_NATS_SERVICES: %v", err)
		}
		d.ServiceKinds = natscallout.ServiceKinds(services)
	})
	failsClosed(t, "a broker with no project-access URL", unset.privacy(t, unset.accountToken(t, privacyListenerAccount, "", time.Hour)))
}

// TestProjectPrivacySpendsTheAddressBucket: the route spends its caller's source address's bucket
// before the token is verified, as every route that takes an identity token does, so a listener
// past it is answered 429 RATE_LIMITED and Dispatch is not asked.
func TestProjectPrivacySpendsTheAddressBucket(t *testing.T) {
	stub := newPrivacyStub(t, answerWith(http.StatusOK, `{"projects":[]}`))
	g := newPrivacyServer(t, stub, nil, func(d *api.Deps) {
		d.AccountLimit = &ratelimit.Limit{Every: time.Hour, Burst: 1}
	})
	token := g.accountToken(t, privacyListenerAccount, "", time.Hour)
	if got := g.privacy(t, token); got.status != http.StatusOK {
		t.Fatalf("the first call = %d %s, want 200", got.status, got.body)
	}
	if got := g.privacy(t, token); got.status != http.StatusTooManyRequests || got.code != "RATE_LIMITED" {
		t.Fatalf("the second call past a bucket of one = %d %s, want 429 RATE_LIMITED", got.status, got.body)
	}
	if n := stub.calls.Load(); n != 1 {
		t.Fatalf("the stub counted %d requests, want 1: the refused call asked Dispatch", n)
	}
}
