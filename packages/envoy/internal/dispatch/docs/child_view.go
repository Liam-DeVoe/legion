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

// A child issue is a view on its ancestor's spec (R23 of this branch's spec): its checks serve the
// requirements of the nearest issue above it that holds requirement nodes and is not done, and a
// requirement written on it is refused with the ancestor's name, so the work it does is the work
// that ancestor states. ViewUnder is the one answer of whose view an issue is, from its parent up:
// ViewOf reads it for an issue that stands, and the tree read's view_of member reads ViewOf, as
// the refusal of a requirement written on such a child does (RefuseRequirementOnView); an issue
// create reads it for the issue it makes (RefuseRequirementUnder), whose own row is not written
// yet. So none of them disagree.

// requirementType is the typed block a requirement is.
const requirementType = "requirement"

// viewOfStatement reads, of the issues $1, a parent first and then each issue above it up to the
// root, the nearest that holds a requirement node - a document_blocks row of type requirement of
// one of its artifacts, its tree's among them, which the block index writes for every current
// requirement block and deletes once a version drops it - with whether that issue is done. It
// answers no row where none of them holds one.
const viewOfStatement = `
	select above.key, i.status = 'done'
	from unnest($1::text[]) with ordinality as above(key, depth)
	join issues i on i.key = above.key
	where exists (select 1 from document_blocks d join artifacts doc on doc.id = d.artifact_id
	              where doc.issue_key = above.key and d.type = '` + requirementType + `')
	order by above.depth
	limit 1`

// ViewOf is the issue whose requirements the issue issueKey is a view of, read through q, and ""
// where it is no view. An issue is a view where it is converted - its issue_trees row, which at
// this head only an issue born converted holds - and the nearest issue above it that holds
// requirement nodes is not done (ViewUnder of its parent): that issue is the one it is a view of,
// whatever the issue holds itself, since a requirement that reached it through its room is a gap
// the coverage read names, not a requirement of its own. An issue the conversion has not reached
// is no view and writes as it always has, and a child whose nearest ancestor holding requirements
// is done stands on its own, since that ancestor's spec can take no more work. A child that held
// requirements of its own when the conversion reached it stands on its own too, by the mark the
// conversion leaves on it, which this head does not write.
func ViewOf(ctx context.Context, q Queryer, issueKey string) (string, error) {
	var parent *string
	var converted bool
	err := q.QueryRow(ctx, `
		select i.parent_key, exists (select 1 from issue_trees tree where tree.issue_key = i.key)
		from issues i where i.key = $1`, issueKey).Scan(&parent, &converted)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read whether %s is converted: %w", issueKey, err)
	}
	if !converted || parent == nil {
		return "", nil
	}
	return ViewUnder(ctx, q, *parent)
}

// ViewUnder is the issue whose requirements a converted issue whose parent is parentKey is a view
// of, read through q, and "" where it would be none (ViewOf): the nearest issue of the chain that
// starts at parentKey and climbs to the root (refs.IssueAncestors) that holds requirement nodes,
// where that issue is not done. A root issue, whose parentKey is "", is no view.
func ViewUnder(ctx context.Context, q Queryer, parentKey string) (string, error) {
	if parentKey == "" {
		return "", nil
	}
	above, err := refs.IssueAncestors(ctx, q, parentKey)
	if err != nil {
		return "", fmt.Errorf("read the issues above %s: %w", parentKey, err)
	}
	var nearest string
	var done bool
	err = q.QueryRow(ctx, viewOfStatement, append([]string{parentKey}, above...)).Scan(&nearest, &done)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the nearest issue from %s up that holds requirements: %w", parentKey, err)
	}
	if done {
		return "", nil
	}
	return nearest, nil
}

// ErrRequirementBelongsToAncestor refuses a write that adds a requirement node to an issue that
// is a view of Ancestor (ViewOf, ViewUnder): Issue's spec states no requirement of its own, so
// work Ancestor does not state goes into Ancestor's spec first. Blocks are the ids of the
// requirement blocks the write would add. Nothing of the write is written.
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
	return requirementBelongsTo(issueKey, ancestor, added)
}

// RefuseRequirementUnder refuses, with ErrRequirementBelongsToAncestor, the spec of the issue
// issueKey that an issue create makes converted under the parent parentKey, where spec holds a
// requirement block and the new issue would be a view (ViewUnder), read through q. The new issue's
// own row is not written yet, so the chain is read from its parent. A spec that holds no
// requirement block reads nothing.
func RefuseRequirementUnder(ctx context.Context, q Queryer, issueKey, parentKey string, spec *pmdoc.Node) error {
	added := requirementIDs(spec)
	if len(added) == 0 || parentKey == "" {
		return nil
	}
	ancestor, err := ViewUnder(ctx, q, parentKey)
	if err != nil || ancestor == "" {
		return err
	}
	return requirementBelongsTo(issueKey, ancestor, added)
}

// requirementBelongsTo is the refusal of the requirement blocks added to the issue, a view of
// ancestor, naming the blocks in sorted order.
func requirementBelongsTo(issue, ancestor string, added []string) error {
	sort.Strings(added)
	return &ErrRequirementBelongsToAncestor{Issue: issue, Ancestor: ancestor, Blocks: added}
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
