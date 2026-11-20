// directory_standing_test.go holds the API to IAP identity's Directory standing (R9, K11): a
// personal token whose owner Dispatch confirms through the Workspace Directory asks Standing at
// every use, and the person route carries the deadline of a not-held answer.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/identity"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestAnIAPPersonalTokenAsksStandingFirst: under IAP identity (Standing.ChecksDirectory) a personal
// token asks Standing at every use, before the fast paths over the refresh token and the sign-in
// confirmation its own statement reads, since IAP keeps neither. Dave's row is an IAP member's, with
// no refresh token: a held answer is 401 MEMBERSHIP_HELD in the Directory's words, an unavailable
// one 503 IDENTITY_UNAVAILABLE, a not-held one with a deadline after its read 200, and a Standing
// that fails 503; a hold recorded on his row is still answered through Standing. Each use asks
// Standing about dave once. In cookie identity carol's held answer keeps the sign-in's words.
//
// Control: ChecksDirectory answering false (personalTokenHeld ignoring it): dave's token takes the
// fast path for an owner with no refresh token, asks Standing nothing and is served, and the test
// fails naming the row.
func TestAnIAPPersonalTokenAsksStandingFirst(t *testing.T) {
	_, database, deps := newTestServer(t, testServerOptions{})
	seedPeople(t, database, accessDave)
	daveToken := personalTokenOwnedBy(t, database, accessDave, model.ScopePerson)
	signInFor(t, database, accessCarol, "carol-account", "carol-refresh", model.MemberFull, 2*time.Hour)
	carolToken := personalTokenOwnedBy(t, database, accessCarol, model.ScopePerson)
	directory := &standingFake{directory: true}
	deps.Standing = directory
	iap := servedWith(t, deps)
	cookie := &standingFake{}
	cookieDeps := deps
	cookieDeps.Standing = cookie
	signedIn := servedWith(t, cookieDeps)

	at := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	const unavailableText = "whether this token's owner may still act could not be read; try again in a minute"
	rows := []struct {
		name     string
		before   func()
		standing identity.Standing
		err      error
		status   int
		code     string
		text     string
	}{
		{name: "held by the Directory", standing: identity.Standing{Held: true, AsOf: at},
			status: http.StatusUnauthorized, code: codeMembershipHeld, text: membershipHeldDirectoryText},
		{name: "unavailable", standing: identity.Standing{AsOf: at, NotHeldUntil: at},
			status: http.StatusServiceUnavailable, code: "IDENTITY_UNAVAILABLE", text: unavailableText},
		{name: "not held before its deadline", standing: identity.Standing{AsOf: at, NotHeldUntil: at.Add(time.Second)},
			status: http.StatusOK},
		{name: "a Standing that fails", err: identity.ErrMembershipUnreadable,
			status: http.StatusServiceUnavailable, code: "IDENTITY_UNAVAILABLE", text: unavailableText},
		{name: "a hold recorded on the row", standing: identity.Standing{Held: true, AsOf: at},
			before: func() {
				if err := peopleOf(database).RecordDirectoryHeld(context.Background(), accessDave); err != nil {
					t.Fatal(err)
				}
			},
			status: http.StatusUnauthorized, code: codeMembershipHeld, text: membershipHeldDirectoryText},
	}
	for _, row := range rows {
		if row.before != nil {
			row.before()
		}
		directory.answers(row.standing, row.err)
		before := len(directory.askedSoFar())
		response := agentRequest(t, iap, http.MethodGet, "/api/v1/whoami", nil, daveToken)
		var refusal refusalAnswer
		_ = json.Unmarshal(response.Body.Bytes(), &refusal)
		if response.Code != row.status || refusal.Code != row.code || refusal.Error != row.text {
			t.Errorf("dave's token under IAP, Standing %s = %d %s, want %d %s %q", row.name, response.Code, strings.TrimSpace(response.Body.String()), row.status, row.code, row.text)
		}
		if asked := directory.askedSoFar()[before:]; len(asked) != 1 || asked[0] != accessDave {
			t.Errorf("dave's token under IAP, Standing %s, asked Standing about %v; want %s once: IAP keeps no refresh token, so only Standing answers", row.name, asked, accessDave)
		}
	}

	cookie.answers(identity.Standing{Held: true, AsOf: at}, nil)
	wantHeldRefusal(t, agentRequest(t, signedIn, http.MethodGet, "/api/v1/whoami", nil, carolToken), membershipHeldText,
		"carol's token in cookie identity, her confirmation two hours old and Standing answering held")
}

// TestThePersonAnswerCarriesNotHeldUntil (K11): the person route carries Standing's NotHeldUntil as
// not_held_until on a not-held answer under IAP identity; an unavailable answer is held false with
// not_held_until equal to as_of; and a held answer, and every answer outside IAP, leave it out.
func TestThePersonAnswerCarriesNotHeldUntil(t *testing.T) {
	issuer := newServiceTokenIssuer(t)
	_, database, deps := newTestServer(t, testServerOptions{oidc: issuer.verifier(t), accessAccounts: []string{listedServiceAccount}})
	bearer := issuer.Mint(t, issuer.signing, issuer.Claims(listedServiceAccount, serviceTokenAudience))
	seedPeople(t, database, accessDave)
	at := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	deadline := at.Add(23 * time.Hour)
	for _, row := range []struct {
		name     string
		standing identity.Standing
		held     bool
		until    *time.Time
	}{
		{"not held under IAP", identity.Standing{AsOf: at, NotHeldUntil: deadline}, false, &deadline},
		{"unavailable under IAP", identity.Standing{AsOf: at, NotHeldUntil: at}, false, &at},
		{"held", identity.Standing{Held: true, AsOf: at}, true, nil},
		{"not held outside IAP", identity.Standing{AsOf: at}, false, nil},
	} {
		fake := &standingFake{directory: row.until != nil}
		fake.answers(row.standing, nil)
		served := deps
		served.Standing = fake
		code, fields, answer := personAnswerOf(t, servedWith(t, served), bearer, accessDave)
		_, carries := fields["not_held_until"]
		untilOK := (row.until == nil && !carries) || (row.until != nil && carries && answer.NotHeldUntil != nil && answer.NotHeldUntil.Equal(*row.until))
		if code != http.StatusOK || answer.Held == nil || *answer.Held != row.held || !answer.AsOf.Equal(at) || !untilOK {
			t.Errorf("%s: the person answer of %s = %d %v; want held %t, as_of %s and not_held_until %s",
				row.name, accessDave, code, fields, row.held, at.Format(time.RFC3339), describeUntil(row.until))
		}
	}
	failing := &standingFake{directory: true}
	failing.answers(identity.Standing{}, errors.New("read membership: the database did not answer"))
	served := deps
	served.Standing = failing
	if _, fields, _ := personAnswerOf(t, servedWith(t, served), bearer, accessDave); fields["held"] != nil || fields["not_held_until"] != nil {
		t.Errorf("with Standing failing under IAP the person answer = %v; want held and not_held_until left out", fields)
	}
}

// describeUntil is a wanted not_held_until as a failure names it.
func describeUntil(until *time.Time) string {
	if until == nil {
		return "left out"
	}
	return until.Format(time.RFC3339)
}
