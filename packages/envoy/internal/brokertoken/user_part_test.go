package brokertoken

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"authelia.com/provider/oauth2/handler/rfc9449"
)

// noReplay stands in for the replay store New requires; this test calls none of its methods.
type noReplay struct{ rfc9449.DPoPReplayStorage }

// The broker URL (DISPATCH_AGENT_SECRETS_URL) and the origin may carry a user and a password. An
// introspection built from a broker URL that does not parse fails naming no part of it, though the
// parse error quotes the URL whole, and New's refusal of an origin names it through redact.URL.
// The control is the same values with no user part, refused in the same way. The password is made
// for this test.
func TestARefusalNamesNoUserPartOfTheBrokerOrTheOrigin(t *testing.T) {
	const (
		user     = "made-up-user"
		password = "not-a-secret-4f1e8a"
	)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("made-up-account-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	holds := func(err error) bool {
		return strings.Contains(err.Error(), password) || strings.Contains(err.Error(), user)
	}
	for _, userPart := range []string{user + ":" + password + "@", ""} {
		broken := &Verifier{brokerURL: "http://" + userPart + "[broker", account: NewAccountToken(token)}
		if _, err := broken.introspect(context.Background(), "made-up-token"); err == nil || !errors.Is(err, ErrBrokerUnreachable) {
			t.Errorf("user part %q: introspect err = %v, want ErrBrokerUnreachable", userPart, err)
		} else if holds(err) {
			t.Errorf("the introspection's refusal holds the user part: %q", err)
		}
		_, err := New(Options{
			BrokerURL: "https://broker.test",
			Account:   NewAccountToken(token),
			Replay:    noReplay{},
			Address:   func(*http.Request) string { return "" },
			Origin:    "https://" + userPart + "dispatch.test/dispatch",
		})
		if err == nil || !strings.Contains(err.Error(), "origin") {
			t.Errorf("user part %q: New err = %v, want a refusal of the origin", userPart, err)
		} else if holds(err) {
			t.Errorf("New's refusal holds the user part: %q", err)
		}
	}
}
