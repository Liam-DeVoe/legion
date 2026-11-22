package writerhold

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/oidc"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// The probe check reads the recorded issuer's discovery document within the auth service's own
// bound for that read (oidc.DiscoveryAttemptTimeout), whatever the command's context allows: the
// command's context has no deadline (signal.NotifyContext), so an issuer that accepts the
// connection and never answers would otherwise hold the check, and the bus, for good. Past the
// bound the check is UNKNOWN (b), naming the issuer's address. The controls, a closed port and a
// 503, answer UNKNOWN at once. The reviewer's case (round 5, M-1); control: db91740cb6, where the
// check had not answered after 90 s.
func TestTheProbeCheckIsUnknownWithinTheBoundWhenTheIssuerNeverAnswers(t *testing.T) {
	signer := oidctest.New(t)
	key := oidctest.NewKey(t, "k")
	release := make(chan struct{})
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(silent.Close)
	t.Cleanup(func() { close(release) })
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(unavailable.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closed := "http://" + listener.Addr().String()
	require.NoError(t, listener.Close())

	for _, row := range []struct{ name, issuer string }{
		{"control: a closed port", closed},
		{"control: a 503", unavailable.URL},
		{"an issuer that accepts and never answers", silent.URL},
	} {
		t.Run(row.name, func(t *testing.T) {
			raw := signer.Mint(t, key, map[string]any{"iss": row.issuer, "sub": "s", "aud": []string{"a"},
				"iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Hour).Unix()})
			op := &Operator{ProbeToken: func() (string, error) { return raw, nil }}
			op.probe = &ProbeCredential{TokenSHA256: "not-this-token", Issuer: row.issuer, Subject: "s", Audience: []string{"a"},
				Expiry: time.Now().Add(time.Hour)}
			// The command's context: no deadline.
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			done := make(chan error, 1)
			go func() { done <- op.probeProven(ctx) }()
			select {
			case err := <-done:
				var unknown *Unknown
				require.ErrorAs(t, err, &unknown, "the check past an issuer that does not answer")
				require.ErrorContains(t, err, "(b)")
				require.ErrorContains(t, err, row.issuer, "the check does not name the issuer's address")
			case <-time.After(oidc.DiscoveryAttemptTimeout + restartPoll):
				t.Fatalf("the probe check had not answered %s after the issuer's discovery read began", oidc.DiscoveryAttemptTimeout+restartPoll)
			}
		})
	}
}
