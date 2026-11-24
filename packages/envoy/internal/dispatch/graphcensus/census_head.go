package graphcensus

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The head projection (migration 20261123180100_issue_tree_head): issue_head_nodes holds one row
// per node of an issue's tree as the store holds it (graphstore.HeadRowsSQL), and issue_head_marks
// names each issue the head read may read the rows of. Every writer of this build writes the rows of
// the nodes it writes (graphstore.QueueHeads); a write of a build that keeps none takes the marks of
// the issues it reaches away (issue_head_marks_drop). The fill (FillIssueHeads) writes every row of
// one issue no mark names from the store and marks it, which the backfill runs over every such issue
// (backfillIssueHeadNodes) and the block index's tick over a bounded batch each tick (docs'
// ReconcileBlockIndex); the census kind (CensusOfHeads) holds every row of a marked issue, every column of it, to
// what the store holds.

// headKind is the head projection's kind, as the census and the backfill report it.
const headKind = "issue_head_node"

// headBackfills are the backfill's pass over the issues no mark names (backfillIssueHeadNodes).
// It runs last, once every other pass has written the nodes and tuples it owes, so the rows it
// writes are of the store those passes leave.
var headBackfills = []backfillPass{oneKind(backfillIssueHeadNodes)}

// HeadFillBatch is how many issues one tick of the block index fills (FillIssueHeads): a tick
// fills a few issues and moves on, and an issue a write of an older build unmarked is filled again
// within a few ticks.
const HeadFillBatch = 20

// headFillLocked, which only a test sets, runs inside the fill of the issue key right after the
// fill takes the issue's head fill lock, so the test holds the fill there while a write of the
// release before starts (TestTheFillHoldsTheHeadFillLockAgainstAWriteOfTheReleaseBefore).
var headFillLocked func(key string)

// unmarkedIssuesStatement is up to $1 issues no mark names, in key order.
const unmarkedIssuesStatement = `
	select i.key from issues i
	where not exists (select 1 from issue_head_marks mark where mark.issue_key = i.key)
	order by i.key collate "C"
	limit $1`

// headFillNodesStatement is every node the tree of the issue $1 can hold, whose rows its fill writes
// (graphstore.QueueHeads also writes the rows of each under another issue): the issue's own node;
// each node whose address begins with its key, a document's, a version's and a block's of its
// documents, and each block that ever stood in a document of the issue (in_document), whatever its
// address; its asks, comments and messages and the blocks of each; each node the issue's rows hold
// already; and under each of those that its walk enters - one of the issue's own, or one whose
// issue its parent gives - every node a part_of into it asserts, down to the leaves. A node of
// another issue placed under one of them is taken, as one the issue's walk answers and does not
// enter, and nothing under it is. The issue's rows sort their kinds and addresses byte by byte
// (collate "C"); the statement reads them at the default collation, as every other seed reads its
// own, so that each step's lookup of graph_tuples by the node it reached is a probe of that
// table's index (graph_tuples_as_of_in) rather than a scan of the table.
var headFillNodesStatement = `
	with recursive head_seeds (kind, id) as (
		select '` + graphstore.KindIssue + `', $1::text
		union
		select n.kind, n.id from graph_nodes n
		where n.kind in ('` + graphstore.KindArtifact + `', '` + graphstore.KindVersion + `', 'block')
		  and n.id like $2 escape '\'
		union
		select t.from_kind, t.from_id
		from artifacts a
		join graph_tuples t on t.to_kind = '` + graphstore.KindArtifact + `' and t.to_id = a.ref_key and t.kind = 'in_document'
		where a.issue_key = $1
		union
		select '` + graphstore.KindAsk + `', id::text from asks where issue_key = $1
		union
		select '` + graphstore.KindComment + `', id::text from comments where issue_key = $1
		union
		select '` + graphstore.KindMessage + `', id::text from messages where issue_key = $1
		union
		select 'ask_block', b.node_id from ask_blocks b join asks a on a.id = b.ask_id where a.issue_key = $1
		union
		select 'comment_block', b.node_id from comment_blocks b join comments c on c.id = b.comment_id where c.issue_key = $1
		union
		select 'message_block', b.node_id from message_blocks b join messages m on m.id = b.message_id where m.issue_key = $1
		union
		select h.node_kind collate "default", h.node_address collate "default" from issue_head_nodes h where h.issue_key = $1
	), head_reached (kind, id, depth) as (
		select kind, id, 0 from head_seeds
		union
		select child.from_kind, child.from_id, reached.depth + 1
		from head_reached reached
		cross join lateral (
			select under.from_kind, under.from_id from (
				select distinct on (t.from_kind, t.from_id) t.from_kind, t.from_id, t.asserted
				from graph_tuples t
				where t.to_kind = reached.kind and t.to_id = reached.id and t.kind = '` + graphstore.TuplePartOf + `'
				  and t.to_kind <> '` + graphstore.KindValue + `'
				order by t.from_kind, t.from_id, t.at desc, t.seq desc
			) under
			where under.asserted
		) child
		where reached.depth < 32
		  and (reached.kind = '` + graphstore.KindIssue + `' and reached.id = $1
		       or reached.kind not in ('` + graphstore.KindIssue + `', '` + graphstore.KindArtifact + `', '` + graphstore.KindVersion + `', 'block')
		       or reached.id like 'lens/%'
		       or split_part(reached.id, '/', 1) = $1)
	)
	select distinct kind, id from head_reached`

// likePrefix is the LIKE pattern of every text that begins with prefix, its own wildcards escaped.
func likePrefix(prefix string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix) + "%"
}

// FillIssueHeads fills up to limit issues no mark names (fillIssueHeads), in key order, each in a
// transaction of its own, and answers how many it filled and whether more stood unmarked when it
// listed them. An issue a fill finds marked once it holds the issue's locks is left as it is.
func FillIssueHeads(ctx context.Context, database *store.Store, limit int) (filled int, more bool, err error) {
	rows, err := database.Pool.Query(ctx, unmarkedIssuesStatement, limit+1)
	if err != nil {
		return 0, false, fmt.Errorf("list the issues no head mark names: %w", err)
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, false, fmt.Errorf("list the issues no head mark names: %w", err)
	}
	if len(keys) > limit {
		keys, more = keys[:limit], true
	}
	for _, key := range keys {
		if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error { return fillIssueHeads(ctx, tx, key) }); err != nil {
			return filled, more, fmt.Errorf("fill the head rows of %s: %w", key, err)
		}
		filled++
	}
	return filled, more, nil
}

// fillIssueHeads writes in tx every row of the issue key's tree from what the store holds there
// (headFillNodesStatement, graphstore.WriteHeads) and marks the issue whole (issue_head_marks):
// under the issue's owner lock, which every writer of the issue holds, and then its head fill lock
// (store.LockHeadFill), which a write of a build that keeps no head rows takes before it takes the
// mark away, so such a write waits for this one and takes away the mark it commits, while this one
// waits for nothing such a write holds. The mark is read again under both locks: a fill that ran
// meanwhile has written the rows and marked the issue already.
func fillIssueHeads(ctx context.Context, tx pgx.Tx, key string) error {
	if err := store.LockOwner(ctx, tx, store.IssueOwner(key)); err != nil {
		return err
	}
	if err := store.LockHeadFill(ctx, tx, key); err != nil {
		return err
	}
	if headFillLocked != nil {
		headFillLocked(key)
	}
	var marked bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from issue_head_marks where issue_key = $1)`, key).Scan(&marked); err != nil {
		return fmt.Errorf("read the head mark of %s: %w", key, err)
	}
	if marked {
		return nil
	}
	rows, err := tx.Query(ctx, headFillNodesStatement, key, likePrefix(key+"/"))
	if err != nil {
		return fmt.Errorf("read the nodes of the tree of %s: %w", key, err)
	}
	nodes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (graphstore.NodeKey, error) {
		var node graphstore.NodeKey
		return node, row.Scan(&node.Kind, &node.ID)
	})
	if err != nil {
		return fmt.Errorf("read the nodes of the tree of %s: %w", key, err)
	}
	if err := graphstore.WriteHeads(ctx, tx, nodes); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into issue_head_marks (issue_key, filled_at) values ($1, clock_timestamp())`, key); err != nil {
		return fmt.Errorf("mark the head rows of %s whole: %w", key, err)
	}
	return nil
}

// backfillIssueHeadNodes fills every issue no mark names (FillIssueHeads), a batch of them at a
// time, until none is left: the issues a store of the release before holds, whose writers kept no
// head rows, and each issue a write of such a release unmarked since. It reports the issues it
// filled as its rows, and none remaining once it has listed no unmarked issue.
func backfillIssueHeadNodes(ctx context.Context, database *store.Store) (KindBackfill, error) {
	done := KindBackfill{Kind: headKind}
	for {
		filled, more, err := FillIssueHeads(ctx, database, backfillBatch)
		done.Rows += int64(filled)
		if err != nil {
			return done, err
		}
		if !more {
			done.Complete = true
			return done, nil
		}
	}
}

// headDigest is the md5 of every column of a head row but its key, read off the relation alias,
// whose columns are graphstore.HeadColumns.
func headDigest(alias string) string {
	columns := strings.Split(graphstore.HeadColumns, ",")
	for index, column := range columns {
		columns[index] = alias + "." + strings.TrimSpace(column)
	}
	return `md5(row(` + strings.Join(columns, ", ") + `)::text)`
}

// headSides is the two sides of the head projection's census, each a select of (issue_key,
// node_kind, node_address, digest): the rows issue_head_nodes holds, and the rows the store says it
// should hold (graphstore.HeadRowsSQL) of every node graph_nodes holds and every node a row names,
// the latter read at the default collation, as graph_nodes' are, so the store's side probes the
// store's indexes by them.
var headSides = `
	with head_stored as (
		select stored.issue_key, stored.node_kind, stored.node_address, ` + headDigest("stored") + ` as digest
		from issue_head_nodes stored
		where exists (select 1 from issue_head_marks mark where mark.issue_key = stored.issue_key)
	), head_expected as (
		select expected.issue_key, expected.kind as node_kind, expected.id as node_address, ` + headDigest("expected") + ` as digest
		from (` + graphstore.HeadRowsSQL(`select kind, id from graph_nodes union select node_kind collate "default", node_address collate "default" from issue_head_nodes`) + `) expected
		where exists (select 1 from issue_head_marks mark where mark.issue_key = expected.issue_key)
	)`

// headDifferences is each row the two sides do not hold alike, as (cause, at): a row the table holds
// that the store gives no row of, or gives another, and a row the store gives that the table holds
// none of, each named by its issue and its node.
var headDifferences = headSides + `
	select case when expected.issue_key is null then 'a row of a node no issue''s tree holds, or of an issue that holds it no more'
	            else 'the row and the store disagree' end,
	       'issue ' || stored.issue_key || ' ' || stored.node_kind || ' ' || stored.node_address
	from head_stored stored
	left join head_expected expected on expected.issue_key = stored.issue_key
	  and expected.node_kind = stored.node_kind and expected.node_address = stored.node_address
	where expected.digest is distinct from stored.digest
	union all
	select 'a node the issue''s tree holds and no row projects',
	       'issue ' || expected.issue_key || ' ' || expected.node_kind || ' ' || expected.node_address
	from head_expected expected
	where not exists (
		select 1 from head_stored stored where stored.issue_key = expected.issue_key
		  and stored.node_kind = expected.node_kind and stored.node_address = expected.node_address)`

// CensusOfHeads is the census of the head projection, the kind issue_head_node of the report: every
// row issue_head_nodes holds of a marked issue, held column by column to the row the store gives of
// its node under its issue (graphstore.HeadRowsSQL), and every row the store gives of a marked issue,
// held to the table, so a row a writer left behind the store, a row of a node no tree holds, and a
// node of an issue's tree no row projects each read as a difference, named by its issue and its
// node. No row of an unmarked issue is compared, since the head read reads none. A deployment that
// holds no marked issue holds no row (MayBeEmpty). It reads zero once the backfill has filled every
// issue and every writer since has kept the rows. The store census reads it as its last kind, and a
// test reads it in its own transaction.
func CensusOfHeads(ctx context.Context, tx pgx.Tx) (KindReport, error) {
	kind := KindReport{Kind: headKind, Table: "issue_head_nodes", MayBeEmpty: true,
		Note: "one row per node of a marked issue's tree, under each issue whose tree holds it; every column of each row" +
			" against the row the store gives of the node (graphstore.HeadRowsSQL), in both directions"}
	if err := tx.QueryRow(ctx, headSides+`
		select (select count(*) from head_stored), (select count(*) from head_expected)`).Scan(&kind.Rows, &kind.Nodes); err != nil {
		return KindReport{}, fmt.Errorf("census of %s: %w", headKind, err)
	}
	rows, err := tx.Query(ctx, headDifferences+`
		order by 2, 1`)
	if err != nil {
		return KindReport{}, fmt.Errorf("census of %s: %w", headKind, err)
	}
	defer rows.Close()
	for rows.Next() {
		var found Difference
		if err := rows.Scan(&found.Cause, &found.At); err != nil {
			return KindReport{}, fmt.Errorf("census of %s: %w", headKind, err)
		}
		if strings.HasPrefix(found.Cause, "a node ") {
			kind.NodeDifferences++
			if len(kind.ReportedNodes) < reportedDifferences {
				kind.ReportedNodes = append(kind.ReportedNodes, found)
			}
			continue
		}
		kind.RowDifferences++
		if len(kind.ReportedRows) < reportedDifferences {
			kind.ReportedRows = append(kind.ReportedRows, found)
		}
	}
	if err := rows.Err(); err != nil {
		return KindReport{}, fmt.Errorf("census of %s: %w", headKind, err)
	}
	return kind, nil
}
