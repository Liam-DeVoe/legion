package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/oidc"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// TestGooglesKeySetLogsEachChangeOnce: while Google's key set cannot be read, a burst of reads
// writes one WARN line naming the issuer and the key set it could not read, never a token; once it
// can be read, one INFO line; and reads while it can be read, or by a caller whose context ended,
// write nothing. A failure after the recovery writes its own line again.
func TestGooglesKeySetLogsEachChangeOnce(t *testing.T) {
	logged := captureLog(t)
	google := oidctest.New(t)
	google.PublishKey(t, "google-key")
	keys, err := oidc.NewKeys(context.Background(), google.URL())
	if err != nil {
		t.Fatal(err)
	}
	// Every Key reads the set, so each step below is the read it says, not a spaced failure.
	keys.FailureSpacing = 0
	set := newGoogleKeySet(keys, google.URL())
	count := func(message string) int { return strings.Count(logged.String(), `msg="`+message+`"`) }

	google.FailKeys(http.StatusServiceUnavailable)
	for range 5 {
		if _, err := set.Key(context.Background(), "google-key", "RS256"); !errors.Is(err, oidc.ErrKeySet) {
			t.Fatalf("Key while the key set answers 503 = %v, want oidc.ErrKeySet", err)
		}
	}
	if got := count(googleKeysUnreadableMessage); got != 1 {
		t.Fatalf("log after five failed reads = %q, want one %q line", logged, googleKeysUnreadableMessage)
	}
	if line := logged.String(); !strings.Contains(line, "level=WARN") || !strings.Contains(line, "issuer="+google.URL()) || !strings.Contains(line, "/keys") {
		t.Fatalf("log = %q, want a WARN line naming the issuer and the key set it could not read", line)
	}

	google.FailKeys(0)
	for range 3 {
		if _, err := set.Key(context.Background(), "google-key", "RS256"); err != nil {
			t.Fatalf("Key once the key set answers = %v", err)
		}
	}
	if got := count(googleKeysReadableMessage); got != 1 || !strings.Contains(logged.String(), `level=INFO msg="`+googleKeysReadableMessage+`" issuer=`+google.URL()) {
		t.Fatalf("log after the key set answered again = %q, want one INFO %q line naming the issuer", logged, googleKeysReadableMessage)
	}

	// A key id the cached set does not hold is the read that meets the next failure: the set
	// read moments ago still answers the key it holds.
	google.FailKeys(http.StatusServiceUnavailable)
	if _, err := set.Key(context.Background(), "a-key-id-no-set-holds", "RS256"); !errors.Is(err, oidc.ErrKeySet) {
		t.Fatalf("Key of an unheld key id while the key set answers 503 = %v, want oidc.ErrKeySet", err)
	}
	if got := count(googleKeysUnreadableMessage); got != 2 {
		t.Fatalf("log after the key set failed again = %q, want a second %q line", logged, googleKeysUnreadableMessage)
	}

	// A read whose caller has gone says nothing of the key set, on a set never read before.
	unread, err := oidc.NewKeys(context.Background(), google.URL())
	if err != nil {
		t.Fatal(err)
	}
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newGoogleKeySet(unread, google.URL()).Key(ended, "google-key", "RS256"); err == nil {
		t.Fatal("Key on an ended context answered a key from a set never read")
	}
	if got := count(googleKeysUnreadableMessage); got != 2 {
		t.Fatalf("log after a read whose caller had gone = %q, want still two %q lines", logged, googleKeysUnreadableMessage)
	}
}

// TestTheGoogleVerifierDiscoveryBuildsReadsTheLoggedKeySet: the listed caller's Google verifier
// that discoverVerifiers builds reads Google's key set through the logged set it hands the token
// exchange (googleKeySet), so a Google token checked while that key set cannot be read writes the
// WARN line, and is answered oidc.ErrIssuerUnavailable.
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
