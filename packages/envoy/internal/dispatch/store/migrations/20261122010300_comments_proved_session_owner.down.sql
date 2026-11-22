-- Run by hand, outside a transaction: DROP INDEX CONCURRENTLY refuses to run inside one, and no
-- runner applies a rollback script. Nothing depends on this index, so it can be dropped on its
-- own, and a person's erase then reads comments whole again under its locks.
drop index concurrently if exists comments_proved_session_owner;
