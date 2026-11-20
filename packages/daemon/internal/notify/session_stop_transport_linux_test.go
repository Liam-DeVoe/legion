package notify

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/stalledconnect"
)

// A limit set on the shared transport, Go's http.DefaultTransport in production, changes nothing
// for the clean stop: stopTransport clones that transport and removes each fixed limit, so a read
// whose connect, or whose TLS handshake, never completes runs until its own context ends, and ends
// with that context's error. Each row gives a clone of Go's transport one limit, of 2 s, 4.9 s or
// 5 s, on the connect or on the TLS handshake, and a one-minute response-header timeout, makes the
// stop's transport from it, and sends the stop's read with a 5 s context, the daemon's budget
// (CleanSessionStopBudget in internal/daemon). A limit the stop's transport kept would end the
// read in the rows under the budget after less than its context, with the transport's error, and
// race the context in the row at it. The connect goes to a listener whose accept queue is full
// (stalledconnect.Listen), the handshake to one that accepts and sends nothing. The rows change no
// transport another test uses, so they run side by side.
func TestTheCleanStopsTransportKeepsNoLimitOfTheSharedOne(t *testing.T) {
	const budget = 5 * time.Second
	for _, step := range []string{"connect", "TLS handshake"} {
		for _, limit := range []time.Duration{2 * time.Second, 4900 * time.Millisecond, budget} {
			t.Run(step+" limited to "+limit.String()+" on the shared transport", func(t *testing.T) {
				t.Parallel()
				shared := http.DefaultTransport.(*http.Transport).Clone()
				shared.ResponseHeaderTimeout = time.Minute
				var target string
				if step == "connect" {
					shared.DialContext = (&net.Dialer{Timeout: limit, KeepAlive: 30 * time.Second}).DialContext
					target = "http://" + stalledconnect.Listen(t)
				} else {
					shared.TLSHandshakeTimeout = limit
					target = "https://" + heldTLSListener(t)
				}
				p := New(target, "envoy-bearer")
				p.stops.Transport = stopTransport(shared)
				wantNoFixedLimit(t, p.stops.Transport)
				ctx, cancel := context.WithTimeout(context.Background(), budget)
				defer cancel()
				began := time.Now()
				_, err := p.ReadSessionRegistration(ctx, "ses_dead")
				took := time.Since(began)
				if took < budget || err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
					t.Fatalf("the clean stop's read, its %s stalled and limited to %s on the shared transport, ended after %s with %v, want it ended at its own context's %s deadline",
						step, limit, took, err, budget)
				}
			})
		}
	}
}

// heldTLSListener listens on loopback, accepts each connection and sends nothing on it until t
// ends, so a TLS handshake to it never completes; it returns the address.
func heldTLSListener(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var mu sync.Mutex
	var held []net.Conn
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			_ = conn.Close()
		}
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	return listener.Addr().String()
}
