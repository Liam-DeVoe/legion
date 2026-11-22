package api

import (
	"sync"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"

	"github.com/sjawhar/envoy/internal/clock"
	"github.com/sjawhar/envoy/internal/clock/clocktest"
	"github.com/sjawhar/envoy/internal/testwait"
)

// heldFiring is a socket's read-deadline clock: a fake on which the function of an AfterFunc, once
// the fake fires it, waits before it runs until the row lets it go (let). It stretches the span
// between a real timer's firing and the start of its function's goroutine, which the scheduler
// decides, so the row can move the deadline on inside it. fired closes once the first firing
// waits.
type heldFiring struct {
	*clocktest.Fake
	fired, released chan struct{}
	fire, release   sync.Once
}

func newHeldFiring(t *testing.T) *heldFiring {
	deadlines := &heldFiring{Fake: clocktest.New(drainStart), fired: make(chan struct{}), released: make(chan struct{})}
	// A row that fails while a firing waits lets it go, so the move that fired it returns.
	t.Cleanup(deadlines.let)
	return deadlines
}

func (c *heldFiring) AfterFunc(d time.Duration, f func()) clock.Timer {
	return c.Fake.AfterFunc(d, func() {
		c.fire.Do(func() { close(c.fired) })
		<-c.released
		f()
	})
}

// fireHeld moves the fake to at on a goroutine of its own, since a move waits for the function of
// each timer it fires to return, and waits until the firing of the timer due there waits. It
// answers a channel that closes once the move has returned, and that function with it.
func (c *heldFiring) fireHeld(t *testing.T, at time.Time) <-chan struct{} {
	t.Helper()
	moved := make(chan struct{})
	go func() {
		defer close(moved)
		c.Set(at)
	}()
	testwait.Receive(t, "the read deadline's timer to fire at "+at.Format(time.RFC3339Nano)+" and its function to wait", c.fired)
	return moved
}

func (c *heldFiring) let() { c.release.Do(func() { close(c.released) }) }

// The read-deadline timer's function runs on a goroutine of its own once the timer fires, so an
// awaitPong or a drain can move the deadline on between the firing and the function's turn, and
// Reset stops no firing already made. Such a firing ends no reads once its function runs
// (readsEndPassed). Had it ended them whatever deadline the socket then held, it would cut short
// the drain of a frame past the read limit, bringing back the reset that overtakes the 1009 the
// drain exists to stop, and it would cut a socket whose page's pong had just moved its pong
// deadline on.
//
// The read deadlines run on a fake whose firing waits before its function runs until the row lets
// it go (heldFiring), and the pings on a fake no one moves, so no ping draws a pong the row did not
// send (countedRig). Each case moves the fake to the pong deadline the upgrade armed, so its timer
// fires and its function waits; it then moves the deadline on and waits until the new one is
// armed, and only then lets the firing go and waits for its function to return:
//   - the drain: the page sends a frame past the read limit, the socket closes with 1009, and the
//     drain arms its own deadline, liveSocketCloseWait on, and reads what the page sends next; the
//     page then sends the rest of the frame and its FIN, and the server must have read every byte
//     the page sent when it closes the connection;
//   - the pong: the page sends a pong, which arms the pong deadline two heartbeats on; the socket
//     must then still answer the page's next frame.
//
// Nothing in the row reads the wall clock but gorilla's own deadline on the drain's 1009 write
// (sendOverTheLimit).
func TestAReadDeadlineFiringThatAMoveOvertookEndsNoReads(t *testing.T) {
	pongBy := drainStart.Add(2 * drainHeartbeat)
	t.Run("the drain", func(t *testing.T) {
		deadlines := newHeldFiring(t)
		rig := newCountedRig(t, deadlines)
		socket := rig.openCounted(t)
		awaitArmedAt(t, deadlines.Fake, "the pong deadline the upgrade armed", pongBy)
		moved := deadlines.fireHeld(t, pongBy)

		socket.sendOverTheLimit(t)
		awaitArmedAt(t, deadlines.Fake, "the drain's own deadline, liveSocketCloseWait on", pongBy.Add(liveSocketCloseWait))
		socket.drainReading(t)
		deadlines.let()
		testwait.Receive(t, "the function of the pong deadline's firing, overtaken by the drain, to return", moved)
		socket.endFrame(t, "the pong deadline's firing, overtaken by the drain, ended the drain before the frame's end")
		rig.sockets(t, 0)
	})
	t.Run("the pong", func(t *testing.T) {
		deadlines := newHeldFiring(t)
		rig := newCountedRig(t, deadlines)
		client := rig.openCounted(t).client
		awaitArmedAt(t, deadlines.Fake, "the pong deadline the upgrade armed", pongBy)
		moved := deadlines.fireHeld(t, pongBy)

		// The pong's write ends where the shared wait gives up (none under -timeout 0).
		writeBy, _ := testwait.Context(t).Deadline()
		if err := client.conn.WriteControl(gws.PongMessage, nil, writeBy); err != nil {
			t.Fatalf("send a pong: %v", err)
		}
		awaitArmedAt(t, deadlines.Fake, "the pong deadline the page's pong armed, two heartbeats on", pongBy.Add(2*drainHeartbeat))
		deadlines.let()
		testwait.Receive(t, "the function of the pong deadline's firing, overtaken by the pong, to return", moved)
		client.send(t, LiveUnsubscribe{Type: "unsubscribe"})
		frame := client.next(t, "the socket's answer to the page's next frame: the pong deadline's firing, overtaken by the pong, must not have cut it")
		if refusal, ok := frame.(LiveError); !ok || refusal.Code != "INVALID_FRAME" {
			t.Fatalf("the socket answered %+v to an unsubscribe without a sub, want INVALID_FRAME", frame)
		}
		rig.sockets(t, 1)
	})
}
