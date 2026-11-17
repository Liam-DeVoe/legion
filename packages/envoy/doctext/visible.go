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
	"bytes"
	"cmp"
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
	"golang.org/x/net/html"
)

// markdown parses a document as GitHub renders one: CommonMark (0.31.2, which goldmark
// implements) with GitHub Flavored Markdown's extensions and its footnotes. A parser is safe for
// concurrent use.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM, extension.Footnote))

// VisibleMarkdown is doc as a reader of the page GitHub renders sees it, for a check that holds a
// guide's text to something: every HTML comment is gone, so a passage moved inside one no longer
// counts as the guide stating it. A check that read the raw bytes passed while the page showed
// nothing, since a comment is in the file and on no page.
//
// Neither grammar is written here. goldmark parses doc as CommonMark with GitHub Flavored
// Markdown's extensions and footnotes, which says which bytes are raw HTML: an HTML block, or raw
// HTML inline. golang.org/x/net/html's tokenizer, which follows the HTML specification's
// tokenizer, reads each of those, and every comment or DOCTYPE token it gives is taken out: a
// comment, the empty `<!-->`, one that `--!>` ends, a bogus comment such as `<?php … ?>`,
// `<![CDATA[ … ]]>` or `<!X …>`, and a DOCTYPE, none of which a page shows. Before it reads a
// piece, the `<` of each tag GitHub's tag filter disallows (`<script>`, `<style>`, `<textarea>`,
// `<title>`, `<xmp>`, `<iframe>`, `<noembed>`, `<noframes>`, `<plaintext>`) is read as text, as
// GitHub writes it `&lt;`, so a comment inside one of those is a comment, as on GitHub.
//
// A `<!--` comment still open at the end of an HTML block stays open on the page: the Markdown
// after it renders `-->` as `--&gt;`, which closes nothing. So it is taken out through the first
// end a later HTML block or inline raw HTML gives it (`-->` or `--!>`, as the tokenizer reads
// one), and to the end of the document when none does; that holds when a list item or a block
// quote is what ended the block. Every other byte stays as written: a `<!--` in a fenced or
// indented code block or in a code span, where goldmark reads no raw HTML, and one goldmark reads
// as text, in the middle of a line whose comment does not close in its paragraph. Each newline
// stays too, so a line number a caller reports from the result is the line in the file, and the
// prefix a block quote or a list item puts before a line of a closed comment stays, since it is
// no byte of the HTML.
//
// Not modelled:
//   - a tab between a block quote's `>` and a `<!--`: goldmark opens no HTML block there, where
//     GitHub and CommonMark do, so the comment is read as inline text and stays;
//   - the documentation site's reading where it differs from GitHub's: it keeps the text of a
//     `<script>`, `<style>` or `<textarea>`, and it ends a comment its block leaves open with that
//     block. In both, this takes out text the site shows, so a check fails rather than passes.
func VisibleMarkdown(doc string) string {
	hidden := commentRanges([]byte(doc))
	if len(hidden) == 0 {
		return doc
	}
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

// piece is the bytes of one raw HTML node, an HTML block or inline raw HTML, without the prefix a
// block quote or a list item puts before each of its lines, and each byte's offset in the source.
type piece struct {
	bytes   []byte
	offsets []int
	block   bool
}

// commentRanges are the source ranges VisibleMarkdown takes out, in order of their start. A range
// of a closed comment lies in its raw HTML node's own bytes, so no container's byte is in one; a
// comment an HTML block leaves open runs on from the block's end to where a later node ends it.
func commentRanges(source []byte) [][2]int {
	pieces := rawHTML(source)
	var hidden [][2]int
	open := -1 // the source offset an open comment runs on from, or -1 when none is open
	for _, p := range pieces {
		if len(p.bytes) == 0 {
			continue
		}
		from := 0
		if open >= 0 {
			end, closed := openCommentEnd(p.bytes)
			hidden = append(hidden, [2]int{open, p.offsets[0]})
			hidden = append(hidden, sourceRanges(p.offsets[:end])...)
			if !closed {
				open = p.offsets[len(p.offsets)-1] + 1
				continue
			}
			open, from = -1, end
		}
		ranges, leftOpen := htmlComments(p.bytes[from:], p.offsets[from:])
		hidden = append(hidden, ranges...)
		if leftOpen && p.block {
			open = p.offsets[len(p.offsets)-1] + 1
		}
	}
	if open >= 0 && open < len(source) {
		hidden = append(hidden, [2]int{open, len(source)})
	}
	slices.SortFunc(hidden, func(a, b [2]int) int { return cmp.Compare(a[0], b[0]) })
	return hidden
}

// rawHTML is every raw HTML node of source as goldmark parses it, in the order of the source:
// goldmark moves footnote definitions to the end of its tree, so the walk's order is not it.
func rawHTML(source []byte) []piece {
	var pieces []piece
	read := func(segments []text.Segment, block bool) {
		p := piece{block: block}
		for _, s := range segments {
			for at := s.Start; at < s.Stop; at++ {
				p.bytes = append(p.bytes, source[at])
				p.offsets = append(p.offsets, at)
			}
		}
		pieces = append(pieces, p)
	}
	root := markdown.Parser().Parse(text.NewReader(source))
	// The walk returns no error of its own: the function below returns none.
	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.HTMLBlock:
			lines := n.Lines()
			segments := make([]text.Segment, 0, lines.Len()+1)
			for i := range lines.Len() {
				segments = append(segments, lines.At(i))
			}
			if n.HasClosure() {
				segments = append(segments, n.ClosureLine)
			}
			read(segments, true)
			return ast.WalkSkipChildren, nil
		case *ast.RawHTML:
			segments := make([]text.Segment, 0, n.Segments.Len())
			for i := range n.Segments.Len() {
				segments = append(segments, n.Segments.At(i))
			}
			read(segments, false)
		}
		return ast.WalkContinue, nil
	})
	slices.SortStableFunc(pieces, func(a, b piece) int {
		return cmp.Compare(first(a.offsets), first(b.offsets))
	})
	return pieces
}

func first(offsets []int) int {
	if len(offsets) == 0 {
		return -1
	}
	return offsets[0]
}

// htmlComments are the source ranges of the comment and DOCTYPE tokens golang.org/x/net/html's
// tokenizer reads in one piece of raw HTML, after GitHub's tag filter, and whether the piece ends
// inside a `<!--` comment that does not close. The tokenizer's raw bytes partition what it reads,
// so a token's offset is the sum of the lengths before it.
func htmlComments(raw []byte, offsets []int) (ranges [][2]int, open bool) {
	filtered := bytes.Clone(raw)
	tagFilter(filtered)
	tokens := html.NewTokenizer(bytes.NewReader(filtered))
	for read := 0; ; {
		kind := tokens.Next()
		if kind == html.ErrorToken {
			return ranges, open
		}
		token := tokens.Raw()
		if (kind == html.CommentToken || kind == html.DoctypeToken) && len(token) > 0 {
			ranges = append(ranges, sourceRanges(offsets[read:read+len(token)])...)
			open = kind == html.CommentToken && bytes.HasPrefix(token, []byte("<!--")) &&
				!bytes.HasSuffix(token, []byte("-->")) && !bytes.HasSuffix(token, []byte("--!>"))
		} else {
			open = false
		}
		read += len(token)
	}
}

// openCommentEnd is how many bytes of raw an already open comment takes, read by the tokenizer as
// the rest of that comment, and whether it ends in raw.
func openCommentEnd(raw []byte) (int, bool) {
	opened := "<!-- " // the tokenizer's comment state, with no byte of raw read yet
	tokens := html.NewTokenizer(bytes.NewReader(append([]byte(opened), raw...)))
	if tokens.Next() != html.CommentToken {
		return len(raw), false
	}
	token := tokens.Raw()
	end := len(token) - len(opened)
	if end >= len(raw) && !bytes.HasSuffix(token, []byte("-->")) && !bytes.HasSuffix(token, []byte("--!>")) {
		return len(raw), false
	}
	return end, true
}

// sourceRanges are the source offsets as ranges, one for each run of consecutive offsets: two
// lines of a piece are apart where a container's prefix stands between them.
func sourceRanges(offsets []int) [][2]int {
	if len(offsets) == 0 {
		return nil
	}
	var ranges [][2]int
	start := offsets[0]
	for i := 1; i < len(offsets); i++ {
		if offsets[i] != offsets[i-1]+1 {
			ranges = append(ranges, [2]int{start, offsets[i-1] + 1})
			start = offsets[i]
		}
	}
	return append(ranges, [2]int{start, offsets[len(offsets)-1] + 1})
}

// filteredTags are the tags GitHub Flavored Markdown's tag filter disallows (the GFM spec, section
// 6.11): GitHub writes the `<` of each as `&lt;`, so the tag is text on the page.
var filteredTags = []string{"title", "textarea", "style", "xmp", "iframe", "noembed", "noframes", "script", "plaintext"}

// tagFilter turns the `<` of each start or end tag in raw that the tag filter disallows into a
// byte that opens no tag, as the GFM spec matches one: `<`, an optional `/`, the tag's name in any
// case, then whitespace, `>` or `/>`.
func tagFilter(raw []byte) {
	for i := range raw {
		if raw[i] != '<' {
			continue
		}
		name := i + 1
		if name < len(raw) && raw[name] == '/' {
			name++
		}
		for _, tag := range filteredTags {
			after := name + len(tag)
			if after >= len(raw) || !strings.EqualFold(string(raw[name:after]), tag) {
				continue
			}
			switch c := raw[after]; {
			case c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r' || c == '>',
				c == '/' && after+1 < len(raw) && raw[after+1] == '>':
				raw[i] = '&'
			}
			break
		}
	}
}
