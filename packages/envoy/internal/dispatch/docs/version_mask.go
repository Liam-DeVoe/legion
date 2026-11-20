package docs

// A stored document version's mask: the words a pending insert suggestion put in the document,
// once the suggestion's comment is deleted, which no read of the version gives although the
// version stores them (R3 of dispatch://INFRA-1523, and the architect's ruling of 2026-11-20
// 20:55Z). Storage keeps the bytes, as it keeps an event's payload, and the read masks them.
//
// A version's markdown carries no anchor mark (pmdoc.Render), so it cannot say which of its words
// an insert put there. The tree the markdown was rendered from can, and a version write stores it
// beside the markdown whenever it holds an insert suggestion's mark (InsertTree,
// artifact_versions.insert_tree). A version written before that column, or by a write that held no
// tree of its markdown, stores none, and its text is answered as stored.

import (
	"context"
	"fmt"

	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// InsertTree is tree as pmdoc's JSON (pmdoc.Node.JSON) when one of its nodes carries an insert
// suggestion's mark, and nil when none does: what a version write stores in
// artifact_versions.insert_tree, in the insert that stores the markdown tree renders to
// (RenderStamp.InsertTree). MaskedVersionText reads it back.
func InsertTree(tree *pmdoc.Node) ([]byte, error) {
	if tree == nil || len(insertMarkIDs(tree)) == 0 {
		return nil, nil
	}
	return tree.JSON()
}

// MaskedVersionText is markdown, the stored text of a version of the document artifactID, as every
// read of that version answers it: without the words of each insert suggestion whose comment its
// author deleted and whose suggestion no accept made document text. insertTree is the version's
// artifact_versions.insert_tree. A version that stores none, or whose inserts all stand, answers
// markdown itself, byte for byte; a masked one answers its tree rendered without those words
// (pmdoc.Render), so its text no longer hashes to the sha256 or the document token of the stored
// bytes, which every read still answers as stored. q reads the comments, inside the caller's
// transaction where it has one.
func MaskedVersionText(ctx context.Context, q Queryer, artifactID, markdown string, insertTree []byte) (string, error) {
	if len(insertTree) == 0 {
		return markdown, nil
	}
	tree, err := pmdoc.FromJSON(insertTree)
	if err != nil {
		return "", fmt.Errorf("read the insert tree of a version of document %s: %w", artifactID, err)
	}
	masked, err := deletedInsertMarks(ctx, q, artifactID, insertMarkIDs(tree))
	if err != nil {
		return "", err
	}
	if len(masked) == 0 {
		return markdown, nil
	}
	dropMarkedNodes(tree, masked)
	text, err := pmdoc.Render(tree)
	if err != nil {
		return "", fmt.Errorf("render a version of document %s without a deleted insert's words: %w", artifactID, err)
	}
	return text, nil
}

// deletedInsertMarksSQL answers, of the insert suggestion marks $2 of document $1, each one whose
// words no read gives: a comment whose anchor names the mark (anchor.mark_id, the id a suggestion
// mark carries) is deleted, and no comment on it had its suggestion accepted. The match is by the
// mark's id alone, never by text.
//
// A delete clears the suggestion column (clearDeletedPostBody), so an accept is read off what
// stays: the version the accept named records the comment (artifact_versions.accepted_from), and
// the accept's suggestion.accepted event names it, on the comment's own owner, as
// api/comment_delete.go's renameAcceptedVersion reads them; the event is the one an accept by a
// release that writes no accepted_from leaves. The column itself is read too, for a row a delete
// has not cleared. Accepted words are document text, which every version after the accept holds
// unmarked, so the words stay.
//
// A mark no comment names keeps its words: no post claims them, so no delete withdrew them. They
// are the document's own pending text, a browser's suggestion that nobody posted, which only a
// reject of the mark takes out, and R3 masks the words of a deleted post alone.
const deletedInsertMarksSQL = `
	with marked as (
		select c.anchor->>'mark_id' as mark_id, c.deleted_at is not null as deleted,
			coalesce(c.suggestion->>'accepted', '') = 'true'
				or exists (
					select from artifact_versions v where v.artifact_id = $1::uuid and v.accepted_from = c.id
				)
				or exists (
					select from events e
					where e.type = 'suggestion.accepted' and e.payload->>'id' = c.id::text
						and (e.issue_key = c.issue_key or (c.issue_key is null and e.artifact_id = c.artifact_id))
				) as accepted
		from comments c
		where (c.anchor->>'artifact_id')::uuid = $1::uuid and c.anchor->>'mark_id' = any($2::text[])
	)
	select mark_id from marked group by mark_id having bool_or(deleted) and not bool_or(accepted)`

// deletedInsertMarks is the set of ids among the insert suggestion marks ids of document
// artifactID whose words no read gives (deletedInsertMarksSQL).
func deletedInsertMarks(ctx context.Context, q Queryer, artifactID string, ids []string) (map[string]bool, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, deletedInsertMarksSQL, artifactID, ids)
	if err != nil {
		return nil, fmt.Errorf("read the deleted comments of document %s's insert suggestions: %w", artifactID, err)
	}
	defer rows.Close()
	masked := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan a deleted comment's insert suggestion: %w", err)
		}
		masked[id] = true
	}
	return masked, rows.Err()
}

// insertMarkIDs is the id of every insert suggestion mark tree carries, each once, in document
// order. A suggestion mark records its kind (suggestionKind); one recording none is a replace,
// whose words are document text.
func insertMarkIDs(tree *pmdoc.Node) []string {
	var ids []string
	seen := map[string]bool{}
	pmdoc.Walk(tree, func(node *pmdoc.Node) bool {
		for _, mark := range node.Marks {
			if id, ok := insertMarkID(mark); ok && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		return true
	})
	return ids
}

// insertMarkID is the id of mark when it is an insert suggestion's mark.
func insertMarkID(mark pmdoc.Mark) (string, bool) {
	if mark.Type != string(MarkSuggestion) {
		return "", false
	}
	if kind, _ := mark.Attrs["kind"].(string); kind != "insert" {
		return "", false
	}
	id, ok := mark.Attrs["id"].(string)
	return id, ok && id != ""
}

// dropMarkedNodes takes out of node, in place, every node carrying the mark of an insert
// suggestion masked names: its text runs, and an inline node such as a hard break the insert
// carried. The blocks stand as they are, an insert's split one included, so the version keeps its
// shape and loses those words alone.
func dropMarkedNodes(node *pmdoc.Node, masked map[string]bool) {
	kept := node.Children[:0]
	for _, child := range node.Children {
		if carriesMaskedInsert(child, masked) {
			continue
		}
		dropMarkedNodes(child, masked)
		kept = append(kept, child)
	}
	node.Children = kept
}

// carriesMaskedInsert reports whether node carries the mark of an insert suggestion masked names.
func carriesMaskedInsert(node *pmdoc.Node, masked map[string]bool) bool {
	for _, mark := range node.Marks {
		if id, ok := insertMarkID(mark); ok && masked[id] {
			return true
		}
	}
	return false
}
