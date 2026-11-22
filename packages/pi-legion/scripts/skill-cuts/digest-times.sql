SET LOCAL statement_timeout = '15min';
-- The times of the prompt builds of the root session of each subagent session of a list, for
-- build.ts's profile of a subagent: Oh My Pi 18.18.6 gives a subagent the skills its parent loaded
-- (task/structured-subagent.ts, lines 806-818 and 895-902), so the packages and plugins of a
-- subagent's profile are read at its root's last prompt build before the subagent's start.
-- `digestTimesSql` of countsql.ts fills it, and build.ts runs it through the store's read command.
--
-- Placeholder, written in the query as its name inside double braces and filled with one whole SQL
-- value:
--   sessions  the subagent sessions, a JSON array (`'[…]'::jsonb`) of objects
--             {"hash": <64 hex>, "before": <ISO time>}: each the SHA-256 in hex of the file's path,
--             as cases.ts's `pathHash` names a session, and the subagent's start
--
-- What it reads. The path of every file of public.omp_session_files, once, to hash it, as
-- session-head.sql does; for each listed subagent, its root: the file the topmost directory of its
-- path named as a top-level session's (`<…>Z_<id>`) stands for, that directory's path and
-- `.jsonl` (Oh My Pi writes a subagent's transcript under its parent's path less `.jsonl`,
-- task/executor.ts, line 4743, so a subagent of a subagent is below its root's directory too).
-- Of the root, the file row's `content` and each part of public.omp_session_files_parts, each
-- part with the last 400 characters of the part before it, so a line a part's end cuts is read
-- whole; of those, only the `timestamp` of each `system-prompt-digest` line (a custom entry, one
-- per prompt build: session/session-tools.ts, lines 1795-1830), found by one pattern. No other
-- text leaves the database.
--
-- Rows, one JSON value per listed subagent the store holds (psql prints each on one line), in hash
-- order:
--   {"hash": the subagent's path hash,
--    "root": its root's path hash, or null when its path names no root directory,
--    "rootHeld": whether the store holds the root's file,
--    "before": the subagent's start, as given,
--    "digests": the root's digest lines' timestamps before that start, earliest first}
WITH wanted AS MATERIALIZED (
  SELECT w ->> 'hash' AS hash, w ->> 'before' AS before
  FROM jsonb_array_elements({{sessions}}) w
),
subagents AS MATERIALIZED (
  SELECT
    w.hash,
    w.before,
    substring(h.path FROM '^(.*?/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9-]+Z_[0-9a-f-]+)/') || '.jsonl'
      AS root
  FROM (
    SELECT f.path, encode(sha256(convert_to(f.path, 'UTF8')), 'hex') AS hash
    FROM public.omp_session_files f
  ) h
  JOIN wanted w ON w.hash = h.hash
),
pieces AS (
  SELECT s.hash, x.piece
  FROM subagents s
  CROSS JOIN LATERAL (
    SELECT f.content AS piece
    FROM public.omp_session_files f
    WHERE f.path = s.root AND coalesce(f.content, '') <> ''
    UNION ALL
    SELECT coalesce(right(lag(pt.content) OVER (ORDER BY pt.start_offset), 400), '') || pt.content
    FROM public.omp_session_files_parts pt
    WHERE pt.path = s.root
  ) x
),
digests AS (
  SELECT DISTINCT p.hash, m[1] AS at
  FROM pieces p
  CROSS JOIN LATERAL regexp_matches(
    p.piece,
    '"customType":"system-prompt-digest","data":"[^"]*","id":"[^"]*","parentId":(?:"[^"]*"|null),"timestamp":"([^"]+)"',
    'g'
  ) m
)
SELECT json_build_object(
  'hash', s.hash,
  'root', CASE WHEN s.root IS NULL THEN NULL ELSE encode(sha256(convert_to(s.root, 'UTF8')), 'hex') END,
  'rootHeld', EXISTS (SELECT 1 FROM public.omp_session_files f WHERE f.path = s.root),
  'before', s.before,
  'digests', coalesce((
    SELECT json_agg(d.at ORDER BY d.at::timestamptz)
    FROM digests d
    WHERE d.hash = s.hash AND d.at::timestamptz < s.before::timestamptz
  ), '[]'::json)
)::text AS digest_times
FROM subagents s
ORDER BY s.hash COLLATE "C";
