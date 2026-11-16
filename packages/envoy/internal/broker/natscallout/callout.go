package natscallout

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/natsgrant"
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
	// concurrentDecisions bounds the decisions one replica makes at once: a reconnect storm after
	// a server restart sends every connection's request together.
	concurrentDecisions = 64
)

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
// was in. Its label in the decision counter is "budget" (reasonLabel).
func budgetRefusal(kind, step string) *Refusal {
	return refuse(kind, fmt.Sprintf("decision exceeded its %s budget at %s", decideBudget, step))
}

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
	// stopHeld ends the asks about the projects a held entry can name (heldAccess.run), and
	// heldDone is closed once they have ended.
	stopHeld context.CancelFunc
	heldDone chan struct{}
}

// decideFunc is how the service decides a request: Decide, unless a test of the guard replaces
// it.
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

func start(ctx context.Context, settings Settings, deps Deps, decide decideFunc) (*Service, error) {
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
	s := &Service{conn: conn, slots: make(chan struct{}, concurrentDecisions), stopHeld: func() {}, heldDone: make(chan struct{})}
	// Dispatch's answers about the projects of the entries held to their exchange projects are
	// asked for in the background from now on, so no decision waits on Dispatch (scopeGrant).
	if deps.held = newHeldAccess(deps, settings.Services); deps.held != nil {
		var asks context.Context
		asks, s.stopHeld = context.WithCancel(context.WithoutCancel(ctx))
		go func() { defer close(s.heldDone); deps.held.run(asks) }()
	} else {
		close(s.heldDone)
	}
	answer := &answerer{issuer: issuer, xkey: xkey, deps: deps, services: settings.Services, decide: decide}
	s.sub, err = conn.QueueSubscribe(authSubject, queueGroup, func(msg *nats.Msg) {
		s.slots <- struct{}{}
		s.wait.Add(1)
		go func() {
			defer func() { <-s.slots; s.wait.Done() }()
			answer.answer(msg)
		}()
	})
	if err != nil {
		s.stopHeld()
		<-s.heldDone
		conn.Close()
		return nil, fmt.Errorf("natscallout: subscribe %s: %w", authSubject, err)
	}
	return s, nil
}

// Close stops answering: it leaves the queue group, so the server sends the next request to
// another replica, waits for the decisions in flight to answer, and closes the connection.
func (s *Service) Close() {
	_ = s.sub.Unsubscribe()
	s.wait.Wait()
	s.stopHeld()
	<-s.heldDone
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

// readSeed is the key pair the seed in the file at path makes, refused, naming setting, when the
// file cannot be read or its seed is not of kind.
func readSeed(setting, path string, kind nkeys.PrefixByte) (nkeys.KeyPair, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("natscallout: %s names %s, which could not be read: %w", setting, path, err)
	}
	seed := []byte(strings.TrimSpace(string(raw)))
	prefix, _, err := nkeys.DecodeSeed(seed)
	if err != nil {
		return nil, fmt.Errorf("natscallout: %s names %s, which holds no nkey seed", setting, path)
	}
	if prefix != kind {
		return nil, fmt.Errorf("natscallout: %s names %s, which holds a %s seed where it needs a %s seed", setting, path, prefix, kind)
	}
	pair, err := nkeys.FromSeed(seed)
	if err != nil {
		return nil, fmt.Errorf("natscallout: %s names %s, whose seed makes no key: %w", setting, path, err)
	}
	return pair, nil
}

// answerer answers one authorization request.
type answerer struct {
	issuer, xkey nkeys.KeyPair
	deps         Deps
	services     []ServiceEntry
	decide       decideFunc
}

// answer opens msg, decides it and answers it, sealed to the server's key when the server sealed
// the request. A request it cannot open or read is not answered: the server refuses that
// connection at its authorization timeout, and the broker's log says why.
func (a *answerer) answer(msg *nats.Msg) {
	began := time.Now()
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
	ctx, cancel := context.WithTimeout(context.Background(), decideBudget)
	defer cancel()
	decision, refusal := a.decide(ctx, a.deps, a.services, req)
	// A decision that ran past the budget is refused, whatever it decided: the client is at or
	// past the point its connect gives up, and admitting it there would hand the server a user
	// for a connection that is no longer waiting.
	if took := time.Since(began); took > decideBudget && refusal == nil {
		refusal = budgetRefusal(credentialKind(req), "the answer")
	}
	reason := ""
	if refusal != nil {
		reason = refusal.Error()
		var refused *Refusal
		if !errors.As(refusal, &refused) {
			refusal = refuse(credentialKind(req), "the service failed: "+reason)
		}
		a.log("natscallout: refused a NATS connection", "reason", refusal.Error(), "host", req.Host, "kind", credentialKind(req))
	}
	a.deps.Metrics.record(credentialKind(req), reason, time.Since(began))
	encoded, err := encodeAnswer(a.issuer, req, decision, refusal)
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

// encodeAnswer is the authorization response the server reads for req, signed by issuer: the user
// decision admits, as a user JWT signed by issuer for req's user key, in the global account, named
// decision.Name and expiring at decision.Expires, with decision.Grant as its publish and subscribe
// allow lists; or, when refusal is not nil, refusal's one line as the response's error.
func encodeAnswer(issuer nkeys.KeyPair, req Request, decision Decision, refusal error) (string, error) {
	response := jwt.NewAuthorizationResponseClaims(req.UserNkey)
	if response == nil {
		return "", errors.New("natscallout: the request names no user key")
	}
	response.Audience = req.ServerID
	// The one place every answer passes, so the last guard is here: a grant with an empty allow
	// list in either direction would be every subject in that direction (natsgrant.OpenDirection),
	// and it is answered as a refusal naming that direction, never encoded as a user.
	if refusal == nil {
		if direction, open := natsgrant.OpenDirection(decision.Grant); open {
			refusal = errors.New(openDirectionReason(direction))
		}
	}
	if refusal != nil {
		response.Error = refusal.Error()
		return response.Encode(issuer)
	}
	user := jwt.NewUserClaims(req.UserNkey)
	user.Audience = globalAccount
	user.Name = decision.Name
	user.Expires = decision.Expires.Unix()
	user.Pub.Allow.Add(decision.Grant.Publish...)
	user.Sub.Allow.Add(decision.Grant.Subscribe...)
	userJWT, err := user.Encode(issuer)
	if err != nil {
		return "", err
	}
	response.Jwt = userJWT
	return response.Encode(issuer)
}
