package testnats

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/testwait"
)

// HoldWriteLock returns holding nats.go's connection lock, which the lock's own state shows: a try
// to take it for reading fails. A publish begun under the hold returns once the release lets the
// lock go and reaches a subscriber, on a connection that stayed connected throughout. No reading
// of how long a call takes decides any of it. The negative controls: a HoldWriteLock that returns
// without the lock fails the try; a release that does not let the lock go fails the publish's
// return.
func TestHoldWriteLockReturnsHoldingTheConnectionLock(t *testing.T) {
	uri := URL(t)
	hold := StartHandshakeHold(t, uri)
	conn, disconnected, _ := proxiedClient(t, hold.Proxy)
	direct := Connect(t, uri)
	t.Cleanup(direct.Close)
	received := make(chan *natsgo.Msg, 1)
	if _, err := direct.ChanSubscribe("testnats.held", received); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := direct.FlushWithContext(Context(t)); err != nil {
		t.Fatalf("flush the subscription: %v", err)
	}

	release := hold.HoldWriteLock(t, conn)
	lock := connLock(t, conn)
	if lock.TryRLock() {
		lock.RUnlock()
		t.Fatal("HoldWriteLock returned with nats.go's connection lock free: a read of it was taken at once")
	}
	published := make(chan error, 1)
	go func() { published <- conn.Publish("testnats.held", []byte("held")) }()
	release()
	if err := testwait.Receive(t, "the publish begun under the hold to return once the release let the lock go", published); err != nil {
		t.Fatalf("the publish begun under the hold answered %v once the release let the lock go, want nil", err)
	}
	if msg := testwait.Receive(t, "the publish begun under the hold at a subscriber", received); string(msg.Data) != "held" {
		t.Fatalf("the subscriber received %q, want the publish begun under the hold", msg.Data)
	}
	select {
	case <-disconnected:
		t.Fatal("the client was disconnected by the hold, which keeps its connection open")
	default:
	}
}

// HoldStalledWrite holds nats.go's connection lock with a write of nats.go's own, which a close of
// the connection's socket ends: once the row closes the socket the client dialed, with the proxy
// still frozen, nats.go lets the lock go and a read of the connection's status answers. The control
// is HoldWriteLock on the same rig: its lock is the row's own, so the read still waits after the
// close, which shows that the rig tells the two apart. The row connects through the hold as
// Connect does, through the shared wait, which makes a connect that failed again, each dial
// carrying the wait's give-up as its socket's deadline (socketDialer, giveUpDialer).
func TestHoldStalledWriteHoldsTheLockWithAWriteTheSocketsCloseEnds(t *testing.T) {
	for _, stalled := range []bool{true, false} {
		name := "HoldStalledWrite"
		if !stalled {
			name = "HoldWriteLock, the control"
		}
		t.Run(name, func(t *testing.T) {
			hold := StartHandshakeHold(t, URL(t))
			dialer := &socketDialer{giveUpDialer: giveUpDialer{t: t}}
			dial := testwait.Until(t, fmt.Sprintf("a connection through the hold at %s", hold.URL()), func() (dialAttempt, bool) {
				conn, err := natsgo.Connect(hold.URL(), natsgo.SetCustomDialer(dialer), natsgo.NoReconnect())
				return dialAttempt{conn: conn, err: err}, err == nil
			})
			conn := dial.conn
			t.Cleanup(conn.Close)
			socket := dialer.last.Load()
			if socket == nil {
				t.Fatal("control: the client dialed no socket through the row's dialer")
			}
			var release func()
			if stalled {
				release = hold.HoldStalledWrite(t, conn)
			} else {
				release = hold.HoldWriteLock(t, conn)
			}
			t.Cleanup(release)
			RequireLockHeld(t, conn)
			_ = (*socket).Close()
			read := make(chan struct{})
			go func() {
				conn.Status()
				close(read)
			}()
			if stalled {
				testwait.Receive(t, "a read of the status once the socket under HoldStalledWrite's write closed", read)
				return
			}
			// The control's absence: a read that waits on a lock the row holds says so by nothing,
			// nats.go's lock has no observer the test could read, and no later action is ordered
			// after the read; the span is main's.
			testwait.Quiet(t, "a read of the status while HoldWriteLock holds the lock after the socket closed", read, 10*time.Second,
				"a read blocked on nats.go's connection lock changes no state the test can read, the lock has no observer, and no later action is ordered after the read")
		})
	}
}

// socketDialer dials as giveUpDial's dialer does (giveUpDialer), so the socket carries the shared
// wait's give-up as its deadline until the connect returns, and keeps the last socket it handed
// out: once the row's wait has ended, the socket of the connect that succeeded, the last it made.
type socketDialer struct {
	giveUpDialer
	last atomic.Pointer[net.Conn]
}

func (d *socketDialer) Dial(network, address string) (net.Conn, error) {
	conn, err := d.giveUpDialer.Dial(network, address)
	if err == nil {
		d.last.Store(&conn)
	}
	return conn, err
}

// holdChildEnv tells the re-executed test binary to run TestTakeWriteLockOnAHooksGoroutineChild,
// which skips in an ordinary run, and holds the URL of the shared server the parent started, which
// the child's handshake hold relays to.
const holdChildEnv = "TESTNATS_HOLD_CHILD"

// hookWentOn is the line the child logs once a TakeWriteLock called on a goroutine of its own has
// returned there.
const hookWentOn = "the hook's goroutine went on after TakeWriteLock"

// TakeWriteLock, the take a hand-back row's hook makes on a goroutine other than the test's,
// returns there with the shared wait's give-up as its error when another holder keeps the lock to
// the moment the wait gives up, so the hook goes on and the test goroutine fails the row by name. A
// t.Fatal there would end the hook's goroutine rather than the test, and the child would log that
// TakeWriteLock never returned. The child runs under -timeout 45s, so its wait gives up about 15 s
// in, and its setup (StartHandshakeHold, proxiedClient) has those 15 s, as main's had: main's child
// ran under the same 45 s, and its first row's take of the lock (takeConnLock) failed that row with
// testwait.Left's "deadline leaves" text, not the guard's text the parent wants, once the setup had
// left the binary's deadline connLockSpare (30 s) or less away, 15 s in, though main's own waits of
// that setup ended only halfway to the deadline, 22.5 s in. The parent starts the shared server
// (URL) and passes its URL to the child, so a server whose start is slow on a loaded machine costs
// the parent time and not the child's 15 s; main's child started it within them.
func TestTakeWriteLockOnAHooksGoroutineFailsByNameAndReturns(t *testing.T) {
	uri := URL(t)
	child := exec.Command(os.Args[0], "-test.run=^TestTakeWriteLockOnAHooksGoroutineChild$", "-test.timeout=45s", "-test.v")
	child.Env = append(os.Environ(), holdChildEnv+"="+uri)
	output, err := child.CombinedOutput()
	text := string(output)
	if err == nil {
		t.Fatalf("the child was meant to fail its row; it passed:\n%s", text)
	}
	if strings.Contains(text, "panic:") {
		t.Fatalf("the child panicked rather than failing its row by name:\n%s", text)
	}
	for _, want := range []string{
		"testwait: testnats: gave up waiting for nats.go to let its connection lock go, so the fixture can take it as a write does 30s before the test binary's deadline",
		"--- FAIL: TestTakeWriteLockOnAHooksGoroutineChild (",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the child's output does not say %q:\n%s", want, text)
		}
	}
	if went := strings.Count(text, hookWentOn); went != 1 {
		t.Errorf("the child logged %q %d time(s), want once:\n%s", hookWentOn, went, text)
	}
}

// TestTakeWriteLockOnAHooksGoroutineChild is the child of
// TestTakeWriteLockOnAHooksGoroutineFailsByNameAndReturns. It holds the connection's lock itself, so
// the TakeWriteLock its hook calls waits until the shared wait gives up. It runs only when the
// parent re-executes this binary.
func TestTakeWriteLockOnAHooksGoroutineChild(t *testing.T) {
	uri := os.Getenv(holdChildEnv)
	if uri == "" {
		t.Skip(holdChildEnv + " is not set: this test is the child of TestTakeWriteLockOnAHooksGoroutineFailsByNameAndReturns")
	}
	hold := StartHandshakeHold(t, uri)
	conn, _, _ := proxiedClient(t, hold.Proxy)
	lock := connLock(t, conn)
	lock.Lock()
	t.Cleanup(lock.Unlock)
	taken, ended := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(ended)
		release, err := hold.TakeWriteLock(conn)
		if release != nil {
			release()
		}
		taken <- err
	}()
	<-ended
	select {
	case err := <-taken:
		t.Log(hookWentOn)
		if err != nil {
			t.Error(err)
		}
	default:
		t.Log("TakeWriteLock ended the hook's goroutine without returning")
	}
}
