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
--   `base` and `removes`;
-- - the block type `conclusion`, holding paragraphs (`paragraph+`), its body a change's evidence
--   and outcomes (D6 of the work-units plan), with `statement` (the approved statement it
--   concludes about), `difference` (an enum of files_differ, code_breaks, check_wrong and
--   statement_wrong) and `no_statement` (a bool).
--
-- It also adds three supertags the seed does not hold: `conclusion`, the block type above, and
-- the two supertags of the approved spec, which declare no block type: `spec_node`, a node of The
-- Spec (internal/dispatch/specstore), and `spec_import_hold`, a statement of an approved level its
-- import could not resolve.
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
-- change when the file does. What it leaves is what the seed would build from the file as it
-- stands at version 7, and TestEveryBlockTypeIsASupertagNodeAndValidationReadsIt holds the two
-- together field by field. Every value a row carries is also stated as the tuple it is, a `value`
-- tuple whose kind is the attribute's name and whose far end is its value as text, since a node
-- row's attributes column is the projection of its tuples (R1).
--
-- The runner applies a migration in one transaction, so the commit is the one moment the schema
-- changes, and the triggers of 20261109051232 announce it once, at that commit, so every running
-- server installs the new types and the new version together.
--
-- Every statement is one of the two shapes the pre-deploy census admits on a table past
-- pgmigrate.CensusTableLimit, which graph_nodes and graph_tuples can be once the node store's
-- backfill has filled them: an `insert … values` of literal rows, and an `update` that sets
-- columns to literals on the rows its `where` names by their primary key, `(kind, id)`
-- (20261109013400), in literal values. So no statement reads a row to write one: each node and
-- each tuple is written out below as the row it is, a tuple states the value the node row above
-- it carries, and an update sets a node's whole `attributes`, the value the node held before this
-- migration with this migration's keys in it. Every row carries the migration's own stamp,
-- 2026-11-22 06:01:00 UTC, as the moment it was made or stated, rather than the moment it runs,
-- so the file states every value it writes and writes the same rows on every store.
--
-- The runner applies each version once and records it, so nothing here guards a second run: run
-- again, the `insert` into graph_nodes meets the primary key of a row the first run wrote, the
-- statement fails and the file's transaction rolls back, leaving the store as it was. The
-- migration records no event, so every tuple it writes carries the `source_event` sentinel 0,
-- and its rows carry a writer of their own, so a reader of the store can tell them from the
-- seed's.

-- The supertag node `component` becomes a block type: what the seed builds for a type of the file,
-- its content rule, who draws it, the schema's version and its place among the types (callout,
-- ask, requirement, plan_task, check, decision, reading and criterion hold 0 to 7, so component
-- holds 8), with its element versions. The seed gave it no attribute (20261109051232), so the value
-- below is the whole of what the node carries. The node is changed in place and keeps the
-- `made_at` and `made_by` the seed gave it.
update graph_nodes
set attributes = '{"block_type": true, "content": "paragraph+", "render": "host", "version": 7, "position": 8, "since": 7, "creatable_since": 7}'::jsonb
where (kind, id) in (('supertag', 'component'));

-- The three supertags this change adds. `conclusion` is a block type, written as the seed writes
-- the supertag of a type of the file: its content rule, who draws it, the schema's version, its
-- place among the types (component holds 8, so conclusion holds 9) and its element versions. The
-- two supertags of the approved spec are written as the seed writes a supertag that declares no
-- block type: its node, carrying no attribute. Then the attribute nodes. Each attribute node's id
-- is `<supertag>/<store name>`, and its attributes are what the seed builds for an attribute of
-- the file: the attribute's own JSON, with its name, its store name and its position beside it.
-- None is named for a base attribute, so each store name is its own name
-- (pmdoc/schemanodes.StoreName). A position is the attribute's place among its type's attributes,
-- counted from 0: requirement holds two before kind, check seven before statement. The store holds
-- none of the twenty-seven before this runs.
insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
values
  ('supertag', 'conclusion', 'supertag', 'project', 'live',
   '{"block_type": true, "content": "paragraph+", "render": "host", "version": 7, "position": 9, "since": 7, "creatable_since": 7}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('supertag', 'spec_import_hold', 'supertag', 'project', 'live', '{}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('supertag', 'spec_node', 'supertag', 'project', 'live', '{}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'conclusion/statement', 'attribute', 'project', 'live',
   '{"kind": "string", "since": 7, "creatable_since": 7, "name": "statement", "store_name": "statement", "position": 0}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'conclusion/difference', 'attribute', 'project', 'live',
   '{"kind": "enum", "choices": ["files_differ", "code_breaks", "check_wrong", "statement_wrong"], "since": 7, "creatable_since": 7, "name": "difference", "store_name": "difference", "position": 1}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'conclusion/no_statement', 'attribute', 'project', 'live',
   '{"kind": "bool", "since": 7, "creatable_since": 7, "name": "no_statement", "store_name": "no_statement", "position": 2}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'requirement/kind', 'attribute', 'project', 'live',
   '{"kind": "enum", "choices": ["behavior", "structure", "property"], "since": 7, "creatable_since": 7, "name": "kind", "store_name": "kind", "position": 2}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'requirement/under', 'attribute', 'project', 'live',
   '{"kind": "string", "since": 7, "creatable_since": 7, "name": "under", "store_name": "under", "position": 3}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'requirement/supports', 'attribute', 'project', 'live',
   '{"kind": "string", "since": 7, "creatable_since": 7, "name": "supports", "store_name": "supports", "position": 4}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'requirement/about', 'attribute', 'project', 'live',
   '{"kind": "string", "since": 7, "creatable_since": 7, "name": "about", "store_name": "about", "position": 5}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'requirement/base', 'attribute', 'project', 'live',
   '{"kind": "string", "since": 7, "creatable_since": 7, "name": "base", "store_name": "base", "position": 6}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'requirement/removes', 'attribute', 'project', 'live',
   '{"kind": "bool", "since": 7, "creatable_since": 7, "name": "removes", "store_name": "removes", "position": 7}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'check/statement', 'attribute', 'project', 'live',
   '{"kind": "string", "since": 7, "creatable_since": 7, "name": "statement", "store_name": "statement", "position": 7}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'check/subject', 'attribute', 'project', 'live',
   '{"kind": "enum", "choices": ["code", "production", "graph"], "since": 7, "creatable_since": 7, "name": "subject", "store_name": "subject", "position": 8}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'check/procedure', 'attribute', 'project', 'live',
   '{"kind": "enum", "choices": ["program", "query", "agent"], "since": 7, "creatable_since": 7, "name": "procedure", "store_name": "procedure", "position": 9}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'check/runner', 'attribute', 'project', 'live',
   '{"kind": "string", "since": 7, "creatable_since": 7, "name": "runner", "store_name": "runner", "position": 10}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'check/selector', 'attribute', 'project', 'live',
   '{"kind": "string", "since": 7, "creatable_since": 7, "name": "selector", "store_name": "selector", "position": 11}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'check/job', 'attribute', 'project', 'live',
   '{"kind": "string", "since": 7, "creatable_since": 7, "name": "job", "store_name": "job", "position": 12}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'check/time_limit', 'attribute', 'project', 'live',
   '{"kind": "string", "since": 7, "creatable_since": 7, "name": "time_limit", "store_name": "time_limit", "position": 13}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'check/base', 'attribute', 'project', 'live',
   '{"kind": "string", "since": 7, "creatable_since": 7, "name": "base", "store_name": "base", "position": 14}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'check/removes', 'attribute', 'project', 'live',
   '{"kind": "bool", "since": 7, "creatable_since": 7, "name": "removes", "store_name": "removes", "position": 15}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'component/name', 'attribute', 'project', 'live',
   '{"kind": "string", "since": 7, "creatable_since": 7, "name": "name", "store_name": "name", "position": 0}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'component/kind', 'attribute', 'project', 'live',
   '{"kind": "enum", "choices": ["interface"], "since": 7, "creatable_since": 7, "name": "kind", "store_name": "kind", "position": 1}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'component/under', 'attribute', 'project', 'live',
   '{"kind": "string", "since": 7, "creatable_since": 7, "name": "under", "store_name": "under", "position": 2}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'component/joins', 'attribute', 'project', 'live',
   '{"kind": "string", "since": 7, "creatable_since": 7, "name": "joins", "store_name": "joins", "position": 3}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'component/base', 'attribute', 'project', 'live',
   '{"kind": "string", "since": 7, "creatable_since": 7, "name": "base", "store_name": "base", "position": 4}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0),
  ('attribute', 'component/removes', 'attribute', 'project', 'live',
   '{"kind": "bool", "since": 7, "creatable_since": 7, "name": "removes", "store_name": "removes", "position": 5}',
   '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0);

-- The two base attributes each new node carries, as the tuples they are, as the seed states them:
-- at the time and by the writer of the node's row.
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
values
  ('attribute', 'conclusion/difference', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/difference', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/no_statement', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/no_statement', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/statement', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/statement', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'conclusion', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'conclusion', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/base', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/base', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/job', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/job', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/procedure', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/procedure', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/removes', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/removes', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/runner', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/runner', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/selector', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/selector', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/statement', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/statement', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/subject', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/subject', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/time_limit', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/time_limit', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/base', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/base', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/joins', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/joins', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/kind', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/kind', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/name', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/name', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/removes', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/removes', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/under', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/under', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/about', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/about', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/base', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/base', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/kind', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/kind', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/removes', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/removes', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/supports', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/supports', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/under', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/under', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'spec_import_hold', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'spec_import_hold', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'spec_node', 'state', 'value', 'live', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'spec_node', 'visibility', 'value', 'project', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true);

-- What ties each attribute to its type. A reader of the schema follows this tuple, never the
-- spelling of the node's id, so without it a node is no attribute of any type.
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
values
  ('attribute', 'conclusion/difference', 'attribute_of', 'supertag', 'conclusion', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/no_statement', 'attribute_of', 'supertag', 'conclusion', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/statement', 'attribute_of', 'supertag', 'conclusion', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/base', 'attribute_of', 'supertag', 'check', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/job', 'attribute_of', 'supertag', 'check', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/procedure', 'attribute_of', 'supertag', 'check', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/removes', 'attribute_of', 'supertag', 'check', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/runner', 'attribute_of', 'supertag', 'check', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/selector', 'attribute_of', 'supertag', 'check', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/statement', 'attribute_of', 'supertag', 'check', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/subject', 'attribute_of', 'supertag', 'check', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/time_limit', 'attribute_of', 'supertag', 'check', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/base', 'attribute_of', 'supertag', 'component', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/joins', 'attribute_of', 'supertag', 'component', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/kind', 'attribute_of', 'supertag', 'component', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/name', 'attribute_of', 'supertag', 'component', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/removes', 'attribute_of', 'supertag', 'component', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/under', 'attribute_of', 'supertag', 'component', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/about', 'attribute_of', 'supertag', 'requirement', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/base', 'attribute_of', 'supertag', 'requirement', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/kind', 'attribute_of', 'supertag', 'requirement', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/removes', 'attribute_of', 'supertag', 'requirement', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/supports', 'attribute_of', 'supertag', 'requirement', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/under', 'attribute_of', 'supertag', 'requirement', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true);

-- The values each node this migration writes or moves carries, as the tuples they are, as the
-- seed states every type node's: a value the row holds as JSON is written as that JSON's text and
-- a string as itself, so `version` is `7` and `choices` is `["code", "production", "graph"]`. The
-- supertag node `component`, to which the seed gave no attribute, and the new supertag node
-- `conclusion` each state every value above.
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
values
  ('attribute', 'conclusion/difference', 'choices', 'value', '["files_differ", "code_breaks", "check_wrong", "statement_wrong"]', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/difference', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/difference', 'kind', 'value', 'enum', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/difference', 'name', 'value', 'difference', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/difference', 'position', 'value', '1', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/difference', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/difference', 'store_name', 'value', 'difference', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/no_statement', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/no_statement', 'kind', 'value', 'bool', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/no_statement', 'name', 'value', 'no_statement', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/no_statement', 'position', 'value', '2', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/no_statement', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/no_statement', 'store_name', 'value', 'no_statement', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/statement', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/statement', 'kind', 'value', 'string', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/statement', 'name', 'value', 'statement', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/statement', 'position', 'value', '0', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/statement', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'conclusion/statement', 'store_name', 'value', 'statement', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/base', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/base', 'kind', 'value', 'string', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/base', 'name', 'value', 'base', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/base', 'position', 'value', '14', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/base', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/base', 'store_name', 'value', 'base', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/job', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/job', 'kind', 'value', 'string', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/job', 'name', 'value', 'job', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/job', 'position', 'value', '12', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/job', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/job', 'store_name', 'value', 'job', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/procedure', 'choices', 'value', '["program", "query", "agent"]', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/procedure', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/procedure', 'kind', 'value', 'enum', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/procedure', 'name', 'value', 'procedure', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/procedure', 'position', 'value', '9', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/procedure', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/procedure', 'store_name', 'value', 'procedure', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/removes', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/removes', 'kind', 'value', 'bool', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/removes', 'name', 'value', 'removes', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/removes', 'position', 'value', '15', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/removes', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/removes', 'store_name', 'value', 'removes', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/runner', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/runner', 'kind', 'value', 'string', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/runner', 'name', 'value', 'runner', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/runner', 'position', 'value', '10', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/runner', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/runner', 'store_name', 'value', 'runner', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/selector', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/selector', 'kind', 'value', 'string', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/selector', 'name', 'value', 'selector', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/selector', 'position', 'value', '11', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/selector', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/selector', 'store_name', 'value', 'selector', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/statement', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/statement', 'kind', 'value', 'string', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/statement', 'name', 'value', 'statement', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/statement', 'position', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/statement', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/statement', 'store_name', 'value', 'statement', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/subject', 'choices', 'value', '["code", "production", "graph"]', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/subject', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/subject', 'kind', 'value', 'enum', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/subject', 'name', 'value', 'subject', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/subject', 'position', 'value', '8', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/subject', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/subject', 'store_name', 'value', 'subject', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/time_limit', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/time_limit', 'kind', 'value', 'string', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/time_limit', 'name', 'value', 'time_limit', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/time_limit', 'position', 'value', '13', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/time_limit', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'check/time_limit', 'store_name', 'value', 'time_limit', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/base', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/base', 'kind', 'value', 'string', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/base', 'name', 'value', 'base', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/base', 'position', 'value', '4', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/base', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/base', 'store_name', 'value', 'base', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/joins', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/joins', 'kind', 'value', 'string', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/joins', 'name', 'value', 'joins', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/joins', 'position', 'value', '3', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/joins', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/joins', 'store_name', 'value', 'joins', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/kind', 'choices', 'value', '["interface"]', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/kind', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/kind', 'kind', 'value', 'enum', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/kind', 'name', 'value', 'kind', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/kind', 'position', 'value', '1', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/kind', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/kind', 'store_name', 'value', 'kind', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/name', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/name', 'kind', 'value', 'string', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/name', 'name', 'value', 'name', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/name', 'position', 'value', '0', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/name', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/name', 'store_name', 'value', 'name', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/removes', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/removes', 'kind', 'value', 'bool', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/removes', 'name', 'value', 'removes', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/removes', 'position', 'value', '5', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/removes', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/removes', 'store_name', 'value', 'removes', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/under', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/under', 'kind', 'value', 'string', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/under', 'name', 'value', 'under', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/under', 'position', 'value', '2', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/under', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'component/under', 'store_name', 'value', 'under', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/about', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/about', 'kind', 'value', 'string', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/about', 'name', 'value', 'about', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/about', 'position', 'value', '5', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/about', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/about', 'store_name', 'value', 'about', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/base', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/base', 'kind', 'value', 'string', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/base', 'name', 'value', 'base', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/base', 'position', 'value', '6', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/base', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/base', 'store_name', 'value', 'base', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/kind', 'choices', 'value', '["behavior", "structure", "property"]', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/kind', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/kind', 'kind', 'value', 'enum', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/kind', 'name', 'value', 'kind', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/kind', 'position', 'value', '2', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/kind', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/kind', 'store_name', 'value', 'kind', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/removes', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/removes', 'kind', 'value', 'bool', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/removes', 'name', 'value', 'removes', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/removes', 'position', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/removes', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/removes', 'store_name', 'value', 'removes', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/supports', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/supports', 'kind', 'value', 'string', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/supports', 'name', 'value', 'supports', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/supports', 'position', 'value', '4', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/supports', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/supports', 'store_name', 'value', 'supports', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/under', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/under', 'kind', 'value', 'string', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/under', 'name', 'value', 'under', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/under', 'position', 'value', '3', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/under', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('attribute', 'requirement/under', 'store_name', 'value', 'under', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'component', 'block_type', 'value', 'true', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'component', 'content', 'value', 'paragraph+', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'component', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'component', 'position', 'value', '8', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'component', 'render', 'value', 'host', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'component', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'component', 'version', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'conclusion', 'block_type', 'value', 'true', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'conclusion', 'content', 'value', 'paragraph+', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'conclusion', 'creatable_since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'conclusion', 'position', 'value', '9', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'conclusion', 'render', 'value', 'host', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'conclusion', 'since', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'conclusion', 'version', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true);

-- The version. Every block type's supertag node carries the version of the schema the store
-- holds, and the schema the nodes build takes the highest of them, so each one moves to 7, not
-- the new type alone. A supertag that declares no block type carries no version and is left
-- alone. The block types the store holds at this version are the eight below, and `component`
-- and `conclusion`, which took 7 with their values above. Each of the eight carries what
-- 20261116082319 gave it at version 5, or, for criterion, what 20261116163700 gave it at version 6,
-- now at version 7. No two carry the same value, so each is a statement of its own.
update graph_nodes
set attributes = '{"block_type": true, "content": "block+", "render": "host", "version": 7, "position": 0}'::jsonb
where (kind, id) in (('supertag', 'callout'));

update graph_nodes
set attributes = '{"block_type": true, "content": "paragraph+ bullet_list?", "render": "host", "version": 7, "position": 1}'::jsonb
where (kind, id) in (('supertag', 'ask'));

update graph_nodes
set attributes = '{"block_type": true, "content": "paragraph+", "render": "host", "version": 7, "position": 2, "since": 3, "creatable_since": 3}'::jsonb
where (kind, id) in (('supertag', 'requirement'));

update graph_nodes
set attributes = '{"block_type": true, "content": "paragraph+", "render": "host", "version": 7, "position": 3, "since": 3, "creatable_since": 3}'::jsonb
where (kind, id) in (('supertag', 'plan_task'));

update graph_nodes
set attributes = '{"block_type": true, "content": "paragraph+", "render": "host", "version": 7, "position": 4, "since": 3, "creatable_since": 3}'::jsonb
where (kind, id) in (('supertag', 'check'));

update graph_nodes
set attributes = '{"block_type": true, "content": "block+", "render": "host", "version": 7, "position": 5, "since": 5, "creatable_since": 5}'::jsonb
where (kind, id) in (('supertag', 'decision'));

update graph_nodes
set attributes = '{"block_type": true, "content": "paragraph+", "render": "host", "version": 7, "position": 6, "since": 5, "creatable_since": 5, "parents": ["decision", "ask"]}'::jsonb
where (kind, id) in (('supertag', 'reading'));

update graph_nodes
set attributes = '{"block_type": true, "content": "paragraph+", "render": "host", "version": 7, "position": 7, "since": 6, "creatable_since": 6}'::jsonb
where (kind, id) in (('supertag', 'criterion'));

-- The move, as the tuple it is: a moved attribute is a later value appended under its name, so
-- each block type's newest `version` tuple is the 7 its row now carries and the 6 before it stays
-- in its history. `component` and `conclusion` state their 7 among their values above.
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
values
  ('supertag', 'ask', 'version', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'callout', 'version', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'check', 'version', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'criterion', 'version', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'decision', 'version', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'plan_task', 'version', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'reading', 'version', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true),
  ('supertag', 'requirement', 'version', 'value', '7', '2026-11-22 06:01:00+00', '{"kind": "system", "id": "block-schema-spec-change"}', 0, null, true);
