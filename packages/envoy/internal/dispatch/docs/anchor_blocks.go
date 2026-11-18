package docs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// AnchorBlockBackfillActor is who an anchor block backfill records as the writer of each
// anchored_in it states: the command is nobody's act.
var AnchorBlockBackfillActor = model.Actor{Kind: "system", ID: "dispatch-anchor-block-backfill"}

// AnchorBlockBackfill reports how many legacy anchor rows gained their stable block identity.
type AnchorBlockBackfill struct {
	Asks     int
	Comments int
	Skipped  int
}

// BackfillAnchorBlocks resolves legacy quote anchors that still have one matching passage.
// Rows with a missing or ambiguous quote stay unchanged, so the command is idempotent and never
// guesses a block for an old row. A comment's anchor it gives a block states its anchored_in to
// that block in the transaction that writes the block id (pinCommentAnchorBlock).
func (s *Service) BackfillAnchorBlocks(ctx context.Context) (AnchorBlockBackfill, error) {
	ctx = store.WithTransactionTracking(ctx)
	var result AnchorBlockBackfill
	for _, target := range []struct {
		table string
		count *int
	}{
		{table: "asks", count: &result.Asks},
		{table: "comments", count: &result.Comments},
	} {
		updated, skipped, err := s.backfillAnchorBlocks(ctx, target.table)
		if err != nil {
			return AnchorBlockBackfill{}, err
		}
		*target.count = updated
		result.Skipped += skipped
	}
	return result, nil
}

func (s *Service) backfillAnchorBlocks(ctx context.Context, table string) (int, int, error) {
	rows, err := s.store.Pool.Query(ctx, fmt.Sprintf(`
		select id::text, anchor
		from %s
		where anchor is not null and nullif(anchor->>'block_id', '') is null
		order by id
	`, table))
	if err != nil {
		return 0, 0, fmt.Errorf("list %s anchor blocks: %w", table, err)
	}
	defer rows.Close()

	type row struct {
		id     string
		anchor model.Anchor
	}
	var candidates []row
	for rows.Next() {
		var candidate row
		var encoded []byte
		if err := rows.Scan(&candidate.id, &encoded); err != nil {
			return 0, 0, fmt.Errorf("scan %s anchor block: %w", table, err)
		}
		if err := model.DecodeAnchor(encoded, &candidate.anchor); err != nil {
			return 0, 0, fmt.Errorf("decode %s anchor block: %w", table, err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("iterate %s anchor blocks: %w", table, err)
	}

	updated := 0
	skipped := 0
	for _, candidate := range candidates {
		blockID, err := s.BlockForQuote(ctx, candidate.anchor.ArtifactID, candidate.anchor.Quote)
		if errors.Is(err, pmdoc.ErrTargetNotFound) {
			skipped++
			continue
		}
		var ambiguous *pmdoc.ErrTargetAmbiguous
		if errors.As(err, &ambiguous) {
			skipped++
			continue
		}
		if err != nil {
			return 0, 0, fmt.Errorf("resolve %s anchor %s: %w", table, candidate.id, err)
		}
		if blockID == "" {
			skipped++
			continue
		}
		pinned, err := s.pinAnchorBlock(ctx, table, candidate.id, blockID)
		if err != nil {
			return 0, 0, err
		}
		if pinned {
			updated++
		}
	}
	return updated, skipped, nil
}

// pinAnchorBlock writes blockID into the anchor of row id of table where that anchor still names
// no block, and answers whether it did. An ask's anchor is written on its own; a comment's in a
// transaction with its anchored_in (pinCommentAnchorBlock).
func (s *Service) pinAnchorBlock(ctx context.Context, table, id, blockID string) (bool, error) {
	if table == "comments" {
		return s.pinCommentAnchorBlock(ctx, id, blockID)
	}
	command, err := s.store.Pool.Exec(ctx, fmt.Sprintf(`
		update %s
		set anchor = jsonb_set(anchor, '{block_id}', to_jsonb($2::text), true)
		where id = $1 and nullif(anchor->>'block_id', '') is null
	`, table), id, blockID)
	if err != nil {
		return false, fmt.Errorf("update %s anchor %s: %w", table, id, err)
	}
	return command.RowsAffected() > 0, nil
}

// pinCommentAnchorBlock writes blockID into the anchor of comment id where that anchor still names
// no block, and states the anchored_in the anchor now names (graphstore.AnchoredIn), in one
// transaction: the comment's row, which the update locks, then the tuple, dated now by
// AnchorBlockBackfillActor at the sentinel, since the backfill appends no event. The tuple's far
// end is read off the row the update leaves (graphstore.CommentAnchoredInSQL), so an anchor the
// refresh orphaned meanwhile, or a comment the store holds no node of, states none. It takes no
// owner row: a settlement holds its owner's row while it refreshes the anchors, and the refresh
// writes only the anchor's quote and orphan flag, never its block, so the two meet at the row
// alone.
func (s *Service) pinCommentAnchorBlock(ctx context.Context, id, blockID string) (bool, error) {
	pinned := false
	if err := pgx.BeginFunc(ctx, s.store.Pool, func(tx pgx.Tx) error {
		var block *string
		err := tx.QueryRow(ctx, `
			update comments c
			set anchor = jsonb_set(c.anchor, '{block_id}', to_jsonb($2::text), true)
			where c.id = $1 and nullif(c.anchor->>'block_id', '') is null
			returning case when c.node_kind is not null then `+graphstore.CommentAnchoredInSQL("c")+` end
		`, id, blockID).Scan(&block)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		pinned = true
		if block == nil || !graphstore.AddressFits(*block) {
			return nil
		}
		return graphstore.Write(ctx, tx, nil, []graphstore.Tuple{
			graphstore.AnchoredIn(id, *block, true, AnchorBlockBackfillActor, time.Now().UTC(), graphstore.NoEvent),
		})
	}); err != nil {
		return false, fmt.Errorf("update comments anchor %s: %w", id, err)
	}
	return pinned, nil
}
