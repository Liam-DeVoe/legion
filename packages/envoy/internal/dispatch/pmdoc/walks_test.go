package pmdoc

import (
	"sync/atomic"
	"testing"

	"github.com/reearth/ygo/crdt"
)

// CountWalksForTest counts the children each positional fragment Insert and Delete of a write
// walks past (walked), and only while it is on. Replacing the middle one of three paragraphs with a
// heading deletes the paragraph at index 1 and inserts the heading at index 1, two walks past one
// child each; the same write once the count is stopped adds nothing to it.
func TestCountWalksForTestCountsTheWalksOfAWrite(t *testing.T) {
	doc := crdt.New()
	fragment := doc.GetXmlFragment("prosemirror")
	writeUpdate(t, doc, fragment, repeatedBlocks(t, "a\n\n", 3))
	withHeading, err := ParseRendering("a\n\n# b\n\na\n")
	if err != nil {
		t.Fatalf("parse the paragraphs around a heading: %v", err)
	}
	var walked atomic.Int64
	stop := CountWalksForTest(&walked)
	writeUpdate(t, doc, fragment, withHeading)
	stop()
	if got := walked.Load(); got != 2 {
		t.Fatalf("replacing the middle one of three paragraphs with a heading walked past %d children, want 2: one for the delete at index 1 and one for the insert", got)
	}
	writeUpdate(t, doc, fragment, repeatedBlocks(t, "a\n\n", 3))
	if got := walked.Load(); got != 2 {
		t.Fatalf("a write after the count stopped moved it to %d, want it left at 2", got)
	}
}
