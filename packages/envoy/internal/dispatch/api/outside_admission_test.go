package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/dispatch/admission"
	"github.com/sjawhar/envoy/internal/dispatch/identity"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// checkStep is what one resolution of a heartbeat's check answers (scriptedCheckIdentity).
type checkStep func(*http.Request) error

var (
	// checkFindsPoolBusy is a check whose read of the caller's session found no connection free.
	checkFindsPoolBusy checkStep = func(*http.Request) error {
		return fmt.Errorf("read the caller's session: %w", store.ErrPoolBusy)
	}
	// checkHangs is a check whose database never answers, so only the check's own bound ends it.
	checkHangs checkStep = func(r *http.Request) error {
		<-r.Context().Done()
		return fmt.Errorf("read the caller's session: %w", r.Context().Err())
	}
	// checkResolves is a check that finds the caller still signed in.
	checkResolves checkStep = func(*http.Request) error { return nil }
	// checkRefuses is a check that finds the caller's sign-in ended: a logout.
	checkRefuses checkStep = func(*http.Request) error { return identity.ErrNoIdentity }
)

// scriptedCheckIdentity answers its resolutions with steps, one each in order, and resolves alice
// once they are spent; asked counts the resolutions it was asked for.
type scriptedCheckIdentity struct {
	steps []checkStep
	asked atomic.Int32
}

func (i *scriptedCheckIdentity) Login(r *http.Request) (string, error) {
	asked := int(i.asked.Add(1))
	if asked <= len(i.steps) {
		if err := i.steps[asked-1](r); err != nil {
			return "", err
		}
	}
	return "alice", nil
}

// A socket's heartbeat outlives a check that fails for a transient reason - the pool busy, the
// check's own bound passing, or the request pool's share outside admission having no place for it,
// so that it is not made - and checks again at the next heartbeat, and the third such failure in a
// row closes the socket as one whose caller no longer resolves, with that failure as its cause. A
// skipped check counts toward those three as the other two do, so a socket whose checks find no
// place three heartbeats running closes though no check was made, and a check that resolves the
// caller, alone, starts the count again. A refusal closes the socket at once, whatever came before
// it. The checks are the document socket's own (signInStillResolves), each answered as the script
// says, on a connection whose handler waits for its context to end; no check is made after the one
// that closes it.
func TestAHeartbeatOutlivesTwoTransientFailuresAndClosesOnTheThird(t *testing.T) {
	const heartbeat = 100 * time.Millisecond
	busy, hangs, resolves, refuses := checkFindsPoolBusy, checkHangs, checkResolves, checkRefuses
	for _, run := range []struct {
		name  string
		steps []checkStep
		// heldFull are the heartbeats, counted from one, whose check finds every place of the
		// share outside admission held.
		heldFull []int
		// closesAt is how many resolutions were made when the socket closed, cause what the close
		// carries, and transient how many of the heartbeats up to it failed for a transient reason.
		closesAt  int
		cause     error
		transient uint64
	}{
		{"a busy pool at three heartbeats in a row", []checkStep{busy, busy, busy}, nil, 3, store.ErrPoolBusy, 3},
		{"a check running out its bound at three heartbeats in a row", []checkStep{hangs, hangs, hangs}, nil, 3, errHeartbeatCheckBound, 3},
		{"a resolution between transient failures", []checkStep{busy, hangs, resolves, busy, busy, busy}, nil, 6, store.ErrPoolBusy, 5},
		{"a skipped check between two busy pools", []checkStep{busy, busy}, []int{2}, 2, store.ErrPoolBusy, 3},
		{"every check skipped at three heartbeats in a row", nil, []int{1, 2, 3}, 0, errHeartbeatCheckSkipped, 3},
		{"a resolution after two skipped checks", []checkStep{resolves, busy, busy, busy}, []int{1, 2}, 4, store.ErrPoolBusy, 5},
		{"a refusal after a transient failure", []checkStep{busy, refuses}, nil, 2, identity.ErrNoIdentity, 1},
	} {
		t.Run(run.name, func(t *testing.T) {
			scripted := &scriptedCheckIdentity{steps: run.steps}
			served := directServer(Deps{Identity: scripted, StreamHeartbeat: heartbeat})
			served.outside = admission.New(admission.DefaultLimits()).Outside()
			var beats atomic.Int32
			check := func(r *http.Request) error {
				if slices.Contains(run.heldFull, int(beats.Add(1))) {
					for range admission.OutsideLimit {
						served.outside.Take(admission.DPoPDecision)
					}
					defer func() {
						for range admission.OutsideLimit {
							served.outside.Release()
						}
					}()
				}
				return served.signInStillResolves(r)
			}
			ended := make(chan error, 1)
			connection := served.whileCallerResolves(check, func(_ http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
				ended <- context.Cause(r.Context())
			})
			transientBefore, ceilingBefore := HeartbeatTransientFailures(), HeartbeatClosesAtCeiling()
			go connection(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ws/doc/room", nil))

			var cause error
			select {
			case cause = <-ended:
			case <-time.After(10 * time.Second):
				t.Fatalf("the socket was still open after %d resolutions, want it closed at resolution %d", scripted.asked.Load(), run.closesAt)
			}
			if asked := int(scripted.asked.Load()); asked != run.closesAt {
				t.Fatalf("the socket closed at resolution %d (%v), want at resolution %d", asked, cause, run.closesAt)
			}
			if !errors.Is(cause, errCallerNoLongerResolves) || !errors.Is(cause, run.cause) {
				t.Fatalf("the socket closed with %v, want %v wrapping %v", cause, errCallerNoLongerResolves, run.cause)
			}
			stats := served.outside.Stats()
			if skipped := stats.Refused[admission.HeartbeatCheck]; skipped != uint64(len(run.heldFull)) || stats.InUse != 0 {
				t.Errorf("%d checks skipped and %d places held, want %d skipped and none held", skipped, stats.InUse, len(run.heldFull))
			}
			if got := HeartbeatTransientFailures() - transientBefore; got < run.transient {
				t.Errorf("%d transient failures counted, want %d", got, run.transient)
			}
			if run.cause != identity.ErrNoIdentity && HeartbeatClosesAtCeiling() == ceilingBefore {
				t.Error("the socket closed at the ceiling was not counted")
			}
			time.Sleep(3 * heartbeat)
			if asked := int(scripted.asked.Load()); asked != run.closesAt {
				t.Errorf("the closed socket's heartbeat checked again, %d resolutions in all, want none after the close at %d", asked, run.closesAt)
			}
		})
	}
}

// A live socket opened by a broker-issued token is checked at every heartbeat by the broker's
// answer alone, which reads nothing of the request pool and so takes no place of the pool's share
// outside admission (callerStillResolves). With every connection of the request pool held and
// every place of that share held, a token the broker stops holding live still closes the socket
// with 1008 at the first heartbeat that asks the broker again. The verifier's clock is moved past
// the live answer it remembers, so that heartbeat is the next one.
func TestARevokedBrokerTokenClosesTheLiveSocketWhateverTheRequestPoolHolds(t *testing.T) {
	const heartbeat = time.Second
	limiter := admission.New(admission.DefaultLimits())
	var ahead atomic.Int64
	rig := newBrokerTokenRigOn(t, testServerOptions{streamHeartbeat: heartbeat, requestPoolSize: 2, admission: limiter},
		func(options *brokertoken.Options) {
			options.Now = func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) }
		})
	caller := newBrokerCaller(t)
	const token = "a-token-holding-the-live-socket-open"
	rig.broker.Says(token, liveNamedToken(caller.jkt))
	server := httptest.NewServer(rig.handler)
	t.Cleanup(server.Close)
	presented := caller.presenting(t, http.MethodGet, "/ws/live", token, nil)
	client := openLiveSocketAt(t, gws.DefaultDialer, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/live", presentedHeaders(presented)).read()
	client.quiet(t, 2*heartbeat+heartbeat/2, "a live socket whose token the broker holds live")

	holdEveryConnection(t, rig.database.Pool)
	release := holdTheShareOutsideAdmission(t, limiter.Outside())
	defer release()

	rig.broker.Says(token, brokertoken.Introspection{Active: false})
	ahead.Store(int64(brokertoken.AffirmativeCacheTTL + time.Second))
	revoked := time.Now()
	if closed := client.closeError(t, 2*heartbeat+time.Second); closed.Code != gws.ClosePolicyViolation {
		t.Fatalf("the live socket closed with %d %q once the broker stopped holding its token live, want 1008", closed.Code, closed.Text)
	}
	t.Logf("the live socket closed %s after its token was revoked (heartbeat %s)", time.Since(revoked).Round(time.Millisecond), heartbeat)
	if skipped := limiter.Outside().Stats().Refused[admission.HeartbeatCheck]; skipped != 0 {
		t.Errorf("%d of the broker token's checks were skipped for the share outside admission, want none", skipped)
	}
}

// A burst of DPoP presentations, more at once than the request pool's share outside admission,
// takes no more of the request pool before admission than that share, 8 of its 48: each decision
// takes a place before it writes its proof's row (decideBrokerToken), and every presentation past
// the share is answered at once with admission's own refusal for a full queue - its status, every
// header and every byte of its body are those admission answers a read it has no room for - and
// its proof is not spent, so the same request sent again is served. Every connection of the
// request pool is held while the burst arrives, so a decision that reaches the pool waits there
// holding its place, and the pool's own count of the acquires waiting on it is the burst's hold on
// the pool before admission. The share is read from the two numbers it is made of rather than from
// the limit, so a limit taken out is a burst this test still holds to them.
func TestABurstOfDPoPPresentationsTakesNoMoreOfTheRequestPoolThanAdmissionLeaves(t *testing.T) {
	share := store.SharedPoolSize - admission.MaxAdmittedUnits
	burst := 3 * share
	// Room for every decision the share lets through once the pool is free, and a read queue that
	// waits long enough for the refusal it is compared with to be had.
	limiter := admission.New(admission.Limits{Read: share, Write: share, ReadWait: time.Minute, WriteWait: time.Minute})
	rig := newBrokerTokenRigOn(t, testServerOptions{requestPoolSize: 2, acquireBound: 30 * time.Second, admission: limiter})
	caller := newBrokerCaller(t)
	const token = "a-token-presented-in-a-burst"
	rig.broker.Says(token, liveNamedToken(caller.jkt))
	requests := make([]*http.Request, burst)
	for i := range requests {
		requests[i] = caller.presenting(t, http.MethodGet, "/api/v1/whoami", token, nil)
	}

	release := holdEveryConnectionUntilReleased(t, rig.database.Pool)
	answers := make([]*httptest.ResponseRecorder, burst)
	done := make(chan struct{}, burst)
	for i, request := range requests {
		go func() {
			answers[i] = rig.serve(request)
			done <- struct{}{}
		}()
	}
	answered, peak := 0, 0
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(time.Millisecond) {
		for more := true; more; {
			select {
			case <-done:
				answered++
			default:
				more = false
			}
		}
		waiting := requestPoolWaiting(t, rig.database.Pool)
		peak = max(peak, waiting)
		if answered+waiting == burst {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after 20 s %d of the %d presentations are answered and %d wait on the request pool", answered, burst, waiting)
		}
	}
	if peak > share {
		t.Fatalf("the burst of %d presentations had %d acquires of the request pool waiting at once before admission, want at most the %d admission leaves", burst, peak, share)
	}
	release()
	for ; answered < burst; answered++ {
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("%d of the %d presentations were never answered once the request pool was free", burst-answered, burst)
		}
	}

	reference := admissionsQueueFullRefusal(t, rig, limiter, share)
	var refused []int
	for i, answer := range answers {
		switch answer.Code {
		case http.StatusOK:
		case http.StatusTooManyRequests:
			refused = append(refused, i)
			if !reflect.DeepEqual(answer.Header(), reference.Header()) || !bytes.Equal(answer.Body.Bytes(), reference.Body.Bytes()) {
				t.Errorf("presentation %d was refused %v %q, want admission's own refusal %v %q",
					i, answer.Header(), answer.Body.Bytes(), reference.Header(), reference.Body.Bytes())
			}
		default:
			t.Errorf("presentation %d was answered %d %s, want 200 or admission's 429", i, answer.Code, answer.Body.String())
		}
	}
	if len(refused) != burst-share {
		t.Fatalf("%d of the %d presentations refused, want the %d past the share", len(refused), burst, burst-share)
	}
	if got := limiter.Outside().Stats().Refused[admission.DPoPDecision]; got != uint64(burst-share) {
		t.Errorf("%d DPoP decisions counted refused, want %d", got, burst-share)
	}

	// A refused presentation decided nothing, so its proof is unspent: sent again, it is served.
	first := requests[refused[0]]
	again := brokerRequest(t, http.MethodGet, "/api/v1/whoami", nil, first.Header.Get("Authorization"), first.Header.Get("DPoP"))
	if response := rig.serve(again); response.Code != http.StatusOK {
		t.Fatalf("a refused presentation sent again was answered %d %s, want 200: its proof was spent", response.Code, response.Body.String())
	}
}

// admissionsQueueFullRefusal is admission's own answer to a read on rig's server it has no room
// for and no place in its queue for: every read unit of limiter, whose read limit is limit, held,
// and as many reads queued as the queue holds, twice that limit (admission.DefaultLimits).
func admissionsQueueFullRefusal(t *testing.T, rig *brokerTokenRig, limiter *admission.Limiter, limit int) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var queued sync.WaitGroup
	defer func() {
		cancel()
		queued.Wait()
	}()
	for range limit {
		ticket, err := limiter.Admit(ctx, admission.Read, 1)
		if err != nil {
			t.Fatalf("hold a read unit: %v", err)
		}
		defer ticket.Release()
	}
	for range 2 * limit {
		queued.Go(func() {
			if ticket, err := limiter.Admit(ctx, admission.Read, 1); err == nil {
				ticket.Release()
			}
		})
	}
	pollUntil(t, 10*time.Second, "a full read queue", func() bool { return limiter.Stats()[admission.Read].Queued == 2*limit })
	request := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	request.Header.Set("X-Dispatch-User", "alice")
	refusal := rig.serve(request)
	if refusal.Code != http.StatusTooManyRequests || limiter.Stats()[admission.Read].QueueFull != 1 {
		t.Fatalf("a read with the read queue full was answered %d %s, want admission's queue_full refusal", refusal.Code, refusal.Body.String())
	}
	return refusal
}

// holdTheShareOutsideAdmission takes every place of outside, the request pool's share admission
// leaves, waiting out a check in flight that holds one, and answers the release that gives them
// back.
func holdTheShareOutsideAdmission(t *testing.T, outside *admission.Outside) func() {
	t.Helper()
	for place := range admission.OutsideLimit {
		pollUntil(t, 5*time.Second, fmt.Sprintf("place %d of the share outside admission", place+1), func() bool {
			return outside.Take(admission.DPoPDecision)
		})
	}
	return sync.OnceFunc(func() {
		for range admission.OutsideLimit {
			outside.Release()
		}
	})
}

// holdEveryConnectionUntilReleased is holdEveryConnection with a release of its own, which the
// test's end runs too.
func holdEveryConnectionUntilReleased(t *testing.T, pool *store.Pool) func() {
	t.Helper()
	held := make([]*pgxpool.Conn, 0, pool.Stat().MaxConns())
	for range pool.Stat().MaxConns() {
		conn, err := pool.Acquire(context.Background())
		if err != nil {
			t.Fatalf("hold a connection: %v", err)
		}
		held = append(held, conn)
	}
	release := sync.OnceFunc(func() {
		for _, conn := range held {
			conn.Release()
		}
	})
	t.Cleanup(release)
	return release
}

// requestPoolWaiting is how many acquires of pool, a request pool, are waiting for a connection
// now: the pool's own count.
func requestPoolWaiting(t *testing.T, pool *store.Pool) int {
	t.Helper()
	for _, stats := range pool.Stats() {
		if stats.Name == store.RequestsPool {
			return int(stats.Acquire.Waiting)
		}
	}
	t.Fatal("the store reports no request pool")
	return 0
}
