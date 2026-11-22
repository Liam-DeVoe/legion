-- Run by hand, outside a transaction: DROP INDEX CONCURRENTLY refuses to run inside one, and no
-- runner applies a rollback script. Nothing depends on this index, so it can be dropped on its
-- own, and the record of a related-item check then reads graph_erasures whole again under its
-- owner lock.
drop index concurrently if exists graph_erasures_block_document;
