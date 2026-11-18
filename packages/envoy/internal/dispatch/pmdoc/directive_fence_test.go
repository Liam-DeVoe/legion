package pmdoc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The browser editor's parser ends a typed block at a line of at least its fence's colons in code
// it holds, measured on the written line: starting at most three columns past the typed block's
// own content column, list markers adding their width, a tab advancing to the next multiple of
// four from the column it stands at, and never inside a blockquote within the typed block.
// testdata/typed-fence-lines.json holds, for code lines across layouts - a callout at the top,
// and inside a blockquote, list items, a footnote definition and another callout - a tree, the
// markdown this renderer writes for it, and the engine's own reading of that markdown (the
// fixture generator writes it). The renderer writes each typed block's fence longer than every
// line that could close it, so the engine keeps every block whole with its code, and so does this
// parser.
func TestTypedFencesReadTheSameInTheEngineAndHere(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "typed-fence-lines.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name         string          `json:"name"`
		Tree         json.RawMessage `json:"tree"`
		Markdown     string          `json:"markdown"`
		EngineCloses bool            `json:"engine_closes"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			doc, err := FromJSON(test.Tree)
			if err != nil {
				t.Fatal(err)
			}
			if markdown, err := Render(doc); err != nil || markdown != test.Markdown {
				t.Fatalf("the case's markdown is not what this renderer writes for its tree: %q (%v)", markdown, err)
			}
			if test.EngineCloses {
				t.Fatalf("the engine ends a typed block early in %q", test.Markdown)
			}
			back, err := Parse(test.Markdown)
			if err != nil {
				t.Fatalf("Parse(%q): %v", test.Markdown, err)
			}
			if reason := readDifference(doc, back, shapeOnly); reason != "" || codeText(back) != codeText(doc) {
				t.Fatalf("Parse(%q): %s, code %q, want the tree's blocks holding %q", test.Markdown, reason, codeText(back), codeText(doc))
			}
		})
	}
}

// TypedFenceLine reads a typed block's fence line as the parser reads it. Each row is held to
// Parse: a document opens with a typed block at the row's line exactly where the reader says the
// line opens one, of the colons the reader gives, and a typed block of three colons ends at the
// row's line exactly where the reader says the line is colons alone, at least three of them. A line
// reads alike with its line feed.
func TestTypedFenceLineReadsALineAsTheParserDoes(t *testing.T) {
	const opening = `:::callout{kind="note" title=""}`
	parse := func(t *testing.T, markdown string) *Node {
		t.Helper()
		doc, err := Parse(markdown)
		if err != nil {
			t.Fatalf("Parse(%q): %v", markdown, err)
		}
		return doc
	}
	type row struct {
		name, line    string
		colons        int
		closes, opens bool
	}
	read := func(t *testing.T, row row) {
		t.Helper()
		colons, closes, opens := TypedFenceLine(row.line)
		if colons != row.colons || closes != row.closes || opens != row.opens {
			t.Fatalf("TypedFenceLine(%q) = %d, %t, %t, want %d, %t, %t", row.line, colons, closes, opens, row.colons, row.closes, row.opens)
		}
		if fed, fedCloses, fedOpens := TypedFenceLine(row.line + "\n"); fed != colons || fedCloses != closes || fedOpens != opens {
			t.Fatalf("TypedFenceLine(%q) = %d, %t, %t, want it read as without its line feed", row.line+"\n", fed, fedCloses, fedOpens)
		}
	}
	for _, row := range []row{
		{name: "an opening line", line: opening, colons: 3, opens: true},
		{name: "an opening line of four colons", line: ":" + opening, colons: 4, opens: true},
		{name: "indented three columns", line: "   " + opening, colons: 3, opens: true},
		{name: "indented four columns", line: "    " + opening},
		{name: "indented by a tab", line: "\t" + opening},
		{name: "two spaces and a tab, four columns", line: "  \t" + opening},
	} {
		t.Run("opening/"+row.name, func(t *testing.T) {
			read(t, row)
			markdown := row.line + "\nx\n" + strings.Repeat(":", max(row.colons, 3)) + "\n"
			if typed := parse(t, markdown).Children[0].Type == "callout"; typed != row.opens {
				t.Fatalf("Parse(%q) opens a typed block: %t; the reader says the line opens one: %t", markdown, typed, row.opens)
			}
		})
	}
	for _, row := range []row{
		{name: "three colons", line: ":::", colons: 3, closes: true},
		{name: "four colons", line: "::::", colons: 4, closes: true},
		{name: "trailing spaces", line: ":::  ", colons: 3, closes: true},
		{name: "a trailing tab", line: ":::\t", colons: 3, closes: true},
		{name: "fewer colons than the fence", line: "::", colons: 2, closes: true},
		{name: "indented three columns", line: "   :::", colons: 3, closes: true},
		{name: "indented four columns", line: "    :::"},
		{name: "indented by a tab", line: "\t:::"},
	} {
		t.Run("closing/"+row.name, func(t *testing.T) {
			read(t, row)
			markdown := opening + "\nx\n" + row.line + "\ny\n:::\n"
			doc := parse(t, markdown)
			if ended := doc.Children[0].Type == "callout" && len(doc.Children) > 1; ended != (row.closes && row.colons >= 3) {
				t.Fatalf("Parse(%q) ends the typed block at the row's line: %t; the reader says the line is %d colons alone: %t", markdown, ended, row.colons, row.closes)
			}
		})
	}
}

func codeText(doc *Node) string {
	text := ""
	Walk(doc, func(node *Node) bool {
		if node.Type == "code_block" && text == "" {
			text = node.Children[0].Text
		}
		return true
	})
	return text
}
