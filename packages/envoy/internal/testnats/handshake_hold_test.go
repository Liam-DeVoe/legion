package testnats

import (
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/testwait"
)

// HoldWriteLock returns holding nats.go's connection lock, which the lock's own state shows: a try
// to take it for reading fails. A publish begun under the hold returns once the release lets the
// lock go and reaches a subscriber, on a connection that stayed connected throughout. No reading
// of how long a call takes decides any of it. The negative controls: a HoldWriteLock that freezes
// the proxy and returns without the lock fails the try; a release that does not let the lock go
// fails the publish's return.
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
	lock, found := connLock(t, conn)
	if !found {
		t.FailNow()
	}
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

// holdChildEnv tells the re-executed test binary to run TestHoldWriteLockOnAHooksGoroutineChild,
// which skips in an ordinary run.
const holdChildEnv = "TESTNATS_HOLD_CHILD"

// hookWentOn is the line the child logs once a HoldWriteLock called on a goroutine of its own has
// returned there.
const hookWentOn = "the hook's goroutine went on after HoldWriteLock"

// HoldWriteLock called on a goroutine other than the test's, as the hand-back rows' hooks call it,
// fails the test by name and returns there when it cannot take the lock, so the hook goes on: once
// when its guard passes while another holder keeps the lock, and once when the binary's deadline is
// already no more than the guard's spare away. A t.Fatal there would end the hook's goroutine
// rather than the test, and the child would log that HoldWriteLock never returned. Neither failure
// depends on how long the child takes to reach its row. The child's -timeout is what the parent's
// own deadline leaves less connLockSpare, so the first row's take, which leaves connLockSpare of the
// child's deadline, has minutes in an ordinary run, and its guard is one that has already passed
// (passedGuard). The second row's take leaves all but a millisecond of the child's -timeout, so it
// finds the deadline spent whenever it runs. The child ran under -timeout 45s before, which left
// its start 15 s to reach the first row's take: a child stalled longer than that on a loaded
// machine failed that row on its spent deadline instead of its guard (INFRA-3904).
func TestHoldWriteLockOnAHooksGoroutineFailsByNameAndReturns(t *testing.T) {
	timeout := testwait.Remaining(t, connLockSpare)
	child := exec.Command(os.Args[0], "-test.run=^TestHoldWriteLockOnAHooksGoroutineChild$", "-test.timeout="+timeout.String(), "-test.v")
	child.Env = append(os.Environ(), holdChildEnv+"=1")
	output, err := child.CombinedOutput()
	text := string(output)
	if err == nil {
		t.Fatalf("the child was meant to fail both rows; it passed:\n%s", text)
	}
	if strings.Contains(text, "panic:") {
		t.Fatalf("the child panicked rather than failing its rows by name:\n%s", text)
	}
	for _, want := range []string{
		"another nats.go call still held the connection lock 30s before the test binary's deadline: nothing let it go",
		"--- FAIL: TestHoldWriteLockOnAHooksGoroutineChild/the_guard_passes (",
		"testwait: TestHoldWriteLockOnAHooksGoroutineChild/the_deadline_is_spent: the test binary's deadline leaves ",
		"--- FAIL: TestHoldWriteLockOnAHooksGoroutineChild/the_deadline_is_spent (",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the child's output does not say %q:\n%s", want, text)
		}
	}
	if went := strings.Count(text, hookWentOn); went != 2 {
		t.Errorf("the child logged %q %d time(s), want once for each row:\n%s", hookWentOn, went, text)
	}
}

// TestHoldWriteLockOnAHooksGoroutineChild is the child of
// TestHoldWriteLockOnAHooksGoroutineFailsByNameAndReturns. Its first row holds the connection's lock
// itself and gives its hook's take a guard that has already passed, so the take finds the lock held
// when the guard passes. Its second row gives its take a spare of all but a millisecond of the
// binary's -timeout, so the take finds the deadline spent. It runs only when the parent re-executes
// this binary.
func TestHoldWriteLockOnAHooksGoroutineChild(t *testing.T) {
	if os.Getenv(holdChildEnv) == "" {
		t.Skip(holdChildEnv + " is not set: this test is the child of TestHoldWriteLockOnAHooksGoroutineFailsByNameAndReturns")
	}
	uri := URL(t)
	t.Run("the guard passes", func(t *testing.T) {
		hold := StartHandshakeHold(t, uri)
		hold.lockGuard.after = passedGuard
		conn, _, _ := proxiedClient(t, hold.Proxy)
		lock, found := connLock(t, conn)
		if !found {
			t.FailNow()
		}
		lock.Lock()
		t.Cleanup(lock.Unlock)
		holdOnAHooksGoroutine(t, hold, conn)
	})
	t.Run("the deadline is spent", func(t *testing.T) {
		hold := StartHandshakeHold(t, uri)
		hold.lockGuard.spare = binaryTimeout(t) - time.Millisecond
		conn, _, _ := proxiedClient(t, hold.Proxy)
		holdOnAHooksGoroutine(t, hold, conn)
	})
}

// passedGuard is a connLockGuard.after whose guard has passed before the take begins to wait.
func passedGuard(time.Duration) (<-chan time.Time, func() bool) {
	fired := make(chan time.Time, 1)
	fired <- time.Now()
	return fired, func() bool { return false }
}

// binaryTimeout is the -timeout the test binary began its tests under, failing t when it cannot be
// read: a spare of all but a millisecond of it is more than the deadline leaves once the binary has
// run any test for a millisecond, and less than the -timeout itself, so testwait.Left says the
// deadline was spent rather than that the -timeout is too short.
func binaryTimeout(t *testing.T) time.Duration {
	t.Helper()
	registered := flag.Lookup("test.timeout")
	if registered == nil {
		t.Fatal("the test binary has no -test.timeout flag")
	}
	timeout, ok := registered.Value.(flag.Getter).Get().(time.Duration)
	if !ok || timeout <= time.Millisecond {
		t.Fatalf("the test binary's -test.timeout reads %v, want a duration over a millisecond", registered.Value)
	}
	return timeout
}

// holdOnAHooksGoroutine calls HoldWriteLock on a goroutine of its own, as a hook does, waits for
// that goroutine to end, and logs hookWentOn when HoldWriteLock returned on it rather than ending it.
func holdOnAHooksGoroutine(t *testing.T, hold *HandshakeHold, conn *natsgo.Conn) {
	t.Helper()
	returned, ended := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(ended)
		hold.HoldWriteLock(t, conn)
		close(returned)
	}()
	<-ended
	select {
	case <-returned:
		t.Log(hookWentOn)
	default:
		t.Log("HoldWriteLock ended the hook's goroutine without returning")
	}
}
