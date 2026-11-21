package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/policy/policytest"
)

// TestProjectPrivacyNeverLogsTheBrokersToken (Tester4672's red test, F1): a Dispatch that repeats
// the bearer it was sent, in a refusal's code or in a malformed projects value, must not get that
// bearer into the broker's log. GET /v1/project-privacy answers each 502 dispatch_unavailable in
// fixed words already; its WARN line must name the failure's kind alone, as GET
// /v1/private-projects does (redact.Class, or ErrBrokerRefused's fixed text).
func TestProjectPrivacyNeverLogsTheBrokersToken(t *testing.T) {
	logged := policytest.CaptureLog(t)
	for _, c := range []struct {
		name   string
		answer http.HandlerFunc
	}{
		{"a 500 whose code repeats the bearer", answerWith(http.StatusInternalServerError, `{"code":"INTERNAL `+privacyStubToken+`"}`)},
		{"a 401 whose code repeats the bearer", answerWith(http.StatusUnauthorized, `{"code":"REFUSED `+privacyStubToken+`"}`)},
		{"a projects value that repeats the bearer", answerWith(http.StatusOK, `{"projects":"`+privacyStubToken+`"}`)},
	} {
		t.Run(c.name, func(t *testing.T) {
			stub := newPrivacyStub(t, c.answer)
			g := newPrivacyServer(t, dispatchAt(stub, nil))
			before := len(logged.String())
			got := g.privacy(t, g.accountToken(t, privacyListenerAccount, "", time.Hour))
			if got.status != http.StatusBadGateway || strings.Contains(string(got.body), privacyStubToken) {
				t.Fatalf("GET %s = %d %s, want 502 with no bearer in it", privacyPath, got.status, got.body)
			}
			if written := logged.String()[before:]; strings.Contains(written, privacyStubToken) {
				t.Fatalf("the broker's log repeats the bearer it presented to Dispatch:\n%s", written)
			}
		})
	}
}
