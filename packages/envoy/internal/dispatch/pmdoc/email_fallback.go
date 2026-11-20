package pmdoc

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

// maxEmailFallbackRounds bounds how many times the fallback's first tier (baseLinksTier) gives a
// tree the base's links and reads the result back. Each round renders the tree and parses the
// rendering twice. Every tree the tier is known to change reads back after one round
// (TestLinkifyStoresEveryClassicSeedShape), so a tree still misread after this many goes on to the
// next tier.
const maxEmailFallbackRounds = 4

// fallbackTier is where the write path's fallback (R4 of INFRA-4224) found the tree a write
// stores, its tiers tried in this order where a tree's rendering reads back otherwise, whatever
// the misread's cause (a link, emphasis beside it, a web address). The base reader is GFM's links
// with Dispatch's rule for the whole address (wholeEmailLinks) not applied (baseEmailReader), how
// an address was read before that rule.
type fallbackTier int

const (
	// noFallback: the tree reads back as written, or no tier gives a tree the write stores.
	noFallback fallbackTier = iota
	// baseLinksTier: the tree with the base reader's links where the rule's and the base's
	// readings of its rendering hold different links, or the tree and the rule's reading do with
	// one of them an email link, in each textblock that reads back otherwise (withBaseLinks),
	// which reads back as written.
	baseLinksTier
	// baseReadingTier: the base reader's whole reading of the text, which reads back as written:
	// on a write whose markdown is known (ParseWritten) that markdown read by the base reader with
	// the block ids its parse for the write gives (writtenAsBase), and on the live path the tree's
	// rendering read by the base reader (withBaseReading).
	baseReadingTier
	// baseStoredTier: that reading as the base stores it, rendered as the build before the rule
	// wrote it where no spelling reads back under the rule (inlineWithEscapes): its rendering reads
	// back as written, each textblock the rule reads otherwise read by the base reader
	// (readBackEither). It is the one stored rendering that does not read back as itself under the
	// rule, since refusing a text the base stores is the larger fault.
	baseStoredTier
)

// FallBackEmailLinks is doc as a write stores it where the renderer cannot write doc so that it
// reads back (R4 of INFRA-4224): where doc's rendering reads back as doc as written (asWritten,
// as documentMisread compares them), doc is answered itself, uncopied, with changed false.
// Otherwise the first of the fallback's tiers (fallbackTier) that gives a tree the write stores
// answers it. The rule's reading of `b-_www.@example.com./bob-smith@example.com` leaves
// `smith@example.com` unlinked, but the renderer writes the earlier email link explicit, and after
// an explicit link the rule links `smith@example.com`, so no spelling of that tree reads back; the
// first tier gives it the base's link there.
//
// No writer's markdown stands behind a live document's tree, so the second tier reads the tree's
// rendering with the base's links, keeping each block of doc with its id and attributes and taking
// that reading's inline nodes only for a textblock that reads back otherwise and whose text holds
// an `@` (withBaseReading); where such a textblock holds a mark the rendering does not write, a
// comment's anchor, the second and third tiers give no tree, so no tier drops one. The third tier
// stores that tree, or doc itself where every textblock that reads back otherwise already holds
// the base's reading: then doc is answered with changed false, and the write stores doc's
// rendering. A doc whose text holds no `@` takes no tier (fallBack).
//
// Where no tier gives a tree the write stores, doc is answered unchanged with changed false, and a
// write refuses it (documentMisread). doc is never written; the tree answered shares its nodes
// wherever they are unchanged. err is the refusal of doc's own rendering or read-back, or any
// error but a refusal (ErrSchema) in a later step, which is this package's bug.
func FallBackEmailLinks(doc *Node) (tree *Node, changed bool, err error) {
	read, err := readBackWritten(doc, maxSpanCells)
	if err != nil {
		return doc, false, err
	}
	return fallBackEmailLinksRead(doc, read)
}

// FallBackEmailLinksRendered is FallBackEmailLinks of doc whose rendering, as Render writes it, is
// rendering, for a caller that rendered doc already: it reads that rendering back rather than
// rendering doc again.
func FallBackEmailLinksRendered(doc *Node, rendering string) (tree *Node, changed bool, err error) {
	read, err := readBackRendering(doc, rendering)
	if err != nil {
		return doc, false, err
	}
	return fallBackEmailLinksRead(doc, read)
}

// fallBackEmailLinksRead is FallBackEmailLinks of doc, whose rendering read reads back as it
// does, otherwise or not.
func fallBackEmailLinksRead(doc *Node, read WrittenReadBack) (*Node, bool, error) {
	tree, _, _, err := fallBackLive(doc, read)
	return tree, tree != doc, err
}

// fallBackLive is FallBackEmailLinks of doc, whose rendering read reads back as it does, with the
// tier that gave the tree answered and that tree's read-back.
func fallBackLive(doc *Node, read WrittenReadBack) (*Node, fallbackTier, WrittenReadBack, error) {
	if readsBackAsWritten(doc, read.Tree) {
		return doc, noFallback, read, nil
	}
	return fallBack(doc, read, func(base *Node) *Node { return withBaseReading(doc, read.Tree, base) }, readsBackDone, readsBackStored)
}

// readsBackDone takes a tree of the first two tiers whose rendering reads back as written.
func readsBackDone(next *Node, back WrittenReadBack) (bool, error) {
	return readsBackAsWritten(next, back.Tree), nil
}

// readsBackStored takes the third tier's tree where back, its rendering's read-back with each
// textblock the rule reads otherwise read by the base reader (readBackEither), is the tree as
// written.
func readsBackStored(next, back *Node) (bool, error) {
	return readsBackAsWritten(next, back), nil
}

// fallBack is the write path's fallback for doc, whose rendering read reads back otherwise: the
// tree of the first tier (fallbackTier) that gives one a write stores, that tier, and the tree's
// read-back, or doc, noFallback and read where no tier does. reading is the second tier's tree,
// given the base's reading of doc's rendering, or nil where there is none. done judges a tree of
// the first two tiers by its read-back; stored judges the third tier's by its rendering's
// read-back with each textblock the rule reads otherwise read by the base reader
// (readBackEither). The third tier's tree is reading, or doc where reading is doc. err is any
// error but a refusal (ErrSchema), which is this package's bug; a refused step ends its tier.
//
// A reader links an address only from text holding an `@`, so where doc's text holds none
// (textHoldsAt) every reading of the base is the rule's, and no tier is tried: a tree of the base
// there would only be the misread itself.
func fallBack(doc *Node, read WrittenReadBack, reading func(base *Node) *Node, done func(next *Node, back WrittenReadBack) (bool, error), stored func(next, back *Node) (bool, error)) (*Node, fallbackTier, WrittenReadBack, error) {
	if !textHoldsAt(doc) {
		return doc, noFallback, read, nil
	}
	base, err := parseRenderingAsBase(read.Markdown)
	if err != nil {
		return doc, noFallback, read, notRefusal(err)
	}
	tree, back, treeBase := doc, read, base
	for range maxEmailFallbackRounds {
		next, ok := withBaseLinks(tree, back.Tree, treeBase)
		if !ok {
			break
		}
		if back, err = readBackWritten(next, maxSpanCells); err != nil {
			if errors.Is(err, ErrSchema) {
				break
			}
			return doc, noFallback, read, err
		}
		finished, err := done(next, back)
		if err != nil {
			return doc, noFallback, read, err
		}
		if finished {
			return next, baseLinksTier, back, nil
		}
		tree = next
		if treeBase, err = parseRenderingAsBase(back.Markdown); err != nil {
			if errors.Is(err, ErrSchema) {
				break
			}
			return doc, noFallback, read, err
		}
	}
	whole := reading(base)
	if whole == nil {
		return doc, noFallback, read, nil
	}
	wholeBack, wholeBase := read, base
	if whole != doc {
		if wholeBack, err = readBackWritten(whole, maxSpanCells); err != nil {
			return doc, noFallback, read, notRefusal(err)
		}
		finished, err := done(whole, wholeBack)
		if err != nil {
			return doc, noFallback, read, err
		}
		if finished {
			return whole, baseReadingTier, wholeBack, nil
		}
		if wholeBase, err = parseRenderingAsBase(wholeBack.Markdown); err != nil {
			return doc, noFallback, read, notRefusal(err)
		}
	}
	finished, err := stored(whole, readBackEither(whole, wholeBack.Tree, wholeBase))
	if err != nil {
		return doc, noFallback, read, err
	}
	if !finished {
		return doc, noFallback, read, nil
	}
	return whole, baseStoredTier, wholeBack, nil
}

// notRefusal is err where it is no refusal (ErrSchema), and nil where it is one.
func notRefusal(err error) error {
	if errors.Is(err, ErrSchema) {
		return nil
	}
	return err
}

// FallBackMisread is after, the tree an edit over before leaves, as the edit stores it, and the
// misread NewMisread names in it, "" where none is left. Where after reads back otherwise in a way
// before did not, it is the tree of the first of the fallback's tiers (fallbackTier) that leaves
// no such misread, so a block that read back otherwise before the edit is still not the edit's;
// the third tier's tree is one whose blocks the rule reads otherwise each read back, under the
// base reader, as before did not read them otherwise (misreadSince over readBackEither). Where no
// tier gives one, after is answered with its misread. err is any error but a refusal, which is
// this package's bug.
func FallBackMisread(before, after *Node) (tree *Node, misread string, err error) {
	return fallBackMisread(before, after, maxSpanCells)
}

// FallBackMisreadWrite is FallBackMisread for a write RefuseMisreadWrite judges: the tree the write
// stores, or that refusal.
func FallBackMisreadWrite(before, after *Node) (*Node, error) {
	tree, misread, err := fallBackMisread(before, after, 0)
	if err := refuseMisread(misread, nil, err); err != nil {
		return nil, err
	}
	return tree, nil
}

// fallBackMisread is FallBackMisread reading before and each tree back with their tables' spans
// adding at most spanCells empty cells, as newMisread does.
func fallBackMisread(before, after *Node, spanCells int) (*Node, string, error) {
	tree, _, misread, err := fallBackMisreadTier(before, after, spanCells)
	return tree, misread, err
}

// fallBackMisreadTier is fallBackMisread with the tier that gave the tree answered, noFallback
// where after is answered itself.
func fallBackMisreadTier(before, after *Node, spanCells int) (*Node, fallbackTier, string, error) {
	misread, err := newMisread(before, after, spanCells)
	if err != nil || misread == "" {
		return after, noFallback, misread, err
	}
	read, err := readBackWritten(after, maxSpanCells)
	switch {
	case errors.Is(err, ErrSchema):
		return after, noFallback, misread, nil
	case err != nil:
		return after, noFallback, "", err
	}
	fallen, tier, _, err := fallBack(after, read,
		func(base *Node) *Node { return withBaseReading(after, read.Tree, base) },
		func(next *Node, _ WrittenReadBack) (bool, error) {
			left, err := newMisread(before, next, spanCells)
			return left == "", err
		},
		func(next, back *Node) (bool, error) {
			left, err := misreadSince(before, next, back, nil, spanCells)
			return left == "", err
		})
	if err != nil || tier == noFallback {
		return after, noFallback, misread, err
	}
	return fallen, tier, "", nil
}

// writtenAsBase is markdown, a write's, read by the base reader, with the block ids and attributes
// doc, the write's parse of the same markdown (parseForWrite), gives its blocks: doc with each
// textblock holding the base's inline nodes where they differ, doc itself where none does, or nil
// where the base reader refuses the markdown or reads other blocks. It is the second tier's tree
// of a write whose markdown is known (baseReadingTier).
func writtenAsBase(markdown string, doc *Node) (*Node, error) {
	base, err := parseMarkdown(baseEmailReader, LineFeeds(markdown), fromStart, true, NewWriteBudget(), nil)
	if err != nil {
		return nil, notRefusal(err)
	}
	inline := map[*Node][]*Node{}
	if !pairReadings(doc, base, inline) {
		return nil, nil
	}
	if len(inline) == 0 {
		return doc, nil
	}
	return withInline(doc, inline), nil
}

// pairReadings adds to inline each textblock of doc whose inline nodes reading, another reader's
// tree of the same markdown, holds otherwise, mapped to reading's. It reports false where the two
// do not hold blocks of the same kinds, one for one.
func pairReadings(doc, reading *Node, inline map[*Node][]*Node) bool {
	if doc.Type != reading.Type {
		return false
	}
	if isTextblock(doc.Type) {
		if !inlineEqual(doc.Children, reading.Children) {
			inline[doc] = reading.Children
		}
		return true
	}
	if len(doc.Children) != len(reading.Children) {
		return false
	}
	for index, child := range doc.Children {
		if !pairReadings(child, reading.Children[index], inline) {
			return false
		}
	}
	return true
}

// withBaseReading is the live path's second tier (baseReadingTier): doc, whose rendering reads
// back as back and as base under the base reader, with each textblock that reads back otherwise
// and whose text holds an `@` (textHoldsAt) holding base's inline nodes, where they are not doc's
// as written already: in a textblock whose text holds none, base reads what the rule reads, the
// misread itself. Every block keeps doc's node, its id and every attribute. It is doc itself where
// no textblock changes, and nil where the three do not pair block for block (pairTextblocks) or a
// textblock to change holds a mark the rendering does not write (an anchor), which base's reading
// would drop.
func withBaseReading(doc, back, base *Node) *Node {
	var triples []textblockReadings
	if !pairTextblocks(doc, back, base, &triples) {
		return nil
	}
	inline := map[*Node][]*Node{}
	for _, triple := range triples {
		written := writtenInline(triple.tree)
		if inlineEqual(written, triple.back.Children) || inlineEqual(written, triple.base.Children) || !textHoldsAt(triple.tree) {
			continue
		}
		if holdsUnwrittenMark(triple.tree.Children) {
			return nil
		}
		inline[triple.tree] = triple.base.Children
	}
	if len(inline) == 0 {
		return doc
	}
	return withInline(doc, inline)
}

// textHoldsAt reports whether a text node at or under node holds an `@`.
func textHoldsAt(node *Node) bool {
	held := false
	Walk(node, func(child *Node) bool {
		held = child.Type == "text" && strings.IndexByte(child.Text, '@') >= 0
		return !held
	})
	return held
}

// holdsUnwrittenMark reports whether a node among nodes, or under one, holds a mark the rendering
// does not write (renderedMarkTypes).
func holdsUnwrittenMark(nodes []*Node) bool {
	for _, node := range nodes {
		for _, mark := range node.Marks {
			if !renderedMarkTypes[mark.Type] {
				return true
			}
		}
		if holdsUnwrittenMark(node.Children) {
			return true
		}
	}
	return false
}

// readBackEither is back, what tree's rendering reads back as, with each textblock that reads
// back otherwise than tree writes it holding base's inline nodes, base being the base reader's
// reading of the same rendering, where base reads it as tree writes it: the reading the third
// tier (baseStoredTier) holds its tree to. It is back where the three do not pair block for block.
func readBackEither(tree, back, base *Node) *Node {
	var triples []textblockReadings
	if !pairTextblocks(tree, back, base, &triples) {
		return back
	}
	inline := map[*Node][]*Node{}
	for _, triple := range triples {
		written := writtenInline(triple.tree)
		if !inlineEqual(written, triple.back.Children) && inlineEqual(written, triple.base.Children) {
			inline[triple.back] = triple.base.Children
		}
	}
	if len(inline) == 0 {
		return back
	}
	return withInline(back, inline)
}

// writtenInline is a textblock's inline nodes as its markdown writes them, as asWritten writes
// them in a document.
func writtenInline(textblock *Node) []*Node {
	return nulsWrittenTree(writtenTree(textblock, true)).Children
}

// withBaseLinks is the first tier (baseLinksTier): tree with the base's links where back, tree's
// read-back, and base, the base reader's reading of the same rendering, read them otherwise,
// in each textblock that reads back otherwise than tree writes it (baseLinksInline). ok is false
// where the three do not pair block for block (as misreads pairs a block with its read-back,
// writtenChildren) or where no text takes a link it did not hold; a textblock whose inline nodes
// do not align keeps its own. Only the textblocks it changes, and the blocks above them, are
// copies.
func withBaseLinks(tree, back, base *Node) (*Node, bool) {
	var triples []textblockReadings
	if !pairTextblocks(tree, back, base, &triples) {
		return nil, false
	}
	inline := map[*Node][]*Node{}
	for _, triple := range triples {
		if inlineEqual(writtenInline(triple.tree), triple.back.Children) {
			continue
		}
		children, changed, aligned := baseLinksInline(triple.tree.Children, triple.back.Children, triple.base.Children)
		if aligned && changed {
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

// baseLinksInline is tree, a textblock's inline nodes, with the base's links where back reads
// them otherwise (baseLinksRun), base and back being two readings of its rendering, and whether
// that changed a link. Each stretch of text nodes is taken with the stretches of the other two
// between the same inline nodes; aligned is false where the three do not hold inline nodes of the
// same kinds in the same order, or where a link to take reaches text the tree holds otherwise
// than the readings do.
func baseLinksInline(tree, back, base []*Node) (children []*Node, changed, aligned bool) {
	for t, b, g := 0, 0, 0; ; {
		tEnd, bEnd, gEnd := textRunEnd(tree, t), textRunEnd(back, b), textRunEnd(base, g)
		run, runChanged, ok := baseLinksRun(tree[t:tEnd], back[b:bEnd], base[g:gEnd])
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

// runAlignment places a run's text as the tree holds it against the text both readings of its
// rendering hold there, which may differ where the rendering reads back otherwise (`)` under
// emphasis read back as `*)*`): the two agree on their first prefix bytes and their last suffix
// bytes, each cut at a character's start, and the middle between is the tree's own, which no
// offset of the readings' maps to. treeLen and readLen are the two texts' lengths.
type runAlignment struct {
	prefix, suffix, treeLen, readLen int
}

// alignRun is the runAlignment of tree, a run's text, with read, the readings' text there.
func alignRun(tree, read string) runAlignment {
	prefix := 0
	for prefix < len(tree) && prefix < len(read) && tree[prefix] == read[prefix] {
		prefix++
	}
	for prefix > 0 && (prefix < len(tree) && !utf8.RuneStart(tree[prefix]) || prefix < len(read) && !utf8.RuneStart(read[prefix])) {
		prefix--
	}
	suffix := 0
	for suffix < len(tree)-prefix && suffix < len(read)-prefix && tree[len(tree)-1-suffix] == read[len(read)-1-suffix] {
		suffix++
	}
	for suffix > 0 && !utf8.RuneStart(tree[len(tree)-suffix]) {
		suffix--
	}
	return runAlignment{prefix: prefix, suffix: suffix, treeLen: len(tree), readLen: len(read)}
}

// treeOffset is the tree's offset where a piece starting at start in the readings' text starts,
// or -1 for a piece inside the middle: a piece at the end of the prefix starts the suffix where
// no middle of the readings' stands between them.
func (a runAlignment) treeOffset(start int) int {
	switch {
	case start < a.prefix:
		return start
	case start >= a.readLen-a.suffix:
		return start - a.readLen + a.treeLen
	}
	return -1
}

// linkPiece is a stretch of a run's text, from start to end in the readings' text, over which
// each reading holds one text node: tree is the tree's, at treeStart in the tree's text, or nil
// inside the middle (runAlignment), and links the link each reading holds there, or nil, in the
// order treeLink, backLink and baseLink name.
type linkPiece struct {
	start, end int
	tree       *Node
	treeStart  int
	links      [3]*Mark
}

// The readings of a linkPiece's links: the tree's, its read-back's and the base's.
const (
	treeLink = iota
	backLink
	baseLink
)

// misread reports whether the piece holds a link the base reads otherwise than the read-back
// does, or the tree and its read-back hold different links there, one of them an email link.
func (piece linkPiece) misread() bool {
	tree, back, base := piece.links[treeLink], piece.links[backLink], piece.links[baseLink]
	return !sameLink(back, base) || piece.tree != nil && !sameLink(tree, back) && (isEmailLink(tree) || isEmailLink(back))
}

// baseLinksRun is tree, a stretch of text nodes between two inline nodes that are not text, with
// the base's links where back reads them otherwise, and whether that changed a link; ok is false
// where back's and base's stretches hold other text, or where a link to take reaches the middle
// of the tree's text (runAlignment). The text that takes base's links is each piece where back and
// base hold different links, or tree and back do with one of them an email link
// (linkPiece.misread), and every link of tree, back or base that reaches into it, and every link
// that reaches into those: every piece of each group of overlapping links (linkGroups) that holds
// such a piece. There a node keeps tree's other marks and attributes and holds base's link, or
// none where base holds none. Neighbouring nodes under the same marks are one, as the parser
// joins them.
func baseLinksRun(tree, back, base []*Node) (run []*Node, changed, ok bool) {
	text := runText(back)
	if runText(base) != text {
		return nil, false, false
	}
	treeText := runText(tree)
	align := alignRun(treeText, text)
	pieces := linkPieces(align, tree, back, base)
	if !slices.ContainsFunc(pieces, linkPiece.misread) {
		return nil, false, true
	}
	region := make([]bool, len(pieces))
	for _, group := range linkGroups(pieces) {
		if !slices.ContainsFunc(pieces[group[0]:group[1]], linkPiece.misread) {
			continue
		}
		for index := group[0]; index < group[1]; index++ {
			if pieces[index].tree == nil {
				return nil, false, false
			}
			region[index] = true
			changed = changed || !sameLink(pieces[index].links[treeLink], pieces[index].links[baseLink])
		}
	}
	if !changed {
		return nil, false, true
	}
	// from is where the node last added starts in the tree's text, which a piece under the same
	// marks extends.
	from := 0
	add := func(node *Node, start, end int, marks []Mark) {
		if last := len(run) - 1; last >= 0 && marksEqual(run[last].Marks, marks) {
			run[last].Text = treeText[from:end]
			return
		}
		from = start
		run = append(run, &Node{Type: "text", Attrs: node.Attrs, Text: treeText[start:end], Marks: marks})
	}
	// middle adds the tree's own nodes over the middle of its text, once.
	middleAdded := false
	middle := func() {
		if middleAdded {
			return
		}
		middleAdded = true
		start, end := align.prefix, align.treeLen-align.suffix
		offset := 0
		for _, node := range tree {
			nodeEnd := offset + len(node.Text)
			if nodeEnd > start && offset < end {
				add(node, max(offset, start), min(nodeEnd, end), node.Marks)
			}
			offset = nodeEnd
		}
	}
	for index, piece := range pieces {
		if piece.tree == nil || piece.start >= align.readLen-align.suffix {
			middle()
		}
		if piece.tree == nil {
			continue
		}
		marks := piece.tree.Marks
		if region[index] {
			marks = withoutMark(slices.Clone(marks), "link")
			if link := piece.links[baseLink]; link != nil {
				marks = append(marks, *link)
			}
			sortMarks(marks)
		}
		add(piece.tree, piece.treeStart, piece.treeStart+piece.end-piece.start, marks)
	}
	middle()
	return run, true, true
}

// linkPieces cuts the readings' text of a run, which back and base each hold as a stretch of text
// nodes and tree holds as align places it, wherever a node of any of them ends and where the
// middle starts and ends (linkPiece).
func linkPieces(align runAlignment, tree, back, base []*Node) []linkPiece {
	cursors := [3]textCursor{{nodes: tree, index: -1}, {nodes: back, index: -1}, {nodes: base, index: -1}}
	middleEnd := align.readLen - align.suffix
	var pieces []linkPiece
	for start := 0; start < align.readLen; {
		piece := linkPiece{start: start, end: align.readLen, treeStart: -1}
		switch {
		case start < align.prefix:
			piece.end = align.prefix
		case start < middleEnd:
			piece.end = middleEnd
		}
		for reading := range cursors {
			offset := start
			if reading == treeLink {
				if offset = align.treeOffset(start); offset < 0 {
					continue
				}
			}
			node := cursors[reading].at(offset)
			piece.links[reading] = linkOf(node)
			end := cursors[reading].end
			if reading == treeLink {
				// The tree's node ends at end in the tree's text: in the prefix that is the
				// readings' offset too, and a node reaching past the prefix leaves the piece
				// ending there; in the suffix the readings' offset is that moved by the
				// difference of the two texts' lengths.
				piece.tree, piece.treeStart = node, offset
				if start >= align.prefix {
					end += align.readLen - align.treeLen
				}
			}
			piece.end = min(piece.end, end)
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

// isEmailLink reports whether link is a link to a mailto: URL, its scheme in any case, as the email
// rule reads one (mailtoHref).
func isEmailLink(link *Mark) bool {
	if link == nil {
		return false
	}
	href, _ := link.Attrs["href"].(string)
	return mailtoHref(href)
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
