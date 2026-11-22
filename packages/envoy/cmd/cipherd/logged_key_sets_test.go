package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/config"
	"github.com/sjawhar/envoy/internal/oidc"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// TestAnIssuersKeySetLogsEachChangeOnce: for Google's key set and the token exchange's GitHub
// issuer's alike, while the key set cannot be read a burst of reads writes one WARN line naming
// the issuer and the key set it could not read, never a token; once it can be read, one INFO line;
// and reads while it can be read, or by a caller whose context ended, write nothing. A failure
// after the recovery writes its own line again.
func TestAnIssuersKeySetLogsEachChangeOnce(t *testing.T) {
	for _, kind := range []struct {
		name                 string
		build                func(*oidc.Keys, string) *loggedKeySet
		unreadable, readable string
	}{
		{"Google's", newGoogleKeySet, googleKeysUnreadableMessage, googleKeysReadableMessage},
		{"the GitHub issuer's", newGitHubKeySet, githubKeysUnreadableMessage, githubKeysReadableMessage},
	} {
		t.Run(kind.name, func(t *testing.T) {
			logged := captureLog(t)
			issuer := oidctest.New(t)
			issuer.PublishKey(t, "issuer-key")
			keys, err := oidc.NewKeys(context.Background(), issuer.URL())
			if err != nil {
				t.Fatal(err)
			}
			// Every Key reads the set, so each step below is the read it says, not a spaced failure.
			keys.FailureSpacing = 0
			set := kind.build(keys, issuer.URL())
			count := func(message string) int { return strings.Count(logged.String(), `msg="`+message+`"`) }

			issuer.FailKeys(http.StatusServiceUnavailable)
			for range 5 {
				if _, err := set.Key(context.Background(), "issuer-key", "RS256"); !errors.Is(err, oidc.ErrKeySet) {
					t.Fatalf("Key while the key set answers 503 = %v, want oidc.ErrKeySet", err)
				}
			}
			if got := count(kind.unreadable); got != 1 {
				t.Fatalf("log after five failed reads = %q, want one %q line", logged, kind.unreadable)
			}
			if line := logged.String(); !strings.Contains(line, "level=WARN") || !strings.Contains(line, "issuer="+issuer.URL()) || !strings.Contains(line, "/keys") {
				t.Fatalf("log = %q, want a WARN line naming the issuer and the key set it could not read", line)
			}

			issuer.FailKeys(0)
			for range 3 {
				if _, err := set.Key(context.Background(), "issuer-key", "RS256"); err != nil {
					t.Fatalf("Key once the key set answers = %v", err)
				}
			}
			if got := count(kind.readable); got != 1 || !strings.Contains(logged.String(), `level=INFO msg="`+kind.readable+`" issuer=`+issuer.URL()) {
				t.Fatalf("log after the key set answered again = %q, want one INFO %q line naming the issuer", logged, kind.readable)
			}

			// A key id the cached set does not hold is the read that meets the next failure: the
			// set read moments ago still answers the key it holds.
			issuer.FailKeys(http.StatusServiceUnavailable)
			if _, err := set.Key(context.Background(), "a-key-id-no-set-holds", "RS256"); !errors.Is(err, oidc.ErrKeySet) {
				t.Fatalf("Key of an unheld key id while the key set answers 503 = %v, want oidc.ErrKeySet", err)
			}
			if got := count(kind.unreadable); got != 2 {
				t.Fatalf("log after the key set failed again = %q, want a second %q line", logged, kind.unreadable)
			}

			// A read whose caller has gone says nothing of the key set, on a set never read before.
			unread, err := oidc.NewKeys(context.Background(), issuer.URL())
			if err != nil {
				t.Fatal(err)
			}
			ended, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := kind.build(unread, issuer.URL()).Key(ended, "issuer-key", "RS256"); err == nil {
				t.Fatal("Key on an ended context answered a key from a set never read")
			}
			if got := count(kind.unreadable); got != 2 {
				t.Fatalf("log after a read whose caller had gone = %q, want still two %q lines", logged, kind.unreadable)
			}
		})
	}
}

// TestTheGoogleVerifierDiscoveryBuildsReadsTheLoggedKeySet: the listed caller's Google verifier
// that discoverVerifiers builds reads Google's key set through the logged set it hands the token
// exchange (newGoogleKeySet), so a Google token checked while that key set cannot be read writes
// the WARN line, and is answered oidc.ErrIssuerUnavailable.
func TestTheGoogleVerifierDiscoveryBuildsReadsTheLoggedKeySet(t *testing.T) {
	logged := captureLog(t)
	google := oidctest.New(t)
	key := google.PublishKey(t, "google-key")
	cfg := vmConfig(t, localCluster(t).URL(), "example-vm-audience")
	cfg.GoogleOIDCIssuer = google.URL()
	found, err := discoverVerifiers(context.Background(), cfg, issuerKeys)
	if err != nil {
		t.Fatal(err)
	}
	google.FailKeys(http.StatusServiceUnavailable)
	claims := google.Claims(exampleVMAccount, "example-vm-audience")
	if _, err := found.GoogleIdentities.Verify(context.Background(), google.Mint(t, key, claims)); !errors.Is(err, oidc.ErrIssuerUnavailable) {
		t.Fatalf("Verify while Google's key set answers 503 = %v, want oidc.ErrIssuerUnavailable", err)
	}
	if got := strings.Count(logged.String(), `msg="`+googleKeysUnreadableMessage+`"`); got != 1 {
		t.Fatalf("log = %q, want one %q line", logged, googleKeysUnreadableMessage)
	}
}

// TestTheExchangesGitHubIssuerReadsALoggedKeySet: the GitHub issuer the broker hands the token
// exchange (githubExchangeIssuer) names BROKER_EXCHANGE_GITHUB_ISSUER and its audience and reads
// that issuer's key set through the logged set (newGitHubKeySet), so a workflow token's key read
// while the key set cannot be read writes the GitHub WARN line once.
func TestTheExchangesGitHubIssuerReadsALoggedKeySet(t *testing.T) {
	logged := captureLog(t)
	github := oidctest.New(t)
	github.PublishKey(t, "github-key")
	cfg := config.Config{ExchangeGitHubIssuer: github.URL(), ExchangeGitHubAudience: "example-workflow-audience"}
	issuer, err := githubExchangeIssuer(context.Background(), cfg, issuerKeys)
	if err != nil {
		t.Fatal(err)
	}
	if issuer.Issuer != github.URL() || issuer.Audience != "example-workflow-audience" {
		t.Fatalf("the exchange's GitHub issuer = %q for %q, want %q for example-workflow-audience", issuer.Issuer, issuer.Audience, github.URL())
	}
	github.FailKeys(http.StatusServiceUnavailable)
	for range 3 {
		if _, err := issuer.Keys.Key(context.Background(), "github-key", "RS256"); !errors.Is(err, oidc.ErrKeySet) {
			t.Fatalf("Key while the GitHub key set answers 503 = %v, want oidc.ErrKeySet", err)
		}
	}
	if got := strings.Count(logged.String(), `msg="`+githubKeysUnreadableMessage+`"`); got != 1 {
		t.Fatalf("log = %q, want one %q line", logged, githubKeysUnreadableMessage)
	}
}
