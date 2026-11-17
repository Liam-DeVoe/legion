// packages/envoy/internal/broker/helper/revoke_printed_test.go
//go:build linux

package helper

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"testing"

	"golang.org/x/oauth2"

	"github.com/sjawhar/envoy/brokeraddr"
	"github.com/sjawhar/envoy/internal/broker/proof"
	"github.com/sjawhar/envoy/internal/brokerclient"
	"github.com/sjawhar/envoy/internal/redact"
)

// TestARevokeThatCannotReachTheBrokerPrintsNoID: the id of a revoke ends its request URL, and for
// `agent-secrets unenroll --enrollment` it is what the caller typed, which the broker never
// accepted. A clean broker address that does not answer makes net/http's error quote that URL
// whole, so the failure names the route with the id left out. It still unwraps to the refused
// connection. Control: the address itself and the route still print, and so does the system's
// reason.
func TestARevokeThatCannotReachTheBrokerPrintsNoID(t *testing.T) {
	const typed = "hunter2pw-enrollment-id-7b31"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + listener.Addr().String()
	listener.Close()
	key, err := proof.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	b := &Broker{URL: brokeraddr.New(closed)}
	t.Cleanup(b.Close)
	err = b.revokeUnder(context.Background(), &brokerclient.Authority{Key: key, CredentialID: "cred-1"}, nil, typed)
	if err == nil {
		t.Fatal("the revoke reached a broker that is not there")
	}
	wantNamedNotPrinted(t, "the revoke's error", err.Error(), []string{closed, "/v1/enrollments/", "connection refused"}, typed)
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("the revoke's error = %v, want it to unwrap to the refused connection", err)
	}
}

// TestAnAnswerUnderClassIsNotTheBrokersRefusal: oauthBrokerError makes a BrokerError, which
// prints an OAuth answer's code and description, only from the broker's own answer. An answer
// that redact.Class holds came from another server, and Class printed it as its class alone, so
// its description, which that server can fill with an echo of a credential, does not print here
// either. The broker's own refusal beside a clean address still prints its code and its words:
// the control.
func TestAnAnswerUnderClassIsNotTheBrokersRefusal(t *testing.T) {
	const secret = "hunter2pw-answer-4d81"
	addr := brokeraddr.New("https://broker.example")
	foreign := &oauth2.RetrieveError{
		Response:  &http.Response{StatusCode: http.StatusBadRequest},
		ErrorCode: "invalid_client", ErrorDescription: "echo of " + secret,
	}
	err := oauthBrokerError(fmt.Errorf("renew the sign-in at X: %w", redact.Class(foreign)), addr)
	wantNamedNotPrinted(t, "the foreign answer", err.Error(), []string{"400"}, secret)
	var be *BrokerError
	if errors.As(err, &be) {
		t.Errorf("the foreign answer made the BrokerError %q, which is the broker's own refusal", be)
	}

	own := &oauth2.RetrieveError{
		Response:  &http.Response{StatusCode: http.StatusBadRequest},
		ErrorCode: "invalid_request", ErrorDescription: "LOGIN_PENDING: a login is already pending",
	}
	err = oauthBrokerError(own, addr)
	if !errors.As(err, &be) || err.Error() != "broker 400 LOGIN_PENDING: a login is already pending" {
		t.Errorf("the broker's own refusal = %q, want the BrokerError %q", err, "broker 400 LOGIN_PENDING: a login is already pending")
	}
}
