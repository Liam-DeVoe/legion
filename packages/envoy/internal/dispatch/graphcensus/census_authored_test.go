package graphcensus

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// requireRowDifference fails unless the census reads, of kind name, a row difference at a row
// whose label starts with at (a kind held triple by triple writes the triple after its label),
// and none of any other kind.
func requireRowDifference(t *testing.T, what string, report *StoreReport, name, at string) {
	t.Helper()
	for _, kind := range report.Kinds {
		found := slices.ContainsFunc(kind.ReportedRows, func(difference Difference) bool { return strings.HasPrefix(difference.At, at) })
		switch {
		case kind.Kind == name && !found:
			t.Errorf("%s reads the row differences %+v of kind %s, want one at %q", what, kind.ReportedRows, name, at)
		case kind.Kind != name && kind.RowDifferences+kind.NodeDifferences != 0:
			t.Errorf("%s reads %+v and %+v of kind %s, want no difference there", what, kind.ReportedRows, kind.ReportedNodes, kind.Kind)
		}
	}
}

// The store census holds an authored node's place and its authors by the rules the block index
// writes them by (T7 of dispatch://INFRA-1253's plan). Beside a store it agrees with, a document
// of the issue holds a criterion whose parent is a person's comment and a requirement under the
// issue, first written in a version bob wrote, who wrote nothing else. The census agrees: each
// row's null parent_block_id stands for a part_of to no block of its artifact (the comment, the
// issue), and each node's authored_by names bob, whose node the store holds. A parent_block_id
// naming a block of the artifact under the criterion is a difference of the block kind. The
// requirement with its authored_by taken off, as a build before this one left a node, is a
// difference of the block_author kind while its document owes nothing, none while the drain has
// it owed, and none once the pass that drains it has run, which gives it its authors.
func TestTheCensusHoldsAnAuthoredNodesPlaceAndItsAuthors(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	seedAgreeingStore(t, database)
	var comment, checks, refKey string
	if err := database.Pool.QueryRow(ctx, `
		insert into comments (issue_key, author, body)
		values ('CORE-1', '{"kind":"user","id":"alice"}', 'I want the urgent asks first.')
		returning id::text
	`).Scan(&comment); err != nil {
		t.Fatalf("write alice's comment: %v", err)
	}
	if err := database.Pool.QueryRow(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
		values ('CORE-1', 'CORE', 'checks-md', 'checks.md', 'doc', false, '{"kind":"user","id":"bob"}')
		returning id::text, ref_key
	`).Scan(&checks, &refKey); err != nil {
		t.Fatalf("create checks.md: %v", err)
	}
	const criterion, requirement = "I want the urgent asks first.", "Urgent asks come first."
	markdown := criterion + "\n\n" + requirement
	blocks := []model.ArtifactBlock{
		{ID: "k1", Type: "criterion", From: 0, To: len(criterion)},
		{ID: "r1", Type: "requirement", From: len(criterion) + 2, To: len(markdown)},
	}
	var at time.Time
	if err := database.Pool.QueryRow(ctx, `
		insert into artifact_versions (artifact_id, number, markdown, authors)
		values ($1::uuid, 1, $2, '[{"kind":"user","id":"bob"}]') returning created_at
	`, checks, markdown).Scan(&at); err != nil {
		t.Fatalf("write version 1 of checks.md: %v", err)
	}
	// The comment, the document and its version take their nodes as every pre-store row does.
	if _, err := Backfill(ctx, database, io.Discard); err != nil {
		t.Fatalf("backfill the comment and checks.md: %v", err)
	}
	pointers := []refs.Pointer{{Block: "k1", BlockType: "criterion", Kind: pmdoc.ParentAttribute,
		Target: text.Ref{Kind: "comment", IssueKey: "CORE-1", ID: comment}}}
	version := 1
	index := func(what string) {
		t.Helper()
		tx, err := database.Pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin %s: %v", what, err)
		}
		defer tx.Rollback(ctx)
		_, won, write, err := refs.IndexBlocks(ctx, tx, checks, refs.OneVersion(checks, version, at), markdown, blocks, nil, pointers, refs.TypedFacts{})
		if err != nil || !won {
			t.Fatalf("%s: won %t, %v", what, won, err)
		}
		if err := graphstore.Caused(ctx, tx, write, graphstore.NoEvent); err != nil {
			t.Fatalf("append what %s stated: %v", what, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit %s: %v", what, err)
		}
	}
	index("the index of checks.md")
	for _, placed := range []struct{ block, kind, id string }{{"k1", graphstore.KindComment, comment}, {"r1", graphstore.KindIssue, "CORE-1"}} {
		held, err := graphstore.CurrentOut(ctx, database.Pool, refs.BlockNodeKind, refKey+"#"+placed.block, []string{graphstore.TuplePartOf})
		if err != nil || len(held) != 1 || held[0].ToKind != placed.kind || held[0].ToID != placed.id {
			t.Fatalf("%s stands under %+v (%v), want %s:%s", placed.block, held, err, placed.kind, placed.id)
		}
	}
	requireTheCensusAgrees(t, "the census beside an authored node under a comment and one under the issue", storeCensusOf(t, database))

	setParent := func(parent *string) {
		t.Helper()
		if tag, err := database.Pool.Exec(ctx, `
			update document_blocks set parent_block_id = $2 where artifact_id = $1::uuid and block_id = 'k1'
		`, checks, parent); err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("set k1's parent_block_id: %d rows, %v", tag.RowsAffected(), err)
		}
	}
	wrong := "r1"
	setParent(&wrong)
	requireRowDifference(t, "the census over k1's row naming r1", storeCensusOf(t, database), "block", "block "+refKey+"#k1")
	setParent(nil)

	if tag, err := database.Pool.Exec(ctx, `
		delete from graph_tuples where from_kind = $1 and from_id = $2 and kind = $3
	`, refs.BlockNodeKind, refKey+"#r1", graphstore.TupleAuthoredBy); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("take r1's authored_by off: %d rows, %v", tag.RowsAffected(), err)
	}
	requireRowDifference(t, "the census over r1 with no authored_by", storeCensusOf(t, database),
		"block_author", "block_author "+refKey+"#r1 "+graphstore.KindPerson+":bob")
	if _, err := database.Pool.Exec(ctx, `update document_block_versions set store_owed_at = now() where artifact_id = $1::uuid`, checks); err != nil {
		t.Fatalf("mark checks.md owed, as the drain marks it: %v", err)
	}
	if owed := censusKind(t, storeCensusOf(t, database), "block_author"); owed.RowDifferences+owed.NodeDifferences != 0 {
		t.Errorf("the census of a document the drain owes reads %+v and %+v of kind block_author, want none", owed.ReportedRows, owed.ReportedNodes)
	}
	index("the pass that drains checks.md")
	requireTheCensusAgrees(t, "the census after the pass that drains checks.md", storeCensusOf(t, database))

	// r1 retyped to a plain paragraph, in a version carol writes: it records its authors no
	// longer, keeps the authored_by bob's first version gave it, which still says who wrote it,
	// and hangs under its document's top as its text places it now.
	const prose = "Urgent asks come first, written as prose."
	markdown = criterion + "\n\n" + prose
	blocks = []model.ArtifactBlock{
		{ID: "k1", Type: "criterion", From: 0, To: len(criterion)},
		{ID: "r1", Type: "paragraph", From: len(criterion) + 2, To: len(markdown)},
	}
	version = 2
	if err := database.Pool.QueryRow(ctx, `
		insert into artifact_versions (artifact_id, number, markdown, authors)
		values ($1::uuid, 2, $2, '[{"kind":"user","id":"carol"}]') returning created_at
	`, checks, markdown).Scan(&at); err != nil {
		t.Fatalf("write version 2 of checks.md: %v", err)
	}
	if _, err := Backfill(ctx, database, io.Discard); err != nil {
		t.Fatalf("backfill version 2 of checks.md: %v", err)
	}
	index("the index of the version retyping r1")
	if held, err := graphstore.CurrentOut(ctx, database.Pool, refs.BlockNodeKind, refKey+"#r1", []string{graphstore.TupleAuthoredBy}); err != nil ||
		len(held) != 1 || held[0].ToKind != graphstore.KindPerson || held[0].ToID != "bob" {
		t.Fatalf("the retyped r1 states the authored_by %+v (%v), want bob's alone", held, err)
	}
	requireTheCensusAgrees(t, "the census after r1 is retyped to a paragraph", storeCensusOf(t, database))
}
