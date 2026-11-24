package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/admission"
	"github.com/sjawhar/envoy/internal/dispatch/embed"
	"github.com/sjawhar/envoy/internal/dispatch/identity"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/githubtest"
	"github.com/sjawhar/envoy/internal/oidc"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
	"github.com/sjawhar/envoy/internal/testwait"
)

// The rows of this file hold an admitted request's caller's resolution on an outbound network
// wait - IAP's key set read for a key id the cached set does not hold, a service-account issuer's
// discovery - with more requests waiting on it than the read class has units, and read what the
// rest of the server meets meanwhile. No admission unit is held across such a wait
// (admission.GiveBack, admission.Away.TakeBack): before, every waiter held a read unit for up to
// the read's ten seconds, the class filled, and every request whose caller resolves from memory
// was refused 429 OVERLOADED until the read answered.

// networkWaitReads is the read class's limit in these rows: small, so the waiters would fill it.
const networkWaitReads = 2

// networkWaiters is how many browsers or service tokens wait on the issuer at once: three times
// the read class's limit, so the waiters would hold every read unit and queue for more.
const networkWaiters = 3 * networkWaitReads

// normalLatency bounds a request whose caller resolves from memory, served while the waiters
// wait: half a read's queue wait (admission.DefaultReadWait), where a request that queued behind
// the waiters for a unit and was refused takes the whole second.
const normalLatency = admission.DefaultReadWait / 2

// networkWaitLimiter is a limiter whose read class holds networkWaitReads units, with the
// deployment's waits: a read waits a second for room, and is refused 429 OVERLOADED after it.
func networkWaitLimiter() *admission.Limiter {
	return admission.New(admission.Limits{Read: networkWaitReads, Write: networkWaitReads})
}

// timedAnswer is one request's answer, when it was answered and how long it took.
type timedAnswer struct {
	status     int
	body       string
	retryAfter string
	ended      time.Time
	elapsed    time.Duration
}

// serveTimed serves request on handler and answers how and when it was answered.
func serveTimed(handler http.Handler, request *http.Request) timedAnswer {
	started := time.Now()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	ended := time.Now()
	return timedAnswer{
		status: recorder.Code, body: recorder.Body.String(), retryAfter: recorder.Header().Get("Retry-After"),
		ended: ended, elapsed: ended.Sub(started),
	}
}

// whoamiAs is a GET /api/v1/whoami carrying header, a browser's IAP assertion or an agent's
// bearer.
func whoamiAs(header http.Header) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	request.Header = header.Clone()
	return request
}

// bearerHeader is the Authorization header of a bearer token.
func bearerHeader(token string) http.Header {
	return http.Header{"Authorization": []string{"Bearer " + token}}
}

// waiterGroup is the requests a row sets waiting on the issuer, each answering on answers, and
// the goroutines serving them, which the row's cleanup waits out.
type waiterGroup struct {
	answers chan timedAnswer
	working sync.WaitGroup
}

func startWaiters(handler http.Handler, requests []*http.Request) *waiterGroup {
	group := &waiterGroup{answers: make(chan timedAnswer, len(requests))}
	for _, request := range requests {
		group.working.Go(func() { group.answers <- serveTimed(handler, request) })
	}
	return group
}

// collect receives every waiter's answer, failing the row for one not answered within bound.
func (g *waiterGroup) collect(t *testing.T, count int, bound time.Duration) []timedAnswer {
	t.Helper()
	answers := make([]timedAnswer, 0, count)
	deadline := time.After(bound)
	for range count {
		select {
		case answer := <-g.answers:
			answers = append(answers, answer)
		case <-deadline:
			t.Fatalf("%d of %d waiters were answered within %v", len(answers), count, bound)
		}
	}
	return answers
}

// requireCachedCallersServed sends each request of cached in turn while the waiters wait, and
// requires each answered 200 within normalLatency: admitted at once, as nothing of the read class
// is held by the waiters.
func requireCachedCallersServed(t *testing.T, handler http.Handler, cached map[string]func() *http.Request) {
	t.Helper()
	for name, request := range cached {
		for attempt := range networkWaiters {
			answer := serveTimed(handler, request())
			if answer.status != http.StatusOK || answer.elapsed > normalLatency {
				t.Errorf("%s, request %d while %d callers wait on the issuer: status=%d after %v body=%s, want 200 within %v",
					name, attempt+1, networkWaiters, answer.status, answer.elapsed, strings.TrimSpace(answer.body), normalLatency)
			}
		}
	}
}

// requireWaitersHoldNoReadUnit requires every waiter to have reached its caller's resolution - it
// was admitted, as the waiters hold no unit to queue behind - and the read class to hold no unit
// while they wait on the issuer: the units in flight do not count them. A waiter's resolution is
// counted as it begins, a moment before it gives its unit back, so the read class is read until it
// holds none, for as long as a resolution is given to begin; a waiter that held its unit across the
// wait would hold it until the row lets the issuer answer, which comes after this.
func requireWaitersHoldNoReadUnit(t *testing.T, limiter *admission.Limiter, begun func() int32, waiting func() bool) {
	t.Helper()
	if !testwait.Holds(2*time.Second, time.Millisecond, func() bool { return int(begun()) == networkWaiters && waiting() }) {
		t.Errorf("%d of %d waiters reached their caller's resolution with the issuer's read held, want every one: the rest queued for a read unit", begun(), networkWaiters)
	}
	if !testwait.Holds(time.Second, time.Millisecond, func() bool {
		stats := limiter.Stats()[admission.Read]
		return stats.InFlight == 0 && stats.Queued == 0
	}) {
		stats := limiter.Stats()[admission.Read]
		t.Errorf("with %d callers waiting on the issuer, the read class holds %d units and queues %d requests, want neither: no unit is held across the wait",
			networkWaiters, stats.InFlight, stats.Queued)
	}
}

// heldKeySetReads carries the reads of the test's IAP key cache: at once while hold is unset, and
// while it is set each read waits until release closes, or the read's own bound ends it
// (oidc.Keys.FetchTimeout), before it goes to the network. held counts the reads it held.
type heldKeySetReads struct {
	hold    atomic.Bool
	held    atomic.Int32
	release chan struct{}
	letGo   func()
}

func newHeldKeySetReads() *heldKeySetReads {
	h := &heldKeySetReads{release: make(chan struct{})}
	h.letGo = sync.OnceFunc(func() { close(h.release) })
	return h
}

func (h *heldKeySetReads) RoundTrip(request *http.Request) (*http.Response, error) {
	if h.hold.Load() {
		h.held.Add(1)
		select {
		case <-h.release:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	}
	return http.DefaultTransport.RoundTrip(request)
}

// iapWaitRig is a Dispatch behind IAP whose read class holds networkWaitReads units, with IAP's
// key set served on this machine through a transport the row holds: the set is read once, for
// the cached key, and a key IAP rotated in after it is read for under the row's hold. person
// counts the resolutions begun after that first read; timeout is the key set read's bound.
type iapWaitRig struct {
	handler http.Handler
	limiter *admission.Limiter
	person  *countedLogins
	reads   *heldKeySetReads
	issuer  *oidctest.Issuer
	cached  *oidctest.Key
	rotated *oidctest.Key
	timeout time.Duration
	waiting []*waiterGroup
}

func newIAPWaitRig(t *testing.T) *iapWaitRig {
	t.Helper()
	return newIAPWaitRigOn(t, nil)
}

// newIAPWaitRigOn is newIAPWaitRig with clock as its key cache's clock (oidc.Keys.Now), nil for
// the wall clock.
func newIAPWaitRigOn(t *testing.T, clock func() time.Time) *iapWaitRig {
	t.Helper()
	database := storetest.Open(t)
	issuer := oidctest.New(t)
	cached := oidctest.NewES256Key(t, "iap-key")
	issuer.Publish(cached)
	reads := newHeldKeySetReads()
	source := oidc.NewKeysAt(issuer.URL() + "/keys")
	source.HTTPClient = &http.Client{Transport: reads}
	source.Now = clock
	rig := &iapWaitRig{
		limiter: networkWaitLimiter(),
		person: &countedLogins{Identity: identity.IAPIdentity{
			Verifier: oidc.NewIAPVerifier(iapSocketAudience, source),
			Rule:     identity.DomainRule{Domain: "example.com"},
			People:   store.NewPgPeopleStore(database.Pool, testSigningKey, nil),
		}},
		reads: reads, issuer: issuer, cached: cached, timeout: source.FetchTimeout,
	}
	rig.handler, _, _ = newTestServer(t, testServerOptions{store: database, admission: rig.limiter, identity: rig.person})
	// Before the server's own cleanups, so no waiter is still held on the key set when the server
	// and its database close.
	t.Cleanup(func() {
		reads.letGo()
		for _, group := range rig.waiting {
			group.working.Wait()
		}
	})
	if answer := serveTimed(rig.handler, whoamiAs(rig.assertion(t, cached))); answer.status != http.StatusOK {
		t.Fatalf("the first assertion, which reads the set: status=%d body=%s, want 200", answer.status, answer.body)
	}
	rig.person.begun.Store(0)
	rig.rotated = oidctest.NewES256Key(t, "iap-key-rotated")
	issuer.Publish(rig.rotated)
	reads.hold.Store(true)
	return rig
}

// assertion is the header of a request IAP passed for a.person@example.com, signed by key.
func (r *iapWaitRig) assertion(t *testing.T, key *oidctest.Key) http.Header {
	t.Helper()
	return iapSocketKeys{keys: r.issuer, signing: key}.assertion(t, time.Now().Add(9*time.Minute))
}

// boundaryClock is a key cache's clock (oidc.Keys.Now) that reads at, a fixed time, everywhere but
// inside a call of oidc.Keys.Key once the row has armed a jump, where it reads at+jump. A boundary
// the jump crosses - the set's Lifetime ending, the miss window opening again - so falls between
// everything the request did before its key lookup (its admission, and any question asked of the
// cache before the lookup, as the predicate this branch once asked was) and the lookup itself,
// at the same point on every run.
type boundaryClock struct {
	at   time.Time
	jump atomic.Int64
}

func (c *boundaryClock) now() time.Time {
	if jump := time.Duration(c.jump.Load()); jump != 0 && insideKeyLookup() {
		return c.at.Add(jump)
	}
	return c.at
}

// insideKeyLookup reports whether the caller runs inside oidc.Keys.Key, inlined or not.
func insideKeyLookup() bool {
	callers := make([]uintptr, 64)
	frames := runtime.CallersFrames(callers[:runtime.Callers(2, callers)])
	for {
		frame, more := frames.Next()
		if strings.HasSuffix(frame.Function, "/oidc.(*Keys).Key") {
			return true
		}
		if !more {
			return false
		}
	}
}

// A boundary of the key cache passed between a request's admission and its key lookup - the set's
// Lifetime ends, MinRefresh past it, or the miss window for a key id the set does not hold opens
// again - turns a lookup the cache would have answered from memory into a read of IAP's key set,
// and the request still gives its admission unit back before it waits on that read: the read
// class's in-flight count reads 0 while the read is held. The cache's clock crosses the boundary
// only inside the lookup (boundaryClock), so it falls after anything the request asked the cache
// before it, every run. Once the read answers, the request takes its unit back and is answered
// 200.
func TestAKeySetBoundaryPassedBeforeTheLookupStillGivesTheUnitBackBeforeTheWait(t *testing.T) {
	for _, boundary := range []string{"the set's Lifetime ends", "the miss window opens again"} {
		t.Run(boundary, func(t *testing.T) {
			clock := &boundaryClock{at: time.Now()}
			rig := newIAPWaitRigOn(t, clock.now)
			presented := rig.cached
			// A set stale by less than MinRefresh still answers the key it holds at once, and
			// refreshes in the background (oidc.Keys.Key): the lookup waits on a read only past
			// Lifetime + MinRefresh.
			jump := oidc.DefaultKeysLifetime + oidc.DefaultKeysMinRefresh + time.Second
			if boundary == "the miss window opens again" {
				// The rotated key's lookup reads the set while the window is open and closes it;
				// a key published after that read is then one the set does not hold, inside the
				// window, until the window opens again.
				rig.reads.hold.Store(false)
				if answer := serveTimed(rig.handler, whoamiAs(rig.assertion(t, rig.rotated))); answer.status != http.StatusOK {
					t.Fatalf("the rotated key's assertion, which reads the set and closes the miss window: status=%d body=%s, want 200", answer.status, answer.body)
				}
				rig.reads.hold.Store(true)
				presented = oidctest.NewES256Key(t, "iap-key-published-later")
				rig.issuer.Publish(presented)
				jump = oidc.DefaultKeysMinRefresh + time.Second
			}
			rig.person.begun.Store(0)
			clock.jump.Store(int64(jump))
			group := startWaiters(rig.handler, []*http.Request{whoamiAs(rig.assertion(t, presented))})
			rig.waiting = append(rig.waiting, group)
			if !testwait.Holds(5*time.Second, time.Millisecond, func() bool {
				return rig.person.begun.Load() == 1 && rig.reads.held.Load() == 1
			}) {
				t.Fatalf("the request's resolution began %d times and %d key set reads are held, want the one resolution waiting on the one read", rig.person.begun.Load(), rig.reads.held.Load())
			}
			if !testwait.Holds(time.Second, time.Millisecond, func() bool {
				return rig.limiter.Stats()[admission.Read].InFlight == 0
			}) {
				t.Errorf("with the request waiting on the key set read its passed boundary started, the read class holds %d units, want none: the unit is given back before the wait",
					rig.limiter.Stats()[admission.Read].InFlight)
			}
			rig.reads.letGo()
			answer := group.collect(t, 1, rig.timeout+5*time.Second)[0]
			if answer.status != http.StatusOK {
				t.Errorf("the request once the read answered: status=%d body=%s, want 200", answer.status, strings.TrimSpace(answer.body))
			}
			if stats := rig.limiter.Stats()[admission.Read]; stats.InFlight != 0 {
				t.Errorf("once the request was answered the read class holds %d units, want none", stats.InFlight)
			}
		})
	}
}

// waitOnRotatedKey sets count browsers going whose assertions name the rotated key, which the
// cached set does not hold, so each verification reads the set or waits for that read.
func (r *iapWaitRig) waitOnRotatedKey(t *testing.T, count int) *waiterGroup {
	t.Helper()
	requests := make([]*http.Request, count)
	for i := range requests {
		requests[i] = whoamiAs(r.assertion(t, r.rotated))
	}
	group := startWaiters(r.handler, requests)
	r.waiting = append(r.waiting, group)
	return group
}

// Rows 1 and 2 behind IAP: a key IAP rotated in sends every browser presenting it to read IAP's
// key set, and that read is held - until the row lets it go with the new key in the set, or for
// its whole bound, the keys' ten-second FetchTimeout, after which it fails. While it is held, the
// waiters hold no read unit (the read class's in-flight count is zero with all of them inside
// their caller's resolution), and the requests whose callers resolve from memory - an agent's
// bearer, a browser whose assertion names the cached key - are admitted and answered 200 at
// normal latency. Each waiter still ends within the read's bound, plus the take-back's own bound
// of a read's queue wait, with the answer the read gives it: 200 once the keys arrive, and the
// 503 SIGNIN_UNAVAILABLE an unreadable key set gives where the read times out.
func TestAnIAPKeySetReadHoldsNoAdmissionUnitWhileItsWaitersWait(t *testing.T) {
	for _, keysArrive := range []bool{true, false} {
		t.Run(fmt.Sprintf("the keys arrive: %t", keysArrive), func(t *testing.T) {
			rig := newIAPWaitRig(t)
			waiters := rig.waitOnRotatedKey(t, networkWaiters)
			requireWaitersHoldNoReadUnit(t, rig.limiter, rig.person.begun.Load, func() bool { return rig.reads.held.Load() == 1 })
			requireCachedCallersServed(t, rig.handler, map[string]func() *http.Request{
				"an agent's bearer":                   func() *http.Request { return whoamiAs(bearerHeader("agent-token")) },
				"a browser presenting the cached key": func() *http.Request { return whoamiAs(rig.assertion(t, rig.cached)) },
			})
			want, code := http.StatusOK, ""
			if keysArrive {
				rig.reads.letGo()
			} else {
				want, code = http.StatusServiceUnavailable, `"code":"SIGNIN_UNAVAILABLE"`
			}
			bound := rig.timeout + admission.DefaultReadWait
			for _, answer := range waiters.collect(t, networkWaiters, bound+5*time.Second) {
				if answer.status != want || !strings.Contains(answer.body, code) || answer.elapsed > bound {
					t.Errorf("a waiter on the rotated key: status=%d after %v body=%s, want %d %s within the read's %v and the take-back's %v",
						answer.status, answer.elapsed, strings.TrimSpace(answer.body), want, code, rig.timeout, admission.DefaultReadWait)
				}
			}
			if stats := rig.limiter.Stats()[admission.Read]; stats.InFlight != 0 {
				t.Errorf("once every waiter was answered the read class holds %d units, want none", stats.InFlight)
			}
		})
	}
}

// inFlightSampler reads a limiter's units in flight every millisecond until stop, keeping the
// most the read class and the combined cap held at once.
type inFlightSampler struct {
	stop    func()
	mu      sync.Mutex
	read    int
	total   int
	stopped chan struct{}
}

func sampleInFlight(limiter *admission.Limiter) *inFlightSampler {
	s := &inFlightSampler{stopped: make(chan struct{})}
	done := make(chan struct{})
	s.stop = sync.OnceFunc(func() {
		close(done)
		<-s.stopped
	})
	go func() {
		defer close(s.stopped)
		for {
			read, total := limiter.Stats()[admission.Read].InFlight, limiter.Totals().InFlight
			s.mu.Lock()
			s.read, s.total = max(s.read, read), max(s.total, total)
			s.mu.Unlock()
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	return s
}

// most stops the sampling and answers the most units the read class and the combined cap held.
func (s *inFlightSampler) most() (read, total int) {
	s.stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read, s.total
}

// The take-back is bounded and refused as admission refuses: with the read class full when the key
// set's read answers - the row itself holds every read unit - each waiter asks for its unit back,
// waits at most a read's queue wait for it, and is answered 429 OVERLOADED with Retry-After: 1,
// never left waiting and never admitted over the cap. The units in flight, sampled through the
// whole row, never pass the read class's limit nor the combined cap, and none is left counted for a
// waiter once it was refused.
func TestAnIAPKeySetWaiterRefusedItsUnitBackIsAnsweredOverloaded(t *testing.T) {
	rig := newIAPWaitRig(t)
	sampler := sampleInFlight(rig.limiter)
	defer sampler.stop()
	const count = networkWaitReads + 1
	waiters := rig.waitOnRotatedKey(t, count)
	if !testwait.Holds(5*time.Second, time.Millisecond, func() bool {
		return rig.person.begun.Load() == count && rig.reads.held.Load() == 1 && rig.limiter.Stats()[admission.Read].InFlight == 0
	}) {
		t.Fatalf("%d of %d waiters reached their caller's resolution with the key set's read held (%d reads held), and the read class holds %d units: want every waiter in, holding none",
			rig.person.begun.Load(), count, rig.reads.held.Load(), rig.limiter.Stats()[admission.Read].InFlight)
	}
	held := make([]*admission.Ticket, 0, networkWaitReads)
	defer func() {
		for _, ticket := range held {
			ticket.Release()
		}
	}()
	for range networkWaitReads {
		ticket := rig.limiter.Arrive(admission.Read, 1).AdmitNow()
		if ticket == nil {
			t.Fatalf("the row could take %d of the read class's %d units with the waiters waiting on the key set, want every one free", len(held), networkWaitReads)
		}
		held = append(held, ticket)
	}
	released := time.Now()
	rig.reads.letGo()
	for _, answer := range waiters.collect(t, count, admission.DefaultReadWait+5*time.Second) {
		afterRead := answer.ended.Sub(released)
		if answer.status != http.StatusTooManyRequests || !strings.Contains(answer.body, `"code":"`+admission.CodeOverloaded+`"`) || answer.retryAfter != admission.RetryAfter {
			t.Errorf("a waiter whose unit was not free when the key set answered: status=%d Retry-After=%q body=%s, want 429 %s with Retry-After: %s",
				answer.status, answer.retryAfter, strings.TrimSpace(answer.body), admission.CodeOverloaded, admission.RetryAfter)
		}
		if afterRead > admission.DefaultReadWait+500*time.Millisecond {
			t.Errorf("a waiter was answered %v after the key set answered, want within a read's %v queue wait", afterRead, admission.DefaultReadWait)
		}
	}
	for _, ticket := range held {
		ticket.Release()
	}
	read, total := sampler.most()
	if limit := rig.limiter.Totals().Limit; read > networkWaitReads || total > limit {
		t.Errorf("through the row the read class held up to %d units and the combined cap %d, past their limits %d and %d", read, total, networkWaitReads, limit)
	}
	if stats := rig.limiter.Stats()[admission.Read]; stats.InFlight != 0 || stats.Queued != 0 {
		t.Errorf("once the waiters were refused and the row let its units go, the read class holds %d units and queues %d, want neither", stats.InFlight, stats.Queued)
	}
	t.Logf("most units in flight through the row: %d of the read class's %d, %d of the combined cap's %d", read, networkWaitReads, total, rig.limiter.Totals().Limit)
}

// countedVerifier is a service-account token verifier that counts the verifications begun with
// it, so a row knows when every token it set going has reached its caller's resolution.
type countedVerifier struct {
	ServiceTokenVerifier
	begun atomic.Int32
}

func (c *countedVerifier) Verify(ctx context.Context, raw string) (oidc.Claims, error) {
	c.begun.Add(1)
	return c.ServiceTokenVerifier.Verify(ctx, raw)
}

// discoveryAttemptBound is how long one read of a service-account issuer's discovery document may
// take before it fails (oidc's discoveryAttemptTimeout).
const discoveryAttemptBound = 10 * time.Second

// Row 3: the same for a service-account token while its issuer's discovery is held. The issuer is
// undiscovered, so the first token's verification reads its discovery document, and the others
// wait for that read, which the issuer holds - four seconds, after which the document and the key
// set answer, or past the read's ten-second bound, after which it fails. While it is held the
// tokens hold no read unit and an agent's bearer is answered 200 at normal latency, and each token
// ends within the read's bound, plus the take-back's, with the answer the read gives it: 200 once
// the issuer answers, and the 503 OIDC_ISSUER_UNAVAILABLE an issuer that cannot be reached gives.
func TestAServiceTokenIssuersDiscoveryHoldsNoAdmissionUnitWhileItsWaitersWait(t *testing.T) {
	for _, answers := range []bool{true, false} {
		t.Run(fmt.Sprintf("the issuer answers: %t", answers), func(t *testing.T) {
			issuer := newServiceTokenIssuer(t)
			hold := 4 * time.Second
			want, code := http.StatusOK, ""
			if !answers {
				hold = discoveryAttemptBound + 5*time.Second
				want, code = http.StatusServiceUnavailable, `"code":"OIDC_ISSUER_UNAVAILABLE"`
			}
			issuer.DelayDiscovery(hold)
			limiter := networkWaitLimiter()
			verifier := &countedVerifier{ServiceTokenVerifier: oidc.NewLazyVerifier(issuer.URL(), serviceTokenAudience)}
			handler, _, _ := newTestServer(t, testServerOptions{admission: limiter, oidc: verifier})
			token := issuer.mint(t, serviceTokenAudience)
			requests := make([]*http.Request, networkWaiters)
			for i := range requests {
				requests[i] = whoamiAs(bearerHeader(token))
			}
			waiters := startWaiters(handler, requests)
			// Before the server's own cleanups, so no token is still waiting on the issuer when the
			// server closes.
			t.Cleanup(waiters.working.Wait)
			requireWaitersHoldNoReadUnit(t, limiter, verifier.begun.Load, func() bool { return issuer.DiscoveryRequests() == 1 })
			requireCachedCallersServed(t, handler, map[string]func() *http.Request{
				"an agent's bearer": func() *http.Request { return whoamiAs(bearerHeader("agent-token")) },
			})
			bound := discoveryAttemptBound + admission.DefaultReadWait
			for _, answer := range waiters.collect(t, networkWaiters, bound+5*time.Second) {
				if answer.status != want || !strings.Contains(answer.body, code) || answer.elapsed > bound {
					t.Errorf("a service token waiting on its issuer's discovery: status=%d after %v body=%s, want %d %s within the read's %v and the take-back's %v",
						answer.status, answer.elapsed, strings.TrimSpace(answer.body), want, code, discoveryAttemptBound, admission.DefaultReadWait)
				}
			}
			if stats := limiter.Stats()[admission.Read]; stats.InFlight != 0 {
				t.Errorf("once every token was answered the read class holds %d units, want none", stats.InFlight)
			}
		})
	}
}

// The rows below do the same for a read's own wait on another server (awayOnNetwork): the Envoy
// listener, GitHub, the embedder a search embeds its query with, and the secrets broker. Each
// holds its server, in front of the fake the route's own tests serve (heldService), with more
// reads waiting on it than the read class has units, and requires what the rows above require:
// no read unit held and none queued while they wait, a read that waits on nothing admitted at
// once meanwhile, and every waiter answered 200, as before, once the server answers. Before, each
// waiter held its read unit and its pinned connection for the whole wait, the class filled, and
// the reads behind it were refused 429 OVERLOADED.

// heldService serves upstream, a route's own test fake, behind a gate: a request hold names is
// counted in calls as it arrives and waits until release, or until its client gives up, and every
// other request reaches upstream at once. Requests reach upstream one at a time, since a fake may
// record what it serves unguarded (fakeBrokerRig).
func heldService(t *testing.T, upstream http.Handler, hold func(*http.Request) bool) (address string, calls *atomic.Int32, release func()) {
	t.Helper()
	calls = &atomic.Int32{}
	gate := make(chan struct{})
	var serial sync.Mutex
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hold(r) {
			calls.Add(1)
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		serial.Lock()
		defer serial.Unlock()
		upstream.ServeHTTP(w, r)
	}))
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	// Release before the close, which waits for every request in flight to finish.
	t.Cleanup(front.Close)
	t.Cleanup(release)
	return front.URL, calls, release
}

// proxyTo passes every request to the server at address, a fake a route's own tests stand up.
func proxyTo(t *testing.T, address string) http.Handler {
	t.Helper()
	target, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse the fake's address %q: %v", address, err)
	}
	return httputil.NewSingleHostReverseProxy(target)
}

// requireReadsHoldNoUnitOnService sets the reads of requests waiting on service, which the row
// holds until release, and requires them to hold no read unit and queue none while they wait
// (calls counts the ones that reached it), a project list, which waits on nothing, answered 200
// at normal latency meanwhile, and every waiter answered 200 once service answers, the read class
// holding nothing after.
func requireReadsHoldNoUnitOnService(t *testing.T, service string, handler http.Handler, limiter *admission.Limiter,
	requests []*http.Request, calls func() int32, release func(),
) {
	t.Helper()
	waiters := startWaiters(handler, requests)
	// Before the server's own cleanups, and releasing the service first, so no read is still
	// waiting on it when the server closes, whatever the row met.
	t.Cleanup(func() {
		release()
		waiters.working.Wait()
	})
	requireWaitersHoldNoReadUnit(t, limiter, calls, func() bool { return true })
	requireCachedCallersServed(t, handler, map[string]func() *http.Request{
		"a project list": func() *http.Request { return testRequestAs(t, http.MethodGet, "/api/v1/projects", "alice") },
	})
	release()
	for _, answer := range waiters.collect(t, networkWaiters, 10*time.Second) {
		if answer.status != http.StatusOK {
			t.Errorf("a read waiting on %s: status=%d after %v body=%s, want 200 once it answered",
				service, answer.status, answer.elapsed, strings.TrimSpace(answer.body))
		}
	}
	if stats := limiter.Stats()[admission.Read]; stats.InFlight != 0 {
		t.Errorf("once every read waiting on %s was answered the read class holds %d units, want none", service, stats.InFlight)
	}
}

// The listener: an issue's collaborators wait on its Legion agent registry (issueLegionAgents),
// and an issue's subscribers on its interests and then its sessions (writeSubscribers), half the
// waiters each, the registry and the interests held.
func TestAReadWaitingOnTheListenerHoldsNoReadUnit(t *testing.T) {
	registry := &legionRegistryListener{}
	subscribers, listener := newFakeListener(t, nil, nil)
	upstream := http.NewServeMux()
	upstream.Handle("/v1/legion/agents", proxyTo(t, registry.serve(t).URL))
	upstream.Handle("/", proxyTo(t, subscribers))
	address, calls, release := heldService(t, upstream, func(r *http.Request) bool {
		return r.URL.Path == "/v1/legion/agents" || r.URL.Path == "/v1/interests/"
	})
	limiter := networkWaitLimiter()
	handler, _, _ := newTestServer(t, testServerOptions{envoyURL: address, envoyTimeout: time.Minute, admission: limiter})
	key := createTestIssue(t, handler, "TEST", "Issue")
	registry.set(key, legionAgentEntry("ses-planner", key, "planner", "2026-11-24T00:00:00Z", nil, nil))
	listener.mu.Lock()
	listener.interests["ses-planner"] = []string{issueSubscriberBase(key) + ".>"}
	listener.mu.Unlock()

	requests := make([]*http.Request, 0, networkWaiters)
	for index := range networkWaiters {
		target := "/api/v1/issues/" + key + "/collaborators"
		if index%2 == 1 {
			target = "/api/v1/issues/" + key + "/subscribers"
		}
		requests = append(requests, testRequestAs(t, http.MethodGet, target, "alice"))
	}
	requireReadsHoldNoUnitOnService(t, "the listener", handler, limiter, requests, calls.Load, release)
}

// GitHub: each read asks whether the App is installed on a repository of its own
// (getAppInstallation), so each makes an installation lookup, which GitHub holds.
func TestAReadWaitingOnGitHubHoldsNoReadUnit(t *testing.T) {
	installation := func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/installation") }
	github := githubtest.Server(t, githubtest.Spec{Handle: func(w http.ResponseWriter, r *http.Request) {
		if !installation(r) {
			githubtest.Unexpected(t, w, r)
			return
		}
		fmt.Fprint(w, `{"id":77,"app_slug":"dispatch-test","permissions":{}}`)
	}})
	address, calls, release := heldService(t, proxyTo(t, github), installation)
	limiter := networkWaitLimiter()
	options := appInstallationOptions(t, address)
	options.admission = limiter
	handler, _, _ := newTestServer(t, options)

	requests := make([]*http.Request, 0, networkWaiters)
	for index := range networkWaiters {
		requests = append(requests, testRequestAs(t, http.MethodGet, fmt.Sprintf("/api/v1/github/app-installation/acme-%d/widgets", index), "alice"))
	}
	requireReadsHoldNoUnitOnService(t, "GitHub", handler, limiter, requests, calls.Load, release)
}

// heldEmbedder is the search tests' fake embedder with every query's embedding held: each is
// counted in calls as it arrives and waits until release, or until its bound
// (searchEmbedTimeout) ends it, before the fake answers it.
type heldEmbedder struct {
	*fakeEmbedder
	calls atomic.Int32
	gate  chan struct{}
	once  sync.Once
}

func (h *heldEmbedder) Embed(ctx context.Context, texts []string, inputType embed.InputType) ([][]float32, error) {
	if inputType == embed.InputQuery {
		h.calls.Add(1)
		select {
		case <-h.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return h.fakeEmbedder.Embed(ctx, texts, inputType)
}

func (h *heldEmbedder) release() { h.once.Do(func() { close(h.gate) }) }

// The embedder: each read is a search, which embeds its query (runSearch), and the embedder holds
// it. The search lane is as wide as the waiters, so the read class is what would hold them.
func TestAReadWaitingOnTheEmbedderHoldsNoReadUnit(t *testing.T) {
	embedder := &heldEmbedder{fakeEmbedder: &fakeEmbedder{}, gate: make(chan struct{})}
	t.Cleanup(embedder.release)
	limiter := admission.New(admission.Limits{Read: networkWaitReads, Write: networkWaitReads, HeavyReadLane: networkWaiters})
	handler, _, _ := newTestServer(t, testServerOptions{embedder: embedder, admission: limiter})

	requests := make([]*http.Request, 0, networkWaiters)
	for range networkWaiters {
		requests = append(requests, testRequestAs(t, http.MethodGet, "/api/v1/search?q=wibble", "alice"))
	}
	requireReadsHoldNoUnitOnService(t, "the embedder", handler, limiter, requests, embedder.calls.Load, embedder.release)
}

// The embedder again, for the related-item check's read: each read is GET
// /api/v1/projects/{key}/related, which embeds its text before its stages start
// (relatedInput.embedFirst), and the embedder holds it. The route reads with two units at once
// (concurrentReads), so the read class holds two of its checks, and the six waiters would need
// twelve units.
func TestARelatedItemReadWaitingOnTheEmbedderHoldsNoReadUnit(t *testing.T) {
	embedder := &heldEmbedder{fakeEmbedder: &fakeEmbedder{}, gate: make(chan struct{})}
	t.Cleanup(embedder.release)
	limiter := admission.New(admission.Limits{Read: concurrentReadWeight * networkWaitReads, Write: networkWaitReads})
	handler, _, _ := newTestServer(t, testServerOptions{embedder: embedder, admission: limiter})
	project := dispatchRequest(t, handler, http.MethodPost, "/api/v1/projects", map[string]string{"key": "RHOLD", "name": "Related hold"}, "alice")
	if project.Code != http.StatusCreated {
		t.Fatalf("create project: status=%d body=%s", project.Code, project.Body.String())
	}

	requests := make([]*http.Request, 0, networkWaiters)
	for range networkWaiters {
		requests = append(requests, testRequestAs(t, http.MethodGet, "/api/v1/projects/RHOLD/related?title=Zymurgy+ledger+drifts", "alice"))
	}
	requireReadsHoldNoUnitOnService(t, "the embedder", handler, limiter, requests, embedder.calls.Load, embedder.release)
}

// The secrets broker: each read is a person's list of the credential requests waiting on them
// (listCredentialPending), which the broker holds.
func TestAReadWaitingOnTheSecretsBrokerHoldsNoReadUnit(t *testing.T) {
	broker := newFakeBrokerRig(t)
	broker.bodyByRoute["GET /v1/pending"] = `{"pending":[]}`
	address, calls, release := heldService(t, proxyTo(t, broker.server.URL), func(r *http.Request) bool { return r.URL.Path == "/v1/pending" })
	limiter := networkWaitLimiter()
	handler, _, _ := newTestServer(t, testServerOptions{agentSecretsURL: address, admission: limiter})

	requests := make([]*http.Request, 0, networkWaiters)
	for range networkWaiters {
		requests = append(requests, testRequestAs(t, http.MethodGet, "/api/v1/credential-requests?approver=me", "alice"))
	}
	requireReadsHoldNoUnitOnService(t, "the secrets broker", handler, limiter, requests, calls.Load, release)
}
