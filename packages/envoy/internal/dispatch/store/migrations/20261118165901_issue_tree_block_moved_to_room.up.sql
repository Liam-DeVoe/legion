-- 20261118165901_issue_tree_block_moved_to_room.up.sql — where a block address resolves to once
-- issues are born into their rooms.
--
-- A reference names `<document ref_key>#<block id>` (text.TargetID), and a block of an issue room
-- is the node `<tree ref_key>#<block id>`: its document_blocks row is keyed by the issue's tree
-- artifact (20261117045607_issue_tree_rooms). block_moved_to (migrations 20261117002006 and
-- 20261117042519) is the one reading every resolver of an address takes, and it found no block
-- at the spec's own address of a block of an issue born converted, so a check of such an issue
-- could serve none of its spec's requirements. This migration resolves a block's address in one
-- order (T17 of this branch's plan), in two steps that are functions of their own:
--
-- block_address_walk(address) follows the address's current moved_to tuple - the newest of each
-- far end kept while it asserts, as before - and so on from each far end while another current
-- moved_to stands there, and answers the address it reaches, the address itself where no tuple
-- stands; a far end already visited ends the walk as a cycle, and a ninth tuple as a chain past
-- graphstore.MovedToHops, each answering null.
--
-- block_room_address(address) is the same block id in the room the document_rooms row of the
-- address's document names, `<tree ref_key>#<block id>`, and null where its document has no room.
--
-- block_moved_to(address) of a block's address is the room address of the reached address where a
-- document_blocks row holds it, else the reached address where one holds that, else null. So a
-- re-minted block's old address reaches its fresh id through its moved_to and never the block of
-- another document that keeps the id in the room. The block index's writer takes the same two
-- steps without asking for a row where a pointer resolves to no block (refs' resolvePointerPairs),
-- so a pointer whose block was deleted keeps the far end it named. From a document's address,
-- which holds no '#', it answers the lens as 20261117042519 does, unchanged.
--
-- Rolling deploy. Two new functions, and create or replace keeps block_moved_to's name, its
-- argument and its result type. The release before this one names neither new function, and it
-- calls block_moved_to after its own probe found no row at the address, or alone, and the body
-- answers each of those calls as before: a document_rooms row is written only for an issue
-- created while every live process lists issue_room, which no process of that release does, and
-- no writer of either release states a moved_to from an address a document_blocks row holds.
-- Rolling back past this migration is an image rollback alone: the release before calls the
-- restated function as it called the one it replaces, and calls neither of the other two.
create function block_address_walk(address text) returns text
  language plpgsql stable strict
  as $$
declare
  reached text := address;
  passed text[] := array[address];
  far_end text;
begin
  for hop in 1 .. 9 loop
    select moved.to_id into far_end
    from (
      select distinct on (t.to_kind, t.to_id) t.to_kind, t.to_id, t.asserted, t.at, t.seq
      from graph_tuples t
      where t.from_kind = 'block' and t.from_id = reached and t.kind = 'moved_to'
      order by t.to_kind, t.to_id, t.at desc, t.seq desc
    ) moved
    where moved.asserted and moved.to_kind = 'block'
    order by moved.at desc, moved.seq desc
    limit 1;
    exit when not found;
    if hop = 9 or far_end = any(passed) then
      return null;
    end if;
    passed := passed || far_end;
    reached := far_end;
  end loop;
  return reached;
end
$$;

create function block_room_address(address text) returns text
  language sql stable strict
  as $$
  select tree.ref_key || substr(address, strpos(address, '#'))
  from artifacts document
  join document_rooms room on room.artifact_id = document.id
  join artifacts tree on tree.id = room.room
  where document.ref_key = split_part(address, '#', 1)
$$;

create or replace function block_moved_to(address text) returns text
  language plpgsql stable strict
  as $$
declare
  reached text := address;
  roomed text;
begin
  if strpos(address, '#') = 0 then
    select moved.to_id into reached
    from (
      select distinct on (t.to_kind, t.to_id) t.to_kind, t.to_id, t.asserted, t.at, t.seq
      from graph_tuples t
      where t.from_kind = 'artifact' and t.from_id = address and t.kind = 'moved_to'
      order by t.to_kind, t.to_id, t.at desc, t.seq desc
    ) moved
    where moved.asserted and moved.to_kind = 'lens'
    order by moved.at desc, moved.seq desc
    limit 1;
    if found and exists (select 1 from graph_nodes n where n.kind = 'lens' and n.id = reached and n.state <> 'gone') then
      return reached;
    end if;
    return null;
  end if;
  reached := block_address_walk(address);
  if reached is null then
    return null;
  end if;
  roomed := block_room_address(reached);
  if roomed is not null and exists (select 1 from document_blocks d where d.node_id = roomed) then
    return roomed;
  end if;
  if exists (select 1 from document_blocks d where d.node_id = reached) then
    return reached;
  end if;
  return null;
end
$$;
