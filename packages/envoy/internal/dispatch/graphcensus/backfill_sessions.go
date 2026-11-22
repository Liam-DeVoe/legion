package graphcensus

import (
	"context"
	"fmt"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// sessionRow is one session the store census holds to a node (recordedSessions) whose node the
// store does not hold: its id, and the cause and the time its node is made at.
type sessionRow struct {
	id    string
	event int64
	at    time.Time
}

// backfillSessions writes the node of every session the store census holds to one and that the
// store holds no node of: the sessions that asked, followed or were reached before the store
// existed, or wrote a comment the comment pass could not give a node. Its input is
// recordedSessions, the census's own session row side, so the sessions this gives a node are
// exactly the sessions the census holds to one, as backfillRoles's input is the census's role row
// side.
//
// A session has no projection row, so there is no row to name the node on and no fence to take.
// It walks only the sessions that carry no node, and a node another write made meanwhile is
// written `do nothing` (graphstore.WriteArms), so a second run, and a run beside a live server,
// writes nothing for a session the store already holds. The writer is the run (backfillActor);
// the cause is the first event that named the session - an ask.follower_added, a comment.delivery
// receipt, or the event that stored a comment the erase took - or the sentinel for a session only
// a row names, and the time is that event's, or the earliest row's that names it.
//
// No arm of graph_edges answers a session, so its Complete switches nothing off: it reads whether
// every session the census names holds its node.
func backfillSessions(ctx context.Context, database *store.Store) (KindBackfill, error) {
	done := KindBackfill{Kind: graphstore.KindSession}
	missing, err := readSessionRows(ctx, database)
	if err != nil {
		return done, err
	}
	for start := 0; start < len(missing); start += backfillBatch {
		batch := missing[start:min(start+backfillBatch, len(missing))]
		nodes, tuples, err := writeSessionBatch(ctx, database.Pool, batch)
		if err != nil {
			return done, err
		}
		done.Rows += int64(len(batch))
		done.Nodes += nodes
		done.Tuples += tuples
	}
	left, err := readSessionRows(ctx, database)
	if err != nil {
		return done, err
	}
	done.Remaining = int64(len(left))
	done.Complete = done.Remaining == 0
	return done, nil
}

// readSessionRows reads every session recordedSessions names whose node the store does not hold
// and whose id fits the store's bound, in id order. A session past the bound is no node of the
// store, so it is neither written here nor counted as remaining (graphstore.QueueSessionRecorded).
func readSessionRows(ctx context.Context, database *store.Store) ([]sessionRow, error) {
	rows, err := database.Pool.Query(ctx, `
		select recorded.id, coalesce(recorded.event, $1), coalesce(e.created_at, recorded.at)
		from (`+recordedSessions(inlineAuthors, MessagesNameTheirNode)+`) recorded
		left join events e on e.id = recorded.event
		left join graph_nodes node on node.kind = $2::text and node.id = recorded.id
		where node.id is null and octet_length(recorded.id) <= $3
		order by recorded.id
	`, graphstore.NoEvent, graphstore.KindSession, graphstore.MaxAddressBytes)
	if err != nil {
		return nil, fmt.Errorf("read the sessions the store holds no node of: %w", err)
	}
	defer rows.Close()
	var missing []sessionRow
	for rows.Next() {
		var row sessionRow
		if err := rows.Scan(&row.id, &row.event, &row.at); err != nil {
			return nil, fmt.Errorf("read a session the store holds no node of: %w", err)
		}
		missing = append(missing, row)
	}
	return missing, rows.Err()
}

// attemptSessions reads, of the comment_deliveries rows keys names (the attempt pass's keys,
// commentAttempts), each session one the listener took reached - recordedSessions's arm over
// comment_deliveries - whose node the store does not hold and whose id fits its bound, with the
// first comment.delivery receipt under the comment's owner that says the listener took an attempt
// to it, the cause backfillSessions gives such a session, which recordedSessions reads too, or the
// sentinel where none does, and the time of that receipt or of the earliest of those rows. The
// block-index tick's catch-up of an attempt row a build before this one left (CatchUpComments,
// CatchUpOwedRows) writes those nodes before the attempts, as the backfill runs its session pass
// before its attempt pass: a row an older build settled after the backfill names a session no
// pass of it gave a node.
func attemptSessions(ctx context.Context, q graphstore.Queryer, keys [][]string) ([]sessionRow, error) {
	rows, err := q.Query(ctx, `
		select d.session_id, coalesce(min(receipt.id), $4), min(coalesce(receipt.created_at, d.created_at))
		from comment_deliveries d
		join comments c on c.id = d.comment_id
		left join lateral (
			select e.id, e.created_at from events e
			where coalesce(e.issue_key, e.artifact_id::text, e.project_key) = coalesce(c.issue_key, c.artifact_id::text)
			  and e.type = 'comment.delivery' and e.payload->>'comment_id' = d.comment_id::text
			  and e.payload->>'session_id' = d.session_id and e.payload->>'state' in `+model.DeliveryTakenStatesSQL+`
			order by e.id limit 1
		) receipt on true
		where (d.comment_id, d.target, d.attempt) in (select * from unnest($1::uuid[], $2::text[], $3::text[]::int[]))
		  and coalesce(d.session_id, '') <> '' and d.state in `+model.DeliveryTakenStatesSQL+`
		  and octet_length(d.session_id) <= $5
		  and not exists (select 1 from graph_nodes n where n.kind = $6 and n.id = d.session_id)
		group by d.session_id
		order by d.session_id
	`, column(keys, 0), column(keys, 1), column(keys, 2), graphstore.NoEvent, graphstore.MaxAddressBytes, graphstore.KindSession)
	if err != nil {
		return nil, fmt.Errorf("read the sessions a batch of attempts reached that hold no node: %w", err)
	}
	defer rows.Close()
	var missing []sessionRow
	for rows.Next() {
		var row sessionRow
		if err := rows.Scan(&row.id, &row.event, &row.at); err != nil {
			return nil, fmt.Errorf("read a session a batch of attempts reached that holds no node: %w", err)
		}
		missing = append(missing, row)
	}
	return missing, rows.Err()
}

// writeSessionBatch writes one batch's session nodes in one statement through q, the pool for the
// backfill's session pass and the catch-up's transaction for the block-index tick
// (writeAttemptsAndSessions), and answers the nodes it wrote and, as the upper bound the report
// states, the tuples: the two base attributes of each node, which the write appends only for a node
// its insert made.
func writeSessionBatch(ctx context.Context, q refs.Execer, batch []sessionRow) (int64, int64, error) {
	nodes := make([]graphstore.Node, 0, len(batch))
	for _, row := range batch {
		nodes = append(nodes, graphstore.SessionNode(row.id, backfillActor, row.at, row.event))
	}
	arms, params, err := graphstore.WriteArms(1, nodes, nil, nil)
	if err != nil {
		return 0, 0, err
	}
	if _, err := q.Exec(ctx, "with "+arms+"\nselect 1", params...); err != nil {
		return 0, 0, fmt.Errorf("write the nodes of a session batch: %w", err)
	}
	return int64(len(nodes)), 2 * int64(len(nodes)), nil
}
