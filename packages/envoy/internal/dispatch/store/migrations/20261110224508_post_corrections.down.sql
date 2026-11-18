-- 20261110224508_post_corrections.down.sql
--
-- The rollback an operator runs by hand: the trigger back to the insert-only one 0144 wrote,
-- then the history table, then the six columns. It loses every body an edit replaced, the mark
-- on each deleted post and the record of each version a delete renamed; a post a delete cleared
-- keeps the empty body it was left with, and a version a delete renamed the name it was given,
-- which no rollback can fill again. Run it only against a build that reads none of them.
create or replace trigger messages_item_blocks_owed after insert on messages
  for each row execute function item_blocks_owe('message');

drop table post_body_history;

alter table artifact_versions drop column renamed_by_delete;

alter table messages drop column edited_at, drop column deleted_at, drop column deleted_by;

alter table comments drop column deleted_at, drop column deleted_by;
