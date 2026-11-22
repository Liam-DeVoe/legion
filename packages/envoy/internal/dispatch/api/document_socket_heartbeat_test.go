package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"
	"github.com/reearth/ygo/crdt"

	"github.com/sjawhar/envoy/internal/ciphertoken"
	"github.com/sjawhar/envoy/internal/dispatch/auth"
	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/identity"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/testwait"
)

// documentSocketTokenRig is a server whose people sign in with a session cookie, as
// newCookieServerOn's do, and whose agents present tokens the secrets broker issued, as
// newBrokerTokenRig's do: the one server where a request can carry both, a cookie and a token
// beside it. The API and the document service share one cookie identity, as cmd/dispatch hands
// them one.
type documentSocketTokenRig struct {
	// tokens is the rig the broker-token verifier comes from: its stub broker answers what a test
	// says a token is, and its deps.BrokerTokens is the verifier this server decides tokens with.
	// Its own server is never sent a request.
	tokens   *brokerTokenRig
	sessions *store.PgSessionStore
	// logins is the API's Identity: the cookie identity the document service shares, counting
	// the lookups made through it (countedLogins).
	logins *countedLogins
	// heartbeats records the calls a socket's heartbeat makes into the product, once a test
	// starts it (heartbeatCalls): the API's lookups of the cookie, and the verifier's clock
	// reads and introspections (timeBrokerCalls).
	heartbeats *heartbeatCalls
	// spans records, for each connection the server accepts, when the server itself ended it
	// (servedSpans): the record of the socket's close a test reads.
	spans      *servedSpans
	cookie     *http.Cookie
	server     *httptest.Server
	sockets    *servedSockets
	artifactID string
}

// documentSocketPerson is the person signed in on a documentSocketTokenRig.
const documentSocketPerson = "alice@d.example"

// newDocumentSocketTokenRig signs documentSocketPerson in with a session cookie and gives her an
// issue whose spec she can open, on a server whose sockets resolve their caller every heartbeat.
// The verifier is newBrokerTokenRig's, taken from that rig rather than built a second time, so it
// is the one every broker-token test drives: the same stub broker, the replay table on this same
// database, and the switches cmd/dispatch sets on it.
func newDocumentSocketTokenRig(t *testing.T, heartbeat time.Duration) *documentSocketTokenRig {
	t.Helper()
	ctx := context.Background()
	rig := &documentSocketTokenRig{heartbeats: &heartbeatCalls{}}
	rig.tokens = newBrokerTokenRig(t, timeBrokerCalls(t, rig.heartbeats))
	database := rig.tokens.database
	rig.sessions = store.NewPgSessionStore(database.Pool)
	person := identity.CookieIdentity{SigningKey: testSigningKey, Sessions: rig.sessions}
	rig.logins = &countedLogins{Identity: person, calls: rig.heartbeats}
	broker := events.NewBroker()
	documents := docs.New(docs.Deps{Store: database, Events: broker, Identity: person, Settle: time.Hour})
	t.Cleanup(func() { _ = documents.Shutdown(context.Background()) })
	deps, err := NewDeps(DepsInput{
		Store: database, Identity: rig.logins, AgentTokens: sharedAgentTokens(t, "agent-token"),
		ServerURL: testServerURL, Docs: documents, Events: broker,
		BrokerTokens: rig.tokens.deps.BrokerTokens, StreamHeartbeat: heartbeat,
	})
	if err != nil {
		t.Fatalf("new API dependencies: %v", err)
	}
	mux := http.NewServeMux()
	t.Cleanup(Register(mux, t.Context(), deps).Wait)
	rig.sockets = &servedSockets{finished: make(map[string]chan struct{})}
	rig.spans = &servedSpans{}
	rig.server = httptest.NewUnstartedServer(rig.sockets.serve(mux.ServeHTTP))
	rig.server.Listener = rig.spans.listen(rig.server.Listener)
	rig.server.Start()
	t.Cleanup(rig.server.Close)

	// A session cookie is issued at sign-in, and a sign-in records the person.
	seedPeople(t, database, documentSocketPerson)
	generation, err := rig.sessions.EnsureSession(ctx, documentSocketPerson)
	if err != nil {
		t.Fatalf("establish the session: %v", err)
	}
	if rig.cookie, err = http.ParseSetCookie(auth.IssueSessionCookie(documentSocketPerson, generation, testSigningKey, false)); err != nil {
		t.Fatalf("parse the session cookie: %v", err)
	}
	signedIn := func(method, target string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
		request := httptest.NewRequest(method, target, bytes.NewReader(encoded))
		request.AddCookie(rig.cookie)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}
	if response := signedIn(http.MethodPost, "/api/v1/projects", map[string]string{"key": "TEST", "name": "Test"}); response.Code != http.StatusCreated {
		t.Fatalf("create project: status=%d body=%s", response.Code, response.Body.String())
	}
	created := signedIn(http.MethodPost, "/api/v1/issues", map[string]string{"project": "TEST", "title": "Live document", "spec": "The spec."})
	if created.Code != http.StatusCreated {
		t.Fatalf("create issue: status=%d body=%s", created.Code, created.Body.String())
	}
	rig.artifactID = decodeBody[struct {
		PrimaryArtifactID string `json:"primary_artifact_id"`
	}](t, created).PrimaryArtifactID
	return rig
}

// closedAt is when the server ended the last connection it accepted - the socket, since every
// request of the rig's own setup goes to the mux directly - by its own record (servedSpans): when
// it began to write its close frame, or, where it wrote none, when it closed the connection. It is
// false until the server has done either.
func (rig *documentSocketTokenRig) closedAt() (time.Time, bool) {
	rig.spans.mu.Lock()
	defer rig.spans.mu.Unlock()
	if len(rig.spans.conns) == 0 {
		return time.Time{}, false
	}
	conn := rig.spans.conns[len(rig.spans.conns)-1]
	if !conn.closeFrame.IsZero() {
		return conn.closeFrame, true
	}
	return conn.closed, !conn.closed.IsZero()
}

// countedLogins is an Identity that records the lookups made through it as heartbeat calls
// (heartbeatCalls.begin) and counts those that start once the test has said the cookie's session
// ended (end): on a documentSocketTokenRig, the API's, whose only caller once the setup's requests
// have answered is the document socket's heartbeat (signInStillResolves). A lookup counts when it
// starts after end, so one already under way when the session ended, which may find the session
// either way, is not counted.
type countedLogins struct {
	identity.Identity
	calls    *heartbeatCalls
	ended    atomic.Bool
	afterEnd atomic.Int64
}

func (c *countedLogins) Login(r *http.Request) (string, error) {
	done := c.calls.begin()
	if c.ended.Load() {
		c.afterEnd.Add(1)
	}
	email, err := c.Identity.Login(r)
	done(err != nil)
	return email, err
}

// end marks the cookie's session ended: every lookup that starts from now on is counted.
func (c *countedLogins) end() { c.ended.Store(true) }

// heartbeatSlack is what a loaded runner may add to a heartbeat: how much later than a heartbeat
// after the last call returned the next call of a socket's heartbeat may start.
const heartbeatSlack = time.Second

// heartbeatCalls records, once the test starts it (record), each call a socket's heartbeat makes
// into the product: when it started, when it returned, and whether its answer refused the caller.
// The calls are the product's own - the API Identity's lookups on a document socket
// (countedLogins), the broker-token verifier's clock reads and introspections on a live socket
// (timeBrokerCalls) - so their times tell a heartbeat that asks on every tick of StreamHeartbeat
// from one that asks every second tick, keeps an answer it stored, or runs on a ticker of its own,
// with no span the test chose. whileCallerResolves arms its ticker before its first call, and a
// tick that falls during a slow call waits for it, so the next call starts at most a heartbeat
// after the last one returned however long a call takes; heartbeatSlack is what a loaded runner
// may add to that. A call that refused is the heartbeat's last: it closes the socket and asks no
// more, so nothing is expected after it, however long the close itself takes.
type heartbeatCalls struct {
	mu        sync.Mutex
	recording bool
	since     time.Time
	calls     []heartbeatCall
	// introspections are the verifier's introspections, recorded from the start, the upgrade's
	// own among them.
	introspections []heartbeatCall
}

// heartbeatCall is one call: when it started, when it returned (zero while it runs), and whether
// its answer refused the caller.
type heartbeatCall struct {
	start, end time.Time
	refused    bool
}

// record starts recording the heartbeat's calls.
func (h *heartbeatCalls) record() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recording, h.since = true, time.Now()
}

// begin records the start of a call, once recording, and answers what records its return and
// whether its answer refused the caller.
func (h *heartbeatCalls) begin() func(refused bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.recording {
		return func(bool) {}
	}
	index := len(h.calls)
	h.calls = append(h.calls, heartbeatCall{start: time.Now()})
	return func(refused bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.calls[index].end, h.calls[index].refused = time.Now(), refused
	}
}

// introspected records an introspection of the verifier's, from its request to its answer.
func (h *heartbeatCalls) introspected(start, end time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.introspections = append(h.introspections, heartbeatCall{start: start, end: end})
}

// longestGap is the longest span of the recording in which no call ran - from when recording
// began, or the latest return, to the next call's start - and how many calls it holds. With open,
// the span from the latest return to now counts as well, unless a call is still running or one
// refused, after which the heartbeat asks no more.
func (h *heartbeatCalls) longestGap(open bool) (time.Duration, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	latest, running, refused, longest := h.since, false, false, time.Duration(0)
	for _, call := range h.calls {
		if !running {
			longest = max(longest, call.start.Sub(latest))
		}
		switch {
		case call.end.IsZero():
			running = true
		case call.end.After(latest):
			latest = call.end
		}
		refused = refused || call.refused
	}
	if open && !running && !refused {
		longest = max(longest, time.Since(latest))
	}
	return longest, len(h.calls)
}

// refusedAt is when the heartbeat's first refusing call returned: the refusal that orders the
// socket closed. It is false while no call has refused.
func (h *heartbeatCalls) refusedAt() (time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, call := range h.calls {
		if call.refused {
			return call.end, true
		}
	}
	return time.Time{}, false
}

// requireClosedAfterRefusal fails the test unless the server ended the socket (closedAt) within a
// heartbeat of the heartbeat's refusing call returning (refusedAt), both the product's own
// records, allowing the time meter saw this process stalled between them (requireClosedWithin):
// a refused caller's socket closes as soon as its refusal is read, and a product that waits on
// its own before closing it fails here.
func (rig *documentSocketTokenRig) requireClosedAfterRefusal(t *testing.T, meter *stallMeter, what string, heartbeat time.Duration) {
	t.Helper()
	refused, ok := rig.heartbeats.refusedAt()
	if !ok {
		t.Fatalf("%s ended with no call of its heartbeat refusing its caller", what)
	}
	closed := testwait.Until(t, "the server's own end of "+what, rig.closedAt)
	requireClosedWithin(t, meter, what, refused, closed, heartbeat)
}

// count is how many calls the recording holds.
func (h *heartbeatCalls) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

// lastIntrospectionGap is the span from the answer of the last introspection but one to the
// request of the last, and false when there were fewer than two.
func (h *heartbeatCalls) lastIntrospectionGap() (time.Duration, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.introspections) < 2 {
		return 0, false
	}
	last, before := h.introspections[len(h.introspections)-1], h.introspections[len(h.introspections)-2]
	return last.start.Sub(before.end), true
}

// await waits until done holds, and fails the test once the heartbeat has made no call for longer
// than heartbeat and heartbeatSlack while it still had one to make (no call of it refused): a
// heartbeat that stopped asking, or asks less often, ends the wait by name rather than at the test
// binary's deadline, which is the wait's one other bound.
func (h *heartbeatCalls) await(t *testing.T, heartbeat time.Duration, what string, done func() bool) {
	t.Helper()
	testwait.UntilPolling(t, 10*time.Millisecond, what+" (the heartbeat's calls recorded)", func() (int, bool) {
		if done() {
			return h.count(), true
		}
		gap, calls := h.longestGap(true)
		if gap > heartbeat+heartbeatSlack {
			t.Fatalf("waiting for %s, the socket's heartbeat made no call for %s (after %d calls), want one at most a heartbeat (%s) after the last returned: it asks less often than StreamHeartbeat, or stopped asking",
				what, gap.Round(time.Millisecond), calls, heartbeat)
		}
		return calls, false
	})
}

// requireEveryHeartbeat fails the test unless every call the recording holds started at most a
// heartbeat and heartbeatSlack after the one before it returned.
func (h *heartbeatCalls) requireEveryHeartbeat(t *testing.T, heartbeat time.Duration) {
	t.Helper()
	if gap, calls := h.longestGap(false); gap > heartbeat+heartbeatSlack {
		t.Fatalf("the socket's heartbeat made no call for %s in %d calls, want one at most a heartbeat (%s) after the last returned: it asks less often than StreamHeartbeat",
			gap.Round(time.Millisecond), calls, heartbeat)
	}
}

// timeBrokerCalls is the verifier option that records its calls on heartbeats: each read of its
// clock (Options.Now), which every Live call makes first (Verifier.known), as a call that returns
// at once, and each introspection, sent through a recording proxy in front of the broker, as a
// call from its request to its answer, which refused when the broker answered the token not live.
func timeBrokerCalls(t *testing.T, heartbeats *heartbeatCalls) func(*ciphertoken.Options) {
	return func(options *ciphertoken.Options) {
		target, err := url.Parse(options.BrokerURL)
		if err != nil {
			t.Fatalf("parse the broker's URL: %v", err)
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ModifyResponse = func(response *http.Response) error {
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil {
				return err
			}
			response.Body = io.NopCloser(bytes.NewReader(body))
			var answer struct {
				Active bool `json:"active"`
			}
			if refused, ok := response.Request.Context().Value(introspectionRefusedKey{}).(*bool); ok && json.Unmarshal(body, &answer) == nil {
				*refused = !answer.Active
			}
			return nil
		}
		recorder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			done := heartbeats.begin()
			started := time.Now()
			refused := new(bool)
			proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), introspectionRefusedKey{}, refused)))
			heartbeats.introspected(started, time.Now())
			done(*refused)
		}))
		t.Cleanup(recorder.Close)
		options.BrokerURL = recorder.URL
		options.Now = func() time.Time {
			heartbeats.begin()(false)
			return time.Now()
		}
	}
}

// introspectionRefusedKey carries, on an introspection timeBrokerCalls's proxy forwards, where it
// writes whether the broker answered the token not live.
type introspectionRefusedKey struct{}

// openDocument opens the spec's document socket as the dashboard's editor does, with the
// person's cookie and every header of beside on the upgrade, and closes it when the test ends.
func (rig *documentSocketTokenRig) openDocument(t *testing.T, beside http.Header) *syncedPeer {
	t.Helper()
	headers := beside.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	headers.Set("Cookie", rig.cookie.Name+"="+rig.cookie.Value)
	peer := &syncedPeer{
		wsURL: "ws" + strings.TrimPrefix(rig.server.URL, "http") + "/ws/doc/" + rig.artifactID, sockets: rig.sockets,
		headers: headers, artifactID: rig.artifactID, doc: crdt.New(),
	}
	peer.connect(t)
	t.Cleanup(peer.close)
	return peer
}

// presentedHeaders are the credential headers of presented, the request a broker caller sends:
// its token under the DPoP scheme and the proof that goes beside it.
func presentedHeaders(presented *http.Request) http.Header {
	headers := http.Header{}
	headers.Set("Authorization", presented.Header.Get("Authorization"))
	headers.Set("DPoP", presented.Header.Get("DPoP"))
	return headers
}

// The document socket admits its caller by the sign-in cookie alone (docs.Service.requestActor),
// so its heartbeat re-checks that cookie and nothing the request carries beside it
// (signInStillResolves). A token sent beside the cookie admitted nothing, and the broker holding
// it live must not keep the socket open once the cookie's session has ended: not a read-only
// token, which the read list lets read no document at all, and not a token that writes, which is
// its session's credential and still not what opened this socket. The socket stays open while
// the cookie resolves, with the token beside it, and closes at the first heartbeat after the
// session's end, while the broker still holds the token live and has not once been asked about
// it. "The first" is read off the heartbeat's own calls, its lookups of the cookie
// (countedLogins): no lookup that started once the session had ended may find it still resolving,
// as one served from a cache of the session's state would, and every lookup starts at most a
// heartbeat and heartbeatSlack after the one before it returned (heartbeatCalls), so a heartbeat
// that asks every second tick, keeps an answer it stored or runs on a ticker of its own fails too.
// The server ends the socket within a heartbeat of the lookup that refused returning, by the
// server's own record of its close (requireClosedAfterRefusal), so a product that waits before it
// closes a refused socket fails too. The heartbeat is two seconds, more than heartbeatSlack, so
// asking every second tick is told apart from a late tick.
func TestADocumentSocketClosesOnceItsCookieEndsThoughALiveTokenRidesBesideIt(t *testing.T) {
	const heartbeat = 2 * time.Second
	for _, beside := range []struct {
		name   string
		answer func(jkt string) ciphertoken.Introspection
	}{
		{"a read-only token", readOnlyToken},
		{"a token that writes", liveNamedToken},
	} {
		t.Run(beside.name, func(t *testing.T) {
			meter := startStallMeter(t)
			rig := newDocumentSocketTokenRig(t, heartbeat)
			caller := newBrokerCaller(t)
			const token = "a-token-sent-beside-the-cookie"
			rig.tokens.broker.Says(token, beside.answer(caller.jkt))
			presented := caller.presenting(t, http.MethodGet, "/ws/doc/"+rig.artifactID, token, nil)
			peer := rig.openDocument(t, presentedHeaders(presented))
			rig.heartbeats.record()

			// While the cookie resolves the socket outlives several heartbeats, each of which
			// looks the cookie up again, and still answers its editor.
			rig.heartbeats.await(t, heartbeat, "three heartbeat lookups of the cookie while it resolves", func() bool {
				return rig.heartbeats.count() >= 3
			})
			peer.barrier(t)

			if err := rig.sessions.RevokeSessions(context.Background(), documentSocketPerson); err != nil {
				t.Fatalf("sign out: %v", err)
			}
			rig.logins.end()
			ended := time.Now()
			rig.heartbeats.await(t, heartbeat, "the document socket's end once its cookie's session ended, with "+beside.name+" the broker holds live beside the cookie", func() bool {
				select {
				case <-peer.connection().Ended:
					return true
				default:
					return false
				}
			})
			lookups := rig.logins.afterEnd.Load()
			t.Logf("the document socket closed %s after its cookie's session ended, after %d lookups that started once it had (heartbeat %s)", time.Since(ended).Round(time.Millisecond), lookups, heartbeat)
			if lookups > 1 {
				t.Fatalf("the document socket closed after %d heartbeat lookups of its cookie that started once its session had ended, want the first to close it: a heartbeat found the ended session still resolving", lookups)
			}
			rig.heartbeats.requireEveryHeartbeat(t, heartbeat)
			rig.requireClosedAfterRefusal(t, meter, "the document socket", heartbeat)
			// The heartbeat cancels the connection's context with no close code, so ygo drops
			// the socket without a close frame.
			if code := peer.connection().CloseCode(); code != gws.CloseAbnormalClosure {
				t.Fatalf("the document socket closed with %d, want %d: the server dropping it", code, gws.CloseAbnormalClosure)
			}
			// The close is the cookie's: nothing asked the broker about the token, and the
			// broker holds it live still.
			if asked := rig.tokens.broker.Count(); asked != 0 {
				t.Fatalf("the document socket's heartbeat asked the broker %d times about a token it was not admitted by, want 0", asked)
			}
			if _, err := rig.tokens.deps.BrokerTokens.Live(context.Background(), presented); err != nil {
				t.Fatalf("the token beside the cookie no longer resolves (%v), so the close says nothing of the cookie", err)
			}
		})
	}
}

// A document socket whose cookie still resolves stays open across its heartbeats, each of which
// re-checks that cookie: opened with the cookie alone, and opened with a token beside it that the
// broker does not hold live at all, which the heartbeat never reads and the broker is never asked
// about.
func TestADocumentSocketOutlivesItsHeartbeatsWhileItsCookieResolves(t *testing.T) {
	const heartbeat = 250 * time.Millisecond
	for _, beside := range []struct {
		name    string
		headers func(*testing.T, *documentSocketTokenRig) http.Header
	}{
		{"the cookie alone", func(*testing.T, *documentSocketTokenRig) http.Header { return nil }},
		{"the cookie beside a token the broker does not hold live", func(t *testing.T, rig *documentSocketTokenRig) http.Header {
			return presentedHeaders(newBrokerCaller(t).presenting(t, http.MethodGet, "/ws/doc/"+rig.artifactID, "a-token-the-broker-never-issued", nil))
		}},
	} {
		t.Run(beside.name, func(t *testing.T) {
			rig := newDocumentSocketTokenRig(t, heartbeat)
			peer := rig.openDocument(t, beside.headers(t, rig))

			time.Sleep(4 * heartbeat)
			select {
			case <-peer.connection().Ended:
				t.Fatalf("the document socket closed (code %d) within four heartbeats while its cookie resolved", peer.connection().CloseCode())
			default:
			}
			peer.barrier(t)
			if asked := rig.tokens.broker.Count(); asked != 0 {
				t.Fatalf("the document socket's heartbeat asked the broker %d times, want 0", asked)
			}
		})
	}
}

// GET /ws/live keeps its own rule, which the document socket's does not change: it is admitted
// by whichever credential the request carries, a broker-issued token here with no cookie at all,
// and its heartbeat resolves that token again (callerStillResolves). The socket outlives several
// heartbeats while the broker holds the token live, and closes with 1008 once the broker stops
// holding it live and the answer it remembered has run out: by the first introspection after the
// broker's answer changed, since the heartbeats before it are answered from the remembered answer
// and the first that asks the broker hears the token is no longer live. That is read off the
// product's own calls: the broker's introspections are counted (StubBroker.Count); the one that
// closes the socket is sent within the remembered answer's life (AffirmativeCacheTTL), a heartbeat
// and heartbeatSlack of the answer of the one before it; and every call the heartbeat makes into
// the verifier starts at most a heartbeat and heartbeatSlack after the one before it returned
// (heartbeatCalls, timeBrokerCalls), so a heartbeat that asks every second tick, keeps an answer
// it stored or runs on a ticker of its own fails too. The server writes its close frame within a
// heartbeat of the introspection that answered the token not live, by the server's own record
// (requireClosedAfterRefusal), so a product that waits before it closes a refused socket fails
// too. The heartbeat is two seconds, more than heartbeatSlack, so asking every second tick is told
// apart from a late tick, and the client's pong meets the pong deadline on a loaded runner.
func TestALiveSocketOpenedByABrokerTokenResolvesThatTokenOnEveryHeartbeat(t *testing.T) {
	meter := startStallMeter(t)
	const heartbeat = 2 * time.Second
	rig := newDocumentSocketTokenRig(t, heartbeat)
	caller := newBrokerCaller(t)
	const token = "a-token-holding-the-live-socket-open"
	rig.tokens.broker.Says(token, liveNamedToken(caller.jkt))
	presented := caller.presenting(t, http.MethodGet, "/ws/live", token, nil)
	client := openLiveSocketAt(t, gws.DefaultDialer, "ws"+strings.TrimPrefix(rig.server.URL, "http")+"/ws/live", presentedHeaders(presented)).read()
	rig.heartbeats.record()

	client.quiet(t, 3*heartbeat+heartbeat/2, "a live socket whose token the broker holds live")

	rig.tokens.broker.Says(token, ciphertoken.Introspection{Active: false})
	asked := rig.tokens.broker.Count()
	rig.heartbeats.await(t, heartbeat, "the live socket's end once the broker stopped holding its token live", func() bool {
		select {
		case <-client.ended:
			return true
		default:
			return false
		}
	})
	if closed := client.closeError(t); closed.Code != gws.ClosePolicyViolation {
		t.Fatalf("the live socket closed with %d %q once the broker stopped holding its token live, want 1008", closed.Code, closed.Text)
	}
	if after := rig.tokens.broker.Count() - asked; after > 1 {
		t.Fatalf("the live socket closed after %d introspections once the broker stopped holding its token live, want the first: a heartbeat kept the remembered answer past the introspection that refused it", after)
	}
	if gap, ok := rig.heartbeats.lastIntrospectionGap(); !ok || gap > ciphertoken.AffirmativeCacheTTL+heartbeat+heartbeatSlack {
		t.Fatalf("the introspection that closed the live socket was sent %s after the answer of the one before it (two recorded: %t), want within the remembered answer's life and a heartbeat, %s",
			gap.Round(time.Millisecond), ok, ciphertoken.AffirmativeCacheTTL+heartbeat+heartbeatSlack)
	}
	rig.heartbeats.requireEveryHeartbeat(t, heartbeat)
	rig.requireClosedAfterRefusal(t, meter, "the live socket", heartbeat)
}
