-- pgmigrate:non-transactional
-- 20261119020000_comment_deliveries_reply_id.up.sql
-- comment_deliveries.reply_id (0039) names the reply that answered a delivered comment, through a
-- foreign key into comments and no index, and Postgres indexes the referenced side of a key and
-- never the referencing side. So two reads made while the erase (envoy-dispatch erase) holds the
-- owner locks of a comment's issue or document read the whole of this table: its count of the rows
-- that point at the comment it erases (grapherase's readBlockers, which refuses the run while one
-- does), and the check of this key that the delete of the comment's row makes. comment_deliveries
-- holds a row for each recipient of each delivered comment, so it grows with every comment that
-- names a session; this index lets both reads probe it by the reply's id instead.
--
-- It holds the rows that name a reply alone, since neither read asks for a row that names none:
-- `reply_id in (...)` and the key's check `reply_id = $1` each imply `reply_id is not null`.
--
-- It is built `concurrently`, outside the runner's transaction: a plain build would hold SHARE on
-- comment_deliveries, which blocks every write, for the length of a read of the table. A concurrent
-- build takes SHARE UPDATE EXCLUSIVE, which no ordinary read or write waits on, and the census
-- exempts it from its table size limit on that ground (pgmigrate's concurrentIndex). A failed
-- build leaves an INVALID index of this name, which the runner drops with DROP INDEX CONCURRENTLY
-- and builds again on the next attempt. It is a performance index: no constraint and no later
-- migration depends on it.
create index concurrently if not exists comment_deliveries_reply_id
    on comment_deliveries (reply_id)
    where reply_id is not null;
