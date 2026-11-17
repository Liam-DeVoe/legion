package graphcensus

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// Two releases serve one database during a roll, and after a rollback. A document this build
// removed has its node stated gone, and a build before this one that then creates a document of
// the same issue and slug writes its row and leaves the node gone, since it writes no node. A
// row whose node is gone is a row a read limited to its project would answer as removed, so the
// project census names it (gone), as the store census names a row naming a gone node for a
// comment and a block, and the backfill gives it a live node again: a live node is the backfill's
// marker for these kinds, so the row is one the marker does not mark, and the backfill writes its
// node from the row's own content, dated at its created_at and made by its author.
//
// Its controls, each on its own: the gone class taken out of the census, and the backfill's
// marker read as any node rather than a live one. With the first the census reads the row as
// complete; with the second the backfill leaves the node gone.
func TestARowWhoseNodeIsGoneIsNamedAndTheBackfillMakesItLive(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	created := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	alice := model.Actor{Kind: "user", ID: "alice"}
	seedGoneRow(t, database, created, alice)

	census, err := ProjectCensus(ctx, database.Pool)
	if err != nil {
		t.Fatalf("take the project census: %v", err)
	}
	for _, kind := range census {
		want := int64(0)
		if kind.Kind == graphstore.KindArtifact {
			want = 1
		}
		if kind.Gone != want || kind.Findings() != want {
			t.Errorf("the census reads %+v for %s, want gone=%d and that many findings", kind, kind.Kind, want)
		}
	}
	incomplete, err := ProjectComplete(ctx, database.Pool)
	if err != nil {
		t.Fatalf("read the incomplete rows: %v", err)
	}
	if want := []string{"artifact GONE-1/notes-md: gone"}; !slices.Equal(incomplete, want) {
		t.Errorf("ProjectComplete answers %v, want %v", incomplete, want)
	}

	report, err := Backfill(ctx, database, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("run the backfill: %v", err)
	}
	// The node was there, gone: moving it live states its state and nothing else.
	if done := reportedKind(t, report, graphstore.KindArtifact); done.Nodes != 1 || done.Tuples != 1 {
		t.Errorf("the backfill reports %+v for documents, want one node and the one state tuple it appended", done)
	}
	requireComplete(t, ctx, database, "after the backfill")
	node, held, err := graphstore.ReadNode(ctx, database.Pool, graphstore.KindArtifact, "GONE-1/notes-md")
	if err != nil || !held || node.State != graphstore.StateLive {
		t.Fatalf("after the backfill the document's node is %+v (held %t, %v), want live", node, held, err)
	}
	history, err := graphstore.History(ctx, database.Pool, graphstore.KindArtifact, "GONE-1/notes-md", graphstore.AttributeState)
	if err != nil || len(history) == 0 {
		t.Fatalf("read the state history: %d tuple(s), %v", len(history), err)
	}
	newest := history[len(history)-1]
	if newest.ToID != graphstore.StateLive || newest.SourceEvent != graphstore.NoEvent || !newest.At.Equal(created) || newest.Who.ID != alice.ID {
		t.Errorf("the newest state tuple is %+v, want live at the sentinel, dated at the row's created_at %s and by its author", newest, created)
	}
}

// The backfill reads a batch of rows and then writes their nodes. A removal that commits between
// the two leaves the row gone, and a node written live then would stand behind no row. The batch
// takes the owner rows a removal takes, reads again under them which of its rows stand, and writes
// the node of those alone, so it leaves no live node behind a row that is gone.
//
// Its control is that second read taken out: the node then stands live behind no row, and the
// census reads it as orphaned.
func TestTheObjectBackfillWritesNoNodeForARowRemovedWhileItRead(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	if _, err := database.Pool.Exec(ctx, `
		insert into projects (key, name, default_assignee) values ('RACE', 'Race', 'alice');
		insert into issues (key, project_key, number, title, created_by, rank)
		values ('RACE-1', 'RACE', 1, 'One', '{"kind":"user","id":"alice"}', 'U');
		insert into artifacts (issue_key, project_key, slug, name, kind, created_by)
		values ('RACE-1', 'RACE', 'notes-md', 'Notes', 'doc', '{"kind":"user","id":"alice"}');
	`); err != nil {
		t.Fatalf("seed a document with no node: %v", err)
	}
	kind := objectKindOf(t, graphstore.KindArtifact)
	nodes, err := readObjectNodes(ctx, database, kind.kind, kind.read, kind.start, backfillBatch)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("read the document naming no node: %d node(s), %v; want 1", len(nodes), err)
	}
	// A build before this one removes the document between the read and the write: it deletes
	// the row and writes no node.
	if _, err := database.Pool.Exec(ctx, `delete from artifacts where ref_key = 'RACE-1/notes-md'`); err != nil {
		t.Fatalf("remove the document: %v", err)
	}
	if _, err := writeObjectBatch(ctx, database, kind, nodes); err != nil {
		t.Fatalf("write the batch: %v", err)
	}
	if node, held, err := graphstore.ReadNode(ctx, database.Pool, graphstore.KindArtifact, "RACE-1/notes-md"); err != nil || (held && node.State == graphstore.StateLive) {
		t.Errorf("after the batch the removed document's node is %+v (held %t, %v), want gone or none", node, held, err)
	}
	census, err := ProjectCensus(ctx, database.Pool)
	if err != nil {
		t.Fatalf("take the project census: %v", err)
	}
	for _, report := range census {
		if report.Orphaned != 0 {
			t.Errorf("the census reads %+v for %s, want no orphaned node", report, report.Kind)
		}
	}
}

// A build before this one can remove a document and create another at the same issue and slug,
// which takes the same address, between the batch's read and its write. The node the batch built
// from the first row would then be written for the second, made by the first row's author at its
// created_at, and the node keeps that over its life. The batch writes a node only for the row it
// read, told by its created_at, and leaves the second for the backfill's next pass, which makes
// its node from its own row.
//
// Its control is the batch's created_at test taken out: the second row's node is then made by
// bob, the first row's author, at the first row's created_at.
func TestTheObjectBackfillWritesNoNodeForARowReplacedWhileItRead(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	first := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	if _, err := database.Pool.Exec(ctx, `
		insert into projects (key, name, default_assignee) values ('REPL', 'Replaced', 'alice');
		insert into issues (key, project_key, number, title, created_by, rank)
		values ('REPL-1', 'REPL', 1, 'One', '{"kind":"user","id":"alice"}', 'U');
	`); err != nil {
		t.Fatalf("seed the project and the issue: %v", err)
	}
	insertDocument := func(author string, at time.Time) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, `
			insert into artifacts (issue_key, project_key, slug, name, kind, created_by, created_at)
			values ('REPL-1', 'REPL', 'notes-md', 'Notes', 'doc', jsonb_build_object('kind', 'user', 'id', $1::text), $2)
		`, author, at); err != nil {
			t.Fatalf("seed %s's document: %v", author, err)
		}
	}
	insertDocument("bob", first)
	kind := objectKindOf(t, graphstore.KindArtifact)
	nodes, err := readObjectNodes(ctx, database, kind.kind, kind.read, kind.start, backfillBatch)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("read the document naming no node: %d node(s), %v; want 1", len(nodes), err)
	}
	// A build before this one removes bob's document and carol makes another at its address.
	if _, err := database.Pool.Exec(ctx, `delete from artifacts where ref_key = 'REPL-1/notes-md'`); err != nil {
		t.Fatalf("remove bob's document: %v", err)
	}
	insertDocument("carol", second)
	written, err := writeObjectBatch(ctx, database, kind, nodes)
	if err != nil {
		t.Fatalf("write the batch: %v", err)
	}
	if written.Nodes != 0 {
		t.Errorf("the batch wrote %+v, want no node for a row it did not read", written)
	}
	if _, err := Backfill(ctx, database, &bytes.Buffer{}); err != nil {
		t.Fatalf("run the backfill: %v", err)
	}
	requireComplete(t, ctx, database, "after the backfill")
	node, held, err := graphstore.ReadNode(ctx, database.Pool, graphstore.KindArtifact, "REPL-1/notes-md")
	if err != nil || !held {
		t.Fatalf("read the node of REPL-1/notes-md: held %t, %v", held, err)
	}
	if node.MadeBy.ID != "carol" || !node.MadeAt.Equal(second) {
		t.Errorf("the node of REPL-1/notes-md is made by %s at %s, want carol at her row's created_at %s", node.MadeBy.ID, node.MadeAt, second)
	}
}

// seedGoneRow writes project GONE, issue GONE-1 with its node, and a document GONE-1/notes-md
// whose row a build before this one wrote at created, by author, after this build removed an
// earlier document of that address and stated its node gone.
func seedGoneRow(t *testing.T, database *store.Store, created time.Time, author model.Actor) {
	t.Helper()
	ctx := context.Background()
	earlier := created.Add(-time.Hour)
	gone := graphstore.ArtifactNode("GONE-1/notes-md", "GONE", "", model.Actor{Kind: "user", ID: "bob"}, earlier)
	gone.State = graphstore.StateGone
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		return graphstore.Write(ctx, tx, []graphstore.Node{graphstore.IssueNode("GONE-1", "GONE", author, earlier), gone}, nil)
	}); err != nil {
		t.Fatalf("write the issue's node and the removed document's: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into projects (key, name, default_assignee) values ('GONE', 'Gone', 'alice');
		insert into issues (key, project_key, number, title, created_by, rank)
		values ('GONE-1', 'GONE', 1, 'One', '{"kind":"user","id":"alice"}', 'U');
	`); err != nil {
		t.Fatalf("seed the project and the issue: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, created_by, created_at)
		values ('GONE-1', 'GONE', 'notes-md', 'Notes', 'doc', '{"kind":"user","id":"alice"}', $1)
	`, created); err != nil {
		t.Fatalf("seed the document a build before this one wrote: %v", err)
	}
}

// objectKindOf is the backfill's object kind named kind.
func objectKindOf(t *testing.T, kind string) objectKind {
	t.Helper()
	for _, held := range objectKinds {
		if held.kind == kind {
			return held
		}
	}
	t.Fatalf("the backfill has no object kind %s", kind)
	return objectKind{}
}
