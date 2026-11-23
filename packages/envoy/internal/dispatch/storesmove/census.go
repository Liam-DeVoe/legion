package storesmove

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/prioritystore"
	"github.com/sjawhar/envoy/internal/dispatch/workflowstore"
)

// Census is the census before the drop: for each store apart, its old tables against the graph
// store, in both directions, counted and digested (StoreCensus). The swap takes it under its locks
// and drops nothing where it reads a difference (Differs), and the pre-deploy census's rehearsal
// takes it in the snapshot its copy wrote in. It reads every row of the four tables and every node
// and tuple of the three kinds the move writes; no row's values leave the database: a difference
// is named by its address alone (Difference).
type Census struct {
	Stores []StoreCensus
}

// StoreCensus is the census of one store.
//
//   - Nodes: each old node row has a graph_nodes row with the same supertag, visibility, state,
//     attributes, made_at, made_by and source_event, and each graph_nodes row of the store's kinds
//     has its old row.
//   - Tuples: each old tuple has one moved tuple with the same from_kind, from_id, kind, to_kind,
//     to_id, at, who, source_event and asserted, at the same place among the tuples of its node,
//     the order of their seq; and each tuple of graph_tuples out of a node of the store's kinds,
//     and, of the priority store, each advances tuple into a priority, has its old tuple. The
//     place makes each row one of its own on either side, so the two compare as sets.
//   - Stated, of the workflow store: the value tuples the copy states of each run and plan, its
//     visibility and its state, which the old row holds as columns, and its issue's project where
//     the row holds no project attribute, each equal to that value, dated at the row's made_at, by
//     its made_by, under its cause, and each stated once. Tuples leaves them out.
//   - OtherIssue and OtherProject, of the workflow store: the runs and plans whose issue attribute
//     names another issue than their part_of tuple, and those whose project_key is null or names
//     another project than their issue's. Each must count 0, since the plans read joins a project's
//     finished issues to their runs and plans by project_key (workflowstore's ProjectPlans).
type StoreCensus struct {
	Store                    string
	Nodes, Tuples            Comparison
	Stated                   *Comparison
	OtherIssue, OtherProject *Count
}

// Comparison is one side of the store against the other: each side's rows counted and digested
// (Tally), the rows that one side holds and the other does not hold equal, and the first of them.
type Comparison struct {
	What        string
	Old, Moved  Tally
	Differences int64
	First       *Difference
}

// Tally is one side of a comparison: how many rows it holds, and the md5 of their text, each row
// in the order of its place, so two sides of equal rows have equal digests.
type Tally struct {
	Rows   int64
	Digest string
}

// Count is one count of the census that must read 0, with the first row it counts.
type Count struct {
	What  string
	Rows  int64
	First *Difference
}

// Difference is the address of one row the census found that differs: its store, what it is, the
// kind and id of the node it is a row or a tuple of, the old seq of an old tuple and the store's
// seq of a moved one, and how it differs. It holds no value of the row.
type Difference struct {
	Store, What, Kind, ID string
	OldSeq, Seq           *int64
	Why                   string
}

func (d Difference) String() string {
	var text strings.Builder
	fmt.Fprintf(&text, "store=%s %s kind=%s id=%s", d.Store, d.What, d.Kind, d.ID)
	if d.OldSeq != nil {
		fmt.Fprintf(&text, " old_seq=%d", *d.OldSeq)
	}
	if d.Seq != nil {
		fmt.Fprintf(&text, " seq=%d", *d.Seq)
	}
	return text.String() + ": " + d.Why
}

// Differs reports whether the census read any difference.
func (c *Census) Differs() bool { return c.First() != nil }

// First is the first difference the census read, store by store, or nil.
func (c *Census) First() *Difference {
	for _, store := range c.Stores {
		for _, compared := range []*Comparison{&store.Nodes, &store.Tuples, store.Stated} {
			if compared != nil && compared.Differences > 0 {
				return compared.First
			}
		}
		for _, counted := range []*Count{store.OtherIssue, store.OtherProject} {
			if counted != nil && counted.Rows > 0 {
				return counted.First
			}
		}
	}
	return nil
}

// Lines are the census as lines of text, one for each comparison and count, and one naming the
// first difference where there is one.
func (c *Census) Lines() []string {
	var lines []string
	for _, store := range c.Stores {
		for _, compared := range []*Comparison{&store.Nodes, &store.Tuples, store.Stated} {
			if compared == nil {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s: %s: old %d (md5 %s), moved %d (md5 %s), %d differing",
				store.Store, compared.What, compared.Old.Rows, compared.Old.Digest, compared.Moved.Rows, compared.Moved.Digest, compared.Differences))
		}
		for _, counted := range []*Count{store.OtherIssue, store.OtherProject} {
			if counted != nil {
				lines = append(lines, fmt.Sprintf("%s: %s: %d", store.Store, counted.What, counted.Rows))
			}
		}
	}
	if first := c.First(); first != nil {
		lines = append(lines, "first difference: "+first.String())
	}
	return lines
}

// DifferenceError is the swap's refusal: the census before the drop read a difference, so the swap
// rolled back before any drop, and the four tables and the record stand as they were.
type DifferenceError struct{ Census *Census }

func (e *DifferenceError) Error() string {
	return "the census before the drop read a difference, and the swap dropped nothing: " + e.Census.First().String()
}

// queryer is what the census reads through: the swap's transaction or the rehearsal's.
type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// planner is handed each statement of the census before it runs, named by what it reads, so the
// rehearsal can print its plan (DISPATCH_TEST_STORES_MOVE_EXPLAIN); nil hands none.
type planner func(ctx context.Context, what, query string) error

// takeCensus takes the census before the drop in q, handing plan each statement first.
func takeCensus(ctx context.Context, q queryer, plan planner) (*Census, error) {
	census := &Census{}
	for _, old := range oldStores {
		taken := StoreCensus{Store: old.name}
		var err error
		if taken.Nodes, err = compare(ctx, q, plan, old, nodesCompared(old)); err != nil {
			return nil, err
		}
		if taken.Tuples, err = compare(ctx, q, plan, old, tuplesCompared(old)); err != nil {
			return nil, err
		}
		if old.name == workflowStore.name {
			stated, err := compare(ctx, q, plan, old, statedCompared)
			if err != nil {
				return nil, err
			}
			taken.Stated = &stated
			if taken.OtherIssue, err = countHits(ctx, q, plan, old, otherIssue); err != nil {
				return nil, err
			}
			if taken.OtherProject, err = countHits(ctx, q, plan, old, otherProject); err != nil {
				return nil, err
			}
		}
		census.Stores = append(census.Stores, taken)
	}
	return census, nil
}

// comparison is one comparison of the census: old and moved each select the columns key, then
// compared, then seq, the old tuple's seq or the store's, or null for a row that has none; key
// places a row on its side, and its first two columns name the node the row is of.
type comparison struct {
	what, row     string
	old, moved    string
	key, compared []string
}

// compare runs one comparison (comparison) in q, as one statement: the two sides, each counted and
// digested, and the rows of either side that the other does not hold equal at the same key, of
// which it reads the first, by the old seq and then the store's.
func compare(ctx context.Context, q queryer, plan planner, old oldStore, c comparison) (Comparison, error) {
	joined := make([]string, len(c.key))
	for index, column := range c.key {
		joined[index] = "o." + column + " = m." + column
	}
	digested := strings.Join(append(append([]string(nil), c.key...), c.compared...), ", ")
	ordered := strings.Join(c.key, ", ")
	kind, id := c.key[0], c.key[1]
	query := `
		with old as (` + c.old + `),
		moved as (` + c.moved + `),
		differing as (
			select coalesce(o.` + kind + `, m.` + kind + `) as node_kind, coalesce(o.` + id + `, m.` + id + `) as node_id,
			       o.seq as old_seq, m.seq as moved_seq,
			       case when m.` + kind + ` is null then 'old' when o.` + kind + ` is null then 'moved' else 'both' end as side
			from old o full join moved m on ` + strings.Join(joined, " and ") + `
			where o.` + kind + ` is null or m.` + kind + ` is null
			   or (` + prefixed("o.", c.compared) + `) is distinct from (` + prefixed("m.", c.compared) + `)
		)
		select (select count(*) from old),
		       (select md5(coalesce(string_agg(row(` + digested + `)::text, E'\n' order by ` + ordered + `), '')) from old),
		       (select count(*) from moved),
		       (select md5(coalesce(string_agg(row(` + digested + `)::text, E'\n' order by ` + ordered + `), '')) from moved),
		       (select count(*) from differing),
		       first.node_kind, first.node_id, first.old_seq, first.moved_seq, first.side
		from (select 1) one
		left join lateral (
			select * from differing order by old_seq nulls last, moved_seq nulls last, node_kind, node_id limit 1
		) first on true`
	if plan != nil {
		if err := plan(ctx, old.name+" "+c.what, query); err != nil {
			return Comparison{}, err
		}
	}
	result := Comparison{What: c.what}
	var kindRead, idRead, side *string
	var oldSeq, movedSeq *int64
	if err := q.QueryRow(ctx, query).Scan(&result.Old.Rows, &result.Old.Digest, &result.Moved.Rows, &result.Moved.Digest,
		&result.Differences, &kindRead, &idRead, &oldSeq, &movedSeq, &side); err != nil {
		return Comparison{}, fmt.Errorf("the census before the drop: compare the %s of the %s store: %w", c.what, old.name, err)
	}
	if result.Differences > 0 && kindRead != nil && idRead != nil && side != nil {
		why := map[string]string{
			"old":   "an old " + c.row + " that the store holds no equal of",
			"moved": "a " + c.row + " of the store that holds no equal old " + c.row,
			"both":  "an old " + c.row + " and the store's at its place differ",
		}[*side]
		result.First = &Difference{Store: old.name, What: c.row, Kind: *kindRead, ID: *idRead, OldSeq: oldSeq, Seq: movedSeq, Why: why}
	}
	return result, nil
}

// prefixed is columns, each with prefix, joined as a list.
func prefixed(prefix string, columns []string) string {
	listed := make([]string, len(columns))
	for index, column := range columns {
		listed[index] = prefix + column
	}
	return strings.Join(listed, ", ")
}

// quoted is values as a list of SQL literals; each is a constant of this tree.
func quoted(values ...string) string {
	listed := make([]string, len(values))
	for index, value := range values {
		listed[index] = "'" + value + "'"
	}
	return strings.Join(listed, ", ")
}

// The node row columns the census compares, beside the key (kind, id).
var nodeCompared = []string{"supertag", "visibility", "state", "attributes", "made_at", "made_by", "source_event"}

// nodesCompared compares old's node table with the store's nodes of old's kinds. The old side reads
// the whole table, which no index narrows; graph_nodes_pkey, whose leading column is kind, reads the
// store's side.
func nodesCompared(old oldStore) comparison {
	columns := "kind, id, " + strings.Join(nodeCompared, ", ") + ", null::bigint as seq"
	return comparison{
		what: "node rows", row: "node row",
		old: `
			-- The whole of ` + old.nodes + `: no index narrows it.
			select ` + columns + ` from ` + old.nodes,
		moved: `
			-- graph_nodes_pkey, whose leading column is kind.
			select ` + columns + ` from graph_nodes where kind in (` + quoted(old.kinds...) + `)`,
		key:      []string{"kind", "id"},
		compared: nodeCompared,
	}
}

// The tuple columns the census compares, beside the key (from_kind, from_id, place).
var tupleCompared = []string{"kind", "to_kind", "to_id", "at", "who", "source_event", "asserted"}

// tupleColumns is a select's list of a tuple's columns as a comparison reads them: the key, with
// each tuple's place among its node's in the order of seq, the compared columns and the seq.
var tupleColumns = `from_kind, from_id, row_number() over (partition by from_kind, from_id order by seq) as place, ` +
	strings.Join(tupleCompared, ", ") + `, seq`

// tupleSelected is the columns of graph_tuples a scoped read of the store selects, as old's tuple
// table holds them.
const tupleSelected = `seq, from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted`

// tuplesCompared compares old's tuple table with the store's tuples of old's store. The old side
// reads the whole table, which no index narrows. The store's side reads the tuples out of a node of
// old's kinds through graph_tuples_as_of_out, whose leading column is from_kind, and, for the
// priority store, the advances tuples into a priority through graph_tuples_as_of_in, whose leading
// columns are to_kind and to_id, partial on to_kind <> 'value', which to_kind = 'priority' meets.
// The workflow store's side leaves out the values the copy states of each run and plan
// (statedCompared reads them).
func tuplesCompared(old oldStore) comparison {
	moved := `
			-- graph_tuples_as_of_out, whose leading column is from_kind.
			select ` + tupleColumns + ` from graph_tuples g
			where g.from_kind in (` + quoted(old.kinds...) + `) and not (` + statedByTheCopy("g") + `)`
	if old.name == priorityStore.name {
		moved = `
			select ` + tupleColumns + ` from (
				-- graph_tuples_as_of_out, whose leading column is from_kind.
				select ` + tupleSelected + ` from graph_tuples
				where from_kind = '` + prioritystore.KindPriority + `'
				union all
				-- graph_tuples_as_of_in, whose leading columns are to_kind and to_id.
				select ` + tupleSelected + ` from graph_tuples
				where to_kind = '` + prioritystore.KindPriority + `' and kind = '` + prioritystore.RelationAdvances + `'
				  and from_kind <> '` + prioritystore.KindPriority + `'
			) scoped`
	}
	return comparison{
		what: "tuples", row: "tuple",
		old: `
			-- The whole of ` + old.tuples + `: no index narrows it.
			select ` + tupleColumns + ` from ` + old.tuples,
		moved:    moved,
		key:      []string{"from_kind", "from_id", "place"},
		compared: tupleCompared,
	}
}

// statedByTheCopy is the condition that the tuple alias names, out of a run or a plan, is a value
// the copy states of it rather than one it moved: its visibility or its state, which no old tuple
// holds, or its project where its old row holds no project attribute. It reads the old row through
// workflow_nodes_pkey.
func statedByTheCopy(alias string) string {
	return alias + `.to_kind = '` + graphstore.KindValue + `' and (` +
		alias + `.kind in (` + quoted(graphstore.AttributeVisibility, graphstore.AttributeState) + `)
		or ` + alias + `.kind = '` + graphstore.AttributeProject + `' and exists (
			select 1 from workflow_nodes w
			where w.kind = ` + alias + `.from_kind and w.id = ` + alias + `.from_id and not w.attributes ? '` + workflowstore.AttributeProject + `'))`
}

// statedCompared compares the values the copy states of each run and plan with the old rows they
// are stated from: the visibility and the state of each row, and the project of its issue where it
// holds none, read through issues_pkey, each dated at the row's made_at, by its made_by, under its
// cause, once. The old side reads the whole of workflow_nodes; graph_tuples_values, partial on
// to_kind = 'value', whose leading columns are from_kind, from_id and kind, reads the store's.
var statedCompared = comparison{
	what: "stated values", row: "stated value",
	old: `
		-- The whole of workflow_nodes, which no index narrows; issues_pkey reads each one's issue.
		select w.kind as from_kind, w.id as from_id, stated.kind, 1::bigint as place,
		       stated.value as to_id, w.made_at as at, w.made_by as who, w.source_event, null::bigint as seq
		from workflow_nodes w
		left join issues i on i.key = w.attributes ->> '` + workflowstore.AttributeIssue + `'
		cross join lateral (values
			('` + graphstore.AttributeVisibility + `', w.visibility),
			('` + graphstore.AttributeState + `', w.state),
			('` + graphstore.AttributeProject + `', i.project_key)
		) stated(kind, value)
		where stated.kind <> '` + graphstore.AttributeProject + `' or not w.attributes ? '` + workflowstore.AttributeProject + `'`,
	moved: `
		-- graph_tuples_values, whose leading columns are from_kind, from_id and kind.
		select g.from_kind, g.from_id, g.kind,
		       row_number() over (partition by g.from_kind, g.from_id, g.kind order by g.seq) as place,
		       g.to_id, g.at, g.who, g.source_event, g.seq
		from graph_tuples g
		where g.from_kind in (` + quoted(workflowStore.kinds...) + `) and ` + statedByTheCopy("g"),
	key:      []string{"from_kind", "from_id", "kind", "place"},
	compared: []string{"to_id", "at", "who", "source_event"},
}

// counted is one count of the census: hits selects the kind and the id of each node it counts.
type counted struct {
	what, why, hits string
}

// otherIssue counts the runs and plans whose issue attribute names another issue than their newest
// part_of tuple, or whose newest part_of is no assertion into an issue. graph_nodes_pkey reads the
// runs and plans by their kind, and graph_tuples_as_of_out, whose columns are from_kind, from_id,
// kind, at desc and seq desc, reads each one's newest part_of in its own order.
var otherIssue = counted{
	what: "runs and plans whose issue attribute names another issue than their part_of",
	why:  "its issue attribute names another issue than its part_of",
	hits: `
		-- graph_nodes_pkey, and graph_tuples_as_of_out for each node's newest part_of.
		select n.kind, n.id from graph_nodes n
		where n.kind in (` + quoted(workflowStore.kinds...) + `)
		  and (n.attributes ->> '` + workflowstore.AttributeIssue + `') is distinct from (
			select case when t.asserted and t.to_kind = '` + graphstore.KindIssue + `' then t.to_id end
			from graph_tuples t
			where t.from_kind = n.kind and t.from_id = n.id and t.kind = '` + graphstore.TuplePartOf + `'
			order by t.at desc, t.seq desc
			limit 1)`,
}

// otherProject counts the runs and plans whose project_key is null or names another project than
// their issue's. graph_nodes_pkey reads the runs and plans by their kind, and issues_pkey each one's
// issue.
var otherProject = counted{
	what: "runs and plans whose project_key is null or names another project than their issue's",
	why:  "its project_key is null or names another project than its issue's",
	hits: `
		-- graph_nodes_pkey, and issues_pkey for each node's issue.
		select n.kind, n.id from graph_nodes n
		left join issues i on i.key = n.attributes ->> '` + workflowstore.AttributeIssue + `'
		where n.kind in (` + quoted(workflowStore.kinds...) + `)
		  and (n.project_key is null or n.project_key is distinct from i.project_key)`,
}

// countHits runs one count of the census in q, handing plan its statement first: how many nodes it
// counts, and the first by kind and id.
func countHits(ctx context.Context, q queryer, plan planner, old oldStore, c counted) (*Count, error) {
	query := `
		with hits as (` + c.hits + `)
		select (select count(*) from hits), first.kind, first.id
		from (select 1) one
		left join lateral (select * from hits order by kind, id limit 1) first on true`
	if plan != nil {
		if err := plan(ctx, old.name+" "+c.what, query); err != nil {
			return nil, err
		}
	}
	result := &Count{What: c.what}
	var kind, id *string
	if err := q.QueryRow(ctx, query).Scan(&result.Rows, &kind, &id); err != nil {
		return nil, fmt.Errorf("the census before the drop: count the %s: %w", c.what, err)
	}
	if result.Rows > 0 && kind != nil && id != nil {
		result.First = &Difference{Store: old.name, What: "node", Kind: *kind, ID: *id, Why: c.why}
	}
	return result, nil
}
