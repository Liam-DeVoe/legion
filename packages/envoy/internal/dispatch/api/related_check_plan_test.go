package api

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/explaintest"
)

// recordedBatches is explaintest.RecordedTx keeping each query of a batch sent through SendBatch as
// well, with the arguments it bound, the call writeRelatedChecks sends its inserts by: RecordedTx
// keeps the statements read by Query or QueryRow.
type recordedBatches struct{ *explaintest.RecordedTx }

func (r recordedBatches) SendBatch(ctx context.Context, batch *pgx.Batch) pgx.BatchResults {
	for _, query := range batch.QueuedQueries {
		r.Sent = append(r.Sent, explaintest.Statement{SQL: query.SQL, Args: query.Arguments})
	}
	return r.Tx.SendBatch(ctx, batch)
}

// relatedCheckErasurePlanRows is how many graph_erasures rows of each of two other kinds of node
// the plan test seeds - blocks of other documents, and comments: enough that a read of the table
// whole costs the planner more than a probe of an index.
const relatedCheckErasurePlanRows = 2000

// TestTheRelatedCheckRecordProbesTheErasuresOfItsDocument explains the inserts writeRelatedChecks
// sends for a check of decision block decide of a document of issue CORE-1 and for CORE-1's own
// check, each of which reads graph_erasures under CORE-1's owner lock for an erase of its source:
// of the decision block's node, and of any block of CORE-1's spec (relatedCheckInsert).
// graph_erasures holds the erasures of relatedCheckErasurePlanRows blocks of other documents and
// as many comments, beside one of another block of the decision block's document, which the probe
// by the document reads and the block's whole node then turns away, written and analyzed before
// the inserts. Each insert is the one writeRelatedChecks sends, with the arguments
// relatedCheckWriteOf binds (explaintest.RecordedTx, its SendBatch kept by recordedBatches), and
// the two are held to writing their rows and to reading graph_erasures when they run. With hash
// and merge joins off, under each plan-cache mode the plan must read graph_erasures through
// graph_erasures_block_document, by its key (explaintest.RequireEachRowProbedOnce): the record
// reads no whole large table while it holds its owner locks (R5). The control is each insert with
// that index dropped inside the explaining transaction, as the database stood before
// 20261119020500, which must plan, run and fail the check under both modes: it reads
// graph_erasures whole.
func TestTheRelatedCheckRecordProbesTheErasuresOfItsDocument(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	pool := database.Pool
	if _, err := pool.Exec(ctx, `
		insert into projects (key, name, default_assignee) values ('CORE', 'Core', 'alice');
		insert into issues (key, project_key, number, title, created_by, rank) values
			('CORE-1', 'CORE', 1, 'One', '{"kind":"user","id":"alice"}', 'U');
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
		values ('CORE-1', 'CORE', 'spec', 'spec.md', 'doc', true, '{"kind":"user","id":"alice"}');
	`); err != nil {
		t.Fatalf("seed issue CORE-1 and its spec: %v", err)
	}
	var document, refKey string
	if err := pool.QueryRow(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
		values ('CORE-1', 'CORE', 'decisions', 'decisions.md', 'doc', false, '{"kind":"user","id":"alice"}')
		returning id::text, ref_key
	`).Scan(&document, &refKey); err != nil {
		t.Fatalf("create the decision block's document: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		insert into graph_erasures (node_kind, node_id, removed, ran_by, ran_at)
		select erased.kind, erased.id, '{}', '{"kind":"user","id":"alice"}', now()
		from (select 'block', 'CORE-' || (i + 1) || '/decisions#b' || i from generate_series(1, $1::int) i
		      union all
		      select 'comment', gen_random_uuid()::text from generate_series(1, $1::int) i
		      union all
		      select 'block', $2::text || '#other') as erased (kind, id)`,
		relatedCheckErasurePlanRows, refKey); err != nil {
		t.Fatalf("seed the erasures of other nodes: %v", err)
	}
	if _, err := pool.Exec(ctx, `analyze artifacts, graph_erasures`); err != nil {
		t.Fatalf("analyze the seeded tables: %v", err)
	}

	actor := model.Actor{Kind: "user", ID: "alice"}
	suggestions := &model.Suggestions{Outcome: relatedNone, Search: relatedKeywordsOnly, Related: []model.WriteSuggestion{}}
	record := relatedRecord{project: "CORE", terms: []string{"ship"}, totalMS: 40}
	checks := []struct {
		name   string
		source suggestionSource
	}{
		{"the insert of a decision block's check",
			suggestionSource{kind: "decision_block", issueKey: "CORE-1", blockID: "decide", blockDocument: document, actor: actor}},
		{"the insert of an issue's own check", suggestionSource{kind: "issue", issueKey: "CORE-1", actor: actor}},
	}
	writes := make([]relatedCheckWrite, 0, len(checks))
	for _, check := range checks {
		write, ok := relatedCheckWriteOf(relatedCheckRecord{source: check.source, suggestions: suggestions, record: record})
		if !ok {
			t.Fatalf("%s has no related_checks row to write", check.name)
		}
		writes = append(writes, write)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the record: %v", err)
	}
	defer tx.Rollback(ctx)
	recorded := recordedBatches{&explaintest.RecordedTx{Tx: tx}}
	if err := writeRelatedChecks(ctx, recorded, writes); err != nil {
		t.Fatalf("write the checks' rows: %v", err)
	}
	var written int
	if err := tx.QueryRow(ctx, `select count(*) from related_checks where source_issue_key = 'CORE-1' and cardinality(terms) > 0`).Scan(&written); err != nil {
		t.Fatalf("read the checks' rows: %v", err)
	}
	if written != len(checks) {
		t.Fatalf("the inserts wrote %d related_checks rows holding terms, want %d, so the plans below would be of inserts an erasure turned away",
			written, len(checks))
	}
	// The record's transaction holds a lock on graph_erasures until it ends, which the control's
	// drop index would wait behind for ever; the statements it sent are all the test needs of it.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("end the record: %v", err)
	}
	var inserts []explaintest.Statement
	for _, statement := range recorded.Sent {
		if statement.SQL == relatedCheckInsert {
			inserts = append(inserts, statement)
		}
	}
	if len(inserts) != len(checks) {
		t.Fatalf("the record sent %d inserts, want one for each of the %d checks", len(inserts), len(checks))
	}

	keys := explaintest.NewIndexKeys(pool)
	keyed := map[string]string{"graph_erasures": "graph_erasures_block_document"}
	for index, insert := range inserts {
		name := checks[index].name
		var loops float64
		for _, scan := range explaintest.Scans(t, explaintest.ExplainAfter(t, ctx, pool, nil, insert, "force_custom_plan", "json")) {
			if scan.Relation == "graph_erasures" {
				loops += scan.Loops
			}
		}
		if loops == 0 {
			t.Fatalf("%s reads graph_erasures nowhere when it runs, so the plans below would hold a read that never runs", name)
		}
		explaintest.RequireEachRowProbedOnce(t, ctx, pool, keys, keyed, name, nil, insert)
		explaintest.RequireTheCheckFails(t, ctx, pool, keys, keyed, name+" with graph_erasures_block_document dropped",
			explaintest.PlanCacheModes, []explaintest.Statement{{SQL: "set local lock_timeout = '10s'"}, {SQL: "drop index graph_erasures_block_document"}}, insert)
	}
}
