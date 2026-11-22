-- pgmigrate:non-transactional
-- 20261122010300_comments_proved_session_owner.up.sql
-- A person's erase (envoy-dispatch erase) refuses the person while a comment that a session the
-- server proved theirs wrote still has its row, and it reads those rows while it holds its locks
-- (grapherase's readBlockers, through graphcensus.ProvedSessionComments): a row whose author is a
-- session the server proved, whose `owner` is the person's login as user_threads_login reads it.
-- No index serves that comparison, so the read reads the whole of comments, which grows with every
-- comment written, while the erase's locks are held. This index lets it probe comments by the
-- person instead.
--
-- Its key is user_threads_login(author->>'owner'), the expression the read compares with the
-- person's login. user_threads_login (0095) is an immutable SQL function, and Postgres inlines it
-- into the read and into this index's expression alike when it plans, so the two still match.
--
-- It holds the comments of proved sessions alone, since the read asks for no other: its predicate
-- is graphcensus's provedSessionOwner over author, clause for clause - the author is a session,
-- its id is set, its act names that same session, and the id is within the store's address bound
-- (graphstore.MaxAddressBytes, 1024 bytes) - so the read's own clauses prove it. A change to that
-- rule that drops or widens one of its clauses changes this index with it.
--
-- It is built `concurrently`, outside the runner's transaction: a plain build would hold SHARE on
-- comments, which blocks every write, for the length of a read of the table. A concurrent build
-- takes SHARE UPDATE EXCLUSIVE, which no ordinary read or write waits on, and the census exempts
-- it from its table size limit on that ground (pgmigrate's concurrentIndex). A failed build leaves
-- an INVALID index of this name, which the runner drops with DROP INDEX CONCURRENTLY and builds
-- again on the next attempt. It is a performance index: no constraint and no later migration
-- depends on it.
create index concurrently if not exists comments_proved_session_owner
    on comments (user_threads_login(author->>'owner'))
    where author->>'kind' = 'session' and coalesce(author->>'id', '') <> ''
      and author->'act'->>'session' = author->>'id'
      and octet_length(author->>'id') <= 1024;
