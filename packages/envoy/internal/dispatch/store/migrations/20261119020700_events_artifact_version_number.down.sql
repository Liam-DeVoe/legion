-- Run by hand, outside a transaction: DROP INDEX CONCURRENTLY refuses to run inside one, and no
-- runner applies a rollback script. Nothing depends on this index, so it can be dropped on its
-- own, and envoy-dispatch backfill-graph-store then reads events whole again for each document
-- version of its block batches.
drop index concurrently if exists events_artifact_version_number;
