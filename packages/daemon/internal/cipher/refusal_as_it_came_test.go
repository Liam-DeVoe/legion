package cipher

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestABrokerRefusalIsReadFromItsCodeAsItCame: the broker refuses a pod-session registration with
// a coded refusal whose error names a URL right before the JSON quote that ends it, which the rule
// cannot read as clean, so it prints the whole body as not readable. The refusal is still the
// broker's code: the client decodes the code as it came (oauth2call.CodedRefusal, its code where
// the rule prints it unchanged) and decides on it - POD_REGISTRAR_NOT_LISTED is retried, never
// permanent - and the error prints the broker's words as the rule reads them, which names no URL.
func TestABrokerRefusalIsReadFromItsCodeAsItCame(t *testing.T) {
	broker, server := newRegistrationBroker(t, "pod-uid-1")
	broker.answers = []enrollAnswer{{status: http.StatusForbidden,
		body: `{"code":"POD_REGISTRAR_NOT_LISTED","error":"pair the account in https://broker.example/settings#registrars"}`}}
	c := &Client{URL: server.URL, HTTP: server.Client(), TokenFile: tokenFile(t, "the-daemon's-token")}
	_, err := c.RegisterPodSession(context.Background(), "pod-uid-1", testPodToken, "ses_implementer", testThumbprint, 7)
	var api *APIError
	if !errors.As(err, &api) || api.Code != "POD_REGISTRAR_NOT_LISTED" || !api.RegistrarNotListed() || IsPermanent(err) {
		t.Fatalf("a refusal coded POD_REGISTRAR_NOT_LISTED beside a URL = %v, want the broker's code, retried", err)
	}
	if strings.Contains(err.Error(), "#registrars") {
		t.Errorf("the refusal prints %q, want the broker's words as the rule reads them", err)
	}
}
