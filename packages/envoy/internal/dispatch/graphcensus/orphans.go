package graphcensus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// SweepActor is who states an orphaned document's node gone, and every stale lens: the
// block-index tick of this build, while it owes the object pass (docs'
// writeObjectNodesAnotherBuildWrote). The removal it completes was a person's, and the event that
// removal appended names them.
var SweepActor = model.Actor{Kind: "system", ID: "dispatch-block-index"}

// OrphanedDocumentNodes answers, in address order, at most limit of the document nodes the
// project census reads as orphaned at an issue's address: a node at any state but gone at an
// address no artifacts row holds, whose address names an issue. It reads through the artifacts
// table's unique index on ref_key and the issues table's key, so a read with nothing to find costs
// an index scan of the document nodes alone (README's cost of the pass).
//
// A document is the one kind of the four whose row any build deletes: no build deletes an issue,
// an ask or a message, and none of the four tables cascades a delete from another. Only an issue's
// document is removable, by this build's removal and by the ones before it. This build's removal
// states the node gone in the transaction that deletes the row (api's removeArtifact); the release
// before this one writes no node and states none gone, so a document it removes after this build
// wrote the node leaves the node standing behind no row, which is all this read can find. No
// writer of this build commits a node before its row: each writes it in the transaction that
// writes its row.
func OrphanedDocumentNodes(ctx context.Context, q graphstore.Queryer, limit int) ([]string, error) {
	rows, err := q.Query(ctx, `
		select n.id from graph_nodes n
		where n.kind = '`+graphstore.KindArtifact+`' and `+orphanedDocumentNode("n")+`
		order by n.id
		limit $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("read the orphaned document nodes: %w", err)
	}
	orphaned, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the orphaned document nodes: %w", err)
	}
	return orphaned, nil
}

// orphanedDocumentNode is, for the document node n, whether it is orphaned at an issue's address:
// at any state but gone, at an address no artifacts row holds, and naming an issue.
func orphanedDocumentNode(n string) string {
	return n + `.state <> '` + graphstore.StateGone + `'
		  and not exists (select 1 from artifacts a where a.ref_key = ` + n + `.id)
		  and exists (select 1 from issues i where i.key = split_part(` + n + `.id, '/', 1))`
}

// ErrNotOrphaned is StateOrphanedGone's answer for a node it may not state gone, which it leaves
// as it is: the store holds no such node, or holds it gone already, or a row stands at its address
// by the time the call reads under its lock of the node, or its address is no issue's document.
var ErrNotOrphaned = errors.New("the node is not an orphaned document's")

// StateOrphanedGone states the orphaned document node at ref gone, as this build's removal of the
// document would have stated it (api's removeArtifact): the node as it stands, its state gone, at
// the time the store dates the address's next statement (graphstore.StatementTime), caused by the
// artifact.removed event the release before this one appended for the removal, the newest one of
// its issue naming the address, and at the sentinel where there is none. Only who states it
// differs, the block-index tick (SweepActor), since the person who removed it is the event's.
//
// It states nothing beneath the node gone. A document any release removes holds one empty version
// (api's artifactIsUntouched), whose index holds no block, so what stands under its node is the
// lenses invoked on it and what stands under each. Each such lens is stale already, since the row
// it recorded no longer stands at the address (refs.StaleSQL), so no read answers one, and
// StateStaleLensesGone, which the tick runs after this, takes each one gone with its whole
// subtree. A node a write moved under the document's node, which is no stale lens and stands under
// none, belongs to the document its own address names and stays as it is.
//
// It takes the store's lock order: the issue's owner lock first (events.LockOwnerRows), as the
// removal and every create of a document of that issue take it, then the node's row `for
// update`, and only under that lock reads whether a row stands at the address. A create of this
// build writes the node and then the row in one transaction under that owner lock, so a create
// that got there first has committed its row when the read runs, and the node is left: the answer
// is ErrNotOrphaned, as it is for every node it leaves. An address that names no issue is no
// removable document's, which only an issue's are, and is left too.
func StateOrphanedGone(ctx context.Context, database *store.Store, ref string) error {
	issue, _, found := strings.Cut(ref, "/")
	if !found {
		return fmt.Errorf("%w: %s holds no issue key", ErrNotOrphaned, ref)
	}
	return pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		var isIssue bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from issues where key = $1)`, issue).Scan(&isIssue); err != nil {
			return fmt.Errorf("read the issue of %s: %w", ref, err)
		}
		if !isIssue {
			return fmt.Errorf("%w: %s is no issue's document", ErrNotOrphaned, ref)
		}
		owners := events.OwnerRows{}
		owners.Add(issue, "", "the issue of document "+ref)
		if err := events.LockOwnerRows(ctx, tx, owners); err != nil {
			return err
		}
		var state string
		switch err := tx.QueryRow(ctx, `select state from graph_nodes where kind = $1 and id = $2 for update`,
			graphstore.KindArtifact, ref).Scan(&state); {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w: the store holds no node %s", ErrNotOrphaned, ref)
		case err != nil:
			return fmt.Errorf("lock the node of %s: %w", ref, err)
		case state == graphstore.StateGone:
			return fmt.Errorf("%w: the node of %s is gone", ErrNotOrphaned, ref)
		}
		if stands, err := ObjectRowStands(ctx, tx, graphstore.KindArtifact, ref); err != nil {
			return err
		} else if stands {
			return fmt.Errorf("%w: a row stands at %s", ErrNotOrphaned, ref)
		}
		node, _, err := graphstore.ReadNode(ctx, tx, graphstore.KindArtifact, ref)
		if err != nil {
			return err
		}
		removal := graphstore.NoEvent
		if err := tx.QueryRow(ctx, `
			select coalesce(max(id), $3) from events
			where issue_key = $1 and type = 'artifact.removed' and payload->'artifact'->>'ref_key' = $2
		`, issue, ref, graphstore.NoEvent).Scan(&removal); err != nil {
			return fmt.Errorf("read the removal event of %s: %w", ref, err)
		}
		removedAt, err := graphstore.StatementTime(ctx, tx, graphstore.KindArtifact, ref)
		if err != nil {
			return err
		}
		gone := graphstore.ArtifactNode(ref, node.Project, sessionParty(node.Parties), SweepActor, removedAt)
		gone.State = graphstore.StateGone
		uncaused := graphstore.PlanUncaused([]graphstore.Node{gone}, nil)
		if err := store.RunWrite(ctx, tx, func(b *store.WriteBatch) error {
			graphstore.QueueNodes(b, uncaused)
			return nil
		}); err != nil {
			return fmt.Errorf("state the orphaned node of %s gone: %w", ref, err)
		}
		return graphstore.Caused(ctx, tx, uncaused, removal)
	})
}

// staleLenses reads the row of each stale lens that is not gone (refs.StaleSQL), as
// graphstore.ScanNode reads a node row: each lens that records a document's row
// (graphstore.AttributeArtifactID) whose row no longer stands at the address of the document it
// stands on, with the key of the issue that address names, `<issue key>/<slug>`, whose owner lock
// every writer under the lens takes (api's lockIssueOwner). Only a lens that records a row can be
// stale itself, so the read keeps those before it walks; every node under one is stale with it and
// is taken with it as NodesBeneath reads it. It reads the lenses of the store, through the primary
// key of graph_nodes.
var staleLenses = `
	select ` + graphstore.NodeColumnsOf(`stale`) + `, split_part(split_part(placed.to_id, '#', 1), '/', 1)
	from graph_nodes stale
	cross join lateral (
		` + refs.CurrentFarEndSQL(`newest.to_kind, newest.to_id`, `'`+graphstore.KindLens+`'`, `stale.id`, graphstore.TuplePartOf) + `
	) placed
	where stale.kind = '` + graphstore.KindLens + `' and stale.state <> '` + graphstore.StateGone + `'
	  and stale.attributes->>'` + graphstore.AttributeArtifactID + `' is not null
	  and ` + refs.StaleSQL(`'`+graphstore.KindLens+`'`, `stale.id`) + `
	order by stale.id`

// staleLensesOn is staleLenses over the lenses whose current part_of stands on the document node
// at the address $1 alone (staleLensesOnSQL). A document any release removes holds one empty
// version, whose index holds no block, so no lens stands on a block of it.
var staleLensesOn = staleLensesOnSQL(graphstore.NodeColumnsOf(`stale`)+`, split_part($1, '/', 1)`, `$1`) + `
	order by stale.id`

// staleLensesOnSQL is the select of columns over each stale lens that is not gone (refs.StaleSQL),
// aliased stale, whose current part_of stands on the document node at the address the SQL document
// names, read into that node through graph_tuples_as_of_in: the lenses on one document that its
// removal reads (StateStaleLensesGone), and that the tick's read of whether its events owe the
// object pass reads at each address an event names (EventsOweObjectPass).
func staleLensesOnSQL(columns, document string) string {
	return `
	select ` + columns + `
	from (
		` + graphstore.CurrentTupleRead(`t.from_id`, `t.from_id, t.asserted`,
		`t.to_kind = '`+graphstore.KindArtifact+`' and t.to_id = `+document+` and t.kind = '`+graphstore.TuplePartOf+`'
			and t.from_kind = '`+graphstore.KindLens+`' and t.to_kind <> '`+graphstore.KindValue+`'`) + `
	) placed
	join graph_nodes stale on stale.kind = '` + graphstore.KindLens + `' and stale.id = placed.from_id
	where placed.asserted and stale.state <> '` + graphstore.StateGone + `'
	  and ` + refs.StaleSQL(`'`+graphstore.KindLens+`'`, `stale.id`)
}

// readStaleLenses answers the row of each stale lens staleLenses reads, or staleLensesOn for the
// document at document where it is not empty, each with the issue its document's address names.
func readStaleLenses(ctx context.Context, tx pgx.Tx, document string) (lenses []graphstore.Node, issues []string, err error) {
	statement, args := staleLenses, []any(nil)
	if document != "" {
		statement, args = staleLensesOn, []any{document}
	}
	rows, err := tx.Query(ctx, statement, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("read the stale lenses: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var issue string
		lens, err := graphstore.ScanNode(rows, &issue)
		if err != nil {
			return nil, nil, fmt.Errorf("read the stale lenses: %w", err)
		}
		lenses, issues = append(lenses, lens), append(issues, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read the stale lenses: %w", err)
	}
	return lenses, issues, nil
}

// StateStaleLensesGone states gone, in the caller's transaction tx, opening none of its own, every
// stale lens the store holds that is not gone (refs.StaleSQL): a lens on a document, or on a block
// of one, whose recorded row (graphstore.AttributeArtifactID) no longer stands at the document's
// address, because the row is gone or another row stands there. Each goes with its whole part_of
// subtree, at every depth, its findings, its risks and the lenses on those, whoever may see each
// node (graphstore.NodesBeneath), each written again as the store holds it but for its state,
// keeping its cause, with one state tuple at the sentinel signed by who at at
// (graphstore.AsStateWrite), all in one write. It reads a lens stale whatever the state of the node
// at its document's address - live behind no row, gone behind no row, or live for the row a later
// create made there - since the recorded row, and not the node, is what tells which document the
// lens was written on. It answers how many nodes it stated gone.
//
// Where document is empty it reads every stale lens of the store: the block-index tick runs it so
// after its orphan sweep, in a transaction of its own, by SweepActor at the database's clock
// (docs' writeObjectNodesAnotherBuildWrote), so a lens a removal by an earlier build left stale
// does not stay live in the store. Where document names a document's address it reads the stale
// lenses on that document's node alone: the removal of the document runs it so, in the transaction
// that deletes the row and after the delete, by the person at the removal's time (api's
// removeArtifact). An at of the zero time is the database's clock, read once the locks are held.
//
// It takes the store's lock order. Where document is empty it reads the stale lenses with no
// lock, and where there are none it takes no lock and writes nothing; otherwise it takes the owner
// lock of each issue those lenses' documents name (events.LockOwnerRows), which every writer of a
// lens, a finding, a risk or a node's state under the issue takes first (api's lockIssueOwner),
// and reads the stale lenses again under them, keeping those of an issue it locked. Where document
// names a document's address, its caller holds the owner lock of the issue that address names
// already, the removal's first lock, so it reads the stale lenses once, under that lock, and takes
// none. Each read answers the row of each stale lens, so no lens is read again; it then reads what
// stands beneath each, and writes the nodes, in ascending (kind, id), and their state tuples, in
// one graphstore.Write.
func StateStaleLensesGone(ctx context.Context, tx pgx.Tx, document string, at time.Time, who model.Actor) (int, error) {
	locked := map[string]bool{}
	if document == "" {
		_, issues, err := readStaleLenses(ctx, tx, "")
		if err != nil || len(issues) == 0 {
			return 0, err
		}
		owners := events.OwnerRows{}
		for _, issue := range issues {
			owners.Add(issue, "", "the issue of a stale lens")
			locked[issue] = true
		}
		if err := events.LockOwnerRows(ctx, tx, owners); err != nil {
			return 0, err
		}
	}
	lenses, issues, err := readStaleLenses(ctx, tx, document)
	if err != nil {
		return 0, err
	}
	if at.IsZero() {
		if err := tx.QueryRow(ctx, `select clock_timestamp()`).Scan(&at); err != nil {
			return 0, fmt.Errorf("read the time of the stale lenses' gone: %w", err)
		}
	}
	var gone []graphstore.Node
	taken := map[[2]string]bool{}
	take := func(node graphstore.Node) {
		if key := [2]string{node.Kind, node.ID}; !taken[key] {
			taken[key] = true
			gone = append(gone, node)
		}
	}
	for index, lens := range lenses {
		if document == "" && !locked[issues[index]] {
			continue
		}
		take(lens)
		beneath, err := graphstore.NodesBeneath(ctx, tx, lens.Kind, lens.ID)
		if err != nil {
			return 0, err
		}
		for _, node := range beneath {
			take(node)
		}
	}
	if len(gone) == 0 {
		return 0, nil
	}
	graphstore.AsStateWrite(gone, graphstore.StateGone, at.UTC(), who)
	if err := graphstore.Write(ctx, tx, gone, nil); err != nil {
		return 0, fmt.Errorf("state %d nodes of stale lenses gone: %w", len(gone), err)
	}
	return len(gone), nil
}

// sessionParty is the session a document node's parties name, the empty string for a node of a
// project: graphstore.ArtifactNode takes the session and writes its party.
func sessionParty(parties []string) string {
	for _, party := range parties {
		if session, ok := strings.CutPrefix(party, "session:"); ok {
			return session
		}
	}
	return ""
}
