package docs

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"testing"

	gws "github.com/gorilla/websocket"

	"github.com/sjawhar/envoy/internal/contracts"
)

// wireConn is a connection whose writes land on wire, in order.
type wireConn struct {
	net.Conn
	wire *bytes.Buffer
}

func (c wireConn) Write(p []byte) (int, error) { return c.wire.Write(p) }

// opaqueContext is a context the standard library cannot see into: it hides every value, so a
// child of it learns of its end only from a goroutine of its own (context.WithCancelCause), not in
// its cancel.
type opaqueContext struct{ context.Context }

func (opaqueContext) Value(any) any { return nil }

// An ended socket's connection lets out the rest of the message going out when the end came and
// the close frame, and nothing else, whatever the writes split: before the end a frame whose
// header two writes carry goes out whole; a frame of the message in flight whose header the end
// splits goes out; a room update split over two writes, a message begun after the end and a ping
// are dropped whole; and the close frame goes out. Every write is answered as written, and a
// message the page begins after the end is refused. The end is a revoke's, which cancels the
// socket's own context, or the API's, which cancels the request's: here through a context the
// standard library cannot see into, so the socket's own context learns of it only later, and the
// connection must read the request's itself. The same writes with no end all go out.
func TestAnEndedSocketsConnectionLetsOutOnlyTheMessageGoingOutAndTheClose(t *testing.T) {
	frame := func(opcode byte, final bool, payload []byte) []byte {
		return append(frameHeader(opcode, final, int64(len(payload)), nil), payload...)
	}
	upgrade := []byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n\r\n")
	before := frame(gws.BinaryMessage, true, []byte("a frame before the end"))
	first := frame(gws.BinaryMessage, false, bytes.Repeat([]byte("a"), 300))
	middle := frame(continuationFrame, false, bytes.Repeat([]byte("b"), 70000))
	last := frame(continuationFrame, true, []byte("c"))
	update := frame(gws.BinaryMessage, true, []byte("a room update after the end"))
	begun := frame(gws.BinaryMessage, false, []byte("a message begun after the end"))
	begunEnd := frame(continuationFrame, true, []byte("its end"))
	ping := frame(gws.PingMessage, true, nil)
	closing := frame(gws.CloseMessage, true, binary.BigEndian.AppendUint16(nil, uint16(contracts.DocumentAdmissionRevokedCloseCode)))

	for _, c := range []struct {
		name string
		// end ends the socket, given the cancels of its own context and of the request's.
		end  func(own, request context.CancelCauseFunc)
		want [][]byte
	}{
		{"a revoke, which cancels the socket's own context", func(own, _ context.CancelCauseFunc) { own(errSchemaRevoked) },
			[][]byte{upgrade, before, first, middle, last, closing}},
		{"the API's end of the request, through a context the standard library cannot see into", func(_, request context.CancelCauseFunc) { request(context.Canceled) },
			[][]byte{upgrade, before, first, middle, last, closing}},
		{"no end", nil, [][]byte{upgrade, before, first, middle, update, last, begun, begunEnd, ping, closing}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var wire bytes.Buffer
			base, endRequest := context.WithCancelCause(context.Background())
			defer endRequest(nil)
			request := opaqueContext{base}
			own, endOwn := context.WithCancelCause(request)
			defer endOwn(nil)
			end := newSocketEnd(own, request)
			conn := &socketEndConn{Conn: wireConn{wire: &wire}, end: end}
			write := func(p []byte) {
				t.Helper()
				if n, err := conn.Write(p); n != len(p) || err != nil {
					t.Fatalf("a write of %d bytes answered %d, %v; want every byte written", len(p), n, err)
				}
			}
			write(upgrade)
			write(before[:1])
			write(before[1:])
			write(first)
			// The middle frame's header is ten bytes; the end comes after the first five.
			write(middle[:5])
			if end.refusesMessage() {
				t.Fatal("the connection refuses the page's messages before the end")
			}
			if c.end != nil {
				c.end(endOwn, endRequest)
			}
			write(middle[5:])
			write(update[:3])
			write(update[3:])
			write(last)
			write(begun)
			write(begunEnd)
			write(ping)
			write(closing)
			if want := bytes.Join(c.want, nil); !bytes.Equal(wire.Bytes(), want) {
				t.Fatalf("the connection let out %d bytes, want %d: %s", wire.Len(), len(want), describeWire(wire.Bytes(), want))
			}
			frames, ended := end.framesWrittenBeforeEnd()
			if ended != (c.end != nil) || (ended && frames != 2) {
				t.Fatalf("framesWrittenBeforeEnd = %d, %t; want 2 frames let out before an end (the frame before it and the message's first), ended %t",
					frames, ended, c.end != nil)
			}
			if refuses := end.refusesMessage(); refuses != (c.end != nil) {
				t.Fatalf("after the writes the connection refuses the page's messages = %t, want %t", refuses, c.end != nil)
			}
		})
	}
}

// describeWire says where got first differs from want.
func describeWire(got, want []byte) string {
	at := 0
	for at < min(len(got), len(want)) && got[at] == want[at] {
		at++
	}
	return fmt.Sprintf("they differ from byte %d", at)
}
