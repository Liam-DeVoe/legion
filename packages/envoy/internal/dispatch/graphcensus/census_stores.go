package graphcensus

import (
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/prioritystore"
	"github.com/sjawhar/envoy/internal/dispatch/workflowstore"
)

// storesProjections are the census's kinds of the priority store and the workflow store, which
// dispatch://INFRA-1098's R32 moved into the graph store (projections): a priority, a run of a step
// and the plan it ran from, each held to the store's own record of it (movedKindCensus). A
// database where no priority is written and the daemon wrote no run yet is correct, so none refuses
// on zero rows.
var storesProjections = []projection{
	movedKindCensus(prioritystore.KindPriority, nil),
	movedKindCensus(workflowstore.KindStep, nil),
	movedKindCensus(workflowstore.KindPlan, []string{workflowstore.AttributeTasks}),
}

// movedKindCensus is the census of one kind the stores' move took into the graph store. No table
// projects it any more: the node row is the projection of the node's own value tuples, so the row
// side is the store's own record, for each node that states a value tuple, the newest tuple of
// each attribute, read as graphstore.CurrentValue reads one, against the node row of graph_nodes.
// The digest carries every attribute that is no base attribute, each by its name and the md5 of
// its value, then the visibility, the state and the project, which the row holds as its
// visibility, state and project_key columns. A node row with no tuple behind it is a node no row
// projects, a tuple whose node has no row is a row naming a node the store does not hold, and a
// row that differs from its newest tuples is a row and a node that disagree; a node whose newest
// state is gone is held to a row at gone as the other kinds are (presentNodes, rowDifferences). No
// value leaves the database.
//
// A value tuple holds its attribute's value as text, and the row's attributes hold it as JSON: a
// text as a string, a number as a number, so the row's text of either is its tuple's.
// jsonAttributes names the attributes whose value is JSON itself (a plan's tasks), which both sides
// read as jsonb, since the writer encodes the tuple's text compactly and jsonb prints it otherwise.
func movedKindCensus(kind string, jsonAttributes []string) projection {
	jsonValued := "false"
	if len(jsonAttributes) > 0 {
		jsonValued = "t.kind in ('" + strings.Join(jsonAttributes, "', '") + "')"
	}
	base := "'" + graphstore.AttributeVisibility + "', '" + graphstore.AttributeState + "', '" +
		graphstore.AttributeProject + "', '" + graphstore.AttributeParties + "'"
	digest := func(attributes, visibility, state, project string) string {
		return `md5('attributes:' || coalesce(` + attributes + `, '')
		       || ' ` + graphstore.AttributeVisibility + `:' || coalesce(md5(` + visibility + `), 'none')
		       || ' ` + graphstore.AttributeState + `:' || coalesce(md5(` + state + `), 'none')
		       || ' ` + graphstore.AttributeProject + `:' || coalesce(md5(` + project + `), 'none'))`
	}
	return projection{
		name:     kind,
		nodeKind: kind,
		table:    "graph_tuples",
		note: "no table projects a " + kind + " since the stores' move, so the row side is the store's own record of each" +
			" node, the newest value tuple of each attribute, against its graph_nodes row: every attribute that is no" +
			" base attribute against the row's attributes, and its visibility, state and project against the row's" +
			" columns; a row with no tuple, a tuple with no node row and a row that differs from its newest tuples are" +
			" each a difference",
		// graph_tuples_as_of_out serves the read of one kind's value tuples, with from_kind as its
		// index condition.
		rows: `
		select '` + kind + ` ' || held.id, '` + kind + `', held.id,
		       ` + digest("held.attributes", "held.visibility", "held.state", "held.project") + `
		from (
			select newest.from_id as id,
			       string_agg(newest.kind || '=' || md5(newest.value), ' ' order by newest.kind)
			           filter (where newest.kind not in (` + base + `) and newest.value is not null) as attributes,
			       max(newest.value) filter (where newest.kind = '` + graphstore.AttributeVisibility + `') as visibility,
			       max(newest.value) filter (where newest.kind = '` + graphstore.AttributeState + `') as state,
			       max(newest.value) filter (where newest.kind = '` + graphstore.AttributeProject + `') as project
			from (
				select distinct on (t.from_id, t.kind) t.from_id, t.kind,
				       case when not t.asserted then null
				            when ` + jsonValued + ` then (t.to_id::jsonb)::text
				            else t.to_id end as value
				from graph_tuples t
				where t.from_kind = '` + kind + `' and t.to_kind = '` + graphstore.KindValue + `'
				order by t.from_id, t.kind, t.seq desc
			) newest
			group by newest.from_id
		) held
		where held.state is distinct from '` + graphstore.StateGone + `'`,
		// graph_nodes_pkey serves the read of one kind's node rows, with kind as its index
		// condition.
		nodes: `
		select n.id, ` + digest(`(
			select string_agg(held.key || '=' || md5(case jsonb_typeof(held.value)
			                                            when 'string' then held.value #>> '{}'
			                                            else held.value::text end), ' ' order by held.key)
			from jsonb_each(n.attributes) held
			where held.key not in (`+base+`) and jsonb_typeof(held.value) <> 'null')`,
			"n.visibility", "n.state", "n.project_key") + `
		from graph_nodes n where n.kind = '` + kind + `'`,
		mayBeEmpty: true,
	}
}
