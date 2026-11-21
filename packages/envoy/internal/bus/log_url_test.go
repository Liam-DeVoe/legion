package bus

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/testnats"
)

// A line about the server a connection reached names it by its position in the servers the
// connection dials, never by its URL: nats.go authenticates with a URL's userinfo, a user name
// alone as a token and user:password as a password, and a URL's host can be a piece of a
// credential a comma split (refuseRemoteNATS). A URL that names no port matches on its host, and a
// server the list does not hold is one the cluster advertised.
func TestServerOf(t *testing.T) {
	// The URL with a user and password is built here, so no source line holds one.
	withUserinfo := "tls://user-marker-0:" + "pw-marker-1@nats.example.test:4223"
	urls := []string{"nats://127.0.0.1:4222", withUserinfo, "127.0.0.1"}
	for _, tc := range []struct{ connected, want string }{
		{"nats://127.0.0.1:4222", "1 of 3"},
		{withUserinfo, "2 of 3"},
		{"nats://NATS.example.test:4223", "2 of 3"},
		{"nats://127.0.0.1:4224", "3 of 3"},
		{"nats://10.0.0.7:4222", "one the cluster advertised, beside the 3 configured"},
		{"", "one the cluster advertised, beside the 3 configured"},
	} {
		if got := serverOf(urls, tc.connected); got != tc.want {
			t.Errorf("serverOf(%q) = %q, want %q", tc.connected, got, tc.want)
		}
	}
}

// A connection's error prints as its class alone: a lookup's, a dial's through the SOCKS5 proxy and
// a certificate's name check name the server by its host, which can be a piece of a credential, and
// none of it prints. The system's words for a failed call, EOF, a time out and nats.go's own
// errors print as they are; nats.go's TLS error keeps its words before the class beneath it; an
// error with no class prints as connectionUnprinted.
func TestConnectionClass(t *testing.T) {
	const host = "madeUpHostPiece0123456789"
	dial := func(err error) error { return &net.OpError{Op: "dial", Net: "tcp", Err: err} }
	socks := func(err error) error {
		return &net.OpError{Op: "socks connect", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1055}, Err: err}
	}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "unknown"},
		{"a lookup", dial(&net.DNSError{Err: "no such host", Name: host, IsNotFound: true}), "name resolution failed"},
		{"a port net cannot read", dial(&net.AddrError{Err: "invalid port", Addr: host}), "the server's address is no host and port the system can dial"},
		{"a refused dial", dial(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}), "connection refused"},
		{"a reset", dial(syscall.ECONNRESET), "connection reset by peer"},
		{"a deadline", fmt.Errorf("wait: %w", context.DeadlineExceeded), "timed out"},
		{"EOF", io.EOF, "EOF"},
		{"a certificate for another host", fmt.Errorf("%w: %w", nats.ErrTLS, x509.HostnameError{Host: host, Certificate: &x509.Certificate{}}), "nats: tls error: TLS failed"},
		{"a TLS error with nothing beneath it to class", nats.ErrTLS, "nats: tls error"},
		{"a proxy's reply", socks(errors.New("unknown error host unreachable")), "the SOCKS5 proxy answered host unreachable"},
		{"a proxy's other failure", socks(errors.New("unknown error " + host)), "the SOCKS5 proxy failed the connection"},
		{"a proxy nothing listens at", socks(dial(syscall.ECONNREFUSED)), "connection refused"},
		{"nats.go's own error", nats.ErrNoServers, "nats: no servers available for connection"},
		{"a permissions violation, its subject left out", fmt.Errorf("%w: %s", nats.ErrPermissionViolation, host), "nats: permissions violation"},
		{"an error with no class", errors.New("nats: " + host), connectionUnprinted},
	} {
		got := connectionClass(tc.err)
		if got != tc.want {
			t.Errorf("%s: connectionClass = %q, want %q", tc.name, got, tc.want)
		}
		if strings.Contains(got, host) {
			t.Errorf("%s: connectionClass = %q, which names the host", tc.name, got)
		}
	}
	// A failed connect returns that class, and errors.Is still reads the error beneath it.
	failed := connectError{dial(&net.DNSError{Err: "no such host", Name: host, IsNotFound: true})}
	var lookup *net.DNSError
	if failed.Error() != "name resolution failed" || !errors.As(failed, &lookup) {
		t.Errorf("connectError = %q, as a DNS error %t; want its class, and the error beneath it", failed.Error(), errors.As(failed, &lookup))
	}
}

// The failed-reconnect WARN names how many servers the client was configured with and the
// failure's class, and the refusal of a remote server names that server by its position; both are
// logged, so neither carries a URL's userinfo or host.
func TestLoggedNATSURLsCarryNoUserinfo(t *testing.T) {
	logs := captureLogs(t)
	urls := []string{
		"nats://user-marker-0:pw-marker-1@127.0.0.1:4222",
		"nats://tok-marker-2@127.0.0.1:4223",
		"u-marker-5:pw-marker-3@127.0.0.1:4224",
	}
	secrets := []string{"user-marker-0", "pw-marker-1", "tok-marker-2", "u-marker-5", "pw-marker-3", "127.0.0.1"}

	options("log-url-test", urls, nil, nil, nil, nil, nil).ReconnectErrCB(nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED})

	// Only this client's records: a client an earlier test left reconnecting logs through the same
	// default.
	var mine []map[string]any
	for _, record := range logs.records(t) {
		if record["name"] == "log-url-test" {
			mine = append(mine, record)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("%d records logged, want 1: %s", len(mine), logs)
	}
	record := mine[0]
	if record["level"] != "WARN" || record["msg"] != "envoy nats reconnect attempt failed" {
		t.Fatalf("record = %v, want the failed reconnect WARN", record)
	}
	if record["servers"] != float64(3) || record["error"] != "connection refused" {
		t.Errorf("servers = %v and error = %v, want 3 and connection refused", record["servers"], record["error"])
	}
	for _, secret := range secrets {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the failed reconnect WARN holds %q: %s", secret, logs)
		}
	}

	unset := func(string) (string, bool) { return "", false }
	err := refuseRemoteNATS([]string{"nats://127.0.0.1:4222", " ", "nats://user-marker-6:" + "pw-marker-4@nats.example.test:4222"}, unset)
	if !errors.Is(err, ErrRemoteNATS) {
		t.Fatalf("refuseRemoteNATS = %v, want a %v", err, ErrRemoteNATS)
	}
	if !strings.Contains(err.Error(), "NATS server URL 3 of 3 names another machine") {
		t.Errorf("refusal %q does not name the server by its position", err)
	}
	for _, secret := range []string{"user-marker-6", "pw-marker-4", "nats.example.test"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("refusal %q holds %q", err, secret)
		}
	}
}

// The reconnected INFO names the server the connection reached by its position, never by the URL
// nats.go renders with its userinfo (ConnectedUrl); testnats.URL is a user:password URL, so the
// line must hold neither part, nor the host.
func TestTheReconnectedLineCarriesNoUserinfo(t *testing.T) {
	uri := testnats.URL(t)
	configured, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse %s: %v", uri, err)
	}
	if configured.User == nil {
		t.Fatalf("testnats.URL %s carries no userinfo, so this test proves nothing", uri)
	}
	nc, err := nats.Connect(uri)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	logs := captureLogs(t)

	options("reconnected-url-test", []string{"nats://127.0.0.1:1", uri}, nil, nil, nil, nil, nil).ReconnectedCB(nc)

	var logged string
	var record map[string]any
	for _, candidate := range logs.records(t) {
		if candidate["name"] == "reconnected-url-test" && candidate["msg"] == "envoy nats reconnected" {
			record = candidate
			logged, _ = candidate["server"].(string)
		}
	}
	if logged != "2 of 2" {
		t.Fatalf("the reconnected record names the server %q, want 2 of 2: %s", logged, logs)
	}
	password, _ := configured.User.Password()
	for _, part := range []string{configured.User.Username() + ":" + password, configured.Host} {
		if line := fmt.Sprint(record); strings.Contains(line, part) {
			t.Errorf("the reconnected line holds %q: %s", part, logs)
		}
	}
}
