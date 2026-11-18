package docs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/processes"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// blockIndexEventPage is the most events one tick's marking reads (markVersionsAnotherBuildWrote).
// A tick reads the events after its build's cursor, at most this many, and moves the cursor to the
// last one it read when the page is full, so it reads at most this many event rows however far the
// log has run ahead of the cursor; a full page makes the tick full (BlockIndexTick.Full), and the
// next tick, blockIndexStep later, reads on from there. It is the suggestion outcome sweep's page
// (api's suggestionOutcomePage).
const blockIndexEventPage = 1000

// markVersionedDocuments is one tick's marking, in one statement: it reads up to $3 events after
// the cursor ($1) and at or below the safe horizon ($2), in id order, and marks owed a block index,
// with this build's own mark, every document an artifact.version or artifact.created event among
// them names, and every document of each issue an issue.created event among them names, that
// holds no such mark yet. It answers how many events it read, the id of the last one (null when it
// read none) and how many documents it marked.
//
// A version event names its document in payload->>'artifact_id' (ArtifactVersionEventPayload),
// and the event that creates a document, which records its version 1, carries the whole artifact
// in its payload and names the document in payload->'artifact'->>'id' (api's
// artifactCreatedEventPayload, read the same way by versionEventID). An issue's create writes its
// primary document, the spec, and records that document's version 1 with the issue.created event
// alone (api's issue create, read the same way by versionEventID), which names the issue and no
// document: the event's issue_key, its owner, is the issue, so every document of kind 'doc' that
// issue holds in artifacts is marked, read through artifacts_issue_key. A document the release
// before this one creates writes its rows with no node as its edits do, so it is owed this build's
// pass as they are. Each event names its document whichever build wrote it, and the read filters
// on nothing else: a project document's event carries no issue key, and an agent's workspace
// document writes no event at all. The payload is read only for the two artifact types, and a
// value that is no uuid names nothing rather than failing the tick (store.CastableUUID). Only a
// document that exists is marked, and only one of kind 'doc', since the row is joined to its
// artifact. The page bounds the issues as it bounds the documents, and an issue holds a few
// documents.
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
		end as artifact_id
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
		order by b.artifact_id
		for no key update of b
	),
	marked as (
		update document_block_versions b set store_owed_at = now()
		from held
		where b.artifact_id = held.artifact_id and b.store_owed_at is null
		returning b.artifact_id
	)
	select (select count(*) from page), (select max(id) from page), (select count(*) from marked)`

// markVersionsAnotherBuildWrote is the first step of a block-index tick: it marks owed a block
// index, with this build's own mark, every document whose version another build may have written
// since the tick before it, and counts in tick what it read, what it marked and whether another
// build serves.
//
// The release before this one indexes a version it writes, and a document it creates, the spec of
// an issue it creates included, with no node and states nothing in the node and tuple store, and
// it neither reads nor sets store_owed_at. A document whose mark this build's drain has cleared
// and which that release then edits, or one it creates after the drain, therefore holds no mark,
// and nothing would bring this build's pass to it to name its rows' nodes and state what they
// project. So while a live server process of another build serves (processes.Live: a process
// whose Commit is not this build's own), each tick marks owed every document that an
// artifact.version or artifact.created event after this build's cursor names, and every document
// of each issue an issue.created event after it names (markVersionedDocuments). Once the fleet
// serves this build alone, the first tick that finds it so reads once more, which marks what the
// tick before it left, and once the cursor has passed every event the other build can have
// written, the tick after reads no event at all: it moves the cursor to the safe horizon and
// stops. A fleet with no live process, as a test harness registers none, serves this build alone.
//
// The cursor is this build's row of block_index_event_cursors (migration 20261118115645). The
// build's first tick that reads a proven horizon inserts the row, at the newest cursor another
// build left (below) or, where none has, at that horizon, so it starts at this build's first boot
// or where the build before it stopped reading and never at the start of the log. It moves with
// each tick, so each tick reads only the events since the last one and nothing scans a whole
// table. A tick reads (cursor, safe], safe being the event log's safe horizon
// (events.Horizon.Safe): event ids are not commit order, so an uncapped `id > cursor` read could
// pass an event still committing, and the cursor never moves past safe.
// A Horizon proves an observation only by a later one, so an event is read by the second tick
// after it commits, or later while a transaction on the instance stays open across those ticks.
//
// The two ends of a roll are each read in their own order. The horizon is read before the
// registry: a process registers before it serves a request (processes.Register), so a process of
// another build that this read of the registry does not show writes the events of the requests
// it serves after the observation safe comes from, above safe, and the next tick, which sees it,
// reads them. And a process deregisters once its drain has ended, after every event it wrote has
// committed, so the newest event the log holds after this read of the registry is at or above
// each of them. A tick that finds another build serving, and the first one that finds the fleet
// alone after it, records that newest id as read_through, and the ticks read on until the cursor
// reaches it: the safe horizon lags the log, so one more read up to it can still stop short of
// the last versions a departing process wrote.
//
// A deploy that stops every process of one build before it starts the other's - a rollback, and
// the roll forward after it - never has two builds serving at once: this build's last tick before
// it stopped found it serving alone, and so does the first tick after it, or after the next
// build, starts, while the previous release wrote versions in between. So the first tick of each
// process that reads a proven horizon (Service.eventCursorJoined) also reads once, from the
// newest cursor any build left through the newest event. Its start is the highest event_id of
// every row of block_index_event_cursors: this build's own where it ran last, and where a build
// whose row this one does not hold yet ran last, the cursor that build stopped reading at, rather
// than the horizon its versions now lie below. It records the newest event id as read_through,
// and the ticks read on from that start until the cursor reaches it. A process that boots while
// others of this build run reads only the few events since their last tick; one that boots after
// an absence reads every event since, one page a tick. Where no build has left a row, the first
// of this code to run, the cursor starts at the horizon.
//
// The marks and the cursor's move commit together, in one transaction that holds the cursor row
// `for update` throughout, so a tick that fails moves nothing and the next reads the same events
// again; a document already marked keeps its mark and its time. A document marked here is listed
// by the owed documents this same tick reads next, and drained by it when its batch has room.
//
// It answers, as owed, the time since which this build owes the object pass
// (writeObjectNodesAnotherBuildWrote), nil where it owes none. A tick on which a build before this
// one may have written rows since the tick before it - another build serves, the tick before found
// one, this process joins its build's cursor, or an event this tick moves the cursor past names a
// row with no node or a removed document's standing node (graphcensus's EventsOweObjectPass), as a
// build before this one leaves behind when it served with no tick of this build in between - and
// a tick of a process that has finished no pass for objectPassInterval set that time in the
// cursor's row, in the transaction that moves the cursor, and a debt an earlier tick left stands
// until a pass clears it, so whichever process of the build holds the lease next runs it. It
// answers owed only once its transaction has committed, nil where it fails, and nil before a
// horizon is proven, when no cursor row holds a debt: such a tick owes the pass only while another
// build serves (rollNow).
func (s *Service) markVersionsAnotherBuildWrote(ctx context.Context, tick *BlockIndexTick) (full bool, owed *time.Time, err error) {
	safe, err := s.blockIndexHorizon().Safe(ctx)
	if err != nil {
		return false, nil, fmt.Errorf("read the event log's safe horizon: %w", err)
	}
	live, err := processes.Live(ctx, s.store.Pool)
	if err != nil {
		return false, nil, err
	}
	tick.AnotherBuild = slices.ContainsFunc(live, func(process processes.Process) bool {
		return process.Commit != s.buildCommit
	})
	tx, err := s.store.Pool.Begin(ctx)
	if err != nil {
		return false, nil, err
	}
	defer tx.Rollback(ctx)
	// A safe of 0 is a horizon that has proven nothing yet, as a process's first observation
	// never does on its own, or an empty log: until a tick reads one, nothing is read, and the
	// cursor never starts at the beginning of the log. A process's first tick with a proven
	// horizon starts this build's cursor, or moves it forward, to the newest cursor any build left,
	// and at the horizon where none has.
	joining := safe > 0 && !s.eventCursorJoined.Load()
	if safe > 0 {
		if _, err := tx.Exec(ctx, `
			insert into block_index_event_cursors (build_commit, event_id)
			select $1, coalesce(max(event_id), $2) from block_index_event_cursors
			on conflict (build_commit) do nothing
		`, s.buildCommit, safe); err != nil {
			return false, nil, fmt.Errorf("start this build's block-index event cursor: %w", err)
		}
	}
	var cursor, readThrough int64
	var seen bool
	switch err := tx.QueryRow(ctx, `
		select event_id, another_build_seen, read_through from block_index_event_cursors
		where build_commit = $1 for update
	`, s.buildCommit).Scan(&cursor, &seen, &readThrough); {
	case errors.Is(err, pgx.ErrNoRows):
		// No horizon is proven yet, so the cursor is not started: nothing is read, and no row
		// holds a debt. The tick owes the object pass only while another build serves.
		if tick.AnotherBuild {
			return false, &rollNow, nil
		}
		return false, nil, nil
	case err != nil:
		return false, nil, fmt.Errorf("read this build's block-index event cursor: %w", err)
	}
	if joining {
		var left int64
		if err := tx.QueryRow(ctx, `select max(event_id) from block_index_event_cursors`).Scan(&left); err != nil {
			return false, nil, fmt.Errorf("read the newest cursor a build left: %w", err)
		}
		cursor = max(cursor, left)
	}
	if tick.AnotherBuild || seen || joining {
		var newest int64
		if err := tx.QueryRow(ctx, `select coalesce(max(id), 0) from events`).Scan(&newest); err != nil {
			return false, nil, fmt.Errorf("read the newest event: %w", err)
		}
		readThrough = max(readThrough, newest)
	}
	// Another process of this build may have moved the cursor past this process's horizon; the
	// cursor never moves back.
	next := max(cursor, safe)
	if tick.AnotherBuild || cursor < readThrough {
		var last *int64
		if err := tx.QueryRow(ctx, markVersionedDocuments, cursor, safe, blockIndexEventPage).Scan(
			&tick.Events, &last, &tick.Marked,
		); err != nil {
			return false, nil, fmt.Errorf("mark the documents the version events after the cursor name: %w", err)
		}
		if full = tick.Events == blockIndexEventPage; full {
			next = *last
		}
	}
	// A build before this one can serve the whole time since the tick before this one with no tick
	// of this build in between, since a process whose ticks come first in each period takes every
	// period of the lease, so none of the triggers above holds; the rows it created and the
	// documents it removed are named by the events this tick moves the cursor past
	// (graphcensus's EventsOweObjectPass). A conversation's file it uploaded is named by none, so
	// the pass is owed too once this process has finished none for objectPassInterval.
	named := false
	if next > cursor {
		if named, err = graphcensus.EventsOweObjectPass(ctx, tx, cursor, next); err != nil {
			return false, nil, err
		}
	}
	// A process's first tick joins its build's cursor and owes the pass already, so the interval
	// runs from the first pass it finishes.
	finished := s.objectPassFinished.Load()
	periodic := finished != 0 && s.now().Sub(time.Unix(0, finished)) >= objectPassInterval
	if err := tx.QueryRow(ctx, `
		update block_index_event_cursors set event_id = $2, another_build_seen = $3, read_through = $4,
			object_nodes_owed_at = case when $5 then clock_timestamp() else object_nodes_owed_at end
		where build_commit = $1
		returning object_nodes_owed_at
	`, s.buildCommit, next, tick.AnotherBuild, readThrough, tick.AnotherBuild || seen || joining || named || periodic).Scan(&owed); err != nil {
		return false, nil, fmt.Errorf("move this build's block-index event cursor: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, nil, fmt.Errorf("commit the documents the version events after the cursor name: %w", err)
	}
	if joining {
		s.eventCursorJoined.Store(true)
	}
	return full, owed, nil
}

// rollNow is the debt of a tick that owes the object pass before any cursor row can hold one: a
// tick with no proven horizon while another build serves. It clears nothing when its pass is done.
var rollNow time.Time

// objectPassBatches bounds the batches one tick's object pass writes across the four kinds, so a
// first deploy's whole backlog never holds the block-index lease in one tick: a pass that stops
// with rows left leaves its debt and makes the tick full, and the next one runs blockIndexStep
// later. At the backfill's batch size of 200 it writes the nodes of up to 800 rows a tick.
const objectPassBatches = 4

// orphanedNodesSwept bounds the orphaned document nodes one tick states gone, each in a
// transaction of its own (graphcensus's StateOrphanedGone); a tick that reads more leaves its debt
// and is full.
const orphanedNodesSwept = 20

// objectPassInterval is the longest a process of this build goes without finishing the object
// pass: a tick owes it once this long has passed since the process's last pass finished
// (Service.objectPassFinished). A conversation's file that a build before this one uploads while
// it holds every period of the lease appends no event, so no other trigger reaches it, and it
// waits for its node this long and one tick at most. A pass with nothing to write reads the four
// tables whole, 97 to 190 ms at production's row counts, so each process spends that once in
// this interval.
const objectPassInterval = 10 * time.Minute

// writeObjectNodesAnotherBuildWrote is the block-index tick's second step: while this build owes
// the object pass (markVersionsAnotherBuildWrote's owed), it writes the node of every issue,
// document, ask and message whose row names none, through the backfill's object pass
// (graphcensus's WriteOwedObjectNodes), and states gone the node of every document a build before
// this one removed (graphcensus's OrphanedDocumentNodes and StateOrphanedGone). It counts both in
// tick, and answers whether it stopped with work left.
//
// The release before this one writes those four kinds' rows with no node, as it writes a
// version's block rows with none, and nothing else would give them one until someone ran the
// backfill again: an issue it creates, its spec, a document it uploads, a conversation's file,
// whose upload appends no event, an ask and a message. And its removal of a document states
// nothing, so the node this build wrote for it stands live behind no row. So the pass reads rows,
// not events, on the triggers the marking reads the log on: while a process of another build
// serves, on the first tick that finds this build serving alone after one did, when that build's
// last rows have committed (a process deregisters once its drain has ended), and on each process's
// first tick, after a deploy that stopped every process of one build before it started the
// other's. A build before this one that takes every period of the lease while it serves leaves
// none of those triggers, so the pass runs too on a tick whose cursor moves past an event that
// names a row with no node or a removed document's standing node (graphcensus's
// EventsOweObjectPass), and, since a conversation's file appends no event, once the process has
// finished no pass for objectPassInterval.
// A document node with no row behind it can mean nothing else: every writer of this build
// writes a node in the transaction that writes its row, and the pass writes one only for a row
// that stands under the owner rows it takes. The sweep states such a node gone as this build's own
// removal of the document states it (api's removeArtifact), under the removal's own event, so the
// store keeps every statement it held and the history of a removed document is one whichever build
// served the removal; a node whose row a create of this build commits meanwhile is left, since the
// sweep reads under its lock of the node whether a row stands.
//
// It runs after the marking has committed and outside its transaction, each batch in a
// transaction of its own that takes its rows' owner rows first (graphcensus's writeObjectBatch),
// and each gone statement in its own, owner row first too, so it holds no lock of the marking's
// while it takes them. It writes at most objectPassBatches batches and states at most
// orphanedNodesSwept nodes gone a tick, and a kind that fails holds back neither the other kinds
// nor the sweep. A pass that fails, or stops with work left, leaves the debt, which only a pass
// that finishes clears, so a failure on the tick that finds the fleet alone, after which the
// marking reads on no more, leaves no row without its node and no node without its row. A node the
// sweep leaves is logged with the reason.
func (s *Service) writeObjectNodesAnotherBuildWrote(ctx context.Context, tick *BlockIndexTick, owed *time.Time) (more bool, err error) {
	if owed == nil {
		return false, nil
	}
	pass, passErr := graphcensus.WriteOwedObjectNodes(ctx, s.store, objectPassBatches)
	tick.ObjectNodes = int(pass.Rows)
	more = pass.More
	if pass.Moved != nil {
		// No second pass cures a row whose node the store holds in another project, so it holds
		// back no debt: it is logged for a person, and the census names the row.
		slog.Error("dispatch: block index left rows whose node the store holds in another project", "error", pass.Moved)
	}
	failed := []error{passErr}
	orphaned, err := graphcensus.OrphanedDocumentNodes(ctx, s.store.Pool, orphanedNodesSwept+1)
	failed = append(failed, err)
	more = more || len(orphaned) > orphanedNodesSwept
	for _, ref := range orphaned[:min(len(orphaned), orphanedNodesSwept)] {
		switch err := graphcensus.StateOrphanedGone(ctx, s.store, ref); {
		case errors.Is(err, graphcensus.ErrNotOrphaned):
			slog.Info("dispatch: block index left a document node it read as orphaned", "node", ref, "reason", err)
		case err != nil:
			failed = append(failed, fmt.Errorf("state the orphaned node of %s gone: %w", ref, err))
		default:
			tick.OrphansGone++
		}
	}
	switch err := errors.Join(failed...); {
	case err != nil:
		return more, fmt.Errorf("write the nodes of the rows another build wrote: %w", err)
	case more:
		return more, nil
	}
	s.objectPassFinished.Store(s.now().UnixNano())
	if owed == &rollNow {
		return false, nil
	}
	if _, err := s.store.Pool.Exec(ctx, `
		update block_index_event_cursors set object_nodes_owed_at = null
		where build_commit = $1 and object_nodes_owed_at = $2
	`, s.buildCommit, *owed); err != nil {
		return false, fmt.Errorf("clear this build's debt of the object pass: %w", err)
	}
	return false, nil
}

// blockIndexHorizon is the events.Horizon the block-index tick reads the event log's safe horizon
// through (markVersionsAnotherBuildWrote), built by the first tick that asks for it and observing
// through the store's horizon pool (store.Pool.Horizon), so an observation never queues behind the
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
