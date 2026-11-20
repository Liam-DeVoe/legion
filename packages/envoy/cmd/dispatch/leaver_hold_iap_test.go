package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/dispatch/identity/directorytest"
)

// The leaver rig under Identity-Aware Proxy (leaver_hold_rig_test.go, withIAP; R9): Dispatch keeps
// no refresh token there, so the Workspace Directory's answer about a person's account holds and
// releases them, and the broker serves Dispatch's answer that a person is not held only until the
// deadline Dispatch puts on it (not_held_until), 24 hours after the last active answer.

// The words an IAP deployment's broker refuses a held ada in (hold.Gate.HeldWords under
// BROKER_HOLD_REQUIRES_DEADLINE), and refuses her in once Dispatch's answer that she is not held has
// passed its deadline (the 503 HOLD_UNCHECKABLE of hold.Reason's deadline words).
const (
	iapBrokerHeldWords     = "Google's Workspace Directory no longer confirms " + adaEmail + "; their credentials work again once it does"
	iapBrokerDeadlineWords = "whether Dispatch holds " + adaEmail + " cannot be read: Dispatch's answer that they are not held has passed its deadline"
)

// iapReleaseBound is how long R4 gives a released person's credentials to serve again once
// Dispatch answers the broker: the broker's 30-second cache of Dispatch's answer, then up to 5
// seconds of Dispatch's cache of the broker's introspection answer and 5 seconds of one in flight.
const iapReleaseBound = 40 * time.Second

// iapLapseBound is how long R9 gives Dispatch to answer a broker token unavailable once its
// person's deadline has passed: the same 5 seconds of cache and 5 seconds in flight.
const iapLapseBound = 10 * time.Second

// TestIAPLeaverHoldThroughTheRealBroker (c-iap-deadline; R9, R2 and R4): the leaver rig under IAP,
// the real Dispatch router and the real broker on Postgres, the broker an IAP deployment's
// (BROKER_HOLD_REQUIRES_DEADLINE). Ada and bob hold every credential of the measurement table, ada
// also a sign-in on a machine and a pending machine login, and the fake Directory answers both
// accounts active.
//
// The membership pass then finds ada's account suspended. Each use of her credentials that R2
// refuses is refused, in an IAP deployment's words: her personal token 401 MEMBERSHIP_HELD, a new
// session under her machine login or her sign-in, her session's asks and its grant reads 403
// MEMBERSHIP_HELD, a broker token mint and a trade of her sign-in 400 membership_held, her broker
// token at Dispatch and her approval of the pending login 401 MEMBERSHIP_HELD, and the broker's
// introspection answers her broker token held. Her machine login's refresh grant and her session's
// lease renewal stay served, and each of bob's credentials serves.
//
// A later pass finds her account active. Within 40 seconds every use refused before serves again,
// with no sign-in and no request from her browser, though the broker still kept its held answer
// for her; the launcher credentials, enrollments, grants, sign-ins and personal tokens are the
// rows they were before the hold, so nothing was issued again; and a new session under her machine
// login or her sign-in, and her approval of the pending login, serve.
//
// Then her last active answer and the deadline it fixed are dated 25 hours back in Postgres, and
// the Directory fails each check of her account. Her broker token at Dispatch answers 503
// BROKER_UNAVAILABLE within 10 seconds; her personal token and her approval of a new pending login
// 503 IDENTITY_UNAVAILABLE; the broker's routes 503 HOLD_UNCHECKABLE, saying her deadline has
// passed, its introspection of her token the same, and its token endpoint server_error; and each of
// bob's credentials still serves.
//
// Controls, each failing the test at the step it names: the refresh grant asking the gate as the
// device code grant does (oauth.Provider.Token), or the lease renewal asking it as the other
// session routes do (the broker's authRenewProof authentication), refuses ada's upkeep while she is
// held; the launcher routes asking no gate serve her new sessions while she is held; an Active
// outcome that releases no recorded hold (store.PgPeopleStore.RecordDirectoryActive) leaves her
// refused after the 40 seconds; the gate serving a not-held answer past its deadline
// (hold.Gate's notHeld) serves her at the broker past it; and withIAP without
// brokertest.WithHoldRequiresDeadline refuses her held sessions in the words of a broker that
// tells her to sign in.
func TestIAPLeaverHoldThroughTheRealBroker(t *testing.T) {
	r := newLeaverRig(t, withBrokerGate(), withPersonSignIns(), withIAP())
	r.directoryAccount(adaEmail, false)
	r.directoryAccount(bobEmail, false)
	ada, bob := r.person(t, adaEmail), r.person(t, bobEmail)
	r.personSignIn(t, ada)
	recordID, code := r.pendingMachineLogin(t, ada)
	before := snapshotLeaverCredentials(t, r)

	r.directoryAccount(adaEmail, true)
	r.directoryPass(t, adaEmail)
	// The broker asked Dispatch about ada as the rig gave her her credentials, and keeps each
	// not-held answer 30 seconds. It must lapse, or her rows below read the answer from before the
	// Directory found her account suspended rather than ask Dispatch, which now holds her.
	r.pastBrokerCache(t)

	token := bearerRequest(r.handler, http.MethodGet, "/api/v1/whoami", "", ada.dsp.plaintext)
	expectRefusal(t, "ada@example.com's personal token once the Directory holds her", token.Code, token.Body.Bytes(), http.StatusUnauthorized, "MEMBERSHIP_HELD", iapHeldText)
	status, body := r.enrolment(t, ada, ada.machineKey, ada.credential)
	expectRefusal(t, "ada@example.com's new enrollment under her machine login once the Directory holds her", status, body, http.StatusForbidden, "MEMBERSHIP_HELD", iapBrokerHeldWords)
	for _, row := range []struct {
		what   string
		status int
		code   string
		call   func() (int, string)
	}{
		{"ada@example.com's refresh grant while the Directory holds her", http.StatusOK, "", func() (int, string) { return r.refreshGrant(t, ada) }},
		{"ada@example.com's lease renewal while the Directory holds her", http.StatusOK, "", func() (int, string) { return r.renewLease(t, ada) }},
		{"ada@example.com's new enrollment under her sign-in while the Directory holds her", http.StatusForbidden, "MEMBERSHIP_HELD", func() (int, string) { return r.enrolUnderSignIn(t, ada) }},
		{"ada@example.com's sign-in trade while the Directory holds her", http.StatusBadRequest, oauthwire.MembershipHeld, func() (int, string) { return r.tradeSignIn(t, ada) }},
		{"ada@example.com's broker token mint while the Directory holds her", http.StatusBadRequest, oauthwire.MembershipHeld, func() (int, string) { return r.mintBrokerToken(t, ada) }},
		{"ada@example.com's existing broker token at Dispatch while the Directory holds her", http.StatusUnauthorized, "MEMBERSHIP_HELD", func() (int, string) { return r.brokerTokenAtDispatch(t, ada) }},
		{"ada@example.com's broker introspection while the Directory holds her", http.StatusOK, "held", func() (int, string) { return r.introspectionAnswer(t, ada) }},
		{"ada@example.com's agent-tier secret request while the Directory holds her", http.StatusForbidden, "MEMBERSHIP_HELD", func() (int, string) { return r.askAgentSecret(t, ada) }},
		{"ada@example.com's agent-tier grant read while the Directory holds her", http.StatusForbidden, "MEMBERSHIP_HELD", func() (int, string) { return r.readAgentGrant(t, ada) }},
		{"ada@example.com's human-tier secret request while the Directory holds her", http.StatusForbidden, "MEMBERSHIP_HELD", func() (int, string) { return r.askHumanSecret(t, ada) }},
		{"ada@example.com's human-tier grant read while the Directory holds her", http.StatusForbidden, "MEMBERSHIP_HELD", func() (int, string) { return r.readHumanGrant(t, ada) }},
		{"ada@example.com's approval of her pending machine login while the Directory holds her", http.StatusUnauthorized, "MEMBERSHIP_HELD", func() (int, string) { return r.approveOnToken(t, ada, recordID, code) }},
	} {
		expectAnswer(t, row.what, row.status, row.code).got(row.call())
	}
	if unserved := r.iapUnserved(t, bob); len(unserved) != 0 {
		t.Errorf("bob@example.com's credentials while the Directory holds ada: %s, want each served", strings.Join(unserved, "; "))
	}
	if t.Failed() {
		return
	}

	r.directoryAccount(adaEmail, false)
	released := time.Now()
	r.directoryPass(t, adaEmail)
	r.waitForIAPRelease(t, ada, released)
	expectLeaverSnapshot(t, before, snapshotLeaverCredentials(t, r))
	expectAnswer(t, "ada@example.com's new enrollment under her machine login after the release", http.StatusCreated, "").got(r.enrol(t, ada))
	expectAnswer(t, "ada@example.com's new enrollment under her sign-in after the release", http.StatusCreated, "").got(r.enrolUnderSignIn(t, ada))
	expectAnswer(t, "ada@example.com's approval of her pending machine login after the release", http.StatusOK, "").got(r.approveOnToken(t, ada, recordID, code))
	if t.Failed() {
		return
	}

	recordID, code = r.pendingMachineLogin(t, ada)
	r.directory.AnswerUser(adaEmail, directorytest.Answer{Status: http.StatusInternalServerError, Body: `{"error":{"code":500}}`})
	r.ageDirectory(t, adaEmail, 25*time.Hour)
	// The broker asked Dispatch about ada while it released her, and keeps that answer, and the
	// deadline it carries, 30 seconds: it must lapse for the broker to read the deadline aged here.
	r.pastBrokerCache(t)
	lapsed := time.Now()
	r.waitForLapsedBrokerToken(t, ada, lapsed)
	token = bearerRequest(r.handler, http.MethodGet, "/api/v1/whoami", "", ada.dsp.plaintext)
	expectRefusal(t, "ada@example.com's personal token past her deadline", token.Code, token.Body.Bytes(), http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", iapUnavailableText)
	status, body = r.enrolment(t, ada, ada.machineKey, ada.credential)
	expectRefusal(t, "ada@example.com's new enrollment under her machine login past her deadline", status, body, http.StatusServiceUnavailable, "HOLD_UNCHECKABLE", iapBrokerDeadlineWords)
	for _, row := range []struct {
		what   string
		status int
		code   string
		call   func() (int, string)
	}{
		{"ada@example.com's new enrollment under her sign-in past her deadline", http.StatusServiceUnavailable, "HOLD_UNCHECKABLE", func() (int, string) { return r.enrolUnderSignIn(t, ada) }},
		{"ada@example.com's sign-in trade past her deadline", http.StatusInternalServerError, "server_error", func() (int, string) { return r.tradeSignIn(t, ada) }},
		{"ada@example.com's broker token mint past her deadline", http.StatusInternalServerError, "server_error", func() (int, string) { return r.mintBrokerToken(t, ada) }},
		{"ada@example.com's broker introspection past her deadline", http.StatusServiceUnavailable, "HOLD_UNCHECKABLE", func() (int, string) { return r.introspectionAnswer(t, ada) }},
		{"ada@example.com's agent-tier secret request past her deadline", http.StatusServiceUnavailable, "HOLD_UNCHECKABLE", func() (int, string) { return r.askAgentSecret(t, ada) }},
		{"ada@example.com's agent-tier grant read past her deadline", http.StatusServiceUnavailable, "HOLD_UNCHECKABLE", func() (int, string) { return r.readAgentGrant(t, ada) }},
		{"ada@example.com's human-tier secret request past her deadline", http.StatusServiceUnavailable, "HOLD_UNCHECKABLE", func() (int, string) { return r.askHumanSecret(t, ada) }},
		{"ada@example.com's human-tier grant read past her deadline", http.StatusServiceUnavailable, "HOLD_UNCHECKABLE", func() (int, string) { return r.readHumanGrant(t, ada) }},
		{"ada@example.com's approval of a new pending machine login past her deadline", http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", func() (int, string) { return r.approveOnToken(t, ada, recordID, code) }},
		{"bob@example.com's new enrollment past ada's deadline", http.StatusCreated, "", func() (int, string) { return r.enrol(t, bob) }},
	} {
		expectAnswer(t, row.what, row.status, row.code).got(row.call())
	}
	if unserved := r.iapUnserved(t, bob); len(unserved) != 0 {
		t.Errorf("bob@example.com's credentials past ada's deadline: %s, want each served", strings.Join(unserved, "; "))
	}
}

// directoryAccount makes the fake Directory answer email's own Workspace account, suspended as
// suspended says: users.get 200 with the account's id, "directory-<email>", and email as its
// primary address.
func (r *leaverRig) directoryAccount(email string, suspended bool) {
	r.directory.SetUser(email, directorytest.User{ID: "directory-" + email, PrimaryEmail: email, Suspended: suspended})
}

// directoryPass makes email's row due a Directory check (ageDirectory, two hours back) and runs
// one membership pass over the router's Membership, as the maintenance sweep runs it: its check
// of email's account records the outcome the fake Directory answers.
func (r *leaverRig) directoryPass(t *testing.T, email string) {
	t.Helper()
	r.ageDirectory(t, email, 2*time.Hour)
	if err := r.membershipPass(nil).Run(context.Background()); err != nil {
		t.Fatalf("the membership pass: %v", err)
	}
}

// ageDirectory dates email's Directory record d back by the database's clock: its last active
// answer (directory_confirmed_at), the deadline that answer fixed (not_held_until), its last check
// (directory_checked_at) and a recorded hold (held_since), so their order stands. It fails t when
// no row of email's holds an active answer to date.
func (r *leaverRig) ageDirectory(t *testing.T, email string, d time.Duration) {
	t.Helper()
	aged, err := r.outside.database.Pool.Exec(context.Background(), `
		update people set directory_confirmed_at = directory_confirmed_at - make_interval(secs => $2),
			not_held_until = not_held_until - make_interval(secs => $2),
			directory_checked_at = directory_checked_at - make_interval(secs => $2),
			held_since = held_since - make_interval(secs => $2)
		where email = $1 and directory_confirmed_at is not null
	`, email, d.Seconds())
	if err != nil {
		t.Fatalf("date %s's Directory record %s back: %v", email, d, err)
	}
	if aged.RowsAffected() != 1 {
		t.Fatalf("dating %s's Directory record %s back changed %d rows, want 1: the Directory never answered their account active", email, d, aged.RowsAffected())
	}
}

// tradeSignIn trades p's sign-in on the fixture's metadata machine again (signInTrade), as the
// machine's agent-secrets renews it, answering as a credential call answers.
func (r *leaverRig) tradeSignIn(t *testing.T, p *leaverPerson) (int, string) {
	t.Helper()
	status, refusal, _ := r.signInTrade(t, p)
	return status, refusal
}

// introspectionAnswer is the broker's introspection of p's broker token (introspection) as a
// credential call answers it, with the code "held" for a 200 that answers the token's person held.
func (r *leaverRig) introspectionAnswer(t *testing.T, p *leaverPerson) (int, string) {
	t.Helper()
	status, held, code := r.introspection(t, p)
	if held {
		return status, "held"
	}
	return status, code
}

// approveOnToken approves the pending machine login recordID, whose confirmation code is code,
// on Dispatch's decision route with p's personal token, as an agent of p's approves one.
func (r *leaverRig) approveOnToken(t *testing.T, p *leaverPerson, recordID, code string) (int, string) {
	t.Helper()
	return dispatchAnswer(bearerRequest(r.handler, http.MethodPost, "/api/v1/credential-requests/"+recordID+"/approve",
		leaverBody(t, map[string]string{"code": code}), p.dsp.plaintext))
}

// iapUnserved is each of p's credential uses that a hold refuses, and the upkeep it serves, that
// does not serve now, by name and answer: none when each serves, the broker's introspection
// answering p's broker token not held. It sends nothing from p's browser, so a release it sees
// came from no IAP assertion of p's, and it adds no row the release snapshot reads, as a new
// session and an approval would. A trade of p's sign-in is one of them only for a person the rig
// gave a sign-in (personSignIn).
func (r *leaverRig) iapUnserved(t *testing.T, p *leaverPerson) []string {
	t.Helper()
	type use struct {
		name string
		call func() (int, string)
	}
	uses := []use{
		{"personal token", func() (int, string) { return r.personalToken(t, p.dsp) }},
		{"refresh grant", func() (int, string) { return r.refreshGrant(t, p) }},
		{"lease renewal", func() (int, string) { return r.renewLease(t, p) }},
		{"broker token mint", func() (int, string) { return r.mintBrokerToken(t, p) }},
		{"broker token at Dispatch", func() (int, string) { return r.brokerTokenAtDispatch(t, p) }},
		{"broker introspection", func() (int, string) { return r.introspectionAnswer(t, p) }},
		{"agent-tier secret request", func() (int, string) { return r.askAgentSecret(t, p) }},
		{"agent-tier grant read", func() (int, string) { return r.readAgentGrant(t, p) }},
		{"human-tier secret request", func() (int, string) { return r.askHumanSecret(t, p) }},
		{"human-tier grant read", func() (int, string) { return r.readHumanGrant(t, p) }},
	}
	if p.signInCredential != "" {
		uses = append(uses, use{"sign-in trade", func() (int, string) { return r.tradeSignIn(t, p) }})
	}
	var unserved []string
	for _, use := range uses {
		if status, code := use.call(); status != http.StatusOK || code != "" {
			unserved = append(unserved, fmt.Sprintf("%s answered %d %q", use.name, status, code))
		}
	}
	return unserved
}

// waitForIAPRelease requires each use iapUnserved reads of p's to serve within iapReleaseBound of
// since, the moment before the pass whose Active outcome released p, polling each second by the
// wall clock: the broker's 30-second cache of its held answer for p lapses on its own, inside the
// bound, as it does in a deployment.
func (r *leaverRig) waitForIAPRelease(t *testing.T, p *leaverPerson, since time.Time) {
	t.Helper()
	for {
		unserved := r.iapUnserved(t, p)
		elapsed := time.Since(since)
		switch {
		case len(unserved) == 0 && elapsed <= iapReleaseBound:
			t.Logf("%s's credentials served again %s after the pass that found their account active", p.email, elapsed.Round(100*time.Millisecond))
			return
		case len(unserved) == 0:
			t.Errorf("%s's credentials served again only %s after the pass that found their account active, want within %s", p.email, elapsed.Round(100*time.Millisecond), iapReleaseBound)
			return
		case elapsed > iapReleaseBound:
			t.Errorf("%s's credentials %s after the pass that found their account active: %s; want each served within %s", p.email, elapsed.Round(100*time.Millisecond), strings.Join(unserved, "; "), iapReleaseBound)
			return
		}
		time.Sleep(time.Second)
	}
}

// waitForLapsedBrokerToken requires p's broker token at Dispatch to answer 503 BROKER_UNAVAILABLE
// within iapLapseBound of since, the moment p's deadline was passed, polling every quarter second
// by the wall clock: until then it serves, from Dispatch's cache of the broker's last
// introspection answer, and it answers nothing else.
func (r *leaverRig) waitForLapsedBrokerToken(t *testing.T, p *leaverPerson, since time.Time) {
	t.Helper()
	for {
		status, code := r.brokerTokenAtDispatch(t, p)
		elapsed := time.Since(since)
		switch {
		case status == http.StatusServiceUnavailable && code == "BROKER_UNAVAILABLE" && elapsed <= iapLapseBound:
			t.Logf("%s's broker token at Dispatch answered 503 BROKER_UNAVAILABLE %s past their deadline", p.email, elapsed.Round(100*time.Millisecond))
			return
		case status == http.StatusServiceUnavailable && code == "BROKER_UNAVAILABLE", elapsed > iapLapseBound:
			t.Errorf("%s's broker token at Dispatch answered %d %q %s past their deadline, want 503 BROKER_UNAVAILABLE within %s", p.email, status, code, elapsed.Round(100*time.Millisecond), iapLapseBound)
			return
		case status != http.StatusOK || code != "":
			t.Errorf("%s's broker token at Dispatch answered %d %q %s past their deadline, want 200 until Dispatch's cache of the introspection lapses, then 503 BROKER_UNAVAILABLE", p.email, status, code, elapsed.Round(100*time.Millisecond))
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// expectRefusal fails t unless a JSON answer of Dispatch or the broker, its status and body, is
// wantStatus with code in words, each of which writes {"code", "error"}: what was asked, named as
// expectAnswer names it. The one wanted is logged, so a run shows each row it read.
func expectRefusal(t *testing.T, what string, status int, body []byte, wantStatus int, code, words string) {
	t.Helper()
	var refusal struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &refusal)
	if status != wantStatus || refusal.Code != code || refusal.Error != words {
		t.Errorf("%s answered %d %s, want %d %s %q", what, status, strings.TrimSpace(string(body)), wantStatus, code, words)
		return
	}
	t.Logf("%s answered %d %q in the words wanted", what, status, code)
}
