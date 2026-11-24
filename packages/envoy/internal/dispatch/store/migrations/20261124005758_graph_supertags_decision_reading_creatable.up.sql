-- 20261124005758_graph_supertags_decision_reading_creatable.up.sql
--
-- The block schema moves from version 6 to version 7, which makes `decision` and `reading`, every
-- attribute of `decision`, and `ask`'s `shaped` creatable. 20261123174700 gave each of them
-- `since` 6 and `creatable_since` 7, so a build of version 6 reads and validates a stored one and
-- refuses to create one. `pmdoc/schema/blocks.json` now declares version 7 and changes no element:
-- a build of version 7 creates every element whose `creatable_since` is 7 or less (pmdoc.Install).
-- A build of version 6, which holds 20261123174700 and not this file, reads the store's schema at
-- its own version (asOfVersion), so it installs the schema it installed before this ran and keeps
-- serving.
--
-- It is a type migration written after the store's backfill, as
-- 20261123080000_graph_supertags_plan_task_steps and 20261123174700_graph_supertags_decision_reading
-- are: it is numbered above backfillVersion (cmd/dispatch/supertag_nodes_test.go) and above
-- 20261123174700, so it runs after that one on every store, and finds what that one left.
-- cmd/dispatch's TestATypeMigrationAfterTheBackfillIsAdmittedAndLeavesTheFilesSchema runs it after
-- a backfill, with the census at a one-byte limit, and holds what it leaves to the file.
--
-- Every statement is one of the two shapes the pre-deploy census admits on a table past
-- pgmigrate.CensusTableLimit, as in 20261123174700: an `update` that sets a node's whole
-- `attributes` to a literal on the row its `where` names by its primary key, `(kind, id)`, and an
-- `insert … values` of literal tuples. Every row carries now(), the start of the runner's
-- transaction, as 20261123174700's do, so this file's version-7 tuples order after that file's
-- version-6 ones. It runs once, as the runner applies every file (typeMigrations in
-- cmd/dispatch/supertag_nodes_test.go). The migration records no event, so every tuple carries the
-- `source_event` sentinel 0, and a writer of its own.

-- The version. Every block type's supertag node carries the version of the schema the store holds,
-- and the schema the nodes build takes the highest of them, so each of the seven moves to 7. Each
-- is set to its whole attributes: what 20261123174700 left on it, its version moved from 6 to 7.
-- Nothing but a migration writes a type node, so that is what each holds, and this rewrites
-- nothing but the version. No two carry the same value, so each is a statement of its own.
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
set attributes = '{"block_type": true, "content": "block+", "render": "host", "version": 7, "position": 5, "since": 6, "creatable_since": 7}'::jsonb
where (kind, id) in (('supertag', 'decision'));

update graph_nodes
set attributes = '{"block_type": true, "content": "paragraph+", "render": "host", "version": 7, "position": 6, "since": 6, "creatable_since": 7, "parents": ["decision", "ask"]}'::jsonb
where (kind, id) in (('supertag', 'reading'));

-- The move, as the tuple it is: each block type's newest `version` tuple is the 7 its row now
-- carries, and the 6 and the versions before it stay in its history.
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
values
  ('supertag', 'ask', 'version', 'value', '7', now(), '{"kind": "system", "id": "block-schema-decision-reading-creatable"}', 0, null, true),
  ('supertag', 'callout', 'version', 'value', '7', now(), '{"kind": "system", "id": "block-schema-decision-reading-creatable"}', 0, null, true),
  ('supertag', 'check', 'version', 'value', '7', now(), '{"kind": "system", "id": "block-schema-decision-reading-creatable"}', 0, null, true),
  ('supertag', 'decision', 'version', 'value', '7', now(), '{"kind": "system", "id": "block-schema-decision-reading-creatable"}', 0, null, true),
  ('supertag', 'plan_task', 'version', 'value', '7', now(), '{"kind": "system", "id": "block-schema-decision-reading-creatable"}', 0, null, true),
  ('supertag', 'reading', 'version', 'value', '7', now(), '{"kind": "system", "id": "block-schema-decision-reading-creatable"}', 0, null, true),
  ('supertag', 'requirement', 'version', 'value', '7', now(), '{"kind": "system", "id": "block-schema-decision-reading-creatable"}', 0, null, true);
