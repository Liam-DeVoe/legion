package pmdoc

import (
	"bytes"
	"iter"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// This file is the second of the engine's two readers of bare addresses that linkifyGuard
// describes: mdast-util-gfm-autolink-literal's transform, which links the addresses it finds in
// each run of a block's text nodes, decoded, at the block's close (linkTextAddresses). linkify.go
// holds the first, the tokenizer. Last comes Dispatch's own rule for the email links both made
// (wholeEmailLinks).

// decodedLabelAttr is the attribute holding the decoded text of an autolink the transform made
// (linkTextAddresses) or the rule moved (wholeEmailLinks), which its source spells with escapes
// and references; a tokenizer's autolink shows its source as written.
const decodedLabelAttr = "pmdocDecodedLabel"

// bareAutolinkAttr is the attribute marking an autolink a reader made from bare text, the
// tokenizer's or the transform's (bareAutolink), with its source offsets, a text.Segment: its
// emails are the links wholeEmailLinks holds to the rule, and the rule reads through each as
// through text. goldmark's `<…>` autolink, which the writer wrote, carries none.
const bareAutolinkAttr = "pmdocBareAutolink"

// autolinkText is the text an autolink shows and the href it links to: an email's mailto: URL
// (emailHref), and a URL's text, `http://` before a www URL's.
func autolinkText(link *ast.AutoLink, source []byte) (label, href string) {
	label = string(link.Label(source))
	if decoded, ok := link.AttributeString(decodedLabelAttr); ok {
		label = decoded.(string)
	}
	switch {
	case link.AutoLinkType == ast.AutoLinkEmail:
		return label, emailHref(label)
	case link.Protocol != nil:
		return label, string(link.Protocol) + "://" + label
	}
	return label, label
}

// emailHref is the href an email address links to, as the engine links it: its mailto: URL.
func emailHref(address string) string {
	return "mailto:" + address
}

// linkTextAddresses links what mdast-util-gfm-autolink-literal's transform links in parent's
// text: each run of goldmark's text nodes between other nodes in each of parent's text containers
// (textContainers) is one of the engine's text nodes (linkRuns).
func linkTextAddresses(parent ast.Node, source []byte) {
	var run []ast.Node
	for container := range textContainers(parent) {
		run = linkRuns(container, source, run)
	}
}

// textContainers is parent and each node inside it whose children the readers read as text:
// outside a link, an image, a code span or an autolink. A block inside parent is left to its own
// CloseBlock, which goldmark calls on every block after its children's: the engine searches each
// text once, and a second search would read the text beside a link it made alone. Each container
// is yielded before the walk enters it, so its children may be changed then.
func textContainers(parent ast.Node) iter.Seq[ast.Node] {
	return func(yield func(ast.Node) bool) {
		for node := parent; node != nil; {
			opaque := node != parent && node.Type() == ast.TypeBlock
			switch node.(type) {
			case *ast.Link, *ast.Image, *ast.CodeSpan, *ast.AutoLink:
				opaque = true
			}
			if !opaque && node.FirstChild() != nil {
				if !yield(node) {
					return
				}
				node = node.FirstChild()
				continue
			}
			for node != parent && node.NextSibling() == nil {
				node = node.Parent()
			}
			if node == parent {
				return
			}
			node = node.NextSibling()
		}
	}
}

// linkRuns links the addresses in each run of container's text and string children, which a hard
// break ends, as the engine's break node ends its text; run is scratch space, returned for reuse.
func linkRuns(container ast.Node, source []byte, run []ast.Node) []ast.Node {
	for child := container.FirstChild(); child != nil; {
		run = run[:0]
		for child != nil {
			text, isText := child.(*ast.Text)
			if _, isString := child.(*ast.String); !isText && !isString {
				break
			}
			run = append(run, child)
			child = child.NextSibling()
			if isText && readsAsHardBreak(text, source) {
				break
			}
		}
		if len(run) == 0 {
			child = child.NextSibling()
			continue
		}
		linkRun(run, source)
	}
	return run
}

// readsAsHardBreak reports whether text ends in a line break the engine reads as hard: a backslash,
// or two spaces or more and no tab, as parseInlineMarks reads it.
func readsAsHardBreak(text *ast.Text, source []byte) bool {
	if !text.HardLineBreak() {
		return false
	}
	_, run, backslash := lineSuffix(text, source)
	return backslash || len(run) >= 2 && !strings.Contains(run, "\t")
}

// textPiece is one node of a run, its decoded text starting at start in the run's; a text's
// offsets map each offset of value, and its end, to the offset in the text's raw value (rawValue)
// it begins at (decodeMarkdown).
type textPiece struct {
	node    ast.Node
	start   int
	value   string
	offsets []int32
}

// foundLink is an address of kind the transform found in a run's decoded text, at [start, end);
// after is where its pattern's match ended, which a URL's trail runs to.
type foundLink struct {
	start, end, after int
	kind              ast.AutoLinkType
	www               bool
}

// linkRun links the addresses the transform finds in the decoded text of run, one of the engine's
// text nodes: its URLs (findURLs), then the emails in the text they leave (findEmails), the text
// around each split where the source decodes to its edges (linkFound).
func linkRun(run []ast.Node, source []byte) {
	holds := false
	for _, node := range run {
		holds = holds || mayHoldAddress(rawValue(node, source))
	}
	if !holds {
		return
	}
	pieces := make([]textPiece, len(run))
	var decoded strings.Builder
	for index, node := range run {
		piece := textPiece{node: node, start: decoded.Len()}
		text, isText := node.(*ast.Text)
		if isText {
			piece.value, piece.offsets = decodeMarkdown(rawValue(node, source), true)
		} else {
			piece.value = decodeMarkdownText(rawValue(node, source))
		}
		pieces[index] = piece
		decoded.WriteString(piece.value)
		if isText && (text.SoftLineBreak() || text.HardLineBreak()) {
			decoded.WriteByte('\n')
		}
	}
	value := decoded.String()
	var found []foundLink
	from := 0
	for _, url := range findURLs(value) {
		found = findEmails(found, value, from, url.start)
		found = append(found, url)
		found = findEmails(found, value, url.end, url.after)
		from = url.after
	}
	found = findEmails(found, value, from, len(value))
	if len(found) != 0 {
		linkFound(pieces, found, value)
	}
}

// rawValue is node's text as the source spells it: a text's segment, its padding included, or a
// string's value.
func rawValue(node ast.Node, source []byte) []byte {
	if text, ok := node.(*ast.Text); ok {
		return text.Segment.Value(source)
	}
	return node.(*ast.String).Value
}

// mayHoldAddress reports whether raw holds what an address needs once decoded: an `@`, a `:`, a
// `www`, or a character reference, which may spell either.
func mayHoldAddress(raw []byte) bool {
	if bytes.ContainsAny(raw, "@:&") {
		return true
	}
	for at := range len(raw) - 2 {
		if raw[at]|0x20 == 'w' && raw[at+1]|0x20 == 'w' && raw[at+2]|0x20 == 'w' {
			return true
		}
	}
	return false
}

// findURLs is the URLs the transform's URL pattern, `(https?://|www(?=\.))([-.\w]+)([^ \t\r\n]*)`
// in any case, links in value (findUrl): a match after the value's start, whitespace or
// punctuation (transformPrevious), whose domain is a correct one, less its trail (splitURL). A
// refused match is searched again one character after where it began, a taken one after its end.
func findURLs(value string) []foundLink {
	var found []foundLink
	var domain urlDomain
	for at := 0; at < len(value); {
		start, domainStart := nextURLPattern(value, at)
		if start < 0 {
			break
		}
		at = start + 1
		if !transformPrevious(value, start, false) {
			continue
		}
		end := domain.read(value, domainStart)
		if !domain.correct(value, domainStart) {
			continue
		}
		after := end
		for after < len(value) && strings.IndexByte(" \t\r\n", value[after]) < 0 {
			after++
		}
		if end = splitURL(value, domainStart, after); end == domainStart {
			continue
		}
		found = append(found, foundLink{start: start, end: end, after: after, kind: ast.AutoLinkURL, www: value[start]|0x20 == 'w'})
		at = after
	}
	return found
}

// nextURLPattern is where the URL pattern next matches in value from at, and where its domain
// begins (a www URL's `www` among it), or -1.
func nextURLPattern(value string, at int) (start, domain int) {
	for ; at < len(value); at++ {
		switch value[at] | 0x20 {
		case 'h':
			for _, protocol := range []string{"https://", "http://"} {
				if end := at + len(protocol); end < len(value) && strings.EqualFold(value[at:end], protocol) && urlWordCharacter(value[end]) {
					return at, end
				}
			}
		case 'w':
			if at+4 <= len(value) && strings.EqualFold(value[at:at+4], "www.") {
				return at, at
			}
		}
	}
	return -1, -1
}

// urlWordCharacter reports whether c is one of the URL pattern's domain characters, `[-.\w]`.
func urlWordCharacter(c byte) bool {
	return c == '.' || emailDomainCharacter(c)
}

// urlDomain is the run of the URL pattern's domain characters last read, from where a domain began
// in it to its end, with its last two dots and whether the dot-separated parts after each are
// correct: every domain beginning inside the run ends where it does, so a run is read once.
type urlDomain struct {
	from, end            int
	lastDot, dotBefore   int // -1 where there is none
	lastOK, beforeLastOK bool
}

// read reads the run from start, a domain's start, unless the last run read holds it, and returns
// where the run ends.
func (d *urlDomain) read(value string, start int) int {
	if start >= d.from && start < d.end {
		return d.end
	}
	d.from, d.end, d.lastDot, d.dotBefore = start, start, -1, -1
	for d.end < len(value) && urlWordCharacter(value[d.end]) {
		if value[d.end] == '.' {
			d.dotBefore, d.lastDot = d.lastDot, d.end
		}
		d.end++
	}
	if d.lastDot >= 0 {
		d.lastOK = domainPart(value[d.lastDot+1 : d.end])
	}
	if d.dotBefore >= 0 {
		d.beforeLastOK = domainPart(value[d.dotBefore+1 : d.lastDot])
	}
	return d.end
}

// correct reports whether the domain from start to the run's end is one the transform takes
// (isCorrectDomain): two dot-separated parts or more, neither of its last two holding an
// underscore or lacking a letter or digit, unless it is empty.
func (d *urlDomain) correct(value string, start int) bool {
	switch {
	case d.lastDot < start:
		return false
	case d.dotBefore < start:
		return d.lastOK && domainPart(value[start:d.lastDot])
	}
	return d.lastOK && d.beforeLastOK
}

// domainPart reports whether part is a correct last or next-to-last part of a domain: empty, or
// holding a letter or digit and no underscore.
func domainPart(part string) bool {
	if part == "" {
		return true
	}
	if strings.IndexByte(part, '_') >= 0 {
		return false
	}
	for index := range len(part) {
		if isASCIIAlphaNumeric(part[index]) {
			return true
		}
	}
	return false
}

// splitURL is where the URL whose domain begins at start and whose match ends at after ends
// (splitUrl): before its trail, a run of `!"&'),.:;<>?]}` ending it, but through each `)` of the
// trail that closes a `(` the URL left open.
func splitURL(value string, start, after int) int {
	end := after
	for end > start && strings.IndexByte(`!"&'),.:;<>?]}`, value[end-1]) >= 0 {
		end--
	}
	opening, closing := strings.Count(value[start:end], "("), strings.Count(value[start:end], ")")
	for ; opening > closing; closing++ {
		next := strings.IndexByte(value[end:after], ')')
		if next < 0 {
			break
		}
		end += next + 1
	}
	return end
}

// transformPrevious reports whether the transform takes a match at value[at] for what comes
// before it (previous): nothing or a transformBoundary; an email takes no `/` before it either.
func transformPrevious(value string, at int, email bool) bool {
	if at == 0 {
		return true
	}
	char, _ := utf8.DecodeLastRuneInString(value[:at])
	return !(email && char == '/') && transformBoundary(char)
}

// transformBoundary reports whether char is one the transform takes a match after: whitespace, or
// punctuation or a symbol, judged by one UTF-16 code unit, so a character outside the Basic
// Multilingual Plane is neither.
func transformBoundary(char rune) bool {
	return literalEnd(char) || literalPunctuation(char)
}

// findEmails is found with the emails the transform's email pattern,
// `([-.\w+]+)@([-\w]+(?:\.[-\w]+)+)` after the text's start, whitespace, punctuation or a symbol,
// links in value[from:to], one of the text nodes the URLs left (findEmail): its previous character
// no `/`, its domain ending in no `-`, digit or `_`. A refused match is searched again one
// character after where it began. That is remark-gfm's reading and no longer Dispatch's where the
// match starts after a character RFC 5322 allows in a local part: there the match names another
// address than the one written (`agent-hosts@…` read after a `/` links `hosts@…`), so the rule
// that follows both readers moves the link to the start of that run of characters where GFM links
// each of them in a local part, or drops it where it does not, as at the `/` (wholeEmailLinks), as
// the engine does too.
func findEmails(found []foundLink, value string, from, to int) []foundLink {
	var local emailRun
	for at := from; at < to; at++ {
		if !gfmAtext(value[at]) || at > from && !transformPrevious(value, at, true) {
			continue
		}
		end := local.read(value, at, to)
		if end == 0 || strings.IndexByte("-_0123456789", value[end-1]) >= 0 {
			continue
		}
		found = append(found, foundLink{start: at, end: end, after: end, kind: ast.AutoLinkEmail})
		at = end - 1
	}
	return found
}

// emailRun is the run of local-part characters last read, and the email domain after its `@`:
// every match beginning inside the run reaches the same `@` and domain, so a run is read once.
type emailRun struct {
	from, end, domainEnd int
}

// read is where the email pattern's match beginning at at, inside value[:to], ends, or 0 where it
// does not match: a run of local-part characters to an `@`, then a domain of `[-\w]` parts, two or
// more, separated by single dots.
func (r *emailRun) read(value string, at, to int) int {
	if at >= r.from && at < r.end {
		return r.domainEnd
	}
	r.from, r.end, r.domainEnd = at, at, 0
	for r.end < to && gfmAtext(value[r.end]) {
		r.end++
	}
	if r.end == to || value[r.end] != '@' {
		return 0
	}
	part := func(at int) int {
		for at < to && emailDomainCharacter(value[at]) {
			at++
		}
		return at
	}
	end := part(r.end + 1)
	if end == r.end+1 {
		return 0
	}
	parts := 1
	for end+1 < to && value[end] == '.' {
		next := part(end + 1)
		if next == end+1 {
			break
		}
		end, parts = next, parts+1
	}
	if parts > 1 {
		r.domainEnd = end
	}
	return r.domainEnd
}

// linkFound replaces the text of pieces, which decode to value, where each link found stands with
// an autolink holding its decoded text, splitting the text nodes around it at the source offsets
// that decode to its edges. A link whose edge falls inside what one escape or character reference
// decodes to, or that would split a string, which no source spells, is left as text.
func linkFound(pieces []textPiece, found []foundLink, value string) {
	type placed struct {
		first, last *ast.Text
		start, end  int // source offsets
		label       string
		kind        ast.AutoLinkType
		www         bool
	}
	links := make([]placed, 0, len(found))
	index := 0
	for _, link := range found {
		for index < len(pieces) && pieces[index].start+len(pieces[index].value) <= link.start {
			index++
		}
		first, start, ok := pieces[index].sourceAt(link.start - pieces[index].start)
		last := index
		for pieces[last].start+len(pieces[last].value) < link.end {
			if _, isText := pieces[last].node.(*ast.Text); !isText {
				ok = false
			}
			last++
		}
		lastText, end, endOK := pieces[last].sourceAt(link.end - pieces[last].start)
		if ok && endOK {
			links = append(links, placed{first: first, last: lastText, start: start, end: end, label: value[link.start:link.end], kind: link.kind, www: link.www})
		}
		index = last
	}
	for _, link := range links {
		parent := link.first.Parent()
		if link.start > link.first.Segment.Start || link.first.Segment.Padding != 0 {
			parent.InsertBefore(parent, link.first, ast.NewTextSegment(link.first.Segment.WithStop(link.start)))
			link.first.Segment = text.NewSegment(link.start, link.first.Segment.Stop)
		}
		autolink := bareAutolink(link.kind, link.www, link.start, link.end)
		autolink.SetAttributeString(decodedLabelAttr, link.label)
		autolink.SetPos(link.start)
		parent.InsertBefore(parent, link.first, autolink)
		for node := ast.Node(link.first); node != link.last; {
			next := node.NextSibling()
			parent.RemoveChild(parent, node)
			node = next
		}
		link.last.Segment = text.NewSegment(link.end, link.last.Segment.Stop)
		if link.last.Segment.IsEmpty() && !link.last.SoftLineBreak() && !link.last.HardLineBreak() {
			parent.RemoveChild(parent, link.last)
		}
	}
}

// sourceAt is the text piece is, and the source offset that decodes to offset in its decoded text,
// or false for a string or an offset inside what one escape or reference decodes to.
func (piece textPiece) sourceAt(offset int) (*ast.Text, int, bool) {
	text, ok := piece.node.(*ast.Text)
	if !ok {
		return nil, 0, false
	}
	raw := offset
	if piece.offsets != nil {
		raw = int(piece.offsets[offset])
	}
	if raw < 0 {
		return nil, 0, false
	}
	return text, text.Segment.Start + max(0, raw-text.Segment.Padding), true
}

// wholeEmailLinks holds each email link a reader made from a bare address in parent's text
// containers (bareAutolinkAttr, textContainers) to Dispatch's rule, once both readers have linked
// them (linkifyGuard). The rule reads the run of characters that RFC 5322 allows in a local part
// (rfcAtext) in the decoded text just before the link: the run ends at the link's own local part,
// so with it, it ends at the `@`. The walk back stops at the first character outside that set
// (whitespace, a line break, a letter outside ASCII, any other character). An autolink a reader
// made from bare text is no markup, so the walk goes on through its label as through text. Any
// other node ends the walk: the run continues into it only when its text ends in a character of
// the set (endsInRunCharacter), and then the writer split the address with markup, as in
// `*agent*-hosts@example.com`, the run is not one run, and the link stays as the reader made it.
// Otherwise, when every character of the run before the link's own text is one GFM links in a
// local part (gfmAtext), the link takes the run; when one is not, as the `/` of a resource path,
// the `%` of `a.b+c_d%e-f@example.com` or the `&` of `agent&amp;hosts@example.com`, the link goes,
// and its text joins the text around it, while a reader's autolink the run reaches into stays.
// A run of GFM's characters that reaches into such an autolink leaves both links as they are,
// since one link never takes another's text. With no run before the link nothing changes. The
// text just before a link is the engine's one text node there: the text and string nodes before
// it, back to another node or to a text a hard break ends (linkRuns), decoded, so a character
// reference or an escape in the run counts as its character. A container's links are read in
// order, so the text a link that went has joined is the text before the next one.
func wholeEmailLinks(parent ast.Node, source []byte) {
	for container := range textContainers(parent) {
		for child := container.FirstChild(); child != nil; {
			next := child.NextSibling()
			if link, ok := child.(*ast.AutoLink); ok && link.AutoLinkType == ast.AutoLinkEmail {
				if span, bare := link.AttributeString(bareAutolinkAttr); bare {
					wholeEmailLink(link, span.(text.Segment), source)
				}
			}
			child = next
		}
	}
}

// wholeEmailLink holds link, an email autolink a reader made from the bare address at source
// offsets span, to the rule (wholeEmailLinks). A link that goes leaves its source as a text node
// between the nodes around it, which the tree reads as one text with the texts there. A moved link
// spans the source from the run's first character and shows the run's decoded text before its own
// (decodedLabelAttr), and the text nodes before it lose the run. A run that starts in a string,
// which no source spells, leaves the link as it is, as linkFound leaves such an address text. No
// run starts inside what one character reference decodes to, since none decodes to a character of
// the run after one that is none.
func wholeEmailLink(link *ast.AutoLink, span text.Segment, source []byte) {
	// The text pieces the run lies in, from the link back, and where it starts in the last of
	// them; the run's length, whether GFM links each of its characters in a local part, and
	// whether it reaches into an autolink a reader made.
	var held [4]textPiece
	pieces, from, length, linked, crossed := held[:0], 0, 0, true, false
walk:
	for node := link.PreviousSibling(); node != nil; node = node.PreviousSibling() {
		var value string
		switch node := node.(type) {
		case *ast.Text, *ast.String:
			if text, ok := node.(*ast.Text); ok && (text.SoftLineBreak() || text.HardLineBreak()) {
				// A line break holds no text, so the run starts at the start of the line, as the
				// engine's break node or the line ending its text holds ends it.
				break walk
			}
			piece := textPiece{node: node}
			if _, ok := node.(*ast.Text); ok {
				piece.value, piece.offsets = decodeMarkdown(rawValue(node, source), true)
			} else {
				piece.value = decodeMarkdownText(rawValue(node, source))
			}
			pieces = append(pieces, piece)
			value = piece.value
		case *ast.AutoLink:
			if _, bare := node.AttributeString(bareAutolinkAttr); !bare {
				if endsInRunCharacter(node, source) {
					return
				}
				break walk
			}
			value, _ = autolinkText(node, source)
		default:
			if endsInRunCharacter(node, source) {
				// Markup splits the address: the link stays as the reader made it.
				return
			}
			break walk
		}
		end := len(value)
		for end > 0 && rfcAtext(value[end-1]) {
			end--
			linked = linked && gfmAtext(value[end])
		}
		length += len(value) - end
		if _, isLink := node.(*ast.AutoLink); isLink {
			crossed = crossed || end < len(value)
		} else {
			from = end
		}
		if end > 0 {
			break
		}
	}
	if length == 0 || linked && crossed {
		return
	}
	parent := link.Parent()
	if !linked {
		parent.ReplaceChild(parent, link, ast.NewTextSegment(span))
		return
	}
	first := pieces[len(pieces)-1]
	firstText, start, ok := first.sourceAt(from)
	if !ok {
		return
	}
	label, _ := autolinkText(link, source)
	var whole strings.Builder
	whole.Grow(length + len(label))
	whole.WriteString(first.value[from:])
	for index := len(pieces) - 2; index >= 0; index-- {
		whole.WriteString(pieces[index].value)
		parent.RemoveChild(parent, pieces[index].node)
	}
	whole.WriteString(label)
	if start > firstText.Segment.Start || firstText.Segment.Padding != 0 {
		firstText.Segment = firstText.Segment.WithStop(start)
	} else {
		parent.RemoveChild(parent, firstText)
	}
	moved := bareAutolink(ast.AutoLinkEmail, false, start, span.Stop)
	moved.SetAttributeString(decodedLabelAttr, whole.String())
	moved.SetPos(start)
	parent.ReplaceChild(parent, link, moved)
}

// rfcAtext reports whether c is a character RFC 5322 allows in an email's local part: its atext,
// an ASCII letter or digit, a backtick or one of `!#$%&'*+-/=?^_{|}~`, or `.` (wholeEmailLinks).
func rfcAtext(c byte) bool {
	return gfmAtext(c) || strings.IndexByte("!#$%&'*/=?^`{|}~", c) >= 0
}

// endsInRunCharacter reports whether the text node shows ends in a character of an email's run
// (rfcAtext), as the engine reads the node beside a run (wholeEmailLinks): a text's or a string's
// decoded value, a code span's children, raw HTML's segments and an autolink's label end in their
// own last character, and a node holding inline children (emphasis, a link, strikethrough) in its
// last child's. An image holds no text a reader sees beside the address, and neither does a node
// with no children, as a footnote reference.
func endsInRunCharacter(node ast.Node, source []byte) bool {
	var value []byte
	switch node := node.(type) {
	case *ast.Image:
		return false
	case *ast.Text, *ast.String:
		if text, ok := node.(*ast.Text); ok && (text.SoftLineBreak() || text.HardLineBreak()) {
			return false
		}
		decoded := decodeMarkdownText(rawValue(node, source))
		return decoded != "" && rfcAtext(decoded[len(decoded)-1])
	case *ast.CodeSpan:
		if last := node.LastChild(); last != nil {
			value = rawValue(last, source)
		}
	case *ast.RawHTML:
		if count := node.Segments.Len(); count != 0 {
			last := node.Segments.At(count - 1)
			value = last.Value(source)
		}
	case *ast.AutoLink:
		label, _ := autolinkText(node, source)
		return label != "" && rfcAtext(label[len(label)-1])
	default:
		last := node.LastChild()
		return last != nil && endsInRunCharacter(last, source)
	}
	return len(value) != 0 && rfcAtext(value[len(value)-1])
}
