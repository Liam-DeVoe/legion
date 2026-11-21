// Package testreason is the probe a daemon test gives a print site of an answer's status line, the
// daemon module's twin of the envoy module's redacttest, which it cannot import: a server whose
// reason phrase quotes a URL carrying a made-up credential in its user information and its query,
// and the assertion that a printed text holds none of them.
//
// The assertion is the designated one of the check c-redact-remote-text's mutation gate
// (packages/envoy/cmd/redact-gate): RequireNone first prints the production output it was given in
// one line, `REDACT-CAPTURE <capture> <base64>`, and fails, where that output holds a credential,
// with a message that begins `REDACT-ASSERT <capture>`. Every credential here is made up, and each
// is one of the gate's markers.
package testreason

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

const (
	// User and Password are the user information the probe's URL carries.
	User     = "not-a-secret-user"
	Password = "not-a-secret-password"
	// Token is the value of the access-token parameter the probe's URL carries.
	Token = "not-a-secret-token"
	// param is the parameter OAuth carries an access token under, in two parts so no line of
	// this file reads as a credential to a scanner.
	param = "access" + "_token"
	// Reason is a status line's reason phrase that quotes the probe's URL, credentials and all:
	// the phrase is the answering party's own words (net/http's Response.Status holds it), and
	// net/http's own server writes only the standard one, so Server writes it on the connection.
	Reason = "see https://" + User + ":" + Password + "@issuer.example/help?" + param + "=" + Token
)

// Secrets are the credentials no printed text may hold.
var Secrets = []string{User, Password, Token}

// Server answers every request with "HTTP/1.1 <status> Reason", header (lines of its own, each
// ending in CRLF) and body, written on the connection itself, and answers its base URL.
func Server(t testing.TB, status int, header, body string) string {
	t.Helper()
	return ServerFunc(t, func(*http.Request) (int, string, string) { return status, header, body })
}

// ServerFunc is Server answering each request with what answer gives it: a status, header lines
// and a body, under Reason.
func ServerFunc(t testing.TB, answer func(*http.Request) (status int, header, body string)) string {
	t.Helper()
	return serve(t, func(conn net.Conn, request *http.Request) bool {
		status, header, body := answer(request)
		_, err := fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\n%sContent-Length: %d\r\n\r\n%s", status, Reason, header, len(body), body)
		return err == nil
	})
}

// CutServer is Server writing half of body under a Content-Length for the whole of it, then
// closing the connection, as a reset mid-body reads to the client: the status line, part of the
// body, then an unexpected end.
func CutServer(t testing.TB, status int, body string) string {
	t.Helper()
	return serve(t, func(conn net.Conn, _ *http.Request) bool {
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", status, Reason, len(body), body[:len(body)/2])
		return false
	})
}

// serve accepts connections until t ends and hands each request read on one to write, which
// answers it on the connection and reports whether the connection serves another request.
func serve(t testing.TB, write func(net.Conn, *http.Request) bool) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				for {
					request, err := http.ReadRequest(reader)
					if err != nil {
						return
					}
					_, _ = io.Copy(io.Discard, request.Body)
					if !write(conn, request) {
						return
					}
				}
			}(conn)
		}
	}()
	return "http://" + listener.Addr().String()
}

// RequireNone captures text under capture and fails t, naming capture and each secret, for each of
// Secrets text holds. It is a print-site test's designated assertion.
func RequireNone(t testing.TB, capture, text string) {
	t.Helper()
	t.Logf("REDACT-CAPTURE %s %s", capture, base64.StdEncoding.EncodeToString([]byte(text)))
	for _, secret := range Secrets {
		if strings.Contains(text, secret) {
			t.Errorf("REDACT-ASSERT %s: the output prints the secret %q: %q", capture, secret, text)
		}
	}
}
