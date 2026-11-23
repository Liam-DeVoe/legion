package graphstore

import "github.com/sjawhar/envoy/internal/dispatch/store"

// StoresMovedTriggers is the SQL that creates the two row triggers on graph_nodes, each calling
// the function of its own name, that complete a run or a plan the release before writes once the
// boot has moved the priority store and the workflow store into this store
// (internal/dispatch/storesmove, dispatch://INFRA-1098's R32). That release writes a run or a plan
// through the view workflow_nodes, whose row holds no project_key and whose visibility and state
// that release stated as no tuple, and it states every other fact of the node itself, through the
// view workflow_tuples:
//
//   - graph_nodes_workflow_state, after the insert of a step or plan node, states the node's
//     visibility and state as value tuples dated at its made_at, by its made_by, under its cause.
//     It runs after the insert, not before it, since a before-insert trigger also runs for a row
//     that an insert's `on conflict do nothing` then skips (INSERT, "ON CONFLICT Clause",
//     https://www.postgresql.org/docs/18/sql-insert.html), and that release's putNode inserts so
//     each write that may make the node;
//   - graph_nodes_workflow_project, before an update of a step or plan node that holds no
//     project_key, fills project_key from the node's project attribute, which that release's
//     putNode sets, together with the attribute's value tuple, in an update after its insert.
//
// Each acts only where the writing session's store.StoresMovedWriterSetting is not on: every
// connection this build opens carries it (store's markWriter), so the triggers pass over every
// write of this build, whose writers state these facts through this package's own writers. Their
// when clauses test both, so a write of any other node, and every write of this build, queues no
// call of either function.
//
// While the triggers stand, the writes of the release before through them and through the views
// are the one write of this store outside its writers here, kept for one release under R22's first
// condition; the boot step of the release after drops them once no build that a rollback reaches
// writes through the views.
const StoresMovedTriggers = `
-- kept for one release: graph-nodes-workflow-state-function
create function graph_nodes_workflow_state() returns trigger language plpgsql as $$
begin
  insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
  values (new.kind, new.id, '` + AttributeVisibility + `', '` + KindValue + `', new.visibility, new.made_at, new.made_by, new.source_event, true),
         (new.kind, new.id, '` + AttributeState + `', '` + KindValue + `', new.state, new.made_at, new.made_by, new.source_event, true);
  return null;
end
$$;
-- kept for one release: graph-nodes-workflow-state-trigger
create trigger graph_nodes_workflow_state after insert on graph_nodes
  for each row
  when (new.kind in ('step', 'plan') and current_setting('` + store.StoresMovedWriterSetting + `', true) is distinct from 'on')
  execute function graph_nodes_workflow_state();
-- kept for one release: graph-nodes-workflow-project-function
create function graph_nodes_workflow_project() returns trigger language plpgsql as $$
begin
  new.project_key := new.attributes ->> '` + AttributeProject + `';
  return new;
end
$$;
-- kept for one release: graph-nodes-workflow-project-trigger
create trigger graph_nodes_workflow_project before update on graph_nodes
  for each row
  when (new.kind in ('step', 'plan') and new.project_key is null
        and current_setting('` + store.StoresMovedWriterSetting + `', true) is distinct from 'on')
  execute function graph_nodes_workflow_project();
`
