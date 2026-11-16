package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// FindNodeUnderIssue is the node the `{id}` of an `/api/v1/issues/{key}/nodes/{id}/…` route names,
// read for who, which must stand under the issue key names. Every route of that shape decodes its
// `{id}` here and nowhere else: this child's `/state`, `/settle`, `/findings` and `/pointers`, and
// dispatch://INFRA-1253's `/parent`. Such a route's idRoutes entry resolves no prefix, because the
// segment is a store address rather than an id a prefix shortens.
//
// id is the one path segment, which the client percent-encodes whole and the router decodes, so
// it may hold a `/` and a `#`. It is the node's store address, in one of three spellings:
//
//   - `<kind>/<id>`, the kind the read registry resolves and the id the store keys the node by:
//     `issue/CORE-1`, `artifact/CORE-1/spec` (a document by its ref_key), `comment/<uuid>`,
//     `ask/<uuid>`, `message/<uuid>`, `lens/<uuid>` once a lens resolves;
//   - a block's own node id, which names its owner before its `#`: `<ref_key>#<block id>` for a
//     block of a document (`CORE-1/spec#r1`), and `<kind>/<uuid>#<block id>` for a block of an ask,
//     a comment or a message, or a finding of a lens once it resolves (`comment/<uuid>#b2`), read
//     under that kind's block kind (`comment_block`);
//   - the node's whole `dispatch://` address (`dispatch://CORE-1/spec#b-r1`,
//     `dispatch://lens/<uuid>`), read as GET /api/v1/references reads one (text.ParseRef,
//     refs.TargetOf). A pin or a version it carries names the same node.
//
// A node stands under key when the issue it is located by is key: the issue itself, a document
// of the issue and each of its blocks, an ask, a comment or a message of the issue and each of
// their blocks, as the registry's loaders answer IssueKey. An address that decodes to nothing, a
// node of another issue or of none, and a node who may not see (the kind's predicate, which
// refs.Node binds) all answer one 404 NOT_FOUND, the same words for each, so the answer says
// nothing of whether such a node exists.
func FindNodeUnderIssue(ctx context.Context, q refs.Queryer, who model.Actor, key, id string) (model.GraphNode, error) {
	notFound := errorf(http.StatusNotFound, "NOT_FOUND", "no node %q under %s", id, key)
	kind, address, ok := decodeNodeAddress(id)
	if !ok {
		return model.GraphNode{}, notFound
	}
	node, found, err := refs.Node(ctx, q, who, kind, address, true)
	if err != nil {
		return model.GraphNode{}, err
	}
	if !found || node.IssueKey == nil || *node.IssueKey != key {
		return model.GraphNode{}, notFound
	}
	return node, nil
}

// decodeNodeAddress is the registry kind and the store id the `{id}` segment names, as
// FindNodeUnderIssue reads it, and false for a segment that names no node of a kind the registry
// resolves. The store id is the one refs.Node takes with byRefKey: a document's ref_key.
func decodeNodeAddress(id string) (kind, address string, ok bool) {
	if strings.HasPrefix(id, "dispatch://") {
		ref, parsed := text.ParseRef(id, "")
		if !parsed {
			return "", "", false
		}
		target, targeted := refs.TargetOf(ref)
		return target.Kind, target.ID, targeted && refs.ResolvesNodeKind(target.Kind)
	}
	if owner, _, isBlock := strings.Cut(id, "#"); isBlock {
		// An item's block names its item's kind and uuid before the `#`; a document's block names
		// the document's ref_key, which begins with an issue key, a project or `agent`, none of
		// which is a kind whose blocks the registry resolves.
		if itemKind, _, named := strings.Cut(owner, "/"); named && refs.ResolvesNodeKind(itemKind+"_block") {
			return itemKind + "_block", id, true
		}
		return refs.BlockNodeKind, id, true
	}
	kind, address, named := strings.Cut(id, "/")
	if !named || address == "" || !refs.ResolvesNodeKind(kind) {
		return "", "", false
	}
	return kind, address, true
}
