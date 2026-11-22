package grapherase

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/api"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/textversions"
)

// eraseOwnerAttempts is how many tries lockInStoreOrder makes at reading which owner rows the
// erase writes under and taking them and the node's row before it refuses. A try fails only when
// a write lands between its read of those rows and its locks and moves the node under another
// owner; three in a row is a stream of such writes, which the operator stops before running the
// erase again, rather than one the erase can outrun.
const eraseOwnerAttempts = 3

// lockInStoreOrder opens a confirmed erase's transaction and takes its locks in the store's lock
// order (api/inbox_read.go's markInboxReadThreads), whose first invariant is that a store write
// takes the owner's lock first. Of the rows that order places, the lock of every owner the erase
// writes under comes first (ownerRows says which they are), then the node's row `for update`, then
// the node's own projection rows (lockProjectionRows), and nothing the erase does after them takes
// an owner lock it does not already hold: docs.ReadErasedLiveText takes a document's owner lock
// again and finds this transaction holding it. A node no row projects - a person, a session, a
// role, a page - stands under no owner, and the erase then starts at its node's row, which the
// order allows: a writer may leave a step out.
//
// Those owners are the node's own and its own document's, never another node's: a row that
// holds the erased bytes and derives from another node is listed and left as it is, so the run
// writes nothing under that node's owner and takes no lock on it.
//
// The owner locks are the advisory locks every writer of an issue or a document takes
// (store.LockOwners), each taken once, in the order of the owner keys (events.LockOwnerRows), as
// events.Broker.LockOwners takes the owners of several events: "artifact:<id>" for a document no
// issue owns, before "issue:<key>". That is the one order over every owner, and the backfill's
// batches take their documents' owners in it too (graphcensus's lockBlockDocuments and
// lockObjectOwners).
//
// Before the owner locks it takes two rows that a background pass holds for the whole of a
// transaction of its own, so that the run waits for such a pass holding no owner lock, which no
// writer of the node's issue or document then waits on, rather than under its owners, where every
// comment create, reply, version write and ask transition of them would wait as long:
//
//   - the term statistics totals row of each project of its owners and of the documents it read
//     them through (ownerProjects), in project key order, its snapshot cleared
//     (api.TakeErasedTermTotalsTx). A refresh pass of a project holds that row for its whole
//     transaction, which production's logs show reaching 18.5 s. Under the locks
//     api.ForgetErasedTermsTx waits on none of those rows, and on the row of any other project for
//     100 ms at most;
//   - then the keyword fill's cursor row `for share` (textversions.HoldFillBack). A batch of the
//     fill in flight, which may have read the node's text before the run took its locks and which
//     runs 0.6 to 1.3 s when full, commits first, so the read under the locks lists every keyword
//     row a batch wrote, and no batch writes until the run ends. The totals rows come first so
//     that the run does not hold the fill back while it waits for a pass.
//
// The store's lock order allows both before the owners because no writer that holds an owner lock
// takes either, so no wait for one can close a cycle through an owner. The other takers of a
// totals row are refresh passes (api's refreshSearchTermStats: the leased pass, the related-item
// check's ensureSearchTermStats and the erase's own count after its commit), and the other takers
// of the cursor row are the fill's batches, each of which takes it first and alone; none of them
// takes an owner lock or waits on anything an owner's writer holds. Two runs take the totals rows
// in one order, and the cursor row in share mode, which the two hold together.
//
// Which owners the run writes under is read before they are locked, so it is read again under
// them with the rest of the listing (readDerived), and the run goes on with that second read. A
// write that, between the two reads, moved the node under an owner the run did not take could go
// on writing under that owner while the run does. The run then rolls back and starts again rather
// than take that owner after the node's row, and after eraseOwnerAttempts it refuses, naming each
// owner it found untaken, having removed nothing.
//
// Before it takes any lock, each try searches the database for the rows the run leaves
// (searchLeft). That search sends one statement per text column of every base table, each a scan
// of the whole table, and every writer of an issue or a document takes its owner's lock first and
// waits there, holding a pool connection, while another transaction holds it: under the run's
// locks, the search would hold every comment create, reply, version write and ask transition of
// the node's issue or document for as long as it scans. The run changes none of the rows the
// search lists, so listing them needs no lock. A row written between the search and the locks
// that holds the erased bytes is therefore not on the listing the digest covers, and the run
// leaves it as it leaves every row the search lists; once the run has committed it searches again,
// outside its locks, and its report lists each row that search finds and this one did not
// (searchLeftAfterCommit). The same search answers what only a read of a whole large table finds,
// so that no such read runs while the run holds its owners' writers: the keys of the display trees
// that hold a text node of the node's where an event of it carried a body it cut
// (treeKeysHolding), which the run deletes under its locks by key, and the ids of the events about
// the node that hold its text, which the run reads again under its locks by id beside the events
// of the owners it holds (readEvents, eventsUnderLocks). Once a try holds its locks, the run holds
// the writers of those owners for the rest of its work: the read of the rest of the listing under
// them, the erase and the commit.
func lockInStoreOrder(ctx context.Context, conn *pgx.Conn, kind, id string) (pgx.Tx, *Erasure, erasedRead, error) {
	var untaken []string
	for range eraseOwnerAttempts {
		found, err := searchLeft(ctx, conn, kind, id)
		if err != nil {
			return nil, nil, erasedRead{}, err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return nil, nil, erasedRead{}, fmt.Errorf("begin: %w", err)
		}
		erasure := &Erasure{Kind: kind, ID: id, RanAt: time.Now().UTC()}
		var read erasedRead
		read, untaken, err = erasure.lockOwnersThenNode(ctx, tx, found)
		if err == nil && len(untaken) == 0 {
			read.heldTrees = found.trees
			return tx, erasure, read, nil
		}
		_ = tx.Rollback(context.Background())
		if err != nil {
			return nil, nil, erasedRead{}, err
		}
	}
	return nil, nil, erasedRead{}, fmt.Errorf("the erase refuses %s:%s: on each of %d tries a write put the node under an owner row the erase had not taken before it took the node's row (%s); nothing was removed, and the erase can be run again",
		kind, id, eraseOwnerAttempts, strings.Join(untaken, "; "))
}

// searchedBeforeLocks is what a try's search before its locks found (searchLeft): the lines of
// the listing's section of rows left, the keys of the display trees that hold a text node of the
// node's (treeKeysHolding), and the ids of the events about the node that hold its text.
type searchedBeforeLocks struct {
	left   []string
	trees  [][]byte
	events []int64
}

// searchLeft is a confirmed run's search for the rows it leaves (readLeft, with the margin records
// readKeptMargins lists), which a try of lockInStoreOrder runs before it takes any lock. It runs in
// a read-only transaction of its own, as a run without --confirm reads its whole listing, over the
// erased texts and the rows that derive from the node as that transaction reads them
// (readDerived), and that transaction ends before the try's own begins, so the try holds none of
// the table locks the search took. It answers the lines of the listing's section of rows left,
// the keys of the display trees that hold a text node of the node's (treeKeysHolding), and the ids
// of the events about the node that its read of every event found holding the node's text.
func searchLeft(ctx context.Context, conn *pgx.Conn, kind, id string) (searchedBeforeLocks, error) {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return searchedBeforeLocks{}, fmt.Errorf("begin the search for the rows the erase leaves: %w", err)
	}
	defer tx.Rollback(context.Background())
	searched := &Erasure{Kind: kind, ID: id}
	read, err := searched.readDerived(ctx, tx)
	if err != nil {
		return searchedBeforeLocks{}, err
	}
	if err := searched.readLeft(ctx, tx, &read); err != nil {
		return searchedBeforeLocks{}, err
	}
	trees, err := searched.treeKeysHolding(ctx, tx, read)
	if err != nil {
		return searchedBeforeLocks{}, err
	}
	found := searchedBeforeLocks{left: read.listing.left, trees: trees}
	for _, event := range read.events {
		found.events = append(found.events, event.id)
	}
	return found, nil
}

// searchLeftAfterCommit is the search for the rows the run leaves made once more, after the commit
// and so outside the run's locks: readLeft over the read under those locks - its texts, its
// document, its block, its versions and the events it redacted - with that read's rows left
// emptied first, in a read-only transaction of its own on the run's connection, as searchLeft
// runs. Each row it lists that the search before the locks did not (read.listing.left) is one
// written after that search, which the listing the digest covers does not name; it goes into
// LeftAfterCommit, which Write prints. A search that fails names no row, and its error says the
// erase committed.
func (e *Erasure) searchLeftAfterCommit(ctx context.Context, conn *pgx.Conn, read erasedRead) error {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("the erase committed, and its search after the commit for the rows it leaves could not begin: %w", err)
	}
	defer tx.Rollback(context.Background())
	listed := read.listing.left
	read.listing.left = nil
	if err := e.readLeft(ctx, tx, &read); err != nil {
		return fmt.Errorf("the erase committed, and its search after the commit for the rows it leaves failed: %w", err)
	}
	e.LeftAfterCommit = []string{}
	for _, line := range read.listing.left {
		if !slices.Contains(listed, line) {
			e.LeftAfterCommit = append(e.LeftAfterCommit, line)
		}
	}
	e.searchedAfterCommit = true
	return nil
}

// lockOwnersThenNode is one try of lockInStoreOrder: it reads which owner rows the erase writes
// under (ownerRows, which reads no node, tuple or event) and their projects (ownerProjects), takes
// those projects' term statistics totals rows (api.TakeErasedTermTotalsTx), holds the keyword fill
// back (textversions.HoldFillBack), takes the owners (events.LockOwnerRows), the node's row and
// the node's projection rows (lockProjectionRows), reads the rest of the listing under those locks
// (readDerived) - the events about the node among those of the owners it took and those the try's
// search found (eventsUnderLocks) - lists beside it the rows left that the search found
// (searchLeft), and answers it with every owner it names that the try did not take.
func (e *Erasure) lockOwnersThenNode(ctx context.Context, tx pgx.Tx, found searchedBeforeLocks) (erasedRead, []string, error) {
	before, documents, err := e.ownerRows(ctx, tx)
	if err != nil {
		return erasedRead{}, nil, err
	}
	projects, err := e.ownerProjects(ctx, tx, before, documents)
	if err != nil {
		return erasedRead{}, nil, err
	}
	if err := api.TakeErasedTermTotalsTx(ctx, tx, projects); err != nil {
		return erasedRead{}, nil, err
	}
	if err := textversions.HoldFillBack(ctx, tx); err != nil {
		return erasedRead{}, nil, err
	}
	if err := events.LockOwnerRows(ctx, tx, before); err != nil {
		return erasedRead{}, nil, err
	}
	if err := tx.QueryRow(ctx,
		`select supertag from graph_nodes where kind = $1 and id = $2 for update`,
		e.Kind, e.ID).Scan(&e.Supertag); err != nil {
		if err == pgx.ErrNoRows {
			return erasedRead{}, nil, fmt.Errorf("the store holds no node %s:%s", e.Kind, e.ID)
		}
		return erasedRead{}, nil, fmt.Errorf("read node %s:%s: %w", e.Kind, e.ID, err)
	}
	if err := lockProjectionRows(ctx, tx, e.Kind, e.ID); err != nil {
		return erasedRead{}, nil, err
	}
	e.underLocks = &eventsUnderLocks{owners: before, ids: found.events}
	locked, err := e.readDerived(ctx, tx)
	if err != nil {
		return erasedRead{}, nil, err
	}
	locked.listing.left = found.left
	locked.termTotals = projects
	e.list(&locked)
	var untaken []string
	for _, key := range slices.Sorted(maps.Keys(locked.owners)) {
		if _, taken := before[key]; !taken {
			untaken = append(untaken, key+", the owner of "+locked.owners[key])
		}
	}
	return locked, untaken, nil
}

// lockProjectionRows takes every projection row of the node `for update`, the level the delete of
// each in apply takes, right after the node's row and before the erase reads under its locks or
// writes anything: table by table in graphstore.ProjectedTables' order, and by key within each.
// R17 has a node's row taken before any projection row whose foreign key names it, and every
// writer of these rows that takes an owner row takes it before them, which the erase already
// holds.
//
// Taking them here, and not when apply deletes them, is what orders the erase against a writer
// that holds one of them and no owner row: the block index tick's window arm takes a comment's
// row `for no key update` and then writes that comment's windows and their window state
// (refs.WriteItemWindows). An erase that took the row only at its delete would have deleted those
// windows and that state first (forgetEmbeddings), and the arm, writing the same keys, would wait
// for the erase while the erase waited for it, until Postgres ended one of them. Here the erase
// waits at the row holding no row the arm writes, and the arm commits first.
func lockProjectionRows(ctx context.Context, tx pgx.Tx, kind, id string) error {
	for _, held := range graphstore.ProjectedTables {
		if _, err := tx.Exec(ctx, `select 1 from `+held.Table+` where node_kind = $1 and node_address = $2 order by `+
			held.Key+` for update`, kind, id); err != nil {
			return fmt.Errorf("lock the %s rows of %s:%s: %w", held.Table, kind, id, err)
		}
	}
	return nil
}
