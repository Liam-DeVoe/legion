package pmdoc

import (
	"cmp"
	"errors"
	"slices"
	"strings"
)

// maxEmailFallbackRounds bounds how many times FallBackEmailLinks gives a tree the base's email
// links and reads the result back. Each round renders the tree and parses the rendering twice.
// Every tree the fallback is known to change reads back after one round
// (TestLinkifyStoresEveryShapeTheTesterDraws), so a tree still misread after this many is left to
// the write's check.
const maxEmailFallbackRounds = 4

// FallBackEmailLinks is doc as a write stores it where the renderer cannot write doc's email links
// so that they read back (R4 of INFRA-4224): such an address takes the base's reading, GFM's
// links with Dispatch's rule for the whole address (wholeEmailLinks) not applied. The rule's
// reading of `b-_www.@example.com./bob-smith@example.com` leaves `smith@example.com` unlinked,
// but the renderer writes the earlier email link explicit, and after an explicit link the rule
// links `smith@example.com`, so no spelling of that tree reads back; the base links it there.
//
// doc is rendered and read back (ReadBack). Where the read-back is doc as written (asWritten, as
// documentMisread compares them), doc is answered itself, uncopied, with changed false. Where it
// is not, the rendering is read again with the base's links (parseRenderingAsBase), and in each
// textblock the text where doc and the read-back hold different links, one of them an email link,
// takes the base's link, together with the text of every link of doc, the read-back or the base
// that reaches into it (withBaseEmailLinks); every other mark stays doc's, and neighbouring text
// nodes of one set of marks are joined as the parser joins them. That tree is read back in turn,
// and the fallback repeats until a tree reads back as written, at most maxEmailFallbackRounds
// times; changed reports that one did, and read is its read-back.
//
// Where the textblocks of a tree, its read-back and the base's reading do not pair, or do not hold
// the same text between the same inline nodes, where no link is left to take, or where no round
// reads back, doc is answered unchanged with changed false and its own read-back: a misread of any
// other kind is the write check's (documentMisread). Block ids and every attribute stay doc's.
// doc is never written; the tree answered shares its nodes wherever they are unchanged. err is
// the refusal of doc's own rendering or read-back, where there is no read-back to answer, or any
// error but a refusal (ErrSchema) in a later round, which is this package's bug.
func FallBackEmailLinks(doc *Node) (tree *Node, changed bool, read WrittenReadBack, err error) {
	read, err = readBackWritten(doc, maxSpanCells)
	if err != nil {
		return doc, false, WrittenReadBack{}, err
	}
	if readDifference(asWritten(doc), read.Tree, skip{}) == "" {
		return doc, false, read, nil
	}
	return fallBackEmailLinks(doc, read)
}

// fallBackEmailLinks is FallBackEmailLinks of doc, whose rendering read reads back otherwise.
func fallBackEmailLinks(doc *Node, read WrittenReadBack) (*Node, bool, WrittenReadBack, error) {
	tree, back := doc, read
	for range maxEmailFallbackRounds {
		base, err := parseRenderingAsBase(back.Markdown)
		if err != nil {
			if errors.Is(err, ErrSchema) {
				break
			}
			return doc, false, read, err
		}
		next, ok := withBaseEmailLinks(tree, back.Tree, base)
		if !ok {
			break
		}
		if back, err = readBackWritten(next, maxSpanCells); err != nil {
			if errors.Is(err, ErrSchema) {
				break
			}
			return doc, false, read, err
		}
		if readDifference(asWritten(next), back.Tree, skip{}) == "" {
			return next, true, back, nil
		}
		tree = next
	}
	return doc, false, read, nil
}

// withBaseEmailLinks is tree with the base's email links where back, tree's read-back, reads them
// otherwise (FallBackEmailLinks), base being the base's reading of the same rendering. ok is false
// where the three do not pair block for block (as misreads pairs a block with its read-back,
// writtenChildren), where a textblock's inline nodes do not align (baseEmailInline), or where no
// text takes a link it did not hold. Only the textblocks it changes, and the blocks above them,
// are copies.
func withBaseEmailLinks(tree, back, base *Node) (*Node, bool) {
	var triples []textblockReadings
	if !pairTextblocks(tree, back, base, &triples) {
		return nil, false
	}
	inline := map[*Node][]*Node{}
	for _, triple := range triples {
		children, changed, aligned := baseEmailInline(triple.tree.Children, triple.back.Children, triple.base.Children)
		if !aligned {
			return nil, false
		}
		if changed {
			inline[triple.tree] = children
		}
	}
	if len(inline) == 0 {
		return nil, false
	}
	return withInline(tree, inline), true
}

// textblockReadings is one textblock of a tree, the one it reads back as, and the one the base
// reads in the same rendering.
type textblockReadings struct{ tree, back, base *Node }

// pairTextblocks adds to triples each textblock under tree with the textblock of back and of base
// it pairs with, back and base being two readings of tree's rendering: tree's written children
// (writtenChildren) pair one to one with each reading's children, of the same kinds. It reports
// false where they do not.
func pairTextblocks(tree, back, base *Node, triples *[]textblockReadings) bool {
	if tree.Type != back.Type || tree.Type != base.Type {
		return false
	}
	if isTextblock(tree.Type) {
		*triples = append(*triples, textblockReadings{tree, back, base})
		return true
	}
	written := writtenChildren(tree)
	if len(written) != len(back.Children) || len(written) != len(base.Children) {
		return false
	}
	for index, child := range written {
		if !pairTextblocks(child, back.Children[index], base.Children[index], triples) {
			return false
		}
	}
	return true
}

// baseEmailInline is tree, a textblock's inline nodes, with the base's email links where back
// reads them otherwise (baseEmailRun), base and back being two readings of its rendering, and
// whether that changed a link. Each stretch of text nodes is taken with the stretches of the
// other two between the same inline nodes; aligned is false where the three do not hold inline
// nodes of the same kinds in the same order with the same text between them.
func baseEmailInline(tree, back, base []*Node) (children []*Node, changed, aligned bool) {
	for t, b, g := 0, 0, 0; ; {
		tEnd, bEnd, gEnd := textRunEnd(tree, t), textRunEnd(back, b), textRunEnd(base, g)
		run, runChanged, ok := baseEmailRun(tree[t:tEnd], back[b:bEnd], base[g:gEnd])
		if !ok {
			return nil, false, false
		}
		if runChanged && !changed {
			changed = true
			children = slices.Clone(tree[:t])
		}
		switch {
		case runChanged:
			children = append(children, run...)
		case changed:
			children = append(children, tree[t:tEnd]...)
		}
		ended := tEnd == len(tree)
		if ended != (bEnd == len(back)) || ended != (gEnd == len(base)) {
			return nil, false, false
		}
		if ended {
			return children, changed, true
		}
		if tree[tEnd].Type != back[bEnd].Type || tree[tEnd].Type != base[gEnd].Type {
			return nil, false, false
		}
		if changed {
			children = append(children, tree[tEnd])
		}
		t, b, g = tEnd+1, bEnd+1, gEnd+1
	}
}

// textRunEnd is the index of the first node of nodes from index on that is no text node, or
// len(nodes).
func textRunEnd(nodes []*Node, index int) int {
	for index < len(nodes) && nodes[index].Type == "text" {
		index++
	}
	return index
}

// runText is the text of a stretch of text nodes, written whole.
func runText(nodes []*Node) string {
	if len(nodes) == 1 {
		return nodes[0].Text
	}
	var text strings.Builder
	for _, node := range nodes {
		text.WriteString(node.Text)
	}
	return text.String()
}

// linkPiece is a stretch of a run's text, from start to end, over which each reading holds one
// text node: tree is the tree's, and links the link each reading holds there, or nil, in the order
// treeLink, backLink and baseLink name.
type linkPiece struct {
	start, end int
	tree       *Node
	links      [3]*Mark
}

// The readings of a linkPiece's links: the tree's, its read-back's and the base's.
const (
	treeLink = iota
	backLink
	baseLink
)

// emailMisread reports whether the tree and its read-back hold different links over the piece,
// one of them an email link.
func (piece linkPiece) emailMisread() bool {
	tree, back := piece.links[treeLink], piece.links[backLink]
	return !sameLink(tree, back) && (isEmailLink(tree) || isEmailLink(back))
}

// baseEmailRun is tree, a stretch of text nodes between two inline nodes that are not text, with
// the base's email links where back reads them otherwise, and whether that changed a link; ok is
// false where back's or base's stretch holds other text. The text that takes base's links is
// each piece where tree and back hold different links, one of them an email link (emailMisread),
// and every link of tree, back or base that reaches into it, and every link that reaches into
// those: every piece of each group of overlapping links (linkGroups) that holds such a piece.
// There a node keeps tree's other marks and attributes and holds base's link, or none where base
// holds none. Neighbouring nodes under the same marks are one, as the parser joins them.
func baseEmailRun(tree, back, base []*Node) (run []*Node, changed, ok bool) {
	text := runText(tree)
	if runText(back) != text || runText(base) != text {
		return nil, false, false
	}
	pieces := linkPieces(text, tree, back, base)
	if !slices.ContainsFunc(pieces, linkPiece.emailMisread) {
		return nil, false, true
	}
	region := make([]bool, len(pieces))
	for _, group := range linkGroups(pieces) {
		if !slices.ContainsFunc(pieces[group[0]:group[1]], linkPiece.emailMisread) {
			continue
		}
		for index := group[0]; index < group[1]; index++ {
			region[index] = true
			changed = changed || !sameLink(pieces[index].links[treeLink], pieces[index].links[baseLink])
		}
	}
	if !changed {
		return nil, false, true
	}
	start := 0
	for index, piece := range pieces {
		marks := piece.tree.Marks
		if region[index] {
			marks = withoutMark(slices.Clone(marks), "link")
			if link := piece.links[baseLink]; link != nil {
				marks = append(marks, *link)
			}
			sortMarks(marks)
		}
		if last := len(run) - 1; last >= 0 && marksEqual(run[last].Marks, marks) {
			run[last].Text = text[start:piece.end]
			continue
		}
		start = piece.start
		run = append(run, &Node{Type: "text", Attrs: piece.tree.Attrs, Text: text[piece.start:piece.end], Marks: marks})
	}
	return run, true, true
}

// linkPieces cuts text, which tree, back and base each hold as a stretch of text nodes, wherever
// a node of any of them ends (linkPiece).
func linkPieces(text string, tree, back, base []*Node) []linkPiece {
	cursors := [3]textCursor{{nodes: tree, index: -1}, {nodes: back, index: -1}, {nodes: base, index: -1}}
	var pieces []linkPiece
	for start := 0; start < len(text); {
		piece := linkPiece{start: start, end: len(text)}
		for reading := range cursors {
			node := cursors[reading].at(start)
			piece.links[reading] = linkOf(node)
			piece.end = min(piece.end, cursors[reading].end)
			if reading == treeLink {
				piece.tree = node
			}
		}
		pieces = append(pieces, piece)
		start = piece.end
	}
	return pieces
}

// textCursor walks a stretch of text nodes by offset into their text: index is the node holding
// the last offset asked for, and end where that node's text ends.
type textCursor struct {
	nodes      []*Node
	index, end int
}

// at is the node whose text holds offset, which no earlier question passed.
func (c *textCursor) at(offset int) *Node {
	for c.end <= offset {
		c.index++
		c.end += len(c.nodes[c.index].Text)
	}
	return c.nodes[c.index]
}

// linkGroups is each group of pieces the links of the three readings overlap into, as [start,
// end) indexes into pieces in order: a link is each longest run of pieces one reading holds under
// one link, and two links that share a piece share a group.
func linkGroups(pieces []linkPiece) [][2]int {
	var spans [][2]int
	for reading := range 3 {
		for start := 0; start < len(pieces); {
			link := pieces[start].links[reading]
			end := start + 1
			for link != nil && end < len(pieces) && sameLink(link, pieces[end].links[reading]) {
				end++
			}
			if link != nil {
				spans = append(spans, [2]int{start, end})
			}
			start = end
		}
	}
	slices.SortFunc(spans, func(a, b [2]int) int { return cmp.Compare(a[0], b[0]) })
	var groups [][2]int
	for _, span := range spans {
		if last := len(groups) - 1; last >= 0 && span[0] < groups[last][1] {
			groups[last][1] = max(groups[last][1], span[1])
			continue
		}
		groups = append(groups, span)
	}
	return groups
}

// linkOf is node's link mark, or nil where it holds none.
func linkOf(node *Node) *Mark {
	for index := range node.Marks {
		if node.Marks[index].Type == "link" {
			return &node.Marks[index]
		}
	}
	return nil
}

// sameLink reports whether a and b, two link marks or nil, are one link: both none, or both to
// the same target under the same title.
func sameLink(a, b *Mark) bool {
	if a == nil || b == nil {
		return a == b
	}
	return attrsEqual(a.Attrs, b.Attrs)
}

// isEmailLink reports whether link is a link to a mailto: URL.
func isEmailLink(link *Mark) bool {
	if link == nil {
		return false
	}
	href, _ := link.Attrs["href"].(string)
	return strings.HasPrefix(href, "mailto:")
}

// withInline is node with each textblock inline names holding the inline nodes it maps to: a copy
// of each such textblock and of every block above one, and node's own nodes everywhere else.
func withInline(node *Node, inline map[*Node][]*Node) *Node {
	if children, ok := inline[node]; ok {
		return &Node{Type: node.Type, Attrs: node.Attrs, Text: node.Text, Marks: node.Marks, Children: children}
	}
	if isTextblock(node.Type) {
		return node
	}
	var children []*Node
	for index, child := range node.Children {
		next := withInline(child, inline)
		if next != child && children == nil {
			children = slices.Clone(node.Children)
		}
		if children != nil {
			children[index] = next
		}
	}
	if children == nil {
		return node
	}
	return &Node{Type: node.Type, Attrs: node.Attrs, Text: node.Text, Marks: node.Marks, Children: children}
}
