package oidc

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// issuerWaitMargin is how long before the test binary's deadline a flow untilTheTestEnds bounds
// gives up on its issuer, as testwait's waits give up: the time the failing test's cleanups and its
// failure have to run in before Go's alarm panics the binary.
const issuerWaitMargin = 30 * time.Second

// issuerWaitEnd is the moment, for a test binary whose deadline is deadline, at which a flow
// untilTheTestEnds bounds at now gives up on its issuer: the deadline less issuerWaitMargin, or
// less half of what is left when that is the smaller, so a run with a short -timeout still waits.
func issuerWaitEnd(deadline, now time.Time) time.Time {
	return deadline.Add(-min(issuerWaitMargin, deadline.Sub(now)/2))
}

// untilTheTestEnds makes flow wait for its issuer until one moment, the test's end
// (issuerWaitEnd), in place of the product's ten seconds for each read of the discovery document
// (discoveryAttemptTimeout) and each call to the token or revocation endpoint (TokenTimeout). Those
// bounds are for an issuer that accepts a call and never answers. The test's own issuer answers in
// the machine's time, which on a loaded machine can be longer, and a test of how a call fails then
// reads the bound's timeout as the issuer's answer. Under -timeout 0, which sets no deadline, only
// the issuer's answer ends a read or a call.
func untilTheTestEnds(t *testing.T, flow *CodeFlow) {
	t.Helper()
	flow.http.Timeout = 0
	deadline, bounded := t.Deadline()
	if !bounded {
		flow.discovery.armAttempt = func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		}
		return
	}
	end := issuerWaitEnd(deadline, time.Now())
	flow.discovery.armAttempt = func(ctx context.Context) (context.Context, context.CancelFunc) {
		return context.WithDeadline(ctx, end)
	}
	base := flow.http.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	flow.http.Transport = endingAt{base: base, end: end}
}

// endingAt is a transport whose every request ends at end, one moment whenever the request starts,
// where a client's Timeout counts from each request's own start.
type endingAt struct {
	base http.RoundTripper
	end  time.Time
}

func (e endingAt) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithDeadline(request.Context(), e.end)
	response, err := e.base.RoundTrip(request.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = cancelOnClose{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

// cancelOnClose is a response body that releases its request's deadline when it is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// A read of the discovery document runs under discoveryAttemptTimeout from its start, or under the
// bound a test armed in its place (armAttempt). The read's own deadline is read against the clock
// on both sides of the call, so the row holds on any machine.
func TestADiscoveryReadRunsUnderItsAttemptBound(t *testing.T) {
	read := func(deadline *time.Time, bounded *bool) func(ctx context.Context) (int, error) {
		return func(ctx context.Context) (int, error) {
			*deadline, *bounded = ctx.Deadline()
			return 0, errors.New("the read is not answered here")
		}
	}

	var deadline time.Time
	var bounded bool
	product := newDiscovery("https://issuer.example", read(&deadline, &bounded))
	before := time.Now()
	if _, err := product.discover(context.Background()); !errors.Is(err, ErrIssuerUnavailable) {
		t.Fatalf("discover: err = %v, want ErrIssuerUnavailable", err)
	}
	after := time.Now()
	if !bounded || deadline.Before(before.Add(discoveryAttemptTimeout)) || deadline.After(after.Add(discoveryAttemptTimeout)) {
		t.Fatalf("the product's read: deadline %v (bounded %t), want discoveryAttemptTimeout (%s) from the call, between %v and %v", deadline, bounded, discoveryAttemptTimeout, before.Add(discoveryAttemptTimeout), after.Add(discoveryAttemptTimeout))
	}

	armed := time.Now().Add(time.Hour)
	tests := newDiscovery("https://issuer.example", read(&deadline, &bounded))
	tests.armAttempt = func(ctx context.Context) (context.Context, context.CancelFunc) {
		return context.WithDeadline(ctx, armed)
	}
	if _, err := tests.discover(context.Background()); !errors.Is(err, ErrIssuerUnavailable) {
		t.Fatalf("discover: err = %v, want ErrIssuerUnavailable", err)
	}
	if !bounded || !deadline.Equal(armed) {
		t.Fatalf("a read a test armed: deadline %v (bounded %t), want the armed %v", deadline, bounded, armed)
	}
}

// A code flow waits for its issuer under the product's bounds, discoveryAttemptTimeout and
// TokenTimeout, until a test moves both to one moment, the test's end (untilTheTestEnds).
func TestACodeFlowWaitsForItsIssuerUnderTheProductsBoundsUntilATestMovesThem(t *testing.T) {
	flow := NewCodeFlow("https://issuer.example", flowClientID, flowClientSecret, "")
	if flow.discovery.armAttempt != nil {
		t.Fatal("a new flow's discovery read has a bound armed, want discoveryAttemptTimeout")
	}
	if got := flow.http.Timeout; got != TokenTimeout {
		t.Fatalf("a new flow's token call bound is %s, want TokenTimeout (%s)", got, TokenTimeout)
	}

	untilTheTestEnds(t, flow)
	if got := flow.http.Timeout; got != 0 {
		t.Fatalf("after untilTheTestEnds the token client's Timeout is %s, want none: the test's end bounds each call", got)
	}
	read, cancel := flow.discovery.attemptContext(context.Background())
	defer cancel()
	readEnd, readBounded := read.Deadline()
	testDeadline, testBounded := t.Deadline()
	if !testBounded {
		if readBounded || flow.http.Transport != nil {
			t.Fatalf("under -timeout 0: the discovery read ends at %v (bounded %t) and the token client's transport is %T, want no bound on either", readEnd, readBounded, flow.http.Transport)
		}
		return
	}
	call, ok := flow.http.Transport.(endingAt)
	if !ok || !readBounded || !call.end.Equal(readEnd) || !readEnd.Before(testDeadline) {
		t.Fatalf("the discovery read ends at %v (bounded %t) and the token client's transport is %T, want both ending at one moment before the test's deadline %v", readEnd, readBounded, flow.http.Transport, testDeadline)
	}
}

// The test's end leaves the failing test issuerWaitMargin before the binary's deadline, or half of
// what is left when that is less: a short -timeout still leaves a wait room to wait.
func TestTheTestsEndLeavesItsMarginOrHalfOfWhatIsLeft(t *testing.T) {
	now := time.Date(2026, 11, 22, 16, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		left, want time.Duration
	}{
		{10 * time.Minute, 10*time.Minute - issuerWaitMargin},
		{2 * issuerWaitMargin, issuerWaitMargin},
		{20 * time.Second, 10 * time.Second},
	} {
		if got := issuerWaitEnd(now.Add(tc.left), now).Sub(now); got != tc.want {
			t.Errorf("with %s left the issuer wait ends %s from now, want %s", tc.left, got, tc.want)
		}
	}
}

// A call through the transport untilTheTestEnds installs ends at the transport's end when its
// issuer never answers, and a call its issuer answers reads the whole answer.
func TestACallThroughTheTestsTransportEndsAtItsEnd(t *testing.T) {
	release := make(chan struct{})
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer silent.Close()
	defer close(release)
	client := &http.Client{Transport: endingAt{base: http.DefaultTransport, end: time.Now().Add(100 * time.Millisecond)}}
	response, err := client.Get(silent.URL)
	if err == nil {
		response.Body.Close()
		t.Fatalf("a call to an issuer that never answers answered %s, want the transport's end", response.Status)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a call to an issuer that never answers: err = %v, want context.DeadlineExceeded", err)
	}

	answering := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "the whole answer")
	}))
	defer answering.Close()
	client = &http.Client{Transport: endingAt{base: http.DefaultTransport, end: time.Now().Add(time.Hour)}}
	response, err = client.Get(answering.URL)
	if err != nil {
		t.Fatalf("a call to an issuer that answers: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	if closeErr := response.Body.Close(); err != nil || closeErr != nil || string(body) != "the whole answer" {
		t.Fatalf("the answer read %q (read error %v, close error %v), want the whole answer", body, err, closeErr)
	}
}
