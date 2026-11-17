package api

import (
	"context"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// A document's node is addressed by its ref_key, which a later document of the same issue and the
// same slug takes again. The removal of a document states its node gone, in the removal's own
// transaction and caused by its artifact.removed event, so no read takes a removed document for a
// live one, and the document that later takes the address finds the node gone: its create moves
// it back to live and states that under its own artifact.created event, rather than finding a
// live node that states nothing and keeps the removed document's cause.
//
// Its control is the removal's node write taken out: the node then stays live after the removal,
// and the new document's node keeps the removed one's event.
func TestARemovedDocumentsNodeIsGoneAndItsAddressIsTakenAgain(t *testing.T) {
	handler, database := newTestHandlerWithStore(t)
	issue := createInteractionIssue(t, handler, "RMV", "A document removed and made again", "The spec.\n")

	first := emptyDocument(t, handler, issue.Key, "Scratch.md")
	created := artifactEvent(t, database, "artifact.created", first.ID)
	requireDocumentNode(t, database, first.RefKey, graphstore.StateLive, created)

	removedDocument(t, handler, first.ID)
	removed := artifactEvent(t, database, "artifact.removed", first.ID)
	requireDocumentNode(t, database, first.RefKey, graphstore.StateGone, removed)

	second := emptyDocument(t, handler, issue.Key, "Scratch.md")
	if second.RefKey != first.RefKey {
		t.Fatalf("the second document took the address %q, want the removed one's %q, so this test proves nothing", second.RefKey, first.RefKey)
	}
	remade := artifactEvent(t, database, "artifact.created", second.ID)
	requireDocumentNode(t, database, second.RefKey, graphstore.StateLive, remade)

	history, err := graphstore.History(context.Background(), database.Pool, graphstore.KindArtifact, first.RefKey, graphstore.AttributeState)
	if err != nil {
		t.Fatalf("read the state history of %s: %v", first.RefKey, err)
	}
	var stated []string
	for _, tuple := range history {
		stated = append(stated, tuple.ToID)
		if tuple.SourceEvent != created && tuple.SourceEvent != removed && tuple.SourceEvent != remade {
			t.Errorf("a state tuple of %s names event %d, which is none of the three writes", first.RefKey, tuple.SourceEvent)
		}
	}
	if want := []string{graphstore.StateLive, graphstore.StateGone, graphstore.StateLive}; !equalStrings(stated, want) {
		t.Errorf("the node of %s states %v over its life, want %v", first.RefKey, stated, want)
	}
}

// requireDocumentNode fails unless the document node at refKey is in state, caused by event, and
// states that state as its newest state tuple under the same event.
func requireDocumentNode(t *testing.T, database *store.Store, refKey, state string, event int64) {
	t.Helper()
	ctx := context.Background()
	node, held, err := graphstore.ReadNode(ctx, database.Pool, graphstore.KindArtifact, refKey)
	if err != nil || !held {
		t.Fatalf("the store holds no node of %s (held %t, %v)", refKey, held, err)
	}
	if node.State != state || node.SourceEvent != event {
		t.Errorf("the node of %s is %s and caused by event %d, want %s and event %d", refKey, node.State, node.SourceEvent, state, event)
	}
	history, err := graphstore.History(ctx, database.Pool, graphstore.KindArtifact, refKey, graphstore.AttributeState)
	if err != nil || len(history) == 0 {
		t.Fatalf("read the state history of %s: %d tuple(s), %v", refKey, len(history), err)
	}
	if newest := history[len(history)-1]; newest.ToID != state || newest.SourceEvent != event {
		t.Errorf("the newest state tuple of %s states %s under event %d, want %s under event %d", refKey, newest.ToID, newest.SourceEvent, state, event)
	}
}

// artifactEvent is the id of the newest event of eventType whose payload carries the artifact id.
func artifactEvent(t *testing.T, database *store.Store, eventType, artifactID string) int64 {
	t.Helper()
	var id int64
	if err := database.Pool.QueryRow(context.Background(), `
		select id from events where type = $1 and payload->'artifact'->>'id' = $2 order by id desc limit 1
	`, eventType, artifactID).Scan(&id); err != nil {
		t.Fatalf("read the %s event of %s: %v", eventType, artifactID, err)
	}
	return id
}
