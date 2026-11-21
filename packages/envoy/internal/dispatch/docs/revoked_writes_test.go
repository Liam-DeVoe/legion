package docs

import (
	"bytes"
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

// A revoked connection lets out the rest of the message going out when the revoke came and the
// close frame, and nothing else, whatever the writes split: before the revoke a frame whose
// header two writes carry goes out whole; a frame of the message in flight whose header the
// revoke split goes out; a room update split over two writes, a message begun after the revoke
// and a ping are dropped whole; and the close frame goes out. Every write is answered as written.
// The same writes with no revoke all go out, as they are.
func TestARevokedConnectionLetsOutOnlyTheMessageGoingOutAndTheClose(t *testing.T) {
	frame := func(opcode byte, final bool, payload []byte) []byte {
		return append(frameHeader(opcode, final, int64(len(payload)), nil), payload...)
	}
	upgrade := []byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n\r\n")
	before := frame(gws.BinaryMessage, true, []byte("a frame before the revoke"))
	first := frame(gws.BinaryMessage, false, bytes.Repeat([]byte("a"), 300))
	middle := frame(continuationFrame, false, bytes.Repeat([]byte("b"), 70000))
	last := frame(continuationFrame, true, []byte("c"))
	update := frame(gws.BinaryMessage, true, []byte("a room update after the revoke"))
	begun := frame(gws.BinaryMessage, false, []byte("a message begun after the revoke"))
	begunEnd := frame(continuationFrame, true, []byte("its end"))
	ping := frame(gws.PingMessage, true, nil)
	closing := frame(gws.CloseMessage, true, binary.BigEndian.AppendUint16(nil, uint16(contracts.DocumentAdmissionRevokedCloseCode)))

	for _, c := range []struct {
		name   string
		revoke bool
		want   [][]byte
	}{
		{"revoked as the message's middle frame starts", true, [][]byte{upgrade, before, first, middle, last, closing}},
		{"never revoked", false, [][]byte{upgrade, before, first, middle, update, last, begun, begunEnd, ping, closing}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var wire bytes.Buffer
			writes := &revokedWrites{}
			conn := &revokedWritesConn{Conn: wireConn{wire: &wire}, writes: writes}
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
			// The middle frame's header is ten bytes; the revoke comes after the first five.
			write(middle[:5])
			if c.revoke {
				writes.revoke()
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
			frames, revoked := writes.framesWrittenBeforeRevoke()
			if revoked != c.revoke || (c.revoke && frames != 2) {
				t.Fatalf("framesWrittenBeforeRevoke = %d, %t; want 2 frames let out before a revoke (the frame before it and the message's first), revoked %t",
					frames, revoked, c.revoke)
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
