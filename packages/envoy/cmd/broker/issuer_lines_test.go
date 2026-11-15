// issuer_lines_test.go pins the lines the broker writes about a cluster issuer whose tokens it
// cannot verify for want of a read (loggedIssuers): one when its tokens start to be answered 503,
// however many arrive, and one when a token of it verifies again.
package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/sjawhar/envoy/internal/oidc"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// TestAnIssuerTheBrokerCannotReadIsLoggedOnceAndOnceWhenReadAgain: while B's key set cannot be
// read, a burst of twenty tokens of B, sent at once and each answered ErrIssuerUnavailable, writes
// one line naming B and the key set's error; a token of A, which verifies, writes none; once B's
// key set answers, the first token of B that verifies writes one line, and the tokens after it
// none. A token of C whose caller hung up while C's discovery document could not be read writes
// no line.
func TestAnIssuerTheBrokerCannotReadIsLoggedOnceAndOnceWhenReadAgain(t *testing.T) {
	logged := captureLog(t)
	a, b, c := oidctest.New(t), oidctest.New(t), oidctest.New(t)
	aKey, bKey, cKey := a.PublishKey(t, "a-key"), b.PublishKey(t, "b-key"), c.PublishKey(t, "c-key")
	issuers := []oidc.NamedIssuer{{Name: "a", URL: a.URL()}, {Name: "b", URL: b.URL()}, {Name: "c", URL: c.URL()}}
	// No spacing: every token of B reads B's key set, so the one line of a burst is the
	// logger's doing and not the spacing's.
	verifier := newLoggedIssuers(oidc.NewIssuerSet(issuers, enrolmentAudience, 0), issuers)
	token := func(issuer *oidctest.Issuer, key *oidctest.Key) string {
		return issuer.Mint(t, key, issuer.Claims("system:serviceaccount:example-ns:example-worker", enrolmentAudience))
	}
	// lines is the log's lines carrying message, as captureLog's text handler writes them.
	lines := func(message string) []string {
		var found []string
		for line := range strings.Lines(logged.String()) {
			if strings.Contains(line, `msg="`+message+`"`) {
				found = append(found, line)
			}
		}
		return found
	}
	b.FailKeys(http.StatusServiceUnavailable)

	t.Run("a burst of tokens of an issuer whose key set cannot be read writes one line", func(t *testing.T) {
		const burst = 20
		errs := make([]error, burst)
		var wg sync.WaitGroup
		for i := range burst {
			wg.Go(func() { _, _, errs[i] = verifier.Verify(context.Background(), token(b, bKey)) })
		}
		wg.Wait()
		for i, err := range errs {
			if !errors.Is(err, oidc.ErrIssuerUnavailable) {
				t.Fatalf("token %d of B = %v, want oidc.ErrIssuerUnavailable", i, err)
			}
		}
		found := lines(issuerUnreadableMessage)
		if len(found) != 1 {
			t.Fatalf("%d tokens of B wrote %d %q lines, want one:\n%s", burst, len(found), issuerUnreadableMessage, logged)
		}
		for _, want := range []string{"level=WARN", "name=b", "url=" + b.URL(), "key set could not be read"} {
			if !strings.Contains(found[0], want) {
				t.Fatalf("the line %q does not carry %q", found[0], want)
			}
		}
	})

	t.Run("a token of an issuer that can be read writes no line", func(t *testing.T) {
		if _, _, err := verifier.Verify(context.Background(), token(a, aKey)); err != nil {
			t.Fatalf("the token of A = %v, want it verified", err)
		}
		if found := lines(issuerUnreadableMessage); len(found) != 1 || strings.Contains(found[0], "name=a") {
			t.Fatalf("a token of A changed the %q lines to %v, want B's one", issuerUnreadableMessage, found)
		}
		if found := lines(issuerReadableMessage); len(found) != 0 {
			t.Fatalf("a token of A wrote %v, want no %q line", found, issuerReadableMessage)
		}
	})

	t.Run("the first token of the issuer that verifies again writes one line", func(t *testing.T) {
		b.FailKeys(0)
		for i := range 3 {
			if _, _, err := verifier.Verify(context.Background(), token(b, bKey)); err != nil {
				t.Fatalf("token %d of B once its key set answers = %v, want it verified", i, err)
			}
		}
		found := lines(issuerReadableMessage)
		if len(found) != 1 {
			t.Fatalf("three tokens of B wrote %d %q lines, want one:\n%s", len(found), issuerReadableMessage, logged)
		}
		for _, want := range []string{"level=INFO", "name=b", "url=" + b.URL()} {
			if !strings.Contains(found[0], want) {
				t.Fatalf("the line %q does not carry %q", found[0], want)
			}
		}
		if found := lines(issuerUnreadableMessage); len(found) != 1 {
			t.Fatalf("the %q lines are now %v, want the one the burst wrote", issuerUnreadableMessage, found)
		}
	})

	t.Run("a token whose caller hung up writes no line", func(t *testing.T) {
		c.FailDiscovery(http.StatusServiceUnavailable)
		hungUp, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, err := verifier.Verify(hungUp, token(c, cKey)); !errors.Is(err, oidc.ErrIssuerUnavailable) {
			t.Fatalf("a token of C on an ended request, C's discovery document failing = %v, want oidc.ErrIssuerUnavailable", err)
		}
		if found := lines(issuerUnreadableMessage); len(found) != 1 || !strings.Contains(found[0], "name=b") {
			t.Fatalf("the token of C changed the %q lines to %v, want B's one: a caller that hung up says nothing of the issuer", issuerUnreadableMessage, found)
		}
	})
}
