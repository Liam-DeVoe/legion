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
	"regexp"
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"golang.org/x/net/html"
)

// markdown parses a document close to how GitHub renders one. goldmark implements CommonMark
// 0.31.2, with GitHub Flavored Markdown's extensions and footnotes here; GitHub renders with
// cmark-gfm 0.29.0.gfm.13 (github/cmark-gfm at 587a12bb54, the version the cmarkgfm binding
// bundles, which agreed with GitHub's renderer on every case read), whose reading differs in a few
// places. Two of them are cmark-gfm's here, read from its source: where an HTML block of the sixth
// kind opens (cmarkGFMBlock), and how an inline `<!` reads (cmarkGFMRawHTML). The rest are named
// under VisibleMarkdown's "Not modelled". A parser is safe for concurrent use.
var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM, extension.Footnote),
	goldmark.WithParserOptions(parser.WithBlockParsers(util.Prioritized(cmarkGFMBlock{}, 899))),
)

// cmarkGFMBlock opens an HTML block of the sixth kind where cmark-gfm opens one: at a line that
// starts, after at most three spaces, with `<` or `</`, one of cmark-gfm's block tag names in any
// case, then a space character (space, tab, vertical tab, form feed, carriage return or line feed)
// or `>` or `/>` (src/scanners.re:187, with `blocktagname` at :40 and `spacechar` at :32).
// goldmark's own start (parser/html_block.go:94 at v1.8.6) takes only a space, `>` or `/>` after
// the name, and its list (:13) lacks `source`. Such a block ends at a blank line and may interrupt
// a paragraph, as goldmark's own blocks of the kind do, so everything after the opening line is
// goldmark's HTML block parser's. It runs at priority 899, before goldmark's (900).
type cmarkGFMBlock struct{}

var cmarkGFMBlockStart = regexp.MustCompile(`(?i)^ {0,3}</?(?:address|article|aside|base|basefont|` +
	`blockquote|body|caption|center|col|colgroup|dd|details|dialog|dir|div|dl|dt|fieldset|figcaption|` +
	`figure|footer|form|frame|frameset|h1|h2|h3|h4|h5|h6|head|header|hr|html|iframe|legend|li|link|` +
	`main|menu|menuitem|nav|noframes|ol|optgroup|option|p|param|section|source|title|summary|table|` +
	`tbody|td|tfoot|th|thead|tr|track|ul)(?:[ \t\v\f\r\n]|/?>)`)

func (cmarkGFMBlock) Trigger() []byte { return []byte{'<'} }

func (cmarkGFMBlock) Open(_ ast.Node, reader text.Reader, _ parser.Context) (ast.Node, parser.State) {
	line, segment := reader.PeekLine()
	if !cmarkGFMBlockStart.Match(line) {
		return nil, parser.NoChildren
	}
	node := ast.NewHTMLBlock(ast.HTMLBlockType6)
	reader.AdvanceToEOL()
	node.Lines().Append(segment)
	return node, parser.NoChildren
}

func (cmarkGFMBlock) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	return parser.NewHTMLBlockParser().Continue(node, reader, pc)
}

func (cmarkGFMBlock) Close(node ast.Node, reader text.Reader, pc parser.Context) {
	parser.NewHTMLBlockParser().Close(node, reader, pc)
}

func (cmarkGFMBlock) CanInterruptParagraph() bool { return true }

func (cmarkGFMBlock) CanAcceptIndentedLine() bool { return false }

// cmarkGFMCommentEnd is where cmark-gfm's comment scanner can end a comment
// (src/scanners.re:57, `htmlcomment`), read on the text after its `<!--`: at a run of dashes, 2
// more than a multiple of 3, before a `>`. Its `"--" [^\x00>]` alternative takes three dashes at a
// time and the closing `-->` takes the last two, so a run of 2, 5, 8 and so on ends a comment and
// a run of any other length does not. The run begins at that text's first byte, where the `<!--`
// it opened stands, or after a byte that is no dash.
var cmarkGFMCommentEnd = regexp.MustCompile(`(?:^|[^-])(?:---)*-->`)

// cmarkGFMRawHTML is whether GitHub writes n, an inline raw HTML node goldmark reads with text raw,
// as raw HTML: when it does not, GitHub writes it as text, which hides nothing and ends no open
// comment. cmark-gfm reads inline raw HTML as goldmark does but at a `<!` (src/inlines.c:940-980),
// and the three readings below are its (a declaration with no space after its name is under
// VisibleMarkdown's "Not modelled"):
//   - `<!-->` and `<!--->` are comments to both (inlines.c:943-947);
//   - another `<!--` opens a comment that cmark-gfm's scanner (inlines.c:949) ends at the first
//     cmarkGFMCommentEnd after it, which may lie past goldmark's end, the first `-->`, anywhere
//     later in the inline text of n's block. With such an end GitHub writes the whole span as raw
//     HTML, and the page's comment ends where goldmark's does, since an HTML parser ends a comment
//     at its first `-->`; with none, the `<` is text;
//   - once a `<!--` in a block's inline text finds no end, raw HTML or a text `<!--`
//     (markTextOpener), cmark-gfm reads no later `<!` in that text as raw HTML: no comment, CDATA
//     section or declaration (FLAG_SKIP_HTML_COMMENT, inlines.c:940 and :952-955). noBang holds
//     those blocks.
func cmarkGFMRawHTML(n *ast.RawHTML, raw, source []byte, noBang map[ast.Node]bool) bool {
	if !bytes.HasPrefix(raw, []byte("<!")) {
		return true
	}
	block := inlineBlock(n)
	if noBang[block] {
		return false
	}
	inner, ok := bytes.CutPrefix(raw, []byte("<!--"))
	if !ok || bytes.HasPrefix(inner, []byte(">")) || bytes.HasPrefix(inner, []byte("->")) {
		return true
	}
	if cmarkGFMCommentEnd.Match(inner) ||
		commentEndAfter(block, n.Segments.At(n.Segments.Len()-1).Stop, source) {
		return true
	}
	noBang[block] = true
	return false
}

// markTextOpener records in noBang the block of n, inline text, when n holds a `<!--` that
// cmark-gfm's scan finds no end for: one that no backslash escapes, outside a code span, which
// cmark-gfm reads as a `<` to scan (inlines.c:913), and that neither opens `<!-->` or `<!--->` nor
// has a cmarkGFMCommentEnd after it.
func markTextOpener(n *ast.Text, source []byte, noBang map[ast.Node]bool) {
	if parent := n.Parent(); parent == nil || parent.Kind() == ast.KindCodeSpan {
		return
	}
	block := inlineBlock(n)
	segment := n.Segment
	for at := segment.Start; at < segment.Stop; {
		i := bytes.Index(source[at:segment.Stop], []byte("<!--"))
		if i < 0 {
			return
		}
		opener := at + i
		slashes := 0
		for p := opener - 1; p >= 0 && source[p] == '\\'; p-- {
			slashes++
		}
		next := source[opener+len("<!--"):]
		if slashes%2 == 0 && !bytes.HasPrefix(next, []byte(">")) && !bytes.HasPrefix(next, []byte("->")) &&
			!commentEndAfter(block, opener+len("<!--"), source) {
			noBang[block] = true
			return
		}
		at = opener + 1
	}
}

// inlineBlock is the block whose inline text holds n, an inline node.
func inlineBlock(n ast.Node) ast.Node {
	block := n.Parent()
	for block != nil && block.Type() != ast.TypeBlock {
		block = block.Parent()
	}
	return block
}

// commentEndAfter is whether the inline text of block, from source offset from on, holds a
// cmarkGFMCommentEnd: the text cmark-gfm's inline scanner reads as one subject, the bytes of the
// block's lines without the prefix a block quote or a list item puts before each. Each line is
// matched where it lies in source, with nothing copied: a `-->` holds no line break, and what
// stands before a line's first byte in that subject is a line break, never a `-`.
func commentEndAfter(block ast.Node, from int, source []byte) bool {
	if block == nil {
		return false
	}
	lines := block.Lines()
	for i := range lines.Len() {
		line := lines.At(i)
		if line.Stop > from && cmarkGFMCommentEnd.Match(source[max(line.Start, from):line.Stop]) {
			return true
		}
	}
	return false
}

// VisibleMarkdown is doc as a reader of the page GitHub renders sees it, for a check that holds a
// guide's text to something: every HTML comment is gone, so a passage moved inside one no longer
// counts as the guide stating it. A check that read the raw bytes passed while the page showed
// nothing, since a comment is in the file and on no page.
//
// Neither grammar is written here, but for the two cmark-gfm readings markdown names. goldmark
// parses doc as CommonMark with GitHub Flavored Markdown's extensions and footnotes, which says
// which bytes are raw HTML: an HTML block, or raw HTML inline. golang.org/x/net/html's tokenizer,
// which follows the HTML specification's tokenizer, reads each of those, and every comment or
// DOCTYPE token it gives is taken out: a comment, the empty `<!-->`, one that `--!>` ends, a bogus
// comment such as `<?php … ?>`, `<![CDATA[ … ]]>` or `<!X …>`, and a `<!DOCTYPE …>` (in upper
// case: see below), none of which a page shows. Inline raw HTML that GitHub writes as text
// (cmarkGFMRawHTML) is not read: it hides nothing and ends no open comment. Before the tokenizer
// reads a piece, the `<` of each tag GitHub's tag filter disallows (`<script>`, `<style>`,
// `<textarea>`, `<title>`, `<xmp>`, `<iframe>`, `<noembed>`, `<noframes>`, `<plaintext>`) is read
// as text, as GitHub writes it `&lt;`, so a comment inside one of those is a comment, as on GitHub.
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
//   - an HTML block at `<search>` or `<meta>`, or at a closing tag with a space after its `/` such
//     as `</ div>`: goldmark opens one there and GitHub does not, so a comment such a line leaves
//     open is carried on here where GitHub shows the text after it. This takes out text the page
//     shows, so a check fails rather than passes;
//   - a declaration whose first letter is lower case, such as `<!doctype html>`: goldmark opens
//     an HTML block or inline raw HTML at `<!` only before an upper-case letter
//     (parser/html_block.go:88 and parser/raw_html.go:41 at v1.8.6), where CommonMark and every
//     renderer take any ASCII letter, so the declaration is read as text and stays, though no page
//     shows it;
//   - a declaration with no space after its name, such as `<!ZM>` or `<!DOCTYPE>`: cmark-gfm takes
//     a name and then one space character at least (scanners.re:61, `declaration`), so GitHub
//     writes such a one as text, where goldmark reads inline raw HTML and this takes it out. That
//     takes out text the page shows, so a check fails rather than passes;
//   - a tag or a quoted attribute value an HTML block leaves open, as in `<div title="x` and a
//     blank line: on GitHub the HTML parser runs the tag on over what follows, up to the quote or
//     the `>` that ends it, and hides that text, where this reads each node on its own, ends the
//     tag with its block, and keeps the text;
//   - the order of footnotes: GitHub renders every footnote definition at the end of the page,
//     after the body, where this carries an open comment in the order of the source. So a comment
//     a footnote leaves open, which on GitHub hides the footnotes after it, here runs over the
//     body text that follows the definition in the source and leaves those footnotes in; and a
//     comment the body leaves open before a footnote definition here hides the definition, which
//     GitHub shows;
//   - a comment inside a `<noscript>` element: the tokenizer reads its content as raw text, and
//     GitHub's tag filter does not cover `<noscript>`, so the comment stays;
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
		if len(p.bytes) == 0 {
			return
		}
		pieces = append(pieces, p)
	}
	root := markdown.Parser().Parse(text.NewReader(source))
	noBang := map[ast.Node]bool{}
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
		case *ast.Text:
			markTextOpener(n, source, noBang)
		case *ast.RawHTML:
			segments := make([]text.Segment, 0, n.Segments.Len())
			var raw []byte
			for i := range n.Segments.Len() {
				segment := n.Segments.At(i)
				segments = append(segments, segment)
				raw = append(raw, segment.Value(source)...)
			}
			if !cmarkGFMRawHTML(n, raw, source, noBang) {
				return ast.WalkContinue, nil
			}
			read(segments, false)
		}
		return ast.WalkContinue, nil
	})
	slices.SortStableFunc(pieces, func(a, b piece) int {
		return cmp.Compare(a.offsets[0], b.offsets[0])
	})
	return pieces
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
