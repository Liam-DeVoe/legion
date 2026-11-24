SET LOCAL statement_timeout = '15min';
-- The paths of the session files a load task names by its path's SHA-256, so build.ts can pull
-- each by its path (pull.sql): `build-tasks --reprofile` finds each session task's session this
-- way, since a tasks file keeps a session's path hash (cases.ts's `pathHash`) and not its path. It
-- reads only public.omp_session_files and hashes the path of every file once, whatever the number
-- of hashes, as session-head.sql does, so build.ts gives it all of a store's path hashes in one
-- query. `sessionPathsSql` of countsql.ts fills it.
--
-- Placeholder, written in the query as its name inside double braces and filled with one whole SQL
-- value:
--   hashes  the path hashes, a text array (`ARRAY['<64 hex>', …]::text[]`), each the SHA-256 in
--           hex of the file's path
--
-- Rows, by path, each one JSON value: {"path": <the file's path>, "hash": <its path hash>}. A hash
-- of a file the store no longer holds has no row. The path goes no further than the process that
-- runs the query, as with sessions.sql.
SELECT json_build_object('path', h.path, 'hash', h.hash)::text AS session
FROM (
  SELECT f.path, encode(sha256(convert_to(f.path, 'UTF8')), 'hex') AS hash
  FROM public.omp_session_files f
) h
WHERE h.hash = ANY ({{hashes}})
ORDER BY h.path COLLATE "C";
