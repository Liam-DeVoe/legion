package testnats

import (
	"bufio"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ReplyHold relays a client's TCP connections to a NATS server and can hold the replies to the
// requests the client sends on a subject: the request reaches the server, which answers it, and the
// hold keeps that answer from the client, so the client waits on a reply that never comes, which
// is what it sees of a request whose reply the connection's drop lost. Drop then ends every
// connection the hold is relaying, as a network that ends a connection does; unlike Proxy.Cut, it
// keeps accepting, so the client reconnects through it at once, on a connection whose replies are
// no longer held unless Hold names them again. Every other protocol line is relayed as it is, so
// the connection is a real one on a real server.
type ReplyHold struct {
	listener net.Listener
	target   string
	url      string

	mu     sync.Mutex
	prefix string
	// replies are the reply subjects of the held requests, whose answers the hold drops.
	replies map[string]struct{}
	// held is closed once a reply has been held and the client's flush behind its request, if it
	// sent one, has been answered (holdSettled); nil while nothing is held.
	held  chan struct{}
	conns map[net.Conn]struct{}
}

// holdSettle is how long after holding a reply the hold waits for the server's next PONG before it
// reports the reply held anyway: a request that flushes after its publish (bus.Client's
// publishConfirmed) sent a PING the server answers right behind the reply, within microseconds,
// and the hold reports it held once that PONG has reached the client; a request that sends no
// flush draws no PONG, and nothing of it is still on the wire.
const holdSettle = 50 * time.Millisecond

// StartReplyHold relays to the NATS server at target, a client URL such as URL returns, on a
// loopback port of its own until the test ends. URL is the same server through the hold.
func StartReplyHold(t testing.TB, target string) *ReplyHold {
	t.Helper()
	server, err := url.Parse(target)
	if err != nil || server.Host == "" {
		t.Fatalf("read the NATS URL %q the hold relays to: %v", target, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the reply hold: %v", err)
	}
	relayed := *server
	relayed.Host = listener.Addr().String()
	h := &ReplyHold{listener: listener, target: server.Host, url: relayed.String(), conns: map[net.Conn]struct{}{}}
	t.Cleanup(h.close)
	go h.serve()
	return h
}

// URL is the NATS URL of the hold's address: the target's, credentials included, with the host and
// port the hold listens on.
func (h *ReplyHold) URL() string { return h.url }

// Hold holds the reply to every request the client sends from now on whose subject starts with
// prefix, until Drop, and returns a channel closed once the first of them is held with the
// client's flush behind it answered, so a test knows the request it waits on is past its publish
// and waiting for a reply that is not coming.
func (h *ReplyHold) Hold(prefix string) <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prefix = prefix
	h.replies = map[string]struct{}{}
	h.held = make(chan struct{})
	return h.held
}

// Drop holds nothing more and closes every connection the hold is relaying: each client through it
// is disconnected at once, and reconnects through it.
func (h *ReplyHold) Drop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prefix, h.replies, h.held = "", nil, nil
	for conn := range h.conns {
		_ = conn.Close()
		delete(h.conns, conn)
	}
}

// request notes the reply subject of a request the client sent on subject when its reply is to be
// held.
func (h *ReplyHold) request(subject, reply string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.replies != nil && reply != "" && strings.HasPrefix(subject, h.prefix) {
		h.replies[reply] = struct{}{}
	}
}

// holds reports whether the server's message on subject is a reply the hold drops, and returns the
// channel to close once it has settled.
func (h *ReplyHold) holds(subject string) (bool, chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.replies[subject]; !ok {
		return false, nil
	}
	return true, h.held
}

// settled closes held, the channel of the hold a reply was held for, once.
func (h *ReplyHold) settled(held chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-held:
	default:
		close(held)
	}
}

func (h *ReplyHold) serve() {
	for {
		client, err := h.listener.Accept()
		if err != nil {
			return
		}
		go h.relay(client)
	}
}

// relay joins client to the server, a protocol line at a time each way: what the client sends is
// relayed as it is, noting the replies of the requests to hold, and what the server sends is
// relayed but for each held reply and its payload.
func (h *ReplyHold) relay(client net.Conn) {
	server, err := net.Dial("tcp", h.target)
	if err != nil {
		_ = client.Close()
		return
	}
	h.mu.Lock()
	h.conns[client], h.conns[server] = struct{}{}, struct{}{}
	h.mu.Unlock()
	defer func() {
		_ = client.Close()
		_ = server.Close()
		h.mu.Lock()
		delete(h.conns, client)
		delete(h.conns, server)
		h.mu.Unlock()
	}()
	go h.down(server, client)
	lines := bufio.NewReader(client)
	for {
		line, payload, fields, err := readMessage(lines, "PUB", "HPUB")
		if err != nil {
			return
		}
		// `PUB <subject> <reply> <bytes>` and `HPUB <subject> <reply> <header bytes> <bytes>`.
		if (strings.EqualFold(fields[0], "PUB") && len(fields) == 4) || (strings.EqualFold(fields[0], "HPUB") && len(fields) == 5) {
			h.request(fields[1], fields[2])
		}
		if _, err := server.Write(append(line, payload...)); err != nil {
			return
		}
	}
}

// down relays what the server sends to client, leaving out each held reply: once one is left out,
// the hold reports it held at the next PONG the client is sent, or after holdSettle when none
// comes.
func (h *ReplyHold) down(server, client net.Conn) {
	defer func() {
		_ = client.Close()
		_ = server.Close()
	}()
	lines := bufio.NewReader(server)
	var pending chan struct{}
	var settle *time.Timer
	defer func() {
		if settle != nil {
			settle.Stop()
		}
	}()
	for {
		line, payload, fields, err := readMessage(lines, "MSG", "HMSG")
		if err != nil {
			return
		}
		if len(fields) >= 4 && (strings.EqualFold(fields[0], "MSG") || strings.EqualFold(fields[0], "HMSG")) {
			if held, settled := h.holds(fields[1]); held {
				if settled != nil && pending == nil {
					pending = settled
					settle = time.AfterFunc(holdSettle, func() { h.settled(settled) })
				}
				continue
			}
		}
		if _, err := client.Write(append(line, payload...)); err != nil {
			return
		}
		if pending != nil && len(fields) == 1 && strings.EqualFold(fields[0], "PONG") {
			settle.Stop()
			h.settled(pending)
			pending = nil
		}
	}
}

// readMessage reads one protocol line from lines and, when its verb is one of withPayload, the
// payload its last field counts and the CRLF after it.
func readMessage(lines *bufio.Reader, withPayload ...string) (line, payload []byte, fields []string, err error) {
	for {
		if line, err = lines.ReadBytes('\n'); err != nil {
			return nil, nil, nil, err
		}
		if fields = strings.Fields(string(line)); len(fields) > 0 {
			break
		}
	}
	for _, verb := range withPayload {
		if !strings.EqualFold(fields[0], verb) || len(fields) < 3 {
			continue
		}
		size, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil {
			return nil, nil, nil, err
		}
		payload = make([]byte, size+len("\r\n"))
		if _, err := io.ReadFull(lines, payload); err != nil {
			return nil, nil, nil, err
		}
		break
	}
	return line, payload, fields, nil
}

func (h *ReplyHold) close() {
	_ = h.listener.Close()
	h.Drop()
}
