package docs

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/benchtime"
)

// anchoredMarkTimingLimit is how many of the document's mark ids the three reads that name marks
// are measured with: an edit sends the marks of the table rows it touches, a handful, not every
// mark the document carries. Twenty is the size of a large edit's set.
const anchoredMarkTimingLimit = 20

// TestStatementTimings checks, on the seeded measurement database (benchtime), which
// DISPATCH_BENCH_DATABASE_URL names, what this package's statement timings rest on, and times
// nothing: the seed holds the anchored document and marks anchored on it (anchoredMarkIDs), and
// each pair's two texts, run once over the arguments both sides bind, answer the same rows
// (requireSameAnchorReadRows), so each pair is two texts of one read. CI sets that variable in one
// step, envoy-go's statement-timing step, which seeds the database and runs this test by name;
// every other run skips it. api/search_bench_test.go and api/related_recall_test.go are gated the
// same way. Every pair below is measured by BenchmarkStatementTimings, a tool: a pair's verdict is
// the order of two medians, which a runner's load decides (statement_timing_bench_test.go says
// what was read).
//
// All six reads are protocol 1 pairs: openAnchoredMarks over asks and over comments, the two
// reads of edits.go (rejectUnindexedTableMarks, rejectLiveTableAnchors) and the two of
// mutation.go (checkTableAnchorRows). Their gain is their own text and no index this change adds.
// Both indexes the new texts reach were already there - comments_anchor_artifact is migration
// 0032 and asks_anchor_artifact_id migration 0148, both below this change's ten migrations,
// 20261101182419 to 20261101182522 - and what each read gained is the uuid comparison beside
// its text comparison, which is what makes the index eligible at all; the two mutation.go
// pairs' before texts also take main's `for share`, which the new texts dropped. One schema
// therefore holds both sides, and the pair measures the gain: the before text cannot use the
// index, so the day a read loses that comparison its median goes back to the before text's and
// the pair fails. Untimed, TestAnchorReadsSearchTheirTablesAnchorIndex holds each read to its
// index in every run of the package.
//
// Each pair's before text is the oracle anchor_reads_test.go already holds - the text the
// release before this change ran - read from there rather than copied a second time: two copies
// of one statement drift apart, and that oracle is what the package's equality test is held to.
func TestStatementTimings(t *testing.T) {
	statementTimings(t, false)
}

// statementTimings reads the subjects and checks every pair TestStatementTimings describes, and
// with measure true times each pair after its check (BenchmarkStatementTimings).
func statementTimings(t testing.TB, measure bool) {
	runner := benchtime.Open(t, os.Getenv(benchtime.DatabaseURLEnv))
	document := runner.Subjects().AnchoredDocument
	markIDs := anchoredMarkIDs(t, runner, document)
	for _, read := range anchorReads() {
		requireSameAnchorReadRows(t, runner, read, document, markIDs)
		if !measure {
			continue
		}
		// The new text adds no parameter: it reads $1 twice, once as text and once as a uuid, so
		// both sides of a pair bind exactly the same arguments.
		arguments := anchorReadValues(read.query, document, markIDs)
		runner.Pair(t, "docs."+read.name,
			benchtime.Statement{SQL: read.oracle, Args: arguments},
			benchtime.Statement{SQL: read.query, Args: arguments})
	}
}

// requireSameAnchorReadRows runs read's two texts once each over the document and the marks, on
// the runner's connection in one transaction it rolls back, as benchtime's explain runs a pair's
// sides (two of the before texts end in `for share`), and fails unless both answer the same
// rows, as TestAnchorReadsReturnTheRowsMainsStatementsDo holds them to on its fixture: two texts
// that answer different rows are no pair of one read's texts. It logs how many rows they
// answered.
func requireSameAnchorReadRows(t testing.TB, runner *benchtime.Runner, read anchorRead, document string, markIDs []string) {
	t.Helper()
	ctx := context.Background()
	tx, err := runner.Conn().Begin(ctx)
	if err != nil {
		t.Fatalf("begin the read of docs.%s: %v", read.name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	after := anchorReadRows(t, ctx, tx, read.query, document, markIDs, read.ordered)
	before := anchorReadRows(t, ctx, tx, read.oracle, document, markIDs, read.ordered)
	if !slices.Equal(after, before) {
		t.Errorf("docs.%s: the after text answered\n%v\nwant the before text's rows\n%v", read.name, after, before)
	}
	benchtime.Logf(t, "docs.%s: both texts answer %d rows", read.name, len(before))
}

// anchoredMarkIDs are mark ids the document actually carries, taken through each table's anchor
// index rather than by the text comparison this change is about: the subject read of a
// measurement must not itself read two tables whole, and it would warm pages unevenly between
// the sides of the first pair.
func anchoredMarkIDs(t testing.TB, runner *benchtime.Runner, document string) []string {
	t.Helper()
	ctx := context.Background()
	var markIDs []string
	for _, target := range anchorTargets {
		rows, err := runner.Conn().Query(ctx, `select anchor->>'mark_id' from `+target.table+
			` where `+target.anchoredOn()+` and anchor->>'mark_id' is not null order by 1 limit $2`,
			document, anchoredMarkTimingLimit)
		if err != nil {
			t.Fatalf("read the marks anchored on %s in %s: %v", document, target.table, err)
		}
		found, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("read the marks anchored on %s in %s: %v", document, target.table, err)
		}
		markIDs = append(markIDs, found...)
	}
	slices.Sort(markIDs)
	markIDs = slices.Compact(markIDs)
	if len(markIDs) == 0 {
		t.Fatalf("no ask or comment is anchored on %s, so the reads that name marks have no subject", document)
	}
	return markIDs[:min(len(markIDs), anchoredMarkTimingLimit)]
}
