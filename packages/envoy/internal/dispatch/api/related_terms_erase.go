package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// searchTermErasedItems is true of a queue row q about an item an erase took words out of,
// whichever transaction queued the row: an item a row of the erase's own transaction names - each
// delete and rewrite of an item, which 20261101182522's triggers queue, and each document_search
// row the erase deletes, which it queues itself (grapherase's restateSearch) - and a document of
// documents, a SQL expression of the documents the erase deleted versions of, whose older rows can
// carry the vector of such a version. An item is matched by searchTermQueueItemKeySQL, as a pass
// groups its rows.
func searchTermErasedItems(documents string) string {
	return `(q.kind, ` + searchTermQueueItemKeySQL + `) in (
    select own.kind, ` + searchTermQueueItemKeySQL + ` from search_term_queue own where own.xid = pg_current_xact_id()
    union all
    select 'document', document::text from unnest(` + documents + `::uuid[]) document)`
}

// searchTermErasedProjects answers, in key order, every project a queue row of an erased item
// names (searchTermErasedItems). A row whose owner is gone names none, and no project's counts
// can be told to hold its item (searchTermQueueProjectSQL).
//
// Parameters: $1 the documents the erase deleted versions of.
var searchTermErasedProjects = `select distinct project from (
  select ` + searchTermQueueProjectSQL + ` as project from search_term_queue q
   where ` + searchTermErasedItems("$1") + `) erased
 where project is not null order by project`

// searchTermTotalsForget makes the project's totals row exist and clears its snapshot, which has
// the next pass of the project count it in full (searchTermTotalsMode), and takes the row's lock.
// A pass of the project already running holds that lock for its whole transaction, so the erase
// waits for it here, and its statements after this one read what that pass committed; a pass that
// begins later waits for the erase's commit. The insert waits as well for a first pass whose own
// claim of the row has not committed yet. The erase runs it before its owner locks for the
// projects it takes then (TakeErasedTermTotalsTx), where that wait holds no writer, and under them,
// bounded, for every project whose items it changed (ForgetErasedTermsTx), where a row it took
// before them is its own already.
const searchTermTotalsForget = `insert into search_term_totals (project_key, items, refreshed_at) values ($1, 0, now())
on conflict (project_key) do update set snapshot = null`

// erasedTermTotalsWait bounds how long ForgetErasedTermsTx waits, under the erase's owner locks,
// for the totals row of a project the erase did not take before them (TakeErasedTermTotalsTx). A
// refresh pass holds that row for its whole transaction, which production's logs of 14 days
// (2,015 passes) show reaching 18.5 s for one project, and every writer of the erased node's issue
// or document waits on the erase for as long as the erase waits there. 100 ms takes a row no pass
// holds, and refuses the erase rather than hold those writers for a pass.
//
// The bound is also what ends a cycle between two erases whose projects overlap: each takes the
// totals rows of its own owners' projects before its owner locks, so one can hold the row the
// other waits for here while it waits for a row or an owner lock the other holds. The run whose
// wait runs out rolls back having removed nothing. A take of a totals row under an erase's owner
// locks therefore waits this bound at most, and never without one.
const erasedTermTotalsWait = 100 * time.Millisecond

// searchTermStatsForget deletes from the project's statistics every lexeme an erase took out of an
// item: each lexeme of an old vector a queue row of an erased item carries
// (searchTermErasedItems) that the item, as the erase's transaction reads it
// (searchTermQueuedItemSQL), no longer holds. Of an item's rows the oldest carries the vector the
// stored counts hold for it, and the erase's own the vector the item held when the erase began, so
// the lexemes it deletes hold every lexeme the erased text put into the counts. It reads the queue,
// each erased item by its key and the rows it deletes by theirs, and no other item of the project.
//
// Parameters: $1 the project key, $2 the documents the erase deleted versions of.
var searchTermStatsForget = `
with queued as (
  select q.kind, ` + searchTermQueueItemKeySQL + ` as item, q.issue_key, q.artifact_id, q.item_id, q.old_search
    from search_term_queue q
   where ` + searchTermErasedItems("$2") + `
     and ` + searchTermQueueProjectSQL + ` = $1
),
changed as (
  select distinct on (kind, item) kind, item, issue_key, artifact_id, item_id from queued
),
held as (
  select it.kind, it.item, u.lexeme
    from changed it
    cross join lateral (` + searchTermQueuedItemSQL + `
    ) v
    cross join lateral unnest(v.search) u
),
erased as (
  select distinct u.lexeme
    from queued it
    cross join lateral unnest(it.old_search) u
   where not exists (select 1 from held h where h.kind = it.kind and h.item = it.item and h.lexeme = u.lexeme)
)
delete from search_term_stats s where s.project_key = $1 and s.lexeme in (select lexeme from erased)`

// searchTermQueueErasedDrop deletes every queue row of an erased item (searchTermErasedItems),
// the erase's own and those queued before it.
//
// Parameters: $1 the documents the erase deleted versions of.
var searchTermQueueErasedDrop = `delete from search_term_queue q where ` + searchTermErasedItems("$1")

// ForgetErasedTermsTx takes out of the term statistics, inside tx, the lexemes the erase
// (`envoy-dispatch erase`, grapherase.Erase) took out of the items it deleted and rewrote, and
// answers the projects it took them from, in key order, with how many queue rows it deleted.
// documents are the documents the erase deleted versions of.
//
// It runs under the erase's owner locks, for a need of the erase's commit: search_term_stats
// holds lexemes, and a lexeme of the erased text that stood in it once the erase committed would
// be the erased words, still stored. So once tx commits no lexeme the erase removed stands in the
// statistics of any project, and no queue row carrying one stands either. Counting each project
// again from its items would hold that too, but it reads every item of the project
// (searchTermStatsRebuild) while the erase still holds its owner locks (grapherase's
// lockInStoreOrder), and all it adds is the other items' counts of the lexemes taken out, which
// the caller counts once tx has committed, outside the locks (RefreshSearchTermStats). Each
// statement here is bounded by the erase's own rows instead:
//
//   - the projects are those the queue rows of the erased items name (searchTermErasedProjects);
//   - each project's totals row is locked and its snapshot cleared, in key order
//     (searchTermTotalsForget), so the project's next pass counts it in full, waiting
//     erasedTermTotalsWait at most for the row (takeErasedTermTotalsBounded): the erase took and
//     cleared the rows of its owners' projects before its owner locks (TakeErasedTermTotalsTx),
//     so tx holds each of those already and takes it without a wait;
//   - from each project's statistics go the lexemes the erased items no longer hold
//     (searchTermStatsForget);
//   - and every queue row of the erased items goes (searchTermQueueErasedDrop): any of them can
//     carry a vector the erased text was part of, and the full count the cleared snapshot asks for
//     needs none of them.
//
// Each reads the queue, which holds what changed since the last pass, the erased items and the
// rows it deletes by their keys, and one totals row; none reads an item the erase did not change.
//
// What the deletes cost, until the count after the commit, is the counts of the lexemes they
// took out that other items of the project still hold: the related-item check reads such a
// lexeme as one the statistics do not hold and probes it live (relatedProbeLimit). A count after
// the commit that fails leaves them so until the project's next refresh pass, which the cleared
// snapshot makes a full one. No lexeme the erase removed comes back either way: every count after
// the commit reads the items as the erase left them.
//
// No project here is expected whose row the erase did not take before its owner locks: every item
// the erase changes stands under an owner it holds, and it took the projects of those owners and
// of their documents (grapherase's ownerProjects). One would be an item whose project is not its
// owner's, such as a queue row of an erased item written under another owner. A refresh pass of
// that project running, or another erase that took its row, would hold that row, and so the erase
// and every writer of the node's issue or document, for as long as it runs; past
// erasedTermTotalsWait, 100 ms, the erase is refused instead, with an error that names the
// project, and its transaction rolls back having removed nothing.
func ForgetErasedTermsTx(ctx context.Context, tx pgx.Tx, documents []string) ([]string, int64, error) {
	rows, err := tx.Query(ctx, searchTermErasedProjects, documents)
	if err != nil {
		return nil, 0, err
	}
	projects, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, 0, err
	}
	for _, project := range projects {
		if err := takeErasedTermTotalsBounded(ctx, tx, project); err != nil {
			return nil, 0, err
		}
		if _, err := tx.Exec(ctx, searchTermStatsForget, project, documents); err != nil {
			return nil, 0, err
		}
	}
	tag, err := tx.Exec(ctx, searchTermQueueErasedDrop, documents)
	if err != nil {
		return nil, 0, err
	}
	return projects, tag.RowsAffected(), nil
}

// TakeErasedTermTotalsTx takes, inside tx, the totals row of each of projects in the order given,
// which is key order, and clears its snapshot (searchTermTotalsForget), as ForgetErasedTermsTx
// does for each project it names. The erase (grapherase's lockInStoreOrder) calls it with the
// projects of the owners it writes under before it takes any owner lock, so ForgetErasedTermsTx,
// which takes the row of each project it names again under those locks, finds each of these held
// by its own transaction and waits on none of them. A refresh pass of one of those projects holds
// the row for its whole transaction, so the erase waits for it here, holding no owner lock and so
// no writer of the node's issue or document, rather than under its owner locks, where every one
// of those writers would wait as long.
//
// Taking a totals row before an owner lock closes no cycle through any writer but another erase:
// no other writer that holds an owner lock takes a totals row, and the row's other takers are
// refreshSearchTermStats's passes - the leased pass, the related-item check's
// ensureSearchTermStats and the erase's own count after its commit - which take no owner lock and
// wait on nothing an owner's writer holds. An erase does take one under its owner locks: the row
// of a project its items name that it did not take here (ForgetErasedTermsTx), so two erases whose
// projects overlap can wait on each other, and that take's bound (erasedTermTotalsWait) is what
// ends such a cycle, with one run rolled back having removed nothing. A project taken here whose
// items the erase then changes none of keeps its cleared snapshot, which costs it a full count at
// its next pass.
func TakeErasedTermTotalsTx(ctx context.Context, tx pgx.Tx, projects []string) error {
	for _, project := range projects {
		if _, err := tx.Exec(ctx, searchTermTotalsForget, project); err != nil {
			return fmt.Errorf("take the term statistics totals of project %s: %w", project, err)
		}
	}
	return nil
}

// takeErasedTermTotalsBounded runs searchTermTotalsForget for a project ForgetErasedTermsTx
// names, waiting erasedTermTotalsWait at most for the row, as docs' lockSettlementCursor bounds
// its wait: lock_timeout is set to the bound with set_config's local flag for that one statement,
// and set back to what it was once the row is taken. A row the erase took before its owner locks
// (TakeErasedTermTotalsTx) is one tx holds already, which the statement takes without a wait. A row
// another transaction still holds when the bound runs out fails the statement
// (lock_not_available, 55P03), which aborts tx; the erase then rolls back, and the error says so.
func takeErasedTermTotalsBounded(ctx context.Context, tx pgx.Tx, project string) error {
	var previous string
	wait := &pgx.Batch{}
	wait.Queue(`select current_setting('lock_timeout')`).QueryRow(func(row pgx.Row) error {
		return row.Scan(&previous)
	})
	wait.Queue(`select set_config('lock_timeout', $1, true)`, fmt.Sprintf("%dms", erasedTermTotalsWait.Milliseconds()))
	wait.Queue(searchTermTotalsForget, project)
	err := tx.SendBatch(ctx, wait).Close()
	if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == "55P03" {
		return fmt.Errorf("the erase changed items of project %s, whose term statistics a running refresh pass or another erase held past %s; "+
			"nothing was removed, and the erase can be run again: %w", project, erasedTermTotalsWait, err)
	}
	if err != nil {
		return fmt.Errorf("take the term statistics totals of project %s: %w", project, err)
	}
	if _, err := tx.Exec(ctx, `select set_config('lock_timeout', $1, true)`, previous); err != nil {
		return fmt.Errorf("restore lock_timeout after the term statistics totals of project %s: %w", project, err)
	}
	return nil
}
