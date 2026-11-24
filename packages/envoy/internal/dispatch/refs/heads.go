package refs

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
// issue whole, each with whether the reader whose credential $2 binds may see it (the block
// loader's predicate, VisibleKindRow over the row's own graph_nodes columns), its stored state and
// visibility selected as a loader selects a node's (StoredRowOf, null where the store holds no row
// of it): the rows rows selects, read once and sent as they are read. The documents a document
// block's row names are read after them, by theirs (headDocumentsStatement), so no row is held
// aside to find them: holding aside the 3,000 rows of an issue of the largest plan cost the read a
// third of its time. It is built at each read, as the block loader's is (blockNodesStatement): the
// registry the predicate reads is filled by the package's init, after its variables.
func headRowsStatement(rows string) string {
	return `
	select heads.node_kind, heads.node_address, heads.parent_kind, heads.parent_address, heads.rank, heads.text,
	       heads.made_at is not null, coalesce(heads.supertag, ''), ` + StoredRowOf("heads") + `,
	       heads.made_at, heads.authors, heads.first_authors, heads.first_at,
	       heads.document_id::text, heads.block_type, heads.text_sha256, heads.changed_version,
	       heads.made_at is null or ` + VisibleKindRow(BlockNodeKind, "heads", "$2") + `,
	       heads.value_kinds, heads.value_texts, heads.relation_kinds, heads.relation_to_kinds, heads.relation_to_ids
	from (` + rows + `) heads
	where exists (select 1 from issue_head_marks mark where mark.issue_key = $1)`
}

// headDocumentsStatement is the slug, the issue and the band of each document $1 names, the
// documents the head rows of a read name, each read by its key once.
var headDocumentsStatement = `
	select a.id::text, a.slug, a.issue_key, ` + DocumentBandSQL("a") + `
	from artifacts a
	where a.id = any($1::uuid[])`

// headRowsWhole is every row of the issue $1, one range of issue_head_nodes' primary key: what a
// view reads.
const headRowsWhole = `select * from issue_head_nodes whole where whole.issue_key = $1`

// headRowsSkeleton is the rows of the issue $1's skeleton (HeadSkeleton), each a range of
// issue_head_nodes' primary key, which sorts byte by byte: every row of a kind other than a
// block, a comment and a comment's block; the blocks whose address $3 and $4 bound, a pair for each
// document of the skeleton and one for the addresses below the issue's own, and the blocks whose
// address is $6 or above; and the comments $5 names, with their blocks. Each pair of bounds, and
// each comment of the page, is a range of its own read through the key (a lateral subquery Postgres
// keeps, `offset 0`): as a join, Postgres reckons each pair to keep a ninth of the issue's blocks,
// and over an issue of 46,000 blocks it read every row of the table rather than the 1,460 the
// pairs bound.
var headRowsSkeleton = `
		select * from issue_head_nodes kinds where kinds.issue_key = $1 and kinds.node_kind < '` + BlockNodeKind + `'
		union all
		select * from issue_head_nodes kinds where kinds.issue_key = $1 and kinds.node_kind > '` + BlockNodeKind + `' and kinds.node_kind < '` + string(ItemComment) + `'
		union all
		select * from issue_head_nodes kinds where kinds.issue_key = $1 and kinds.node_kind > '` + ItemComment.BlockKind() + `'
		union all
		select blocks.* from unnest($3::text[], $4::text[]) bounds(low, high)
		cross join lateral (
			select * from issue_head_nodes blocks where blocks.issue_key = $1 and blocks.node_kind = '` + BlockNodeKind + `'
			  and blocks.node_address >= bounds.low and blocks.node_address < bounds.high
			offset 0) blocks
		union all
		select * from issue_head_nodes blocks where blocks.issue_key = $1 and blocks.node_kind = '` + BlockNodeKind + `' and blocks.node_address >= $6
		union all
		select comments.* from issue_head_nodes comments
		where comments.issue_key = $1 and comments.node_kind = '` + string(ItemComment) + `' and comments.node_address = any($5::text[])
		union all
		select blocks.* from unnest($5::text[]) page(id)
		cross join lateral (
			select * from issue_head_nodes blocks where blocks.issue_key = $1 and blocks.node_kind = '` + ItemComment.BlockKind() + `'
			  and blocks.node_address >= '` + string(ItemComment) + `/' || page.id || '#' and blocks.node_address < '` + string(ItemComment) + `/' || page.id || '$'
			offset 0) blocks`

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
	rows, err := q.Query(ctx, statement, append([]any{headArraysAsText}, args...)...)
	if err != nil {
		return nil, false, fmt.Errorf("read the head rows of %s: %w", issueKey, err)
	}
	fields, held, err := drainHeadRows(rows)
	if err != nil {
		return nil, false, fmt.Errorf("read the head rows of %s: %w", issueKey, err)
	}
	if len(held) == 0 {
		return nil, false, nil
	}
	typeMap := pgtype.NewMap()
	if conn := rows.Conn(); conn != nil {
		typeMap = conn.TypeMap()
	}
	decode := headTextsText
	if fields[headFirstArray].Format == pgx.BinaryFormatCode {
		decode = headTexts
	}
	read := make([]HeadRow, 0, len(held))
	var blocks []headBlock
	var documents []string
	named := map[string]bool{}
	var plans []pgtype.ScanPlan
	for _, raw := range held {
		var head HeadRow
		var parentKind, parentID, document, blockType, textSHA256, state, visibility pgtype.Text
		var madeAt *time.Time
		var changedVersion pgtype.Int4
		var valueKinds, valueTexts, relationKinds, relationToKinds, relationToIDs []string
		destinations := []any{&head.Node.Kind, &head.Node.ID, &parentKind, &parentID, &head.Rank, &head.Text,
			&head.Stored, &head.Supertag, &state, &visibility, &madeAt, &head.Authors, &head.FirstAuthors, &head.FirstAt,
			&document, &blockType, &textSHA256, &changedVersion, &head.Seen}
		if plans == nil {
			plans = make([]pgtype.ScanPlan, len(destinations))
			for index, destination := range destinations {
				plans[index] = typeMap.PlanScan(fields[index].DataTypeOID, fields[index].Format, destination)
			}
		}
		for index, destination := range destinations {
			if err := plans[index].Scan(raw[index], destination); err != nil {
				return nil, false, fmt.Errorf("read the column %s of a head row of %s: %w", fields[index].Name, issueKey, err)
			}
		}
		for index, array := range []*[]string{&valueKinds, &valueTexts, &relationKinds, &relationToKinds, &relationToIDs} {
			if *array, err = decode(raw[headFirstArray+index]); err != nil {
				return nil, false, fmt.Errorf("read a head row of %s: %w", issueKey, err)
			}
		}
		head.State, head.Visibility = state.String, visibility.String
		if parentKind.Valid && parentID.Valid {
			head.Parent = Target{Kind: parentKind.String, ID: parentID.String}
		}
		if madeAt != nil {
			head.MadeAt = *madeAt
		}
		if document.Valid {
			_, blockID, _ := strings.Cut(head.Node.ID, "#")
			blocks = append(blocks, headBlock{read: len(read), row: BlockRow{BlockID: blockID, Document: document.String,
				Type: blockType.String, TextSHA256: textSHA256.String, ChangedVersion: int(changedVersion.Int32)}})
			if !named[document.String] {
				named[document.String] = true
				documents = append(documents, document.String)
			}
		}
		head.Facts = headFacts(head.Rank, head.Text, valueKinds, valueTexts, relationKinds, relationToKinds, relationToIDs)
		read = append(read, head)
	}
	if err := readHeadDocuments(ctx, q, issueKey, read, blocks, documents); err != nil {
		return nil, false, err
	}
	heads := make(map[Target]*HeadRow, len(read))
	for index := range read {
		heads[read[index].Node] = &read[index]
	}
	return newHeadTree(heads, read), true, nil
}

// drainHeadRows reads every row of the head rows statement off the connection as it arrives, each
// row's values copied into one allocation of its own, and closes the statement: its fields, and
// its rows' values, nil for a null. It decodes nothing, so Postgres sends the rows as fast as it
// writes them and the statement ends there; HeadRows decodes them after. Decoding each row as it
// arrived held the statement open while the client scanned it: of the 26 ms Postgres counted for
// the 3,013 rows of an issue of the largest plan, it spent 14 writing them and the rest waiting
// on the client to read the next.
func drainHeadRows(rows pgx.Rows) ([]pgconn.FieldDescription, [][][]byte, error) {
	defer rows.Close()
	var fields []pgconn.FieldDescription
	var held [][][]byte
	for rows.Next() {
		values := rows.RawValues()
		if fields == nil {
			fields = slices.Clone(rows.FieldDescriptions())
		}
		size := 0
		for _, value := range values {
			size += len(value)
		}
		bytes := make([]byte, size)
		row := make([][]byte, len(values))
		at := 0
		for index, value := range values {
			if value == nil {
				continue
			}
			row[index] = bytes[at : at+len(value) : at+len(value)]
			copy(row[index], value)
			at += len(value)
		}
		held = append(held, row)
	}
	rows.Close()
	return fields, held, rows.Err()
}

// headFirstArray is the column of headRowsStatement's first text[], value_kinds, which the four
// others follow: value_texts, relation_kinds, relation_to_kinds and relation_to_ids.
const headFirstArray = 19

// headArraysAsText asks Postgres for the head rows' text arrays in its text form, every other
// column as pgx asks for it: a text[] in binary carries a 20-byte header and a 4-byte length for
// each element, so the five arrays of the 3,013 rows of an issue of the largest plan are 1,114 kB
// in binary and 655 kB as text, of the 2,472 kB the rows are sent in, at about the same cost to
// Postgres to write.
var headArraysAsText = pgx.QueryResultFormatsByOID{pgtype.TextArrayOID: pgx.TextFormatCode}

// headTextsText is a text[] of a head row in Postgres's text form (array_out): `{a,b,…}`, an
// element quoted, with `"` and `\` escaped by a backslash, where it is empty, is NULL in any case,
// or holds a brace, a comma, a quote, a backslash or white space, and a lower bound other than one
// written before it as `[lower:upper]=`. The unquoted elements are cut out of one string of the
// whole value. An empty array is the empty slice; a null one, or an unquoted NULL element, which
// no head row holds, is an error.
func headTextsText(raw []byte) ([]string, error) {
	if raw == nil {
		return nil, errors.New("a text array of a head row is null")
	}
	whole := string(raw)
	if strings.HasPrefix(whole, "[") {
		_, after, cut := strings.Cut(whole, "=")
		if !cut {
			return nil, fmt.Errorf("a text array of a head row reads %q, with no = after its bounds", whole)
		}
		whole = after
	}
	if len(whole) < 2 || whole[0] != '{' || whole[len(whole)-1] != '}' {
		return nil, fmt.Errorf("a text array of a head row reads %q, not one {…}", whole)
	}
	body := whole[1 : len(whole)-1]
	texts := make([]string, 0, strings.Count(body, ",")+1)
	if body == "" {
		return texts, nil
	}
	for at := 0; ; {
		var text string
		if body[at] == '"' {
			var element strings.Builder
			at++
			for ; at < len(body) && body[at] != '"'; at++ {
				if body[at] == '\\' && at+1 < len(body) {
					at++
				}
				element.WriteByte(body[at])
			}
			if at >= len(body) {
				return nil, fmt.Errorf("a text array of a head row reads %q, with a quote it does not close", whole)
			}
			at++
			text = element.String()
		} else {
			end := strings.IndexByte(body[at:], ',')
			if end < 0 {
				end = len(body) - at
			}
			text = body[at : at+end]
			if strings.EqualFold(text, "NULL") {
				return nil, errors.New("a text array of a head row holds a null element")
			}
			at += end
		}
		texts = append(texts, text)
		if at == len(body) {
			return texts, nil
		}
		if body[at] != ',' {
			return nil, fmt.Errorf("a text array of a head row reads %q, with %q after an element", whole, body[at])
		}
		at++
	}
}

// headTexts is a text[] of a head row as Postgres sends it in binary, where a caller's connection
// asks for it so: its dimensions, whether it holds a null and its element type, the length and
// lower bound of its one dimension, and each element's length and bytes. The elements are cut out
// of one string of the whole value, so the array costs two allocations, where a scan through its
// codec costs one an element and more: the five arrays of each of 3,000 rows are what a client
// spends most on while Postgres waits to send the next rows. An empty array is the empty slice; a
// null one, or a null element, which no head row holds, is an error.
func headTexts(raw []byte) ([]string, error) {
	if raw == nil {
		return nil, errors.New("a text array of a head row is null")
	}
	if len(raw) < 12 {
		return nil, fmt.Errorf("a text array of a head row is %d bytes, short of its header", len(raw))
	}
	dimensions := int32(binary.BigEndian.Uint32(raw))
	if dimensions == 0 {
		return []string{}, nil
	}
	if dimensions != 1 || len(raw) < 20 {
		return nil, fmt.Errorf("a text array of a head row has %d dimensions in %d bytes, not one", dimensions, len(raw))
	}
	count := int32(binary.BigEndian.Uint32(raw[12:]))
	if count < 0 {
		return nil, fmt.Errorf("a text array of a head row holds %d elements", count)
	}
	whole := string(raw)
	texts := make([]string, count)
	at := 20
	for index := range texts {
		if at+4 > len(whole) {
			return nil, errors.New("a text array of a head row ends inside an element's length")
		}
		length := int(int32(binary.BigEndian.Uint32(raw[at:])))
		at += 4
		if length < 0 {
			return nil, errors.New("a text array of a head row holds a null element")
		}
		if at+length > len(whole) {
			return nil, errors.New("a text array of a head row ends inside an element")
		}
		texts[index] = whole[at : at+length]
		at += length
	}
	return texts, nil
}

// headBlock is the document_blocks row a head row of a document block holds, read, the row's index
// among the rows read, until its document's slug and band are read (readHeadDocuments).
type headBlock struct {
	read int
	row  BlockRow
}

// readHeadDocuments reads each of documents, the documents the head rows read name
// (headDocumentsStatement), and gives each row of blocks its document's issue, and its
// document_blocks row with the document's slug and band, where the document stands; a row whose
// document no artifact stands for holds neither.
func readHeadDocuments(ctx context.Context, q Queryer, issueKey string, read []HeadRow, blocks []headBlock, documents []string) error {
	if len(documents) == 0 {
		return nil
	}
	rows, err := q.Query(ctx, headDocumentsStatement, documents)
	if err != nil {
		return fmt.Errorf("read the documents of the head rows of %s: %w", issueKey, err)
	}
	type headDocument struct {
		slug     string
		issueKey *string
		band     string
	}
	found := make(map[string]headDocument, len(documents))
	for rows.Next() {
		var id string
		var document headDocument
		if err := rows.Scan(&id, &document.slug, &document.issueKey, &document.band); err != nil {
			rows.Close()
			return fmt.Errorf("read a document of the head rows of %s: %w", issueKey, err)
		}
		found[id] = document
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the documents of the head rows of %s: %w", issueKey, err)
	}
	for _, block := range blocks {
		document, held := found[block.row.Document]
		if !held {
			continue
		}
		head := &read[block.read]
		head.IssueKey = document.issueKey
		row := block.row
		row.Slug = document.slug
		if document.band != "" {
			row.Band = document.band[0]
		}
		head.Block = &row
	}
	return nil
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
