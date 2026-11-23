package buckets

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/testnats"
)

// answerRelay relays a client's connections to a hub and acts on the client's requests on one
// subject. Each such request is counted, and dropRequest keeps it from the hub. Each hub answer to
// one of them is held for hold before it is passed on, or kept from the client for ever when
// dropAnswer is set. The client's side is read a protocol message at a time (CONNECT, SUB, UNSUB,
// PING, PONG and publishes, each publish followed by its bytes and a CRLF), and so is the hub's
// (INFO, MSG, HMSG, PING, PONG, +OK, -ERR, each message followed by its bytes and a CRLF).
type answerRelay struct {
	url         string
	subject     string
	hold        time.Duration
	dropAnswer  bool
	dropRequest bool
	requests    atomic.Int32

	mu      sync.Mutex
	replies map[string]bool
}

func startAnswerRelay(t *testing.T, target string, r *answerRelay) *answerRelay {
	t.Helper()
	server, err := url.Parse(target)
	if err != nil || server.Host == "" {
		t.Fatalf("read the hub's URL %q: %v", target, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the answer relay: %v", err)
	}
	r.url, r.replies = "nats://"+listener.Addr().String(), map[string]bool{}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			hub, err := net.Dial("tcp", server.Host)
			if err != nil {
				_ = client.Close()
				continue
			}
			go r.requestsTo(hub, client)
			go r.answersTo(client, hub)
		}
	}()
	return r
}

// requestsTo passes what the client sends on to the hub, noting each request on subject by its
// reply subject, and keeping it from the hub when dropRequest is set.
func (r *answerRelay) requestsTo(hub, client net.Conn) {
	defer func() { _ = hub.Close(); _ = client.Close() }()
	reader := bufio.NewReader(client)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		fields := strings.Fields(line)
		publish := len(fields) >= 3 && (strings.EqualFold(fields[0], "PUB") || strings.EqualFold(fields[0], "HPUB"))
		if !publish {
			if _, err := io.WriteString(hub, line); err != nil {
				return
			}
			continue
		}
		size, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil {
			return
		}
		body := make([]byte, size+2)
		if _, err := io.ReadFull(reader, body); err != nil {
			return
		}
		if fields[1] == r.subject {
			r.requests.Add(1)
			headed := strings.EqualFold(fields[0], "HPUB")
			if (!headed && len(fields) == 4) || (headed && len(fields) == 5) {
				r.mu.Lock()
				r.replies[fields[2]] = true
				r.mu.Unlock()
			}
			if r.dropRequest {
				continue
			}
		}
		if _, err := io.WriteString(hub, line); err != nil {
			return
		}
		if _, err := hub.Write(body); err != nil {
			return
		}
	}
}

// answersTo passes what the hub sends on to the client, holding or keeping back each answer to a
// noted request.
func (r *answerRelay) answersTo(client, hub net.Conn) {
	defer func() { _ = hub.Close(); _ = client.Close() }()
	reader := bufio.NewReader(hub)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		fields := strings.Fields(line)
		message := len(fields) >= 4 && (fields[0] == "MSG" || fields[0] == "HMSG")
		if !message {
			if _, err := io.WriteString(client, line); err != nil {
				return
			}
			continue
		}
		size, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil {
			return
		}
		body := make([]byte, size+2)
		if _, err := io.ReadFull(reader, body); err != nil {
			return
		}
		r.mu.Lock()
		noted := r.replies[fields[1]]
		r.mu.Unlock()
		if noted {
			if r.dropAnswer {
				continue
			}
			time.Sleep(r.hold)
		}
		if _, err := io.WriteString(client, line); err != nil {
			return
		}
		if _, err := client.Write(body); err != nil {
			return
		}
	}
}

// relayedStream makes stream on a server of its own and connects through relay to it, answering
// the relay's connection and a direct one.
func relayedStream(t *testing.T, stream string, relay *answerRelay) (relayed, direct *natsgo.Conn) {
	t.Helper()
	_, uri := testnats.Start(t)
	direct = testnats.Connect(t, uri)
	t.Cleanup(direct.Close)
	js, err := direct.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&natsgo.StreamConfig{Name: stream, Subjects: []string{"$KV." + strings.TrimPrefix(stream, "KV_") + ".>"}}); err != nil {
		t.Fatalf("make %s: %v", stream, err)
	}
	relay.subject = "$JS.API.STREAM.DELETE." + stream
	startAnswerRelay(t, uri, relay)
	relayed, err = natsgo.Connect(relay.url)
	if err != nil {
		t.Fatalf("connect through the relay: %v", err)
	}
	t.Cleanup(relayed.Close)
	return relayed, direct
}

// streamThere reads stream on conn: true when the hub answers it, false when it answers it absent.
func streamThere(t *testing.T, conn *natsgo.Conn, stream string) bool {
	t.Helper()
	js, err := conn.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	_, err = js.StreamInfo(stream)
	if errors.Is(err, natsgo.ErrStreamNotFound) {
		return false
	}
	if err != nil {
		t.Fatalf("read %s: %v", stream, err)
	}
	return true
}

// A restore's delete waits its own bound, the restore's request bound, for the hub's answer, not
// nats.go's 5 s default. In CI run 45074967422, at a load where the hub answered late, the delete of
// a cut run's leftover had no answer within 5 s, and the restore stopped instead of running again
// (TestHoldTheRestoresOneRerun). Here the hub's answer to the delete is held 7 s: the delete lands,
// and the cut run's cleanup (deleteLeftover) completes with the stream gone, sending the delete
// once while the stream was there.
func TestTheRestoresDeleteWaitsPastFiveSecondsForItsAnswer(t *testing.T) {
	saved := leftoverWait
	leftoverWait = time.Second
	t.Cleanup(func() { leftoverWait = saved })
	stream := "KV_delete_bound"
	relay := &answerRelay{hold: 7 * time.Second}
	relayed, direct := relayedStream(t, stream, relay)

	start := time.Now()
	err := deleteLeftover(context.Background(), relayed, stream)
	took := time.Since(start)
	if err != nil {
		t.Fatalf("the cleanup of a leftover whose delete is answered after 7 s = %v (after %s), want it done", err, took.Round(time.Millisecond))
	}
	if took < 7*time.Second {
		t.Fatalf("the cleanup took %s, under the 7 s the answer was held: the hold did not take", took.Round(time.Millisecond))
	}
	if streamThere(t, direct, stream) {
		t.Fatalf("%s is still there after its cleanup", stream)
	}
}
