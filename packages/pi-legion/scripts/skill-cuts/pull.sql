-- The transcript pull of R2's cases: one session file's bytes, by its path, one row
-- per stored piece, at most 64 rows per call, so one call's output stays small. `pullSql` of
-- countsql.ts fills it; psql-rows.ts's `pullText` joins the rows of every call into the file's
-- text.
--
-- Placeholders, each written in the query as its name inside double braces and filled with one
-- whole SQL value:
--   path   the file's path, a string literal
--   after  the offset the rows start after, a number: -2 for the first call (so the file row,
--          offset -1, comes first), then the last row's `o` of the call before
-- A call that returns fewer than 64 rows is the last. A path the store does not hold returns no
-- row.
--
-- Rows, in offset order, each one JSON value (psql prints each on one line):
--   {"o": -1, "c": <the file row's content>}       the bytes before every part
--   {"o": <start_offset>, "c": <the part's content>}
-- start_offset is the part's byte offset in the file.
SELECT p.part
FROM (
  SELECT -1::bigint AS o, json_build_object('o', -1, 'c', coalesce(f.content, ''))::text AS part
  FROM public.omp_session_files f
  WHERE f.path = {{path}}
  UNION ALL
  SELECT pt.start_offset, json_build_object('o', pt.start_offset, 'c', pt.content)::text
  FROM public.omp_session_files_parts pt
  WHERE pt.path = {{path}}
) p
WHERE p.o > {{after}}
ORDER BY p.o
LIMIT 64;
