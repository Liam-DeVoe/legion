-- Run by hand, outside a transaction: DROP INDEX CONCURRENTLY refuses to run inside one, and no
-- runner applies a rollback script. Nothing depends on this index, so it can be dropped on its
-- own, and the erase's blockers read and the delete of a comment's row then read
-- comment_deliveries whole again.
drop index concurrently if exists comment_deliveries_reply_id;
