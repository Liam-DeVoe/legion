package testnats

import (
	"os"
	"os/exec"
	"strings"
	"testing"

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
	if err := direct.Flush(); err != nil {
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

// holdChildEnv tells the re-executed test binary to run TestTakeWriteLockOnAHooksGoroutineChild,
// which skips in an ordinary run.
const holdChildEnv = "TESTNATS_HOLD_CHILD"

// hookWentOn is the line the child logs once a TakeWriteLock called on a goroutine of its own has
// returned there.
const hookWentOn = "the hook's goroutine went on after TakeWriteLock"

// TakeWriteLock, the take a hand-back row's hook makes on a goroutine other than the test's,
// returns there with the shared wait's give-up as its error when another holder keeps the lock to
// the moment the wait gives up, so the hook goes on and the test goroutine fails the row by name. A
// t.Fatal there would end the hook's goroutine rather than the test, and the child would log that
// TakeWriteLock never returned. The child runs under -timeout 45s, so its wait gives up about 15 s
// in.
func TestTakeWriteLockOnAHooksGoroutineFailsByNameAndReturns(t *testing.T) {
	child := exec.Command(os.Args[0], "-test.run=^TestTakeWriteLockOnAHooksGoroutineChild$", "-test.timeout=45s", "-test.v")
	child.Env = append(os.Environ(), holdChildEnv+"=1")
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
	if os.Getenv(holdChildEnv) == "" {
		t.Skip(holdChildEnv + " is not set: this test is the child of TestTakeWriteLockOnAHooksGoroutineFailsByNameAndReturns")
	}
	hold := StartHandshakeHold(t, URL(t))
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
