package refs

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The head read of an issue's tree reads the issue's rows of the head projection
// (issue_head_nodes, graphstore.HeadRowsSQL) in one range of its primary key, where the issue is
// marked whole (issue_head_marks), and answers from them what the walk's reads answer of each node
// at the head: its place among its parent's children (TreeChild), its values and relations
// (TreeFacts), its row (TreeNodeRow), and for a document block what the block loader answers of
// it for the reader and its document_blocks row (BlockNodesAndRows). A read at a version walks the
// tuples.

// HeadRow is one row of the head projection: a node of the issue's tree as the store held it when
// a writer, or the backfill, last wrote the row.
type HeadRow struct {
	Node   Target
	Parent Target
	// Rank and Text are the node's rank and text values, nil where it holds none.
	Rank, Text *string
	// Stored says the store holds the node's graph_nodes row, whose supertag, state, visibility and
	// made_at follow.
	Stored                      bool
	Supertag, State, Visibility string
	MadeAt                      time.Time
	// Authors is the who of each authored_by the node states now, in seq order, nil where none.
	Authors []byte
	// FirstAuthors and FirstAt are, for a document block, the authors and the time of the version
	// that first held it where Dispatch knows the block was made there.
	FirstAuthors []byte
	FirstAt      *time.Time
	// Block is the document block's document_blocks row, its slug and band read off its document as
	// the head read runs; nil for any other node and for a block no row stands for.
	Block *BlockRow
	// IssueKey and Project are the block's document's issue and project.
	IssueKey *string
	// Seen says the reader may see the node, by its graph_nodes row's visibility and author, as the
	// block loader's predicate reads it.
	Seen  bool
	Facts TreeFacts
}

// headRowsStatement is the rows of the issue $1 the head projection holds while a mark names the
// issue whole, each with its document's slug and band read off the document a document block's row
// names, and whether the reader whose credential $2 binds may see it (the block loader's predicate,
// VisibleKindRow over the row's own graph_nodes columns), its stored state and visibility selected
// as a loader selects a node's (StoredRowOf, null where the store holds no row of it): the rows
// rows selects, held aside once, and the few documents they name by theirs. It is built at each
// read, as the block loader's is (blockNodesStatement): the registry the predicate reads is filled
// by the package's init, after its variables.
func headRowsStatement(rows string) string {
	return `
	with heads as materialized (` + rows + `),
	documents as materialized (
		select a.id, a.slug, a.issue_key, ` + DocumentBandSQL("a") + ` as band
		from artifacts a
		where a.id in (select named.document_id from heads named where named.document_id is not null)
	)
	select heads.node_kind, heads.node_address, heads.parent_kind, heads.parent_address, heads.rank, heads.text,
	       heads.made_at is not null, coalesce(heads.supertag, ''), ` + StoredRowOf("heads") + `,
	       heads.made_at, heads.authors, heads.first_authors, heads.first_at,
	       heads.document_id::text, heads.block_type, heads.text_sha256, heads.changed_version,
	       documents.slug, documents.issue_key, documents.band,
	       heads.made_at is null or ` + VisibleKindRow(BlockNodeKind, "heads", "$2") + `,
	       heads.value_kinds, heads.value_texts, heads.relation_kinds, heads.relation_to_kinds, heads.relation_to_ids
	from heads
	left join documents on documents.id = heads.document_id
	where exists (select 1 from issue_head_marks mark where mark.issue_key = $1)`
}

// headRowsWhole is every row of the issue $1, one range of issue_head_nodes' primary key: what a
// view reads.
const headRowsWhole = `select * from issue_head_nodes whole where whole.issue_key = $1`

// headRowsSkeleton is the rows of the issue $1's skeleton (HeadSkeleton), each a range of
// issue_head_nodes' primary key, which sorts byte by byte: every row of a kind other than a
// block, a comment and a comment's block; the blocks whose address $3 and $4 bound, a pair for each
// document of the skeleton and one for the addresses below the issue's own, and the blocks whose
// address is $6 or above; and the comments $5 names, with their blocks.
var headRowsSkeleton = `
		select * from issue_head_nodes kinds where kinds.issue_key = $1 and kinds.node_kind < '` + BlockNodeKind + `'
		union all
		select * from issue_head_nodes kinds where kinds.issue_key = $1 and kinds.node_kind > '` + BlockNodeKind + `' and kinds.node_kind < '` + string(ItemComment) + `'
		union all
		select * from issue_head_nodes kinds where kinds.issue_key = $1 and kinds.node_kind > '` + ItemComment.BlockKind() + `'
		union all
		select blocks.* from unnest($3::text[], $4::text[]) bounds(low, high)
		join issue_head_nodes blocks on blocks.issue_key = $1 and blocks.node_kind = '` + BlockNodeKind + `'
			and blocks.node_address >= bounds.low and blocks.node_address < bounds.high
		union all
		select * from issue_head_nodes blocks where blocks.issue_key = $1 and blocks.node_kind = '` + BlockNodeKind + `' and blocks.node_address >= $6
		union all
		select comments.* from issue_head_nodes comments
		where comments.issue_key = $1 and comments.node_kind = '` + string(ItemComment) + `' and comments.node_address = any($5::text[])
		union all
		select blocks.* from unnest($5::text[]) page(id)
		join issue_head_nodes blocks on blocks.issue_key = $1 and blocks.node_kind = '` + ItemComment.BlockKind() + `'
			and blocks.node_address >= '` + string(ItemComment) + `/' || page.id || '#' and blocks.node_address < '` + string(ItemComment) + `/' || page.id || '$'`

// HeadSkeleton is the part of an issue's rows the read with no view reads. Of the blocks whose
// address begins with the issue's key and `/`, the blocks of the issue's own documents, it reads
// the blocks of Documents alone, each a document's ref_key; it reads every other block, a finding
// of a lens (graphstore.FindingID) and a block of another issue's document placed under a node of
// the issue among them. Of the comments it reads those of the comment page, Comments, alone, each
// with its blocks. It reads every row of every other kind.
type HeadSkeleton struct {
	Documents []string
	Comments  []string
}

// bounds is the low and the high end of each range of addresses of the blocks the skeleton reads
// below the high end of its last range, above, which begins at the lowest address past the
// issue's own: the addresses below `<key>/`, and `<ref_key>#` up to `<ref_key>$` for each of
// Documents; every high end is its range's prefix with its last byte one higher, past which no
// address that begins with the prefix sorts.
func (skeleton HeadSkeleton) bounds(issueKey string) (low, high []string, above string) {
	low, high = make([]string, 0, len(skeleton.Documents)+1), make([]string, 0, len(skeleton.Documents)+1)
	low, high = append(low, ""), append(high, issueKey+"/")
	for _, document := range skeleton.Documents {
		low, high = append(low, document+"#"), append(high, document+"$")
	}
	return low, high, issueKey + "0"
}

// HeadTree is the rows of one issue's tree the head projection holds, keyed by the node, and the
// children each parent holds among them, as a children read answers them (Subtrees).
type HeadTree struct {
	Rows     map[Target]*HeadRow
	children map[Target][]TreeChild
}

// HeadRows is the rows of the issue issueKey's tree the head projection holds, read for who, and
// whether the issue is marked whole: an issue no mark names answers nil and false, and its read
// walks the tuples. Every marked issue holds at least its own node's row. skeleton, where it is
// set, reads the skeleton's rows alone (HeadSkeleton); nil reads every row.
func HeadRows(ctx context.Context, q Queryer, who model.Actor, issueKey string, skeleton *HeadSkeleton) (*HeadTree, bool, error) {
	statement, args := headRowsStatement(headRowsWhole), []any{issueKey, VisibilityArg(who)}
	if skeleton != nil {
		low, high, above := skeleton.bounds(issueKey)
		comments := skeleton.Comments
		if comments == nil {
			comments = []string{}
		}
		statement, args = headRowsStatement(headRowsSkeleton), append(args, low, high, comments, above)
	}
	rows, err := q.Query(ctx, statement, args...)
	if err != nil {
		return nil, false, fmt.Errorf("read the head rows of %s: %w", issueKey, err)
	}
	defer rows.Close()
	var read []HeadRow
	for rows.Next() {
		var head HeadRow
		var parentKind, parentID, document, blockType, textSHA256, slug, band, state, visibility pgtype.Text
		var madeAt *time.Time
		var changedVersion pgtype.Int4
		var valueKinds, valueTexts, relationKinds, relationToKinds, relationToIDs []string
		if err := rows.Scan(&head.Node.Kind, &head.Node.ID, &parentKind, &parentID, &head.Rank, &head.Text,
			&head.Stored, &head.Supertag, &state, &visibility, &madeAt, &head.Authors, &head.FirstAuthors, &head.FirstAt,
			&document, &blockType, &textSHA256, &changedVersion, &slug, &head.IssueKey, &band, &head.Seen,
			&valueKinds, &valueTexts, &relationKinds, &relationToKinds, &relationToIDs); err != nil {
			return nil, false, fmt.Errorf("read a head row of %s: %w", issueKey, err)
		}
		head.State, head.Visibility = state.String, visibility.String
		if parentKind.Valid && parentID.Valid {
			head.Parent = Target{Kind: parentKind.String, ID: parentID.String}
		}
		if madeAt != nil {
			head.MadeAt = *madeAt
		}
		if document.Valid && slug.Valid {
			_, blockID, _ := strings.Cut(head.Node.ID, "#")
			head.Block = &BlockRow{BlockID: blockID, Document: document.String, Slug: slug.String, Type: blockType.String,
				TextSHA256: textSHA256.String, ChangedVersion: int(changedVersion.Int32)}
			if band.String != "" {
				head.Block.Band = band.String[0]
			}
		}
		head.Facts = headFacts(head.Rank, head.Text, valueKinds, valueTexts, relationKinds, relationToKinds, relationToIDs)
		read = append(read, head)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("read the head rows of %s: %w", issueKey, err)
	}
	if len(read) == 0 {
		return nil, false, nil
	}
	heads := make(map[Target]*HeadRow, len(read))
	for index := range read {
		heads[read[index].Node] = &read[index]
	}
	return newHeadTree(heads, read), true, nil
}

// newHeadTree is rows, read, with the children of each parent among them, as the children read
// answers them under it (treeChildJoins, treeChildStands, treeChildOrder): each node whose
// standing part_of names the parent, unless its kind is one of workflowKinds or its state is gone,
// the ranked ones first by rank and then the rest, a tie by the lower address, each compared byte
// for byte.
func newHeadTree(rows map[Target]*HeadRow, read []HeadRow) *HeadTree {
	tree := &HeadTree{Rows: rows, children: map[Target][]TreeChild{}}
	for index := range read {
		head := &read[index]
		if head.Parent == (Target{}) || slices.Contains(workflowKinds, head.Node.Kind) || head.Gone() {
			continue
		}
		tree.children[head.Parent] = append(tree.children[head.Parent], head.Child())
	}
	for _, children := range tree.children {
		slices.SortFunc(children, func(left, right TreeChild) int {
			if left.Ranked != right.Ranked {
				if left.Ranked {
					return -1
				}
				return 1
			}
			if order := strings.Compare(left.Rank, right.Rank); order != 0 {
				return order
			}
			return CompareAddresses(left.Node, right.Node)
		})
	}
	return tree
}

// Subtrees is what TreeSubtrees answers of the tree at the head, read from its rows: the children
// of each of parents, and of each node under them the walk of the tree of the issue owner enters
// (TreeOwns), down to the leaves, keyed by the parent, each parent's in the children read's order.
// A child the walk does not enter - an issue's node, a node of another issue - is answered under
// its parent, and nothing under it is read.
func (tree *HeadTree) Subtrees(parents []Target, owner string) map[Target][]TreeChild {
	under := map[Target][]TreeChild{}
	read := map[Target]bool{}
	for len(parents) > 0 {
		var next []Target
		for _, parent := range parents {
			if read[parent] {
				continue
			}
			read[parent] = true
			children := tree.children[parent]
			if len(children) == 0 {
				continue
			}
			under[parent] = children
			for _, child := range children {
				if TreeOwns(owner, child.Node) {
					next = append(next, child.Node)
				}
			}
		}
		parents = next
	}
	return under
}

// Under is the children of each parent the rows hold children of that match holds, each parent's
// in the children read's order: what a walk reads of a parent it takes no node to reach, as a walk
// of an issue's skeleton reads the blocks of a document it does not enter.
func (tree *HeadTree) Under(match func(Target) bool) map[Target][]TreeChild {
	under := map[Target][]TreeChild{}
	for parent, children := range tree.children {
		if match(parent) {
			under[parent] = children
		}
	}
	return under
}

// headFacts is the values and relations a head row holds, as TreeFactsOf answers them: every value
// by its attribute, the rank and the text among them, and every relation in the row's order, which
// is the facts read's. A node that states nothing answers the zero TreeFacts.
func headFacts(rank, text *string, valueKinds, valueTexts, relationKinds, relationToKinds, relationToIDs []string) TreeFacts {
	var facts TreeFacts
	if rank != nil || text != nil || len(valueKinds) > 0 {
		facts.Values = make(map[string]string, len(valueKinds)+2)
		for index, kind := range valueKinds {
			facts.Values[kind] = valueTexts[index]
		}
		if rank != nil {
			facts.Values[AttributeRank] = *rank
		}
		if text != nil {
			facts.Values[graphstore.AttributeText] = *text
		}
	}
	if len(relationKinds) > 0 {
		facts.Relations = make([]TreeRelation, len(relationKinds))
		for index, kind := range relationKinds {
			facts.Relations[index] = TreeRelation{Kind: kind, To: Target{Kind: relationToKinds[index], ID: relationToIDs[index]}}
		}
	}
	return facts
}

// Child is the node as a children read answers it under its parent (TreeChild): its rank, its
// supertag and when it was made off its row, and its type off its supertag and the opening line of
// its text (TreeNodeType).
func (head *HeadRow) Child() TreeChild {
	child := TreeChild{Parent: head.Parent, Node: head.Node, Supertag: head.Supertag, MadeAt: head.MadeAt}
	if head.Rank != nil {
		child.Rank, child.Ranked = *head.Rank, true
	}
	opening := ""
	if head.Text != nil {
		opening, _, _ = strings.Cut(*head.Text, "\n")
	}
	child.Type = TreeNodeType(head.Node.Kind, head.Supertag, opening)
	return child
}

// Row is the node's row as TreeNodeRows answers it: the zero row where the store holds none.
func (head *HeadRow) Row() TreeNodeRow {
	if !head.Stored {
		return TreeNodeRow{}
	}
	return TreeNodeRow{Supertag: head.Supertag, MadeAt: head.MadeAt}
}

// Gone says the node's state value is gone, which a children read keeps out (treeChildStands).
func (head *HeadRow) Gone() bool {
	return head.Facts.Values[graphstore.AttributeState] == graphstore.StateGone
}

// BlockNode is the document block as BlockNodesAndRows answers it for the reader the head read was
// read for: the node the block loader builds, with the members a tree read answers of it - its
// author, when it was made, its stored state and visibility, its document's issue - and whether
// the reader may see it; ok is false for a node that is no document block with a row.
func (head *HeadRow) BlockNode() (node ExcerptedNode, seen, ok bool, err error) {
	if head.Block == nil || head.Node.Kind != BlockNodeKind {
		return ExcerptedNode{}, false, false, nil
	}
	built := model.GraphNode{Kind: BlockNodeKind, ID: head.Node.ID, BlockID: head.Block.BlockID, IssueKey: head.IssueKey,
		ChangedVersion: head.Block.ChangedVersion}
	first := blockFirst{authors: head.FirstAuthors, createdAt: head.FirstAt}
	known := head.FirstAt != nil || head.FirstAuthors != nil
	first.known = &known
	if built.MadeBy, built.MadeAt, err = first.maker(); err != nil {
		return ExcerptedNode{}, false, false, fmt.Errorf("decode the maker of block %s: %w", head.Node.ID, err)
	}
	if head.Authors != nil {
		if built.MadeBy, err = storedActors(head.Authors); err != nil {
			return ExcerptedNode{}, false, false, fmt.Errorf("decode the authors block %s records: %w", head.Node.ID, err)
		}
	}
	stored := StoredNode{}
	if head.Stored {
		stored = StoredNode{State: &head.State, Visibility: &head.Visibility}
	}
	setStored(&built, stored)
	return ExcerptedNode{Node: built}, head.Seen, true, nil
}
