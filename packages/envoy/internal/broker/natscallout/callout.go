package natscallout

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/sjawhar/envoy/internal/broker/config"
	"github.com/sjawhar/envoy/internal/bus"
)

const (
	// authSubject is the subject the server sends each authorization request on.
	authSubject = "$SYS.REQ.USER.AUTH"
	// queueGroup is the queue group every replica subscribes authSubject in, so the server's
	// request reaches one replica and a stopped replica is sent none.
	queueGroup = "envoy-auth"
	// serverXKeyHeader carries the server's curve key on a request it sealed to the service's.
	serverXKeyHeader = "Nats-Server-Xkey"
	// globalAccount is the account every admitted user is placed in: the hub keeps one, the
	// global account, and its streams and every static user's grant live there.
	globalAccount = "$G"
	// connectionName is the name the service's own connection carries in the server's listings.
	connectionName = "envoy-broker-callout"
)

// concurrentDecisions bounds the decisions one replica makes at once: a reconnect storm after a
// server restart sends every connection's request together. It is a var so a test can make
// requests queue for a slot with a handful of them (queue_test.go).
var concurrentDecisions = 64

// maxWaiting bounds the requests one replica holds waiting for a decision slot; the one past it is
// refused at once (waitingRefusal). Each waiting request holds a goroutine, its sealed request and
// a deadline until a slot frees or its budget ends, and a client holding no credential can send one
// per TCP connect, so with no bound what a replica holds grows with whatever such a client sends.
// The bound is sixteen times the slots: at the slowest decision this package's storm measured
// (146 ms), concurrentDecisions slots decide about 64 x 1.5 s / 146 ms = 650 requests inside one
// budget, so a request behind 1024 others would be refused at its budget anyway. At faster
// decisions the bound refuses some that could have waited; their clients retry a refusal
// (IgnoreAuthErrorAbort) and hear it at once rather than after their budget. It is a var so a test
// can fill it with a handful of requests (queue_test.go).
var maxWaiting = 1024

// waitingRefusal is what a request that arrives with maxWaiting others already waiting is refused
// with. Its label in the decision counter is labelWaitingBound.
func waitingRefusal() *Refusal {
	return refuse(labelWaitingBound, fmt.Sprintf("%d requests already wait for a decision slot on this replica, the most it holds", maxWaiting))
}

// decideBudget is how long one decision may take. A Go client fails its connect when the server's
// answer arrives later than two seconds: nats.go sets one deadline of Opts.Timeout, 2 s by
// default, over the whole handshake (nats.go, processConnectInit), and the server sends its PONG
// only once the callout answers; the server gives up on its own at its authorization timeout,
// nats-server's default of 2 s (server/const.go, AUTH_TIMEOUT), which the hub takes by naming
// none. So the decision gets 1.5 s, and the dial, the request's trip here and the answer's trip
// back get the other 0.5 s. A decision that runs past it is answered with a refusal naming the
// step it was in, never with a late answer.
//
// It is a var so a test can raise it: at 2.5 s the same blocking stub makes a client fail its
// connect, which is the control this budget exists for (deadline_test.go).
var decideBudget = 1500 * time.Millisecond

// budgetRefusal is what a decision that ran past decideBudget is refused with, naming the step it
// was in. Its label in the decision counter is labelBudget.
func budgetRefusal(step string) *Refusal {
	return refuse(labelBudget, fmt.Sprintf("decision exceeded its %s budget at %s", decideBudget, step))
}

// drainBound bounds how long Close waits for the subscription to finish draining the requests the
// hub had already routed to this replica. Each one past its budget is refused at once without a
// decision (answer), so a long queue drains in far less than this.
const drainBound = 5 * time.Second

// minimumDecision is the least of its budget a request must have left when it gets a decision
// slot for its decision to start. Less than that, and the wait for the slot spent the budget: the
// request is refused naming the wait, rather than starting an introspection that its deadline
// cancels and naming that.
const minimumDecision = 100 * time.Millisecond

// holdDelivery, when a test sets it, runs at the start of every delivery of a request to replica
// s, before the request's budget starts; a test that blocks in it holds the hub's requests in that
// replica's subscription, which is where a replica that drops rather than drains them on Close
// loses them (queue_test.go). Nil outside tests.
var holdDelivery func(s *Service)

// Settings is where the service connects and the keys it holds.
type Settings struct {
	// URLs are the hub's NATS servers.
	URLs []string
	// SeedFile holds the service's own NATS user seed, a user the hub lists as an auth user, so
	// it is never sent to itself; IssuerSeedFile the account seed that signs every answer and
	// every user it admits, the hub's auth_callout issuer; XKeySeedFile the curve seed the hub
	// seals each request to, read by nkeys.FromSeed, which derives the curve key a curve seed
	// names (nkeys.FromCurveSeed).
	SeedFile, IssuerSeedFile, XKeySeedFile string
	// Services are the services it admits by platform identity (ParseServices).
	Services []ServiceEntry
}

// Service is the running auth service: one connection to the hub, subscribed to the authorization
// subject in the replicas' queue group.
type Service struct {
	conn  *nats.Conn
	sub   *nats.Subscription
	slots chan struct{}
	wait  sync.WaitGroup
	// waiting counts the requests waiting for a decision slot, which maxWaiting bounds.
	waiting atomic.Int64
	// beat is when the heartbeat last took a decision slot, in Unix nanoseconds, and done ends
	// the heartbeat at Close.
	beat      atomic.Int64
	done      chan struct{}
	closeOnce sync.Once
}

// livenessTick is how often the heartbeat tries to take a decision slot, and livenessBound how
// old its last beat may be before the service reads as stalled. Vars so a test can make a stall
// read stale in milliseconds (liveness_test.go).
var (
	livenessTick  = time.Second
	livenessBound = 10 * time.Second
)

// beatWhileTurning is the heartbeat: every livenessTick it takes a decision slot and gives it back,
// and records the time it did. When the loop turns a slot is free within a tick, idle or busy, so
// the beat stays fresh; when every slot is held by a decision that never ends, no request can be
// decided and the beat goes stale, which Livez reports. A process stopped outright never answers
// the probe at all, which a liveness probe also reads as a failure. Start stores the first beat
// itself, before anything can read it.
func (s *Service) beatWhileTurning() {
	ticker := time.NewTicker(livenessTick)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
		}
		select {
		case s.slots <- struct{}{}:
			<-s.slots
			s.beat.Store(time.Now().UnixNano())
		case <-s.done:
			return
		}
	}
}

// Livez is GET /livez on the broker's metrics port, its whole answer: 200 "ok" while the auth
// service's loop turned within livenessBound, 503 with a line naming how long ago it last turned
// once it has not, and 200 when service answers nil, a broker that runs no auth service or has
// not started it yet, since nothing then can stall. A liveness probe that reads the status kills a
// replica whose loop has stalled: its connection to the hub then resets and it leaves the queue
// group, where a stalled replica would keep being sent requests it never answers.
func Livez(service func() *Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if s := service(); s != nil {
			if age := time.Since(time.Unix(0, s.beat.Load())); age > livenessBound {
				http.Error(w, fmt.Sprintf("the NATS auth service's loop last turned %s ago", age.Round(time.Second)), http.StatusServiceUnavailable)
				return
			}
		}
		_, _ = w.Write([]byte("ok\n"))
	})
}

// decideFunc is how the service decides a request: Decide, unless a test replaces it to hand the
// answer a grant of its own.
type decideFunc func(ctx context.Context, deps Deps, services []ServiceEntry, req Request) (Decision, error)

// Start connects to the hub as the service's own user and answers every authorization request it
// is sent, in the replicas' queue group, until Close. It reads the three seeds first, refusing a
// file that is unreadable or holds a seed of the wrong kind (a user, an account, a curve key) by
// the setting's own name and never the seed. The hub need not answer yet: the connection is made
// in the background and retried, so a hub that is down or restarting when the broker starts is
// answered once it is up.
func Start(ctx context.Context, settings Settings, deps Deps) (*Service, error) {
	return start(ctx, settings, deps, Decide)
}

func start(_ context.Context, settings Settings, deps Deps, decide decideFunc) (*Service, error) {
	issuer, err := readSeed("BROKER_NATS_ISSUER_SEED_FILE", settings.IssuerSeedFile, nkeys.PrefixByteAccount)
	if err != nil {
		return nil, err
	}
	xkey, err := readSeed("BROKER_NATS_XKEY_SEED_FILE", settings.XKeySeedFile, nkeys.PrefixByteCurve)
	if err != nil {
		return nil, err
	}
	if _, err := readSeed("BROKER_NATS_SEED_FILE", settings.SeedFile, nkeys.PrefixByteUser); err != nil {
		return nil, err
	}
	// The service's own connection is the bus's: its seed by file, its reconnects, its silent-cut
	// detection, and the hub reached wherever it is, since the broker and the hub are separate
	// deployments.
	lookup := func(name string) (string, bool) {
		switch name {
		case "NATS_NKEY_SEED_FILE":
			return settings.SeedFile, true
		case bus.AllowRemoteEnvVar:
			return "1", true
		}
		return "", false
	}
	// WithImmediateReconnect: every other client of the hub waits on this one. A restart drops
	// the service's own connection with theirs, and a client that comes back before the service
	// has resubscribed is refused until it does, so the service retries at once where they wait a
	// second (internal/bus).
	conn, err := bus.Dial(connectionName, settings.URLs, lookup,
		bus.WithConnectInBackground(), bus.WithSilentCutDetection(), bus.WithImmediateReconnect())
	if err != nil {
		return nil, fmt.Errorf("natscallout: connect to %s: %w", strings.Join(settings.URLs, ","), err)
	}
	s := &Service{conn: conn, slots: make(chan struct{}, concurrentDecisions), done: make(chan struct{})}
	s.beat.Store(time.Now().UnixNano())
	answer := &answerer{issuer: issuer, xkey: xkey, deps: deps, services: settings.Services, decide: decide}
	s.sub, err = conn.QueueSubscribe(authSubject, queueGroup, func(msg *nats.Msg) {
		if holdDelivery != nil {
			holdDelivery(s)
		}
		// The budget counts from here, and it is one clock: the deadline below bounds both the
		// wait for a decision slot and the decision itself. The callback never blocks: nats.go
		// delivers one message at a time on this goroutine, so a callback that waited here for a
		// slot would hold back the next request's arrival too, and that request's budget would
		// start late. Each request waits for its slot on a goroutine of its own, and one still
		// waiting when its budget is spent is answered with a refusal without one (answer).
		received := time.Now()
		// Past maxWaiting the request is refused here, on the delivery goroutine, with no
		// goroutine of its own: opening, signing and sealing a refusal takes no broker call, and
		// what a flood of them costs then is this one goroutine's time, which nats.go's pending
		// limits bound, rather than a goroutine each.
		if s.waiting.Add(1) > int64(maxWaiting) {
			s.waiting.Add(-1)
			answer.answer(context.Background(), msg, received, waitingRefusal())
			return
		}
		ctx, cancel := context.WithDeadline(context.Background(), received.Add(decideBudget))
		// Added on the delivery goroutine Close waits out, so every Add happens before its Wait.
		s.wait.Add(1)
		go func() {
			defer s.wait.Done()
			defer cancel()
			select {
			case s.slots <- struct{}{}:
				s.waiting.Add(-1)
				defer func() { <-s.slots }()
			case <-ctx.Done():
				s.waiting.Add(-1)
			}
			answer.answer(ctx, msg, received, nil)
		}()
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("natscallout: subscribe %s: %w", authSubject, err)
	}
	go s.beatWhileTurning()
	return s, nil
}

// Close stops answering.
//
// Connected, it drains the subscription: that leaves the queue group at once, so the hub sends the
// next request to another replica, and still delivers every request the hub had already routed
// here, which an unsubscribe would drop and leave each of those clients waiting out the hub's
// whole authorization timeout. The subscription reports itself closed once the last of them has
// reached the callback, so the wait that follows covers every decision started.
//
// Reconnecting, it unsubscribes instead. A drain then would leave the subscription among the
// connection's own, which nats.go sends again on the reconnect (nats.go v1.53.1,
// resendSubscriptions, which does not check draining), putting this closing replica back in the
// queue group; the drain's end then removes it on the client's side only, and every request the
// hub routes here after that has no subscriber. An unsubscribe removes it from the connection, so
// the reconnect never sends it, and while the connection is down the hub can route nothing here.
func (s *Service) Close() {
	s.closeOnce.Do(s.close)
}

// close is Close's body, which runs once however many times Close is called: a caller that stops
// a replica early and a cleanup that stops it at the end may both call Close.
func (s *Service) close() {
	close(s.done)
	if s.conn.IsConnected() {
		closed := s.sub.StatusChanged(nats.SubscriptionClosed)
		if s.sub.Drain() == nil {
			select {
			case <-closed:
			case <-time.After(drainBound):
			}
		}
	}
	// After a drain this removes nothing; without one, or after a drain that outran drainBound, it
	// is what keeps the subscription from being sent again.
	_ = s.sub.Unsubscribe()
	s.wait.Wait()
	s.conn.Close()
}

// readyPoll is how often Ready asks whether the service's subscription has reached the hub, and
// readyFlush how long one ask waits for the hub's answer.
const (
	readyPoll  = 50 * time.Millisecond
	readyFlush = time.Second
)

// Ready returns once the service's subscription has reached the hub, so the next connection the
// hub authorizes is sent to it, or ctx's error when ctx ends first; ctx needs no deadline, as the
// broker's own serving context has none. Start does not wait for it, since a hub that is down
// when the broker starts is answered once it is up; the broker logs when it is (cmd/broker).
func (s *Service) Ready(ctx context.Context) error {
	for {
		if s.conn.IsConnected() && s.conn.FlushTimeout(readyFlush) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readyPoll):
		}
	}
}

// readSeed is config.ReadSeedFile, the one reader of the auth service's seeds, which the broker's
// start also runs (config.loadNATSCallout).
var readSeed = config.ReadSeedFile

// answerer answers one authorization request.
type answerer struct {
	issuer, xkey nkeys.KeyPair
	deps         Deps
	services     []ServiceEntry
	decide       decideFunc
}

// answer opens msg, decides it and answers it, sealed to the server's key. ctx carries the
// request's budget, which counts from received, when the request reached the subscription, so the
// time it waited for a decision slot is spent from it; a request past its deadline before its
// decision starts is refused at once, without asking the broker anything. The request's own expiry
// is not used: the server writes it in whole seconds (server/auth_callout.go, claim.Expires), so it
// can name a moment up to a second before the server actually gives up, and would cut a budget of
// 1.5 s by as much. A request it cannot open or read is not answered: the server refuses that
// connection at its authorization timeout, and the broker's log says why. refusal, when it is not
// nil, is the answer already settled before any decision (waitingRefusal), and nothing is decided.
func (a *answerer) answer(ctx context.Context, msg *nats.Msg, received time.Time, refusal *Refusal) {
	serverXKey := msg.Header.Get(serverXKeyHeader)
	data := msg.Data
	if serverXKey != "" {
		opened, err := a.xkey.Open(data, serverXKey)
		if err != nil {
			a.log("natscallout: an authorization request did not open with the service's curve key", "error", err.Error())
			return
		}
		data = opened
	}
	claims, err := jwt.DecodeAuthorizationRequestClaims(string(data))
	if err != nil {
		a.log("natscallout: an authorization request is no authorization request JWT", "error", err.Error())
		return
	}
	req := Request{
		UserNkey: claims.UserNkey, ServerID: claims.Server.ID, ServerXKey: serverXKey,
		Nonce: claims.ClientInformation.Nonce, Token: claims.ConnectOptions.Token, Sig: claims.ConnectOptions.SignedNonce,
		User: claims.ConnectOptions.Username, Password: claims.ConnectOptions.Password, Host: claims.ClientInformation.Host,
	}
	kind := credentialKind(req)
	var decision Decision
	var refused *Refusal
	switch {
	case serverXKey == "":
		// The service always holds a curve key, and a hub configured with it seals every request,
		// so a request in the clear means the hub's callout lost its xkey and every token and proof
		// is crossing the wire unencrypted. Refused, and said loudly.
		refused = refuse(labelUnsealed, "the authorization request was not sealed to the service's key")
		if a.deps.Log != nil {
			a.deps.Log.Error("natscallout: an authorization request arrived in the clear; the hub's auth_callout names no xkey, so every token and proof crosses the wire unencrypted", "host", req.Host)
		}
	case refusal != nil:
		refused = refusal
	case time.Since(received) > decideBudget-minimumDecision:
		refused = budgetRefusal("the wait for a decision slot")
	default:
		decision, err = a.decide(ctx, a.deps, a.services, req)
		refused = asRefusal(err)
		// A decision that ran past the deadline is refused, whatever it decided: the client is at
		// or past the point its connect gives up, and admitting it there would hand the server a
		// user for a connection that is no longer waiting.
		if refused == nil && ctx.Err() != nil {
			refused = budgetRefusal("the answer")
		}
	}
	user, refused := admit(req, decision, refused)
	if refused != nil {
		fields := []any{"reason", refused.Reason, "label", refused.Label, "host", req.Host, "kind", kind}
		if refused.Cause != nil {
			fields = append(fields, "cause", refused.Cause.Error())
		}
		a.log("natscallout: refused a NATS connection", fields...)
	}
	a.deps.Metrics.record(kind, refused, time.Since(received))
	encoded, err := sign(a.issuer, req, user, refused)
	if err != nil {
		a.log("natscallout: an answer could not be encoded", "error", err.Error(), "host", req.Host)
		return
	}
	out := []byte(encoded)
	if serverXKey != "" {
		if out, err = a.xkey.Seal(out, serverXKey); err != nil {
			a.log("natscallout: an answer could not be sealed to the server's key", "error", err.Error())
			return
		}
	}
	if err := msg.Respond(out); err != nil {
		a.log("natscallout: an answer could not be sent", "error", err.Error(), "host", req.Host)
	}
}

func (a *answerer) log(message string, args ...any) {
	if a.deps.Log != nil {
		a.deps.Log.Warn(message, args...)
	}
}

// asRefusal is err as the refusal it answers: itself when it is one, and a refusal naming the
// service's own failure, with err as its cause, when it is not.
func asRefusal(err error) *Refusal {
	if err == nil {
		return nil
	}
	var refused *Refusal
	if errors.As(err, &refused) {
		return refused
	}
	return refuse(labelServiceFailed, "the service failed").because(err)
}

// admit is the verdict an answer carries, settled before it is counted: refused when the decision
// refused, and otherwise the user decision admits, once the grant guard (guard) passes it. Every
// answer the service sends passes here, whatever decided it, so this is the one place the guard
// runs.
func admit(req Request, decision Decision, refused *Refusal) (*jwt.UserClaims, *Refusal) {
	if refused != nil {
		return nil, refused
	}
	if refused := guard(decision.Grant); refused != nil {
		return nil, refused
	}
	user := jwt.NewUserClaims(req.UserNkey)
	user.Audience = globalAccount
	user.Name = decision.Name
	user.Expires = decision.Expires.Unix()
	user.Pub.Allow.Add(decision.Grant.Publish...)
	user.Sub.Allow.Add(decision.Grant.Subscribe...)
	user.Pub.Deny.Add(decision.Grant.PublishDeny...)
	user.Sub.Deny.Add(decision.Grant.SubscribeDeny...)
	return user, nil
}

// sign is the authorization response for req, signed by issuer: user, signed by issuer for req's
// user key, or, when refused is not nil, refused's one line as the response's error.
func sign(issuer nkeys.KeyPair, req Request, user *jwt.UserClaims, refused *Refusal) (string, error) {
	response := jwt.NewAuthorizationResponseClaims(req.UserNkey)
	if response == nil {
		return "", errors.New("natscallout: the request names no user key")
	}
	response.Audience = req.ServerID
	if refused != nil {
		response.Error = refused.Reason
		return response.Encode(issuer)
	}
	userJWT, err := user.Encode(issuer)
	if err != nil {
		return "", err
	}
	response.Jwt = userJWT
	return response.Encode(issuer)
}
