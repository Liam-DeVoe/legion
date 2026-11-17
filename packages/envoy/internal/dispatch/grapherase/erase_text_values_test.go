package grapherase

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// An erase takes the erased block's words out of the text value of every block that holds it
// (R5 of dispatch://INFRA-1149; D9 of dispatch://INFRA-1253's plan: a block's text value is its
// whole slice of its version, the blocks inside it included). A document of a block outer holding
// a block inner is indexed through the block index (refs.IndexBlocks), which states each block's
// text value, and inner is erased as an operator erases it. Before the erase the listing names the
// outer block's text value tuple as one the run rewrites, and the outer value holds the words;
// after it no value tuple of the store holds them, the outer block's text value holds the erase's
// marker where they stood and still its own words, its document_block_texts row agrees with it,
// and the store census reads zero.
func TestTheEraseTakesABlocksWordsOutOfTheTextValueOfEveryBlockHoldingIt(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	const (
		secret   = "The secret words."
		markdown = "Outer words.\n" + secret + "\n"
	)
	document := seedIssueDocument(t, database, "CORE", coreDocumentID, markdown)
	// The issue, the document and its version are written by SQL; the backfill gives each its
	// node, as it does for rows a build before the store wrote, so the census over the store
	// after the erase reads what the erase did and nothing the fixture left.
	if _, err := graphcensus.Backfill(ctx, database, io.Discard); err != nil {
		t.Fatalf("backfill the fixture's nodes: %v", err)
	}
	at := len("Outer words.\n")
	blocks := []model.ArtifactBlock{
		{ID: "outer", Type: "paragraph", From: 0, To: len(markdown)},
		{ID: "inner", Type: "paragraph", From: at, To: at + len(secret)},
	}
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		var versionAt time.Time
		if err := tx.QueryRow(ctx, `select created_at from artifact_versions where artifact_id = $1::uuid and number = 1`,
			document.artifactID).Scan(&versionAt); err != nil {
			return err
		}
		_, won, write, err := refs.IndexBlocks(ctx, tx, document.artifactID, 1, versionAt, markdown, blocks, nil, nil, refs.TypedFacts{})
		if err != nil {
			return err
		}
		if !won {
			t.Fatalf("the index of version 1 did not stand")
		}
		return graphstore.Caused(ctx, tx, write, graphstore.NoEvent)
	}); err != nil {
		t.Fatalf("index the document through the block index: %v", err)
	}
	outer, inner := document.refKey+"#outer", document.refKey+"#inner"
	holding := func() []string {
		t.Helper()
		rows, err := database.Pool.Query(ctx, `
			select from_kind || ':' || from_id || ' ' || kind from graph_tuples
			where to_kind = $1 and position($2 in to_id) > 0 order by seq`, graphstore.KindValue, secret)
		if err != nil {
			t.Fatalf("read the value tuples holding the words: %v", err)
		}
		held, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("read the value tuples holding the words: %v", err)
		}
		return held
	}
	if held := holding(); !strings.Contains(strings.Join(held, "\n"), "block:"+outer+" text") {
		t.Fatalf("before the erase the value tuples holding the words are %v, want the outer block's text among them: the case proves nothing", held)
	}

	listing := dryRun(t, database, refs.BlockNodeKind, inner)
	if !listed(listing, "redact graph_tuples", "block:"+outer+" text") {
		t.Errorf("the erase's listing names no rewrite of the outer block's text value:\n%s", strings.Join(listing.Listing, "\n"))
	}
	for _, line := range listing.Listing {
		if strings.HasPrefix(line, "leave ") && strings.Contains(line, "graph_tuples") {
			t.Errorf("the erase's listing leaves a tuple holding the words, which derives from the erased block: %s", line)
		}
	}
	requireErased(t, database, refs.BlockNodeKind, inner)

	if held := holding(); len(held) != 0 {
		t.Errorf("after the erase the value tuples %v still hold the erased words", held)
	}
	text, found, err := graphstore.CurrentValue(ctx, database.Pool, refs.BlockNodeKind, outer, graphstore.AttributeText)
	if err != nil || !found {
		t.Fatalf("read the outer block's text value after the erase: found %t, %v", found, err)
	}
	var row string
	if err := database.Pool.QueryRow(ctx, `
		select text from document_block_texts where artifact_id = $1::uuid and block_id = 'outer' order by version desc limit 1
	`, document.artifactID).Scan(&row); err != nil {
		t.Fatalf("read the outer block's document_block_texts row: %v", err)
	}
	if !strings.HasPrefix(text, "Outer words.\n") || strings.Contains(text, secret) || text != row {
		t.Errorf("after the erase the outer block's text value is %q and its document_block_texts row %q, want one text, its own words kept and the erased ones gone",
			text, row)
	}
	requireCensusZero(t, database, "after the erase of a block another holds")
}
