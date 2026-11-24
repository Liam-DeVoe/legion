-- 20261123174800_issue_tree_block_schema.up.sql
--
-- The block schema moves to version 7 (dispatch://INFRA-1253/spec, R2, R3, R10, R11, R18 and
-- R21). `pmdoc/schema/blocks.json` adds, each read and created from version 7:
--
-- - the `criterion` block type, a person's statement of what they want to be able to do, holding
--   paragraphs (`paragraph+`) and carrying `parent`;
-- - `parent`, a `block[]` attribute holding at most one reference, whose `items` are ask, comment
--   and message and which names no `targets`, on `criterion`, `requirement`, `check` and
--   `plan_task`, after each type's other attributes: the one place a node's parent is written
--   where its rendering cannot give it;
-- - `requirement`'s `serves`, a `block[]` with the targets criterion and requirement;
-- - `plan_task`'s `size`, of the kind `days`, after `needs` and main's `steps`.
--
-- It also widens `plan_task`'s `needs`, which every version has read: its `scope` of `document`
-- goes, so it names a plan task of its issue or of an issue above it, and it gains `items` of
-- issue, so it names an issue under its issue as well, an item kind read and created from version
-- 7 (`item_versions`). A build of version 6 reads the store's schema at its own version
-- (pmdoc.Install, asOfVersion), which leaves out every element whose `since` is 7, and its decoder
-- ignores `items` and `item_versions`, so it reads `needs` naming no scope, which takes more than it
-- took. It would read an issue a `needs` names as a value outside the schema, not as newer content,
-- so a build of version 7 introduces one only while every process serving the database reads
-- version 7 (pmdoc.CheckBlockPointers under docs' schemaFleet.creatable), as a page of version 7
-- is writable only then.
--
-- A type is a node of the store, and the server installs the schema it builds from those nodes,
-- not the file (20261109051232), so the change reaches a store only as this migration, in the
-- release of the binary that handles version 7. The seed already holds a `supertag` node for
-- `criterion`, which the closed list names, carrying no attribute: this makes it a block type.
--
-- It is a type migration written after the store's backfill, as
-- 20261123080000_graph_supertags_plan_task_steps and 20261123174700_graph_supertags_decision_reading
-- are, and runs after both on every store, so it writes graph_nodes and graph_tuples by bounded
-- writes alone, the two shapes the pre-deploy census admits on a table past
-- pgmigrate.CensusTableLimit: an `insert … values` of literal rows, and an `update` that sets a
-- node's whole `attributes` to a literal on the row its `where` names by its primary key. No
-- statement reads a row to write one. Each update writes the value the node held after the type
-- migrations before this one with this migration's keys in it; nothing but a migration writes a
-- type node (cmd/dispatch's requireNoRouteOrCommandWritesAType), so that is what each holds.
-- cmd/dispatch's TestATypeMigrationAfterTheBackfillIsAdmittedAndLeavesTheFilesSchema runs this file
-- after a backfill, with the census at a one-byte limit.
--
-- The change is stated here as rows rather than read from the file, for the reason the seed
-- copies its text in: a migration states what the database held at one version and must not
-- change when the file does. What it leaves is what the seed would build from the file as it
-- stands at version 7, and TestEveryBlockTypeIsASupertagNodeAndValidationReadsIt holds the two
-- together field by field. Every value a row carries is also stated as the tuple it is, and the
-- value `needs` stops carrying is retracted, as the newest tuple of an attribute decides whether a
-- node holds it (graphstore.CurrentValue).
--
-- It runs once, as the runner applies every file: its inserts are not repeatable, so it joins no
-- second run (typeMigrations in cmd/dispatch/supertag_nodes_test.go). A second run all the same
-- stops at its first insert, on graph_nodes' primary key, and the runner's one transaction takes
-- back everything it wrote. Every row carries now(), the start of the runner's transaction, as the
-- rows of 20261123080000 and 20261123174700 do, so a reader at a moment (graphstore.AsOfOut) orders
-- these tuples after theirs. The migration records no event, so every tuple carries the
-- `source_event` sentinel 0, and its rows carry a writer of their own, so a reader of the store can
-- tell them from the seed's.

-- The supertag node becomes a block type: what the seed builds for a type of the file, its
-- content rule, who draws it, the schema's version and its place among the types (callout, ask,
-- requirement, plan_task, check, decision and reading hold 0 to 6, so criterion holds 7), with
-- its element versions. The seed gave it no attribute, so the value below is the whole of it.
update graph_nodes
set attributes = '{"block_type": true, "content": "paragraph+", "render": "host", "version": 7, "position": 7, "since": 7, "creatable_since": 7}'::jsonb
where kind = 'supertag' and id = 'criterion';

-- The attribute nodes. Each id is `<supertag>/<store name>`, and its attributes are what the seed
-- builds for an attribute of the file: the attribute's own JSON, with its name, its store name
-- and its position beside it. None is named for a base attribute, so each store name is its own
-- name (pmdoc/schemanodes.StoreName). A position is the attribute's place among its type's
-- attributes, counted from 0: check holds six before parent; plan_task holds serves, needs and
-- steps (20261123080000) before size, so size holds 3 and parent 4.
insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
values
  ('attribute', 'check/parent', 'attribute', 'project', 'live',
   '{"kind": "block[]", "items": ["ask", "comment", "message"], "name": "parent", "store_name": "parent", "position": 6, "since": 7, "creatable_since": 7}'::jsonb,
   now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0),
  ('attribute', 'criterion/parent', 'attribute', 'project', 'live',
   '{"kind": "block[]", "items": ["ask", "comment", "message"], "name": "parent", "store_name": "parent", "position": 0, "since": 7, "creatable_since": 7}'::jsonb,
   now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0),
  ('attribute', 'plan_task/parent', 'attribute', 'project', 'live',
   '{"kind": "block[]", "items": ["ask", "comment", "message"], "name": "parent", "store_name": "parent", "position": 4, "since": 7, "creatable_since": 7}'::jsonb,
   now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0),
  ('attribute', 'plan_task/size', 'attribute', 'project', 'live',
   '{"kind": "days", "name": "size", "store_name": "size", "position": 3, "since": 7, "creatable_since": 7}'::jsonb,
   now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0),
  ('attribute', 'requirement/parent', 'attribute', 'project', 'live',
   '{"kind": "block[]", "items": ["ask", "comment", "message"], "name": "parent", "store_name": "parent", "position": 1, "since": 7, "creatable_since": 7}'::jsonb,
   now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0),
  ('attribute', 'requirement/serves', 'attribute', 'project', 'live',
   '{"kind": "block[]", "targets": ["criterion", "requirement"], "name": "serves", "store_name": "serves", "position": 0, "since": 7, "creatable_since": 7}'::jsonb,
   now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0);

-- `plan_task`'s `needs` widens: its `scope` goes and it takes `items` of issue, read and created
-- from version 7. The whole value is written: what the seed gave the node, without `scope`, with
-- the two keys.
update graph_nodes
set attributes = '{"kind": "block[]", "items": ["issue"], "item_versions": {"issue": {"since": 7, "creatable_since": 7}}, "targets": ["plan_task"], "name": "needs", "store_name": "needs", "position": 1}'::jsonb
where kind = 'attribute' and id = 'plan_task/needs';

-- The version. Every block type's supertag node carries the version of the schema the store
-- holds, and the schema the nodes build takes the highest of them, so each one moves to 7, not
-- the new type alone: each is written whole, what 20261123080000 and 20261123174700 left on it with
-- its version moved from 6 to 7. A supertag that declares no block type carries no version and is
-- left alone.
update graph_nodes
set attributes = '{"block_type": true, "content": "block+", "render": "host", "version": 7, "position": 0}'::jsonb
where kind = 'supertag' and id = 'callout';

update graph_nodes
set attributes = '{"block_type": true, "content": "paragraph+ bullet_list?", "render": "host", "version": 7, "position": 1}'::jsonb
where kind = 'supertag' and id = 'ask';

update graph_nodes
set attributes = '{"block_type": true, "content": "paragraph+", "render": "host", "version": 7, "position": 2, "since": 3, "creatable_since": 3}'::jsonb
where kind = 'supertag' and id = 'requirement';

update graph_nodes
set attributes = '{"block_type": true, "content": "paragraph+", "render": "host", "version": 7, "position": 3, "since": 3, "creatable_since": 3}'::jsonb
where kind = 'supertag' and id = 'plan_task';

update graph_nodes
set attributes = '{"block_type": true, "content": "paragraph+", "render": "host", "version": 7, "position": 4, "since": 3, "creatable_since": 3}'::jsonb
where kind = 'supertag' and id = 'check';

update graph_nodes
set attributes = '{"block_type": true, "content": "block+", "render": "host", "version": 7, "position": 5, "since": 6, "creatable_since": 6}'::jsonb
where kind = 'supertag' and id = 'decision';

update graph_nodes
set attributes = '{"block_type": true, "content": "paragraph+", "render": "host", "version": 7, "position": 6, "since": 6, "creatable_since": 6, "parents": ["decision", "ask"]}'::jsonb
where kind = 'supertag' and id = 'reading';

-- The facts of each node this migration writes or moves, as the tuples they are, as the seed states
-- every type node's: the two base attributes of each new attribute node, the `attribute_of` tuple
-- that ties it to its type (a reader of the schema follows this tuple, never the spelling of the
-- node's id), a `value` tuple for each value a row carries that the node did not already state, the
-- retraction of the `scope` `needs` no longer carries (a later tuple under the name that asserts
-- nothing), and each block type's moved `version`. A value held as JSON is written as that JSON's
-- text and a string as itself, so `version` is `7` and `items` is `["issue"]`. 76 rows.
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
values
  ('attribute', 'check/parent', 'state', 'value', 'live', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'check/parent', 'visibility', 'value', 'project', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'criterion/parent', 'state', 'value', 'live', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'criterion/parent', 'visibility', 'value', 'project', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/parent', 'state', 'value', 'live', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/parent', 'visibility', 'value', 'project', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/size', 'state', 'value', 'live', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/size', 'visibility', 'value', 'project', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/parent', 'state', 'value', 'live', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/parent', 'visibility', 'value', 'project', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/serves', 'state', 'value', 'live', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/serves', 'visibility', 'value', 'project', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'check/parent', 'attribute_of', 'supertag', 'check', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'criterion/parent', 'attribute_of', 'supertag', 'criterion', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/parent', 'attribute_of', 'supertag', 'plan_task', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/size', 'attribute_of', 'supertag', 'plan_task', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/parent', 'attribute_of', 'supertag', 'requirement', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/serves', 'attribute_of', 'supertag', 'requirement', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'check/parent', 'creatable_since', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'check/parent', 'items', 'value', '["ask", "comment", "message"]', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'check/parent', 'kind', 'value', 'block[]', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'check/parent', 'name', 'value', 'parent', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'check/parent', 'position', 'value', '6', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'check/parent', 'since', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'check/parent', 'store_name', 'value', 'parent', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'criterion/parent', 'creatable_since', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'criterion/parent', 'items', 'value', '["ask", "comment", "message"]', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'criterion/parent', 'kind', 'value', 'block[]', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'criterion/parent', 'name', 'value', 'parent', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'criterion/parent', 'position', 'value', '0', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'criterion/parent', 'since', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'criterion/parent', 'store_name', 'value', 'parent', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/needs', 'items', 'value', '["issue"]', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/needs', 'item_versions', 'value', '{"issue": {"since": 7, "creatable_since": 7}}', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/parent', 'creatable_since', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/parent', 'items', 'value', '["ask", "comment", "message"]', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/parent', 'kind', 'value', 'block[]', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/parent', 'name', 'value', 'parent', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/parent', 'position', 'value', '4', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/parent', 'since', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/parent', 'store_name', 'value', 'parent', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/size', 'creatable_since', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/size', 'kind', 'value', 'days', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/size', 'name', 'value', 'size', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/size', 'position', 'value', '3', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/size', 'since', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/size', 'store_name', 'value', 'size', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/parent', 'creatable_since', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/parent', 'items', 'value', '["ask", "comment", "message"]', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/parent', 'kind', 'value', 'block[]', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/parent', 'name', 'value', 'parent', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/parent', 'position', 'value', '1', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/parent', 'since', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/parent', 'store_name', 'value', 'parent', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/serves', 'creatable_since', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/serves', 'kind', 'value', 'block[]', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/serves', 'name', 'value', 'serves', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/serves', 'position', 'value', '0', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/serves', 'since', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/serves', 'store_name', 'value', 'serves', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'requirement/serves', 'targets', 'value', '["criterion", "requirement"]', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('supertag', 'criterion', 'block_type', 'value', 'true', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('supertag', 'criterion', 'content', 'value', 'paragraph+', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('supertag', 'criterion', 'creatable_since', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('supertag', 'criterion', 'position', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('supertag', 'criterion', 'render', 'value', 'host', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('supertag', 'criterion', 'since', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('supertag', 'criterion', 'version', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('attribute', 'plan_task/needs', 'scope', 'value', 'document', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, false),
  ('supertag', 'ask', 'version', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('supertag', 'callout', 'version', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('supertag', 'check', 'version', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('supertag', 'decision', 'version', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('supertag', 'plan_task', 'version', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('supertag', 'reading', 'version', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true),
  ('supertag', 'requirement', 'version', 'value', '7', now(), '{"kind": "system", "id": "block-schema-issue-tree"}'::jsonb, 0, null, true);
