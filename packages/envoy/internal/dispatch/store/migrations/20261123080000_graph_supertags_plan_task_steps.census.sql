-- This migration inserts the attribute node plan_task/steps and its tuples, and sets the
-- attributes of the five block type supertag nodes (callout, ask, requirement, plan_task and
-- check) to what 20261109051232_graph_supertags, 20261113230000_graph_supertags_check_ci_check
-- and 20261115130000_graph_supertags_element_versions left on them, the version moved from 4 to
-- 5. It reads graph_nodes, which 20261109013400_graph_store creates; all four ship in an earlier
-- release than this one (legion-envoy v34.11.0 and later).
--
-- The rows it would refuse or rewrite are counted here: a plan_task/steps node already in the
-- store, whose insert would fail on the primary key; a block type supertag node that holds
-- anything but those version-4 attributes, which the update would overwrite; a block type
-- supertag node of another name, which no update moves to 5; and each of the five that is
-- missing, whose update would move nothing. On a store those migrations alone wrote the count is
-- 0. The read is by graph_nodes' primary key, (kind, id), under kind 'supertag' and one id.
select
  (select count(*) from graph_nodes where kind = 'attribute' and id = 'plan_task/steps')
  + (select count(*) from graph_nodes node
     where node.kind = 'supertag' and coalesce((node.attributes->>'block_type')::boolean, false)
       and (node.id, node.attributes) not in (
         ('callout', '{"block_type": true, "content": "block+", "render": "host", "version": 4, "position": 0}'::jsonb),
         ('ask', '{"block_type": true, "content": "paragraph+ bullet_list?", "render": "host", "version": 4, "position": 1}'::jsonb),
         ('requirement', '{"block_type": true, "content": "paragraph+", "render": "host", "version": 4, "position": 2, "since": 3, "creatable_since": 3}'::jsonb),
         ('plan_task', '{"block_type": true, "content": "paragraph+", "render": "host", "version": 4, "position": 3, "since": 3, "creatable_since": 3}'::jsonb),
         ('check', '{"block_type": true, "content": "paragraph+", "render": "host", "version": 4, "position": 4, "since": 3, "creatable_since": 3}'::jsonb)))
  + (select 5 - count(*) from graph_nodes
     where kind = 'supertag' and id in ('callout', 'ask', 'requirement', 'plan_task', 'check'))
