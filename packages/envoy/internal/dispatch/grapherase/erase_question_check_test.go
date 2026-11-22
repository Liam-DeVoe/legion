package grapherase

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// R5: a decision block's question is a paragraph block inside it, so the erase that takes the
// question's text is the erase of that paragraph; the decision block itself can be erased only
// once no block states part_of into it. The decision block's related-item check holds the
// lexemes its keyword search picked from that question (related_checks.terms), stemmed, so the
// search for the erased bytes cannot find them. The inner block of seedAgreeingStore is the
// question, a sentence of words whose lexemes are not spelled as written, and outer is the
// decision block whose check holds them. The listing of the erase of the inner block must name
// that check, as a row the run rewrites or as one it leaves, and the census reads zero.
func TestTheEraseOfADecisionBlocksQuestionNamesItsCheck(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seeded := seedAgreeingStore(t, database)
	const question = "Should zymurgy ledger totals round down?"
	if tag, err := database.Pool.Exec(ctx, `update document_blocks set excerpt = $2 where artifact_id = $1::uuid and block_id = 'inner'`,
		seeded.document.artifactID, question); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("write the inner block's text: %d rows, %v", tag.RowsAffected(), err)
	}
	check := seedRelatedCheck(t, database, "CORE-1", "outer", question)
	before := relatedCheckTerms(t, database, check)
	if len(before) == 0 || strings.Contains(strings.Join(before, " "), "zymurgy") {
		t.Fatalf("the check holds the terms %q, want the stemmed lexemes of the question, so this test would prove nothing", before)
	}
	requireCensusZero(t, database, "before the erase")

	erasure, err := confirmedErase(t, database, refs.BlockNodeKind, seeded.inner, time.Millisecond)
	if err != nil {
		t.Fatalf("erase %s: %v", seeded.inner, err)
	}
	if !erasure.Applied {
		t.Fatalf("the erase of %s removed nothing: %q", seeded.inner, erasure.Blockers)
	}
	if !listed(erasure, "related_checks", check) {
		t.Errorf("the erase of the question %s neither rewrites nor leaves the decision_block check %s, which holds the question's lexemes %q, and its listing does not name it:\n%s",
			seeded.inner, check, relatedCheckTerms(t, database, check), strings.Join(erasure.Listing, "\n"))
	}
	requireCensusZero(t, database, "after the erase")
}
