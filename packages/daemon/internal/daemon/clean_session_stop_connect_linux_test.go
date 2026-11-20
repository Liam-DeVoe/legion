package daemon

import (
	"bytes"
	"context"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// A clean stop whose connect to the listener never completes ends on its own context deadline,
// cleanSessionStopBudget, and not on a connect limit of the transport. The stop's client sends
// through http.DefaultTransport (notify.HTTPPublisher.stops), whose dialer gives a connect 30 s.
// That dialer sits inside the transport's DialContext function, where no test can read its limit
// as TestCleanSessionStopDeadlinePrecedesDefaultTransportTLSHandshake reads the TLS one, so this
// test stalls a connect instead. On Linux a listener whose accept queue is full drops each SYN
// that reaches it, so a connect to it is neither answered nor refused. The listener here gets a
// backlog of none, one connection the test makes and never accepts fills its queue, and the
// listener's own count of connections waiting to be accepted (TCP_INFO's tcpi_unacked, beside
// its backlog in tcpi_sacked) says the queue is full before the stop is sent. Nothing accepts, so
// it stays full. A connect limit at or under the budget would end the stop first, with the
// dialer's "i/o timeout", and so would the dialer's own 30 s if the stop's context had no
// deadline: the stop must log its context's deadline instead.
func TestCleanSessionStopContextEndsBeforeAStalledConnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	raw, err := listener.(*net.TCPListener).SyscallConn()
	if err != nil {
		t.Fatalf("the listener's socket: %v", err)
	}
	var listenErr error
	if err := raw.Control(func(fd uintptr) { listenErr = syscall.Listen(int(fd), 0) }); err != nil || listenErr != nil {
		t.Fatalf("giving the listener a backlog of none: %v, %v", err, listenErr)
	}
	queue := func() (waiting, backlog uint32) {
		t.Helper()
		var info *unix.TCPInfo
		var infoErr error
		if err := raw.Control(func(fd uintptr) {
			info, infoErr = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
		}); err != nil || infoErr != nil {
			t.Fatalf("reading the listener's accept queue: %v, %v", err, infoErr)
		}
		return info.Unacked, info.Sacked
	}
	filler, err := net.DialTimeout("tcp", listener.Addr().String(), testwait.Bounded(t, time.Minute, time.Second))
	if err != nil {
		t.Fatalf("the connection that fills the listener's accept queue: %v", err)
	}
	t.Cleanup(func() { _ = filler.Close() })
	testwait.Eventually(t, "the listener's accept queue to be full", func() bool {
		waiting, backlog := queue()
		return waiting > backlog
	})

	var inFlight sessionStopsInFlight
	var logged bytes.Buffer
	stop := cleanSessionStop(&inFlight, "http://"+listener.Addr().String(), "envoy-token", sessionStopOverrides{}, bufferedLogger(&logged))
	began := time.Now()
	ended := stop("ses_dead")
	bound := testwait.Bounded(t, time.Minute, time.Second)
	select {
	case <-ended:
	case <-time.After(bound):
		t.Fatalf("the clean stop had not returned %s after it was sent, with its connect stalled", bound)
	}
	took := time.Since(began)
	if !strings.Contains(logged.String(), context.DeadlineExceeded.Error()) {
		t.Fatalf("the clean stop with its connect stalled ended after %s and logged %q, want it ended at its context's deadline, cleanSessionStopBudget (%s), before any connect limit of the transport",
			took, logged.String(), cleanSessionStopBudget)
	}
}
