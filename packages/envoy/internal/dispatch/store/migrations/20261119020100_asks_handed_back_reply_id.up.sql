-- pgmigrate:non-transactional
-- 20261119020100_asks_handed_back_reply_id.up.sql
-- asks.handed_back_reply_id (0064, its key 0066) names the reply a hand-back answered, through a
-- foreign key into comments and no index, and Postgres indexes the referenced side of a key and
-- never the referencing side. So two reads made while the erase (envoy-dispatch erase) holds the
-- owner locks of a comment's issue or document read the whole of asks: its count of the rows that
-- point at the comment it erases (grapherase's readBlockers, which refuses the run while one does),
-- and the check of this key that the delete of the comment's row makes. This index lets both reads
-- probe asks by the reply's id instead. 20261119020000 is the same index on comment_deliveries.
--
-- It holds the rows that name a reply alone, since neither read asks for a row that names none:
-- `handed_back_reply_id in (...)` and the key's check `handed_back_reply_id = $1` each imply
-- `handed_back_reply_id is not null`.
--
-- It is built `concurrently`, outside the runner's transaction: a plain build would hold SHARE on
-- asks, which blocks every write, for the length of a read of the table. A concurrent build takes
-- SHARE UPDATE EXCLUSIVE, which no ordinary read or write waits on, and the census exempts it from
-- its table size limit on that ground (pgmigrate's concurrentIndex). A failed build leaves an
-- INVALID index of this name, which the runner drops with DROP INDEX CONCURRENTLY and builds again
-- on the next attempt. It is a performance index: no constraint and no later migration depends on
-- it.
create index concurrently if not exists asks_handed_back_reply_id
    on asks (handed_back_reply_id)
    where handed_back_reply_id is not null;
