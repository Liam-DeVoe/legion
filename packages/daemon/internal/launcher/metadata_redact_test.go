package launcher

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

// The probe's made-up credentials, and the parameter an access token rides under, in two parts so
// no line of this file reads as a credential to a scanner.
const (
	probeUser     = "not-a-secret-user"
	probePassword = "not-a-secret-password"
	probeToken    = "not-a-secret-token"
)

var probeWords = "the issuer at https://" + probeUser + ":" + probePassword + "@issuer.example/path?" +
	strings.Join([]string{"access", "token"}, "_") + "=" + probeToken

// TestAFailedExchangesLogLinePrintsNoCredentialTheServicesWordsQuote: the security token service
// refuses the exchange with words that quote a URL carrying a user, a password and an access
// token. The one line the endpoint logs for the failure names the service's answer only as the
// rule for remote text reads it (redact.OAuth2, through oauth2call.Call), and none of the three.
// It prints the line it read as `REDACT-CAPTURE launcher.exchange-failed <base64>` and fails with
// a message that begins `REDACT-ASSERT launcher.exchange-failed`, as the redaction check's rows do.
func TestAFailedExchangesLogLinePrintsNoCredentialTheServicesWordsQuote(t *testing.T) {
	g := newMetadataRig(t)
	g.sts.refuseWith(probeWords)
	if response, body := g.get(t, tokenPath, flavor); response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("a refused exchange answered %d %q, want 500", response.StatusCode, redacted(body))
	}
	logged := g.log.String()
	if !strings.Contains(logged, "launcher: metadata: token exchange failed ") {
		t.Fatalf("the endpoint logged %q, want its failure line", logged)
	}
	const capture = "launcher.exchange-failed"
	t.Logf("REDACT-CAPTURE %s %s", capture, base64.StdEncoding.EncodeToString([]byte(logged)))
	for _, secret := range []string{probeUser, probePassword, probeToken} {
		if strings.Contains(logged, secret) {
			t.Errorf("REDACT-ASSERT %s: the output prints the secret %q: %q", capture, secret, logged)
		}
	}
}
