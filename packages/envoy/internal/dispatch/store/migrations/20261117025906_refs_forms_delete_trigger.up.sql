-- 20261117025906_refs_forms_delete_trigger.up.sql
-- Every delete from refs owes refs_forms_owed (20261117025900) each source whose edges it deleted.
-- An older build deletes the edges of the forms that migration names whenever it re-indexes a text
-- with the grammar it knows, and not every such re-index is a write of the source row the four
-- trigger migrations before the seed watch: its drain of a refs_grammar_owed `scope` row re-indexes
-- each comment, ask and issue message of a document's scope that writes a `#id`
-- (refs.ReindexScopeSource), and its block-index pass re-indexes a document this build left owed a
-- block index (docs.Service.indexOwedDocument), each from text no write changed. Its delete is the
-- one statement each of those re-indexes makes, so a trigger on it sees them all.
-- Every path of this build that deletes from refs settles the source's row after its delete, in
-- the same transaction - a write's index and every re-index through refs.replaceRefs, a gone
-- source's re-index (refs.reindexText), a document's removal (refs.ForgetDocument) and
-- rebuild-refs' orphan sweep (refs.RebuildAll) - so only a delete of a build that does not settle
-- this table stays owed.
--
-- A statement trigger over the deleted rows rather than a row trigger: a write's re-index deletes
-- its edges in one statement (refs.replaceRefs), and the source it owes is the same for every row
-- that statement deletes, so one insert per statement owes it once, where a row trigger would
-- insert once per edge. A statement that deletes no row inserts nothing.
-- The insert keeps an owed row's enqueued_at (`do nothing`): a delete re-reads no text, it only
-- says the text is owed, and each path that deletes holds the row its source's writers hold (the
-- source row, or a document's owner row), so a drain that listed the row earlier either ran after
-- this delete committed and indexes the text as it stands, or ran before it and settled the row,
-- which this insert then writes again.
-- Only the queue's four source kinds are owed, and only an id in the one spelling a source's
-- `id::text` writes, the canonical lowercase uuid (refs.canonicalUUID's rule): a refs row a test
-- writes for a synthetic source carries an id that is no uuid and owes nothing, and the filter
-- reads the id before the cast, so such a row is passed over rather than refused.
-- Its own migration, so the transaction holds refs' lock alone, for milliseconds (0153's
-- precedent).
create or replace function refs_forms_owe_deleted() returns trigger language plpgsql as $$
begin
  insert into refs_forms_owed (kind, id)
  select distinct gone.from_kind, gone.from_id::uuid from gone
  where gone.from_kind in ('comment', 'ask', 'message', 'artifact')
    and gone.from_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  on conflict (kind, id) do nothing;
  return null;
end;
$$;
create trigger refs_forms_owed_delete after delete on refs
  referencing old table as gone
  for each statement execute function refs_forms_owe_deleted();
