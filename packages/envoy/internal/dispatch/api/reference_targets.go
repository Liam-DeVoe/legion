package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// referenceTargetsMax is how many references one batched read may name. A page draws a chip per
// reference it holds, and the point of this read is that one page's whole first load is one
// request, so the cap is set above the heaviest set a page can hold rather than at a round guess.
// Measured on 2026-11-05 against this repository's own Dispatch, on dispatch://INFRA-1196, its
// heaviest issue: its spec, plan and verification documents write 177 edges, 174 of them neither
// part_of nor attached_to, naming 72 distinct targets; the renders of that issue and of its four
// largest documents name 247 distinct references in all. The tabs this page replaces send one
// GET /api/v1/references per chip, 14 on the heaviest of them. 256 is above all of those, so no
// first load needs a second request, and a set at the cap still costs one statement per node
// kind (refs.TargetNames), not one per reference.
//
// The other bound on a batch is the request line itself, which this server sets no limit of its
// own on: those 247 references encode to about 17 KB of query string, and a load balancer in
// front of Dispatch admits a request line of its own size (16 KB on an AWS one). A caller whose
// references are long enough to meet that reads them in two requests, as it would past the cap.
const referenceTargetsMax = 256

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
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
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
	targets := make([]refs.Target, 0, len(raw))
	seen := make(map[refs.Target]struct{}, len(raw))
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
		if _, duplicate := seen[target]; !duplicate {
			seen[target] = struct{}{}
			targets = append(targets, target)
		}
	}
	names, err := refs.TargetNames(r.Context(), s.deps.Store.Pool, targets)
	if err != nil {
		return nil, err
	}
	for index, target := range named {
		if name, found := names[target]; found {
			answers[index].Name, answers[index].Gone = name, false
		}
	}
	if err := s.blockTrees(r, answers, named, trees); err != nil {
		return nil, err
	}
	return answers, nil
}

// blockTrees registers, for each member that names a block something answers to, that block's
// whole text with trees as the member's text_tree: the page draws a plan task, a requirement or a
// check it reaches by reference from it, where the member's name is the block's stored excerpt,
// cut at refs.BlockExcerptRunes and holding a typed block's opening line. The texts are read in
// one statement per kind of block the request names (refs.BlockTexts), beside the one per kind
// the names cost, and no document is loaded or parsed for them.
//
// answers must have stopped growing: what is registered is a pointer into each member.
func (s *server) blockTrees(r *http.Request, answers []model.ReferenceTarget, named []refs.Target, trees *displayTreeReader) error {
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
	texts, err := refs.BlockTexts(r.Context(), s.deps.Store.Pool, blocks)
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
// with its fence, which are no part of what the block says: its tree is of its own content
// (blockContent), the tree GET /api/v1/issues/{key}/coverage and /results answer for the same
// block. Every other block's text is its content already. A text that opens like a typed block
// and does not read back as one alone - its lines carry the prefix of a list item or a quote it
// stands in - is answered as it stands.
func blockTextContent(whole string) string {
	opening, _, _ := strings.Cut(whole, "\n")
	if _, _, typed := pmdoc.ParseTypedDirectiveOpen(strings.TrimRight(opening, "\r")); !typed {
		return whole
	}
	tree, err := pmdoc.ParseRendering(whole)
	if err != nil || len(tree.Children) != 1 || !pmdoc.IsTypedBlock(tree.Children[0].Type) {
		return whole
	}
	return blockContent(tree.Children[0])
}

// referenceTargetsETag hashes the answer itself, every member in order: its reference, its kind,
// whether it is gone, its name and its tree, each of the last two after its length so no name's
// own line endings can read as another member's fields. The answer depends on nothing else, so a
// set whose members all stand as they did is answered 304, and a member renamed, deleted or
// restored, or a block whose text changed anywhere, changes the tag. A caller given trees and
// one given none are answered different bodies wherever a block is named, and different tags
// with them.
func referenceTargetsETag(targets []model.ReferenceTarget) string {
	hash := sha256.New()
	for _, target := range targets {
		_, _ = fmt.Fprintf(hash, "%s %s %t %d %s %d %s\n", target.Ref, target.Kind, target.Gone,
			len(target.Name), target.Name, len(target.TextTree), target.TextTree)
	}
	return `"` + hex.EncodeToString(hash.Sum(nil)) + `"`
}
