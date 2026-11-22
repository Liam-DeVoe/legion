package grapherase

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/explaintest"
)

// R5: a related-item check of a decision block records the lexemes its keyword search picked from
// the block's question (related_checks.terms), which derive from the block. The inner block of
// seedAgreeingStore reads one word, which a lexeme spells as written, and a check of it as a
// decision block of issue CORE-1 holds that word in its terms; a check of the outer block, another
// block of the same issue, holds the word too, beside words of its own. The listing names the
// inner block's check as a row the run rewrites and never as one it leaves, and the outer block's
// as a row it leaves holding the word. After the run the inner block's check stands with no terms,
// the outer block's reads as it did, the erase counts the one row it rewrote, and the store census
// reads zero.
func TestTheBlockEraseClearsTheTermsOfItsDecisionChecks(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seeded := seedAgreeingStore(t, database)
	const word = "quokka7lighthouse"
	if tag, err := database.Pool.Exec(ctx, `update document_blocks set excerpt = $2 where artifact_id = $1::uuid and block_id = 'inner'`,
		seeded.document.artifactID, word); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("write the inner block's text: %d rows, %v", tag.RowsAffected(), err)
	}
	erased := seedRelatedCheck(t, database, "CORE-1", "inner", word)
	kept := seedRelatedCheck(t, database, "CORE-1", "outer", "Block outer repeats "+word)
	requireErasedChecksEmptied(t, database, seeded.inner, word, "decision_block", erased, kept)
}

// R5: an issue's own related-item check, made when the issue was created, records the lexemes its
// keyword search picked from the issue's title and spec together (related_checks.terms), so none of
// them can be told to come from one block of the spec, and the erase of a block of the spec takes
// them all. The document of seedAgreeingStore is made issue CORE-1's spec, its inner block reads
// one word, which a lexeme spells as written, and CORE-1's own check holds that word in its terms;
// the own check of issue CORE-2 holds the word too, beside words of its own. The listing names
// CORE-1's check as a row the run rewrites and never as one it leaves, and CORE-2's as a row it
// leaves holding the word. After the run CORE-1's check stands with no terms, CORE-2's reads as it
// did, the erase counts the one row it rewrote, and the store census reads zero.
func TestTheSpecBlockEraseClearsTheTermsOfItsIssuesOwnCheck(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seeded := seedAgreeingStore(t, database)
	const word = "quokka7lighthouse"
	for _, seed := range []struct {
		what, sql string
		args      []any
	}{
		{"make the document CORE-1's spec", `update artifacts set is_primary = true where id = $1::uuid`, []any{seeded.document.artifactID}},
		{"write the inner block's text", `update document_blocks set excerpt = $2 where artifact_id = $1::uuid and block_id = 'inner'`,
			[]any{seeded.document.artifactID, word}},
		{"seed issue CORE-2", `insert into issues (key, project_key, number, title, created_by, rank)
			values ('CORE-2', 'CORE', 2, 'Two', '{"kind":"user","id":"alice"}', 'U')`, nil},
	} {
		if tag, err := database.Pool.Exec(ctx, seed.sql, seed.args...); err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("%s: %d rows, %v", seed.what, tag.RowsAffected(), err)
		}
	}
	erased := seedRelatedCheck(t, database, "CORE-1", "", "One\n"+word)
	kept := seedRelatedCheck(t, database, "CORE-2", "", "Two repeats "+word)
	requireErasedChecksEmptied(t, database, seeded.inner, word, "issue", erased, kept)
}

// requireErasedChecksEmptied holds the related-item checks erased and kept to holding word, runs
// the confirmed erase of block node, and holds the run to emptying erased's terms, a check of kind
// it lists as a row it rewrites and never as one it leaves, and to leaving kept's as they were,
// listed as a row it leaves holding the word. The run counts the one row it rewrote, and the store
// census reads zero before and after it.
func requireErasedChecksEmptied(t *testing.T, database *store.Store, node, word, kind, erased, kept string) {
	t.Helper()
	keptTerms := relatedCheckTerms(t, database, kept)
	if !slices.Contains(relatedCheckTerms(t, database, erased), word) || !slices.Contains(keptTerms, word) {
		t.Fatalf("the checks hold the terms %q and %q, want the word in both, so this test would prove nothing",
			relatedCheckTerms(t, database, erased), keptTerms)
	}
	requireCensusZero(t, database, "before the erase")

	erasure, err := confirmedErase(t, database, refs.BlockNodeKind, node, time.Millisecond)
	if err != nil {
		t.Fatalf("erase %s: %v", node, err)
	}
	if !erasure.Applied {
		t.Fatalf("the erase of %s removed nothing: %q", node, erasure.Blockers)
	}
	if !listed(erasure, "rewrite related_checks.terms of "+kind+" check "+erased) {
		t.Errorf("the listing does not name the %s check %s as a row the run rewrites:\n%s",
			kind, erased, strings.Join(erasure.Listing, "\n"))
	}
	if listed(erasure, "leave public.related_checks", erased) {
		t.Errorf("the listing names the %s check %s as a row the run leaves:\n%s",
			kind, erased, strings.Join(erasure.Listing, "\n"))
	}
	if !listed(erasure, "leave public.related_checks (id)=("+kept+") terms") {
		t.Errorf("the listing does not name the check %s as a row the run leaves holding the word:\n%s",
			kept, strings.Join(erasure.Listing, "\n"))
	}
	if got := relatedCheckTerms(t, database, erased); len(got) != 0 {
		t.Errorf("after the erase of %s the %s check %s holds the terms %q, want none", node, kind, erased, got)
	}
	if got := relatedCheckTerms(t, database, kept); !slices.Equal(got, keptTerms) {
		t.Errorf("after the erase of %s the check %s holds the terms %q, want them as they were, %q",
			node, kept, got, keptTerms)
	}
	requireRowsCounted(t, erasure.RedactedRows, "rewritten", "related_checks", 1)
	requireCensusZero(t, database, "after the erase")
}

// seedRelatedCheck writes a related-item check of issue, a decision block check of block blockID
// of a document of the issue or, where blockID is empty, the issue's own check, as
// recordRelatedChecks writes one, its terms the lexemes of text, and answers its id.
func seedRelatedCheck(t *testing.T, database *store.Store, issue, blockID, text string) string {
	t.Helper()
	kind, block := "decision_block", any(blockID)
	if blockID == "" {
		kind, block = "issue", nil
	}
	var id string
	if err := database.Pool.QueryRow(context.Background(), `
		insert into related_checks (project_key, source_kind, source_issue_key, source_block_id, actor_kind, actor_id,
		                            outcome, search, terms, total_ms)
		values ('CORE', $1, $2, $3, 'user', 'alice', 'none', 'keywords_only', tsvector_to_array(to_tsvector('english', $4)), 40)
		returning id::text`, kind, issue, block, text).Scan(&id); err != nil {
		t.Fatalf("write the %s check of %s %s: %v", kind, issue, blockID, err)
	}
	return id
}

// relatedCheckTerms reads the terms of the related-item check id.
func relatedCheckTerms(t *testing.T, database *store.Store, id string) []string {
	t.Helper()
	var terms []string
	if err := database.Pool.QueryRow(context.Background(), `select terms from related_checks where id = $1::uuid`, id).Scan(&terms); err != nil {
		t.Fatalf("read the terms of the check %s: %v", id, err)
	}
	return terms
}

// relatedChecksPlanRows is how many related_checks rows of each other source the plan test seeds -
// the own checks of as many other issues, and decision block checks of other blocks of issue
// CORE-1: enough that a read of the table whole costs the planner more than a probe of an index.
const relatedChecksPlanRows = 2000

// TestTheRelatedChecksReadsProbeTheirSourceIndexes explains the two reads readRelatedChecks sends
// under the erase's locks for a block of an issue's spec: that of the block's decision block
// checks, and that of the issue's own checks. related_checks holds the own checks of
// relatedChecksPlanRows other issues and as many decision block checks of other blocks of issue
// CORE-1, beside one check of the block and one of CORE-1 the reads are for, written and analyzed
// before the reads; the document seedPreStoreDocument writes is made CORE-1's spec. Each read is
// the one readRelatedChecks sends, with the arguments it binds (explaintest.RecordedTx), and the
// two are held to finding the two checks. With hash and merge joins off, under each plan-cache mode
// the plan must read related_checks through related_checks_source_block and
// related_checks_source_issue respectively, by its key (explaintest.RequireEachRowProbedOnce): the
// erase reads no whole large table while it holds its locks (R5). The control is each read with
// its index dropped inside the explaining transaction, as the database stood before
// 20261119020300 and 20261119020400, which must plan, run and fail the check under both modes: it
// reads related_checks whole.
func TestTheRelatedChecksReadsProbeTheirSourceIndexes(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	pool := database.Pool
	document := seedPreStoreDocument(t, database)
	if _, err := pool.Exec(ctx, `update artifacts set is_primary = true where id = $1::uuid`, document.artifactID); err != nil {
		t.Fatalf("make the document CORE-1's spec: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		insert into issues (key, project_key, number, title, created_by, rank)
		select 'CORE-' || (i + 1), 'CORE', i + 1, 'Other', '{"kind":"user","id":"alice"}'::jsonb, 'U'
		from generate_series(1, $1::int) i`,
		relatedChecksPlanRows); err != nil {
		t.Fatalf("seed the other issues: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		insert into related_checks (project_key, source_kind, source_issue_key, source_block_id,
		                            actor_kind, actor_id, outcome, search, terms, total_ms)
		select 'CORE', source.kind, source.issue, source.block, 'user', 'alice', 'none', 'keywords_only', '{ship}', 40
		from (select 'issue', 'CORE-' || (i + 1), null::text from generate_series(1, $1::int) i
		      union all
		      select 'decision_block', 'CORE-1', 'b' || i from generate_series(1, $1::int) i
		      union all
		      select 'issue', 'CORE-1', null::text
		      union all
		      select 'decision_block', 'CORE-1', 'erased') as source (kind, issue, block)`,
		relatedChecksPlanRows); err != nil {
		t.Fatalf("seed the related-item checks: %v", err)
	}
	if _, err := pool.Exec(ctx, `analyze issues, related_checks`); err != nil {
		t.Fatalf("analyze the seeded tables: %v", err)
	}
	keys := explaintest.NewIndexKeys(pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the reads: %v", err)
	}
	defer tx.Rollback(ctx)
	recorded := &explaintest.RecordedTx{Tx: tx}
	block := &Erasure{Kind: refs.BlockNodeKind, ID: document.refKey + "#erased"}
	var read erasedRead
	if read.document, read.blockID, err = block.ownDocument(ctx, recorded); err != nil || read.document.specIssue() != "CORE-1" {
		t.Fatalf("read the document of %s: %+v, %v; want CORE-1's spec", block.ID, read.document, err)
	}
	if err := block.readRelatedChecks(ctx, recorded, &read); err != nil {
		t.Fatalf("read the checks of %s: %v", block.ID, err)
	}
	if len(read.relatedChecks) != 2 {
		t.Fatalf("the reads found the checks %q of %s, want the block's and CORE-1's own, so the plans below would be of reads that find nothing",
			read.relatedChecks, block.ID)
	}
	// The reads' transaction holds a lock on related_checks until it ends, which the control's drop
	// index would wait behind for ever; the statements they sent are all the test needs of it.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("end the reads: %v", err)
	}
	for _, source := range []struct{ name, marker, index string }{
		{"the read of a block's checks", "held.source_block_id", "related_checks_source_block"},
		{"the read of an issue's own checks", "held.source_kind = 'issue'", "related_checks_source_issue"},
	} {
		statement := recorded.Holding(t, source.marker)
		keyed := map[string]string{"related_checks": source.index}
		explaintest.RequireEachRowProbedOnce(t, ctx, pool, keys, keyed, source.name, nil, statement)
		explaintest.RequireTheCheckFails(t, ctx, pool, keys, keyed, source.name+" with "+source.index+" dropped",
			explaintest.PlanCacheModes, []explaintest.Statement{{SQL: "set local lock_timeout = '10s'"}, {SQL: "drop index " + source.index}}, statement)
	}
}
