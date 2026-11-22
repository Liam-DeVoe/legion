-- 20261122060100_spec_change_block_schema.up.sql
--
-- The block schema moves to version 7, so an issue's blocks hold a change request of The Spec (D2
-- of the work-units plan; R22). `pmdoc/schema/blocks.json` adds, each read and created from
-- version 7, the attributes that hold the ids and fields of The Spec, apart from an issue's own
-- placement (`parent`) and its issue-local `serves`:
--
-- - on `requirement`: `kind` (an enum of behavior, structure and property), `under` (the
--   statement it stands under), `supports` (the statements it supports), `about`, `base` (the
--   approved version its writer read) and `removes` (a bool);
-- - on `check`: `statement`, `subject` (an enum of code, production and graph), `procedure` (an
--   enum of program, query and agent), `runner`, `selector`, `job`, `time_limit`, `base` and
--   `removes`;
-- - the block type `component`, holding paragraphs (`paragraph+`), its body a component's purpose,
--   with `name`, `kind` (an enum of interface alone, absent for a component), `under`, `joins`,
--   `base` and `removes`.
--
-- It also adds the two supertags of the approved spec, which declare no block type: `spec_node`,
-- a node of The Spec (internal/dispatch/specstore), and `spec_import_hold`, a statement of an
-- approved level its import could not resolve.
--
-- A type is a node of the store, and the server installs the schema it builds from those nodes,
-- not the file (20261109051232), so the change reaches a store only as this migration, in the
-- release of the binary that handles version 7. The seed already holds a `supertag` node for
-- `component`, which the closed list names and which the architecture's component nodes carry,
-- carrying no attribute: this makes it a block type as well. A block's node is of kind `block`, so
-- the two kinds of node that carry the supertag stay apart by their kind. A build of version 6
-- reads the store's schema at its own version (pmdoc.Install, asOfVersion), which leaves out every
-- element whose `since` is 7.
--
-- The change is stated here as rows rather than read from the file, for the reason the seed
-- copies its text in: a migration states what the database held at one version and must not
-- change when the file does. TestEveryBlockTypeIsASupertagNodeAndValidationReadsIt holds the two
-- together field by field. Every value a row carries is also stated as the tuple it is.
--
-- The runner applies a migration in one transaction, so the commit is the one moment the schema
-- changes, and the triggers of 20261109051232 announce it once, at that commit.
--
-- Running this file again writes the same rows: each node takes the values it already holds,
-- each tuple insert appends only a fact the node does not already carry, and the version is set
-- to the value it already holds. The migration records no event, so every tuple it writes carries
-- the `source_event` sentinel 0, and its rows carry a writer of their own.

-- The two supertags of the approved spec, written as the seed writes a supertag that declares no
-- block type: its node, carrying no attribute.
with change as (
  select now() as at, '{"kind": "system", "id": "block-schema-spec-change"}'::jsonb as writer
)
insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
select 'supertag', names.name, 'supertag', 'project', 'live', '{}'::jsonb, change.at, change.writer, 0
from change, (values ('spec_node'), ('spec_import_hold')) as names(name)
order by names.name
on conflict (kind, id) do update set
  supertag = excluded.supertag,
  visibility = excluded.visibility,
  state = excluded.state,
  attributes = excluded.attributes,
  source_event = excluded.source_event;

-- The supertag node `component` becomes a block type: what the seed builds for a type of the file,
-- its content rule, who draws it, the schema's version and its place among the types (callout,
-- ask, requirement, plan_task, check, decision, reading and criterion hold 0 to 7, so component
-- holds 8), with its element versions. The node is changed in place and keeps the `made_at` and
-- `made_by` the seed gave it.
update graph_nodes
set attributes = attributes || '{"block_type": true, "content": "paragraph+", "render": "host", "version": 7, "position": 8, "since": 7, "creatable_since": 7}'::jsonb
where kind = 'supertag' and id = 'component';

-- The attribute nodes. Each id is `<supertag>/<store name>`, and its attributes are what the seed
-- builds for an attribute of the file: the attribute's own JSON, with its name, its store name
-- and its position beside it. None is named for a base attribute, so each store name is its own
-- name (pmdoc/schemanodes.StoreName). A position is the attribute's place among its type's
-- attributes, counted from 0: requirement holds two before kind, check seven before statement.
with change as (
  select now() as at, '{"kind": "system", "id": "block-schema-spec-change"}'::jsonb as writer
)
insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
select 'attribute', attribute.type_name || '/' || attribute.name, 'attribute', 'project', 'live',
       attribute.schema || jsonb_build_object('name', attribute.name, 'store_name', attribute.name, 'position', attribute.position),
       change.at, change.writer, 0
from change, (values
  ('requirement', 'kind', 2, '{"kind": "enum", "choices": ["behavior", "structure", "property"], "since": 7, "creatable_since": 7}'::jsonb),
  ('requirement', 'under', 3, '{"kind": "string", "since": 7, "creatable_since": 7}'::jsonb),
  ('requirement', 'supports', 4, '{"kind": "string", "since": 7, "creatable_since": 7}'::jsonb),
  ('requirement', 'about', 5, '{"kind": "string", "since": 7, "creatable_since": 7}'::jsonb),
  ('requirement', 'base', 6, '{"kind": "string", "since": 7, "creatable_since": 7}'::jsonb),
  ('requirement', 'removes', 7, '{"kind": "bool", "since": 7, "creatable_since": 7}'::jsonb),
  ('check', 'statement', 7, '{"kind": "string", "since": 7, "creatable_since": 7}'::jsonb),
  ('check', 'subject', 8, '{"kind": "enum", "choices": ["code", "production", "graph"], "since": 7, "creatable_since": 7}'::jsonb),
  ('check', 'procedure', 9, '{"kind": "enum", "choices": ["program", "query", "agent"], "since": 7, "creatable_since": 7}'::jsonb),
  ('check', 'runner', 10, '{"kind": "string", "since": 7, "creatable_since": 7}'::jsonb),
  ('check', 'selector', 11, '{"kind": "string", "since": 7, "creatable_since": 7}'::jsonb),
  ('check', 'job', 12, '{"kind": "string", "since": 7, "creatable_since": 7}'::jsonb),
  ('check', 'time_limit', 13, '{"kind": "string", "since": 7, "creatable_since": 7}'::jsonb),
  ('check', 'base', 14, '{"kind": "string", "since": 7, "creatable_since": 7}'::jsonb),
  ('check', 'removes', 15, '{"kind": "bool", "since": 7, "creatable_since": 7}'::jsonb),
  ('component', 'name', 0, '{"kind": "string", "since": 7, "creatable_since": 7}'::jsonb),
  ('component', 'kind', 1, '{"kind": "enum", "choices": ["interface"], "since": 7, "creatable_since": 7}'::jsonb),
  ('component', 'under', 2, '{"kind": "string", "since": 7, "creatable_since": 7}'::jsonb),
  ('component', 'joins', 3, '{"kind": "string", "since": 7, "creatable_since": 7}'::jsonb),
  ('component', 'base', 4, '{"kind": "string", "since": 7, "creatable_since": 7}'::jsonb),
  ('component', 'removes', 5, '{"kind": "bool", "since": 7, "creatable_since": 7}'::jsonb)
) as attribute(type_name, name, position, schema)
order by attribute.type_name, attribute.name
on conflict (kind, id) do update set
  supertag = excluded.supertag,
  visibility = excluded.visibility,
  state = excluded.state,
  attributes = excluded.attributes,
  source_event = excluded.source_event;

-- The two base attributes each new node carries, as the tuples they are, read back off the rows
-- just written, as the seed reads them.
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
select node.kind, node.id, fact.kind, 'value', fact.value, node.made_at, node.made_by, 0, null, true
from graph_nodes node
cross join lateral (values ('visibility', node.visibility), ('state', node.state)) as fact(kind, value)
where ((node.kind = 'supertag' and node.id in ('spec_node', 'spec_import_hold'))
   or (node.kind = 'attribute' and node.id in (
     'requirement/kind', 'requirement/under', 'requirement/supports', 'requirement/about', 'requirement/base', 'requirement/removes',
     'check/statement', 'check/subject', 'check/procedure', 'check/runner', 'check/selector', 'check/job', 'check/time_limit',
     'check/base', 'check/removes',
     'component/name', 'component/kind', 'component/under', 'component/joins', 'component/base', 'component/removes')))
  and not exists (
    select 1 from graph_tuples held
    where held.from_kind = node.kind and held.from_id = node.id and held.kind = fact.kind
      and held.to_kind = 'value' and held.to_id = fact.value
      and held.source_event = 0 and held.asserted
  )
order by node.kind, node.id, fact.kind;

-- What ties each attribute to its type. A reader of the schema follows this tuple, never the
-- spelling of the node's id, so without it a node is no attribute of any type.
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
select node.kind, node.id, 'attribute_of', 'supertag', split_part(node.id, '/', 1), node.made_at, node.made_by, 0, null, true
from graph_nodes node
where node.kind = 'attribute'
  and node.id in (
    'requirement/kind', 'requirement/under', 'requirement/supports', 'requirement/about', 'requirement/base', 'requirement/removes',
    'check/statement', 'check/subject', 'check/procedure', 'check/runner', 'check/selector', 'check/job', 'check/time_limit',
    'check/base', 'check/removes',
    'component/name', 'component/kind', 'component/under', 'component/joins', 'component/base', 'component/removes')
  and not exists (
    select 1 from graph_tuples held
    where held.from_kind = node.kind and held.from_id = node.id and held.kind = 'attribute_of'
      and held.to_kind = 'supertag' and held.to_id = split_part(node.id, '/', 1)
      and held.source_event = 0 and held.asserted
  )
order by node.id;

-- The values each node this migration writes or moves carries, as the tuples they are, read back
-- off the rows just written as the seed reads every type node's: a value the row holds as JSON is
-- written as that JSON's text and a string as itself. A value a node already states as its tuple
-- is left as the seed wrote it.
with change as (
  select now() as at, '{"kind": "system", "id": "block-schema-spec-change"}'::jsonb as writer
)
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
select node.kind, node.id, fact.kind, 'value', fact.value, change.at, change.writer, 0, null, true
from change, graph_nodes node
cross join lateral (
  select attribute.key, attribute.value #>> '{}' from jsonb_each(node.attributes) as attribute
) as fact(kind, value)
where ((node.kind = 'supertag' and node.id = 'component')
   or (node.kind = 'attribute' and node.id in (
     'requirement/kind', 'requirement/under', 'requirement/supports', 'requirement/about', 'requirement/base', 'requirement/removes',
     'check/statement', 'check/subject', 'check/procedure', 'check/runner', 'check/selector', 'check/job', 'check/time_limit',
     'check/base', 'check/removes',
     'component/name', 'component/kind', 'component/under', 'component/joins', 'component/base', 'component/removes')))
  and not exists (
    select 1 from graph_tuples held
    where held.from_kind = node.kind and held.from_id = node.id and held.kind = fact.kind
      and held.to_kind = 'value' and held.to_id = fact.value
      and held.source_event = 0 and held.asserted
  )
order by node.kind, node.id, fact.kind;

-- The version. Every block type's supertag node carries the version of the schema the store
-- holds, and the schema the nodes build takes the highest of them, so each one moves to 7, not
-- the new type alone. A supertag that declares no block type carries no version and is left
-- alone.
update graph_nodes
set attributes = jsonb_set(attributes, '{version}', to_jsonb(7))
where kind = 'supertag' and coalesce((attributes->>'block_type')::boolean, false);

-- The move, as the tuple it is: each block type's newest `version` tuple is the 7 its row now
-- carries and the 6 before it stays in its history.
with change as (
  select now() as at, '{"kind": "system", "id": "block-schema-spec-change"}'::jsonb as writer
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
