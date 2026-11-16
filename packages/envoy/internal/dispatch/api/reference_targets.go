package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// referenceTargetsMax is how many references one batched read may name. Its value is
// contracts.ReferenceTargetsMax, the literal REFERENCE_TARGETS_MAX in packages/contracts, which
// the client batches by too, and whose comment gives the measurement it is set above: the 247
// distinct references that dispatch://INFRA-1196 and its four largest documents name. So the
// count of references splits no such load into a second request, and a set at the cap costs one
// statement per node kind (refs.TargetNames), not one per reference.
//
// The other bound on a batch is the request line itself, which this server sets no limit of its
// own on. A proxy in front of the server can set one; 16 KB is one known such limit, an AWS load
// balancer's. The client keeps each request's query string within a budget of its own,
// REFERENCE_TARGETS_MAX_QUERY_CHARS in packages/dispatch/web/src/api/client.ts, whose own comment
// gives its value and the reason for it. Those 247 references encode to about 17 KB of query
// string, so that budget splits such a load into two requests where the cap splits none.
const referenceTargetsMax = contracts.ReferenceTargetsMax

// getReferenceTargets resolves many dispatch:// references in one read. For each ?ref= it answers
// what that reference points at: its kind, the words a reader is shown for its target in place of
// the target's text (refs.TargetNames, the same words GET /api/v1/references shows beside an edge
// at that node), and whether the target is gone. The answer holds one member per ?ref= in the
// request's order, each echoing the reference as the request wrote it, so a caller reads the
// answer by index or by reference; a reference named twice is answered twice.
//
// It is a GET with one repeated parameter, not a POST of a list: a POST of the page goes through
// the write store with an Idempotency-Key (packages/dispatch/AGENTS.md), so a POST read would be
// a second write path, and a GET is what a browser cache, a conditional read and the page's
// request budget are counted by. GET /api/v1/references reads one node's edges and stays as it
// is; this read names no edges at all.
//
// A reference the grammar cannot read is refused by name (parseRequestRef, as the per-reference
// route refuses its own), and so is a set past referenceTargetsMax, which is refused before
// anything is parsed. A reference that reads but names nothing is answered, gone: a chip whose
// target was deleted still has to draw.
//
// A caller that sent `Dispatch-Trees: 1` is given each block's whole text as its text_tree
// (blockTrees). The trees are resolved before the ETag is made, because the ETag hashes them: a
// block's name stops at its stored excerpt, so a change past it moves the tree and not the name.
func (s *server) getReferenceTargets(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuthenticated(w, r) {
		return
	}
	trees := s.readDisplayTrees(r.Context(), wantsDisplayTrees(r))
	targets, err := s.referenceTargets(r, trees)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	trees.resolve()
	etag := referenceTargetsETag(targets)
	if answerNotModified(w, r, etag) {
		return
	}
	WriteJSON(w, http.StatusOK, model.ReferenceTargets{Targets: targets})
}

// referenceTargets reads every ?ref= of the request and resolves them in one query per node kind.
// The kind of each answer is the kind the reference itself names, which is the kind its node
// resolves to (refs.TargetOf stores a block of an ask, a comment or a message under its item's
// block kind, and those resolve to nodes of kind block), so a gone target is answered under its
// kind too.
//
// A reference is read past the whitespace around it, and echoed as the request wrote it, that
// whitespace included: the answer is read by index or by reference, and a caller matching the
// members against the strings it sent finds none of them under a reference this read had
// rewritten.
//
// trees is the request's own reader, nil for a caller that asked for no tree, which then reads
// no block's text at all.
func (s *server) referenceTargets(r *http.Request, trees *displayTreeReader) ([]model.ReferenceTarget, error) {
	raw := r.URL.Query()["ref"]
	if len(raw) == 0 {
		return nil, errorf(http.StatusBadRequest, "INVALID_REFERENCE", "ref is required, once per reference to resolve")
	}
	if len(raw) > referenceTargetsMax {
		return nil, countExceededError("CAP_EXCEEDED", "ref", len(raw), referenceTargetsMax)
	}
	answers := make([]model.ReferenceTarget, len(raw))
	named := make([]refs.Target, len(raw))
	for index, value := range raw {
		ref, err := parseRequestRef(strings.TrimSpace(value), s.deps.ServerURL)
		if err != nil {
			return nil, err
		}
		// Every kind parseRequestRef admits has a target; only an external URL has none, and
		// that is no reference.
		target, _ := refs.TargetOf(ref)
		answers[index] = model.ReferenceTarget{Ref: value, Kind: ref.Kind, Gone: true}
		named[index] = target
	}
	names, err := refs.TargetNames(r.Context(), s.deps.Store.Pool, named)
	if err != nil {
		return nil, err
	}
	for index, target := range named {
		if name, found := names[target]; found {
			answers[index].Name, answers[index].Gone = name, false
		}
	}
	if err := s.blockTrees(r.Context(), answers, named, trees); err != nil {
		return nil, err
	}
	return answers, nil
}

// blockTrees registers, for each member that names a block something answers to, that block's
// whole text with trees as the member's text_tree: the page draws a plan task, a requirement or a
// check it reaches by reference from it, where the member's name is the block's stored excerpt,
// cut at refs.BlockExcerptRunes and holding a typed block's opening line. The texts are read in
// one statement per kind of block the request names (refs.BlockTexts), beside the one per kind
// the names cost, and no document is loaded for them: a block of a document, a comment or an ask
// has its text read from the block index, and a block of a message has its text split again from
// its message's body, since no table records it.
//
// answers must have stopped growing: what is registered is a pointer into each member.
func (s *server) blockTrees(ctx context.Context, answers []model.ReferenceTarget, named []refs.Target, trees *displayTreeReader) error {
	if trees == nil {
		return nil
	}
	var blocks []refs.Target
	seen := map[refs.Target]struct{}{}
	for index, target := range named {
		if answers[index].Kind != "block" || answers[index].Gone {
			continue
		}
		if _, duplicate := seen[target]; !duplicate {
			seen[target] = struct{}{}
			blocks = append(blocks, target)
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	texts, err := refs.BlockTexts(ctx, s.deps.Store.Pool, blocks)
	if err != nil {
		return err
	}
	contents := make(map[refs.Target]string, len(texts))
	for target, whole := range texts {
		contents[target] = blockTextContent(whole)
	}
	for index, target := range named {
		if answers[index].Kind == "block" && !answers[index].Gone {
			trees.text(&answers[index].TextTree, contents[target])
		}
	}
	return nil
}

// blockTextContent is the markdown a block's text_tree is read from, given the block's whole
// text as the index holds it. A typed block's text opens with its directive line and closes
// with its fence, which are no part of what the block says: its tree is of its own content, the
// tree GET /api/v1/issues/{key}/coverage and /results answer for the same block
// (typedBlocksIn). Every other block's text is its content already.
//
// The content is cut from the text itself and is never rendered again. A parse gives each typed
// block that has no id a new random one (pmdoc.EnsureBlockIDs), and a rendering writes that id
// into the block's opening line. So for a block that holds a typed block written without an id, a
// content rendered from a parse would change on every read, and its tree and the ETag with it.
//
// The cut drops the first line and the last one, and takes out of each line between them the
// indentation of the last line, the closing fence. The text starts at the directive, so a block
// in a list item carries that indentation on every line but its first; left in, it would make the
// content after a marker such as `10. ` an indented code block. A blank line, which the renderer
// can write without that indentation, is kept as it stands. The blank lines at the start and at
// the end of the content are no part of it, so a block that holds nothing has no content. A first
// line `---` is written `***`, the same rule: at the start of a document of its own, `---` opens
// front matter, which it does not in the block.
//
// The text is cut only where its last line is a fence and its first line, the cut lines and that
// fence read back as one typed block (pmdoc.ParseRendering). The parse checks the shape alone;
// nothing of the content comes from it. A text that opens like a typed block and fails that
// check is answered as it stands: one with a line between its fences that does not carry the
// fence's indentation, one that carries the `>` of a quote it stands in, or one whose fence does
// not close it.
//
// In the markdown a document stores, which gives every typed block its id, the cut reads back as
// a rendering of the block's content does (blockContent), and it is the same bytes except in
// lines that read as nothing or as the same text: an escape that keeps a line from closing the
// block, the blank line of an empty code block, the blank lines a rendering writes for the empty
// paragraph an ask holds ahead of a list. One shape reads back otherwise: in a footnote
// definition, a blank line of code is written without the definition's indentation, and the cut
// cannot tell those columns from the code's, so such a line can keep other spaces than the
// document reads in it.
func blockTextContent(whole string) string {
	opening, rest, _ := strings.Cut(whole, "\n")
	if _, _, typed := pmdoc.ParseTypedDirectiveOpen(strings.TrimRight(opening, "\r")); !typed {
		return whole
	}
	inner, closing := "", rest
	if end := strings.LastIndexByte(rest, '\n'); end >= 0 {
		inner, closing = rest[:end+1], rest[end+1:]
	}
	fence := strings.TrimLeft(closing, " \t")
	indent := closing[:len(closing)-len(fence)]
	if colons := strings.TrimRight(fence, " \t\r"); colons == "" || strings.Trim(colons, ":") != "" {
		return whole
	}
	lines, cut := outdented(inner, indent)
	if !cut {
		return whole
	}
	standing := whole
	if indent != "" {
		standing = opening + "\n" + lines + closing[len(indent):]
	}
	tree, err := pmdoc.ParseRendering(standing)
	if err != nil || len(tree.Children) != 1 || !pmdoc.IsTypedBlock(tree.Children[0].Type) {
		return whole
	}
	content := withoutBlankEnds(lines)
	if first, _, _ := strings.Cut(content, "\n"); strings.TrimRight(first, " \t") == "---" {
		return "***" + content[len("---"):]
	}
	return content
}

// outdented is lines, each ending in its line feed, with indent taken from the start of each
// line that carries it. A blank line, one of no more than spaces and tabs, that does not carry
// indent is kept as it stands. cut is false when a line that is not blank does not carry indent.
func outdented(lines, indent string) (text string, cut bool) {
	if indent == "" {
		return lines, true
	}
	var written strings.Builder
	written.Grow(len(lines))
	for line := range strings.Lines(lines) {
		rest, carried := strings.CutPrefix(line, indent)
		if !carried && strings.Trim(line, " \t\r\n") != "" {
			return "", false
		}
		written.WriteString(rest)
	}
	return written.String(), true
}

// withoutBlankEnds is lines from the first that holds more than whitespace to the line ending
// of the last that does, and "" when none does.
func withoutBlankEnds(lines string) string {
	from, to, at := -1, 0, 0
	for line := range strings.Lines(lines) {
		if strings.Trim(line, " \t\r\n") != "" {
			if from < 0 {
				from = at
			}
			to = at + len(line)
		}
		at += len(line)
	}
	if from < 0 {
		return ""
	}
	return lines[from:to]
}

// referenceTargetsETag hashes the answer itself, every member in order: its reference, its kind,
// whether it is gone, its name and its tree, each of the last two after its length so no name's
// own line endings can read as another member's fields. The answer depends on nothing else, so a
// set whose members all stand as they did is answered 304, and a member renamed, deleted or
// restored changes the tag; for a caller given trees, a block whose text changed anywhere changes
// the tag. A caller given trees and one given none are answered different bodies wherever a
// block is named, and different tags with them.
func referenceTargetsETag(targets []model.ReferenceTarget) string {
	hash := sha256.New()
	for _, target := range targets {
		_, _ = fmt.Fprintf(hash, "%s %s %t %d %s %d %s\n", target.Ref, target.Kind, target.Gone,
			len(target.Name), target.Name, len(target.TextTree), target.TextTree)
	}
	return `"` + hex.EncodeToString(hash.Sum(nil)) + `"`
}
