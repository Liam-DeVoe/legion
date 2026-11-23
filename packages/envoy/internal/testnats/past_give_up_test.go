package testnats

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/testwait"
)

const (
	// pastGiveUpChildEnv has TestAConnectionAWaitMadeKeepsWorkingPastTheGiveUp run as its own
	// child, which connects to the server whose URL the variable holds, then publishes and flushes
	// past the shared wait's give-up, under a deadline of pastGiveUpDeadline.
	pastGiveUpChildEnv = "TESTNATS_PAST_GIVE_UP_CHILD"

	// pastGiveUpDeadline is the child's go test -timeout. The shared wait gives up 30 s before it,
	// 20 s in. The child's setup is the connect alone, as main's row gave its window to the connect
	// alone: the parent starts the server, under its own deadline, before it runs the child. The
	// connect has 18 s of the 20: a dial begun later than nats.go's default timeout (2 s) before
	// the give-up gives its connection that timeout as its deadline rather than the give-up
	// (giveUpDialer.Dial), so a connect that returns that late fails the child naming it. The
	// publish and flush past the give-up then have the 30 s before go test's timeout panics.
	pastGiveUpDeadline = 50 * time.Second
)

// pastGiveUpDone is the line the child logs once its publish and its flush past the give-up have
// both succeeded.
var pastGiveUpDone = regexp.MustCompile(`published and flushed \S+ past the shared wait's give-up`)

// A connection a wait made keeps working once the shared wait's give-up has passed: the give-up
// the dial sets as the connection's deadline (giveUpDial) bounds the connect alone, as nats.go's
// processConnectInit sets its own deadline over it and clears both before the connect returns.
// Left on the connection, that deadline would end the read of every connection a test made through
// Connect at the give-up, the last 30 s of every test binary: nats.go closes a connection with no
// reconnect on that read's i/o timeout, and every later publish and flush on it fails. Main's
// TestAConnectionAWaitMadeKeepsWorkingPastTheWaitsEnd held the same of main's wait. The row runs as
// its own child, whose short deadline brings the give-up within its reach. The parent starts the
// server and hands the child its URL, so a server whose start is slow on a loaded machine costs the
// parent time and not the child's 18 s, which the connect alone spends.
//
// The parent waits only for the child, which go test's own alarm ends at pastGiveUpDeadline at the
// latest. In the child, nothing the row picks bounds the publish or the flush: the publish appends
// its few bytes to the connection's buffer and writes nothing itself, and the flush waits for the
// server's answer at most nats.go's own 10 s (Conn.Flush, nats.go v1.55.0 nats.go:6162-6164),
// inside the 30 s left before the child's alarm. The writes of nats.go's flusher are a few bytes
// to a socket the server's relay in the parent reads, which runs while the parent waits, so none
// of them waits.
func TestAConnectionAWaitMadeKeepsWorkingPastTheGiveUp(t *testing.T) {
	if uri := os.Getenv(pastGiveUpChildEnv); uri != "" {
		publishPastTheGiveUp(t, uri)
		return
	}
	_, uri := Start(t)
	child := exec.Command(os.Args[0], "-test.run", "^"+t.Name()+"$", "-test.count=1", "-test.v", "-test.timeout="+pastGiveUpDeadline.String())
	child.Env = append(os.Environ(), pastGiveUpChildEnv+"="+uri)
	output, err := child.CombinedOutput()
	if err != nil || !pastGiveUpDone.Match(output) {
		t.Fatalf("the child's connection did not publish and flush past the shared wait's give-up, before its %s deadline (%v):\n%s", pastGiveUpDeadline, err, output)
	}
}

// publishPastTheGiveUp is the child of TestAConnectionAWaitMadeKeepsWorkingPastTheGiveUp: it
// connects through Connect to the real server the parent started at uri, waits for the shared
// wait's give-up to pass, then publishes and flushes on the connection, and fails unless both
// succeed.
func publishPastTheGiveUp(t *testing.T, uri string) {
	end, bounded := testwait.GiveUpAt(t)
	if !bounded {
		t.Fatal("the child runs with no deadline: no wait of its gives up, and its dial gives its connection no deadline")
	}
	conn := Connect(t, uri)
	t.Cleanup(conn.Close)
	// Every dial of the connect began before the connect returned, and a dial begun more than
	// nats.go's default timeout before the give-up gives its connection the give-up as its deadline
	// (giveUpDialer.Dial): this connection's deadline was the give-up.
	if connected := time.Now(); connected.Add(natsgo.DefaultTimeout).After(end) {
		t.Fatalf("the connect returned at %s, less than nats.go's default timeout (%s) before the shared wait's give-up at %s: its dial may have given its connection that timeout as its deadline rather than the give-up, so the child's connect took too long for the row to hold anything", connected.Format(time.RFC3339Nano), natsgo.DefaultTimeout, end.Format(time.RFC3339Nano))
	}
	testwait.Eventually(giveUpPassed{t}, "the shared wait's give-up to pass", func() bool { return !time.Now().Before(end) })
	if err := conn.Publish("past.the.give.up", []byte("x")); err != nil {
		t.Fatalf("publish %s past the shared wait's give-up: %v (the connection's last error: %v)", time.Since(end), err, conn.LastError())
	}
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush %s past the shared wait's give-up: %v (the connection's last error: %v)", time.Since(end), err, conn.LastError())
	}
	t.Logf("published and flushed %s past the shared wait's give-up", time.Since(end))
}

// giveUpPassed is t for the wait on the give-up's passing, with that wait's own give-up taken as
// the fact it waited for rather than failing the test. The fact comes at the very moment the wait
// gives up, so an ask that read the clock just short of that moment ends the wait in its give-up,
// and a wait gives up only once it has read the clock past that moment (testwait's asked). Its
// Deadline is t's, so the wait gives up at the moment it waits for.
type giveUpPassed struct{ *testing.T }

func (g giveUpPassed) Fatalf(format string, args ...any) {
	g.Logf("the wait on the give-up's passing gave up, which it does only once the give-up has passed: %s", fmt.Sprintf(format, args...))
}
