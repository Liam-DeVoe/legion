package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/brokertoken/brokertokentest"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// A machine login's approve mints a credential at the secrets broker in the approver's name, so
// Dispatch relays it only while its caller still holds the address, read again once the request's
// body has arrived, and names the Google account that approves, so the broker can end the login
// with that account. A deny is held to the same re-read, and names no account.

// approvePath is the approve the tests relay: a machine login's, rec1.
const approvePath = "/api/v1/credential-requests/rec1/approve"

// denyPath is the deny the tests relay: the same machine login's.
const denyPath = "/api/v1/credential-requests/rec1/deny"

// approveBody is the body of an approve or a deny, the code the person typed.
const approveBody = `{"code":"ABCD-1234"}`

// approveCredentials are the three credentials a person approves with: their cookie, a personal
// token they minted on it, and a broker token of a machine they approved, each answered as the
// caller's next request is once the caller no longer holds the address.
var approveCredentials = []struct{ name, refusal string }{
	{"cookie", "NO_IDENTITY"},
	{"personal token", "TOKEN_REVOKED"},
	{"broker token", "BROKER_TOKEN_NOT_ACTIVE"},
}

// approver is the first holder of reassignedEmail signed in through handler, holding credential:
// it answers a request of the decision at path (approvePath or denyPath) carrying body, which the
// caller sends as that credential. Each of beforeCredential runs after the sign-in and before the
// credential is made. A broker token's machine login records the account the address's row
// records when it is made, the one Dispatch's approve of that login named, none for none, and is
// made now by the broker's clock.
func (s *outsideSignIn) approver(t *testing.T, handler http.Handler, broker *fakeBroker, credential string, beforeCredential ...func()) func(path string, body io.Reader) *http.Request {
	t.Helper()
	cookie := s.workspaceSignIn(t, handler, reassignedEmail, firstHolder)
	for _, step := range beforeCredential {
		step()
	}
	var token string
	var machine *brokertokentest.Caller
	switch credential {
	case "personal token":
		token = mintTokenOn(t, handler, cookie)
	case "broker token":
		var approvedBy string
		if err := s.database.Pool.QueryRow(context.Background(), `select coalesce(sub, '') from people where email = $1`, reassignedEmail).Scan(&approvedBy); err != nil {
			t.Fatal(err)
		}
		machine, token = brokertokentest.NewCaller(t), "token-of-the-first-holders-machine"
		broker.holds(&fakeCredential{id: "login-first", person: reassignedEmail, subject: approvedBy, tokens: []string{token}, createdAt: time.Now()}, machine)
	}
	return func(path string, body io.Reader) *http.Request {
		request := httptest.NewRequest(http.MethodPost, brokerOrigin+path, body)
		request.ContentLength = int64(len(approveBody))
		request.Header.Set("Content-Type", "application/json")
		switch credential {
		case "cookie":
			request.Header.Set("Origin", brokerOrigin)
			for _, c := range cookie.Result().Cookies() {
				if c.Name == "dsession" && c.Value != "" {
					request.AddCookie(c)
				}
			}
		case "personal token":
			request.Header.Set("Authorization", "Bearer "+token)
		case "broker token":
			signed := machine.Signed(t, http.MethodPost, brokerOrigin+path, token)
			request.Header.Set("Authorization", signed.Header.Get("Authorization"))
			request.Header.Set("DPoP", signed.Header.Get("DPoP"))
		}
		return request
	}
}

// heldBody is a request body whose first read waits, after saying it began, until the test lets
// it go: a client that sent its headers and holds its body back.
type heldBody struct {
	body             io.Reader
	reading, release chan struct{}
	once             sync.Once
}

func newHeldBody(body string) *heldBody {
	return &heldBody{body: strings.NewReader(body), reading: make(chan struct{}), release: make(chan struct{})}
}

func (b *heldBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.reading)
		<-b.release
	})
	return b.body.Read(p)
}

// serveHeld serves request through handler while the client holds body back: once Dispatch waits on
// the body it runs whileHeld, then lets the body go and answers the response.
func serveHeld(t *testing.T, handler http.Handler, request *http.Request, body *heldBody, whileHeld func()) *httptest.ResponseRecorder {
	t.Helper()
	recorder, answered := httptest.NewRecorder(), make(chan struct{})
	go func() {
		defer close(answered)
		handler.ServeHTTP(recorder, request)
	}()
	var release sync.Once
	defer release.Do(func() { close(body.release) })
	select {
	case <-body.reading:
	case <-answered:
		t.Fatalf("%s %s answered %d %s before reading its body", request.Method, request.URL.Path, recorder.Code, recorder.Body.String())
	case <-time.After(30 * time.Second):
		t.Fatalf("%s %s did not begin reading its body within 30 s", request.Method, request.URL.Path)
	}
	whileHeld()
	release.Do(func() { close(body.release) })
	<-answered
	return recorder
}

// An approve whose caller resolved before its body arrived, and lost the address while the client
// held the body back, never reaches the broker: the first holder sends an approve's headers by
// each credential, unkeyed and under an Idempotency-Key (whose layer resolves the caller before it
// spools the body), and while Dispatch waits on the body Google gives the address to another
// Workspace account, which signs in. Once the body arrives the approve is refused 401 as the
// caller's next request would be, and the broker is told nothing.
func TestOutsideSignInApproveRefusesACallerWhoLostTheAddressWhileItsBodyArrived(t *testing.T) {
	for _, credential := range approveCredentials {
		for _, keyed := range []bool{false, true} {
			t.Run(credential.name+" keyed "+strconv.FormatBool(keyed), func(t *testing.T) {
				s := newOutsideSignIn(t, nil)
				broker := newFakeBroker(t)
				handler, _ := s.brokerRouter(t, broker)
				body := newHeldBody(approveBody)
				request := s.approver(t, handler, broker, credential.name)(approvePath, body)
				if keyed {
					request.Header.Set("Idempotency-Key", "approve-while-the-address-passes-on")
				}
				recorder := serveHeld(t, handler, request, body, func() {
					s.workspaceSignIn(t, handler, reassignedEmail, secondHolder)
				})

				if recorder.Code != http.StatusUnauthorized || errorCode(recorder) != credential.refusal {
					t.Errorf("the approve by the first holder's %s, whose body arrived after the address passed on, answered %d %s, want 401 %s",
						credential.name, recorder.Code, recorder.Body.String(), credential.refusal)
				}
				for _, call := range broker.calls() {
					if strings.Contains(call, "/approve") {
						t.Errorf("the approve by the first holder's %s, whose body arrived after the address passed on, was relayed to the broker: %s", credential.name, call)
					}
				}
			})
		}
	}
}

// A deny is read again as an approve is: the broker decides a machine login's denial whatever
// account asks, so a deny relayed for a caller who lost the address would let the replaced account
// deny the new holder's pending machine login. The first holder sends a deny's headers by each
// credential, unkeyed and under an Idempotency-Key, and while Dispatch waits on the body Google
// gives the address to another Workspace account, which signs in. Once the body arrives the deny is
// refused 401 as the caller's next request would be, and the broker is told no deny.
func TestOutsideSignInDenyRefusesACallerWhoLostTheAddressWhileItsBodyArrived(t *testing.T) {
	for _, credential := range approveCredentials {
		for _, keyed := range []bool{false, true} {
			t.Run(credential.name+" keyed "+strconv.FormatBool(keyed), func(t *testing.T) {
				s := newOutsideSignIn(t, nil)
				broker := newFakeBroker(t)
				handler, _ := s.brokerRouter(t, broker)
				body := newHeldBody(approveBody)
				request := s.approver(t, handler, broker, credential.name)(denyPath, body)
				if keyed {
					request.Header.Set("Idempotency-Key", "deny-while-the-address-passes-on")
				}
				recorder := serveHeld(t, handler, request, body, func() {
					s.workspaceSignIn(t, handler, reassignedEmail, secondHolder)
				})

				if recorder.Code != http.StatusUnauthorized || errorCode(recorder) != credential.refusal {
					t.Errorf("the deny by the first holder's %s, whose body arrived after the address passed on, answered %d %s, want 401 %s",
						credential.name, recorder.Code, recorder.Body.String(), credential.refusal)
				}
				for _, call := range broker.calls() {
					if strings.Contains(call, "/deny") {
						t.Errorf("the deny by the first holder's %s, whose body arrived after the address passed on, was relayed to the broker: %s", credential.name, call)
					}
				}
			})
		}
	}
}

// A deny by a caller who still holds the address is relayed, whichever credential it acts with,
// and names the approver and the code alone: a denial mints nothing, so it names no Google account
// (no approver_issuer, no approver_subject), though the re-read that let it through read one.
func TestOutsideSignInDenyByTheHolderNamesNoAccount(t *testing.T) {
	for _, credential := range approveCredentials {
		t.Run(credential.name, func(t *testing.T) {
			s := newOutsideSignIn(t, nil)
			broker := newFakeBroker(t)
			handler, _ := s.brokerRouter(t, broker)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, s.approver(t, handler, broker, credential.name)(denyPath, strings.NewReader(approveBody)))
			if recorder.Code != http.StatusOK {
				t.Fatalf("the deny by the holder's %s answered %d %s, want 200", credential.name, recorder.Code, recorder.Body.String())
			}
			requireCalls(t, broker, "after the deny by the holder's "+credential.name,
				`POST /v1/credential-requests/rec1/deny {"approver":"`+reassignedEmail+`","code":"ABCD-1234"}`)
		})
	}
}

// An approve held back past the whole revocation is still refused, whichever credential it was sent
// with: the first holder sends an approve's headers by each credential, and while Dispatch waits on
// the body Google gives the address to another Workspace account, which signs in; the revocation
// pass tells the broker to revoke the first holder's account and settles, and an hour passes. An
// approve relayed then would mint a machine login in the address's name for a caller that lost it.
// Each approve is refused 401 as the caller's next request would be - BROKER_TOKEN_NOT_ACTIVE for
// the broker token, which records the first holder's account, which a revocation names - and the
// broker is told no approve.
func TestOutsideSignInAnApproveHeldPastTheRevocationIsNotRelayed(t *testing.T) {
	for _, credential := range approveCredentials {
		t.Run(credential.name, func(t *testing.T) {
			ctx := context.Background()
			s := newOutsideSignIn(t, nil)
			broker := newFakeBroker(t)
			handler, background := s.brokerRouter(t, broker)
			body := newHeldBody(approveBody)
			request := s.approver(t, handler, broker, credential.name)(approvePath, body)
			recorder := serveHeld(t, handler, request, body, func() {
				s.workspaceSignIn(t, handler, reassignedEmail, secondHolder)
				background.RevokeBrokerCredentials(ctx)
				if recorded, done := s.brokerRevocation(t, reassignedEmail); !recorded || !done {
					t.Fatalf("the revocation of %s after the pass reads recorded %t, done %t; want it settled", reassignedEmail, recorded, done)
				}
				s.settledAgo(t, reassignedEmail, time.Hour)
			})

			if recorder.Code != http.StatusUnauthorized || errorCode(recorder) != credential.refusal {
				t.Errorf("the first holder's %s approve, whose body arrived an hour after the revocation settled, answered %d %s, want 401 %s",
					credential.name, recorder.Code, recorder.Body.String(), credential.refusal)
			}
			for _, call := range broker.calls() {
				if strings.Contains(call, "/approve") {
					t.Errorf("the first holder's %s approve, whose body arrived an hour after the revocation settled, was relayed to the broker: %s", credential.name, call)
				}
			}
		})
	}
}

// An approve held back by a broker token recording no account is refused once another account's
// sign-in records itself for the address while the body is held: the token's machine login was
// made after the first holder's account was recorded and past brokerCredentialMargin, so it acts
// and its first approve is relayed; the second holder's sign-in records the address's account
// again, after the login was made, so the held approve is refused 401
// BROKER_TOKEN_RECORDS_NO_ACCOUNT, as the token's next request is, and the broker is told no
// second approve.
//
// Control: brokerAccountReplaced without its time arm; the held approve is relayed.
func TestOutsideSignInAnApproveHeldByATokenRecordingNoAccountIsRefusedOnceAnotherAccountSignsIn(t *testing.T) {
	ctx := context.Background()
	s := newOutsideSignIn(t, nil)
	broker := newFakeBroker(t)
	handler, _ := s.brokerRouter(t, broker)
	recordsNone := func() { storetest.TestOnlyClearAccount(t, s.database, reassignedEmail) }
	approver := s.approver(t, handler, broker, "broker token", recordsNone)
	if _, err := s.database.Pool.Exec(ctx, `update people set sub = $2 where email = $1`, reassignedEmail, firstHolder); err != nil {
		t.Fatal(err)
	}
	var twentyMinutesAgo time.Time
	if err := s.database.Pool.QueryRow(ctx, `select now() - interval '20 minutes'`).Scan(&twentyMinutesAgo); err != nil {
		t.Fatal(err)
	}
	storetest.SetRecordingTime(t, s.database, reassignedEmail, twentyMinutesAgo)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, approver(approvePath, strings.NewReader(approveBody)))
	if first.Code != http.StatusOK {
		t.Fatalf("the approve by a broker token recording no account, its login made 20 minutes after the address's account was recorded, answered %d %s, want 200",
			first.Code, first.Body.String())
	}
	body := newHeldBody(approveBody)
	recorder := serveHeld(t, handler, approver(approvePath, body), body, func() {
		s.workspaceSignIn(t, handler, reassignedEmail, secondHolder)
	})
	if recorder.Code != http.StatusUnauthorized || errorCode(recorder) != "BROKER_TOKEN_RECORDS_NO_ACCOUNT" {
		t.Errorf("the approve held by a broker token recording no account while the second holder signed in answered %d %s, want 401 BROKER_TOKEN_RECORDS_NO_ACCOUNT",
			recorder.Code, recorder.Body.String())
	}
	var approves int
	for _, call := range broker.calls() {
		if strings.Contains(call, "/approve") {
			approves++
		}
	}
	if approves != 1 {
		t.Errorf("the broker was told %d approves, want only the first: the held one is refused", approves)
	}
}

// A personal token records the Google account of the credential that minted it, and acts only while
// Dispatch records that account for its address. The first holder mints a token on its cookie, a
// second token with that token, and a third with its machine's broker token: each records the first
// holder's account. Then the address's row comes to record no account (as a pool or header sign-in
// leaves it), and the second holder signs in, which records the address's first account in a while:
// that sign-in revokes the address's tokens that recorded another account, all three of the first
// holder's (store.PgPeopleStore.SignInSubject). The first holder's token is refused 401
// TOKEN_REVOKED at its approve, and the broker is told no approve; a token the second holder mints
// records the second's account.
func TestOutsideSignInAnApproveNamesTheAccountOfTheCredentialThatMadeIt(t *testing.T) {
	ctx := context.Background()
	s := newOutsideSignIn(t, nil)
	broker := newFakeBroker(t)
	handler, _ := s.brokerRouter(t, broker)
	cookie := s.workspaceSignIn(t, handler, reassignedEmail, firstHolder)
	token := mintTokenOn(t, handler, cookie)
	if minted := bearerRequest(handler, http.MethodPost, "/api/v1/me/agent-tokens", `{"name":"minted by a token"}`, token); minted.Code != http.StatusCreated {
		t.Fatalf("a mint by the first holder's token answered %d %s, want 201", minted.Code, minted.Body.String())
	}
	machine := brokertokentest.NewCaller(t)
	broker.holds(&fakeCredential{id: "login-first", person: reassignedEmail, subject: firstHolder, tokens: []string{"token-of-the-first-holders-machine"}}, machine)
	if minted := brokerTokenRequest(t, handler, machine, http.MethodPost, "/api/v1/me/agent-tokens", `{"name":"minted by a broker token"}`, "token-of-the-first-holders-machine"); minted.Code != http.StatusCreated {
		t.Fatalf("a mint by the first holder's broker token answered %d %s, want 201", minted.Code, minted.Body.String())
	}
	subjects := func() []string {
		t.Helper()
		rows, err := s.database.Pool.Query(ctx, `select coalesce(subject, '') from agent_tokens order by created_at, id`)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var subject string
			if err := rows.Scan(&subject); err != nil {
				t.Fatal(err)
			}
			got = append(got, subject)
		}
		return got
	}
	if got := subjects(); strings.Join(got, ",") != firstHolder+","+firstHolder+","+firstHolder {
		t.Fatalf("the tokens minted by the first holder's cookie, token and broker token recorded the accounts %q, want the first holder's on each", got)
	}

	storetest.TestOnlyClearAccount(t, s.database, reassignedEmail)
	second := s.workspaceSignIn(t, handler, reassignedEmail, secondHolder)
	var live int
	if err := s.database.Pool.QueryRow(ctx, `select count(*) from agent_tokens where subject = $1 and revoked_at is null`, firstHolder).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("%d tokens recording the first holder's account are live after the second holder's sign-in recorded itself for the address, want all three revoked", live)
	}
	approve := httptest.NewRequest(http.MethodPost, brokerOrigin+approvePath, strings.NewReader(approveBody))
	approve.Header.Set("Content-Type", "application/json")
	approve.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, approve)
	if recorder.Code != http.StatusUnauthorized || errorCode(recorder) != "TOKEN_REVOKED" {
		t.Fatalf("the approve by the first holder's token, once the second holder's sign-in recorded itself, answered %d %s, want 401 TOKEN_REVOKED", recorder.Code, recorder.Body.String())
	}
	for _, call := range broker.calls() {
		if strings.Contains(call, "/approve") {
			t.Fatalf("the refused approve by the first holder's token was relayed to the broker: %s", call)
		}
	}
	mintTokenOn(t, handler, second)
	if got := subjects(); len(got) != 4 || got[3] != secondHolder {
		t.Fatalf("the token the second holder minted recorded %q, want the second holder's account", got)
	}
}

// An approve names the Google account that approves, so the broker records it on the machine login
// it mints: Dispatch's sign-in issuer and the `sub` people records for the approver, beside the
// approver, the code and the session generation its check read, whichever credential the approver
// acts with.
func TestOutsideSignInApproveNamesTheApprovingAccount(t *testing.T) {
	for _, credential := range approveCredentials {
		t.Run(credential.name, func(t *testing.T) {
			s := newOutsideSignIn(t, nil)
			broker := newFakeBroker(t)
			handler, _ := s.brokerRouter(t, broker)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, s.approver(t, handler, broker, credential.name)(approvePath, strings.NewReader(approveBody)))
			if recorder.Code != http.StatusOK {
				t.Fatalf("the approve by the holder's %s answered %d %s, want 200", credential.name, recorder.Code, recorder.Body.String())
			}
			requireCalls(t, broker, "after the approve by the holder's "+credential.name, s.approveRelayed(t, firstHolder))
		})
	}
}

// An approver whose credential is bound to no Google account (made while the address's row recorded
// none, as a row from before Dispatch recorded one is) sends neither approver_issuer nor
// approver_subject: the broker takes both or neither and refuses one alone 400
// APPROVER_ACCOUNT_INPUT, so Dispatch never sends the issuer by itself.
func TestOutsideSignInApproveOfARowWithNoAccountSendsNeitherAccountField(t *testing.T) {
	for _, credential := range approveCredentials {
		t.Run(credential.name, func(t *testing.T) {
			s := newOutsideSignIn(t, nil)
			broker := newFakeBroker(t)
			handler, _ := s.brokerRouter(t, broker)
			request := s.approver(t, handler, broker, credential.name, func() {
				storetest.TestOnlyClearAccount(t, s.database, reassignedEmail)
			})
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request(approvePath, strings.NewReader(approveBody)))
			if recorder.Code != http.StatusOK {
				t.Fatalf("the approve by the %s of a row with no account answered %d %s, want 200", credential.name, recorder.Code, recorder.Body.String())
			}
			requireCalls(t, broker, "after the approve by the "+credential.name+" of a row with no account", s.approveRelayed(t, ""))
		})
	}
}
