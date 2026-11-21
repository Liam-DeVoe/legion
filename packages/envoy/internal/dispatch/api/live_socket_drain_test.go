package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"

	"github.com/sjawhar/envoy/internal/clock/clocktest"
	"github.com/sjawhar/envoy/internal/testwait"
)

// countingConn counts the bytes read from and written to one connection, and closes closed once
// the connection is closed, with readAtClose the bytes read by then.
type countingConn struct {
	net.Conn
	read, written atomic.Int64
	closeOnce     sync.Once
	closed        chan struct{}
	readAtClose   int64
}

func newCountingConn(conn net.Conn) *countingConn {
	return &countingConn{Conn: conn, closed: make(chan struct{})}
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.read.Add(int64(n))
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written.Add(int64(n))
	return n, err
}

func (c *countingConn) Close() error {
	c.closeOnce.Do(func() {
		c.readAtClose = c.read.Load()
		close(c.closed)
	})
	return c.Conn.Close()
}

// countingListener hands each connection the server accepts on accepted, counted.
type countingListener struct {
	net.Listener
	accepted chan<- *countingConn
}

func (l countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return conn, err
	}
	counted := newCountingConn(conn)
	l.accepted <- counted
	return counted, nil
}

// A frame past the read limit closes the socket with 1009, and the socket then reads off the rest
// of the frame before it closes the connection, so the close is a FIN behind the 1009 and not a
// reset that can overtake it. The drain's own deadline, liveSocketCloseWait on the socket's
// read-deadline clock, replaces the pong deadline, so a pong deadline that falls due inside the
// drain does not cut it short.
//
// The read deadlines run on a fake of the row's own (liveSocketClock), on which the upgrade arms
// the pong deadline two heartbeats on, inside the second the drain waits; the pings run on a fake
// no one moves (liveSocketPingClock), so no pong moves that deadline. Once the drain is reading,
// the row moves the fake to the pong deadline. The page then sends the rest of the frame and its
// FIN, and the server must have read every byte the page sent when it closes the connection. A
// drain the pong deadline cut short closes it having read none of that rest. Nothing in the row
// reads the wall clock.
func TestAFrameOverTheReadLimitIsDrainedPastThePongDeadline(t *testing.T) {
	const heartbeat = 100 * time.Millisecond
	deadlines := clocktest.New(time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC))
	pongBy, drainBy := deadlines.Now().Add(2*heartbeat), deadlines.Now().Add(liveSocketCloseWait)
	accepted := make(chan *countingConn, 1)
	counted := func(listener net.Listener) net.Listener {
		return countingListener{Listener: listener, accepted: accepted}
	}
	options := testServerOptions{streamHeartbeat: heartbeat, liveSocketClock: deadlines, liveSocketPingClock: clocktest.New(deadlines.Now())}
	rig := newLiveSocketRig(t, options, counted)
	var page *countingConn
	dialer := &gws.Dialer{NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		page = newCountingConn(conn)
		return page, nil
	}, HandshakeTimeout: handshakeBound(t)}
	client := rig.open(t, dialer, asUser("alice"))
	server := testwait.Receive(t, "the server's side of the socket's connection", accepted)
	awaitArmedAt(t, deadlines, "the pong deadline the upgrade armed", pongBy)
	client.read()
	send := func(what string, data []byte) {
		t.Helper()
		if _, err := page.Write(data); err != nil {
			t.Fatalf("send %s: %v", what, err)
		}
	}
	chunk := bytes.Repeat([]byte("a"), 4096)

	// The header of a 9 MiB text frame, masked as a page's frames are, and the first of its bytes:
	// gorilla compares the declared length with the limit as it reads the header, and answers 1009.
	header := []byte{0x81, 0x80 | 127, 0, 0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4}
	binary.BigEndian.PutUint64(header[2:10], 9<<20)
	send("the frame's header", append(header, chunk...))
	if code, _ := client.closeCode(t); code != gws.CloseMessageTooBig {
		t.Fatalf("a 9 MiB frame closed the socket with %d, want 1009", code)
	}

	// The read loop has ended, so only the drain reads what the page sends from here: once the
	// server has read every byte the page sent, the drain is reading, and its deadline is set.
	send("more of the frame", chunk)
	testwait.Until(t, "the server to read every byte the page sent, the last of them in its drain", func() ([2]int64, bool) {
		read, written := server.read.Load(), page.written.Load()
		return [2]int64{read, written}, read == written
	})
	if armed := deadlines.Armed(); !slices.Equal(armed, []time.Time{drainBy}) {
		t.Errorf("the drain's read deadlines armed %v, want only the drain's own, liveSocketCloseWait on, at %s, in place of the pong deadline at %s",
			armed, drainBy.Format(time.RFC3339Nano), pongBy.Format(time.RFC3339Nano))
	}

	deadlines.Set(pongBy)
	// The rest goes out whatever the server did at the move: a drain cut short has closed the
	// connection, and the page's write then draws a reset, which the verdict below names.
	rest := bytes.Repeat([]byte("a"), 2*len(chunk))
	if _, err := page.Write(rest); err != nil {
		t.Logf("send the rest of the frame after the pong deadline: %v", err)
	}
	if err := page.Conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Logf("send the page's FIN: %v", err)
	}
	testwait.Receive(t, "the server's close of the connection once its drain ended", server.closed)
	if sent := page.written.Load(); server.readAtClose != sent {
		t.Errorf("the server closed the connection having read %d of the %d bytes the page sent: its drain ended at the pong deadline, before the frame's end",
			server.readAtClose, sent)
	}
	rig.sockets(t, 0)
}
