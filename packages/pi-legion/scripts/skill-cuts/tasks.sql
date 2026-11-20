SET LOCAL statement_timeout = '15min';
-- The task query of R6's load-test tasks: the first user message of each session
-- whose first user message holds one of a list of terms. `tasksSql` of countsql.ts fills it.
--
-- Placeholders, each written in the query as its name inside double braces and filled with one
-- whole SQL value:
--   day_start     the first mtime_ms of the files read, epoch milliseconds, inclusive
--   day_end       the mtime_ms bound, epoch milliseconds, exclusive (one day at a time keeps a
--                 query within its timeout, as for the count)
--   window_start  an ISO instant string: the first user message's line timestamp is at or after it
--   window_end    an ISO instant string: that timestamp is before it
--   terms         a text array (`ARRAY['term', …]::text[]`); a message holds a term when it holds
--                 its text, letter case aside
--
-- What it reads: per file, the file row's content and the parts that start in the first MiB
-- (start_offset < 1048576), joined in offset order, one file at a time; the text after their last
-- newline is left out, since it may be cut. The first user message is the first line that is an
-- Oh My Pi user message (`"type": "message"`, `message.role` `user`) or a Claude Code prompt
-- (`"type": "user"`, not `isMeta`, holding no `tool_result`); its text is its string content, or
-- its text blocks joined by newlines. A session whose first user message does not end in that
-- first MiB gives no row.
--
-- Rows, by path, each one JSON value: {"path": <the file's path>, "text": <the message's first
-- 4,000 characters>}.
WITH heads AS (
  SELECT f.path, h.txt
  FROM public.omp_session_files f
  CROSS JOIN LATERAL (
    SELECT coalesce(f.content, '')
      || coalesce(string_agg(pt.content, '' ORDER BY pt.start_offset), '') AS txt
    FROM public.omp_session_files_parts pt
    WHERE pt.path = f.path AND pt.start_offset < 1048576
  ) h
  WHERE f.mtime_ms >= {{day_start}} AND f.mtime_ms < {{day_end}}
),
firsts AS (
  SELECT hd.path, fu.j
  FROM heads hd
  CROSS JOIN LATERAL (
    SELECT u.j
    FROM (
      SELECT t.n, CASE WHEN pg_input_is_valid(t.ln, 'jsonb') THEN t.ln::jsonb END AS j
      FROM string_to_table(
        left(hd.txt, greatest(length(hd.txt) - strpos(reverse(hd.txt), E'\n'), 0)),
        E'\n'
      ) WITH ORDINALITY AS t(ln, n)
      WHERE strpos(hd.txt, E'\n') > 0 AND strpos(t.ln, '"user"') > 0
    ) u
    WHERE (u.j ->> 'type' = 'message' AND u.j -> 'message' ->> 'role' = 'user')
      OR (u.j ->> 'type' = 'user'
        AND (u.j -> 'isMeta') IS DISTINCT FROM 'true'::jsonb
        AND NOT jsonb_path_exists(u.j, '$.message.content[*] ? (@.type == "tool_result")'))
    ORDER BY u.n
    LIMIT 1
  ) fu
),
texts AS (
  SELECT fi.path,
    CASE
      WHEN pg_input_is_valid(fi.j ->> 'timestamp', 'timestamptz') THEN (fi.j ->> 'timestamp')::timestamptz
    END AS ts,
    CASE jsonb_typeof(fi.j -> 'message' -> 'content')
      WHEN 'string' THEN fi.j -> 'message' ->> 'content'
      WHEN 'array' THEN (
        SELECT string_agg(x.e ->> 'text', E'\n' ORDER BY x.n)
        FROM jsonb_array_elements(fi.j -> 'message' -> 'content') WITH ORDINALITY AS x(e, n)
        WHERE x.e ->> 'type' = 'text'
      )
    END AS text
  FROM firsts fi
)
SELECT json_build_object('path', tx.path, 'text', left(tx.text, 4000))::text AS task
FROM texts tx
WHERE tx.text IS NOT NULL
  AND tx.ts >= {{window_start}}::timestamptz AND tx.ts < {{window_end}}::timestamptz
  AND EXISTS (
    SELECT 1 FROM unnest({{terms}}) AS term(t)
    WHERE term.t <> '' AND strpos(lower(tx.text), lower(term.t)) > 0
  )
ORDER BY tx.path COLLATE "C";
