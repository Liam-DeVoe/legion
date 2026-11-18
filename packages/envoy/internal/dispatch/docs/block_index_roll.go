package docs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/processes"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// blockIndexEventPage is the most events one tick reads (readEventsAfterCursor). A tick reads the
// events after its build's cursor, at most this many, and moves the cursor to the last one it read
// when the page is full, so it reads at most this many event rows however far the log has run
// ahead of the cursor; a full page makes the tick full (BlockIndexTick.Full), and the next tick,
// blockIndexStep later, reads on from there. It is the suggestion outcome sweep's page (api's
// suggestionOutcomePage).
const blockIndexEventPage = 1000

// markVersionedDocuments is one tick's marking, in one statement: it reads up to $3 events after
// the cursor ($1) and at or below the safe horizon ($2), in id order, and marks owed a block index,
// with this build's own mark, every document an artifact.version or artifact.created event among
// them names, and every document of each issue an issue.created event among them names, that
// holds no such mark yet and whose blocks this build's store last stated at a version other than
// the one the document stands at. It answers how many events it read, the id of the last one
// (null when it read none), how many documents it marked, and the comments a comment.created or
// comment.answered event among them names, in id order, each beside the first of those events that
// names it.
//
// A version event names its document in payload->>'artifact_id' (ArtifactVersionEventPayload),
// and the event that creates a document, which records its version 1, carries the whole artifact
// in its payload and names the document in payload->'artifact'->>'id' (api's
// artifactCreatedEventPayload, read the same way by versionEventID). An issue's create writes its
// primary document, the spec, and records that document's version 1 with the issue.created event
// alone (api's issue create, read the same way by versionEventID), which names the issue and no
// document: the event's issue_key, its owner, is the issue, so every document of kind 'doc' that
// issue holds in artifacts is marked, read through artifacts_issue_key. A comment's event carries
// the comment, the reply a comment.answered stores included, and names it in payload->>'id' (api's
// commentEventPayload). Each event names what it names whichever build wrote it, and the read
// filters on nothing else: a project document's event carries no issue key, and an agent's
// workspace document writes no event at all. The payload is read only for the four types that
// name a document or a comment, and a value that is no uuid names nothing rather than failing the
// tick (store.CastableUUID). Only a document that exists is marked, and only one of kind 'doc',
// since the row is joined to its artifact. The page bounds the issues as it bounds the documents,
// and an issue holds a few documents.
//
// A document is marked where store_version, the version whose blocks this build's index pass last
// stated in the store (refs' standsAtVersion, its one writer), is not the document's version, a
// null on either side included. The release before this one moves version and never
// store_version, so a document it indexed after this build's store stated it is marked, and so is
// one it created, whose store_version is null, and one whose version a write owes an index still
// (null): whatever that version drops, moves or rewords, which may leave no row naming no node,
// this build's pass over it states. A document this build wrote and indexed stands at the
// store_version that pass wrote, and is not marked again.
//
// The mark is the one the backfill sets (graphcensus's markStaleBatch) and oweDocumentIndex keeps:
// store_owed_at, which the release before this one neither reads nor clears. The statement takes
// no owner row and writes nothing but document_block_versions rows, and it takes those `for no key
// update` in ascending artifact_id order before its update re-locks them, which is the order
// graphcensus's lockBlockDocuments takes several of them in: a writer holding several such rows
// and this statement can never each hold one the other waits for.
var markVersionedDocuments = `
	with page as (
		select id, type, issue_key, case type
			when 'artifact.version' then ` + store.CastableUUID(`payload->>'artifact_id'`) + `
			when 'artifact.created' then ` + store.CastableUUID(`payload->'artifact'->>'id'`) + `
		end as artifact_id, case when type in ('comment.created', 'comment.answered')
			then ` + store.CastableUUID(`payload->>'id'`) + `
		end as comment_id
		from events
		where id > $1 and id <= $2
		order by id
		limit $3
	),
	named as (
		select artifact_id from page where artifact_id is not null
		union
		select a.id from artifacts a
		where a.issue_key = any(array(select distinct issue_key from page where type = 'issue.created' and issue_key is not null))
		  and a.kind = 'doc'
	),
	held as (
		select b.artifact_id from document_block_versions b
		join artifacts a on a.id = b.artifact_id and a.kind = 'doc'
		where b.artifact_id = any(array(select artifact_id from named))
		  and b.store_owed_at is null
		  and not coalesce(b.store_version = b.version, false)
		order by b.artifact_id
		for no key update of b
	),
	marked as (
		update document_block_versions b set store_owed_at = now()
		from held
		where b.artifact_id = held.artifact_id and b.store_owed_at is null
		returning b.artifact_id
	),
	commented as (
		select comment_id, min(id) as cause from page where comment_id is not null group by comment_id
	)
	select (select count(*) from page), (select max(id) from page), (select count(*) from marked),
	       coalesce((select array_agg(comment_id::text order by comment_id) from commented), '{}'),
	       coalesce((select array_agg(cause order by comment_id) from commented), '{}')`

// readEventsAfterCursor is the first step of every block-index tick: it reads the events after its
// build's cursor, marks owed a block index, with this build's own mark, every document they name
// that this build's store has not stated at its version (markVersionedDocuments), gives every
// comment they name whose row names no node its node and its tuples (writeCommentsTheEventsName),
// and counts in tick what it read, marked and wrote, and whether another build serves.
//
// The release before this one indexes a version it writes, and a document it creates, the spec of
// an issue it creates included, with no node, writes a comment's row with no node, states nothing
// in the node and tuple store, and neither reads nor sets store_owed_at. A document whose mark this
// build's drain has cleared and which that release then edits, one it creates after the drain,
// and a comment it writes after the backfill therefore hold no mark, and nothing would bring this
// build to them to name their rows' nodes and state what they project. So every tick reads the
// events after its build's cursor, whether or not another build serves: which build's ticks come
// first in each period of the shared block-index lease (BlockIndexLease), and so whether this
// build ticks while that release serves at all, decides nothing, and no roll, rollback or roll
// forward, with two builds serving at once or none of this code serving for a while, leaves a
// write of that release unread. tick.AnotherBuild says whether a live server process of another
// build serves (processes.Live: a process whose Commit is not this build's own); the tick reports
// it, and the `dispatch: block index` line prints it, and it decides nothing.
//
// The cursor is this build's row of block_index_event_cursors (migration 20261118115645). The
// build's first tick that reads a proven horizon inserts the row, at the earlier of the newest
// event_id another build's row holds and the horizon the newest backfill recorded when it began
// (graph_backfill_horizon, graphcensus's recordBackfillHorizon) where both exist, at whichever one
// exists where only one does, and at that tick's horizon where neither does, so the cursor never
// starts at the beginning of the log. Another build's cursor is where it stopped reading, and every
// event above the backfill's horizon is one the backfill cannot have seen, so whatever the release
// before wrote since either, this build reads. A build that holds a row starts at its own row,
// however old, and reads every event since, one page a tick. The cursor moves with each tick, so
// each tick reads only the events since the last one and nothing scans a whole table. A tick reads
// (cursor, safe], safe being the event log's safe horizon (events.Horizon.Safe): event ids are not
// commit order, so an uncapped `id > cursor` read could pass an event still committing, and the
// cursor never moves past safe. A Horizon proves an observation only by a later one, so an event
// is read by the second tick after it commits, or later while a transaction on the instance stays
// open across those ticks.
//
// Everything a tick does here commits together, in one transaction, which takes its locks in the
// store's order: the cursor row `for update` first, which only block-index ticks take; then the
// marking statement's document_block_versions rows `for no key update`, in ascending artifact_id
// order; then the comments' node rows, in ascending (kind, id) in one store write, their tuples,
// and their comments rows. A writer under the revision fence takes a document's
// document_block_versions row before any node it writes (the fifth invariant on api's
// markInboxReadThreads), and a settlement takes its document's row and then the node of the
// session that follows an ask it opens (lockSettlementFence, causeFollows): a tick that wrote a
// comment's author's node first and then waited for that document's row would hold what the
// settlement waits for, while the settlement held what the tick waits for. The comment write takes
// no owner row (graphcensus's writeCommentBatch takes none), so nothing of it goes before the
// marking. A tick that fails moves nothing and the next reads the same events again; a document
// already marked keeps its mark and its time. A document marked here is listed by the owed
// documents this same tick reads next, and drained by it when its batch has room.
func (s *Service) readEventsAfterCursor(ctx context.Context, tick *BlockIndexTick) (full bool, err error) {
	safe, err := s.blockIndexHorizon().Safe(ctx)
	if err != nil {
		return false, fmt.Errorf("read the event log's safe horizon: %w", err)
	}
	live, err := processes.Live(ctx, s.store.Pool)
	if err != nil {
		return false, err
	}
	tick.AnotherBuild = slices.ContainsFunc(live, func(process processes.Process) bool {
		return process.Commit != s.buildCommit
	})
	tx, err := s.store.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	// A safe of 0 is a horizon that has proven nothing yet, as a process's first observation
	// never does on its own, or an empty log: until a tick reads one, nothing is read, and a
	// build's cursor never starts at the beginning of the log. least() passes over a null, so the
	// row starts at whichever of the two starts exists where only one does.
	if safe > 0 {
		if _, err := tx.Exec(ctx, `
			insert into block_index_event_cursors (build_commit, event_id)
			select $1, coalesce(least(
				(select max(event_id) from block_index_event_cursors),
				(select event_id from graph_backfill_horizon where id = 1)), $2)
			on conflict (build_commit) do nothing
		`, s.buildCommit, safe); err != nil {
			return false, fmt.Errorf("start this build's block-index event cursor: %w", err)
		}
	}
	var cursor int64
	switch err := tx.QueryRow(ctx, `
		select event_id from block_index_event_cursors where build_commit = $1 for update
	`, s.buildCommit).Scan(&cursor); {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read this build's block-index event cursor: %w", err)
	}
	// Another process of this build may have moved the cursor past this process's horizon; the
	// cursor never moves back.
	next := max(cursor, safe)
	// What the tick counts is what it committed: a read that fails counts nothing.
	var read, marked, written int
	if safe > cursor {
		var last *int64
		var comments []string
		var causes []int64
		if err := tx.QueryRow(ctx, markVersionedDocuments, cursor, safe, blockIndexEventPage).Scan(
			&read, &last, &marked, &comments, &causes,
		); err != nil {
			return false, fmt.Errorf("mark the documents the events after the cursor name: %w", err)
		}
		if full = read == blockIndexEventPage; full {
			next = *last
		}
		if written, err = writeCommentsTheEventsName(ctx, tx, comments, causes); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `
		update block_index_event_cursors set event_id = $2 where build_commit = $1
	`, s.buildCommit, next); err != nil {
		return false, fmt.Errorf("move this build's block-index event cursor: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit what the events after the cursor name: %w", err)
	}
	tick.Events, tick.Marked, tick.Comments = read, marked, written
	return full, nil
}

// writeCommentsTheEventsName gives each comment of ids whose row names no node the backfill's own
// write of that row, in tx, and answers how many it wrote: the comment's node, the facts its row
// projects and who wrote it, in one store write, and then the node named on its row
// (graphcensus.CommentsNamingNoNode, the one builder of those facts both writers share). causes
// holds, beside each id, the event that stored the comment, which is the cause of what the write
// states. A comment written by this build, or by a backfill since, names its node already and is
// left alone, so this is the backfill's contract applied to the rows the log names, and no
// reconcile: it writes only a row its marker does not mark. readEventsAfterCursor runs it after its
// marking, which is why that order is written there.
func writeCommentsTheEventsName(ctx context.Context, tx pgx.Tx, ids []string, causes []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	write, err := graphcensus.CommentsNamingNoNode(ctx, tx, ids, causes)
	if err != nil || write.Comments() == 0 {
		return 0, err
	}
	if err := graphstore.Write(ctx, tx, write.Nodes, write.Tuples); err != nil {
		return 0, fmt.Errorf("write the nodes of the comments the events after the cursor name: %w", err)
	}
	if err := write.NameNodes(ctx, tx); err != nil {
		return 0, err
	}
	return write.Comments(), nil
}

// blockIndexHorizon is the events.Horizon the block-index tick reads the event log's safe horizon
// through (readEventsAfterCursor), built by the first tick that asks for it and observing through
// the store's horizon pool (store.Pool.Horizon), so an observation never queues behind the
// requests holding the shared pool, as the API server's one Horizon does (api's server.horizon).
// It is the service's own rather than that one because the service is built before the API
// server, and the tick also runs where no API server is: the service's own tests and the
// harnesses that drain the block index. It observes once a tick, and a tick runs under the
// block-index lease, so the fleet spends one transaction id on it a tick. A horizon pool that
// cannot open is logged once, and the Horizon then observes through the shared pool.
func (s *Service) blockIndexHorizon() *events.Horizon {
	s.horizonOnce.Do(func() {
		var db events.HorizonDB = s.store.Pool
		switch pool, err := s.store.Pool.Horizon(); {
		case err == nil:
			db = pool
		case !errors.Is(err, store.ErrPoolClosed):
			slog.Error("dispatch: could not open the event horizon's pool; the block-index tick observes the event log through the shared request pool until the process restarts",
				"error", err)
		}
		s.eventHorizon = events.NewHorizon(db)
	})
	return s.eventHorizon
}
