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

// TestAnOlderBrokersUnknownFieldIsReadAsItCame: a broker from before the claim refuses it as an
// unknown field in a sentence whose URL holds a quote, which the rule prints as not readable. The
// refusal is still that broker's, decided on its words as they came (refusesAnUnknownField), so
// the enrollment is sent once more without the claim and the broker's second answer enrols it.
func TestAnOlderBrokersUnknownFieldIsReadAsItCame(t *testing.T) {
	claimed := podEnrollment
	claimed.Claim = plannerClaim
	broker, client := newIssuedClient(t)
	broker.enrollAnswers = []enrollAnswer{
		{http.StatusBadRequest, `{"code":"INVALID_ENROLLMENT","error":"body must be one valid JSON object: json: unknown field \"claim\"; see https://broker.example/fields?f=\"claim\""}`},
		{http.StatusCreated, `{"enrollment_id":"enr-older","lease_expires_at":"2026-09-26T12:15:00Z"}`},
	}
	got, err := client.Enroll(context.Background(), claimed)
	broker.mu.Lock()
	calls := len(broker.enrollReqs)
	broker.mu.Unlock()
	if err != nil || got.ID != "enr-older" || calls != 2 {
		t.Fatalf("Enroll = %+v, %v after %d calls; want the older broker's enr-older on the second", got, err, calls)
	}
}
