-- Run by hand, outside a transaction: DROP INDEX CONCURRENTLY refuses to run inside one, and no
-- runner applies a rollback script. Nothing depends on this index, so it can be dropped on its
-- own, and envoy-dispatch backfill-graph-store then reads events whole again for each comment of
-- its comment batches.
drop index concurrently if exists events_comment_created_id;
