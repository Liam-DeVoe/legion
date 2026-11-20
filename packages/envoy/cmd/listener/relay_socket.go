package main

// relay_socket.go is an edge listener's relay socket: GET /v1/sessions/{session}/relay, a
// WebSocket on which a session that holds no NATS connection of its own (an outside person's
// agent, reaching the edge over HTTPS) receives its relay frames and answers them, for as long as
// the broker still holds the token that opened it live. Only a listener in edge mode
// (ENVOY_EDGE_PROJECTS) serves it; every other listener answers the path 404 (relaySocketRoute),
// so no shared listener's NATS grant has to cover it.
//
// The wire is the subprotocol envoy-relay.v1, text frames of JSON:
//
//   - the client's {"op":"sub","sid":<int>,"subject":<s>} subscribes, on this replica's own NATS
//     connection, exactly the socket's own relay subject (bus.DeliverSubject), and is answered
//     {"op":"err","sid":<int>,"code":"subject_not_granted"} for any other subject, the agent
//     stream's included, or when the server refuses the subscription; a sub past
//     relaySocketMaxSubscriptions is answered that sid's err subscription_limit and the socket
//     stays open; a sub that succeeds is answered nothing;
//   - {"op":"unsub","sid":<int>} ends that subscription;
//   - {"op":"pub","subject":<s>,"data":<base64>} publishes data on s, which must be the receipt
//     subject of a frame this socket forwarded in the last relaySocketReplyWindow, else
//     {"op":"err","code":"publish_not_granted"};
//   - the server's {"op":"msg","sid":…,"subject":…,"reply":…,"headers":{…},"data":<base64>} is
//     one relay frame, its headers always present as nats.Header in JSON (each name as NATS carries
//     it, case kept, to every one of its values in order; {} when there are none);
//   - {"op":"err","code":"bad_frame"}, then a close 1008 with reason bad_frame, answers a frame the
//     socket cannot read at all: not JSON, an unknown op, a sub or unsub naming no sid, or a sub
//     of a sid already held.
//
// The socket reads its frames one at a time on one reader and handles each whole before it reads
// the next: a sub's subscribe and the flush that makes the server hold it, a pub's publish and
// its flush. So the client's flush(), a WebSocket ping, is answered by gorilla/websocket's own
// ping handler on that reader only once every frame read before it is handled; nothing here
// installs another.
//
// A frame's receipt subject is the owning replica's, under bus.RelayReplyPrefix
// (startSessionRelays in edge mode), which the edge's grant publishes and subscribes: no reply
// inbox is answered from here. The connection probe (connection_probe.go) is never forwarded and
// never answered: the subscription itself is what keeps the session connected, and a probe costs
// its sender at most its one second. A message on the relay subject that is no relay frame (its
// reply not a relay receipt) is not forwarded either.
//
// While a socket is open it asks the broker again every relaySocketCheckEvery, on its ping tick
// (watch), and closes with the code its client reopens by (relaySocketVerdict).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/logging"
)

const (
	// relaySocketPattern is the socket's route, one segment the session.
	relaySocketPattern = "/v1/sessions/{session}/relay"
	// relaySubprotocol is the one subprotocol the socket speaks; an upgrade that offers none of
	// it is refused 400.
	relaySubprotocol = "envoy-relay.v1"
	// relaySocketCheckDefault is how often an open socket pings its client and asks the broker
	// about its token: inside the 120 s backend timeout of the edge's load balancer, and the 20 s
	// of a revoke's 25 s bound (the broker's answer is reused for at most
	// brokertoken.AffirmativeCacheTTL more).
	relaySocketCheckDefault = 20 * time.Second
	// relaySocketReadLimit is the largest frame a client may send; a larger one closes the socket
	// 1009, which gorilla/websocket writes.
	relaySocketReadLimit = 1 << 20
	// relaySocketMaxPerSession is how many sockets one session may hold open on one replica; the
	// next is refused 429.
	relaySocketMaxPerSession = 4
	// relaySocketMaxSubscriptions is how many subscriptions one socket may hold. A socket has one
	// subject to subscribe, so a client needs one; the bound keeps a client from multiplying every
	// frame by subscribing it again under sid after sid.
	relaySocketMaxSubscriptions = 4
	// relaySocketReplyWindow is how long a forwarded frame's receipt subject may be published on.
	relaySocketReplyWindow = 10 * time.Minute
	// relaySocketMaxReplies bounds the receipt subjects one socket remembers; past it the oldest
	// is forgotten first.
	relaySocketMaxReplies = 1 << 16
	// relaySocketWriteWait bounds one write to the client, a frame or a control frame.
	relaySocketWriteWait = 10 * time.Second
	// relaySocketFlushWait bounds the flush that makes the server hold a sub, an unsub or a pub.
	relaySocketFlushWait = 5 * time.Second
	// relaySocketCheckWait bounds one ask of the broker.
	relaySocketCheckWait = 10 * time.Second
	// relaySocketCloseGrace is how long a socket the server closed waits for its client's close
	// frame before it drops the connection, so the close frame is read before the connection ends.
	relaySocketCloseGrace = time.Second
	// relaySocketPongFloor is the least the reader waits for a frame or a pong before it takes
	// the client for gone, whatever relaySocketCheckEvery a test gives it.
	relaySocketPongFloor = 10 * time.Second
)

// The close codes and reasons an open socket ends with, and what its client does on each: 4401
// reopens at once with a fresh trade; 4403 reopens with a fresh trade after 1 s, doubling to 60 s;
// 1013 after 5 s, doubling to 60 s; 1012 at once.
const (
	relaySocketCloseExpired = 4401
	relaySocketCloseRefused = 4403

	relaySocketReasonExpired           = "credential_expired"
	relaySocketReasonRefused           = "credential_refused"
	relaySocketReasonBrokerUnavailable = "broker_unavailable"
	relaySocketReasonNATSUnavailable   = "nats_unavailable"
	relaySocketReasonStopping          = "listener_stopping"
	relaySocketReasonTextOnly          = "text_frames_only"
)

// The codes of the err frame.
const (
	relayErrSubjectNotGranted = "subject_not_granted"
	relayErrPublishNotGranted = "publish_not_granted"
	relayErrBadFrame          = "bad_frame"
	relayErrSubscriptionLimit = "subscription_limit"
)

// relaySocketCheckBuild is a test build's check interval, as time.ParseDuration reads it, which a
// test that runs the listener's binary sets with -ldflags "-X main.relaySocketCheckBuild=1s" so a
// socket's token is asked about again within a second (relay_socket_live_test.go). It is empty in
// every other build, whose sockets check every relaySocketCheckDefault.
var relaySocketCheckBuild string

// relaySocketCheckInterval is relaySocketCheckBuild when a build set it, and relaySocketCheckDefault
// otherwise. A build that set it to anything but a positive duration is a broken build, and stops.
func relaySocketCheckInterval() time.Duration {
	if relaySocketCheckBuild == "" {
		return relaySocketCheckDefault
	}
	every, err := time.ParseDuration(relaySocketCheckBuild)
	if err != nil || every <= 0 {
		panic("main.relaySocketCheckBuild is " + strconv.Quote(relaySocketCheckBuild) + ", which is no positive duration")
	}
	return every
}

// relaySockets is an edge listener's relay sockets: the route that opens them and every one this
// replica holds open, so a stop closes each with 1012.
type relaySockets struct {
	client       *bus.Client
	brokers      *brokerTokens
	edgeProjects []string
	logger       *logging.Logger
	upgrader     websocket.Upgrader
	// relaySocketCheckEvery is how often an open socket pings its client and asks the broker
	// about its token: relaySocketCheckDefault, which only a test lowers.
	relaySocketCheckEvery time.Duration
	// now is the clock a socket judges its token's expiry by; a test moves it with the
	// verifier's.
	now func() time.Time

	mu      sync.Mutex
	open    map[string]map[*relaySocket]struct{}
	stopped bool
}

// newRelaySockets serves relay sockets on client's connection, admitting the callers brokers
// admits and asking it about each one again while it is open, for the projects edgeProjects
// lists.
func newRelaySockets(client *bus.Client, brokers *brokerTokens, edgeProjects []string, logger *logging.Logger) *relaySockets {
	return &relaySockets{
		client:       client,
		brokers:      brokers,
		edgeProjects: slices.Clone(edgeProjects),
		logger:       logger,
		upgrader: websocket.Upgrader{
			Subprotocols:     []string{relaySubprotocol},
			HandshakeTimeout: relaySocketWriteWait,
			// The socket is admitted by a DPoP-bound token and its proof, which a page cannot
			// present for another origin's key, and by no cookie, so there is no cross-site
			// request to fence by the Origin header; a client outside a browser sends one or
			// none as it likes.
			CheckOrigin: func(*http.Request) bool { return true },
		},
		relaySocketCheckEvery: relaySocketCheckInterval(),
		now:                   time.Now,
		open:                  map[string]map[*relaySocket]struct{}{},
	}
}

// relaySocketRoute is the handler of relaySocketPattern: the sockets' own on an edge listener, and
// on every other listener the 404 /v1 answers a path it serves nothing at.
func relaySocketRoute(sockets *relaySockets) http.Handler {
	if sockets == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSONError(w, http.StatusNotFound, "not found")
		})
	}
	return http.HandlerFunc(sockets.serve)
}

// serve opens one socket. The token was verified by apiAuth with its proof, as every /v1 request's
// is, and held to the edge's projects there; here it is held to the session the path names, and
// then the upgrade is refused 400 without the subprotocol, 429 past relaySocketMaxPerSession and
// 503 once the listener is stopping.
func (s *relaySockets) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session := r.PathValue("session")
	caller, ok := brokerCaller(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if refusal := sessionNotBound(r, s.logger, session); refusal != "" {
		writeJSONError(w, http.StatusForbidden, refusal)
		return
	}
	if !slices.Contains(websocket.Subprotocols(r), relaySubprotocol) {
		writeJSONError(w, http.StatusBadRequest, "the relay socket speaks the subprotocol "+relaySubprotocol)
		return
	}
	// The answer apiAuth's verify just read is the one this reads, from the verifier's cache: the
	// instant the token stops being live, which tells its expiry from a revoke once it is over.
	_, expires, err := s.brokers.verifier.Load().LiveUntil(r.Context(), r)
	if err != nil {
		refuseBrokerToken(w, s.logger, r, s.brokers, err)
		return
	}
	socket := &relaySocket{
		sockets:  s,
		session:  session,
		admitted: caller,
		expires:  expires,
		check:    r.Clone(context.Background()),
		subs:     map[int64]*nats.Subscription{},
		replies:  map[string]time.Time{},
		done:     make(chan struct{}),
	}
	if status, refusal := s.admit(socket); status != 0 {
		writeJSONError(w, status, refusal)
		return
	}
	defer s.release(socket)
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has answered the request itself.
		return
	}
	if !s.attach(socket, conn) {
		// The listener began to stop during the upgrade.
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseServiceRestart, relaySocketReasonStopping), time.Now().Add(relaySocketWriteWait))
		_ = conn.Close()
		return
	}
	s.logger.Info("listener: relay socket opened", slog.String("session_id", session))
	socket.read()
}

// attach gives socket the connection its upgrade made, unless the listener began to stop
// meanwhile. stop ends only the sockets attached, under the same lock, so it never reaches one
// without its connection.
func (s *relaySockets) attach(socket *relaySocket, conn *websocket.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return false
	}
	socket.conn = conn
	return true
}

// admit records socket as open, or answers the status it is refused with: 503 once the listener
// is stopping, 429 when its session already holds relaySocketMaxPerSession.
func (s *relaySockets) admit(socket *relaySocket) (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return http.StatusServiceUnavailable, "the listener is stopping; open the socket again on another replica"
	}
	held := s.open[socket.session]
	if len(held) >= relaySocketMaxPerSession {
		return http.StatusTooManyRequests, "this session already holds the most relay sockets a replica serves it"
	}
	if held == nil {
		held = map[*relaySocket]struct{}{}
		s.open[socket.session] = held
	}
	held[socket] = struct{}{}
	return 0, ""
}

// release forgets socket, whatever ended it.
func (s *relaySockets) release(socket *relaySocket) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.open[socket.session], socket)
	if len(s.open[socket.session]) == 0 {
		delete(s.open, socket.session)
	}
}

// stop closes every open socket with 1012, so each client opens it again at once on another
// replica, and refuses every upgrade after it 503. It returns once each close frame is written or
// its write has failed. It is safe to call more than once.
func (s *relaySockets) stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopped = true
	var open []*relaySocket
	for _, held := range s.open {
		for socket := range held {
			if socket.conn != nil {
				open = append(open, socket)
			}
		}
	}
	s.mu.Unlock()
	var ends sync.WaitGroup
	for _, socket := range open {
		ends.Go(func() { socket.end(websocket.CloseServiceRestart, relaySocketReasonStopping) })
	}
	ends.Wait()
}

// relaySocket is one open socket.
type relaySocket struct {
	sockets *relaySockets
	conn    *websocket.Conn
	session string
	// admitted is the caller the upgrade was admitted as, which every later answer is compared
	// with.
	admitted brokertoken.Caller
	// expires is when the upgrade's answer said the token stops being live, zero when it named no
	// expiry.
	expires time.Time
	// check is a detached copy of the upgrade's request, which every later ask of the broker
	// presents: its token, and no proof checked again, since that proof is spent.
	check *http.Request

	// writeMu orders the data frames written to the client, which gorilla/websocket takes from
	// one writer at a time: the reader's err frames and each subscription's msg frames. ended is
	// set under it, once, by end.
	writeMu sync.Mutex
	ended   bool

	subsMu sync.Mutex
	subs   map[int64]*nats.Subscription

	// repliesMu guards replies, the receipt subjects of the frames forwarded in the last
	// relaySocketReplyWindow, by when each was forwarded, and replyOrder, the same in the order
	// they were, oldest first, which the prune walks.
	repliesMu  sync.Mutex
	replies    map[string]time.Time
	replyOrder []forwardedReply
	// strayLogged is set once this socket has logged a message on its relay subject that was no
	// relay frame.
	strayLogged atomic.Bool

	endOnce sync.Once
	done    chan struct{}
}

// forwardedReply is one receipt subject and when its frame was forwarded.
type forwardedReply struct {
	subject string
	at      time.Time
}

// relayClientFrame is a frame the client sends.
type relayClientFrame struct {
	Op      string `json:"op"`
	SID     *int64 `json:"sid"`
	Subject string `json:"subject"`
	Data    []byte `json:"data"`
}

// relayMsgFrame is one relay frame, forwarded.
type relayMsgFrame struct {
	Op      string      `json:"op"`
	SID     int64       `json:"sid"`
	Subject string      `json:"subject"`
	Reply   string      `json:"reply"`
	Headers nats.Header `json:"headers"`
	Data    string      `json:"data"`
}

// relayErrFrame is the answer to a frame the socket did not take.
type relayErrFrame struct {
	Op   string `json:"op"`
	SID  *int64 `json:"sid,omitempty"`
	Code string `json:"code"`
}

// pongWait is how long the reader waits for any frame or pong before it takes the client for gone:
// three pings' worth, so one late pong closes nothing, and never under relaySocketPongFloor.
func (s *relaySockets) pongWait() time.Duration {
	return max(3*s.relaySocketCheckEvery, relaySocketPongFloor)
}

// read is the socket's one reader: it handles each frame whole before it reads the next, and
// returns once the connection ends.
func (socket *relaySocket) read() {
	conn := socket.conn
	conn.SetReadLimit(relaySocketReadLimit)
	extend := func() { _ = conn.SetReadDeadline(time.Now().Add(socket.sockets.pongWait())) }
	extend()
	conn.SetPongHandler(func(string) error {
		extend()
		return nil
	})
	go socket.watch()
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			// The client's close frame, which gorilla answers, a frame past the read limit, which
			// gorilla answers 1009, or a connection that ended: the socket ends with nothing more
			// to write.
			socket.end(0, "")
			_ = conn.Close()
			return
		}
		extend()
		if kind != websocket.TextMessage {
			socket.end(websocket.CloseUnsupportedData, relaySocketReasonTextOnly)
			continue
		}
		socket.handle(data)
	}
}

// handle takes one client frame.
func (socket *relaySocket) handle(data []byte) {
	select {
	case <-socket.done:
		// The socket has ended; a frame its client sent before the close is not taken.
		return
	default:
	}
	var frame relayClientFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		socket.badFrame(nil)
		return
	}
	switch frame.Op {
	case "sub":
		socket.subscribe(frame)
	case "unsub":
		socket.unsubscribe(frame)
	case "pub":
		socket.publish(frame)
	default:
		socket.badFrame(frame.SID)
	}
}

// badFrame answers a frame the socket cannot read as the wire's - one that is not JSON, of an
// unknown op, a sub or unsub naming no sid, or a sub of a sid the socket already holds - with the
// err frame bad_frame, under sid when it named one, and closes the socket 1008 with that reason:
// its client speaks something else, and nothing it sends next can be read either.
func (socket *relaySocket) badFrame(sid *int64) {
	socket.refuse(sid, relayErrBadFrame)
	socket.end(websocket.ClosePolicyViolation, relayErrBadFrame)
}

// subscribe takes a sub: the socket's own relay subject alone, subscribed on this replica's
// connection and flushed before the reader reads on.
func (socket *relaySocket) subscribe(frame relayClientFrame) {
	if frame.SID == nil {
		socket.badFrame(nil)
		return
	}
	sid := *frame.SID
	subject := bus.DeliverSubject(socket.session)
	if frame.Subject != subject {
		socket.refuse(frame.SID, relayErrSubjectNotGranted)
		return
	}
	socket.subsMu.Lock()
	_, held := socket.subs[sid]
	full := len(socket.subs) >= relaySocketMaxSubscriptions
	socket.subsMu.Unlock()
	switch {
	case held:
		socket.badFrame(frame.SID)
		return
	case full:
		// The socket stays open: its subscriptions up to the bound go on.
		socket.refuse(frame.SID, relayErrSubscriptionLimit)
		return
	}
	conn := socket.sockets.client.Connection()
	sub, err := conn.Subscribe(subject, func(msg *nats.Msg) { socket.forward(sid, msg) })
	if err != nil {
		socket.endNATS("subscribe", err)
		return
	}
	if err := conn.FlushTimeout(relaySocketFlushWait); err != nil {
		_ = sub.Unsubscribe()
		socket.endNATS("flush a subscribe", err)
		return
	}
	// A subscription the server's grant refuses is refused asynchronously, ahead of the flush's
	// answer, and nats.go keeps the refusal as the connection's last error.
	if denied := conn.LastError(); errors.Is(denied, nats.ErrPermissionViolation) && strings.Contains(denied.Error(), `Subscription to "`+subject+`"`) {
		_ = sub.Unsubscribe()
		socket.sockets.logger.Warn("listener: the relay socket's subscription was refused by the NATS server",
			slog.String("session_id", socket.session), slog.String("error", denied.Error()))
		socket.refuse(frame.SID, relayErrSubjectNotGranted)
		return
	}
	socket.subsMu.Lock()
	defer socket.subsMu.Unlock()
	if socket.subs == nil {
		// The socket ended while the subscribe was in flight.
		_ = sub.Unsubscribe()
		return
	}
	socket.subs[sid] = sub
}

// unsubscribe takes an unsub: the subscription of that sid ends, and the flush that makes the
// server drop it comes before the reader reads on. A sid the socket does not hold is nothing.
func (socket *relaySocket) unsubscribe(frame relayClientFrame) {
	if frame.SID == nil {
		socket.badFrame(nil)
		return
	}
	socket.subsMu.Lock()
	sub := socket.subs[*frame.SID]
	delete(socket.subs, *frame.SID)
	socket.subsMu.Unlock()
	if sub == nil {
		return
	}
	_ = sub.Unsubscribe()
	if err := socket.sockets.client.Connection().FlushTimeout(relaySocketFlushWait); err != nil {
		socket.endNATS("flush an unsubscribe", err)
	}
}

// publish takes a pub: a receipt of a frame this socket forwarded, published on this replica's
// connection and flushed before the reader reads on.
func (socket *relaySocket) publish(frame relayClientFrame) {
	if !socket.forwarded(frame.Subject) {
		socket.refuse(nil, relayErrPublishNotGranted)
		return
	}
	conn := socket.sockets.client.Connection()
	if err := conn.Publish(frame.Subject, frame.Data); err != nil {
		socket.endNATS("publish a receipt", err)
		return
	}
	if err := conn.FlushTimeout(relaySocketFlushWait); err != nil {
		socket.endNATS("flush a receipt", err)
	}
}

// forward writes one message of a subscription to the client, on the subscription's own goroutine:
// a relay frame alone, never the connection probe.
func (socket *relaySocket) forward(sid int64, msg *nats.Msg) {
	if _, probe := msg.Header[bus.ConnectionProbeHeader]; probe {
		return
	}
	if !strings.HasPrefix(msg.Reply, bus.RelayReplyPrefix) {
		socket.stray(msg)
		return
	}
	socket.remember(msg.Reply)
	headers := msg.Header
	if headers == nil {
		headers = nats.Header{}
	}
	frame := relayMsgFrame{
		Op: "msg", SID: sid, Subject: msg.Subject, Reply: msg.Reply, Headers: headers,
		Data: base64.StdEncoding.EncodeToString(msg.Data),
	}
	if err := socket.write(frame); err != nil && !errors.Is(err, errRelaySocketEnded) {
		socket.sockets.logger.Warn("listener: the relay socket could not write a frame; it is closed",
			slog.String("session_id", socket.session), slog.String("error", err.Error()))
		socket.end(0, "")
	}
}

// stray logs, once a socket, a message on the relay subject that is no relay frame: one whose
// reply is no relay receipt, which this socket could not publish an answer to.
func (socket *relaySocket) stray(msg *nats.Msg) {
	if !socket.strayLogged.Swap(true) {
		socket.sockets.logger.Warn("listener: the relay socket passed over a message on its relay subject whose reply is no relay receipt",
			slog.String("session_id", socket.session), slog.String("reply", msg.Reply))
	}
}

// remember records reply as a receipt subject the client may publish on, and forgets the ones
// older than relaySocketReplyWindow.
func (socket *relaySocket) remember(reply string) {
	now := socket.sockets.now()
	socket.repliesMu.Lock()
	defer socket.repliesMu.Unlock()
	socket.replies[reply] = now
	socket.replyOrder = append(socket.replyOrder, forwardedReply{subject: reply, at: now})
	for len(socket.replyOrder) > 0 {
		oldest := socket.replyOrder[0]
		if now.Sub(oldest.at) <= relaySocketReplyWindow && len(socket.replyOrder) <= relaySocketMaxReplies {
			break
		}
		if socket.replies[oldest.subject].Equal(oldest.at) {
			delete(socket.replies, oldest.subject)
		}
		socket.replyOrder = socket.replyOrder[1:]
	}
}

// forwarded reports whether subject is the receipt subject of a frame this socket forwarded in the
// last relaySocketReplyWindow.
func (socket *relaySocket) forwarded(subject string) bool {
	socket.repliesMu.Lock()
	defer socket.repliesMu.Unlock()
	at, ok := socket.replies[subject]
	return ok && socket.sockets.now().Sub(at) <= relaySocketReplyWindow
}

// errRelaySocketEnded is a write to a socket that has ended.
var errRelaySocketEnded = errors.New("the relay socket has ended")

// write writes one data frame to the client.
func (socket *relaySocket) write(frame any) error {
	socket.writeMu.Lock()
	defer socket.writeMu.Unlock()
	if socket.ended {
		return errRelaySocketEnded
	}
	if err := socket.conn.SetWriteDeadline(time.Now().Add(relaySocketWriteWait)); err != nil {
		return err
	}
	return socket.conn.WriteJSON(frame)
}

// refuse answers a frame the socket did not take, under sid when it named one.
func (socket *relaySocket) refuse(sid *int64, code string) {
	if err := socket.write(relayErrFrame{Op: "err", SID: sid, Code: code}); err != nil && !errors.Is(err, errRelaySocketEnded) {
		socket.end(0, "")
	}
}

// endNATS ends the socket 1013 over a NATS call that did not complete, which says nothing about
// the session: its client opens it again after 5 s, on whichever replica answers.
func (socket *relaySocket) endNATS(step string, err error) {
	socket.sockets.logger.Warn("listener: the relay socket could not reach NATS; it is closed",
		slog.String("session_id", socket.session), slog.String("step", step), slog.String("error", err.Error()))
	socket.end(websocket.CloseTryAgainLater, relaySocketReasonNATSUnavailable)
}

// end ends the socket once: it forwards nothing more, drops every subscription, writes the close
// frame of code when code is not 0, and drops the connection once the client has answered it or
// relaySocketCloseGrace has passed.
func (socket *relaySocket) end(code int, reason string) {
	socket.endOnce.Do(func() {
		socket.writeMu.Lock()
		socket.ended = true
		socket.writeMu.Unlock()
		socket.subsMu.Lock()
		subs := socket.subs
		socket.subs = nil
		socket.subsMu.Unlock()
		for _, sub := range subs {
			_ = sub.Unsubscribe()
		}
		close(socket.done)
		if code == 0 {
			return
		}
		socket.sockets.logger.Info("listener: relay socket closed",
			slog.String("session_id", socket.session), slog.Int("code", code), slog.String("reason", reason))
		// The close frame's write is bounded by the grace too, so a stop that closes every
		// socket at once spends at most that on a client that reads nothing.
		_ = socket.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(relaySocketCloseGrace))
		time.AfterFunc(relaySocketCloseGrace, func() { _ = socket.conn.Close() })
	})
}

// watch is the socket's tick: every relaySocketCheckEvery it asks the broker about the socket's
// token and closes it on the verdict, closes it 1013 once a subscription has ended under it (its
// connection closed and was replaced), and otherwise pings the client.
func (socket *relaySocket) watch() {
	ticker := time.NewTicker(socket.sockets.relaySocketCheckEvery)
	defer ticker.Stop()
	for {
		select {
		case <-socket.done:
			return
		case <-ticker.C:
		}
		if code, reason := socket.verdict(); code != 0 {
			socket.end(code, reason)
			return
		}
		if !socket.subscribed() {
			socket.end(websocket.CloseTryAgainLater, relaySocketReasonNATSUnavailable)
			return
		}
		if err := socket.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(relaySocketWriteWait)); err != nil {
			socket.end(0, "")
			_ = socket.conn.Close()
			return
		}
	}
}

// subscribed reports whether every subscription the socket holds is still the server's.
func (socket *relaySocket) subscribed() bool {
	socket.subsMu.Lock()
	defer socket.subsMu.Unlock()
	for _, sub := range socket.subs {
		if !sub.IsValid() {
			return false
		}
	}
	return true
}

// verdict asks the broker about the socket's token again, the answer it gave reused for at most
// brokertoken.AffirmativeCacheTTL and no proof checked a second time, and answers the close code
// and reason the socket ends with, 0 while it stays open:
//
//   - the token's expiry has passed and the broker no longer holds it live: 4401, on which the
//     client trades for a fresh token and opens again at once;
//   - the broker no longer holds it live before its expiry (a revoke, a login ended early), or
//     holds it as something no session's token is: 4403;
//   - the answer no longer passes the edge's admission (EdgeAdmits), or names another caller
//     than the one the socket was admitted as (its session, its person or service, its project
//     scope and its projects): 4403;
//   - the broker could not be asked: 1013, and every upgrade meanwhile is refused 503 by apiAuth.
func (socket *relaySocket) verdict() (int, string) {
	ctx, cancel := context.WithTimeout(context.Background(), relaySocketCheckWait)
	defer cancel()
	caller, _, err := socket.sockets.brokers.verifier.Load().LiveUntil(ctx, socket.check)
	var why string
	switch {
	case err == nil:
		if refusal := EdgeAdmits(socket.sockets.edgeProjects, caller); refusal != nil {
			why = refusal.Error()
		} else if !sameRelayCaller(caller, socket.admitted) {
			why = "the broker's answer names another caller than the socket was opened as"
		} else {
			return 0, ""
		}
	case errors.Is(err, brokertoken.ErrNotActive):
		if !socket.expires.IsZero() && !socket.sockets.now().Before(socket.expires) {
			return relaySocketCloseExpired, relaySocketReasonExpired
		}
		why = err.Error()
	case errors.Is(err, brokertoken.ErrNotASession), errors.Is(err, brokertoken.ErrKeyMismatch):
		why = err.Error()
	default:
		socket.sockets.logger.Warn("listener: the relay socket could not ask the broker about its token; it is closed",
			slog.String("session_id", socket.session), slog.String("reason", brokerTokenReason(err)))
		return websocket.CloseTryAgainLater, relaySocketReasonBrokerUnavailable
	}
	socket.sockets.logger.Warn("listener: the relay socket's token no longer admits it; it is closed",
		slog.String("session_id", socket.session), slog.String("why", why))
	return relaySocketCloseRefused, relaySocketReasonRefused
}

// sameRelayCaller reports whether a later answer names the caller the socket was admitted as.
func sameRelayCaller(answer, admitted brokertoken.Caller) bool {
	return answer.Session == admitted.Session && answer.Person == admitted.Person && answer.Service == admitted.Service &&
		answer.ProjectScope == admitted.ProjectScope && answer.ReadOnly == admitted.ReadOnly &&
		slices.Equal(answer.Projects, admitted.Projects)
}
