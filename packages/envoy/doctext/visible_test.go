package doctext

import (
	"strings"
	"testing"
)

// VisibleMarkdown takes out every HTML comment, on one line or across several, and keeps each
// comment's newlines, so a line number read from its result is the file's. A `<!--` inside a
// fenced code block or a code span is text a reader sees, and stays; a fenced block or a list that
// a comment opens before and closes after is hidden with it, as a page hides it. Each expected
// result is CommonMark's reading of the document.
func TestVisibleMarkdownTakesOutWhatNoReaderSees(t *testing.T) {
	for _, row := range []struct{ name, doc, want string }{
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
		{"no comment at all", "# Title\n\n- `x` — y\n", "# Title\n\n- `x` — y\n"},
	} {
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

// Each marker word is hidden on the rendered page or shown there, by the CommonMark spec (0.31.2)
// with GitHub Flavored Markdown's extensions, and VisibleMarkdown keeps exactly the shown ones. The
// rows are the shapes a reader that approximated the spec line by line got wrong: a fence or a code
// span that CommonMark does not open or close, and a comment opener CommonMark reads as text.
func TestVisibleMarkdownReadsFencesSpansAndCommentsAsCommonMarkDoes(t *testing.T) {
	for _, row := range []struct {
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
	} {
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
