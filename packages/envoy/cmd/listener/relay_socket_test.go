package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	natsgo "github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/brokertoken/brokertokentest"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/logging"
	"github.com/sjawhar/envoy/internal/testnats"
)

// The edge project every row of this file's rig admits, and the second project a row names when
// it changes what a token lists.
const (
	relaySocketProject      = "PRIV"
	relaySocketOtherProject = "INFRA"
	relaySocketPerson       = "outside@example.com"
	// relaySocketTestCheck is how often a socket of this file's rig asks the broker again: a
	// tenth of a second, so a row that waits for a close waits for it within a second of the
	// answer that ends it.
	relaySocketTestCheck = 100 * time.Millisecond
)

// relaySocketClock is the rig's reading of now for the verifier's cache and for the socket's own
// test of whether a token's expiry has passed, so a row moves past the five seconds a live answer
// is reused for (brokertoken.AffirmativeCacheTTL) without sleeping through them. A proof's own
// window is the library's, on the real clock, which is why this clock starts at the real one and
// only moves forward.
type relaySocketClock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *relaySocketClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *relaySocketClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset += d
}

// relaySocketRig is two replicas of an edge deployment over the in-process server. The owner is
// the replica that holds the session's consumer and relays it, as an edge listener's relay runs:
// its receipts arrive under bus.RelayReplyPrefix (relayReplyInbox). The socket is the other
// replica's: its /v1 behind apiAuth with broker tokens on, on a NATS connection of its own, the
// broker the stub that answers what each row says each token is.
type relaySocketRig struct {
	owner   *relayRig
	client  *bus.Client
	sockets *relaySockets
	server  *httptest.Server
	broker  *brokertokentest.StubBroker
	caller  *brokertokentest.Caller
	clock   *relaySocketClock
	logs    *cmdtest.LockedBuffer
}

// relaySocketRigOption changes how newRelaySocketRig builds the socket's replica.
type relaySocketRigOption func(*relaySocketRigConfig)

type relaySocketRigConfig struct {
	// natsLatency is the round trip the socket replica's own connection is given (a
	// testnats.Proxy with ProxyLatency), so a row can tell a subscribe that reached the server
	// from one that has not yet; none by default.
	natsLatency time.Duration
	// edgeProjects is the replica's ENVOY_EDGE_PROJECTS: relaySocketProject alone by default.
	edgeProjects []string
}

func withSocketNATSLatency(rtt time.Duration) relaySocketRigOption {
	return func(c *relaySocketRigConfig) { c.natsLatency = rtt }
}

func withEdgeProjects(projects ...string) relaySocketRigOption {
	return func(c *relaySocketRigConfig) { c.edgeProjects = projects }
}

func newRelaySocketRig(t *testing.T, options ...relaySocketRigOption) *relaySocketRig {
	t.Helper()
	config := relaySocketRigConfig{edgeProjects: []string{relaySocketProject}}
	for _, option := range options {
		option(&config)
	}
	owner := newRelayRigUnownedAs(t, relayPauseFirst, relayPauseMax, true)
	owner.relays.setOwned(true)

	url := owner.url
	if config.natsLatency > 0 {
		url = testnats.StartProxy(t, url, testnats.ProxyLatency(config.natsLatency)).URL()
	}
	client, err := bus.Connect([]string{url})
	if err != nil {
		t.Fatalf("connect the socket's replica: %v", err)
	}
	t.Cleanup(client.Close)

	broker := brokertokentest.NewStubBroker(t)
	clock := &relaySocketClock{}
	tokens := newBrokerTokens(listenerBrokerConfig{
		BrokerURL:        broker.Server.URL,
		AccountTokenFile: "/var/run/secrets/broker/token",
		Origin:           listenerBrokerOrigin,
	})
	verifier, err := brokertoken.New(brokertoken.Options{
		BrokerURL: broker.Server.URL,
		Account:   listenerBrokerAccount("listener-account-token"),
		Origin:    listenerBrokerOrigin,
		Replay:    &spentProofsInMemory{},
		Address:   listenerSourceAddress,
		Now:       clock.now,
	})
	if err != nil {
		t.Fatalf("build the broker-token verifier: %v", err)
	}
	tokens.verifier.Store(verifier)

	logs := &cmdtest.LockedBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("the socket replica's log:\n%s", logs.String())
		}
	})
	logger := logging.NewWithWriter("edge-b", logs)
	sockets := newRelaySockets(client, tokens, config.edgeProjects, logger)
	sockets.relaySocketCheckEvery = relaySocketTestCheck
	sockets.now = clock.now
	routes := http.NewServeMux()
	registerV1Routes(routes, &listenerDeps{client: client, relaySockets: sockets}, "edge-b", logger)
	server := httptest.NewServer(apiAuth(listenerAPIAuth{brokers: tokens, logger: logger}, routes))
	t.Cleanup(server.Close)
	t.Cleanup(sockets.stop)
	return &relaySocketRig{
		owner: owner, client: client, sockets: sockets, server: server,
		broker: broker, caller: brokertokentest.NewCaller(t), clock: clock, logs: logs,
	}
}

// edgeAnswer is the broker's answer about a token traded for session acting for the rig's person,
// listing projects and expiring after life: what an outside agent's access token is.
func edgeAnswer(session, jkt string, life time.Duration, projects ...string) brokertoken.Introspection {
	return brokertoken.Introspection{
		Active:       true,
		Session:      session,
		Person:       relaySocketPerson,
		ProjectScope: brokertoken.ProjectScopePerson,
		Projects:     projects,
		ExpiresAt:    time.Now().Add(life).Unix(),
		ClientID:     brokertoken.ExchangeClientID,
		Thumbprint:   jkt,
	}
}

// relaySocketPath is session's relay socket.
func relaySocketPath(session string) string { return "/v1/sessions/" + session + "/relay" }

// upgradeHeader is the upgrade's credential: token under the DPoP scheme and a proof the rig's
// caller signed for a GET of session's socket at the listener's own origin.
func (rig *relaySocketRig) upgradeHeader(t *testing.T, session, token string) http.Header {
	t.Helper()
	signed := rig.caller.Signed(t, http.MethodGet, listenerBrokerOrigin+relaySocketPath(session), token)
	return http.Header{"Authorization": {signed.Header.Get("Authorization")}, "DPoP": {signed.Header.Get("DPoP")}}
}

// dialAs opens session's relay socket presenting header, offering the subprotocols named, and
// answers the socket (nil when the upgrade was refused) with the upgrade's response.
func (rig *relaySocketRig) dialAs(t *testing.T, session string, header http.Header, subprotocols ...string) (*relaySocketClient, *http.Response, error) {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: subprotocols, HandshakeTimeout: 5 * time.Second}
	conn, response, err := dialer.Dial("ws"+strings.TrimPrefix(rig.server.URL, "http")+relaySocketPath(session), header)
	if err != nil {
		return nil, response, err
	}
	return startRelaySocketClient(t, conn), response, nil
}

// open is a socket session opened with a fresh token the broker holds live for an hour, listing
// the rig's project, which it fails the test unless the upgrade admits.
func (rig *relaySocketRig) open(t *testing.T, session string) (*relaySocketClient, string) {
	t.Helper()
	token := "access-token-of-" + session + "-" + t.Name()
	rig.broker.Says(token, edgeAnswer(session, rig.caller.JKT(), time.Hour, relaySocketProject))
	return rig.openWith(t, session, token), token
}

// openWith opens session's socket with token, failing the test unless the upgrade admits it.
func (rig *relaySocketRig) openWith(t *testing.T, session, token string) *relaySocketClient {
	t.Helper()
	client, response, err := rig.dialAs(t, session, rig.upgradeHeader(t, session, token), relaySubprotocol)
	if err != nil {
		t.Fatalf("open the relay socket of %s: %v (%s)\n%s", session, err, describeUpgrade(response), rig.logs.String())
	}
	if got := client.conn.Subprotocol(); got != relaySubprotocol {
		t.Fatalf("the socket was opened with the subprotocol %q, want %q", got, relaySubprotocol)
	}
	return client
}

// describeUpgrade is an upgrade's refusal as a failure names it: its status and its body.
func describeUpgrade(response *http.Response) string {
	if response == nil {
		return "no response"
	}
	body := &bytes.Buffer{}
	if response.Body != nil {
		_, _ = body.ReadFrom(response.Body)
	}
	return response.Status + " " + strings.TrimSpace(body.String())
}

// relayFrameRead is one frame the server sent a socket client, decoded as K-E2 spells it.
type relayFrameRead struct {
	Op      string              `json:"op"`
	SID     *int64              `json:"sid"`
	Subject string              `json:"subject"`
	Reply   string              `json:"reply"`
	Headers map[string][]string `json:"headers"`
	Data    string              `json:"data"`
	Code    string              `json:"code"`
	// raw is the frame's own text, so a row can read a field's presence and not only its value.
	raw string
}

// relaySocketClient is a session's end of a relay socket: one goroutine reads every frame the
// server sends, answering the server's pings as gorilla's client does, and hands each frame, each
// pong and the close the server ends with to the row.
type relaySocketClient struct {
	t      *testing.T
	conn   *websocket.Conn
	frames chan relayFrameRead
	pongs  chan time.Time
	// closed carries the close the read ended with: the server's close frame, or the error that
	// ended the read without one.
	closed  chan error
	writeMu sync.Mutex
}

func startRelaySocketClient(t *testing.T, conn *websocket.Conn) *relaySocketClient {
	t.Helper()
	client := &relaySocketClient{t: t, conn: conn, frames: make(chan relayFrameRead, 64), pongs: make(chan time.Time, 8), closed: make(chan error, 1)}
	conn.SetPongHandler(func(string) error {
		client.pongs <- time.Now()
		return nil
	})
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				client.closed <- err
				return
			}
			frame := relayFrameRead{raw: string(data)}
			if err := json.Unmarshal(data, &frame); err != nil {
				client.closed <- err
				return
			}
			client.frames <- frame
		}
	}()
	return client
}

// send writes one client frame.
func (c *relaySocketClient) send(frame map[string]any) {
	c.t.Helper()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.WriteJSON(frame); err != nil {
		c.t.Fatalf("send %v: %v", frame, err)
	}
}

func (c *relaySocketClient) sub(sid int, subject string) {
	c.t.Helper()
	c.send(map[string]any{"op": "sub", "sid": sid, "subject": subject})
}

func (c *relaySocketClient) pub(subject, data string) {
	c.t.Helper()
	c.send(map[string]any{"op": "pub", "subject": subject, "data": base64.StdEncoding.EncodeToString([]byte(data))})
}

// ping sends a ping, as the client's flush() does.
func (c *relaySocketClient) ping() {
	c.t.Helper()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second)); err != nil {
		c.t.Fatalf("send a ping: %v", err)
	}
}

// next is the next frame within the bound, failing the test when none arrives.
func (c *relaySocketClient) next(within time.Duration) relayFrameRead {
	c.t.Helper()
	select {
	case frame := <-c.frames:
		return frame
	case err := <-c.closed:
		c.t.Fatalf("the socket closed (%v) before a frame arrived", err)
	case <-time.After(within):
		c.t.Fatalf("no frame arrived within %s", within)
	}
	return relayFrameRead{}
}

// none fails the test when a frame arrives within the window.
func (c *relaySocketClient) none(window time.Duration, what string) {
	c.t.Helper()
	select {
	case frame := <-c.frames:
		c.t.Fatalf("%s: the socket sent %s within %s, want nothing", what, frame.raw, window)
	case <-time.After(window):
	}
}

// closeCode waits up to within for the server to close the socket and answers its close frame's
// code and text, failing the test when the socket stays open or ends without a close frame.
func (c *relaySocketClient) closeCode(within time.Duration) (int, string) {
	c.t.Helper()
	select {
	case err := <-c.closed:
		var closed *websocket.CloseError
		if !errors.As(err, &closed) {
			c.t.Fatalf("the socket ended with %v, not a close frame", err)
		}
		return closed.Code, closed.Text
	case frame := <-c.frames:
		c.t.Fatalf("the socket sent %s, want it closed", frame.raw)
	case <-time.After(within):
		c.t.Fatalf("the socket was still open %s later", within)
	}
	return 0, ""
}

// open fails the test when the socket closes within the window.
func (c *relaySocketClient) stillOpen(window time.Duration, what string) {
	c.t.Helper()
	select {
	case err := <-c.closed:
		c.t.Fatalf("%s: the socket closed within %s (%v), want it open", what, window, err)
	case <-time.After(window):
	}
}

// sid is a frame's sid, failing the test when it names none.
func (f relayFrameRead) sid(t *testing.T) int64 {
	t.Helper()
	if f.SID == nil {
		t.Fatalf("the frame %s names no sid", f.raw)
	}
	return *f.SID
}

// data is a msg frame's data, decoded.
func (f relayFrameRead) data(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(f.Data)
	if err != nil {
		t.Fatalf("the frame %s carries data that is not base64: %v", f.raw, err)
	}
	return data
}

// Row 1, in one process: a message to S is stored, the owning replica relays it, the socket's
// replica forwards it to the session as a msg frame under the sid it subscribed with, carrying
// the frame's headers and its receipt subject, which is an edge relay's (bus.RelayReplyPrefix) and
// not a reply inbox; the session answers done over the socket, the socket's replica publishes it
// on that subject, and the owner's consumer acks the message. Its control is the live row's
// (relay_socket_live_test.go): the receipt not published, and the message is still pending.
func TestARelaySocketForwardsAFrameAndPublishesItsReceipt(t *testing.T) {
	rig := newRelaySocketRig(t)
	const session = "ses_edge_forward"
	rig.owner.subscribe(t, consumerRegistration(session, "epoch-1"))
	client, _ := rig.open(t, session)
	client.sub(1, bus.DeliverSubject(session))
	client.ping()
	<-client.pongs

	stored, seq := rig.owner.storeEvent(t, contracts.LegacyAgentSubject(session))
	frame := client.next(5 * time.Second)
	if frame.Op != "msg" || frame.sid(t) != 1 || frame.Subject != bus.DeliverSubject(session) {
		t.Fatalf("the socket sent %s, want a msg of sid 1 on %s", frame.raw, bus.DeliverSubject(session))
	}
	if !strings.HasPrefix(frame.Reply, bus.RelayReplyPrefix) {
		t.Fatalf("the frame's receipt subject is %q, want one under %s: an edge relay answers no reply inbox", frame.Reply, bus.RelayReplyPrefix)
	}
	var item contracts.Envelope
	if err := json.Unmarshal(frame.data(t), &item); err != nil || item.EventID != stored.EventID {
		t.Fatalf("the frame carries %q (%v), want the stored event %s", frame.data(t), err, stored.EventID)
	}
	if got := frame.Headers[relayHeaderSubject]; !slices.Equal(got, []string{contracts.LegacyAgentSubject(session)}) {
		t.Fatalf("the frame's %s header is %q, want the stored subject", relayHeaderSubject, got)
	}

	client.pub(frame.Reply, relayReplyReceived)
	client.pub(frame.Reply, relayReplyDone)
	awaitAcked(t, rig.owner.consumers, session, seq)
}

// Row 2, in one process: a sub to another session's relay subject and to the session's own
// agent-stream control subject is answered subject_not_granted under its sid, and the replica
// holds no subscription to either, so a frame published to the other session never reaches this
// socket; a pub to a reply subject the socket never forwarded is publish_not_granted, and nothing
// is published on it. Control: the subject check removed; the other session's frame reaches the
// socket and the row fails naming it.
func TestARelaySocketRefusesASubjectAndAReplyItWasNotGranted(t *testing.T) {
	rig := newRelaySocketRig(t)
	const session, other = "ses_edge_refused", "ses_someone_else"
	client, _ := rig.open(t, session)
	held := rig.client.Connection().NumSubscriptions()

	// refused waits for sid's err, and answers whether one came: a sub answered nothing within the
	// wait is one the replica took, which the frame below then shows.
	refused := func(sid int, subject string) bool {
		t.Helper()
		select {
		case frame := <-client.frames:
			if frame.Op != "err" || frame.Code != "subject_not_granted" || frame.sid(t) != int64(sid) {
				t.Fatalf("a sub to %s was answered %s, want err subject_not_granted for sid %d", subject, frame.raw, sid)
			}
			return true
		case <-time.After(2 * time.Second):
			t.Errorf("a sub to %s was answered nothing within 2 s, want err subject_not_granted for sid %d", subject, sid)
			return false
		}
	}
	client.sub(1, bus.DeliverSubject(other))
	refused(1, bus.DeliverSubject(other))
	client.sub(2, "agentstream."+session+".control")
	refused(2, "agentstream."+session+".control")
	if now := rig.client.Connection().NumSubscriptions(); now != held {
		t.Errorf("the replica holds %d subscriptions after two refused subs, want the %d it held before", now, held)
	}
	publisher := testnats.Connect(t, rig.owner.url)
	stray := &natsgo.Msg{Subject: bus.DeliverSubject(other), Reply: bus.RelayReplyPrefix + "stray.1", Data: []byte(`{"stray":true}`), Header: natsgo.Header{}}
	if err := publisher.PublishMsg(stray); err != nil {
		t.Fatalf("publish a frame for %s: %v", other, err)
	}
	select {
	case frame := <-client.frames:
		t.Fatalf("the replica subscribed %s for this socket: the other session's frame reached it as %s", bus.DeliverSubject(other), frame.raw)
	case <-time.After(time.Second):
	}
	if t.Failed() {
		return
	}

	watcher := testnats.Connect(t, rig.owner.url)
	never := bus.RelayReplyPrefix + "never.forwarded"
	heard, err := watcher.SubscribeSync(never)
	if err != nil {
		t.Fatalf("watch %s: %v", never, err)
	}
	if err := watcher.Flush(); err != nil {
		t.Fatalf("flush the watcher: %v", err)
	}
	client.pub(never, relayReplyDone)
	if frame := client.next(5 * time.Second); frame.Op != "err" || frame.Code != "publish_not_granted" || frame.SID != nil {
		t.Fatalf("a pub to a reply never forwarded was answered %s, want err publish_not_granted naming no sid", frame.raw)
	}
	if msg, err := heard.NextMsg(500 * time.Millisecond); err == nil {
		t.Fatalf("the replica published %q on %s, which it never forwarded", msg.Data, never)
	}
}

// Row 3, in one process: a send to S probes S's relay subject (probeSessionConnection) while the
// socket's replica subscribes it; the probe reads connected, since a subscriber that answers
// nothing reads connected, and the session's client receives no probe frame. The row's control is
// its second half: the socket closed first, the replica holds no subscription, and the same probe
// reads stopped.
func TestAProbeOfASocketIsNeitherForwardedNorAnswered(t *testing.T) {
	rig := newRelaySocketRig(t)
	const session = "ses_edge_probed"
	client, _ := rig.open(t, session)
	held := rig.client.Connection().NumSubscriptions()
	client.sub(1, bus.DeliverSubject(session))
	client.ping()
	<-client.pongs

	prober := setupPublishTestClientOn(t, rig.owner.url)
	logger := logging.NewWithWriter("prober", &bytes.Buffer{})
	if !probeSessionConnection(t.Context(), prober, session, logger) {
		t.Fatal("the probe of a session whose socket subscribes its relay subject read stopped, want connected")
	}
	client.none(500*time.Millisecond, "the connection probe")

	_ = client.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	waitFor(t, 5*time.Second, "the replica to drop the socket's subscription", func() bool {
		return rig.client.Connection().NumSubscriptions() == held
	})
	if probeSessionConnection(t.Context(), prober, session, logger) {
		t.Fatal("the probe of a session whose socket closed read connected, want stopped")
	}
}

// Rows 4, 5, 7 and 8, in one process, each on its own socket: the broker's answer for the
// socket's token changes, and the socket closes with the code K-E2 gives that case within a
// second of the first check that reads it (the rig's verifier clock moved past the five seconds a
// live answer is reused for, so the next check asks the broker). The last case is the negative
// control of all of them: an answer that has not changed keeps the socket open. Each row's own
// control is the check of its case removed, and the socket stays open.
func TestAnOpenSocketClosesByTheBrokersAnswer(t *testing.T) {
	type outcome struct {
		code int
		text string
	}
	expired := outcome{relaySocketCloseExpired, relaySocketReasonExpired}
	refused := outcome{relaySocketCloseRefused, relaySocketReasonRefused}
	for _, row := range []struct {
		name  string
		edge  []string
		life  time.Duration
		later func(rig *relaySocketRig, session, token string)
		want  *outcome
	}{
		{name: "its token's exp passed", life: 2 * time.Second, want: &expired,
			later: func(rig *relaySocketRig, session, token string) {
				rig.clock.advance(3 * time.Second)
				rig.broker.Says(token, brokertoken.Introspection{})
			}},
		{name: "its token was revoked", life: time.Hour, want: &refused,
			later: func(rig *relaySocketRig, session, token string) {
				rig.broker.Says(token, brokertoken.Introspection{})
			}},
		{name: "its projects grew a project the edge does not serve", life: time.Hour, want: &refused,
			later: func(rig *relaySocketRig, session, token string) {
				rig.broker.Says(token, edgeAnswer(session, rig.caller.JKT(), time.Hour, relaySocketOtherProject, relaySocketProject))
			}},
		{name: "its projects became another the edge serves", edge: []string{relaySocketOtherProject, relaySocketProject}, life: time.Hour, want: &refused,
			later: func(rig *relaySocketRig, session, token string) {
				rig.broker.Says(token, edgeAnswer(session, rig.caller.JKT(), time.Hour, relaySocketOtherProject))
			}},
		{name: "its session changed", life: time.Hour, want: &refused,
			later: func(rig *relaySocketRig, session, token string) {
				rig.broker.Says(token, edgeAnswer("ses_another", rig.caller.JKT(), time.Hour, relaySocketProject))
			}},
		{name: "the broker cannot be asked", life: time.Hour, want: &outcome{websocket.CloseTryAgainLater, relaySocketReasonBrokerUnavailable},
			later: func(rig *relaySocketRig, session, token string) {
				rig.broker.Server.Close()
			}},
		{name: "its answer stands", life: time.Hour,
			later: func(rig *relaySocketRig, session, token string) {}},
	} {
		t.Run(row.name, func(t *testing.T) {
			options := []relaySocketRigOption{}
			if row.edge != nil {
				options = append(options, withEdgeProjects(row.edge...))
			}
			rig := newRelaySocketRig(t, options...)
			const session = "ses_edge_checked"
			token := "access-token-of-a-checked-socket"
			rig.broker.Says(token, edgeAnswer(session, rig.caller.JKT(), row.life, relaySocketProject))
			client := rig.openWith(t, session, token)
			client.stillOpen(5*relaySocketTestCheck, "before the answer changed")

			row.later(rig, session, token)
			rig.clock.advance(brokertoken.AffirmativeCacheTTL + time.Second)
			if row.want == nil {
				client.stillOpen(10*relaySocketTestCheck, "an answer that stands")
				return
			}
			code, text := client.closeCode(time.Second)
			if code != row.want.code || text != row.want.text {
				t.Fatalf("the socket closed %d %q, want %d %q\n%s", code, text, row.want.code, row.want.text, rig.logs.String())
			}
		})
	}
}

// Row 8's second half: while the broker cannot be asked, a new upgrade is refused 503, so the
// client comes back rather than discarding its token.
func TestAnOpenSocketsReopenIsRefused503WhileTheBrokerCannotBeAsked(t *testing.T) {
	rig := newRelaySocketRig(t)
	const session = "ses_edge_broker_down"
	token := "access-token-while-the-broker-is-down"
	rig.broker.Says(token, edgeAnswer(session, rig.caller.JKT(), time.Hour, relaySocketProject))
	rig.broker.Server.Close()
	_, response, err := rig.dialAs(t, session, rig.upgradeHeader(t, session, token), relaySubprotocol)
	if err == nil || response == nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("an upgrade while the broker cannot be asked = %v (%s), want 503", err, describeUpgrade(response))
	}
}

// K-E2's upgrade, its refusals each with its control: a token naming another session is refused
// 403 before the upgrade, with sessionNotBound's line (control: the token's own session opens); a
// client that offers no envoy-relay.v1 is refused 400; a fifth socket of one session is refused 429
// (control: the fourth opens); and a socket the listener closes at its stop is closed 1012.
func TestARelaySocketUpgradeIsRefusedAsTheWireSays(t *testing.T) {
	rig := newRelaySocketRig(t)
	const session, other = "ses_edge_upgrade", "ses_not_the_tokens"
	token := "access-token-of-the-upgrading-session"
	rig.broker.Says(token, edgeAnswer(session, rig.caller.JKT(), time.Hour, relaySocketProject))

	_, response, err := rig.dialAs(t, other, rig.upgradeHeader(t, other, token), relaySubprotocol)
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("an upgrade of %s with %s's token = %v (%s), want 403", other, session, err, describeUpgrade(response))
	}
	if logged := rig.logs.String(); !strings.Contains(logged, "listener: broker token acts as another session") || !strings.Contains(logged, other) {
		t.Fatalf("the refusal wrote no sessionNotBound line naming %s:\n%s", other, logged)
	}

	if _, response, err := rig.dialAs(t, session, rig.upgradeHeader(t, session, token)); err == nil || response == nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("an upgrade offering no subprotocol = %v (%s), want 400", err, describeUpgrade(response))
	}

	open := make([]*relaySocketClient, 0, relaySocketMaxPerSession)
	for range relaySocketMaxPerSession {
		open = append(open, rig.openWith(t, session, token))
	}
	if _, response, err := rig.dialAs(t, session, rig.upgradeHeader(t, session, token), relaySubprotocol); err == nil || response == nil || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("a fifth socket of %s = %v (%s), want 429", session, err, describeUpgrade(response))
	}

	rig.sockets.stop()
	for _, client := range open {
		if code, text := client.closeCode(2 * time.Second); code != websocket.CloseServiceRestart || text != relaySocketReasonStopping {
			t.Fatalf("a socket the listener's stop closed got %d %q, want %d %q", code, text, websocket.CloseServiceRestart, relaySocketReasonStopping)
		}
	}
	if _, response, err := rig.dialAs(t, session, rig.upgradeHeader(t, session, token), relaySubprotocol); err == nil || response == nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("an upgrade after the listener's stop = %v (%s), want 503", err, describeUpgrade(response))
	}
}

// A frame over 1 MiB closes the socket 1009 (control: a frame just under the bound is read and
// answered).
func TestARelaySocketClosesOnAFrameOverItsReadLimit(t *testing.T) {
	rig := newRelaySocketRig(t)
	const session = "ses_edge_oversized"
	client, _ := rig.open(t, session)
	// A pub of a reply never forwarded, padded to just under the bound, is read and refused.
	padding := strings.Repeat("a", relaySocketReadLimit-200)
	client.send(map[string]any{"op": "pub", "subject": "notifications.envoy.relayreply.padded", "data": "", "padding": padding})
	if frame := client.next(5 * time.Second); frame.Code != "publish_not_granted" {
		t.Fatalf("a frame under the bound was answered %s, want it read and refused publish_not_granted", frame.raw)
	}
	client.send(map[string]any{"op": "pub", "subject": "notifications.envoy.relayreply.padded", "data": "", "padding": padding + strings.Repeat("a", 400)})
	if code, _ := client.closeCode(5 * time.Second); code != websocket.CloseMessageTooBig {
		t.Fatalf("a frame over the bound closed the socket %d, want %d", code, websocket.CloseMessageTooBig)
	}
}

// K-E2 clarification (1): a socket's frames are read one at a time, in order, and a sub is handled
// whole - its subscribe and the flush that makes the server hold it - before the next frame is
// read. The socket's replica reaches NATS over a link with a 400 ms round trip, so a subscribe
// takes that long to be held. The row sends a sub of its own relay subject and, at once, a sub of
// another session's: the second's err is written only once the first is held, so a message
// published the moment that err arrives reaches the socket. Control: each frame handled on a
// goroutine of its own; the err arrives before the subscribe is held, the message is lost, and the
// row fails naming it.
func TestARelaySocketHandlesASubWholeBeforeItReadsTheNextFrame(t *testing.T) {
	rig := newRelaySocketRig(t, withSocketNATSLatency(400*time.Millisecond))
	const session = "ses_edge_in_order"
	client, _ := rig.open(t, session)
	client.sub(1, bus.DeliverSubject(session))
	client.sub(2, bus.DeliverSubject("ses_someone_else"))
	if frame := client.next(5 * time.Second); frame.Code != "subject_not_granted" || frame.sid(t) != 2 {
		t.Fatalf("the second sub was answered %s, want err subject_not_granted for sid 2", frame.raw)
	}
	sent := publishRelayFrame(t, rig.owner.url, session, natsgo.Header{})
	select {
	case frame := <-client.frames:
		if frame.Op != "msg" || frame.sid(t) != 1 || frame.Reply != sent.Reply {
			t.Fatalf("the message published once the second sub was answered reached the socket as %s, want the msg %s under sid 1", frame.raw, sent.Reply)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("the message %s published once the second sub was answered never reached the socket: the second sub was answered before the first's subscribe was held", sent.Reply)
	}
}

// K-E2 clarification (3), row 10: the client's flush() is a ping, answered on the socket's one
// reader only after every frame read before it is handled, so a ping sent right after a sub gets
// its pong only once that sub's subscribe is held, and a message published the moment the pong
// arrives reaches the socket. The replica's link has a 400 ms round trip, as above. Control: a
// ping handler that answers at once, off the reader; the pong precedes the subscribe, the message
// is lost, and the row fails naming it.
func TestAPingAfterASubIsAnsweredAfterTheSubscribe(t *testing.T) {
	rig := newRelaySocketRig(t, withSocketNATSLatency(400*time.Millisecond))
	const session = "ses_edge_flush"
	client, _ := rig.open(t, session)
	client.sub(1, bus.DeliverSubject(session))
	client.ping()
	select {
	case <-client.pongs:
	case <-time.After(5 * time.Second):
		t.Fatal("no pong within 5 s of the ping")
	}
	sent := publishRelayFrame(t, rig.owner.url, session, natsgo.Header{})
	select {
	case frame := <-client.frames:
		if frame.Op != "msg" || frame.Reply != sent.Reply {
			t.Fatalf("the socket sent %s, want the msg %s published the moment the pong arrived", frame.raw, sent.Reply)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("the message %s published the moment the pong arrived never reached the socket: the pong came before the subscribe was held", sent.Reply)
	}
}

// K-E2 clarification (2): msg.headers is always present, a JSON object mapping each header name as
// NATS carries it, its case kept, to every one of its values in order, and {} when the message
// carries none. Control: the headers read one value per name (Header.Get); the repeated values are
// lost and the row fails naming them.
func TestARelaySocketCarriesEveryValueOfAHeaderInOrder(t *testing.T) {
	rig := newRelaySocketRig(t)
	const session = "ses_edge_headers"
	client, _ := rig.open(t, session)
	client.sub(7, bus.DeliverSubject(session))
	client.ping()
	<-client.pongs

	want := natsgo.Header{"X-Repeated": {"first", "second", "third"}, "lower-case-Name": {"kept"}}
	publishRelayFrame(t, rig.owner.url, session, want)
	frame := client.next(5 * time.Second)
	if len(frame.Headers) != len(want) {
		t.Fatalf("the frame carries the headers %v, want exactly %v", frame.Headers, want)
	}
	for name, values := range want {
		if got := frame.Headers[name]; !slices.Equal(got, values) {
			t.Fatalf("the frame carries %s: %q, want %q in that order (all of %s)", name, got, values, frame.raw)
		}
	}

	publishRelayFrame(t, rig.owner.url, session, nil)
	bare := client.next(5 * time.Second)
	if !strings.Contains(bare.raw, `"headers":{}`) {
		t.Fatalf("a frame of a message with no headers is %s, want \"headers\":{}", bare.raw)
	}
}

// Row 9: a listener without ENVOY_EDGE_PROJECTS answers the relay socket's path 404, an upgrade
// included, through the same /v1 a deployment mounts. Control: the same path on the edge rig is
// upgraded.
func TestANonEdgeListenerServesNoRelaySocket(t *testing.T) {
	routes := http.NewServeMux()
	logger := logging.NewWithWriter("not-edge", &bytes.Buffer{})
	registerV1Routes(routes, &listenerDeps{}, "not-edge", logger)
	server := httptest.NewServer(apiAuth(listenerAPIAuth{logger: logger}, routes))
	t.Cleanup(server.Close)
	const session = "ses_not_on_an_edge"

	plain, err := http.Get(server.URL + relaySocketPath(session))
	if err != nil {
		t.Fatalf("GET %s: %v", relaySocketPath(session), err)
	}
	_ = plain.Body.Close()
	if plain.StatusCode != http.StatusNotFound {
		t.Fatalf("GET %s on a listener that is not an edge = %d, want 404", relaySocketPath(session), plain.StatusCode)
	}
	dialer := websocket.Dialer{Subprotocols: []string{relaySubprotocol}, HandshakeTimeout: 5 * time.Second}
	if _, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relaySocketPath(session), nil); err == nil || response == nil || response.StatusCode != http.StatusNotFound {
		t.Fatalf("an upgrade of %s on a listener that is not an edge = %v (%s), want 404", relaySocketPath(session), err, describeUpgrade(response))
	}

	rig := newRelaySocketRig(t)
	rig.open(t, session)
}

// publishRelayFrame publishes, on session's relay subject, a message shaped as a relay frame -
// its receipt subject under bus.RelayReplyPrefix - carrying header, and answers it.
func publishRelayFrame(t *testing.T, url, session string, header natsgo.Header) *natsgo.Msg {
	t.Helper()
	conn := testnats.Connect(t, url)
	msg := &natsgo.Msg{
		Subject: bus.DeliverSubject(session),
		Reply:   bus.RelayReplyPrefix + "rowsown." + strings.ReplaceAll(t.Name(), "/", "."),
		Header:  header,
		Data:    []byte(`{"row":"` + t.Name() + `"}`),
	}
	if err := conn.PublishMsg(msg); err != nil {
		t.Fatalf("publish a frame for %s: %v", session, err)
	}
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush the frame for %s: %v", session, err)
	}
	return msg
}

// setupPublishTestClientOn is a bus client of its own on the server at url.
func setupPublishTestClientOn(t *testing.T, url string) *bus.Client {
	t.Helper()
	client, err := bus.Connect([]string{url})
	if err != nil {
		t.Fatalf("connect a client: %v", err)
	}
	t.Cleanup(client.Close)
	return client
}
