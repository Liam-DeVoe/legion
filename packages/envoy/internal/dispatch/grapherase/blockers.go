package grapherase

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
)

// referencingColumn is one foreign key pointing at a projected table: the table and column that
// hold the reference, the column they hold, and whether that reference can be null.
type referencingColumn struct {
	Table, Column, Referenced string
	Required                  bool
}

// referencesTo reads every single-column foreign key pointing at one table. The composite key a
// projection carries to graph_nodes is not one of them, which is why the read binds a key of one
// column: that pair is the node's own address and goes with the node.
func referencesTo(ctx context.Context, tx pgx.Tx, table string) ([]referencingColumn, error) {
	rows, err := tx.Query(ctx, `
		select c.conrelid::regclass::text, child.attname, parent.attname, child.attnotnull
		from pg_constraint c
		join pg_attribute child on child.attrelid = c.conrelid and child.attnum = c.conkey[1]
		join pg_attribute parent on parent.attrelid = c.confrelid and parent.attnum = c.confkey[1]
		where c.contype = 'f' and c.confrelid = $1::regclass and array_length(c.conkey, 1) = 1
		order by 1, 2`, table)
	if err != nil {
		return nil, fmt.Errorf("read the foreign keys into %s: %w", table, err)
	}
	references, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (referencingColumn, error) {
		var reference referencingColumn
		err := row.Scan(&reference.Table, &reference.Column, &reference.Referenced, &reference.Required)
		return reference, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the foreign keys into %s: %w", table, err)
	}
	return references, nil
}

// projectionRows is the select of one projection's key column, which every delete against a row
// that points at it is written over.
func projectionRows(reference referencingColumn, table string) string {
	return `select held.` + reference.Referenced + ` from ` + table +
		` held where held.node_kind = $1 and held.node_address = $2`
}

// readBlockers names every live row of another node that states something about this one, each
// of which refuses the run, so an operator erases or edits those first and the store census
// still reads zero once the erase runs.
//
// Three kinds of row do. A row that points at this node's projection through a nullable
// reference: a reply whose reply_to names an erased comment is somebody else's words, and an ask
// handed back through it is the ask's own history, so the erase neither deletes one nor rewrites
// the other. A projection row of another live node whose node states a tuple into this one now: a
// block inside an erased block (its part_of and its parent_block_id), a block that cites it (its
// mentions or pointer tuple and its block_refs row), a comment its author is (its authored_by and
// its author). The erase deletes the tuples into the node, so such a row would go on stating a
// tuple the store no longer holds, which is a difference `census --store` reads. And, for a
// person, a comments row that names them as the owner of the session the server proved that wrote
// it (graphcensus.ProvedSessionComments, the census's own reading of that row): its authored_by
// names the session and only the session's alias_of names the person, and a session has no
// projection row, so the read before finds no such row, but the census names the person from the
// comment's row and would read her node gone while that row stands.
//
// Each node stating a tuple into this one probes its own row through graph_nodes' primary key,
// in a subquery behind an offset 0 the planner cannot flatten, so no plan reads graph_nodes
// again for each such tuple. The tuples are read through graph_tuples_as_of_in, whose predicate
// the read proves by naming `to_kind <> 'value'` (a value is no node, so nothing points into
// one), so a plan cached without the node's kind still reaches that index
// (TestTheBlockersReadProbesEachStatingNodeOnce).
func (e *Erasure) readBlockers(ctx context.Context, tx pgx.Tx, read *erasedRead) error {
	for _, held := range graphstore.ProjectedTables {
		references, err := referencesTo(ctx, tx, held.Table)
		if err != nil {
			return err
		}
		for _, reference := range references {
			if reference.Required {
				continue
			}
			var rows int64
			if err := tx.QueryRow(ctx, `select count(*) from `+reference.Table+` child where child.`+
				reference.Column+` in (`+projectionRows(reference, held.Table)+`)`, e.Kind, e.ID).Scan(&rows); err != nil {
				return fmt.Errorf("count the %s.%s rows at %s:%s: %w", reference.Table, reference.Column, e.Kind, e.ID, err)
			}
			if rows > 0 {
				e.Blockers = append(e.Blockers, fmt.Sprintf("%d %s row(s) point at this node through %s.%s",
					rows, reference.Table, reference.Table, reference.Column))
			}
		}
	}
	projected := make([]string, len(graphstore.ProjectedTables))
	for index, held := range graphstore.ProjectedTables {
		projected[index] = `exists (select 1 from ` + held.Table + ` p where p.node_kind = t.from_kind and p.node_address = t.from_id)`
	}
	rows, err := tx.Query(ctx, `
		select t.from_kind, t.from_id, t.kind from (
			`+graphstore.CurrentTupleRead(`t.from_kind, t.from_id, t.kind`, `t.from_kind, t.from_id, t.kind, t.asserted`,
		`t.to_kind = $1 and t.to_id = $2 and t.to_kind <> '`+graphstore.KindValue+`'
			and not (t.from_kind = $1 and t.from_id = $2)`)+`
		) t
		cross join lateral (
			select n.state from graph_nodes n
			where n.kind = t.from_kind and n.id = t.from_id
			offset 0
		) n
		where n.state = $3 and t.asserted and (`+strings.Join(projected, " or ")+`)
		order by 1, 2, 3`, e.Kind, e.ID, graphstore.StateLive)
	if err != nil {
		return fmt.Errorf("read the nodes stating a tuple into %s:%s: %w", e.Kind, e.ID, err)
	}
	stating, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
		var fromKind, fromID, kind string
		err := row.Scan(&fromKind, &fromID, &kind)
		return fmt.Sprintf("%s:%s states %s into this node, and its own row says so: erase or edit it first",
			fromKind, fromID, kind), err
	})
	if err != nil {
		return fmt.Errorf("read the nodes stating a tuple into %s:%s: %w", e.Kind, e.ID, err)
	}
	e.Blockers = append(e.Blockers, stating...)
	if e.Kind == graphstore.KindPerson {
		rows, err := tx.Query(ctx, graphcensus.ProvedSessionComments, e.ID)
		if err != nil {
			return fmt.Errorf("read the comments naming %s:%s as the owner of the session that wrote them: %w", e.Kind, e.ID, err)
		}
		naming, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
			var comment string
			err := row.Scan(&comment)
			return fmt.Sprintf("%s:%s was written by a session the server proved this person's, and its own row names this person as that session's owner: erase it first",
				graphstore.KindComment, comment), err
		})
		if err != nil {
			return fmt.Errorf("read the comments naming %s:%s as the owner of the session that wrote them: %w", e.Kind, e.ID, err)
		}
		e.Blockers = append(e.Blockers, naming...)
	}
	read.listing.blockers = e.Blockers
	return nil
}
