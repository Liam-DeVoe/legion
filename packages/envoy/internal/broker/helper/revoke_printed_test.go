// packages/envoy/internal/broker/helper/revoke_printed_test.go
//go:build linux

package helper

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/sjawhar/envoy/brokeraddr"
	"github.com/sjawhar/envoy/internal/broker/proof"
	"github.com/sjawhar/envoy/internal/brokerclient"
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
	if strings.Contains(err.Error(), typed) {
		t.Errorf("the revoke's error = %q, which prints the id it was given", err)
	}
	for _, want := range []string{closed, "/v1/enrollments/", "connection refused"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the revoke's error = %q, want it to name %q", err, want)
		}
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("the revoke's error = %v, want it to unwrap to the refused connection", err)
	}
}
