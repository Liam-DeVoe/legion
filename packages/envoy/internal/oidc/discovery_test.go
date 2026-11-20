package oidc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/clock/clocktest"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
	"github.com/sjawhar/envoy/internal/testwait"
)

// newTestClock is the clock a test moves by hand, for the spacing between failed discovery
// attempts and the background loop's waits.
func newTestClock() *clocktest.Fake {
	return clocktest.New(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC))
}

// A code flow and a lazy verifier read nothing when they are made: a process builds both at boot,
// and the issuer being down must not stop it.
func TestNothingIsDiscoveredAtConstruction(t *testing.T) {
	issuer := oidctest.New(t)
	issuer.EnableCodeFlow(issuer.PublishKey(t, "signing-key"), flowClientID, flowClientSecret)
	flow := NewCodeFlow(issuer.URL(), flowClientID, flowClientSecret, "")
	verifier := NewLazyVerifier(issuer.URL(), testAudience)
	if got := issuer.DiscoveryRequests(); got != 0 {
		t.Fatalf("constructing a code flow and a lazy verifier read the discovery document %d times, want none", got)
	}
	if flow.Discovered() || verifier.Discovered() {
		t.Fatalf("Discovered before any read: code flow %t, verifier %t, want both false", flow.Discovered(), verifier.Discovered())
	}
}

// Until its issuer is discovered, every call of a code flow fails with ErrIssuerUnavailable, and
// the calls made within five seconds of a failed attempt read the issuer no more; once the issuer
// answers, the next call discovers it and works.
func TestCodeFlowFailsUnavailableUntilDiscoveredAndWorksAfter(t *testing.T) {
	issuer := oidctest.New(t)
	issuer.EnableCodeFlow(issuer.PublishKey(t, "signing-key"), flowClientID, flowClientSecret)
	issuer.FailDiscovery(http.StatusServiceUnavailable)
	flow := NewCodeFlow(issuer.URL(), flowClientID, flowClientSecret, "")
	clock := newTestClock()
	flow.discovery.clock = clock

	ctx := context.Background()
	calls := map[string]func() error{
		"AuthURL": func() error {
			_, err := flow.AuthURL(ctx, flowRedirect, "state-1", "nonce-1")
			return err
		},
		"Exchange": func() error {
			_, err := flow.Exchange(ctx, flowRedirect, "a-code", "nonce-1")
			return err
		},
		"Refresh": func() error {
			_, err := flow.Refresh(ctx, "a-refresh-token")
			return err
		},
		"Revoke": func() error { return flow.Revoke(ctx, "a-refresh-token") },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrIssuerUnavailable) || errors.Is(err, ErrGrantRefused) {
			t.Errorf("%s before discovery: err = %v, want ErrIssuerUnavailable alone", name, err)
		}
	}
	if got := issuer.DiscoveryRequests(); got != 1 {
		t.Fatalf("four calls within five seconds of the first failed read the discovery document %d times, want once", got)
	}
	if flow.Discovered() {
		t.Fatal("a flow whose discovery failed reads as discovered")
	}

	issuer.FailDiscovery(0)
	clock.Advance(discoveryRetrySpacing)
	session := exchange(t, issuer, flow, personClaims("dispatch-members"))
	if !flow.Discovered() {
		t.Fatal("a flow that has signed someone in does not read as discovered")
	}
	if _, err := flow.Refresh(ctx, session.RefreshToken); err != nil {
		t.Fatalf("Refresh after discovery: %v", err)
	}
	if err := flow.Revoke(ctx, session.RefreshToken); err != nil {
		t.Fatalf("Revoke after discovery: %v", err)
	}
	if got := issuer.DiscoveryRequests(); got != 2 {
		t.Fatalf("the discovery document was read %d times, want twice: the failed read and the one that succeeded", got)
	}
}

// After a failed attempt, Discover reads the issuer again only once five seconds have passed;
// within them it fails at once with the last attempt's error.
func TestDiscoverSpacesFailedAttemptsFiveSecondsApart(t *testing.T) {
	issuer := oidctest.New(t)
	issuer.EnableCodeFlow(issuer.PublishKey(t, "signing-key"), flowClientID, flowClientSecret)
	issuer.FailDiscovery(http.StatusServiceUnavailable)
	flow := NewCodeFlow(issuer.URL(), flowClientID, flowClientSecret, "")
	clock := newTestClock()
	flow.discovery.clock = clock
	ctx := context.Background()

	for _, step := range []struct {
		advance time.Duration
		reads   int
	}{
		{0, 1},
		{time.Second, 1},
		{discoveryRetrySpacing - time.Second - time.Millisecond, 1},
		{time.Millisecond, 2},
		{discoveryRetrySpacing / 2, 2},
		{discoveryRetrySpacing / 2, 3},
	} {
		clock.Advance(step.advance)
		if err := flow.Discover(ctx); !errors.Is(err, ErrIssuerUnavailable) {
			t.Fatalf("Discover against a failing issuer: err = %v, want ErrIssuerUnavailable", err)
		}
		if got := issuer.DiscoveryRequests(); got != step.reads {
			t.Fatalf("after advancing %v: %d discovery reads, want %d", step.advance, got, step.reads)
		}
	}
	issuer.FailDiscovery(0)
	clock.Advance(discoveryRetrySpacing)
	if err := flow.Discover(ctx); err != nil {
		t.Fatalf("Discover once the issuer answers: %v", err)
	}
	clock.Advance(time.Hour)
	if err := flow.Discover(ctx); err != nil || issuer.DiscoveryRequests() != 4 {
		t.Fatalf("Discover after it succeeded: err = %v, %d reads, want nil and no read past the fourth", err, issuer.DiscoveryRequests())
	}
}

// Concurrent calls share one discovery read.
func TestDiscoverIsOneReadForConcurrentCallers(t *testing.T) {
	issuer := oidctest.New(t)
	issuer.EnableCodeFlow(issuer.PublishKey(t, "signing-key"), flowClientID, flowClientSecret)
	issuer.DelayDiscovery(300 * time.Millisecond)
	flow := NewCodeFlow(issuer.URL(), flowClientID, flowClientSecret, "")
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Go(func() { errs <- flow.Discover(context.Background()) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Discover: %v", err)
		}
	}
	if got := issuer.DiscoveryRequests(); got != 1 {
		t.Fatalf("16 concurrent Discover calls read the discovery document %d times, want once", got)
	}
}

// A caller whose own context ends while a discovery read is in flight stops waiting with
// ErrIssuerUnavailable, and the read goes on for the callers that remain.
func TestDiscoverOutlivesACallerThatGoesAway(t *testing.T) {
	issuer := oidctest.New(t)
	issuer.EnableCodeFlow(issuer.PublishKey(t, "signing-key"), flowClientID, flowClientSecret)
	issuer.DelayDiscovery(300 * time.Millisecond)
	flow := NewCodeFlow(issuer.URL(), flowClientID, flowClientSecret, "")
	impatient, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := flow.Discover(impatient); !errors.Is(err, ErrIssuerUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Discover on a context that ends mid-read: err = %v, want ErrIssuerUnavailable from the context's deadline", err)
	}
	if err := flow.Discover(context.Background()); err != nil {
		t.Fatalf("Discover after the impatient caller left: %v", err)
	}
	if got := issuer.DiscoveryRequests(); got != 1 {
		t.Fatalf("the discovery document was read %d times, want once: the read the impatient caller started", got)
	}
}

// A lazy verifier fails with ErrIssuerUnavailable until its issuer is discovered, and verifies as
// Verifier does after.
func TestLazyVerifierVerifiesOnceItsIssuerIsDiscovered(t *testing.T) {
	issuer := oidctest.New(t)
	signing := issuer.PublishKey(t, "signing-key")
	issuer.FailDiscovery(http.StatusServiceUnavailable)
	verifier := NewLazyVerifier(issuer.URL(), testAudience)
	clock := newTestClock()
	verifier.discovery.clock = clock
	token := issuer.Mint(t, signing, issuer.Claims(testSubject, testAudience))

	if _, err := verifier.Verify(context.Background(), token); !errors.Is(err, ErrIssuerUnavailable) {
		t.Fatalf("Verify before discovery: err = %v, want ErrIssuerUnavailable", err)
	}
	if verifier.Discovered() {
		t.Fatal("a verifier whose discovery failed reads as discovered")
	}
	issuer.FailDiscovery(0)
	clock.Advance(discoveryRetrySpacing)
	claims, err := verifier.Verify(context.Background(), token)
	if err != nil || claims.Subject != testSubject {
		t.Fatalf("Verify once the issuer answers: %+v, %v; want %s", claims, err, testSubject)
	}
	if !verifier.Discovered() {
		t.Fatal("a verifier that has verified a token does not read as discovered")
	}
	if _, err := verifier.Verify(context.Background(), issuer.Mint(t, signing, issuer.Claims(testSubject, "another-audience"))); !errors.Is(err, ErrAudience) {
		t.Fatalf("Verify of a token for another audience: err = %v, want ErrAudience", err)
	}
}

// The background discovery (Run) attempts at once and, while attempts fail, again after a wait of
// discoveryRetryFirst that doubles after each failure up to discoveryRetryMax, each wait armed on
// its clock; an attempt its wait brings inside discoveryRetrySpacing of the last failed read
// answers that failure and reads nothing. Once the issuer answers, the next attempt that reads
// discovers it and the loop returns. Each wait is read off the fake the loop arms it on
// (AwaitArmed), so a loop that waits longer than its schedule, or stops retrying, fails here.
func TestTheBackgroundDiscoveryRetriesOnItsScheduleUntilTheIssuerAnswers(t *testing.T) {
	issuer := oidctest.New(t)
	issuer.PublishKey(t, "signing-key")
	issuer.FailDiscovery(http.StatusServiceUnavailable)
	verifier := NewLazyVerifier(issuer.URL(), testAudience)
	clock := newTestClock()
	verifier.discovery.clock = clock
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		verifier.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		testwait.Receive(t, "the background discovery to return once its context ended", ran)
	})

	// Each step's wait is armed once the attempt before it has finished, the read included, so
	// the reads are counted when it is.
	reads, lastRead, wait := 1, clock.Now(), discoveryRetryFirst
	for step := 1; ; step++ {
		armed := clock.AwaitArmed(t, fmt.Sprintf("the background discovery's wait %d", step), 1)
		if got := issuer.DiscoveryRequests(); got != reads {
			t.Fatalf("before wait %d: %d discovery reads, want %d", step, got, reads)
		}
		if len(armed) != 1 || !armed[0].Equal(clock.Now().Add(wait)) {
			t.Fatalf("wait %d: the loop armed %v at %s, want one wait of %v", step, armed, clock.Now().Format(time.TimeOnly), wait)
		}
		if wait == discoveryRetryMax {
			break
		}
		clock.Set(armed[0])
		if clock.Now().Sub(lastRead) >= discoveryRetrySpacing {
			reads, lastRead = reads+1, clock.Now()
		}
		wait = min(2*wait, discoveryRetryMax)
	}
	// The wait stays at discoveryRetryMax: one more failed read at it, then the issuer answers.
	clock.Set(clock.Now().Add(discoveryRetryMax))
	reads++
	if armed := clock.AwaitArmed(t, "the background discovery's wait after its longest", 1); len(armed) != 1 ||
		!armed[0].Equal(clock.Now().Add(discoveryRetryMax)) || issuer.DiscoveryRequests() != reads {
		t.Fatalf("after a wait of %v: armed %v with %d reads, want one more wait of %v and %d reads",
			discoveryRetryMax, armed, issuer.DiscoveryRequests(), discoveryRetryMax, reads)
	}
	issuer.FailDiscovery(0)
	clock.Set(clock.Now().Add(discoveryRetryMax))
	testwait.Receive(t, "the background discovery to return once the issuer answered", ran)
	if !verifier.Discovered() || issuer.DiscoveryRequests() != reads+1 {
		t.Fatalf("once the issuer answered: discovered %t with %d reads, want true and %d", verifier.Discovered(), issuer.DiscoveryRequests(), reads+1)
	}
}
