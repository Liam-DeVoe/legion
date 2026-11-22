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
