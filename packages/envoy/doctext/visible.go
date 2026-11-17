// Package doctext reads a document as its reader sees it, for a check that holds a guide's text
// to something: a test that compares a guide with a constant, a committed copy or a command
// reads the guide through VisibleMarkdown rather than its raw bytes.
//
// It is public, as testnatsimages is, so that packages/daemon, a module outside
// github.com/sjawhar/envoy, reads its guides through this one helper: Go's internal-package rule
// keeps that module from importing an internal package of this one.
// packages/daemon/internal/textcheck's envoy_helper_copies_test.go fails a copy of it there.
package doctext

import (
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// markdown parses a document as GitHub and the documentation site render one: CommonMark (0.31.2,
// which goldmark implements) with GitHub Flavored Markdown's extensions. A parser is safe for
// concurrent use.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// VisibleMarkdown is doc as a reader of the rendered page sees it, for a check that holds a
// guide's text to something: every HTML comment is gone, so a passage moved inside one no longer
// counts as the guide stating it. A check that read the raw bytes passed while the page showed
// nothing, since a comment is in the file and on no page.
//
// It parses doc as CommonMark with GitHub Flavored Markdown's extensions, and takes out exactly the
// bytes a browser reads as comment text: an inline comment (raw HTML that is a comment), and each
// comment inside an HTML block, as an HTML parser ends it (`-->`, or the empty `<!-->` and
// `<!--->`; a comment a block opens and does not close runs to the end of that block, which for a
// block that opens with `<!--` is the line holding `-->`, or the end of the document). Every other
// byte stays as written, so a `<!--` in a fenced or indented code block, or in a code span, is
// text a reader sees, and so is a `<!--` the parser reads as text (one in the middle of a line
// whose comment does not close in its paragraph). Each newline of a comment stays too, so a line
// number a caller reports from the result is the line in the file.
//
// Two shapes are not modelled. A comment inside a `<script>`, `<style>` or `<textarea>` block is
// that element's text, and stays. And a comment that an HTML block other than a comment opens and
// does not close hides, on a page, what follows up to the next raw `-->`; this takes out the rest
// of that block only.
func VisibleMarkdown(doc string) string {
	source := []byte(doc)
	var hidden [][2]int
	root := markdown.Parser().Parse(text.NewReader(source))
	err := ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.HTMLBlock:
			hidden = append(hidden, blockComments(n, source)...)
			return ast.WalkSkipChildren, nil
		case *ast.RawHTML:
			if r, ok := inlineComment(n, source); ok {
				hidden = append(hidden, r)
			}
		}
		return ast.WalkContinue, nil
	})
	if err != nil || len(hidden) == 0 {
		// The walk returns no error of its own: the function above returns none.
		return doc
	}
	slices.SortFunc(hidden, func(a, b [2]int) int { return a[0] - b[0] })
	var out strings.Builder
	out.Grow(len(doc))
	at := 0
	for _, r := range hidden {
		if r[0] < at {
			r[0] = at
		}
		if r[1] <= r[0] {
			continue
		}
		out.WriteString(doc[at:r[0]])
		out.WriteString(strings.Repeat("\n", strings.Count(doc[r[0]:r[1]], "\n")))
		at = r[1]
	}
	out.WriteString(doc[at:])
	return out.String()
}

// inlineComment is the source range of n when n is an inline comment: raw HTML whose text opens
// with `<!--`. A comment that runs across lines is one range, from its first byte to its last.
func inlineComment(n *ast.RawHTML, source []byte) ([2]int, bool) {
	segments := n.Segments
	if segments == nil || segments.Len() == 0 {
		return [2]int{}, false
	}
	first, last := segments.At(0), segments.At(segments.Len()-1)
	if !strings.HasPrefix(string(source[first.Start:first.Stop]), "<!--") {
		return [2]int{}, false
	}
	return [2]int{first.Start, last.Stop}, true
}

// blockComments are the source ranges of the comments inside an HTML block, read as an HTML
// parser reads them over the block's own lines (without the prefix a block quote or a list item
// puts before each). A `<script>`, `<style>` or `<textarea>` block holds text, not comments.
func blockComments(n *ast.HTMLBlock, source []byte) [][2]int {
	lines := n.Lines()
	var segments []text.Segment
	for i := range lines.Len() {
		segments = append(segments, lines.At(i))
	}
	if n.HasClosure() {
		segments = append(segments, n.ClosureLine)
	}
	if len(segments) == 0 {
		return nil
	}
	// The block's text, and the source offset of each of its bytes.
	var html []byte
	var offsets []int
	for _, s := range segments {
		for at := s.Start; at < s.Stop; at++ {
			html = append(html, source[at])
			offsets = append(offsets, at)
		}
	}
	if n.HTMLBlockType == ast.HTMLBlockType1 {
		opening := strings.ToLower(strings.TrimLeft(string(html), " \t"))
		for _, raw := range []string{"<script", "<style", "<textarea"} {
			if strings.HasPrefix(opening, raw) {
				return nil
			}
		}
	}
	var ranges [][2]int
	for _, c := range htmlComments(string(html)) {
		// A comment's bytes map to source ranges that may skip a container's prefix between two
		// lines; each run of consecutive source offsets is one range.
		start := offsets[c[0]]
		for i := c[0] + 1; i < c[1]; i++ {
			if offsets[i] != offsets[i-1]+1 {
				ranges = append(ranges, [2]int{start, offsets[i-1] + 1})
				start = offsets[i]
			}
		}
		ranges = append(ranges, [2]int{start, offsets[c[1]-1] + 1})
	}
	return ranges
}

// htmlComments are the byte ranges of html an HTML parser reads as comments: from `<!--` to the
// `-->` that ends it, `<!-->` and `<!--->` being whole empty comments, and a comment with no end
// running to the end of html.
func htmlComments(html string) [][2]int {
	var ranges [][2]int
	for i := 0; i < len(html); {
		at := strings.Index(html[i:], "<!--")
		if at < 0 {
			break
		}
		start := i + at
		body := start + len("<!--")
		switch {
		case strings.HasPrefix(html[body:], ">"):
			ranges = append(ranges, [2]int{start, body + 1})
			i = body + 1
			continue
		case strings.HasPrefix(html[body:], "->"):
			ranges = append(ranges, [2]int{start, body + 2})
			i = body + 2
			continue
		}
		end := strings.Index(html[body:], "-->")
		if end < 0 {
			ranges = append(ranges, [2]int{start, len(html)})
			break
		}
		ranges = append(ranges, [2]int{start, body + end + len("-->")})
		i = body + end + len("-->")
	}
	return ranges
}
