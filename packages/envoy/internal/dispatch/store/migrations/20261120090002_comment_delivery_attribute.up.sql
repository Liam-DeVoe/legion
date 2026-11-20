-- 20261120090002_comment_delivery_attribute.up.sql
--
-- The comment supertag takes its one attribute, its urgency. A comment that names targets as
-- delivery routes states one delivery mode for all of them - btw, aside or steer, the mode
-- validateCommentMentions in api picks and applies to every target - as a `delivery` value tuple out
-- of the comment's node (R5, graphstore.TupleDelivery), which comment_mentions.delivery projects for
-- each target. 20261109051232 seeded the supertag node with no attribute of its own, since the
-- kinds that move in a later child bring theirs with them; this is that child's. The attribute is a
-- node, tied to the supertag by an `attribute_of` tuple, exactly as 20261116102759 ties each of a
-- delivery attempt's, so a reader of the store learns what a comment carries from the store rather
-- than from Go.
--
-- The block schema is not changed. comment declares no block type, so the schema the server builds
-- from the type nodes (pmdoc/schemanodes.Read) passes over it and its attribute, and no block type's
-- version moves. The triggers of 20261109051232 still announce the write once, at this migration's
-- commit, and every running server reads the schema again and installs the same one.
--
-- The attribute node's attributes are what 20261116102759 builds for each of an attempt's: its own
-- schema fields, with its name, its store name and its position beside them. Its kind is the block
-- schema's `enum`, its choices the three modes comment_mentions' check admits. It is not marked
-- `server`: the writer of a comment names the mode in its request, where an attempt's values are
-- the server's alone. Its store name is its own name, since `delivery` is no base attribute of a
-- node (graphstore.IsBaseAttribute).
--
-- Running this file again writes the same rows, as running the seed again does: the node upsert
-- moves every column but `made_at` and `made_by`, and each tuple insert appends only a fact the node
-- does not already carry. The migration records no event, so every row it writes carries the
-- `source_event` sentinel 0, and its rows carry a writer of their own, so a reader of the store can
-- tell them from the seed's.
with change as (
  select now() as at, '{"kind": "system", "id": "block-schema-comment"}'::jsonb as writer
),
declared (name, store_name, position, schema) as (
  values ('delivery', 'delivery', 0, '{"kind": "enum", "choices": ["btw", "aside", "steer"]}'::jsonb)
)
insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
select 'attribute', 'comment/' || declared.store_name, 'attribute', 'project', 'live',
       declared.schema || jsonb_build_object(
         'name', declared.name, 'store_name', declared.store_name, 'position', declared.position
       ),
       change.at, change.writer, 0
from change, declared
order by declared.store_name
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
where node.kind = 'attribute' and node.id like 'comment/%'
  and not exists (
    select 1 from graph_tuples held
    where held.from_kind = node.kind and held.from_id = node.id and held.kind = fact.kind
      and held.to_kind = 'value' and held.to_id = fact.value
      and held.source_event = 0 and held.asserted
  )
order by node.id, fact.kind;

-- What ties the attribute to comment. A reader of the schema follows this tuple, never the spelling
-- of the node's id, so without it the node is no attribute of any type.
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
select node.kind, node.id, 'attribute_of', 'supertag', 'comment', node.made_at, node.made_by, 0, null, true
from graph_nodes node
where node.kind = 'attribute' and node.id like 'comment/%'
  and not exists (
    select 1 from graph_tuples held
    where held.from_kind = node.kind and held.from_id = node.id and held.kind = 'attribute_of'
      and held.to_kind = 'supertag' and held.to_id = 'comment'
      and held.source_event = 0 and held.asserted
  )
order by node.id;

-- The values the node carries, as the tuples they are, read back off the row just written as the
-- seed reads every type node's: a `value` tuple whose kind is the attribute's name and whose far
-- end is its value as text, at the `source_event` sentinel.
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
select node.kind, node.id, fact.kind, 'value', fact.value, node.made_at, node.made_by, 0, null, true
from graph_nodes node
cross join lateral (
  select attribute.key, attribute.value #>> '{}' from jsonb_each(node.attributes) as attribute
) as fact(kind, value)
where node.kind = 'attribute' and node.id like 'comment/%'
  and not exists (
    select 1 from graph_tuples held
    where held.from_kind = node.kind and held.from_id = node.id and held.kind = fact.kind
      and held.to_kind = 'value' and held.to_id = fact.value
      and held.source_event = 0 and held.asserted
  )
order by node.id, fact.kind;
