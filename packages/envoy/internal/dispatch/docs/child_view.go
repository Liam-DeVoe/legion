package docs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// A child issue that holds no requirement of its own is a view on its ancestor's spec (R23 of
// this branch's spec): its checks serve the requirements of the nearest issue above it that holds
// requirement nodes and is not done, and a requirement written on it is refused with the
// ancestor's name, so the work it does is the work that ancestor states. ViewOf is the one answer
// of whose view an issue is: the tree read's view_of member reads it, and so does the refusal of a
// requirement written on such a child (RefuseRequirementOnView), so the two never disagree.

// requirementType is the typed block a requirement is.
const requirementType = "requirement"

// viewOfStatement reads, for the issue $1 whose ancestors, parent first, are $2: whether the issue
// is converted (its issue_trees row), whether it holds a requirement node of its own (a
// document_blocks row of type requirement of one of its artifacts, its tree's among them, which
// the block index writes for every current requirement block and deletes once a version drops
// it), and the nearest of its ancestors that holds one, with whether that ancestor is done.
const viewOfStatement = `
	select exists (select 1 from issue_trees tree where tree.issue_key = $1),
	       exists (select 1 from document_blocks d join artifacts doc on doc.id = d.artifact_id
	               where doc.issue_key = $1 and d.type = '` + requirementType + `'),
	       coalesce(nearest.key, ''), coalesce(nearest.done, false)
	from (select 1) one
	left join lateral (
		select above.key, i.status = 'done' as done
		from unnest($2::text[]) with ordinality as above(key, depth)
		join issues i on i.key = above.key
		where exists (select 1 from document_blocks d join artifacts doc on doc.id = d.artifact_id
		              where doc.issue_key = above.key and d.type = '` + requirementType + `')
		order by above.depth
		limit 1
	) nearest on true`

// ViewOf is the issue whose requirements the issue issueKey is a view of, read through q, and ""
// where it is no view. An issue is a view where it is converted - born so, or by the conversion
// - and holds no requirement node of its own, and its nearest ancestor that holds requirement
// nodes is not done: that ancestor is the one it is a view of. An issue the conversion has not
// reached is no view and writes as it always has; an issue that holds requirements of its own
// stands on its own, as a child that already held them when it converted does; and a child whose
// nearest ancestor holding requirements is done stands on its own too, since that ancestor's spec
// can take no more work.
func ViewOf(ctx context.Context, q Queryer, issueKey string) (string, error) {
	ancestors, err := refs.IssueAncestors(ctx, q, issueKey)
	if err != nil {
		return "", fmt.Errorf("read the issues above %s: %w", issueKey, err)
	}
	if len(ancestors) == 0 {
		return "", nil
	}
	var converted, holds, done bool
	var nearest string
	if err := q.QueryRow(ctx, viewOfStatement, issueKey, ancestors).Scan(&converted, &holds, &nearest, &done); err != nil {
		return "", fmt.Errorf("read whether %s is a view of an issue above it: %w", issueKey, err)
	}
	if !converted || holds || nearest == "" || done {
		return "", nil
	}
	return nearest, nil
}

// ErrRequirementBelongsToAncestor refuses a write that adds a requirement node to an issue that
// is a view of Ancestor (ViewOf): Issue's spec states no requirement of its own, so work Ancestor
// does not state goes into Ancestor's spec first. Blocks are the ids of the requirement blocks the
// write would add. Nothing of the write is written.
type ErrRequirementBelongsToAncestor struct {
	Issue, Ancestor string
	Blocks          []string
}

func (e *ErrRequirementBelongsToAncestor) Error() string {
	return fmt.Sprintf("%s is a view on the spec of %s and holds no requirement of its own, so this write's requirement %s belongs to %s: write it into the spec of %s (dispatch://%s/spec), and serve it from a check of %s",
		e.Issue, e.Ancestor, strings.Join(e.Blocks, ", "), e.Ancestor, e.Ancestor, e.Ancestor, e.Issue)
}

// requirementIDs is the id of each requirement block tree holds, in document order, an id held
// twice once.
func requirementIDs(tree *pmdoc.Node) []string {
	var ids []string
	pmdoc.Walk(tree, func(node *pmdoc.Node) bool {
		if node.Type != requirementType {
			return true
		}
		if id, _ := node.Attrs[pmdoc.BlockIDAttr].(string); id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
		return true
	})
	return ids
}

// RefuseRequirementOnView refuses, with ErrRequirementBelongsToAncestor, a write to a document of
// the issue issueKey that leaves after holding a requirement block before did not hold, where that
// issue is a view (ViewOf), read through q. before is the document the write replaces, nil for a
// new document. A write that adds no requirement block reads nothing.
func RefuseRequirementOnView(ctx context.Context, q Queryer, issueKey string, before, after *pmdoc.Node) error {
	held := requirementIDs(before)
	var added []string
	for _, id := range requirementIDs(after) {
		if !slices.Contains(held, id) {
			added = append(added, id)
		}
	}
	if len(added) == 0 || issueKey == "" {
		return nil
	}
	ancestor, err := ViewOf(ctx, q, issueKey)
	if err != nil || ancestor == "" {
		return err
	}
	sort.Strings(added)
	return &ErrRequirementBelongsToAncestor{Issue: issueKey, Ancestor: ancestor, Blocks: added}
}

// refuseRequirementOnViewOf is RefuseRequirementOnView for a write to the document artifactID,
// whose issue its row names; a document no issue holds is never refused.
func refuseRequirementOnViewOf(ctx context.Context, q Queryer, artifactID string, before, after *pmdoc.Node) error {
	if len(requirementIDs(after)) == 0 {
		return nil
	}
	var issue *string
	err := q.QueryRow(ctx, `select issue_key from artifacts where id = $1::uuid`, artifactID).Scan(&issue)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && issue == nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the issue of document %s: %w", artifactID, err)
	}
	return RefuseRequirementOnView(ctx, q, *issue, before, after)
}
