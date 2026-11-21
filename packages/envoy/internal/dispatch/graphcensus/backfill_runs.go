package graphcensus

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// runRowHeld is true of an interaction node n whose going_requests row stands. Every writer of an
// interaction keys it by its run's uuid as Postgres writes it, and an id of any other shape is
// read as a run with no row rather than cast (store.CanonicalUUID), so no node can fail the read.
var runRowHeld = `exists (
		select 1 from going_requests r
		where r.id = ` + store.CanonicalUUID("n.id") + `)`

// runOwed is the from and the where of the runs whose row names no interaction node, which
// backfillRuns reads in batches and counts once it is done, $1 the interaction kind.
const runOwed = `
		from going_requests r
		where not exists (select 1 from graph_nodes n where n.kind = $1 and n.id = r.id::text)`

// runRowless is the from and the where of the interaction nodes, not gone, whose row is gone
// (runRowHeld), which backfillGoneRuns reads in batches and counts once it is done, $1 the
// interaction kind and $2 the gone state.
var runRowless = `
		from graph_nodes n
		where n.kind = $1 and n.state <> $2 and not ` + runRowHeld

// backfillRuns gives every run going_requests holds and the store holds no interaction node of its
// node, through the one writer of such a node from its row (graphstore.WriteRun): the node, its
// author's node and tuples, and a frame value of every frame its row holds, in the shape the going
// POST and the frame path write them. It reads the runs owed in batches by id, and writes each in
// a transaction of its own, which takes the run's row first and reads again whether the node is
// there, so a run the frame path or another backfill gave its node meanwhile is left as it is.
func backfillRuns(ctx context.Context, database *store.Store) (KindBackfill, error) {
	done := KindBackfill{Kind: graphstore.KindInteraction}
	after := uuid.Nil.String()
	for {
		owed, err := readOwedRuns(ctx, database, after, backfillBatch)
		if err != nil {
			return done, err
		}
		for _, id := range owed {
			written, err := writeOwedRun(ctx, database, id)
			if err != nil {
				return done, err
			}
			if written.Nodes > 0 {
				done.Rows++
				done.Nodes += written.Nodes
				done.Tuples += written.Tuples
			}
		}
		if len(owed) < backfillBatch {
			break
		}
		after = owed[len(owed)-1]
	}
	if err := database.Pool.QueryRow(ctx, `select count(*)`+runOwed, graphstore.KindInteraction).Scan(&done.Remaining); err != nil {
		return done, fmt.Errorf("count the runs the store holds no node of: %w", err)
	}
	done.Complete = done.Remaining == 0
	return done, nil
}

// readOwedRuns reads the runs past after, in id order, whose row names no interaction node, at
// most limit of them.
func readOwedRuns(ctx context.Context, database *store.Store, after string, limit int) ([]string, error) {
	rows, err := database.Pool.Query(ctx, `
		select r.id::text`+runOwed+` and r.id > $2::uuid
		order by r.id
		limit $3
	`, graphstore.KindInteraction, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read the runs the store holds no node of: %w", err)
	}
	owed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the runs the store holds no node of: %w", err)
	}
	return owed, nil
}

// writeOwedRun writes the node of one run in a transaction of its own (graphstore.WriteRun).
func writeOwedRun(ctx context.Context, database *store.Store, id string) (graphstore.RunWritten, error) {
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		return graphstore.RunWritten{}, fmt.Errorf("begin the node of run %s: %w", id, err)
	}
	defer tx.Rollback(ctx)
	written, err := graphstore.WriteRun(ctx, tx, id)
	if err != nil {
		return graphstore.RunWritten{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return graphstore.RunWritten{}, fmt.Errorf("commit the node of run %s: %w", id, err)
	}
	return written, nil
}

// backfillRunFrames states a frame value of every going_events row whose run's interaction holds
// no frame value of that sequence (graphstore.FrameHeldSQL): a frame a process of the release
// before appended to a run this build or the passes before gave a node. Each owed run is written in
// a transaction of its own (writeRunFrames). Rows counts the frames it stated and Tuples the
// values, one each, with what graphstore.WriteRun stated of a run that had no node by then.
func backfillRunFrames(ctx context.Context, database *store.Store) (KindBackfill, error) {
	done := KindBackfill{Kind: graphstore.AttributeFrame}
	after := uuid.Nil.String()
	for {
		owed, err := readOwedFrameRuns(ctx, database, after, backfillBatch)
		if err != nil {
			return done, err
		}
		for _, run := range owed {
			frames, written, err := writeRunFrames(ctx, database, run)
			if err != nil {
				return done, err
			}
			done.Rows += frames
			done.Tuples += frames + written.Tuples
			done.Nodes += written.Nodes
		}
		if len(owed) < backfillBatch {
			break
		}
		after = owed[len(owed)-1]
	}
	var left int64
	if err := database.Pool.QueryRow(ctx, `select count(*) from going_events e where not `+graphstore.FrameHeldSQL).Scan(&left); err != nil {
		return done, fmt.Errorf("count the frames the store holds no value of: %w", err)
	}
	done.Remaining = left
	done.Complete = left == 0
	return done, nil
}

// readOwedFrameRuns reads the runs past after, in id order, that hold a frame the store holds no
// value of, at most limit of them. The runs are grouped and ordered by the uuid itself, the order
// `after` compares in, so the read takes going_events' (request_id, sequence) key in its order.
func readOwedFrameRuns(ctx context.Context, database *store.Store, after string, limit int) ([]string, error) {
	rows, err := database.Pool.Query(ctx, `
		select e.request_id::text from going_events e
		where e.request_id > $1::uuid and not `+graphstore.FrameHeldSQL+`
		group by e.request_id
		order by e.request_id
		limit $2
	`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read the runs holding a frame the store holds no value of: %w", err)
	}
	owed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the runs holding a frame the store holds no value of: %w", err)
	}
	return owed, nil
}

// writeRunFrames states, in one transaction, a frame value of each frame of run the store holds no
// value of, and answers how many. It takes the run's interaction node row first
// (graphstore.LockInteraction), as every frame of this build does (api's appendGoingEvent), so a
// frame this build is appending to the run commits first, value and row together, and the read
// under the lock no longer counts it owed. A run with no node by then gets it from its row through
// graphstore.WriteRun, which states every frame the row holds, and which it answers. The values are
// read off the rows (graphstore.ReadRunFrames), exactly as the frame path states them.
func writeRunFrames(ctx context.Context, database *store.Store, run string) (int64, graphstore.RunWritten, error) {
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		return 0, graphstore.RunWritten{}, fmt.Errorf("begin the frames of run %s: %w", run, err)
	}
	defer tx.Rollback(ctx)
	locked, err := graphstore.LockInteraction(ctx, tx, run)
	if err != nil {
		return 0, graphstore.RunWritten{}, err
	}
	if !locked {
		written, err := graphstore.WriteRun(ctx, tx, run)
		if err != nil {
			return 0, graphstore.RunWritten{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return 0, graphstore.RunWritten{}, fmt.Errorf("commit the node of run %s: %w", run, err)
		}
		return 0, written, nil
	}
	frames, err := graphstore.ReadRunFrames(ctx, tx, run, true)
	if err != nil {
		return 0, graphstore.RunWritten{}, err
	}
	if len(frames) == 0 {
		return 0, graphstore.RunWritten{}, nil
	}
	if err := graphstore.Write(ctx, tx, nil, frames); err != nil {
		return 0, graphstore.RunWritten{}, fmt.Errorf("state the frames of run %s: %w", run, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, graphstore.RunWritten{}, fmt.Errorf("commit the frames of run %s: %w", run, err)
	}
	return int64(len(frames)), graphstore.RunWritten{}, nil
}

// backfillGoneRuns states gone every interaction node, not gone already, whose going_requests row
// is gone (runRowHeld): a run a process of the release before deleted an hour old, frames with it,
// whose node it left standing. The store keeps the node, as it keeps every node an erase has not
// removed, and the census reads a gone node with no row as no difference. A run an erase removed
// has no node either, so this states nothing of it.
func backfillGoneRuns(ctx context.Context, database *store.Store) (KindBackfill, error) {
	done := KindBackfill{Kind: "interaction_gone"}
	after := ""
	for {
		owed, err := readRowlessRuns(ctx, database, after, backfillBatch)
		if err != nil {
			return done, err
		}
		for _, id := range owed {
			stated, err := writeGoneRun(ctx, database, id)
			if err != nil {
				return done, err
			}
			if stated {
				done.Rows++
				done.Tuples++
			}
		}
		if len(owed) < backfillBatch {
			break
		}
		after = owed[len(owed)-1]
	}
	if err := database.Pool.QueryRow(ctx, `select count(*)`+runRowless, graphstore.KindInteraction, graphstore.StateGone).Scan(&done.Remaining); err != nil {
		return done, fmt.Errorf("count the runs whose row is gone: %w", err)
	}
	done.Complete = done.Remaining == 0
	return done, nil
}

// readRowlessRuns reads the interaction nodes past after, in id order, not gone and whose row is
// gone, at most limit of them.
func readRowlessRuns(ctx context.Context, database *store.Store, after string, limit int) ([]string, error) {
	rows, err := database.Pool.Query(ctx, `
		select n.id`+runRowless+` and n.id > $3
		order by n.id
		limit $4
	`, graphstore.KindInteraction, graphstore.StateGone, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read the runs whose row is gone: %w", err)
	}
	owed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the runs whose row is gone: %w", err)
	}
	return owed, nil
}

// writeGoneRun states the interaction node id gone where its row is still gone, in a transaction
// of its own, and answers whether it did. It takes the node's row first, as a frame and an erase
// of the run do, then reads under that lock the node and whether its row stands: the going POST
// commits a run's node and its row together, so a node it reads has its row whenever the run has
// one, and a node an erase removed meanwhile is read as none. The write is the node as the store
// holds it but for its state, a plain write that moves the row and appends the state tuple, made
// by the run (backfillActor) now, as api's writeNodeState moves a node's state.
func writeGoneRun(ctx context.Context, database *store.Store, id string) (bool, error) {
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin the end of run %s: %w", id, err)
	}
	defer tx.Rollback(ctx)
	if _, err := graphstore.LockInteraction(ctx, tx, id); err != nil {
		return false, err
	}
	node, held, err := graphstore.ReadNode(ctx, tx, graphstore.KindInteraction, id)
	if err != nil || !held || node.State == graphstore.StateGone {
		return false, err
	}
	var rowHeld bool
	if err := tx.QueryRow(ctx, `select `+runRowHeld+` from graph_nodes n where n.kind = $1 and n.id = $2`,
		graphstore.KindInteraction, id).Scan(&rowHeld); err != nil {
		return false, fmt.Errorf("read whether run %s holds its row: %w", id, err)
	}
	if rowHeld {
		return false, nil
	}
	node.State, node.Restates = graphstore.StateGone, false
	node.MadeAt, node.MadeBy, node.SourceEvent = time.Now().UTC(), backfillActor, graphstore.NoEvent
	if err := graphstore.Write(ctx, tx, []graphstore.Node{node}, nil); err != nil {
		return false, fmt.Errorf("state run %s gone: %w", id, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit the end of run %s: %w", id, err)
	}
	return true, nil
}
