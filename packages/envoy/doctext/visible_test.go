package doctext

import (
	"strings"
	"testing"
)

// VisibleMarkdown takes out every HTML comment, on one line or across several, and keeps each
// comment's newlines, so a line number read from its result is the file's. A `<!--` inside a
// fenced code block or a code span is text a reader sees, and stays; a fenced block or a list that
// a comment opens before and closes after is hidden with it, as a page hides it.
func TestVisibleMarkdownTakesOutWhatNoReaderSees(t *testing.T) {
	for _, row := range []struct{ name, doc, want string }{
		{"a comment on one line", "before <!-- hidden --> after\n", "before  after\n"},
		{"a comment across lines keeps its newlines", "a\n<!--\nhidden\n-->\nb\n", "a\n\n\n\nb\n"},
		{"a comment across a blank line", "a\n\n<!--\nhidden\n\nstill hidden\n-->\nb\n", "a\n\n\n\n\n\n\nb\n"},
		{"two comments on one line", "x<!-- 1 -->y<!-- 2 -->z\n", "xyz\n"},
		{"text after a comment's end, with a comment of its own", "<!--\nh\n--> seen <!-- h -->\n", "\n\n seen \n"},
		{"a comment that never closes runs to the end", "a\n<!-- hidden\nhidden\n", "a\n\n\n"},
		{"a comment inside a fenced code block stays", "```html\n<!-- shown -->\n```\n", "```html\n<!-- shown -->\n```\n"},
		{"a fence of tildes", "~~~\n<!-- shown\n~~~\nafter <!-- gone -->\n", "~~~\n<!-- shown\n~~~\nafter \n"},
		{"a code span holding an opener stays", "the `<!-- legion:` footer\n", "the `<!-- legion:` footer\n"},
		{"a code span across lines", "a ``x\n<!-- y`` b <!-- gone -->\n", "a ``x\n<!-- y`` b \n"},
		{"a closer with no comment open stays", "an end marker (`-->`) and --> alone\n", "an end marker (`-->`) and --> alone\n"},
		{"an unmatched backtick is text", "a ` b <!-- gone --> c\n", "a ` b  c\n"},
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
