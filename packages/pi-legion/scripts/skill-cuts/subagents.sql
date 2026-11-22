SET LOCAL statement_timeout = '15min';
-- A subagent session each top-level session of a list started whose recorded system prompt holds
-- a skill listing, for build.ts's listing measure: a top-level session records only its prompt's
-- digest, and Oh My Pi 18.18.6 gives a subagent its parent's skills, so a subagent's recorded
-- listing is the listing to compare the rebuilt profile with. `subagentsSql` of countsql.ts fills
-- it, and build.ts runs it through the store's read command, one query a store.
--
-- Placeholder, written in the query as its name inside double braces and filled with one whole SQL
-- value:
--   hashes  the top-level sessions' path hashes, a text array (`ARRAY['<64 hex>', …]::text[]`),
--           each the SHA-256 in hex of the file's path, as cases.ts's `pathHash` names a session
--
-- What it reads. The path of every file of public.omp_session_files, once, to hash it, as
-- session-head.sql does (the store names a file by its path alone, and its one index on the path
-- is a btree under the database's collation, which serves no match of a path's start); then each
-- file one level below a listed session's directory, the session's path less `.jsonl` and a `/`
-- (Oh My Pi 18.18.6 writes a subagent's transcript at `<artifacts dir>/<id>.jsonl`,
-- task/executor.ts, line 4743, the artifacts dir being its parent's transcript path less `.jsonl`,
-- as testdata/F1 shows), matched by `starts_with` in the same scan; and of each such file
-- only its head, as session-head.sql reads one: the file row's `content`, then each part of
-- public.omp_session_files_parts whose start_offset is below 262144, in start_offset order, each
-- piece cut to its first 262144 characters and the head too (256 Ki). Of the head, only the lines
-- that hold `"type":"session"` (the header; not `"type":"session_init"`, whose closing quote comes
-- later) or `"type":"session_init"` are looked at. A session_init line counts when it holds
-- `<skills>` and is one JSON value (a line the head's end cuts is not).
--
-- Rows, one JSON value per listed session that has such a subagent (psql prints each on one line),
-- in hash order; a session with none has no row:
--   {"parent": <the session's path hash>,
--    "hash": <the subagent file's path hash>, the subagent whose header's timestamp is the earliest
--      of those whose session_init line counts (ties by path),
--    "start": that header's `timestamp`, or null,
--    "init": that session_init line, whole,
--    "subagents": the files one level below the session's directory,
--    "recorded": those of them whose session_init line counts}
-- No path leaves the database: the rows name files by path hash.
WITH parents AS MATERIALIZED (
  SELECT h.hash, left(h.path, -length('.jsonl')) || '/' AS dir
  FROM (
    SELECT f.path, encode(sha256(convert_to(f.path, 'UTF8')), 'hex') AS hash
    FROM public.omp_session_files f
  ) h
  WHERE h.hash = ANY ({{hashes}}) AND right(h.path, length('.jsonl')) = '.jsonl'
),
children AS MATERIALIZED (
  SELECT p.hash AS parent, c.path
  FROM public.omp_session_files c
  JOIN parents p
    ON starts_with(c.path, p.dir)
    AND strpos(substr(c.path, length(p.dir) + 1), '/') = 0
    AND right(c.path, length('.jsonl')) = '.jsonl'
),
heads AS (
  SELECT ch.parent, ch.path, hl.start, hl.init
  FROM children ch
  CROSS JOIN LATERAL (
    SELECT substr(
      coalesce((
        SELECT substr(f.content, 1, 262144) FROM public.omp_session_files f WHERE f.path = ch.path
      ), '') || coalesce((
        SELECT string_agg(substr(pt.content, 1, 262144), '' ORDER BY pt.start_offset)
        FROM public.omp_session_files_parts pt
        WHERE pt.path = ch.path AND pt.start_offset < 262144
      ), ''),
      1,
      262144
    ) AS head
    OFFSET 0
  ) hd
  CROSS JOIN LATERAL (
    SELECT
      (array_agg(
        CASE WHEN pg_input_is_valid(t.ln, 'jsonb') THEN t.ln::jsonb ->> 'timestamp' END
        ORDER BY t.n
      ) FILTER (WHERE strpos(t.ln, '"type":"session"') > 0))[1] AS start,
      (array_agg(t.ln ORDER BY t.n) FILTER (
        WHERE strpos(t.ln, '"type":"session_init"') > 0
          AND strpos(t.ln, '<skills>') > 0
          AND pg_input_is_valid(t.ln, 'jsonb')
      ))[1] AS init
    FROM string_to_table(hd.head, E'\n') WITH ORDINALITY AS t(ln, n)
    WHERE strpos(t.ln, '"type":"session') > 0
  ) hl
),
ranked AS (
  SELECT
    h.parent,
    h.path,
    h.start,
    h.init,
    count(*) OVER (PARTITION BY h.parent) AS subagents,
    count(h.init) OVER (PARTITION BY h.parent) AS recorded,
    row_number() OVER (
      PARTITION BY h.parent
      ORDER BY (h.init IS NULL), h.start COLLATE "C", h.path COLLATE "C"
    ) AS place
  FROM heads h
)
SELECT json_build_object(
  'parent', r.parent,
  'hash', encode(sha256(convert_to(r.path, 'UTF8')), 'hex'),
  'start', r.start,
  'init', r.init,
  'subagents', r.subagents,
  'recorded', r.recorded
)::text AS subagent
FROM ranked r
WHERE r.place = 1 AND r.init IS NOT NULL
ORDER BY r.parent COLLATE "C";
