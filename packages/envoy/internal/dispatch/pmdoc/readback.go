package pmdoc

import (
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sort"
)

// skip names what a comparison leaves aside: the attributes it names, or all of them, and with
// text a textblock's inline content.
type skip struct {
	names map[string]bool
	all   bool
	text  bool
}

// shapeOnly compares blocks alone, their kinds and how they nest.
var shapeOnly = skip{all: true, text: true}

func (s skip) has(name string) bool { return s.all || s.names[name] }

// A document's markdown reads back as written when Parse gives back its blocks, their attributes and
// their text, as the markdown writes them (asWritten). Block ids, a typed block's server-owned
// attributes and anchor marks are not written, so they are not compared, and neither is an empty
// paragraph the renderer does not write (writtenChildren).

// WrittenReadBack is a document a write makes rendered - the markdown the write stores it as -
// and that markdown read back (ParseRendering). Every whole document a write makes is read back
// already, to refuse one that reads back otherwise (RefuseMisreadDocument), so ParseWritten hands
// that reading to the write: a step of the same write that would read the same rendering again -
// the task items its issue records, the blocks its advice counts - reads this tree instead of
// making a second one of the same markdown. Markdown is the rendering this tree was read from,
// which a caller compares with the markdown it stored before it shares the tree: a write that
// renders the tree it stores otherwise - an upload whose server-owned attributes are taken off
// before it is rendered (docs' asUploaded) - stored another rendering, which this is not.
type WrittenReadBack struct {
	Markdown string
	Tree     *Node
}

// ReadBack is what doc's markdown reads back as.
func ReadBack(doc *Node) (*Node, error) {
	written, err := readBackWritten(doc, maxSpanCells)
	return written.Tree, err
}

// readBack is what doc's markdown, its tables' spans adding at most spanCells cells (render),
// reads back as (ParseRendering).
func readBack(doc *Node, spanCells int) (*Node, error) {
	written, err := readBackWritten(doc, spanCells)
	return written.Tree, err
}

// readBackWritten is readBack with the markdown it read back, so a caller that stores that
// markdown shares this reading of it.
func readBackWritten(doc *Node, spanCells int) (WrittenReadBack, error) {
	r, err := render(doc, spanCells)
	if err != nil {
		return WrittenReadBack{}, err
	}
	return readStored(doc, r.b.String())
}

// readStored is markdown, the text doc is stored as, read back as every stored text is read
// (ParseRendering). A refusal of blocks nested past the bound is told by how many blocks doc nests
// (renderedNesting), since the line the parser names counts lines of a text nobody wrote.
func readStored(doc *Node, markdown string) (WrittenReadBack, error) {
	back, err := ParseRendering(markdown)
	return WrittenReadBack{Markdown: markdown, Tree: back}, renderedNesting(doc, err)
}

// NewMisread names how after, which a write made from before, reads back otherwise where before
// did not, or is "" when it does not. Each block that reads back otherwise is found as far down as
// its markdown still pairs with what reads back (misreads), and one that before already read back
// otherwise the same way, under the same block id, is not the write's: a textblock whose text
// differs in the same characters, an attribute with the same values, or a block that pairs with
// nothing. So a write beside a stale misread, or inside the block that holds one, is judged by what
// it changed, while a new misread in that block is still named. An attribute before read back with
// another value anywhere (a table cell's alignment) is not compared. A before whose markdown the
// parser refuses (a browser edit can leave one) gives nothing to judge against, and is "" whether
// or not after parses: the checks that read each changed block alone still refuse one the write
// leaves unreadable. A before whose read-back would pad more cells than a read-back's budget is no
// such document, and is the verdict: its markdown reads, so a write into it is refused rather than
// stored unjudged. Only a refusal (ErrSchema) reading either back is a verdict; any other error, a
// panic (ErrPanic) among them, is this package's bug, not a misread, and is the error.
func NewMisread(before, after *Node) (string, error) {
	return newMisread(before, after, maxSpanCells)
}

// newMisread is NewMisread reading before and after back with their tables' spans adding at most
// spanCells cells each.
func newMisread(before, after *Node, spanCells int) (string, error) {
	backAfter, err := readBack(after, spanCells)
	if err != nil && !errors.Is(err, ErrSchema) {
		return "", err
	}
	written := asWritten(after)
	if err == nil && readDifference(written, backAfter, skip{}) == "" {
		return "", nil
	}
	backBefore, beforeErr := readBack(before, spanCells)
	if beforeErr != nil && !errors.Is(beforeErr, ErrSchema) {
		return "", beforeErr
	}
	if errors.Is(beforeErr, ErrTablePadding) {
		return beforeErr.Error(), nil
	}
	if beforeErr != nil {
		return "", nil
	}
	if err != nil {
		return err.Error(), nil
	}
	previous := asWritten(before)
	drift := skip{names: attributeDrift(previous, backBefore)}
	known := map[sameMisread]bool{}
	for _, stale := range misreads(previous, backBefore, drift) {
		known[sameMisread{stale.id, stale.differs}] = true
	}
	for _, found := range misreads(written, backAfter, drift) {
		if found.id == "" || !known[sameMisread{found.id, found.differs}] {
			return found.reason, nil
		}
	}
	return "", nil
}

// sameMisread is what makes a misread one a document already held (NewMisread,
// RefuseMisreadStored): the block's id and what differs, which is the same for the same misread
// whatever else changed around it.
type sameMisread struct{ id, differs string }

// documentMisread names how doc, a whole document a write makes, reads back otherwise, or is ""
// when it does not: its rendering is judged as the text the write stores (storedMisread), so every
// block that reads back otherwise is the write's, the first named as far down as its markdown
// still pairs (misreads). A doc the renderer refuses, or whose markdown no reader of this package
// reads at all, is that refusal instead, answered as refused, which refuseMisread turns into
// ErrRenderingRefused. Only a refusal (ErrSchema) is a verdict; any other error is the error. It
// answers the read-back it judged as well, which is the zero value where it made none, so a write
// that stores that rendering shares the one parse of it (WrittenReadBack).
func documentMisread(doc *Node) (read WrittenReadBack, misread string, refused error, err error) {
	r, err := render(doc, maxSpanCells)
	if err != nil {
		if errors.Is(err, ErrSchema) {
			return WrittenReadBack{}, "", err, nil
		}
		return WrittenReadBack{}, "", nil, err
	}
	reading, refused, err := storedMisread(doc, r.b.String())
	return reading.read, reading.misread(), refused, err
}

// storedReading is how a stored text reads back (storedMisread): the read-back itself, the first
// way the whole reads back otherwise (readDifference), "" where it reads back as written, and each
// block that reads back otherwise (misreads), which is listed only where difference is not "".
type storedReading struct {
	read       WrittenReadBack
	difference string
	found      []misread
}

// misread names how the stored text reads back otherwise, "" where it does not: the first block
// that does, as far down as its markdown still pairs, or the whole's difference where no block is
// named.
func (s storedReading) misread() string {
	switch {
	case s.difference == "":
		return ""
	case len(s.found) > 0:
		return s.found[0].reason
	}
	return s.difference
}

// storedMisread is how markdown, the text a write stores doc as, reads back: markdown parsed as
// every stored text is read (readStored) and compared with doc as written (asWritten), every
// attribute and every textblock's text and marks. It is the one comparison an upload's rendering is
// judged by (documentMisread) and a write into a document judges the very text it stores by
// (RefuseMisreadStored), so the two cannot disagree. A markdown no reader of this package reads at
// all is that reader's refusal, answered as refused. Only a refusal (ErrSchema) is a verdict; any
// other error is the error.
func storedMisread(doc *Node, markdown string) (reading storedReading, refused error, err error) {
	read, err := readStored(doc, markdown)
	if err != nil {
		if errors.Is(err, ErrSchema) {
			return storedReading{}, err, nil
		}
		return storedReading{}, nil, err
	}
	written := asWritten(doc)
	reading = storedReading{read: read, difference: readDifference(written, read.Tree, skip{})}
	if reading.difference != "" {
		reading.found = misreads(written, read.Tree, skip{})
	}
	return reading, nil, nil
}

// RefuseMisreadDocument refuses (ErrSchema) a whole document a write makes, a spec, an upload or a
// version, that reads back otherwise (documentMisread), and RefuseMisreadWrite a write into a
// document, an insert, whose result reads back otherwise where before did not (NewMisread). What
// is stored is the rendering, which the next read would give back as another document or refuse.
// The rules that read a shape refuse what they know first, so a refusal here names a shape none of
// them reads, and every one of them is logged. Any other error reading it back, a panic (ErrPanic)
// among them, is the error.
func RefuseMisreadDocument(doc *Node) error {
	_, misread, refused, err := documentMisread(doc)
	return refuseMisread(misread, refused, err)
}

// StoredMisread is a block a stored text reads back otherwise in, named without the document's
// text: its block id and the kind of difference (misread.kind) - a paragraph's text, a list item
// that reads back as a paragraph, a heading's level - never what the block holds, so a log line
// may carry it.
type StoredMisread struct {
	Block, Kind string
}

// StoredVerdict is RefuseMisreadStored's verdict on the text a write into a document stores.
// Refusal is nil where the text may be stored, and otherwise the refusal an upload of that text
// gets (ErrMisreadRendering, ErrRenderingRefused); Block is the id of the block it names, "" where
// it names none; Excused is each block that reads back otherwise as it already did in the text the
// document was stored as before the write.
type StoredVerdict struct {
	Refusal error
	Block   string
	Excused []StoredMisread
}

// RefuseMisreadStored judges markdown, the text a write into a document stores after as, by the
// comparison an upload is refused by (storedMisread), so a write stores no text an upload of it
// would be refused for: what is stored is the text, and the next read gives it back as another
// document. The text is judged exactly as it is stored, whatever wrote it, so the verdict does not
// rest on the renderer writing every tree in a spelling that reads back.
//
// One difference is excused: a block that reads back otherwise under the same block id and with
// the same difference (sameMisread) in previous, the text the document was stored as before the
// write, read against before, the tree previous was stored from. So a write beside a block that a
// browser edit or an earlier writer left reading back otherwise is stored, while a new misread in
// that block is still refused. previous is parsed only where markdown reads back otherwise, so a
// write whose text reads back pays for one parse, as an upload does. A nil before - the document
// stood outside the schema, and no text of it was read - excuses nothing, and neither does a
// previous the parser refuses. A refusal of markdown by the parser itself names no block, and is
// never excused. Only a refusal (ErrSchema) is a verdict; any other error, a panic (ErrPanic)
// among them, is the error.
func RefuseMisreadStored(before *Node, previous string, after *Node, markdown string) (StoredVerdict, error) {
	reading, refused, err := storedMisread(after, markdown)
	switch {
	case err != nil:
		return StoredVerdict{}, err
	case refused != nil:
		return StoredVerdict{Refusal: refuseMisread("", refused, nil)}, nil
	case reading.difference == "":
		return StoredVerdict{}, nil
	}
	known, err := storedMisreadsOf(before, previous)
	if err != nil {
		return StoredVerdict{}, err
	}
	var verdict StoredVerdict
	for _, found := range reading.found {
		if found.id != "" && known[sameMisread{found.id, found.differs}] {
			verdict.Excused = append(verdict.Excused, StoredMisread{Block: found.id, Kind: found.kind})
			continue
		}
		if verdict.Refusal == nil {
			verdict.Refusal, verdict.Block = refuseMisread(found.reason, nil, nil), found.id
		}
	}
	if len(reading.found) == 0 {
		verdict.Refusal = refuseMisread(reading.difference, nil, nil)
	}
	return verdict, nil
}

// storedMisreadsOf is each block previous, the text before was stored as, reads back otherwise in
// (sameMisread), judged as RefuseMisreadStored judges a write's text: none where before is nil or
// the parser refuses previous, since then nothing was read back to excuse.
func storedMisreadsOf(before *Node, previous string) (map[sameMisread]bool, error) {
	if before == nil {
		return nil, nil
	}
	reading, refused, err := storedMisread(before, previous)
	if err != nil || refused != nil {
		return nil, err
	}
	known := make(map[sameMisread]bool, len(reading.found))
	for _, stale := range reading.found {
		known[sameMisread{stale.id, stale.differs}] = true
	}
	return known, nil
}

// RefuseMisreadWrite refuses a write into a document; see RefuseMisreadDocument. It reads both
// documents back as renderSpanless writes them. An insert checks its write with it once for each
// operation of a batch, and a batch holds as many as a request carries, where a budget of span
// cells for each read-back would let one batch spend two for every operation. An insert writes
// markdown, which carries no span, between document-level blocks, so it changes no table, and every
// table reads back the same way before and after it under the same block ids: a table that reads
// back otherwise does so in both, and NewMisread's filter of misreads before already held, which
// keys on the block id, sets it aside either way. Spanless, a render writes no more than the cells
// the tables hold, and its parse pads the rows a span leaves short on the read-back's own budget
// (ParseRendering).
func RefuseMisreadWrite(before, after *Node) error {
	misread, err := newMisread(before, after, 0)
	return refuseMisread(misread, nil, err)
}

// ErrRenderingRefused is the refusal of a document whose own rendering no reader of this package
// reads at all. It wraps that reader's own refusal, which names the shape and carries ErrSchema,
// so a caller tests for either with errors.Is.
var ErrRenderingRefused = errors.New("the markdown the document would be stored as cannot be read back")

// ErrMisreadRendering is the refusal of a document whose own rendering reads back as another
// document. A caller that answers such a write with a reason of its own (docs' ask rules) tests
// for this and for ErrRenderingRefused rather than for either sentence.
var ErrMisreadRendering = fmt.Errorf("%w: the markdown the document would be stored as reads back otherwise", ErrSchema)

// refuseMisread is the one place either refusal is built: refused is the rendering's own refusal
// where no reader read it, misread how it reads back otherwise where one did, and err anything
// else, which is the error. Each refusal is logged.
func refuseMisread(misread string, refused, err error) error {
	switch {
	case err != nil:
		return err
	case refused != nil:
		slog.Warn("pmdoc: refused a write whose markdown cannot be read back", "refusal", refused)
		return fmt.Errorf("%w: %w", ErrRenderingRefused, refused)
	case misread == "":
		return nil
	}
	slog.Warn("pmdoc: refused a write whose markdown reads back otherwise", "misread", misread)
	return fmt.Errorf("%w (%s)", ErrMisreadRendering, misread)
}

// misread is a block that reads back otherwise: its id, what differs (the same for the same
// misread whatever else changed around it), the kind of difference, which names block types and
// an attribute's name and never the document's text or an attribute's value (StoredMisread), and
// how a reader is told.
type misread struct {
	id, differs, kind, reason string
}

// misreads lists the blocks under want, a block as written, that read back otherwise in got, in
// document order, each as far down as the two still pair: children pair one to one when both hold
// as many, and otherwise as alignChildren pairs them, a child it cannot pair naming itself.
func misreads(want, got *Node, ignore skip) []misread {
	var found []misread
	collectMisreads(want, got, ignore, &found)
	return found
}

func collectMisreads(want, got *Node, ignore skip, found *[]misread) {
	add := func(id, differs, kind, reason string) {
		*found = append(*found, misread{id: id, differs: differs, kind: kind, reason: reason})
	}
	own := selfDifference(want, got, ignore)
	switch {
	case own.kind != "":
		add(blockIDOf(want), want.Type+" as "+got.Type, want.Type+" as "+got.Type, own.kind)
		return
	case own.attribute != "":
		add(blockIDOf(want), want.Type+" "+own.attribute, want.Type+" "+own.attributeKey, own.attribute)
	}
	if isTextblock(want.Type) {
		if own.text != "" {
			add(blockIDOf(want), want.Type+" text "+own.textDiffers, want.Type+" text", own.text)
		}
		return
	}
	written := writtenChildren(want)
	pairs, unpaired := pairChildren(want.Type, written, got.Children, ignore)
	for _, pair := range pairs {
		collectMisreads(written[pair[0]], got.Children[pair[1]], ignore, found)
	}
	indexes := make([]int, 0, len(unpaired))
	for index := range unpaired {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		child := written[index]
		add(blockIDOf(child), child.Type+" unpaired", child.Type+" unpaired", unpaired[index])
	}
}

// pairChildren pairs written, a parent's children as written, with back's: one to one when both
// hold as many, so a pair that reads back otherwise is looked into, and otherwise as alignChildren
// pairs them, with the children it cannot pair.
func pairChildren(parent string, written, back []*Node, ignore skip) ([][2]int, map[int]string) {
	if len(written) != len(back) {
		return alignChildren(parent, written, back, ignore)
	}
	pairs := make([][2]int, len(written))
	for index := range written {
		pairs[index] = [2]int{index, index}
	}
	return pairs, nil
}

// ownDifference is how a block differs from its read-back apart from its child blocks: its kind,
// its first differing attribute and that attribute's name, and a textblock's text, each "" where
// they agree.
type ownDifference struct {
	kind, attribute, attributeKey, text, textDiffers string
}

func selfDifference(want, got *Node, ignore skip) ownDifference {
	if want.Type != got.Type {
		return ownDifference{kind: blockName(want.Type) + " reads back as " + blockName(got.Type)}
	}
	own := ownDifference{}
	own.attribute, own.attributeKey = attributeReason(want, got, ignore)
	if isTextblock(want.Type) && !ignore.text && !inlineEqual(want.Children, got.Children) {
		wanted, read := TextContent(want), TextContent(got)
		own.text = fmt.Sprintf("%s reads back holding %q, not %q", blockName(want.Type), read, wanted)
		own.textDiffers = differingSpan(wanted, read)
	}
	return own
}

// differingSpan is what wanted and read hold between the text they start and end with alike.
func differingSpan(wanted, read string) string {
	w, r := []rune(wanted), []rune(read)
	prefix := 0
	for prefix < len(w) && prefix < len(r) && w[prefix] == r[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(w)-prefix && suffix < len(r)-prefix && w[len(w)-1-suffix] == r[len(r)-1-suffix] {
		suffix++
	}
	return fmt.Sprintf("%q as %q", string(w[prefix:len(w)-suffix]), string(r[prefix:len(r)-suffix]))
}

// AgreeWithReadBack is doc with its document-level blocks first to last, which a write changed,
// holding what their markdown reads back as where the write left a shape the markdown cannot carry
// but reads back unambiguously, touching only what the write wrote. With halves, an empty
// paragraph without a block id that the renderer does not write goes, as Splice leaves the empty
// halves of the textblock a block replacement lands in. Each list and list item takes the spread
// its markdown reads back with, paired as far down as misreads pairs blocks, so one in a list that
// already reads back otherwise elsewhere takes it too, and so does an item the write left alone
// in a list it made loose, since a list's looseness is its whole markdown's. The exception is one
// before holds unchanged under its id whose spread already read back otherwise there (or when
// before does not read back): that spread is not the write's, and it is kept. Blocks outside
// first to last are doc's own.
func AgreeWithReadBack(before, doc *Node, first, last int, halves bool) *Node {
	out := &Node{Type: doc.Type, Attrs: doc.Attrs, Children: slices.Clone(doc.Children)}
	for index := first; index <= last; index++ {
		out.Children[index] = cloneNode(doc.Children[index])
	}
	if halves {
		kept := make([]*Node, 0, len(out.Children))
		written := writtenChildren(out)
		dropped := 0
		for index, child := range out.Children {
			changed := index >= first && index <= last
			if changed {
				dropUnwrittenHalves(child)
			}
			if changed && !slices.Contains(written, child) && blockIDOf(child) == "" {
				dropped++
				continue
			}
			kept = append(kept, child)
		}
		out.Children = kept
		last -= dropped
	}
	back, err := ReadBack(out)
	if err != nil {
		return out
	}
	held := map[string]*Node{}
	Walk(before, func(node *Node) bool {
		if id := blockIDOf(node); id != "" {
			held[id] = node
		}
		return true
	})
	stale, known := staleSpreads(before)
	spreadDifferences(out, back, first, last, func(node, read *Node) {
		id := blockIDOf(node)
		if previous, ok := held[id]; ok && previous.Equal(node) && (!known || stale[id]) {
			return
		}
		if node.Attrs == nil {
			node.Attrs = Attrs{}
		}
		node.Attrs["spread"] = read.Attrs["spread"]
	})
	return out
}

// staleSpreads is the block ids of before's lists and list items whose spread its markdown already
// reads back otherwise, and false when before does not read back.
func staleSpreads(before *Node) (map[string]bool, bool) {
	back, err := ReadBack(before)
	if err != nil {
		return nil, false
	}
	stale := map[string]bool{}
	spreadDifferences(before, back, 0, len(before.Children)-1, func(node, _ *Node) {
		stale[blockIDOf(node)] = true
	})
	return stale, true
}

// spreadDifferences pairs doc's document-level blocks first to last with back, doc's read-back, as
// misreads does, and calls visit with each list and list item under them and the block it pairs
// with when the two hold different spreads.
func spreadDifferences(doc, back *Node, first, last int, visit func(node, read *Node)) {
	pairs, _ := pairChildren("doc", writtenChildren(StripAnchorMarks(doc)), back.Children, spreadAside)
	writtenIndex := writtenIndexes(doc)
	for _, pair := range pairs {
		if index := writtenIndex[pair[0]]; index >= first && index <= last {
			blockSpreadDifferences(doc.Children[index], back.Children[pair[1]], visit)
		}
	}
}

// spreadAside compares blocks leaving their spread aside.
var spreadAside = skip{names: map[string]bool{"spread": true}}

func dropUnwrittenHalves(node *Node) {
	if isTextblock(node.Type) {
		return
	}
	written := writtenChildren(node)
	kept := node.Children[:0]
	for _, child := range node.Children {
		if !slices.Contains(written, child) && blockIDOf(child) == "" {
			continue
		}
		dropUnwrittenHalves(child)
		kept = append(kept, child)
	}
	node.Children = kept
}

// blockSpreadDifferences is spreadDifferences for node, which pairs with back, and the blocks it
// holds, visiting node before them.
func blockSpreadDifferences(node, back *Node, visit func(node, read *Node)) {
	if node.Type != back.Type || isTextblock(node.Type) {
		return
	}
	if (node.Type == "bullet_list" || node.Type == "ordered_list" || node.Type == "list_item") && node.Attrs["spread"] != back.Attrs["spread"] {
		visit(node, back)
	}
	written := writtenChildren(node)
	pairs, _ := pairChildren(node.Type, written, back.Children, spreadAside)
	for _, pair := range pairs {
		blockSpreadDifferences(written[pair[0]], back.Children[pair[1]], visit)
	}
}

func blockIDOf(node *Node) string {
	id, _ := node.Attrs[BlockIDAttr].(string)
	return id
}

// writtenChildren is node's children as its markdown writes them: an empty paragraph is not
// written, except where the parser reads one as the browser editor does (emptyParagraphFirst), as
// its container's only child, ahead of a list item's first block when that is not a paragraph, and
// ahead of the first block of a typed block whose schema opens with a paragraph
// (opensWithAParagraph), which both parsers fill in again.
func writtenChildren(node *Node) []*Node {
	only := len(node.Children) == 1
	fills := node.Type == "list_item" || opensWithAParagraph(node.Type)
	written := make([]*Node, 0, len(node.Children))
	for index, child := range node.Children {
		readFirst := fills && index == 0 && len(node.Children) > 1 && node.Children[1].Type != "paragraph"
		if only || readFirst || child.Type != "paragraph" || len(child.Children) != 0 {
			written = append(written, child)
		}
	}
	return written
}

// writtenIndexes maps each of doc's written document-level blocks to its index in doc.
func writtenIndexes(doc *Node) []int {
	written := writtenChildren(doc)
	indexes := make([]int, 0, len(written))
	for index, child := range doc.Children {
		if slices.Contains(written, child) {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

// resyncWindow bounds how far alignBlocks looks past a block that reads back otherwise for the next
// pair that reads back as written.
const resyncWindow = 8

// alignBlocks pairs doc's written document-level blocks, anchor marks stripped, with back's, what
// its markdown reads back as (alignChildren), naming each block that reads back otherwise by its
// index in doc.
func alignBlocks(doc, back *Node, ignore skip) ([][2]int, map[int]string) {
	indexes := writtenIndexes(doc)
	pairs, unpaired := alignChildren(doc.Type, writtenChildren(doc), back.Children, ignore)
	misread := make(map[int]string, len(unpaired))
	for index, reason := range unpaired {
		misread[indexes[index]] = reason
	}
	return pairs, misread
}

// alignChildren pairs written blocks, a parent's children, with back's in order: each pair reads
// back as written
// (what ignore names aside), and each written block that does not is named by its index with how
// it reads back. Past a block that reads back otherwise the pairing resumes at the nearest pair
// that reads back as written, so two lists that meet are both named and the blocks after them pair
// again.
func alignChildren(parent string, written, back []*Node, ignore skip) ([][2]int, map[int]string) {
	var pairs [][2]int
	misread := map[int]string{}
	i, j := 0, 0
	for i < len(written) && j < len(back) {
		reason := readDifference(written[i], back[j], ignore)
		if reason == "" {
			pairs = append(pairs, [2]int{i, j})
			i, j = i+1, j+1
			continue
		}
		di, dj, found := resync(written, back, i, j, ignore)
		if !found {
			break
		}
		if di == 0 {
			// back holds blocks the document does not: the block before wrote them.
			misread[max(i-1, 0)] = reason
		}
		for k := i; k < i+di; k++ {
			misread[k] = reason
		}
		i, j = i+di, j+dj
	}
	for k := i; k < len(written); k++ {
		reason := blockName(written[k].Type) + " reads back as nothing"
		if j < len(back) {
			reason = readDifference(written[k], back[j], ignore)
		}
		misread[k] = reason
	}
	if i == len(written) && j < len(back) && len(written) > 0 {
		misread[len(written)-1] = endOf(parent) + " reads back as " + blockName(back[j].Type)
	}
	return pairs, misread
}

// resync finds the nearest blocks past written[i] and back[j] that read back as written, nearest by
// how many blocks it skips on both sides.
func resync(written, back []*Node, i, j int, ignore skip) (int, int, bool) {
	for distance := 1; distance <= 2*resyncWindow; distance++ {
		for di := range distance + 1 {
			dj := distance - di
			if di > resyncWindow || dj > resyncWindow || i+di >= len(written) || j+dj >= len(back) {
				continue
			}
			if readDifference(written[i+di], back[j+dj], ignore) == "" {
				return di, dj, true
			}
		}
	}
	return 0, 0, false
}

// attributeDrift is the attributes doc's blocks read back with other values where they read back
// holding the same blocks and text - but a task item's checked, which is its own item's: a task
// item emptied of its text is written as a plain empty item and reads back with checked none, and
// NewMisread tells a stale one from a new one by its block id, so the attribute drifting would hide
// every other task item emptied.
func attributeDrift(doc, back *Node) map[string]bool {
	drift := map[string]bool{}
	pairs, _ := alignBlocks(doc, back, skip{all: true})
	written := writtenChildren(doc)
	for _, pair := range pairs {
		collectDrift(written[pair[0]], back.Children[pair[1]], drift)
	}
	delete(drift, "checked")
	return drift
}

func collectDrift(node, back *Node, drift map[string]bool) {
	for {
		key, _, _, differs := attributeDifference(node, back, skip{names: drift})
		if !differs {
			break
		}
		drift[key] = true
	}
	children := back.Children
	if isTextblock(node.Type) {
		return
	}
	for index, child := range writtenChildren(node) {
		if index < len(children) {
			collectDrift(child, children[index], drift)
		}
	}
}

// readDifference names how got, what a block's markdown reads back as, differs from want, the
// block as written: its kind, its blocks, its text or an attribute; "" when it reads back as
// written. Attributes ignore names are not compared.
func readDifference(want, got *Node, ignore skip) string {
	own := selfDifference(want, got, ignore)
	if own.kind != "" {
		return own.kind
	}
	// A list's own attributes follow its items, which name a list the next one joins; any other
	// block's come first, which names a task item that reads back plain rather than its text.
	list := want.Type == "bullet_list" || want.Type == "ordered_list"
	if !list && own.attribute != "" {
		return own.attribute
	}
	if isTextblock(want.Type) {
		return own.text
	}
	written := writtenChildren(want)
	for index := range max(len(written), len(got.Children)) {
		switch {
		case index >= len(written):
			if list && got.Children[index].Type == "list_item" {
				// A list of the same kind right after it continues it.
				return blockName(want.Type) + " beside another of its kind reads back as one list"
			}
			return endOf(want.Type) + " reads back as " + blockName(got.Children[index].Type)
		case index >= len(got.Children):
			return blockName(written[index].Type) + " reads back as nothing"
		}
		if reason := readDifference(written[index], got.Children[index], ignore); reason != "" {
			return reason
		}
	}
	return own.attribute
}

// attributeReason names the first attribute want and got hold with different values, and the
// attribute's name, or is "" twice.
func attributeReason(want, got *Node, ignore skip) (reason, key string) {
	key, wanted, read, differs := attributeDifference(want, got, ignore)
	switch {
	case !differs:
		return "", ""
	case want.Type == "list_item" && key == "checked" && read == nil:
		return "a task item reads back as a plain list item", key
	default:
		return fmt.Sprintf("%s reads back with %s %v, not %v", blockName(want.Type), key, orNone(read), orNone(wanted)), key
	}
}

func orNone(value any) any {
	if value == nil {
		return "none"
	}
	return value
}

// attributeDifference is the first attribute, by name, that want and got hold with different
// values, block ids, server-owned attributes (tokenAttrs) and those ignore names aside.
func attributeDifference(want, got *Node, ignore skip) (string, any, any, bool) {
	if ignore.all {
		return "", nil, nil, false
	}
	wanted, read := writtenAttrs(want), tokenAttrs(got.Type, got.Attrs)
	// The capacity is a hint that only moves where a growth happens, so it names the written
	// attributes alone rather than their sum with the read ones: a sum of two lengths is what
	// CodeQL's go/allocation-size-overflow refuses, and the same keys are appended in the same
	// order either way.
	keys := make([]string, 0, len(wanted))
	for key := range wanted {
		keys = append(keys, key)
	}
	for key := range read {
		if _, ok := wanted[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !ignore.has(key) && !reflect.DeepEqual(wanted[key], read[key]) {
			return key, wanted[key], read[key], true
		}
	}
	return "", nil, nil, false
}

// writtenAttrs is the attributes node's markdown writes: tokenAttrs, with a table column that has
// no alignment - null, or the "none" this parser once stored - written unaligned (tableAlignment),
// which reads back with none.
func writtenAttrs(node *Node) Attrs {
	attrs := tokenAttrs(node.Type, node.Attrs)
	if node.Type == "table_header" || node.Type == "table_cell" {
		switch attrs["alignment"] {
		case "left", "center", "right":
		default:
			delete(attrs, "alignment")
		}
	}
	return attrs
}

// inlineEqual compares a textblock's inline content: each node's kind, text, attributes and marks,
// the marks in any order.
func inlineEqual(want, got []*Node) bool {
	if len(want) != len(got) {
		return false
	}
	for index := range want {
		w, g := want[index], got[index]
		if w.Type != g.Type || w.Text != g.Text || !reflect.DeepEqual(tokenAttrs(w.Type, w.Attrs), tokenAttrs(g.Type, g.Attrs)) || !marksEqual(sortedMarks(w.Marks), sortedMarks(g.Marks)) || !inlineEqual(w.Children, g.Children) {
			return false
		}
	}
	return true
}

func sortedMarks(marks []Mark) []Mark {
	sorted := slices.Clone(marks)
	sortMarks(sorted)
	return sorted
}
