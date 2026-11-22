-- pgmigrate:non-transactional
-- 20261122010100_related_checks_source_issue.up.sql
-- related_checks (0139) records each related-item check, and an issue's own check, made when the
-- issue is created, keys its row by the issue (source_issue_key) and picks its terms from the
-- issue's title and spec together. Its one index, (project_key, created_at), serves the records
-- route alone, and every check of an ask or a decision block on the issue names the same key. So a
-- read made while the erase (envoy-dispatch erase) holds the owner lock of an issue reads the whole
-- of this table: its read of the issue's own checks when it erases a block of the issue's spec,
-- whose terms it empties (grapherase's readRelatedChecks). This index lets the read probe it by the
-- issue's key instead. 20261122010000 is the same kind of index on the block id a decision block's
-- check names.
--
-- It holds an issue's own checks alone, since the read asks for no other: it names
-- `source_kind = 'issue'`, the index's predicate, and a check of an ask or a decision block on the
-- issue is not one the erase of a spec block empties.
--
-- It is built `concurrently`, outside the runner's transaction: a plain build would hold SHARE on
-- related_checks, which blocks every write, for the length of a read of the table. A concurrent
-- build takes SHARE UPDATE EXCLUSIVE, which no ordinary read or write waits on, and the census
-- exempts it from its table size limit on that ground (pgmigrate's concurrentIndex). A failed
-- build leaves an INVALID index of this name, which the runner drops with DROP INDEX CONCURRENTLY
-- and builds again on the next attempt. It is a performance index: no constraint and no later
-- migration depends on it.
create index concurrently if not exists related_checks_source_issue
    on related_checks (source_issue_key)
    where source_kind = 'issue';
