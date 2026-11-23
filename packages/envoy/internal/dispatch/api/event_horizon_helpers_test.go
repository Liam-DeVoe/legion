package api

import (
	"context"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// appendOutOfOrder appends two events through broker so they commit out of id order: tx A
// appends on issue first (the lower id, low) and stays open, then tx B appends on issue second
// (high) and commits. commitLow commits A; until it runs, low is an id below a committed one that
// is not yet visible. A that the test never commits rolls back when the test ends.
func appendOutOfOrder(t *testing.T, database *store.Store, broker *events.Broker, first, second string) (low, high int64, commitLow func()) {
	t.Helper()
	ctx := t.Context()
	open, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the lower id's transaction: %v", err)
	}
	t.Cleanup(func() { _ = open.Rollback(context.Background()) })
	lowEvent, err := broker.Append(ctx, open, model.Event{
		IssueKey: new(first),
		Type:     "test.concurrent",
		Actor:    model.Actor{Kind: "session", ID: "seed"},
		Payload:  map[string]any{},
	})
	if err != nil {
		t.Fatalf("append the lower id: %v", err)
	}
	committed, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the higher id's transaction: %v", err)
	}
	defer func() { _ = committed.Rollback(context.Background()) }()
	highEvent, err := broker.Append(ctx, committed, model.Event{
		IssueKey: new(second),
		Type:     "test.concurrent",
		Actor:    model.Actor{Kind: "session", ID: "seed"},
		Payload:  map[string]any{},
	})
	if err != nil {
		t.Fatalf("append the higher id while the lower one is open: %v", err)
	}
	if err := committed.Commit(ctx); err != nil {
		t.Fatalf("commit the higher id: %v", err)
	}
	if highEvent.ID <= lowEvent.ID {
		t.Fatalf("event ids %d (open) and %d (committed) are not out of commit order, so this test proves nothing", lowEvent.ID, highEvent.ID)
	}
	return lowEvent.ID, highEvent.ID, func() {
		t.Helper()
		if err := open.Commit(ctx); err != nil {
			t.Fatalf("commit the lower id: %v", err)
		}
	}
}
