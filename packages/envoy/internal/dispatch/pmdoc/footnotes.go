package pmdoc

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extensionast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	gmtext "github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// footnoteReferenceParser reads a footnote reference (`[^label]`, or `![^label]` with its `!` left
// as text) as goldmark's footnote reference parser (extension.NewFootnoteParser) reads it, with two
// differences. It finds the definition whose label equals the reference's byte for byte in a map
// built once for the document (exactFootnoteReference), where goldmark's parser compares the
// reference with every definition in the document's footnote list, so a body of n references and
// n definitions cost n² comparisons. And a reference whose label matches a definition's only up to
// case (footnoteLabelKey) resolves to that definition, as the browser editor's parser resolves it:
// goldmark matches a label byte for byte, and read `[^A]` as text beside `[^a]: `. Such a reference
// carries its label as written (referenceLabelAttr), which the browser editor keeps on it.
type footnoteReferenceParser struct{}

// Trigger is goldmark's footnote reference parser's: a reference opens at `[`, and at the `!` of
// `![^label]`, which goldmark's image parser would take otherwise.
func (footnoteReferenceParser) Trigger() []byte { return []byte{'!', '['} }

// footnoteLabelKey is a footnote label as the browser editor's parser compares it: with full case
// mapping, lowered and then uppercased, so `ß` matches `SS` and `İ` matches `i̇`, which simple case
// folding keeps apart.
func footnoteLabelKey(label string) string {
	return cases.Upper(language.Und).String(cases.Lower(language.Und).String(label))
}

// referenceLabelAttr is the label a reference resolved by footnoteReferenceParser is written with.
var referenceLabelAttr = []byte("pmdoc-reference-label")

func (p footnoteReferenceParser) Parse(parent ast.Node, block gmtext.Reader, pc parser.Context) ast.Node {
	row, position := block.Position()
	if node := exactFootnoteReference(parent, block, pc); node != nil {
		return node
	}
	block.SetPosition(row, position)
	line, segment := block.PeekLine()
	start := 1
	if len(line) > 0 && line[0] == '!' {
		start++
	}
	if start+1 >= len(line) || line[start] != '^' {
		return nil
	}
	closure := util.FindClosure(line[start+1:], '[', ']', false, false) //nolint:staticcheck
	if closure < 0 {
		return nil
	}
	label := line[start+1 : start+1+closure]
	definition := footnoteDefinitionsByKey(pc)[footnoteLabelKey(string(label))]
	if definition == nil {
		return nil
	}
	if definition.Index < 0 {
		list := definition.Parent().(*extensionast.FootnoteList)
		list.Count++
		definition.Index = list.Count
	}
	block.Advance(start + 1 + closure + 1)
	link := extensionast.NewFootnoteLink(definition.Index)
	link.SetAttribute(referenceLabelAttr, string(label))
	if line[0] == '!' {
		parent.AppendChild(parent, ast.NewTextSegment(gmtext.NewSegment(segment.Start, segment.Start+1)))
	}
	return link
}

// exactFootnoteReference is goldmark's footnoteParser.Parse (extension/footnote.go:125-185 at
// v1.8.6) with its walk of the footnote list (lines 154-165) replaced by a lookup in
// footnoteDefinitionsByRef. It gives goldmark's answers: the label read to its closer by
// util.FindClosure, whose length nothing bounds (goldmark's 999-character bound on a link label,
// parser/link.go:153, 188 and 288, is the link parser's alone); the first definition in the list
// whose label equals it byte for byte; the Index goldmark gives, the definition's place in the
// order of first references, set at its first; and nil where no definition matches or the document
// holds none. goldmark's parser also records each link it makes for its footnote transformer,
// which these readers do not register (footnoteParserOptions). goldmark could take the same change
// as a map of its FootnoteList kept by its definition parser's Close.
func exactFootnoteReference(parent ast.Node, block gmtext.Reader, pc parser.Context) ast.Node {
	line, segment := block.PeekLine()
	pos := 1
	if len(line) > 0 && line[0] == '!' {
		pos++
	}
	if pos >= len(line) || line[pos] != '^' {
		return nil
	}
	pos++
	if pos >= len(line) {
		return nil
	}
	open := pos
	closure := util.FindClosure(line[pos:], '[', ']', false, false) //nolint:staticcheck
	if closure < 0 {
		return nil
	}
	closes := pos + closure
	value := block.Value(gmtext.NewSegment(segment.Start+open, segment.Start+closes))
	block.Advance(closes + 1)
	definition := footnoteDefinitionsByRef(pc)[string(value)]
	if definition == nil {
		return nil
	}
	if definition.Index < 0 {
		list := definition.Parent().(*extensionast.FootnoteList)
		list.Count++
		definition.Index = list.Count
	}
	link := extensionast.NewFootnoteLink(definition.Index)
	if line[0] == '!' {
		parent.AppendChild(parent, ast.NewTextSegment(gmtext.NewSegment(segment.Start, segment.Start+1)))
	}
	return link
}

// footnoteRefsKey holds a parse's footnoteDefinitionsByRef.
var footnoteRefsKey = parser.NewContextKey()

// footnoteDefinitionsByRef is each footnote definition in goldmark's list by its label as written,
// the first in the list where two share one, as goldmark's walk of the list finds it. It is built
// once per parse, at the first reference: inline text is parsed after every block has closed, so
// every definition stands in the list by then. The list is the one goldmark's definition parser
// gathers them in, which each definition's slot names.
func footnoteDefinitionsByRef(pc parser.Context) map[string]*extensionast.Footnote {
	if byRef, ok := pc.Get(footnoteRefsKey).(map[string]*extensionast.Footnote); ok {
		return byRef
	}
	byRef := map[string]*extensionast.Footnote{}
	slots, _ := pc.Get(footnoteSlotsKey).([]*footnoteSlot)
	for _, slot := range slots {
		list, ok := slot.definition.Parent().(*extensionast.FootnoteList)
		if !ok {
			continue
		}
		for node := list.FirstChild(); node != nil; node = node.NextSibling() {
			definition := node.(*extensionast.Footnote)
			if _, first := byRef[string(definition.Ref)]; !first {
				byRef[string(definition.Ref)] = definition
			}
		}
		break
	}
	pc.Set(footnoteRefsKey, byRef)
	return byRef
}

// footnoteKeysKey holds a parse's footnoteDefinitionsByKey.
var footnoteKeysKey = parser.NewContextKey()

// footnoteDefinitionsByKey is each footnote definition in goldmark's list by its label's key
// (footnoteLabelKey), the first in the document where two share one. It is built once per parse,
// on the first reference the exact lookup leaves unresolved (exactFootnoteReference): inline text
// is parsed after every block has closed, so each definition has its slot by then, and a parse
// that keyed every definition again for each reference would key definitions times references.
func footnoteDefinitionsByKey(pc parser.Context) map[string]*extensionast.Footnote {
	if byKey, ok := pc.Get(footnoteKeysKey).(map[string]*extensionast.Footnote); ok {
		return byKey
	}
	slots, _ := pc.Get(footnoteSlotsKey).([]*footnoteSlot)
	byKey := make(map[string]*extensionast.Footnote, len(slots))
	for _, slot := range slots {
		definition, ok := slot.definition.(*extensionast.Footnote)
		if !ok {
			continue
		}
		if _, listed := definition.Parent().(*extensionast.FootnoteList); !listed {
			continue
		}
		key := footnoteLabelKey(string(definition.Ref))
		if _, first := byKey[key]; !first {
			byKey[key] = definition
		}
	}
	pc.Set(footnoteKeysKey, byKey)
	return byKey
}

// footnotes is goldmark's footnote extension with its definition parser taking every space and
// tab after a definition's `]:` as part of its prefix, as the browser editor's parser does, so the
// definition's first block starts at the column its later lines are measured from: goldmark left
// them to that block, which read a fence opening the definition as indented by one (and dropped a
// space from each code line) and a list's marker one column in (which put its item's later blocks
// outside it).
type footnotes struct{}

func (footnotes) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(footnoteParserOptions()...)
}

// footnoteParserOptions registers footnotes: the definition parser (footnoteDefinitionParser) and
// the reference parser (footnoteReferenceParser), which numbers the definitions it refers to as
// goldmark's does. Goldmark's footnote transformer is left out: it only orders the definitions,
// drops those nothing refers to and appends backlinks, and the parser keeps every definition where
// it is written (definitionsInPlace) and writes no backlink.
func footnoteParserOptions() []parser.Option {
	return []parser.Option{
		parser.WithBlockParsers(util.Prioritized(frontmatterAttempt{endsContainers{footnoteDefinitionParser{nestingGuard{BlockParser: extension.NewFootnoteBlockParser()}}}}, 999)),
		parser.WithInlineParsers(util.Prioritized(footnoteReferenceParser{}, 101)),
	}
}

type footnoteDefinitionParser struct{ parser.BlockParser }

// footnoteSlot stands where a footnote definition is written while the document parses:
// goldmark's footnote extension moves each definition it closes into the list it gathers at the
// document's end, and its transformer drops the ones nothing refers to. definitionsInPlace puts
// each back in its slot, where the browser editor's parser keeps it.
type footnoteSlot struct {
	ast.BaseBlock
	definition ast.Node
}

var (
	kindFootnoteSlot = ast.NewNodeKind("FootnoteSlot")
	footnoteSlotsKey = parser.NewContextKey()
)

func (s *footnoteSlot) Kind() ast.NodeKind { return kindFootnoteSlot }

func (s *footnoteSlot) Dump(source []byte, level int) { ast.DumpHelper(s, source, level, nil, nil) }

// Close leaves a slot where the definition is written and hands goldmark the definition from the
// document's level, where goldmark puts the list it gathers definitions in when it closes the first
// one: a list put inside a definition would leave the document with the definition that holds it,
// and goldmark reads no text in blocks outside the document. That list goes ahead of the blocks
// written, where no check of the block before another (interruptsOpenBlock) meets it while the
// document parses: goldmark put it at the document's end, between the blocks written before and
// after the definition closed.
func (p footnoteDefinitionParser) Close(node ast.Node, reader gmtext.Reader, pc parser.Context) {
	slot := &footnoteSlot{definition: node}
	node.Parent().InsertBefore(node.Parent(), node, slot)
	slots, _ := pc.Get(footnoteSlotsKey).([]*footnoteSlot)
	pc.Set(footnoteSlotsKey, append(slots, slot))
	root := node.Parent()
	for root.Parent() != nil {
		root = root.Parent()
	}
	root.AppendChild(root, node)
	p.BlockParser.Close(node, reader, pc)
	if list := node.Parent(); list != root.FirstChild() {
		root.InsertBefore(root, root.FirstChild(), list)
	}
}

// definitionsInPlace puts each footnote definition back where it is written, in place of its slot,
// and removes the list goldmark gathered them in.
func definitionsInPlace(root ast.Node, context parser.Context) {
	slots, _ := context.Get(footnoteSlotsKey).([]*footnoteSlot)
	for _, slot := range slots {
		if parent := slot.definition.Parent(); parent != nil {
			parent.RemoveChild(parent, slot.definition)
		}
		slot.Parent().ReplaceChild(slot.Parent(), slot, slot.definition)
	}
	if list, ok := root.FirstChild().(*extensionast.FootnoteList); ok {
		root.RemoveChild(root, list)
	}
}

// Open reads the definition's opener without the padding a tab split by the containers' prefix
// leaves ahead of it: goldmark's parser measures where the definition's text starts from the line
// without that padding, so it would take the label's first characters as the text (`]: def`).
func (p footnoteDefinitionParser) Open(parent ast.Node, reader gmtext.Reader, pc parser.Context) (ast.Node, parser.State) {
	_, segment := reader.PeekLine()
	offset := pc.BlockOffset()
	padded := segment.Padding > 0 && offset >= segment.Padding
	if padded {
		setPadding(reader, 0)
		pc.SetBlockOffset(offset - segment.Padding)
	}
	node, state := p.BlockParser.Open(parent, reader, pc)
	if node == nil {
		if padded {
			setPadding(reader, segment.Padding)
			pc.SetBlockOffset(offset)
		}
		return node, state
	}
	if state&parser.HasChildren != 0 {
		line, _ := reader.PeekLine()
		skip := 0
		for skip < len(line) && (line[skip] == ' ' || line[skip] == '\t') {
			skip++
		}
		if skip < len(line) && !util.IsBlank(line[skip:]) {
			reader.Advance(skip)
		}
	}
	return node, state
}
