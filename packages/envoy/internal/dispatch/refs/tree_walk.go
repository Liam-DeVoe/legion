package refs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The reads a walk of an issue's tree makes (docs' WalkIssueTree), each as of one cut of the tuple
// log: every tuple a read takes has a seq at or below the cut's, and of each relation or value it
// takes the newest such tuple by its time and then its seq, kept while it asserts, as
// graphstore.AsOfOut and AsOfIn read a node at a time. The head is read at the newest seq the log
// holds, and a version at the read_seq its record was taken at, so the head and every version are
// one read at two cuts. Each statement reaches graph_tuples through graph_tuples_as_of_out, keyed
// on the node and the kind, or graph_tuples_as_of_in, keyed on the far end and the kind,
// and refs' TestEveryAsOfReadOfThisChildUsesAnIndexOfTheStore holds each to that.

// TreeCut is the one moment a walk of an issue's tree reads the store at: ReadSeq, the highest
// graph_tuples.seq it takes, and At, the time it takes the issue's asks, comments and messages as
// of, since the conversation test reads their rows by their times (ConversationNodes). The zero TreeCut is no
// cut: a walk given it reads the head's (HeadCut).
type TreeCut struct {
	At      time.Time
	ReadSeq int64
}

// headCutStatement is the head's cut: the newest seq the tuple log holds, read off its primary
// key, and the transaction's time.
const headCutStatement = `select coalesce((select seq from graph_tuples order by seq desc limit 1), 0), now()`

// HeadCut is the cut of the store as q sees it now: the newest seq of the tuple log and the
// transaction's time. A version written in q's transaction records it as the read_seq its record
// was read at.
func HeadCut(ctx context.Context, q Queryer) (TreeCut, error) {
	var cut TreeCut
	if err := q.QueryRow(ctx, headCutStatement).Scan(&cut.ReadSeq, &cut.At); err != nil {
		return TreeCut{}, fmt.Errorf("read the head's cut of the tuple log: %w", err)
	}
	return cut, nil
}

// asOfValue is a lateral's body reading, at the cut the SQL cut names, the value of attribute out
// of the node whose kind and id the SQL kind and id name: the newest value tuple of that name, by
// its time and then its seq, as held where it asserts and null where it retracts. The opening of
// a text value is its first line alone, which says the type of a typed block (TreeNodeType).
func asOfValue(kind, id, attribute, cut string, opening bool) string {
	held := `t.to_id`
	if opening {
		held = `split_part(t.to_id, chr(10), 1)`
	}
	return `select case when t.asserted then ` + held + ` end as held
		from graph_tuples t
		where t.from_kind = ` + kind + ` and t.from_id = ` + id + ` and t.kind = '` + attribute + `'
		  and t.to_kind = '` + graphstore.KindValue + `' and t.seq <= ` + cut + `
		order by t.at desc, t.seq desc
		limit 1`
}

// asOfPlacement is a lateral's body reading, at the cut the SQL cut names, the parent of the node
// whose kind and id the SQL kind and id name: of its part_of tuples, the newest of each far end
// kept while it asserts, and of those the newest, so a node stands under one parent at a time.
func asOfPlacement(kind, id, cut string) string {
	return `select held.to_kind, held.to_id from (
			select distinct on (t.to_kind, t.to_id) t.to_kind, t.to_id, t.asserted, t.at, t.seq
			from graph_tuples t
			where t.from_kind = ` + kind + ` and t.from_id = ` + id + ` and t.kind = '` + graphstore.TuplePartOf + `'
			  and t.to_kind <> '` + graphstore.KindValue + `' and t.seq <= ` + cut + `
			order by t.to_kind, t.to_id, t.at desc, t.seq desc
		) held
		where held.asserted
		order by held.at desc, held.seq desc
		limit 1`
}

// treeNodeColumns are the columns of a node the children read and the placed-above read answer
// beside its parent and its address: its rank, its supertag, when it was made and the opening of
// its text, read off the laterals and the row asOfNodeJoins names.
const treeNodeColumns = `rank.held, coalesce(node.supertag, ''), node.made_at, coalesce(opening.held, '')`

// asOfNodeJoins are the joins of the node whose kind and id the SQL kind and id name that
// treeNodeColumns reads, at the cut the SQL cut names: its rank, its state, which a read keeps
// out where it is gone, the opening of its text, and its row.
func asOfNodeJoins(kind, id, cut string) string {
	return `left join lateral (` + asOfValue(kind, id, AttributeRank, cut, false) + `) rank on true
		left join lateral (` + asOfValue(kind, id, graphstore.AttributeState, cut, false) + `) state on true
		left join lateral (` + asOfValue(kind, id, graphstore.AttributeText, cut, true) + `) opening on true
		left join graph_nodes node on node.kind = ` + kind + ` and node.id = ` + id
}

// treeChildrenStatement is the children of each parent $1 and $2 name, kinds and ids, at the cut
// $3: each node whose part_of names the parent there - read into the parent through
// graph_tuples_as_of_in - and whose newest standing part_of out of it, read through
// graph_tuples_as_of_out, is that one, unless its state there is gone. It answers each parent's
// children by the parent's position, the ranked ones first by rank and then the unranked, a tie by
// the lower address, `<kind>:<id>`, each compared byte for byte (collate "C"), since a rank is a
// base-62 string of mixed case.
var treeChildrenStatement = `
	select parent.position, child.from_kind, child.from_id, ` + treeNodeColumns + `
	from unnest($1::text[], $2::text[]) with ordinality as parent (kind, id, position)
	cross join lateral (
		select distinct on (t.from_kind, t.from_id) t.from_kind, t.from_id, t.asserted
		from graph_tuples t
		where t.to_kind = parent.kind and t.to_id = parent.id and t.kind = '` + graphstore.TuplePartOf + `'
		  and t.to_kind <> '` + graphstore.KindValue + `' and t.seq <= $3
		order by t.from_kind, t.from_id, t.at desc, t.seq desc
	) child
	cross join lateral (` + asOfPlacement(`child.from_kind`, `child.from_id`, `$3`) + `) placed
	` + asOfNodeJoins(`child.from_kind`, `child.from_id`, `$3`) + `
	where child.asserted and placed.to_kind = parent.kind and placed.to_id = parent.id
	  and state.held is distinct from '` + graphstore.StateGone + `'
	order by parent.position, rank.held is null, rank.held collate "C", (child.from_kind || ':' || child.from_id) collate "C"`

// TreeChild is one node a walk's read answers under a parent at a cut: the parent and the node;
// its rank, and whether it holds one; its supertag and when it was made, off its row (the zero
// time where the store holds none); and its type (TreeNodeType).
type TreeChild struct {
	Parent, Node Target
	Rank         string
	Ranked       bool
	Supertag     string
	MadeAt       time.Time
	Type         string
}

// TreeChildren is the children of each of parents at the cut readSeq, each parent's in the order
// treeChildrenStatement answers them, the parents' in the order given.
func TreeChildren(ctx context.Context, q Queryer, parents []Target, readSeq int64) ([]TreeChild, error) {
	if len(parents) == 0 {
		return nil, nil
	}
	kinds, ids := targetColumns(parents)
	rows, err := q.Query(ctx, treeChildrenStatement, kinds, ids, readSeq)
	if err != nil {
		return nil, fmt.Errorf("read the children of %d nodes at seq %d: %w", len(parents), readSeq, err)
	}
	defer rows.Close()
	var children []TreeChild
	var position int
	var child TreeChild
	var row treeNodeRow
	targets := append([]any{&position, &child.Node.Kind, &child.Node.ID}, row.targets()...)
	for rows.Next() {
		child, row = TreeChild{}, treeNodeRow{}
		if err := rows.Scan(targets...); err != nil {
			return nil, fmt.Errorf("read a child at seq %d: %w", readSeq, err)
		}
		child.Parent = parents[position-1]
		row.fill(&child)
		children = append(children, child)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the children of %d nodes at seq %d: %w", len(parents), readSeq, err)
	}
	return children, nil
}

// treeNodeRow is what treeNodeColumns answers of one node, as scanned.
type treeNodeRow struct {
	rank     *string
	supertag string
	madeAt   *time.Time
	opening  string
}

// targets are where a scan of treeNodeColumns writes, in their order.
func (row *treeNodeRow) targets() []any {
	return []any{&row.rank, &row.supertag, &row.madeAt, &row.opening}
}

// fill gives child what row read of it.
func (row treeNodeRow) fill(child *TreeChild) {
	if row.rank != nil {
		child.Rank, child.Ranked = *row.rank, true
	}
	if row.madeAt != nil {
		child.MadeAt = *row.madeAt
	}
	child.Supertag = row.supertag
	child.Type = TreeNodeType(child.Node.Kind, row.supertag, row.opening)
}

// TreeNodeType is the type a node of the tree is taken by. A block's is the typed block its text
// opens (pmdoc's grammar of a typed block's opening line), read off its text value as of the walk's
// cut, whatever supertag its row holds: the store keeps no history of a node's supertag -
// graphstore.Write moves the column of its row in place, and no tuple states it - so the text value
// at the cut is the one record of a block's type at that cut, and a version is typed as it stood,
// before a later supertag change and after one alike. A block whose text opens no typed block the
// schema declares - a paragraph, a finding of a lens - is taken by its supertag, and every other
// node by its supertag too: a result, an ask, a comment. A node the store holds no row of is taken
// by its kind.
func TreeNodeType(kind, supertag, opening string) string {
	if kind == BlockNodeKind {
		if name, _, ok := pmdoc.ParseTypedDirectiveOpen(strings.TrimRight(trimContainerMarkers(opening), "\r")); ok && pmdoc.IsTypedBlock(name) {
			return name
		}
	}
	if supertag == "" {
		return kind
	}
	return supertag
}

// treePlacedAboveStatement is, at the cut $3, each block of the issue $1 whose parent is a node of
// an issue of $2, the issues above it: the blocks whose in_document names a document whose part_of
// names the issue, each read into its far end through graph_tuples_as_of_in, of which those whose
// newest standing part_of, read through graph_tuples_as_of_out, names a block of a document of one
// of $2 - its address begins with that issue's key and a slash - or an ask, a comment or a message
// of one, or a block of one, unless its state there is gone. It answers them by the parent's
// address, then rank, then the node's address, each compared byte for byte, with the columns the
// children read answers.
var treePlacedAboveStatement = `
	with documents as (
		select placed.from_id as ref_key
		from (
			select distinct on (t.from_id) t.from_id, t.asserted
			from graph_tuples t
			where t.to_kind = '` + graphstore.KindIssue + `' and t.to_id = $1 and t.kind = '` + graphstore.TuplePartOf + `'
			  and t.from_kind = '` + graphstore.KindArtifact + `' and t.to_kind <> '` + graphstore.KindValue + `' and t.seq <= $3
			order by t.from_id, t.at desc, t.seq desc
		) placed
		where placed.asserted
	), blocks as (
		select distinct member.from_id as id
		from documents
		cross join lateral (
			select distinct on (t.from_id) t.from_id, t.asserted
			from graph_tuples t
			where t.to_kind = '` + graphstore.KindArtifact + `' and t.to_id = documents.ref_key and t.kind = '` + TupleInDocument + `'
			  and t.from_kind = '` + BlockNodeKind + `' and t.to_kind <> '` + graphstore.KindValue + `' and t.seq <= $3
			order by t.from_id, t.at desc, t.seq desc
		) member
		where member.asserted
	)
	select placed.to_kind, placed.to_id, '` + BlockNodeKind + `', blocks.id, ` + treeNodeColumns + `
	from blocks
	cross join lateral (` + asOfPlacement(`'`+BlockNodeKind+`'`, `blocks.id`, `$3`) + `) placed
	` + asOfNodeJoins(`'`+BlockNodeKind+`'`, `blocks.id`, `$3`) + `
	where state.held is distinct from '` + graphstore.StateGone + `'
	  and (placed.to_kind = '` + BlockNodeKind + `' and split_part(placed.to_id, '/', 1) = any($2::text[])
	    or ` + itemOfIssuesSQL(ItemAsk, "asks", "$2") + `
	    or ` + itemOfIssuesSQL(ItemComment, "comments", "$2") + `
	    or ` + itemOfIssuesSQL(ItemMessage, "messages", "$2") + `)
	order by (placed.to_kind || ':' || placed.to_id) collate "C", rank.held is null, rank.held collate "C",
	         ('` + BlockNodeKind + `:' || blocks.id) collate "C"`

// itemOfIssuesSQL is true of a far end, placed.to_kind and placed.to_id, that is an item of kind,
// whose rows table holds, or a block of one (`<kind>/<uuid>#<block id>`, ParseItemBlockNode), of
// one of the issues the SQL text array issues names, read off the item's row by its key.
func itemOfIssuesSQL(kind ItemKind, table, issues string) string {
	id := `case when placed.to_kind = '` + string(kind) + `' then placed.to_id
		else split_part(split_part(placed.to_id, '#', 1), '/', 2) end`
	return `placed.to_kind in ('` + string(kind) + `', '` + kind.BlockKind() + `') and exists (
		select 1 from ` + table + ` item where item.id = ` + store.CastableUUID(id) + ` and item.issue_key = any(` + issues + `::text[]))`
}

// TreePlacedAbove is, at the cut readSeq, each block of the issue issueKey that stands under a
// node of one of ancestors, the issues above it (IssueAncestors), with that node as its Parent, in
// the order treePlacedAboveStatement answers them. An issue with no issue above holds none.
func TreePlacedAbove(ctx context.Context, q Queryer, issueKey string, ancestors []string, readSeq int64) ([]TreeChild, error) {
	if len(ancestors) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, treePlacedAboveStatement, issueKey, ancestors, readSeq)
	if err != nil {
		return nil, fmt.Errorf("read the nodes of %s under an issue above at seq %d: %w", issueKey, readSeq, err)
	}
	defer rows.Close()
	var placed []TreeChild
	var child TreeChild
	var row treeNodeRow
	targets := append([]any{&child.Parent.Kind, &child.Parent.ID, &child.Node.Kind, &child.Node.ID}, row.targets()...)
	for rows.Next() {
		child, row = TreeChild{}, treeNodeRow{}
		if err := rows.Scan(targets...); err != nil {
			return nil, fmt.Errorf("read a node of %s under an issue above: %w", issueKey, err)
		}
		row.fill(&child)
		placed = append(placed, child)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the nodes of %s under an issue above at seq %d: %w", issueKey, readSeq, err)
	}
	return placed, nil
}

// treeValuesStatement is, at the cut $3, every value out of each node $1 and $2 name: the newest
// value tuple of each attribute, kept while it asserts.
var treeValuesStatement = `
	select node.position, newest.kind, newest.to_id
	from unnest($1::text[], $2::text[]) with ordinality as node (kind, id, position)
	cross join lateral (
		select distinct on (t.kind) t.kind, t.to_id, t.asserted
		from graph_tuples t
		where t.from_kind = node.kind and t.from_id = node.id and t.to_kind = '` + graphstore.KindValue + `' and t.seq <= $3
		order by t.kind, t.at desc, t.seq desc
	) newest
	where newest.asserted
	order by node.position`

// treeRelationsStatement is, at the cut $3, every relation out of each node $1 and $2 name: the
// newest tuple of each kind and far end, kept while it asserts.
var treeRelationsStatement = `
	select node.position, newest.kind, newest.to_kind, newest.to_id
	from unnest($1::text[], $2::text[]) with ordinality as node (kind, id, position)
	cross join lateral (
		select distinct on (t.kind, t.to_kind, t.to_id) t.kind, t.to_kind, t.to_id, t.asserted
		from graph_tuples t
		where t.from_kind = node.kind and t.from_id = node.id and t.to_kind <> '` + graphstore.KindValue + `' and t.seq <= $3
		order by t.kind, t.to_kind, t.to_id, t.at desc, t.seq desc
	) newest
	where newest.asserted
	order by node.position`

// TreeRelation is one relation out of a node: its kind and its far end.
type TreeRelation struct {
	Kind string
	To   Target
}

// TreeFacts is what a node states at a cut: each attribute's value, by its name, and each
// relation, in the order the read answers them.
type TreeFacts struct {
	Values    map[string]string
	Relations []TreeRelation
}

// TreeFactsOf is what each of nodes states at the cut readSeq, keyed by the node: a node that
// states nothing has no entry.
func TreeFactsOf(ctx context.Context, q Queryer, nodes []Target, readSeq int64) (map[Target]TreeFacts, error) {
	facts := make(map[Target]TreeFacts, len(nodes))
	if len(nodes) == 0 {
		return facts, nil
	}
	kinds, ids := targetColumns(nodes)
	rows, err := q.Query(ctx, treeValuesStatement, kinds, ids, readSeq)
	if err != nil {
		return nil, fmt.Errorf("read the values of %d nodes at seq %d: %w", len(nodes), readSeq, err)
	}
	for rows.Next() {
		var position int
		var attribute, value string
		if err := rows.Scan(&position, &attribute, &value); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read a value at seq %d: %w", readSeq, err)
		}
		node := nodes[position-1]
		held := facts[node]
		if held.Values == nil {
			held.Values = map[string]string{}
		}
		held.Values[attribute] = value
		facts[node] = held
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the values of %d nodes at seq %d: %w", len(nodes), readSeq, err)
	}
	rows, err = q.Query(ctx, treeRelationsStatement, kinds, ids, readSeq)
	if err != nil {
		return nil, fmt.Errorf("read the relations of %d nodes at seq %d: %w", len(nodes), readSeq, err)
	}
	defer rows.Close()
	for rows.Next() {
		var position int
		var relation TreeRelation
		if err := rows.Scan(&position, &relation.Kind, &relation.To.Kind, &relation.To.ID); err != nil {
			return nil, fmt.Errorf("read a relation at seq %d: %w", readSeq, err)
		}
		node := nodes[position-1]
		held := facts[node]
		held.Relations = append(held.Relations, relation)
		facts[node] = held
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the relations of %d nodes at seq %d: %w", len(nodes), readSeq, err)
	}
	return facts, nil
}

// treeNodesStatement is the row of each node $1 and $2 name that the store holds: its supertag and
// when it was made, read off graph_nodes' primary key.
var treeNodesStatement = `
	select node.position, held.supertag, held.made_at
	from unnest($1::text[], $2::text[]) with ordinality as node (kind, id, position)
	join graph_nodes held on held.kind = node.kind and held.id = node.id`

// TreeNodeRow is what a node's row holds that a walk reads: its supertag and when it was made.
type TreeNodeRow struct {
	Supertag string
	MadeAt   time.Time
}

// TreeNodeRows is the row of each of nodes the store holds, keyed by the node.
func TreeNodeRows(ctx context.Context, q Queryer, nodes []Target) (map[Target]TreeNodeRow, error) {
	held := make(map[Target]TreeNodeRow, len(nodes))
	if len(nodes) == 0 {
		return held, nil
	}
	kinds, ids := targetColumns(nodes)
	rows, err := q.Query(ctx, treeNodesStatement, kinds, ids)
	if err != nil {
		return nil, fmt.Errorf("read the rows of %d nodes: %w", len(nodes), err)
	}
	defer rows.Close()
	for rows.Next() {
		var position int
		var row TreeNodeRow
		if err := rows.Scan(&position, &row.Supertag, &row.MadeAt); err != nil {
			return nil, fmt.Errorf("read a node's row: %w", err)
		}
		held[nodes[position-1]] = row
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the rows of %d nodes: %w", len(nodes), err)
	}
	return held, nil
}

// treeMovedToStatement follows, at the cut $2, the moved_to tuples from each block address of $1:
// from an address, the newest standing moved_to to a block or an ask, read through
// graph_tuples_as_of_out, and on from a block it reaches, at most $3 steps, as
// block_address_walk follows them now (migration 20261118165901); a step that ends at an ask ends
// the walk where it stands. It answers each address, the address its walk reaches, and the same
// block id in the room the reached address's document is edited in (block_room_address), null
// where its document has no room.
var treeMovedToStatement = `
	with recursive walk (start, address, hop) as (
		select start.address, start.address, 0 from unnest($1::text[]) as start (address)
		union all
		select walk.start, moved.to_id, walk.hop + 1
		from walk
		cross join lateral (
			select held.to_kind, held.to_id from (
				select distinct on (t.to_kind, t.to_id) t.to_kind, t.to_id, t.asserted, t.at, t.seq
				from graph_tuples t
				where t.from_kind = '` + BlockNodeKind + `' and t.from_id = walk.address and t.kind = '` + graphstore.TupleMovedTo + `'
				  and t.to_kind <> '` + graphstore.KindValue + `' and t.seq <= $2
				order by t.to_kind, t.to_id, t.at desc, t.seq desc
			) held
			where held.asserted and held.to_kind in ('` + BlockNodeKind + `', '` + graphstore.KindAsk + `')
			order by held.at desc, held.seq desc
			limit 1
		) moved
		where moved.to_kind = '` + BlockNodeKind + `' and walk.hop < $3
	)
	select distinct on (walk.start) walk.start, walk.address, block_room_address(walk.address)
	from walk
	order by walk.start, walk.hop desc`

// TreeMovedTo is, for each of addresses, the block addresses it resolves to at the cut readSeq,
// in the order a resolve takes them: the same block id in the room of the document the address
// its moved_to tuples reach is in, where that document has a room, and then that reached address
// (graphstore.MovedTo's order, read as of the cut).
func TreeMovedTo(ctx context.Context, q Queryer, addresses []string, readSeq int64) (map[string][]string, error) {
	reached := make(map[string][]string, len(addresses))
	if len(addresses) == 0 {
		return reached, nil
	}
	rows, err := q.Query(ctx, treeMovedToStatement, addresses, readSeq, graphstore.MovedToHops)
	if err != nil {
		return nil, fmt.Errorf("follow the moves of %d addresses at seq %d: %w", len(addresses), readSeq, err)
	}
	defer rows.Close()
	for rows.Next() {
		var start, address string
		var room *string
		if err := rows.Scan(&start, &address, &room); err != nil {
			return nil, fmt.Errorf("follow the moves of an address at seq %d: %w", readSeq, err)
		}
		if room != nil {
			reached[start] = append(reached[start], *room)
		}
		reached[start] = append(reached[start], address)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("follow the moves of %d addresses at seq %d: %w", len(addresses), readSeq, err)
	}
	return reached, nil
}

// targetColumns is targets as the two arrays a read binds them as: their kinds and their ids.
func targetColumns(targets []Target) (kinds, ids []string) {
	kinds, ids = make([]string, len(targets)), make([]string, len(targets))
	for index, target := range targets {
		kinds[index], ids[index] = target.Kind, target.ID
	}
	return kinds, ids
}
