package api

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// blockTextContent answers every shape of a block's stored text found so far with the content
// its text_tree is read from, and reads no database. A document's block, which can stand in a
// list item or a footnote definition, has the closing fence's indentation taken out of its lines.
// A block of a comment, a message or an ask keeps its lines as written, and its content reads as
// the item's own parse reads what the block holds. A text that does not read back as one typed
// block is answered as it stands. The documents' texts are slices of a rendering, as the block
// index stores them; the items' texts are as a comment writes them.
func TestBlockTextContentReadsEveryShape(t *testing.T) {
	const document, item = true, false
	for _, row := range []struct {
		name     string
		document bool
		whole    string
		want     string
	}{
		{"a document's top-level block", document,
			":::requirement{#r1}\nAn answered ask leaves the list.\n:::",
			"An answered ask leaves the list.\n"},
		{"a document's block in a list item, its lines two spaces in", document,
			":::requirement{#l1}\n  Nested in a list.\n  :::",
			"Nested in a list.\n"},
		{"a document's block after the marker `10. `, its two paragraphs four spaces in", document,
			":::requirement{#l10}\n    Nested in item 10.\n\n    Its second paragraph.\n    :::",
			"Nested in item 10.\n\nIts second paragraph.\n"},
		{"a document's block two levels deep in a list, its lines four spaces in", document,
			":::requirement{#r2}\n    Deep in a nested list.\n    :::",
			"Deep in a nested list.\n"},
		{"a document's block in a footnote definition, its lines four spaces in", document,
			":::requirement{#f1}\n    In a footnote.\n    :::",
			"In a footnote.\n"},
		{"a document's block in a list item with a blank line written without the indentation, which is kept", document,
			":::requirement{#l2}\n  First.\n\n  Second.\n  :::",
			"First.\n\nSecond.\n"},
		{"a document's block with a line that does not carry the fence's indentation", document,
			":::requirement{#r1}\nNot carried.\n  :::",
			":::requirement{#r1}\nNot carried.\n  :::"},
		{"a document's block in a quote, answered with its opening line", document,
			":::requirement{#q1}\n> Nested in a quote.\n> :::",
			":::requirement{#q1}\n> Nested in a quote.\n> :::"},
		{"a document's empty block", document,
			":::requirement{#e1}\n\n:::",
			""},

		{"an item's block holding another, neither written with an id", item,
			"::::callout{}\n:::callout{}\nInner words.\n:::\n::::",
			":::callout{}\nInner words.\n:::\n"},
		{"an item's block opening with a rule `---` and holding a second one", item,
			":::callout{}\n---\n\nAfter a rule.\n\n---\n\nTail.\n:::",
			"***\n\nAfter a rule.\n\n---\n\nTail.\n"},
		{"an item's block whose fence stands two spaces in, over a line at column 0", item,
			":::callout{}\nText of the callout.\n  :::",
			"Text of the callout.\n"},
		{"an item's block whose fence stands three spaces in, over a line at column 0", item,
			":::callout{}\nText of the callout, its fence three spaces in.\n   :::",
			"Text of the callout, its fence three spaces in.\n"},
		{"an item's block whose fence and lines stand two spaces in, with a code line four spaces in (decision 100)", item,
			":::callout{}\n  Intro\n\n    # Not a heading\n  :::",
			"  Intro\n\n    # Not a heading\n"},
		{"an item's block whose fence and every line stand two spaces in", item,
			":::callout{}\n  Every line carries the spaces.\n  :::",
			"  Every line carries the spaces.\n"},
		{"an item's block whose fence stands four spaces in, over a line at column 0", item,
			":::callout{}\nFour in.\n    :::",
			":::callout{}\nFour in.\n    :::"},
		{"an item's block whose fence stands four spaces in, over a line four spaces in", item,
			":::callout{}\n    Four in.\n    :::",
			":::callout{}\n    Four in.\n    :::"},
		{"an item's block whose fence stands a tab in, over a line at column 0", item,
			":::callout{}\nA tab in.\n\t:::",
			":::callout{}\nA tab in.\n\t:::"},
		{"an item's block whose fence stands a tab in, over a line a tab in", item,
			":::callout{}\n\tA tab in.\n\t:::",
			":::callout{}\n\tA tab in.\n\t:::"},
		{"an item's block whose fence is shorter than its opening", item,
			"::::callout{}\nShorter fence.\n:::",
			"::::callout{}\nShorter fence.\n:::"},
		{"an item's text that is no typed block", item,
			"Not a typed block.",
			"Not a typed block."},
		{"an item's block with CRLF line ends", item,
			":::callout{}\r\nCRLF.\r\n:::",
			":::callout{}\r\nCRLF.\r\n:::"},
		{"an item's block holding `:::` in a fenced code block", item,
			"::::callout{}\n```\n:::\n```\n::::",
			"```\n:::\n```\n"},
		{"an item's block with a line feed after its fence", item,
			":::callout{}\nThen a line feed.\n:::\n",
			":::callout{}\nThen a line feed.\n:::\n"},
		{"an item's block in a quote, answered with its opening line", item,
			"> :::callout{}\n> Quoted.\n> :::",
			"> :::callout{}\n> Quoted.\n> :::"},
		{"an item's empty block", item,
			":::callout{}\n:::",
			""},
	} {
		t.Run(row.name, func(t *testing.T) {
			content := blockTextContent(row.whole, row.document)
			if content != row.want {
				t.Fatalf("blockTextContent(%q, document=%t) = %q, want %q", row.whole, row.document, content, row.want)
			}
			if row.document || content == row.whole || content == "" {
				return
			}
			got := sameShape(t, treeContent(t, pmdoc.DisplayJSON(content, pmdoc.DisplayLatest)))
			want := sameShape(t, heldByItsOneBlock(t, row.whole))
			if !bytes.Equal(got, want) {
				t.Errorf("the content %q reads as\n\t%s\nwant what the item's own parse reads in the block\n\t%s", content, got, want)
			}
		})
	}
}

// treeContent is the content of the node tree encodes: a document's blocks, or a block's own.
func treeContent(t *testing.T, tree json.RawMessage) json.RawMessage {
	t.Helper()
	var node struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(tree, &node); err != nil {
		t.Fatalf("decode the tree %s: %v", tree, err)
	}
	return node.Content
}

// heldByItsOneBlock is the tree of what the one block of whole holds, as the display read of
// whole itself reads it: the reading the item's own parse gives that block's lines.
func heldByItsOneBlock(t *testing.T, whole string) json.RawMessage {
	t.Helper()
	var blocks []json.RawMessage
	tree := pmdoc.DisplayJSON(whole, pmdoc.DisplayLatest)
	if err := json.Unmarshal(treeContent(t, tree), &blocks); err != nil || len(blocks) != 1 {
		t.Fatalf("%q reads as %s, want one block (%v)", whole, tree, err)
	}
	return treeContent(t, blocks[0])
}
