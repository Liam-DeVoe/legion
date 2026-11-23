-- 20261123080000_graph_supertags_plan_task_steps.up.sql
--
-- The block schema moves to version 5. `pmdoc/schema/blocks.json` on main gave the `plan_task`
-- type a third attribute, `steps`, a string declared after `needs` that a build reads and creates
-- from version 5, and raised the file's version from 4 to 5 (#4611: the steps an owner's plan
-- task runs, in order). A type is a node of the store, and the server installs the schema it
-- builds from those nodes, not the file, so the change reaches a store only as a migration: a type
-- changes by a migration, in the release of the binary that handles its version (20261109051232).
-- Without this one a binary built for version 5 installs version 4, whose `plan_task` declares no
-- `steps`, so a plan task that names its steps is refused as markdown outside the schema, and the
-- daemon's register_plan reads no plan that picks its steps.
--
-- It ships in the release that creates graph_nodes and graph_tuples (20261109013400), so it runs
-- before that release's backfill (`backfill-graph-store`) fills graph_tuples, as 20261113230000
-- and 20261115130000 do. It is one of the type migrations written before the store's backfill and
-- takes 20261113230000's shape: cmd/dispatch's TestEveryBlockTypeIsASupertagNodeAndValidationReadsIt
-- runs it a second time after them (typeMigrations).
--
-- The change is stated here as rows rather than read from the file, for the reason the seed
-- copies its text in: a migration states what the database held at one version and must not
-- change when the file does. What it leaves is what the seed would build from the file as it
-- stands at version 5, and TestEveryBlockTypeIsASupertagNodeAndValidationReadsIt holds the two
-- together field by field.
--
-- The runner applies a migration in one transaction, so the commit is the one moment the schema
-- changes: a server reads the new attribute and the new version together, or neither. The
-- triggers of 20261109051232 announce the change once, at that commit, however many rows it
-- writes, and every running server installs the schema the nodes then build.
--
-- Running this file again writes the same rows, as running the seed again does: the node upsert
-- moves every column but `made_at` and `made_by`, each tuple insert appends only a fact the node
-- does not already carry, and the version is set to the value it already holds. The migration
-- records no event, so every row it writes carries the `source_event` sentinel 0, and its rows
-- carry a writer of their own, so a reader of the store can tell them from the seed's.

-- The attribute node. Its id is `<supertag>/<store name>`, and its attributes are what the seed
-- builds for an attribute of the file: the attribute's own JSON, `{"kind": "string", "since": 5,
-- "creatable_since": 5}`, with its name, its store name and its position beside it. `steps` is the
-- name of no base attribute, so its store name is its own name
-- (pmdoc/schemanodes/schemanodestest.StoreName). The position is its place among the type's
-- attributes, counted from 0: serves and needs hold 0 and 1, so steps holds 2.
with change as (
  select now() as at, '{"kind": "system", "id": "block-schema-plan-task-steps"}'::jsonb as writer
)
insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
select 'attribute', 'plan_task/steps', 'attribute', 'project', 'live',
       '{"kind": "string", "since": 5, "creatable_since": 5}'::jsonb
         || jsonb_build_object('name', 'steps', 'store_name', 'steps', 'position', 2),
       change.at, change.writer, 0
from change
on conflict (kind, id) do update set
  supertag = excluded.supertag,
  visibility = excluded.visibility,
  state = excluded.state,
  attributes = excluded.attributes,
  source_event = excluded.source_event;

-- The two base attributes the node carries, as the tuples they are, read back off the row just
-- written, as the seed reads them.
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
select node.kind, node.id, fact.kind, 'value', fact.value, node.made_at, node.made_by, 0, null, true
from graph_nodes node
cross join lateral (values ('visibility', node.visibility), ('state', node.state)) as fact(kind, value)
where node.kind = 'attribute' and node.id = 'plan_task/steps'
  and not exists (
    select 1 from graph_tuples held
    where held.from_kind = node.kind and held.from_id = node.id and held.kind = fact.kind
      and held.to_kind = 'value' and held.to_id = fact.value
      and held.source_event = 0 and held.asserted
  )
order by fact.kind;

-- What ties the attribute to `plan_task`. A reader of the schema follows this tuple, never the
-- spelling of the node's id, so without it the node is no attribute of any type.
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
select node.kind, node.id, 'attribute_of', 'supertag', 'plan_task', node.made_at, node.made_by, 0, null, true
from graph_nodes node
where node.kind = 'attribute' and node.id = 'plan_task/steps'
  and not exists (
    select 1 from graph_tuples held
    where held.from_kind = node.kind and held.from_id = node.id and held.kind = 'attribute_of'
      and held.to_kind = 'supertag' and held.to_id = 'plan_task'
      and held.source_event = 0 and held.asserted
  );

-- The values the node carries, as the tuples they are, read back off the row just written as the
-- seed reads every type node's: a `value` tuple whose kind is the attribute's name and whose far
-- end is its value as text, at the `source_event` sentinel.
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
select node.kind, node.id, fact.kind, 'value', fact.value, node.made_at, node.made_by, 0, null, true
from graph_nodes node
cross join lateral (
  select attribute.key, attribute.value #>> '{}' from jsonb_each(node.attributes) as attribute
) as fact(kind, value)
where node.kind = 'attribute' and node.id = 'plan_task/steps'
  and not exists (
    select 1 from graph_tuples held
    where held.from_kind = node.kind and held.from_id = node.id and held.kind = fact.kind
      and held.to_kind = 'value' and held.to_id = fact.value
      and held.source_event = 0 and held.asserted
  )
order by fact.kind;

-- The version. The seed wrote the schema's version on every block type's supertag node, and the
-- schema the nodes build takes the highest of them. Each one moves to 5, not `plan_task` alone, so
-- every type node goes on carrying the version of the schema the store holds, as 20261113230000
-- left them. A supertag that declares no block type carries no version and is left alone.
update graph_nodes
set attributes = jsonb_set(attributes, '{version}', to_jsonb(5))
where kind = 'supertag' and coalesce((attributes->>'block_type')::boolean, false);

-- The move, as the tuple it is: a moved attribute is a later value appended under its name, so
-- each block type's newest `version` tuple is the 5 its row now carries and the 3 and the 4 before
-- it stay in its history. The tuples carry this migration's writer and its own time.
with change as (
  select now() as at, '{"kind": "system", "id": "block-schema-plan-task-steps"}'::jsonb as writer
)
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
select node.kind, node.id, 'version', 'value', node.attributes->>'version', change.at, change.writer, 0, null, true
from change, graph_nodes node
where node.kind = 'supertag' and coalesce((node.attributes->>'block_type')::boolean, false)
  and not exists (
    select 1 from graph_tuples held
    where held.from_kind = node.kind and held.from_id = node.id and held.kind = 'version'
      and held.to_kind = 'value' and held.to_id = node.attributes->>'version'
      and held.source_event = 0 and held.asserted
  )
order by node.id;
