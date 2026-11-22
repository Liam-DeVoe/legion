-- pgmigrate:non-transactional
-- 20261122010000_related_checks_source_block.up.sql
-- related_checks (0139) records each related-item check, and a check of a decision block keys its
-- row by the block's id within its document (source_block_id) beside the document's issue or the
-- document itself. Its one index, (project_key, created_at), serves the records route alone. So a
-- read made while the erase (envoy-dispatch erase) holds the owner locks of a block's document
-- reads the whole of this table: its read of the checks of the block it erases, whose terms hold
-- the lexemes each check picked from the block's question and which it empties (grapherase's
-- readRelatedChecks). related_checks holds a row for every issue create, ask and new decision
-- block, so it grows with every such write; this index lets the read probe it by the block's id
-- instead.
--
-- It holds the rows that name a block alone, since the read asks for no row that names none:
-- `source_block_id = $1` implies `source_block_id is not null`, and only a decision block's check
-- names a block (0139's check).
--
-- It is built `concurrently`, outside the runner's transaction: a plain build would hold SHARE on
-- related_checks, which blocks every write, for the length of a read of the table. A concurrent
-- build takes SHARE UPDATE EXCLUSIVE, which no ordinary read or write waits on, and the census
-- exempts it from its table size limit on that ground (pgmigrate's concurrentIndex). A failed
-- build leaves an INVALID index of this name, which the runner drops with DROP INDEX CONCURRENTLY
-- and builds again on the next attempt. It is a performance index: no constraint and no later
-- migration depends on it.
create index concurrently if not exists related_checks_source_block
    on related_checks (source_block_id)
    where source_block_id is not null;
