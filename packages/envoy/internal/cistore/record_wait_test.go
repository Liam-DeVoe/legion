package cistore

import (
	"context"
	"testing"
	"time"
)

// A store's Record and RecordSuite give up at recordWait from their start, which bounds a webhook
// delivery, unless a test armed another wait in its place (openStore arms the test's own deadline).
// The deadline is read against the clock on both sides of the call, so the row holds on any machine.
func TestARecordWaitsRecordWaitUnlessATestArmedAnother(t *testing.T) {
	before := time.Now()
	ctx, cancel := (&Store{}).recordWaitContext(context.Background())
	after := time.Now()
	defer cancel()
	deadline, bounded := ctx.Deadline()
	if !bounded || deadline.Before(before.Add(recordWait)) || deadline.After(after.Add(recordWait)) {
		t.Fatalf("a store no test armed: deadline %v (bounded %t), want recordWait (%s) from the call, between %v and %v", deadline, bounded, recordWait, before.Add(recordWait), after.Add(recordWait))
	}

	armed := time.Now().Add(time.Hour)
	ctx, cancel = (&Store{armRecordWait: func(ctx context.Context) (context.Context, context.CancelFunc) {
		return context.WithDeadline(ctx, armed)
	}}).recordWaitContext(context.Background())
	defer cancel()
	if deadline, bounded := ctx.Deadline(); !bounded || !deadline.Equal(armed) {
		t.Fatalf("a store a test armed: deadline %v (bounded %t), want the armed %v", deadline, bounded, armed)
	}
}

// A wait of a store's test ends recordWaitMargin before the binary's deadline while more than twice
// that is left, and at the deadline itself with less: it never has less time than the 4 s, 5 s and
// 10 s bounds it replaces gave it, under any -timeout.
func TestAWaitOfAStoresTestEndsItsMarginBeforeTheDeadlineWhileTwiceItIsLeft(t *testing.T) {
	now := time.Date(2026, 11, 22, 16, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		left, want time.Duration
	}{
		{10 * time.Minute, 10*time.Minute - recordWaitMargin},
		{2*recordWaitMargin + time.Second, recordWaitMargin + time.Second},
		{2 * recordWaitMargin, 2 * recordWaitMargin},
		{20 * time.Second, 20 * time.Second},
		{3 * time.Second, 3 * time.Second},
	} {
		if got := endBefore(now.Add(tc.left), now).Sub(now); got != tc.want {
			t.Errorf("with %s left the wait ends %s from now, want %s", tc.left, got, tc.want)
		}
	}
}
