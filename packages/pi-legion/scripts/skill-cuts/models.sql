SET LOCAL statement_timeout = '15min';
-- The model of each session of one session store and one UTC day (or hour) of file mtime, for the
-- behaviour test's model population (R2): which models the sessions that read a skill ran on.
-- `countsql.ts --query models --config <skill-cuts.json> --store <db> --day <YYYY-MM-DD>
-- [--hour <0-23>]` prints it with every placeholder filled; that text goes into the store's read
-- command unchanged, as count.sql's does. The files are the same as count.sql's query of that
-- store and day reads, so each file hash of the count has its model in the query of its day.
--
-- Placeholders, each written in the query as its name inside double braces and filled with one
-- whole SQL value:
--   store      the store's database name, a string: a value of each row, never a table name
--   day_start  the first mtime_ms of the files read, epoch milliseconds, inclusive
--   day_end    the mtime_ms bound, epoch milliseconds, exclusive
-- (A session's model does not depend on the read window, so count.sql's window placeholders are
-- not here.)
--
-- What it reads. The files of public.omp_session_files whose mtime_ms is in [day_start, day_end),
-- and of each only its head: the file row's `content`, then the first part of
-- public.omp_session_files_parts (the lowest start_offset, read by the primary key), never the
-- rest of the file. The head's lines that hold `"type":"model_change"`, `"type":"session_init"`,
-- `"type":"assistant"` or `"attachment":{"type":"model"` are parsed as jsonb; one that is not one
-- JSON value (the head's last line, cut where the first part ends) is counted in bad_lines and
-- read no further.
--
-- The model of a file, from its head, the first of these sources that gives one (`source`):
--   model_change      Oh My Pi: the first `model_change` line's `model` (`provider/modelId`),
--                     which a subagent's file holds too, before its session_init line;
--   session_init      Oh My Pi writes one only in a subagent's file: its `resolvedModel`, the
--                     provider/model selector the subagent was given (a thinking suffix such as
--                     `:low` kept as written). It follows the subagent's whole system prompt, at
--                     the end of the file's longest line, so a first part that ends inside that
--                     line gives none;
--   assistant         Claude Code: the first assistant message's `message.model` that is not
--                     `<synthetic>` (Claude Code's model id, which names no provider);
--   model attachment  Claude Code: the `model` attachment's `identity.modelId`, which Claude Code
--                     writes near the file's start, before its first assistant message;
-- else none: the model is null (the head holds no such line, as when the first part ends before
-- the session's first model line).
--
-- Rows: one JSON value per row (psql prints each on one line), the files in hash order, then the
-- summary:
--   {"row": "model", "store", "hash" (md5 of the path cut to 16 hex characters, as count.sql names
--    a file), "model" (or null), "source" (one of the sources above, or null)}
--   {"row": "summary", "store", "mtime_from", "mtime_to", "files" (files read), "with_model"
--    (files whose model is not null), "bytes" (bytes read: each head), "lines_parsed",
--    "bad_lines"}
-- No path or line text leaves the database.
-- Each head is cut to its first MiB (the file row's content, then the first part): a file row can
-- hold a whole older file of hundreds of MB, and the first model line is near the file's start; a
-- head left whole filled the server's temp_file_limit on four devbox days (2026-11-20 run).
WITH
heads AS MATERIALIZED (
  SELECT f.path,
    substr(coalesce(substr(f.content, 1, 1048576), '') || coalesce(substr(fp.content, 1, 1048576), ''), 1, 1048576) AS head
  FROM public.omp_session_files f
  LEFT JOIN LATERAL (
    SELECT pt.content
    FROM public.omp_session_files_parts pt
    WHERE pt.path = f.path
    ORDER BY pt.start_offset
    LIMIT 1
  ) fp ON true
  WHERE f.mtime_ms >= {{day_start}} AND f.mtime_ms < {{day_end}}
),
parsed AS MATERIALIZED (
  SELECT h.path, t.n, CASE WHEN pg_input_is_valid(t.ln, 'jsonb') THEN t.ln::jsonb END AS j
  FROM heads h
  CROSS JOIN LATERAL string_to_table(h.head, E'\n') WITH ORDINALITY AS t(ln, n)
  WHERE strpos(t.ln, '"type":"model_change"') > 0
    OR strpos(t.ln, '"type":"session_init"') > 0
    OR strpos(t.ln, '"type":"assistant"') > 0
    OR strpos(t.ln, '"attachment":{"type":"model"') > 0
),
found AS (
  SELECT p.path, p.n, m.source, m.rank, m.model
  FROM parsed p
  CROSS JOIN LATERAL (
    SELECT 'model_change' AS source, 0 AS rank, p.j ->> 'model' AS model
    WHERE p.j ->> 'type' = 'model_change'
    UNION ALL
    SELECT 'session_init', 1, p.j ->> 'resolvedModel'
    WHERE p.j ->> 'type' = 'session_init'
    UNION ALL
    SELECT 'assistant', 2, p.j -> 'message' ->> 'model'
    WHERE p.j ->> 'type' = 'assistant' AND p.j -> 'message' ->> 'model' <> '<synthetic>'
    UNION ALL
    SELECT 'model attachment', 3, p.j -> 'attachment' -> 'identity' ->> 'modelId'
    WHERE p.j -> 'attachment' ->> 'type' = 'model'
  ) m
  WHERE p.j IS NOT NULL AND m.model <> ''
),
chosen AS (
  SELECT DISTINCT ON (f.path) f.path, f.model, f.source
  FROM found f
  ORDER BY f.path, f.rank, f.n
)
SELECT o.total
FROM (
  SELECT 0 AS pos, left(md5(h.path), 16) AS hash,
    json_build_object(
      'row', 'model', 'store', {{store}}, 'hash', left(md5(h.path), 16), 'model', c.model,
      'source', c.source
    )::text AS total
  FROM heads h
  LEFT JOIN chosen c ON c.path = h.path
  UNION ALL
  SELECT 1, NULL,
    json_build_object(
      'row', 'summary', 'store', {{store}}, 'mtime_from', {{day_start}}::bigint,
      'mtime_to', {{day_end}}::bigint,
      'files', (SELECT count(*) FROM heads),
      'with_model', (SELECT count(*) FROM chosen),
      'bytes', (SELECT coalesce(sum(octet_length(h.head)), 0) FROM heads h),
      'lines_parsed', (SELECT count(*) FROM parsed),
      'bad_lines', (SELECT count(*) FROM parsed WHERE j IS NULL)
    )::text
) o
ORDER BY o.pos, o.hash COLLATE "C";
