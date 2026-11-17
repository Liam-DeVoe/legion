package doctext

import (
	"strings"
	"testing"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// exactRows are documents and what VisibleMarkdown makes of each, CommonMark's reading of it.
var exactRows = []struct{ name, doc, want string }{
	{"a comment on one line", "before <!-- hidden --> after\n", "before  after\n"},
	{"a comment across lines keeps its newlines", "a\n<!--\nhidden\n-->\nb\n", "a\n\n\n\nb\n"},
	{"a comment across a blank line", "a\n\n<!--\nhidden\n\nstill hidden\n-->\nb\n", "a\n\n\n\n\n\n\nb\n"},
	{"two comments on one line", "x<!-- 1 -->y<!-- 2 -->z\n", "xyz\n"},
	{"text after a comment's end, with a comment of its own", "<!--\nh\n--> seen <!-- h -->\n", "\n\n seen \n"},
	{"a comment that never closes runs to the end", "a\n<!-- hidden\nhidden\n", "a\n\n\n"},
	{"a comment inside a fenced code block stays", "```html\n<!-- shown -->\n```\n", "```html\n<!-- shown -->\n```\n"},
	// A fence of tildes keeps a comment as a fence of backticks does, and its end is a fence's end.
	{"a fence of tildes keeps its comment",
		"~~~html\n<!-- shown -->\n~~~\nafter <!-- gone -->\n", "~~~html\n<!-- shown -->\n~~~\nafter \n"},
	// A fence a nested list item holds is indented past three spaces, and a blank line inside it
	// splits what a paragraph reader would take for a code span.
	{"a fence indented inside a list item keeps its comment",
		"- outer\n  - inner\n\n    ```html\n    first\n\n    <!-- shown -->\n    ```\n\nafter <!-- gone -->\n",
		"- outer\n  - inner\n\n    ```html\n    first\n\n    <!-- shown -->\n    ```\n\nafter \n"},
	// The shape of docs/site/src/content/docs/legion/troubleshooting.md:1369: a whole comment,
	// opener and closer, inside a code span on a list item's continuation line.
	{"a code span holding a whole comment stays",
		"  (`<!-- legion: {\"session\":\"…\",\"phase\":\"review\",\"head\":\"…\"} -->`) and requires the session it\n",
		"  (`<!-- legion: {\"session\":\"…\",\"phase\":\"review\",\"head\":\"…\"} -->`) and requires the session it\n"},
	{"a code span holding an opener stays", "the `<!-- legion:` footer\n", "the `<!-- legion:` footer\n"},
	// CommonMark: a code span may run across lines, and its text stays; a line that is a
	// paragraph's continuation opens no HTML block, so the second comment is an inline one.
	{"D2 a code span across lines whose second line opens no block",
		"a ``x\nthen <!-- y`` b <!-- gone -->\n", "a ``x\nthen <!-- y`` b \n"},
	{"a closer with no comment open stays", "an end marker (`-->`) and --> alone\n", "an end marker (`-->`) and --> alone\n"},
	{"an unmatched backtick is text", "a ` b <!-- gone --> c\n", "a ` b  c\n"},
	// An indented code block holds a comment as text, as a fenced one does.
	{"a comment inside an indented code block stays",
		"Para.\n\n    <!-- shown -->\n\nafter <!-- gone -->\n", "Para.\n\n    <!-- shown -->\n\nafter \n"},
	{"a comment opening before a fenced block and closing after it hides the block",
		"a\n<!--\n```go\nhidden()\n```\n-->\nb\n", "a\n\n\n\n\n\nb\n"},
	{"a comment opening before a list and closing after it hides the list",
		"a\n\n<!--\n- `session` — hidden\n- `person` — hidden\n-->\n\nb\n", "a\n\n\n\n\n\n\nb\n"},
	// An inline comment across lines inside a container: the comment's text goes, and each
	// line's container prefix (a block quote's `> `, a list item's indentation) stays.
	{"a multi-line inline comment in a block quote keeps the quote's prefix",
		"> a <!-- x\n> y --> b\n", "> a \n>  b\n"},
	{"a multi-line inline comment in a list item keeps the item's indentation",
		"- a <!-- x\n  y --> b\n", "- a \n   b\n"},
	// GitHub's tag filter writes the `<` of `<script>`, `<style>` and `<textarea>` as `&lt;`, so
	// the element is text on the page and a `<!--` inside it is a comment, which goes.
	{"a comment inside a script block is a comment on GitHub and goes",
		"<script>\nvar a = '<!-- gone -->';\n</script>\n", "<script>\nvar a = '';\n</script>\n"},
	{"a comment inside a style block is a comment on GitHub and goes",
		"<style>\n/* <!-- gone --> */\n</style>\n", "<style>\n/*  */\n</style>\n"},
	{"a comment inside a textarea block is a comment on GitHub and goes",
		"<textarea>\n<!-- gone -->\n</textarea>\n", "<textarea>\n\n</textarea>\n"},
	{"a comment after a script's end tag goes",
		"<script>\nx\n</script><!-- gone -->\n", "<script>\nx\n</script>\n"},
	// A tag whose name only begins with a filtered one is no filtered tag.
	{"a tag named like a filtered one is a tag",
		"<scripts>\n<!-- gone -->\n</scripts>\n", "<scripts>\n\n</scripts>\n"},
	// `<?` opens a bogus comment in the HTML specification's tokenizer, which a page shows no more
	// than a comment.
	{"a processing instruction block is a bogus comment and goes",
		"a\n\n<?php hidden(); ?>\n\nb\n", "a\n\n\n\nb\n"},
	{"no comment at all", "# Title\n\n- `x` — y\n", "# Title\n\n- `x` — y\n"},
}

// VisibleMarkdown takes out every HTML comment, on one line or across several, and keeps each
// comment's newlines, so a line number read from its result is the file's. A `<!--` inside a
// fenced code block or a code span is text a reader sees, and stays; a fenced block or a list that
// a comment opens before and closes after is hidden with it, as a page hides it.
func TestVisibleMarkdownTakesOutWhatNoReaderSees(t *testing.T) {
	for _, row := range exactRows {
		t.Run(row.name, func(t *testing.T) {
			got := VisibleMarkdown(row.doc)
			if got != row.want {
				t.Fatalf("VisibleMarkdown(%q) = %q, want %q", row.doc, got, row.want)
			}
			if strings.Count(got, "\n") != strings.Count(row.doc, "\n") {
				t.Fatalf("VisibleMarkdown(%q) has %d lines, the document %d", row.doc, strings.Count(got, "\n"), strings.Count(row.doc, "\n"))
			}
		})
	}
}

// markerRows are documents each marker word of which is hidden on the rendered page or shown
// there, by the CommonMark spec (0.31.2) with GitHub Flavored Markdown's extensions. The rows are
// the shapes a reader that approximated the spec line by line got wrong: a fence or a code span
// that CommonMark does not open or close, and a comment opener CommonMark reads as text.
var markerRows = []struct {
	name, doc     string
	hidden, shown []string
}{
	// An indented code block (four spaces or a tab) holds a line of backticks as text: it opens
	// no fence, so the paragraph after the block is prose.
	{"A1 a backtick line in an indented code block", "Para.\n\n    ```\n    code\n\nAfter <!-- PROBE1 --> text.\n", []string{"PROBE1"}, nil},
	{"A2 a backtick line in a tab-indented code block", "Para.\n\n\t```\n\tcode\n\nAfter <!-- PROBE2 --> text.\n", []string{"PROBE2"}, nil},
	// An indented code block cannot interrupt a paragraph: an indented run is a continuation line.
	{"A3 a 4-space backtick run continuing a paragraph", "Some text\n    ```\nmore text\n\nAfter <!-- PROBE3 --> text.\n", []string{"PROBE3"}, nil},
	{"A3t a 4-space tilde run continuing a paragraph", "Some text\n    ~~~\nmore text\n\nAfter <!-- PROBE31 --> text.\n", []string{"PROBE31"}, nil},
	// A list item's paragraph continues on a line indented past its content, as text.
	{"A4 a list item's continuation line indented past its content", "- item text\n      ```\n  more\n\nAfter <!-- PROBE4 --> text.\n", []string{"PROBE4"}, nil},
	// Four columns past a list item's content, after a blank line, is an indented code block.
	{"A5 an indented code block in a list item", "- item\n\n      ```\n      code\n\nAfter <!-- PROBE5 --> text.\n", []string{"PROBE5"}, nil},
	// With no false fence the bare fences read the right way: code keeps its comments, prose not.
	{"A6 bare fences after an indented code block", "    ```\n\nprose <!-- PROSEA6 -->\n\n```\ncode <!-- CODEA6 -->\n```\n\nprose <!-- PROSEB6 -->\n\n```\ncode <!-- CODEB6 -->\n```\n\nprose <!-- PROSEC6 -->\n",
		[]string{"PROSEA6", "PROSEB6", "PROSEC6"}, []string{"CODEA6", "CODEB6"}},
	// A closing fence may be indented three spaces at most; four makes it the block's content.
	{"A7 a closer indented four spaces is content", "```\ncode\n    ```\nstill code <!-- CODE7 -->\n```\n\nAfter <!-- PROBE7 --> text.\n", []string{"PROBE7"}, []string{"CODE7"}},
	// A fence a list item opens ends with the item: at a line indented less than its content
	// after a blank line, or at the next item.
	{"A8 a list item's fence ends when the item ends", "- item\n\n  ```\n  code\n\nAfter <!-- PROBE8 --> text.\n", []string{"PROBE8"}, nil},
	{"A9 a list item's fence ends at the next item", "- a\n  ```\n  code\n- b <!-- PROBE9 --> text\n", []string{"PROBE9"}, nil},
	// A backtick fence's info string may not hold a backtick, so that line is no fence.
	{"A12 a backtick info string holding a backtick", "``` a`b\n<!-- PROBE12 -->\n```\n", []string{"PROBE12"}, nil},
	// A fence at column 0 after an item's fence ends the item and opens a fence of its own.
	{"A20 a fence at column 0 after an item's fence opens a new fence", "- a\n\n  ```\n  code\n```\nafter <!-- CODE20 -->\n", nil, []string{"CODE20"}},
	// A backslash-escaped backtick is a literal backtick and opens no code span.
	{"B1 an escaped backtick opens no code span", "a \\`<!-- PROBE21 -->` b\n", []string{"PROBE21"}, nil},
	// A code span runs within one paragraph; a list item or a heading is a block of its own.
	{"B6 no code span across a list item boundary", "- one ` b\n- two <!-- PROBE26 --> `x`\n", []string{"PROBE26"}, nil},
	{"B7 no code span across a heading", "# A `b\n<!-- PROBE27 -->`\n", []string{"PROBE27"}, nil},
	// An opener in the middle of a line is an inline comment only when it closes in its
	// paragraph; otherwise it is text, and what follows stays.
	{"C1 an inline opener whose closer is past the paragraph", "Text <!-- x\n\nVisible SHOWN31\n\n-->\n", nil, []string{"SHOWN31"}},
	{"C2 an inline opener that never closes", "Text <!-- x\n\nVisible SHOWN32\n", nil, []string{"SHOWN32"}},
	// `<!-->` is a whole, empty comment.
	{"C4 an empty comment", "a <!--> SHOWN34 <!-- HIDDEN34 --> b\n", []string{"HIDDEN34"}, []string{"SHOWN34"}},
	// A line that opens with `<!--` starts an HTML block, which ends the paragraph before it,
	// so a code span cannot run into it.
	{"D1 an odd backtick before a commented-out line", "Run it with a ` prefix.\n<!-- The flag `HIDE41` is required. -->\n", []string{"HIDE41"}, nil},
	// A `<!--` comment an HTML block leaves open stays open on the page: Markdown text renders
	// `-->` as `--&gt;`, so only a later raw HTML node's `-->` ends it, or the end of the page.
	{"E1 a comment block whose first line opens a second comment", "<!-- note --> <!--\nHIDE51 paragraph\n-->\n\nAfter TAIL51\n", []string{"HIDE51", "TAIL51"}, nil},
	{"E2 a comment block whose last line opens a second comment", "<!-- a\n--> <!-- b\n\nHIDE52\n\n-->\n\nTAIL52\n", []string{"HIDE52", "TAIL52"}, nil},
	{"E3 a div block that leaves a comment open", "<div>\n<!-- open\n</div>\n\nHIDE53\n", []string{"HIDE53"}, nil},
	{"F2 a block quote that ends an open comment block", "> <!--\n> HIDE62\n\nTAIL62\n\n-->\n\nAFTER62\n", []string{"HIDE62", "TAIL62", "AFTER62"}, nil},
	{"F3 a list item comment with an under-indented line", "- item\n  <!-- TODO\nHIDE63 note\n-->\n- next NEXT63\n", []string{"HIDE63", "NEXT63"}, nil},
	{"F8 a lazy block-quote line after a comment block", "> <!--\nHIDE68 lazy\n> -->\nSHOWN68\n", []string{"HIDE68", "SHOWN68"}, nil},
	{"E13 a later block's closer ends the open comment", "> <!--\n> HIDE69\n\nHIDE70\n\n<!-- -->\n\nSHOWN70\n", []string{"HIDE69", "HIDE70"}, []string{"SHOWN70"}},
	{"E14 a later inline comment's closer ends the open comment", "- <!--\n  HIDE76\n\nHIDE77 <!-- x --> SHOWN77\n", []string{"HIDE76", "HIDE77"}, []string{"SHOWN77"}},
	// `--!>` ends a comment as `-->` does.
	{"E5 --!> ends a comment", "<!-- a --!> SHOWN55\n\nSHOWN55b\n", nil, []string{"SHOWN55", "SHOWN55b"}},
	// GitHub's tag filter: a comment inside `<textarea>` or `<script>` is a comment, and one the
	// element leaves open runs on.
	{"E10 a textarea block's comment", "<textarea>\n<!-- HIDE60 -->\n</textarea>\n", []string{"HIDE60"}, nil},
	{"E12 a script block that leaves a comment open", "<script>\n// <!-- HIDE60s\n</script>\n\nHIDE61s\n", []string{"HIDE60s", "HIDE61s"}, nil},
	// What the HTML specification's tokenizer reads as a comment that is not `<!--`, and a
	// DOCTYPE, which a page's body ignores.
	{"G1 an inline processing instruction", "Text <?pi HIDE71 ?> SHOWN71\n", []string{"HIDE71"}, []string{"SHOWN71"}},
	{"G2 an inline CDATA section", "Text <![CDATA[ HIDE72 ]]> SHOWN72\n", []string{"HIDE72"}, []string{"SHOWN72"}},
	{"G3 an inline declaration", "Text <!X HIDE73> SHOWN73\n", []string{"HIDE73"}, []string{"SHOWN73"}},
	{"G4 a processing-instruction block", "<?php\nHIDE74\n?>\n\nSHOWN74\n", []string{"HIDE74"}, []string{"SHOWN74"}},
	{"G5 a DOCTYPE block", "<!DOCTYPE HIDE75>\n\nSHOWN75\n", []string{"HIDE75"}, []string{"SHOWN75"}},
	// GitHub reads footnotes: a footnote definition's indented continuation is its content, not code.
	{"H9 a comment in a footnote's indented continuation", "Text[^1].\n\n[^1]: Note.\n\n    <!-- HIDE89 -->\n\nSHOWN89\n", []string{"HIDE89"}, []string{"SHOWN89"}},
	{"H10 a comment block in a footnote's continuation paragraph", "Text[^1].\n\n[^1]: Note.\n\n    More SHOWN90.\n    <!--\n    HIDE90\n    -->\n", []string{"HIDE90"}, []string{"SHOWN90"}},
}

// VisibleMarkdown keeps exactly the marker words the page shows.
func TestVisibleMarkdownReadsFencesSpansAndCommentsAsCommonMarkDoes(t *testing.T) {
	for _, row := range markerRows {
		t.Run(row.name, func(t *testing.T) {
			got := VisibleMarkdown(row.doc)
			for _, word := range row.hidden {
				if strings.Contains(got, word) {
					t.Errorf("%s: the page hides it, VisibleMarkdown keeps it: %q", word, got)
				}
			}
			for _, word := range row.shown {
				if !strings.Contains(got, word) {
					t.Errorf("%s: the page shows it, VisibleMarkdown takes it out: %q", word, got)
				}
			}
			if strings.Count(got, "\n") != strings.Count(row.doc, "\n") {
				t.Errorf("VisibleMarkdown(%q) has %d lines, the document %d", row.doc, strings.Count(got, "\n"), strings.Count(row.doc, "\n"))
			}
		})
	}
}

// removable is, for each byte of source, whether a comment may take it, read from the parse on its
// own: each segment of inline raw HTML and each line of an HTML block, its closing line included,
// and every byte after an HTML block whose text opens a `<!--` after its last `-->`, which may
// leave a comment open to run on. A block quote's `> ` and a list item's indentation are in no
// segment.
func removable(source []byte) []bool {
	allowed := make([]bool, len(source))
	allow := func(from, to int) {
		for at := from; at < to; at++ {
			allowed[at] = true
		}
	}
	root := markdown.Parser().Parse(text.NewReader(source))
	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.HTMLBlock:
			var block []byte
			end := 0
			segments := []text.Segment{}
			for i := range n.Lines().Len() {
				segments = append(segments, n.Lines().At(i))
			}
			if n.HasClosure() {
				segments = append(segments, n.ClosureLine)
			}
			for _, s := range segments {
				allow(s.Start, s.Stop)
				block = append(block, source[s.Start:s.Stop]...)
				end = max(end, s.Stop)
			}
			if opener := strings.LastIndex(string(block), "<!--"); opener >= 0 && strings.LastIndex(string(block), "-->") < opener {
				allow(end, len(source))
			}
		case *ast.RawHTML:
			for i := range n.Segments.Len() {
				s := n.Segments.At(i)
				allow(s.Start, s.Stop)
			}
		}
		return ast.WalkContinue, nil
	})
	return allowed
}

// inContainers is doc as the text of a block quote and as the text of a list item: each line gets
// the container's prefix, the bytes a removal that spanned lines would take with it.
func inContainers(doc string) map[string]string {
	var quote, item strings.Builder
	for i, line := range strings.SplitAfter(doc, "\n") {
		if line == "" {
			continue
		}
		quote.WriteString("> " + line)
		if i == 0 {
			item.WriteString("- " + line)
		} else {
			item.WriteString("  " + line)
		}
	}
	return map[string]string{"in a block quote": quote.String(), "in a list item": item.String()}
}

// Removing comments takes out no byte outside what a comment may take, whatever the shape: over
// every row of both tables, as written and inside a block quote and a list item, each range
// VisibleMarkdown takes out lies in a raw HTML node's segments or after an HTML block that may
// leave a comment open (removable), the ranges do not overlap, and the result is the document
// without exactly their bytes, newlines kept. A removal that took a container's prefix of a closed
// comment, or any byte between two of its lines, fails here.
func TestVisibleMarkdownRemovesOnlyCommentBytes(t *testing.T) {
	docs := map[string]string{}
	for _, row := range exactRows {
		docs[row.name] = row.doc
	}
	for _, row := range markerRows {
		docs[row.name] = row.doc
	}
	wrapped := map[string]string{}
	for name, doc := range docs {
		for where, in := range inContainers(doc) {
			wrapped[name+", "+where] = in
		}
	}
	for name, doc := range wrapped {
		docs[name] = doc
	}
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			source := []byte(doc)
			allowed := removable(source)
			removed, end := 0, 0
			for _, r := range commentRanges(source) {
				if r[0] < end {
					t.Fatalf("ranges overlap at byte %d of %q", r[0], doc)
				}
				for at := r[0]; at < r[1]; at++ {
					if !allowed[at] {
						t.Fatalf("byte %d (%q) of %q is taken out, and it is in no comment's segment", at, doc[at], doc)
					}
					if doc[at] != '\n' {
						removed++
					}
				}
				end = r[1]
			}
			got := VisibleMarkdown(doc)
			if len(got) != len(doc)-removed || strings.Count(got, "\n") != strings.Count(doc, "\n") {
				t.Fatalf("VisibleMarkdown(%q) = %q, %d bytes; want %d bytes and the document's newlines", doc, got, len(got), len(doc)-removed)
			}
		})
	}
}
