package docs

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"net/http"
	"sync"

	gws "github.com/gorilla/websocket"
)

// A document socket ends with the admission-revoked close when its request context is cancelled
// with a cause that names that code: a re-check's revoke (revokeUnless), or the heartbeat's end
// of the credential the socket was opened with (the API's whileCallerResolves, errCredentialExpired).
// From that moment its page is owed no more of the document. ygo sends the close from the socket's
// context watcher, a goroutine the cancel only wakes, and until that goroutine runs the room's
// broadcast goes on writing every update to the socket: with the watcher scheduled 30 ms late, an
// edit made after the revoke reached the revoked page (INFRA-5142). ygo offers no hook on what it
// writes to a peer, so the connection the websocket upgrade hijacks closes itself, where the
// awareness filter reads what the socket sends (awareness_limit.go): revokedWrites decides, frame
// by frame and under its own lock, what goes out, and reads the socket's context as it decides.
// Cancelling a context sets its error before the cancel returns, and a parent's cancel cancels its
// children before it returns, so every frame decided once the cancel has returned is decided as
// revoked: only the rest of the message already going out and the close frame go out. A frame that
// starts after the cancel and is neither never reaches the socket, however late the watcher runs.

// revokedWrites is what a document socket's connection writes once the socket's request context
// has ended: it reads the frames ygo writes, whole or split over writes, and decides each as its
// header completes. Before the end every frame goes out. After it, a frame goes out only if it is
// a close frame, or the next frame of a data message that went out before the end and has not
// ended - the message the close waits for, which gorilla's WriteControl never cuts into. Every
// other frame - a room update, an awareness message, a sync answer - is dropped whole, header and
// payload, and the write that carried it is answered as written, so gorilla and ygo go on to the
// close as they would have. The connection's first write is the upgrade's 101 answer, which the
// upgrader makes in one write before any frame (processHeaderConn reads the same), and goes out
// as it is.
type revokedWrites struct {
	// ctx is the socket's request context, the one ygo serves it on.
	ctx context.Context
	mu  sync.Mutex
	// revoked is set by the first decision that finds ctx ended.
	revoked bool
	// upgraded is set once the upgrade's answer has gone out; every byte after it is a frame.
	upgraded bool
	// header is the header of the frame being read, as far as the writes have carried it; its
	// bytes wait until it is whole and decided.
	header []byte
	// left is how many payload bytes of the frame in hand are still to come, and passing whether
	// they go out.
	left    int64
	passing bool
	// inMessage is set while a data message that went out has frames still to come.
	inMessage bool
	// frames counts the frames that went out, and framesBeforeRevoke is that count when ctx
	// ended: every frame counted before it was decided before the cancel, and every frame after
	// it is the end's to answer for, so a test reads that boundary here rather than off a clock
	// (framesWrittenBeforeRevoke).
	frames             int
	framesBeforeRevoke int
	// parts is decide's answer, reused from one write to the next: a connection takes one write
	// at a time (revokedWritesConn), and each is done with it before the next is decided.
	parts [][]byte
}

// noteEnd marks the writes revoked once ctx has ended, keeping the count of the frames let out
// before; w.mu is held. A decision that found ctx live was made before the cancel, so the frames
// counted when the end is first seen are exactly those.
func (w *revokedWrites) noteEnd() {
	if !w.revoked && w.ctx.Err() != nil {
		w.revoked = true
		w.framesBeforeRevoke = w.frames
	}
}

// framesWrittenBeforeRevoke is how many frames the connection had let out when the socket's
// context ended, and whether it has.
func (w *revokedWrites) framesWrittenBeforeRevoke() (int, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.noteEnd()
	return w.framesBeforeRevoke, w.revoked
}

// decide reads p, the next bytes ygo's connection writes, and returns what of them goes out:
// whole when every byte of p goes out as it is, else the parts that do, in order, which may
// start with header bytes an earlier write left waiting.
func (w *revokedWrites) decide(p []byte) ([][]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.upgraded {
		w.upgraded = true
		return nil, true
	}
	w.noteEnd()
	w.parts = w.parts[:0]
	whole := len(w.header) == 0
	// headerAt is where in p the header being read began, or -1 when an earlier write began it.
	headerAt := -1
	for i := 0; i < len(p); {
		if w.left > 0 {
			n := int(min(int64(len(p)-i), w.left))
			if w.passing {
				w.parts = append(w.parts, p[i:i+n])
			} else {
				whole = false
			}
			w.left -= int64(n)
			i += n
			continue
		}
		if len(w.header) == 0 {
			headerAt = i
		}
		need := 2
		if len(w.header) >= 2 {
			need = frameHeaderLength(w.header)
		}
		n := min(need-len(w.header), len(p)-i)
		w.header = append(w.header, p[i:i+n]...)
		i += n
		if len(w.header) < 2 || len(w.header) < frameHeaderLength(w.header) {
			continue
		}
		if w.passing = w.admits(w.header[0]); !w.passing {
			whole = false
		} else if w.frames++; headerAt >= 0 {
			w.parts = append(w.parts, p[headerAt:i])
		} else {
			w.parts = append(w.parts, bytes.Clone(w.header))
		}
		w.left = max(framePayloadLength(w.header), 0)
		w.header, headerAt = w.header[:0], -1
	}
	if len(w.header) > 0 {
		whole = false
	}
	return w.parts, whole
}

// admits decides the frame whose header begins with first, and keeps track of the data message
// the frames that go out are part of.
func (w *revokedWrites) admits(first byte) bool {
	opcode, final := first&0x0f, first&finalFrame != 0
	switch {
	case !w.revoked:
	case opcode == gws.CloseMessage:
		return true
	case opcode == continuationFrame && w.inMessage:
	default:
		return false
	}
	if opcode == continuationFrame || opcode == gws.TextMessage || opcode == gws.BinaryMessage {
		w.inMessage = !final
	}
	return true
}

// revokedWritesWriter is a document socket's response writer whose hijacked connection writes
// through writes (revokedWritesConn).
type revokedWritesWriter struct {
	http.ResponseWriter
	writes *revokedWrites
}

func (w *revokedWritesWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffers, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	return &revokedWritesConn{Conn: conn, writes: w.writes}, buffers, nil
}

// Unwrap gives http.ResponseController the writer underneath.
func (w *revokedWritesWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// revokedWritesConn is a document socket's connection under ygo's writes: each write puts on the
// connection underneath what writes lets out of it. ygo writes from one goroutine at a time and
// gorilla serialises that writer against WriteControl, so the frames arrive here in the order
// they go out.
type revokedWritesConn struct {
	net.Conn
	writes *revokedWrites
}

func (c *revokedWritesConn) Write(p []byte) (int, error) {
	parts, whole := c.writes.decide(p)
	if whole {
		return c.Conn.Write(p)
	}
	if len(parts) > 0 {
		buffers := net.Buffers(parts)
		if _, err := buffers.WriteTo(c.Conn); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
