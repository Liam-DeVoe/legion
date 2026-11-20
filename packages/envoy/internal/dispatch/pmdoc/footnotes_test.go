package pmdoc

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extensionast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	gmtext "github.com/yuin/goldmark/text"
)

// A footnote reference this package resolves by its label (exactFootnoteReference) resolves as
// goldmark's own footnote reference parser resolves it, which compares the reference with every
// definition in the document's list: the same definition, the first whose label equals the
// reference's byte for byte, and the same Index, each definition numbered in the order of its first
// reference. Each document is read by the document reader and by goldmark with its footnote
// extension alone, and the two give each reference, in document order, the same Index, and each
// definition a reference numbered the same Index: references repeated, references to no definition
// and an empty one, a label defined twice, a label of 1,500 characters (goldmark's footnote parser
// bounds no label's length; its 999 characters bound a link's label alone, parser/link.go:153, 188
// and 288 at v1.8.6) and one a character longer that no definition has, an image's `!` before a
// reference, references after their definitions, and a document with no definition at all.
func TestFootnoteReferencesResolveAsGoldmarksParserResolvesThem(t *testing.T) {
	long := strings.Repeat("l", 1500)
	for _, test := range []struct{ name, markdown string }{
		{"repeated references", "a[^b] b[^a] c[^b] d[^a] e[^c] f[^b]\n\n[^a]: x\n\n[^b]: y\n\n[^c]: z\n"},
		{"references to no definition", "a[^nope] b[^a] c[^missing] d[^a] e[^]\n\n[^a]: x\n"},
		{"a label defined twice", "a[^a] b[^a]\n\n[^a]: first\n\n[^a]: second\n"},
		{"a label of 1,500 characters", "a[^" + long + "] b[^" + long + "x] c[^" + long + "]\n\n[^" + long + "]: x\n"},
		{"an image's ! before a reference", "a![^a] b\n\n[^a]: x\n"},
		{"references after their definitions", "[^a]: x\n\n[^b]: y\n\nc[^b] d[^a] e[^b]\n"},
		{"no definition at all", "a[^a] b[^b]\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := []byte(test.markdown)
			pc := parser.NewContext()
			pc.Set(writeBudgetKey, NewWriteBudget())
			ours := footnoteNumbering(blockReader.md.Parser().Parse(gmtext.NewReader(source), parser.WithContext(pc)))
			theirs := footnoteNumbering(goldmark.New(goldmark.WithExtensions(extension.Footnote)).Parser().Parse(gmtext.NewReader(source)))
			if ours != theirs {
				t.Errorf("%q: the document reader numbers %s, goldmark's footnote extension %s", test.markdown, ours, theirs)
			}
		})
	}
}

// footnoteNumbering is the Index of each footnote reference under root, in document order, and the
// label and Index of each definition a reference numbered, in label order.
func footnoteNumbering(root ast.Node) string {
	var references, definitions []string
	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := node.(type) {
		case *extensionast.FootnoteLink:
			references = append(references, strconv.Itoa(node.Index))
		case *extensionast.Footnote:
			if node.Index > 0 {
				definitions = append(definitions, fmt.Sprintf("%q=%d", node.Ref, node.Index))
			}
		}
		return ast.WalkContinue, nil
	})
	slices.Sort(definitions)
	return fmt.Sprintf("references [%s] and definitions [%s]", strings.Join(references, " "), strings.Join(definitions, " "))
}
