package text

import (
	"bytes"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	markdowntext "github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// HeadRunes returns value cut to its first limit runes; a value at or under the
// limit is returned unchanged.
func HeadRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// HeadWords returns value cut to its first limit runes at a word boundary, and whether it cut:
// the cut falls back to the last space inside the limit, so the answer ends at a whole word,
// and a value whose first limit runes hold no space is cut at the limit itself, since there is
// no boundary to fall back to. A value at or under the limit is returned unchanged and uncut.
// A caller that marks a cut (an ellipsis) reads the second answer rather than comparing
// lengths, which a trailing space the cut removed would make wrong.
func HeadWords(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	head := string(runes[:limit])
	if space := strings.LastIndexAny(head, " \t\n"); space > 0 {
		head = head[:space]
	}
	return strings.TrimRight(head, " \t\n"), true
}

// longestWholeReferenceRunes is the longest reference HeadRefs keeps whole, in runes: the text it
// reads past the cut. A reference, or a link around one, that runs further than this past the cut
// is cut by runes, as HeadRunes cuts it. It is far longer than any reference a person writes - an
// address to a block is a few hundred characters, a mention's link a few dozen - and short enough
// that a cut reads the same few kilobytes whatever the length of the text it cuts.
const longestWholeReferenceRunes = 1024

// HeadRefs is value cut to at most limit runes, ellipsis included, at a place no reference
// crosses: the cut of a text a reader is shown with its references drawn (an Inbox subject, a
// candidate's first line, an excerpt), where HeadRunes would leave half of one - `CORE-12` of
// `CORE-1234`, a working link to another issue, or `[@Planner](dispatch://agent/` drawn raw. A
// value at or under the limit is returned unchanged. One over it is cut to its first limit runes
// less the ellipsis's and the ellipsis is appended: `…` for a line, as contracts.OneLineSummary
// marks its cut, and "" for an excerpt, which marks none.
//
// It reads value's first limit and longestWholeReferenceRunes runes, no more, so a cut costs the
// same on a text of any length - which every read of the replies, the backlinks and the id
// prefixes pays for each row it cuts. The references in them are the ones Dispatch holds
// (cutReferences): a `dispatch://` address and a dashboard URL of the deployment at serverURL as
// Extract reads them, a bare issue key and a `#id` as Mentions reads its candidates, each taken
// with the inline link around it whole, as goldmark reads links - a title, an address in angle
// brackets, brackets in the words and words over a line break included. A URL of anywhere else
// names nothing Dispatch holds, so it is cut as text is.
//
// Where a reference starts before the cut and ends after it, the cut moves back to its start - the
// link's `[` where a link wraps it. A code span the cut opens and does not close counts as
// crossing it too, and the cut moves back to before its opening backticks (codeSpans): the head
// would hold those backticks with no closing run, which reads them as text and what the span
// held - an address in it included - as prose with live references. It moves back the same way
// where the head, with the ellipsis after it, does not read the references of the text it holds
// as the text does: a reference it reads that the text does not write (`CORE-12` of `CORE-12x4`),
// or one the text writes whole that the ellipsis joins (`dispatch://agent/s1` and `…` read as the
// session `s1…`). A cut that moved drops the whitespace before it, line breaks included, save
// where the head then ends after a reference: there it keeps the first whitespace character the
// text has after that reference, so the ellipsis joins nothing and the walk stops - a line of
// short addresses separated by spaces is cut after its last whole address and a space, not back
// past every one of them. A cut never empties the head: where moving back would leave nothing
// before the ellipsis - a text that opens with a reference or a code span longer than the bound -
// the rune cut stands, since a head of nothing tells a reader less than half a reference does. A
// cut that moves nothing is HeadRunes' own.
//
// The dashboard's headRefs (`packages/dispatch/web/src/features/refs/head-refs.ts`) cuts the
// same way, and `testdata/head-refs.json` is the table both copies are held to.
func HeadRefs(value string, limit int, ellipsis, serverURL string) string {
	keep := max(limit-utf8.RuneCountInString(ellipsis), 0)
	cut, window, count := 0, len(value), 0
	over := false
	for index := range value {
		if count == keep {
			cut = index
		}
		if count == limit {
			over = true
		}
		if count == limit+longestWholeReferenceRunes {
			window = index
			break
		}
		count++
	}
	if !over {
		return value
	}
	return value[:headEnd(value[:window], window < len(value), cut, ellipsis, serverURL)] + ellipsis
}

// headEnd is where the head of window, value's first runes, cut at cut ends (HeadRefs). A
// reference or a code span that runs to the window's edge where the window stops short of value
// may run further, so it is none the cut keeps whole.
func headEnd(window string, truncated bool, cut int, ellipsis, serverURL string) int {
	written := cutReferences(window, serverURL)
	spans := codeSpans(window)
	if truncated {
		written = slices.DeleteFunc(written, func(reference cutReference) bool {
			return reference.end >= len(window)
		})
		spans = slices.DeleteFunc(spans, func(span codeRange) bool {
			return span.end >= len(window)
		})
	}
	head := cut
	for head > 0 {
		back := head
		for _, reference := range written {
			if reference.start < head && head < reference.end {
				back = min(back, reference.start)
			}
		}
		for _, span := range spans {
			if span.start < head && head < span.end {
				back = min(back, span.start)
			}
		}
		read := cutReferences(window[:head]+ellipsis, serverURL)
		for _, reference := range read {
			if reference.start < head && !slices.Contains(written, reference) {
				back = min(back, reference.start)
			}
		}
		for _, reference := range written {
			if reference.end <= head && !slices.Contains(read, reference) {
				back = min(back, reference.start)
			}
		}
		if back == head {
			break
		}
		head = len(strings.TrimRight(window[:back], " \t\r\n"))
		// A head that now ends after a reference the text follows with whitespace keeps the
		// first of it, so the ellipsis after the head joins nothing: without it the ellipsis would
		// join that reference and the walk step back past it, and on a line of short addresses
		// back past every one of them.
		if head < back && slices.ContainsFunc(written, func(reference cutReference) bool {
			return reference.end == head
		}) {
			head++
		}
	}
	if head == 0 {
		return cut
	}
	return head
}

// codeSpans is every code span body writes (codeRanges), its blocks left out: a cut inside a
// span the head does not close leaves its opening backticks as text, and what the span held -
// an address in it included - read as prose, where a cut inside a fenced block leaves the rest
// of the head in the block as the text has it. A body with no backtick has no span, and is not
// walked for one.
func codeSpans(body string) []codeRange {
	if !strings.Contains(body, "`") {
		return nil
	}
	return slices.DeleteFunc(codeRanges(body), func(code codeRange) bool { return code.block })
}

// cutReference is one reference a text writes, as a cut reads it: the bytes [start, end) a reader
// sees it in - the whole inline link around it, where one is - and what it names.
type cutReference struct {
	start, end int
	ref        Ref
}

// cutReferences is every reference body writes that Dispatch holds, in the reader's own terms:
// each reference span Extract reads as a `dispatch://` address or a page of the dashboard at
// serverURL (locateSpan), a URL of anywhere else left out, and each bare key and `#id` Mentions
// would ask about (bareCandidates), whether or not it names anything, since a cut that splits one
// changes what the text says. Each is widened over the inline link that holds it (linkRanges),
// and a span that is a link's address is that address, as the link reads it, rather than the
// span, which would run on over the `)` into whatever follows it.
func cutReferences(body, serverURL string) []cutReference {
	spans := referencePattern.FindAllStringIndex(body, -1)
	links := linkRanges(body)
	var base *url.URL
	baseParsed := false
	baseOf := func() *url.URL {
		if !baseParsed {
			base, _ = url.Parse(serverURL)
			baseParsed = true
		}
		return base
	}
	var found []cutReference
	add := func(start, end int, ref Ref) {
		for _, link := range links {
			if link.open <= start && start < link.close {
				start, end = link.open, link.close
				break
			}
		}
		found = append(found, cutReference{start: start, end: end, ref: ref})
	}
	for _, span := range spans {
		start, raw := span[0], trimReference(body[span[0]:span[1]])
		for _, link := range links {
			if link.open < start && start < link.close && link.destination != "" &&
				strings.HasPrefix(body[start:], link.destination) {
				raw = link.destination
				break
			}
		}
		if raw == "" {
			continue
		}
		if ref, ok := locateSpan(raw, baseOf, true); ok && ref.Kind != "url" {
			add(start, start+len(raw), ref)
		}
	}
	candidates, _, _ := bareCandidates(body, spans)
	for _, candidate := range candidates {
		if candidate.key != "" {
			add(candidate.offset, candidate.offset+len(candidate.key),
				Ref{Kind: "issue", IssueKey: candidate.key, ID: candidate.key})
			continue
		}
		add(candidate.offset, candidate.offset+1+len(candidate.id), Ref{Kind: "ask", ID: candidate.id})
	}
	return found
}

// linkRange is one inline link or picture a text writes: its `[`, or the `!` before it, one past
// its `)`, and its address as the link reads it.
type linkRange struct {
	open, close int
	destination string
}

// linkRanges is every inline link and picture body writes, in the order they open, as goldmark
// reads CommonMark's links: a title, an address in angle brackets, balanced brackets in the words,
// words over a line break, a backslash escape and no link inside another, all of it the parser's.
// goldmark records where a link opens (ast.Node.Pos) but not where it closes, so its link parser
// is wrapped to note where the reader stands once it has read one (rangedLinkParser).
func linkRanges(body string) []linkRange {
	if !strings.Contains(body, "[") {
		return nil
	}
	var ranges []linkRange
	context := parser.NewContext()
	context.Set(linkRangesKey, &ranges)
	linkReader.Parse(markdowntext.NewReader([]byte(body)), parser.WithContext(context))
	slices.SortFunc(ranges, func(left, right linkRange) int { return left.open - right.open })
	return ranges
}

// linkRangesKey holds, in one parse's context, the links its link parser has read.
var linkRangesKey = parser.NewContextKey()

// linkReader is goldmark's CommonMark parser with its link parser wrapped (rangedLinkParser). It
// holds no state of its own between parses, so one serves every cut.
var linkReader = parser.NewParser(
	parser.WithBlockParsers(parser.DefaultBlockParsers()...),
	parser.WithInlineParsers(rangedInlineParsers()...),
	parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
)

// rangedInlineParsers is goldmark's inline parsers with its link parser wrapped.
func rangedInlineParsers() []util.PrioritizedValue {
	parsers := parser.DefaultInlineParsers()
	link := parser.NewLinkParser()
	for index, prioritized := range parsers {
		if inline, held := prioritized.Value.(parser.InlineParser); held && inline == link {
			parsers[index].Value = rangedLinkParser{InlineParser: inline}
		}
	}
	return parsers
}

// rangedLinkParser is goldmark's link parser, noting the range of each link or picture it reads
// in the parse's context (linkRangesKey): it opens where the node says, and closes at the `)` the
// parser has just read past - or the `]`, for a link to a reference definition, which has no
// address of its own written there.
type rangedLinkParser struct {
	parser.InlineParser
}

func (p rangedLinkParser) Parse(parent ast.Node, block markdowntext.Reader, pc parser.Context) ast.Node {
	node := p.InlineParser.Parse(parent, block, pc)
	var destination []byte
	closer := byte(')')
	switch link := node.(type) {
	case *ast.Link:
		destination = link.Destination
		if link.Reference != nil {
			closer, destination = ']', nil
		}
	case *ast.Image:
		destination = link.Destination
		if link.Reference != nil {
			closer, destination = ']', nil
		}
	default:
		return node
	}
	ranges, held := pc.Get(linkRangesKey).(*[]linkRange)
	if !held {
		return node
	}
	_, after := block.Position()
	closed := bytes.LastIndexByte(block.Source()[:after.Start], closer) + 1
	*ranges = append(*ranges, linkRange{open: node.Pos(), close: closed, destination: string(destination)})
	return node
}

// CloseBlock is the link parser's own: it drops the brackets a block left open, so a `[` of one
// paragraph opens no link in the next. A wrapper that did not pass it on would let it.
func (p rangedLinkParser) CloseBlock(parent ast.Node, block markdowntext.Reader, pc parser.Context) {
	if closer, held := p.InlineParser.(parser.CloseBlocker); held {
		closer.CloseBlock(parent, block, pc)
	}
}
