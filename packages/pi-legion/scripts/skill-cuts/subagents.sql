SET LOCAL statement_timeout = '15min';
-- A subagent session each top-level session of a list started between two times, whose recorded
-- system prompt holds a skill listing, for build.ts's listing measure: a top-level session records
-- only its prompt's digest, and Oh My Pi 18.18.6 gives a subagent its parent's skills, so the
-- recorded listing of a subagent spawned under the prompt build a profile is read at is the
-- listing to compare that profile with. `subagentsSql` of countsql.ts fills it, and build.ts runs
-- it through the store's read command, one query a store or a session.
--
-- Placeholder, written in the query as its name inside double braces and filled with one whole SQL
-- value:
--   sessions  the top-level sessions, a JSON array (`'[…]'::jsonb`) of objects
--             {"hash": <64 hex>, "from": <ISO time> or null, "to": <ISO time> or null}: each the
--             SHA-256 in hex of the file's path, as cases.ts's `pathHash` names a session, and the
--             times a subagent's start must be at or after (`from`) and before (`to`); null bounds
--             nothing
--
-- What it reads. The path of every file of public.omp_session_files, once, to hash it, as
-- session-head.sql does (the store names a file by its path alone, and its one index on the path
-- is a btree under the database's collation, which serves no match of a path's start); then, in a
-- second scan, each file whose directory (its path up to and including its last `/`) is a listed
-- session's directory, the session's path less `.jsonl` and a `/` (Oh My Pi 18.18.6 writes a
-- subagent's transcript at `<artifacts dir>/<id>.jsonl`, task/executor.ts, line 4743, the
-- artifacts dir being its parent's transcript path less `.jsonl`, as testdata/F1 shows), matched
-- by equality, so the listed sessions are a hash table the scan probes; and of each such file, once
-- however many times its session is listed, only its head, as session-head.sql reads one: the file
-- row's `content`, then each part of public.omp_session_files_parts whose start_offset is below
-- 262144, in start_offset order, each piece cut to its first 262144 characters and the head too
-- (256 Ki). Of the head, only the lines that hold `"type":"session"` (the header; not
-- `"type":"session_init"`, whose closing quote comes later) or `"type":"session_init"` are looked
-- at. A session_init line counts when it holds `<skills>` and is one JSON value (a line the head's
-- end cuts is not). A subagent's start is its header's `timestamp`; one with no valid time there
-- is between no two times.
--
-- Rows, one JSON value per listed session and times that has a subagent (psql prints each on one
-- line), in hash order; a session with none has no row:
--   {"parent": <the session's path hash>,
--    "from", "to": the times, as given,
--    "hash": <the subagent file's path hash>, the subagent whose header's timestamp is the earliest
--      of those whose session_init line counts and whose start is between the times (ties by
--      path), or null when none is,
--    "start": that header's `timestamp`, or null,
--    "init": that session_init line, whole, or null,
--    "subagents": the files one level below the session's directory,
--    "recorded": those of them whose session_init line counts,
--    "under": those of them whose session_init line counts and whose start is between the times}
-- No path leaves the database: the rows name files by path hash.
WITH wanted AS MATERIALIZED (
  SELECT DISTINCT w ->> 'hash' AS hash, w ->> 'from' AS from_at, w ->> 'to' AS to_at
  FROM jsonb_array_elements({{sessions}}) w
),
parents AS MATERIALIZED (
  SELECT h.hash, left(h.path, -length('.jsonl')) || '/' AS dir
  FROM (
    SELECT f.path, encode(sha256(convert_to(f.path, 'UTF8')), 'hex') AS hash
    FROM public.omp_session_files f
  ) h
  WHERE h.hash IN (SELECT w.hash FROM wanted w) AND right(h.path, length('.jsonl')) = '.jsonl'
),
children AS MATERIALIZED (
  SELECT p.hash AS parent, c.path
  FROM public.omp_session_files c
  JOIN parents p
    ON left(c.path, length(c.path) - strpos(reverse(c.path), '/') + 1) = p.dir
  WHERE right(c.path, length('.jsonl')) = '.jsonl'
),
heads AS MATERIALIZED (
  SELECT
    ch.parent,
    ch.path,
    hl.start,
    CASE WHEN pg_input_is_valid(hl.start, 'timestamptz') THEN hl.start::timestamptz END AS at,
    hl.init
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
placed AS (
  SELECT
    w.hash AS parent,
    w.from_at,
    w.to_at,
    h.path,
    h.start,
    h.init,
    h.init IS NOT NULL
      AND (w.from_at IS NULL OR coalesce(h.at >= w.from_at::timestamptz, false))
      AND (w.to_at IS NULL OR coalesce(h.at < w.to_at::timestamptz, false)) AS fits
  FROM wanted w
  JOIN heads h ON h.parent = w.hash
),
ranked AS (
  SELECT
    p.*,
    count(*) OVER session AS subagents,
    count(p.init) OVER session AS recorded,
    count(*) FILTER (WHERE p.fits) OVER session AS under,
    row_number() OVER (
      PARTITION BY p.parent, p.from_at, p.to_at
      ORDER BY (NOT p.fits), p.start COLLATE "C", p.path COLLATE "C"
    ) AS place
  FROM placed p
  WINDOW session AS (PARTITION BY p.parent, p.from_at, p.to_at)
)
SELECT json_build_object(
  'parent', r.parent,
  'from', r.from_at,
  'to', r.to_at,
  'hash', CASE WHEN r.fits THEN encode(sha256(convert_to(r.path, 'UTF8')), 'hex') END,
  'start', CASE WHEN r.fits THEN r.start END,
  'init', CASE WHEN r.fits THEN r.init END,
  'subagents', r.subagents,
  'recorded', r.recorded,
  'under', r.under
)::text AS subagent
FROM ranked r
WHERE r.place = 1
ORDER BY r.parent COLLATE "C", r.from_at COLLATE "C" NULLS FIRST, r.to_at COLLATE "C" NULLS FIRST;
