-- Run by hand, outside a transaction: DROP INDEX CONCURRENTLY refuses to run inside one, and no
-- runner applies a rollback script. Nothing depends on this index, so it can be dropped on its
-- own, and each block batch of envoy-dispatch backfill-graph-store then reads and sorts every row
-- still naming no node again.
drop index concurrently if exists document_blocks_naming_no_node;
