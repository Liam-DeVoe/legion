package refs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/itemblocks"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// IndexItemIfOwed indexes comment, message or ask itemID in the caller's transaction when its
// blocks are owed: its state row is missing (an older build wrote it before the state existed),
// owed (owed_at set by an older build's write, or by rebuild-refs), or indexed from another text
// than the stored one. It indexes as ReindexOwed does: the blocks and the mentions each
// holds, and for a comment or an issue message its own refs, at the time its body was written; a
// direct message writes no mention, and an ask's own refs are its question's, which the grammar
// reconciliation owns (ReindexOwed). A reply that names a block of the item calls it first,
// so the block it names is one the item holds now; the block-index reconciliation drains owed
// items through it (IndexOwedItemBlocks). The caller holds the item's owner row (the
// reconciliation holds none) and the item row; the index takes the state row after them. It
// reports whether the item was owed and so indexed.
//
// What the index states in the node and tuple store - the block nodes, their part_of and links,
// and the item's citations - is appended here, at the sentinel (graphstore.NoEvent): an owed
// index is of text an earlier write already appended an event for, and no event records this
// pass, whichever transaction it runs in. For the same reason it is dated by the time that text
// was written (writtenAt: a comment's coalesce(edited_at, created_at), a message's created_at),
// and not by this pass's clock: the facts are the item's text's, not the pass's
// (IndexOwedItemBlocks).
func IndexItemIfOwed(ctx context.Context, tx pgx.Tx, kind ItemKind, itemID, serverURL string) (bool, error) {
	var query string
	switch kind {
	case ItemAsk:
		return indexAskIfOwed(ctx, tx, itemID, serverURL)
	case ItemComment:
		query = `
			select c.body, coalesce(c.edited_at, c.created_at), true,
			       s.comment_id is not null, s.owed_at is not null, s.body_sha256
			from comments c left join comment_block_state s on s.comment_id = c.id
			where c.id = $1::uuid`
	case ItemMessage:
		query = `
			select m.body, m.created_at, m.issue_key is not null,
			       s.message_id is not null, s.owed_at is not null, s.body_sha256
			from messages m left join message_block_state s on s.message_id = m.id
			where m.id = $1::uuid`
	default:
		return false, fmt.Errorf("index the blocks of %s %s: not an item kind", kind, itemID)
	}
	var body string
	var writtenAt time.Time
	var mentions, stated, owed bool
	var sum *string
	if err := tx.QueryRow(ctx, query, itemID).Scan(&body, &writtenAt, &mentions, &stated, &owed, &sum); err != nil {
		return false, fmt.Errorf("read the block state of %s %s: %w", kind, itemID, err)
	}
	if stated && !owed && sum != nil && *sum == itemblocks.BodySHA256(body) {
		return false, nil
	}
	scope, err := readItemScope(ctx, tx, kind, itemID)
	if err != nil {
		return false, err
	}
	var located []text.Located
	if mentions {
		located = NewReader(ctx, tx, string(kind), itemID, serverURL).Mentions(body)
	}
	_, written, err := indexItem(ctx, tx, kind, itemID, body, writtenAt, located, nil, scope)
	if err != nil {
		return false, fmt.Errorf("index the blocks of %s %s: %w", kind, itemID, err)
	}
	if mentions {
		_, cited, err := replaceRefsOf(ctx, tx, string(kind), itemID, text.Refs(located), &writtenAt, scope)
		if err != nil {
			return false, fmt.Errorf("index %s %s: %w", kind, itemID, err)
		}
		written.Tuples = append(written.Tuples, cited...)
	}
	if err := graphstore.Caused(ctx, tx, written, graphstore.NoEvent); err != nil {
		return false, fmt.Errorf("state the blocks of %s %s in the store: %w", kind, itemID, err)
	}
	return true, nil
}

// CurrentItemBlock is block blockID of comment, message or ask itemID while a current revision of
// the item holds it: its node id and stored excerpt. found is false when the item holds no such
// block now, a removed one included, and when who may not see the item the block is part of,
// which is then answered as a block the item does not hold.
//
// The visibility read is of the item's own node - the comment, the message or the ask - and not
// of a node of the block's own kind: a block's text is its item's text, and the store holds a
// node for the item (graphstore.KindComment), where no writer makes a `comment_block`,
// `message_block` or `ask_block` node, so a test over one of those always passed.
func CurrentItemBlock(ctx context.Context, q Queryer, who model.Actor, kind ItemKind, itemID, blockID string) (nodeID, excerpt string, found bool, err error) {
	tables, err := kind.tables()
	if err != nil {
		return "", "", false, err
	}
	err = q.QueryRow(ctx, `
		select b.node_id, b.excerpt from `+tables.blocks+` b
		where b.`+tables.item+` = $1::uuid and b.block_id = $2 and b.position is not null
		  and `+VisibleKind(string(kind), "b."+tables.item+"::text", "$3")+`
	`, itemID, blockID, VisibilityArg(who)).Scan(&nodeID, &excerpt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("read block %s of %s %s: %w", blockID, kind, itemID, err)
	}
	return nodeID, excerpt, true, nil
}

// ReplyBlocks reads the block each of the replies replyIDs names in replies_to_block, keyed by
// reply id, in one query; a reply that names no block has no entry. kind is the kind of item the
// block belongs to, which names the table its blocks are in and the column keying them
// (itemTables); the replies are read from the table of the kind that answers such a block
// (repliesFrom): a comment's and an ask's replies are comments, a message's are messages. A
// block belongs to an item in its reply's own thread, so its reference is addressed under the
// reply's owner (as itemBlockParent addresses the reply's parent); a message is owned by its
// issue alone, so a message reply needs neither project nor slug, and a direct message's block
// has no reference. Text is the block's stored excerpt, which a removal keeps, so a removed
// block reads the last text it held.
//
// It answers for who: a block of an item who may not see is answered as no block, so the reply
// that names it reads as one that names none, and neither the block's id nor its text is
// answered. That test is of the item's own node, as CurrentItemBlock's is.
func ReplyBlocks(ctx context.Context, q Queryer, who model.Actor, kind ItemKind, replyIDs []string) (map[string]*model.ReplyBlock, error) {
	blocks := make(map[string]*model.ReplyBlock)
	if len(replyIDs) == 0 {
		return blocks, nil
	}
	query, err := ReplyBlocksStatement(kind)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, query, replyIDs, VisibilityArg(who))
	if err != nil {
		return nil, fmt.Errorf("read the blocks %s replies name: %w", kind, err)
	}
	defer rows.Close()
	for rows.Next() {
		replyID, block, err := ScanReplyBlock(rows, kind)
		if err != nil {
			return nil, err
		}
		blocks[replyID] = block
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the blocks %s replies name: %w", kind, err)
	}
	return blocks, nil
}

// ReplyBlocksStatement is the statement ReplyBlocks reads the blocks of kind its replies name
// with: $1 the reply ids (uuid[]) and $2 the acting credential (VisibilityArg), one row per reply
// whose block who may see, read by ScanReplyBlock. It is exported for a reader that splices it
// into a statement of its own over replies of its own (api's GET /api/v1/me/inbox). A kind that
// is no item kind is refused.
func ReplyBlocksStatement(kind ItemKind) (string, error) {
	tables, err := kind.tables()
	if err != nil {
		return "", fmt.Errorf("read reply blocks of %s: not an item kind", kind)
	}
	replies, _ := repliesFrom(kind).tables()
	owner, joins := `coalesce(i.project_key, a.project_key, ''), coalesce(a.slug, '')`, `
			left join issues i on i.key = r.issue_key
			left join artifacts a on a.id = r.artifact_id`
	if repliesFrom(kind) == ItemMessage {
		owner, joins = `'', ''`, ``
	}
	return `
		select r.id::text, b.node_id, b.block_id, b.excerpt, b.position is null, b.` + tables.item + `::text,
		       r.issue_key, ` + owner + `
		from ` + replies.items + ` r
		join ` + tables.blocks + ` b on b.node_id = r.replies_to_block` + joins + `
		where r.id = any($1::uuid[]) and r.replies_to_block is not null
		  and ` + VisibleKind(string(kind), "b."+tables.item+"::text", "$2"), nil
}

// ScanReplyBlock reads one row of ReplyBlocksStatement(kind): the reply's id and the block it
// names, its reference addressed under the reply's owner.
func ScanReplyBlock(row pgx.Row, kind ItemKind) (replyID string, block *model.ReplyBlock, err error) {
	var itemID, project, slug string
	var issueKey *string
	block = &model.ReplyBlock{}
	if err := row.Scan(&replyID, &block.Node, &block.BlockID, &block.Text, &block.Removed, &itemID, &issueKey, &project, &slug); err != nil {
		return "", nil, fmt.Errorf("scan the block a %s reply names: %w", kind, err)
	}
	block.Ref = BlockRef(itemRef(string(kind), issueKey, project, slug, itemID), block.BlockID)
	return replyID, block, nil
}
