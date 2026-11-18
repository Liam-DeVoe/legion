package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/admission"
	"github.com/sjawhar/envoy/internal/dispatch/auth"
	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/identity"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/oidc"
)

// stalledIssuer is a sign-in issuer that answers no refresh until release closes, or the
// refresh's own bound passes, and then answers that it cannot be reached. entered counts the
// refreshes that have begun waiting on it.
type stalledIssuer struct {
	entered atomic.Int32
	release chan struct{}
}

func (i *stalledIssuer) Refresh(ctx context.Context, _ string) (oidc.Session, error) {
	i.entered.Add(1)
	select {
	case <-i.release:
	case <-ctx.Done():
	}
	return oidc.Session{}, fmt.Errorf("oidc: refresh: %w: the test's issuer answers none", oidc.ErrIssuerUnavailable)
}

// A sign-in issuer slow to answer holds none of the request pool's share outside admission: a
// document socket's admission and a heartbeat check give their place back while their caller's
// lapsed confirmation is renewed with the issuer (identity.Membership.Confirm). So while eight
// people whose confirmations have lapsed wait on refreshes the issuer has not answered - four
// document sockets admitting them (docs.Service.ServeHTTP) and four document sockets' heartbeat
// checks (signInStillResolves), as many as the share holds - no place of the share is held, and a
// DPoP presentation, whose decision takes a place (decideBrokerToken), takes one at once and is
// answered as the same presentation is with the share empty. Once the issuer answers that it
// cannot renew them, each socket is refused 503 SIGNIN_UNAVAILABLE and each check answers
// ErrSignInUnavailable, as before, and every place is free again.
func TestASignInIssuerSlowToAnswerHoldsNoPlaceOfTheShareOutsideAdmission(t *testing.T) {
	ctx := context.Background()
	limiter := admission.New(admission.DefaultLimits())
	issuer := &stalledIssuer{release: make(chan struct{})}
	answer := sync.OnceFunc(func() { close(issuer.release) })
	database := storetest.Open(t)
	people := store.NewPgPeopleStore(database.Pool, testSigningKey, nil)
	sessions := store.NewPgSessionStore(database.Pool)
	person := identity.CookieIdentity{
		SigningKey: testSigningKey,
		Sessions:   sessions,
		Membership: &identity.Membership{People: people, Sessions: sessions, SignIn: issuer, Rule: identity.GroupRule{Group: "dispatch-members"}},
	}
	rig := newBrokerTokenRigOn(t, testServerOptions{store: database, admission: limiter, identity: person})
	documents := docs.New(docs.Deps{Store: database, Events: events.NewBroker(), Identity: person, Outside: limiter.Outside(), Settle: time.Hour})
	t.Cleanup(func() { _ = documents.Shutdown(context.Background()) })
	// Before the rig's own cleanups, so no work is still waiting on the issuer when the server and
	// its database close.
	var working sync.WaitGroup
	t.Cleanup(func() {
		answer()
		working.Wait()
	})

	caller := newBrokerCaller(t)
	const before, during = "a-token-presented-with-the-share-empty", "a-token-presented-while-the-issuer-stalls"
	rig.broker.Says(before, liveNamedToken(caller.jkt))
	rig.broker.Says(during, liveNamedToken(caller.jkt))
	emptyShare := rig.serve(caller.presenting(t, http.MethodGet, "/api/v1/whoami", before, nil))
	if emptyShare.Code != http.StatusOK {
		t.Fatalf("a presentation with the share empty: status=%d body=%s, want 200", emptyShare.Code, emptyShare.Body.String())
	}

	lapsed := make([]string, admission.OutsideLimit)
	cookies := make([]*http.Cookie, len(lapsed))
	for i := range lapsed {
		lapsed[i] = fmt.Sprintf("lapsed-%d@d.example", i)
		if err := people.SignIn(ctx, lapsed[i], fmt.Sprintf("refresh-token-%d", i)); err != nil {
			t.Fatalf("record %s's sign-in: %v", lapsed[i], err)
		}
		generation, err := sessions.EnsureSession(ctx, lapsed[i])
		if err != nil {
			t.Fatalf("establish %s's session: %v", lapsed[i], err)
		}
		if cookies[i], err = http.ParseSetCookie(auth.IssueSessionCookie(lapsed[i], generation, testSigningKey, false)); err != nil {
			t.Fatalf("parse %s's session cookie: %v", lapsed[i], err)
		}
	}
	if _, err := database.Pool.Exec(ctx, `update people set confirmed_at = now() - interval '2 hours' where email = any($1)`, lapsed); err != nil {
		t.Fatalf("date the confirmations two hours back: %v", err)
	}

	checks := make(chan error, len(lapsed))
	sockets := make(chan *httptest.ResponseRecorder, len(lapsed))
	for i, cookie := range cookies {
		if i%2 == 0 {
			check := httptest.NewRequest(http.MethodGet, "/ws/doc/room", nil)
			check.AddCookie(cookie)
			working.Go(func() { checks <- rig.served.signInStillResolves(check) })
			continue
		}
		socket := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/ws/doc/00000000-0000-4000-8000-%012d", i), nil)
		socket.AddCookie(cookie)
		working.Go(func() {
			answered := httptest.NewRecorder()
			documents.ServeHTTP(answered, socket)
			sockets <- answered
		})
	}
	pollUntil(t, 10*time.Second, "every lapsed sign-in's refresh waiting on the issuer", func() bool {
		return int(issuer.entered.Load()) == len(lapsed)
	})

	stalled := rig.serve(caller.presenting(t, http.MethodGet, "/api/v1/whoami", during, nil))
	if stalled.Code != emptyShare.Code || stalled.Body.String() != emptyShare.Body.String() {
		t.Errorf("a presentation while %d refreshes wait on the issuer: status=%d body=%s, want it answered as with the share empty: status=%d body=%s",
			len(lapsed), stalled.Code, stalled.Body.String(), emptyShare.Code, emptyShare.Body.String())
	}
	if stats := limiter.Outside().Stats(); stats.InUse != 0 || stats.MaxQueued != 0 || stats.Refused != [len(stats.Refused)]admission.OutsideRefusals{} {
		t.Errorf("with %d refreshes waiting on the issuer, %d places of the share outside admission are held, %d waited at most and %v were refused, want none of any",
			len(lapsed), stats.InUse, stats.MaxQueued, stats.Refused)
	}

	answer()
	for range len(lapsed) / 2 {
		select {
		case err := <-checks:
			if !errors.Is(err, identity.ErrSignInUnavailable) {
				t.Errorf("a heartbeat check once the issuer answered it cannot renew: %v, want ErrSignInUnavailable", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("a heartbeat check that waited on the issuer was never answered once it answered")
		}
		select {
		case answered := <-sockets:
			if answered.Code != http.StatusServiceUnavailable || !strings.Contains(answered.Body.String(), `"code":"SIGNIN_UNAVAILABLE"`) {
				t.Errorf("a document socket once the issuer answered it cannot renew: status=%d body=%s, want 503 SIGNIN_UNAVAILABLE", answered.Code, answered.Body.String())
			}
		case <-time.After(30 * time.Second):
			t.Fatal("a document socket that waited on the issuer was never answered once it answered")
		}
	}
	if stats := limiter.Outside().Stats(); stats.InUse != 0 || stats.Queued != 0 {
		t.Errorf("once every refresh was answered, %d places are held and %d wait, want none", stats.InUse, stats.Queued)
	}
}
