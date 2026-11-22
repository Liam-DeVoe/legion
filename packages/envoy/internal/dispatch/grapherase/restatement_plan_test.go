package grapherase

import (
	"context"
	"regexp"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/explaintest"
)

// owedPlanDocuments is how many other documents the owed-mark plan test seeds, each with its
// document_block_versions row and a version holding markdown: enough that a read of either table
// whole costs the planner more than a probe of an index.
const owedPlanDocuments = 5000

// TestTheEraseMarksTheDocumentsOwedWindowsThroughTheirKey explains the update restateEmbeddings
// sends under the erase's owner locks to mark the documents whose windows went owed them, over
// owedPlanDocuments other documents of issue CORE-1, each given its document_block_versions row by
// its version holding markdown (migration 0122's trigger), written and analyzed before the update.
// The update is the one restateEmbeddings sends for the document seedPreStoreDocument writes, whose
// version 1 holds markdown, with the argument it binds (explaintest.RecordedTx, which keeps what
// Exec sends), and the document is held to being marked by it. With hash and merge joins off,
// under each plan-cache mode the plan must read document_block_versions through its primary key
// (document_block_versions_pkey) and artifact_versions through an index, each by its key
// (explaintest.RequireEachRowProbedOnce): the erase reads no whole large table while it holds its
// owner locks (R5). The control is the update as it stood before, its column cast to text and
// compared with the documents bound as text (owedBefore), which must plan, run and fail the check
// under both modes: no index keys the cast column, so it reads document_block_versions whole.
func TestTheEraseMarksTheDocumentsOwedWindowsThroughTheirKey(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	pool := database.Pool
	document := seedPreStoreDocument(t, database)
	if _, err := pool.Exec(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
		select 'CORE-1', 'CORE', 'other-' || i, 'other-' || i || '.md', 'doc', false, '{"kind":"user","id":"alice"}'
		from generate_series(1, $1::int) i`, owedPlanDocuments); err != nil {
		t.Fatalf("seed the other documents of issue CORE-1: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		insert into artifact_versions (artifact_id, number, markdown, authors)
		select a.id, 1, 'Another document.', '[{"kind":"user","id":"alice"}]'
		from artifacts a where a.issue_key = 'CORE-1' and a.slug like 'other-%'`); err != nil {
		t.Fatalf("write version 1 of the other documents of issue CORE-1: %v", err)
	}
	if _, err := pool.Exec(ctx, `analyze artifacts, artifact_versions, document_block_versions`); err != nil {
		t.Fatalf("analyze the seeded tables: %v", err)
	}
	keys := explaintest.NewIndexKeys(pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the update: %v", err)
	}
	defer tx.Rollback(ctx)
	// The version's trigger marked the document owed its windows when the seed wrote it, which
	// would leave the update nothing to show; the mark is cleared so the update must set it.
	if _, err := tx.Exec(ctx, `update document_block_versions set windows_owed_at = null where artifact_id = $1::uuid`,
		document.artifactID); err != nil {
		t.Fatalf("clear the mark of %s: %v", document.refKey, err)
	}
	recorded := &explaintest.RecordedTx{Tx: tx}
	erasure := &Erasure{Kind: refs.BlockNodeKind, ID: document.refKey + "#erased"}
	if err := erasure.restateEmbeddings(ctx, recorded, forgottenEmbeddings{documents: []string{document.artifactID}}); err != nil {
		t.Fatalf("mark %s owed its windows: %v", document.refKey, err)
	}
	var owed bool
	if err := tx.QueryRow(ctx, `select windows_owed_at is not null from document_block_versions where artifact_id = $1::uuid`,
		document.artifactID).Scan(&owed); err != nil || !owed {
		t.Fatalf("%s is not owed its windows after the update (err %v), so the plan read below is of an update that marks nothing",
			document.refKey, err)
	}
	// The update's transaction holds the document's row until it ends, which each explained update
	// would wait behind for ever; the statement it sent is all the test needs of it.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("end the update: %v", err)
	}
	update := recorded.Holding(t, "update document_block_versions owed")
	keyed := map[string]string{"document_block_versions": "document_block_versions_pkey", "artifact_versions": explaintest.AnyIndex}
	explaintest.RequireEachRowProbedOnce(t, ctx, pool, keys, keyed, "the mark of the documents owed their windows", nil, update)
	explaintest.RequireTheCheckFails(t, ctx, pool, keys, keyed, "the mark as it stood, its column cast to text",
		explaintest.PlanCacheModes, nil,
		explaintest.Statement{SQL: explaintest.Spliced(t, update.SQL, owedBefore(t, update.SQL)), Args: update.Args})
}

// owedBefore is restateEmbeddings' test of a row's document as it stood before it compared the
// column as stored, written with the parameter sql, the update it sends now, binds the documents
// at: the column cast to text and compared with the documents bound as text.
func owedBefore(t *testing.T, sql string) explaintest.Part {
	t.Helper()
	documents := explaintest.Parameter(t, sql, regexp.MustCompile(`owed\.artifact_id = any\((\$[0-9]+)::uuid\[\]\)`))
	return explaintest.Part{
		Name: "the test of the row's document", Opens: "owed.artifact_id = any(", Closes: "and exists (",
		Text: "owed.artifact_id::text = any(" + documents + "::text[])\n\t\t\t  ",
	}
}
