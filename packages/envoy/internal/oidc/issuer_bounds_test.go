package oidc

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A read of the discovery document runs under discoveryAttemptTimeout, or under the bound a test
// set in its place (attemptTimeout). The read's own deadline is read against the clock on both
// sides of the call, so the row holds on any machine.
func TestADiscoveryReadRunsUnderItsAttemptBound(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  time.Duration
		want time.Duration
	}{
		{"the product's bound", 0, discoveryAttemptTimeout},
		{"a bound a test set", time.Hour, time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var deadline time.Time
			var bounded bool
			d := newDiscovery("https://issuer.example", func(ctx context.Context) (int, error) {
				deadline, bounded = ctx.Deadline()
				return 0, errors.New("the read is not answered here")
			})
			d.attemptTimeout = tc.set
			before := time.Now()
			if _, err := d.discover(context.Background()); !errors.Is(err, ErrIssuerUnavailable) {
				t.Fatalf("discover: err = %v, want ErrIssuerUnavailable", err)
			}
			after := time.Now()
			if !bounded || deadline.Before(before.Add(tc.want)) || deadline.After(after.Add(tc.want)) {
				t.Fatalf("the read's deadline is %v (bounded %t), want %s from the call, between %v and %v", deadline, bounded, tc.want, before.Add(tc.want), after.Add(tc.want))
			}
		})
	}
}

// A code flow waits for its issuer under the product's bounds, discoveryAttemptTimeout and
// TokenTimeout, until a test moves both to what is left of its own deadline (untilTheTestEnds).
func TestACodeFlowWaitsForItsIssuerUnderTheProductsBoundsUntilATestMovesThem(t *testing.T) {
	flow := NewCodeFlow("https://issuer.example", flowClientID, flowClientSecret, "")
	if got := flow.discovery.attemptBound(); got != discoveryAttemptTimeout {
		t.Fatalf("a new flow's discovery read bound is %s, want discoveryAttemptTimeout (%s)", got, discoveryAttemptTimeout)
	}
	if got := flow.http.Timeout; got != TokenTimeout {
		t.Fatalf("a new flow's token call bound is %s, want TokenTimeout (%s)", got, TokenTimeout)
	}
	untilTheTestEnds(t, flow)
	if read, call := flow.discovery.attemptBound(), flow.http.Timeout; read != call || read == discoveryAttemptTimeout {
		t.Fatalf("after untilTheTestEnds the discovery read bound is %s and the token call bound %s, want both what is left of the test", read, call)
	}
}
