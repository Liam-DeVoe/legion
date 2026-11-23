package api

import (
	"context"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// The corpus tests (TestNearCopyRefusalReplay, TestRelatedRecallOnDuplicates and
// TestRelatedCheckLatencyOnRecallCorpus) create every issue of a copied project through the API,
// and the create refuses a `block[]` pointer at a block the database does not hold, or at a block
// of an issue that is neither the new issue nor one above it (pmdoc.CheckBlockPointers). A corpus
// cannot hold what production held there: it carries only some versions of each spec, a later
// version goes in as a row and not as a write (so the block index never holds its blocks), and a
// load leaves off a parent the walk has not reached, or every parent. So each create writes its
// spec without its pointers into other documents (corpusWritableSpec), and then sets version 1
// back to the corpus's text (setCorpusFirstSpec). A pointer is an attribute value, which every
// search reads only as words of the markdown, so every vector these tests read is of the corpus's
// text, as it was before a pointer was refused.

// corpusWritableSpec is spec, a spec of issue key, with every `block[]` pointer into another
// document taken out, and rewritten true when it took one out; else spec as it is. A pointer into
// the issue's own spec is kept: the create writes all of that document, so it holds the block.
func corpusWritableSpec(t testing.TB, key, spec string) (written string, rewritten bool) {
	t.Helper()
	tree, err := pmdoc.ParseForWrite(spec, nil)
	if err != nil {
		// The create refuses the spec with this parse's own error, which the caller reports.
		return spec, false
	}
	own := key + "/spec#"
	kinds := pmdoc.LinkKinds()
	pmdoc.Walk(tree, func(node *pmdoc.Node) bool {
		for _, kind := range kinds {
			var values []string
			switch held := node.Attrs[kind].(type) {
			case []string:
				values = held
			case []any:
				for _, value := range held {
					if value, ok := value.(string); ok {
						values = append(values, value)
					}
				}
			default:
				continue
			}
			kept := make([]string, 0, len(values))
			for _, value := range values {
				if ref, ok := text.CanonicalBlockRef(value); ok && !strings.HasPrefix(text.TargetID(ref), own) {
					continue
				}
				kept = append(kept, value)
			}
			if len(kept) != len(values) {
				node.Attrs[kind] = kept
				rewritten = true
			}
		}
		return true
	})
	if !rewritten {
		return spec, false
	}
	written, err = pmdoc.Render(tree)
	if err != nil {
		t.Fatalf("render %s's spec without its pointers into other documents: %v", key, err)
	}
	return written, true
}

// setCorpusFirstSpec sets version 1 of key's spec to markdown, the corpus's text, which its create
// wrote without its pointers into other documents (corpusWritableSpec). The search triggers keep
// every vector of the markdown in step with it.
func setCorpusFirstSpec(t testing.TB, ctx context.Context, database *store.Store, key, markdown string) {
	t.Helper()
	tag, err := database.Pool.Exec(ctx, `
		update artifact_versions v set markdown = $2
		  from artifacts a
		 where a.id = v.artifact_id and a.issue_key = $1 and a.is_primary and v.number = 1
	`, key, markdown)
	if err != nil {
		t.Fatalf("set %s's first spec to the corpus's text: %v", key, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("set %s's first spec to the corpus's text: %d versions changed, want 1", key, tag.RowsAffected())
	}
}

// TestACorpusSpecIsWrittenWithoutItsPointersIntoOtherDocuments holds corpusWritableSpec to its
// rule, with no corpus and no database: a pointer into another issue's spec is taken out, one
// into the issue's own spec is kept, and a spec with no pointer out is written as it is.
func TestACorpusSpecIsWrittenWithoutItsPointersIntoOtherDocuments(t *testing.T) {
	const own, other = "dispatch://CORE-2/spec#b-r1", "dispatch://CORE-1/spec#b-r9"
	spec := ":::requirement{#r1}\nThe page lists every open ask.\n:::\n\n" +
		":::check{#k1 serves=\"" + other + " " + own + "\" when=\"after_merge\"}\nNo answered ask is drawn.\n:::\n"
	written, rewritten := corpusWritableSpec(t, "CORE-2", spec)
	if !rewritten || strings.Contains(written, other) || !strings.Contains(written, own) {
		t.Fatalf("CORE-2's spec was written (rewritten %v) as\n%s\nwant %s kept and %s taken out", rewritten, written, own, other)
	}
	tree, err := pmdoc.ParseForWrite(written, nil)
	if err != nil {
		t.Fatalf("the written spec does not parse: %v", err)
	}
	if pointers := pmdoc.BlockPointers(tree); len(pointers) != 1 || pointers[0].Ref != own {
		t.Fatalf("the written spec's pointers are %+v, want %s alone", pointers, own)
	}
	if written, rewritten := corpusWritableSpec(t, "CORE-1", strings.ReplaceAll(spec, other+" "+own, other)); rewritten {
		t.Fatalf("CORE-1's spec, whose one pointer is into its own spec, was rewritten as\n%s", written)
	}
}
