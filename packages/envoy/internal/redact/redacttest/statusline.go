package redacttest

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
)

// Reason is a status line's reason phrase that quotes the probe's URL, credentials and all, as
// whatever answers in a server's place can write it: the phrase is the answering party's own
// words (net/http's Response.Status holds it), and net/http's own server writes only the standard
// one, so StatusLineServer writes it on the connection itself.
const Reason = "see " + Code

// StatusLineServer answers every request with "HTTP/1.1 <status> Reason", header (lines of its
// own, each ending in CRLF) and body, written on the connection itself, and answers its base URL.
// A message that prints the answer's status line prints the probe unless it names the status
// through the code and the standard phrase (redact.StatusLine).
func StatusLineServer(t testing.TB, status int, header, body string) string {
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
					if _, err := fmt.Fprintf(conn, "HTTP/1.1 %s %s\r\n%sContent-Length: %d\r\n\r\n%s", strconv.Itoa(status), Reason, header, len(body), body); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return "http://" + listener.Addr().String()
}
