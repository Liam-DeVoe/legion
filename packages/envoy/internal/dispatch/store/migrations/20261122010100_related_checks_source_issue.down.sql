-- Run by hand, outside a transaction: DROP INDEX CONCURRENTLY refuses to run inside one, and no
-- runner applies a rollback script. Nothing depends on this index, so it can be dropped on its
-- own, and the erase's read of an issue's own related-item checks then reads related_checks whole
-- again.
drop index concurrently if exists related_checks_source_issue;
