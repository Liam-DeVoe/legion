SET LOCAL statement_timeout = '15min';
-- The prompt builds of the root session of each session of a list, for build.ts's read times. Oh
-- My Pi 18.18.6 gives a subagent the skills its parent loaded (task/structured-subagent.ts, lines
-- 806-818 and 895-902), so a subagent's profile reads its packages and plugins at its root's last
-- prompt build before the subagent's start; a top-level session is its own root, and its profile
-- is read at the build its step ran under. `digestTimesSql` of countsql.ts fills it, and build.ts
-- runs it through the store's read command.
--
-- Placeholder, written in the query as its name inside double braces and filled with one whole SQL
-- value:
--   hashes  the sessions' path hashes, a text array (`ARRAY['<64 hex>', …]::text[]`), each the
--           SHA-256 in hex of the file's path, as cases.ts's `pathHash` names a session
--
-- What it reads. The path of every file of public.omp_session_files, once, to hash it, as
-- session-head.sql does; for each listed session, its root: the file the topmost directory of its
-- path named as a top-level session's (`<…>Z_<id>`) stands for, that directory's path and
-- `.jsonl` (Oh My Pi writes a subagent's transcript under its parent's path less `.jsonl`,
-- task/executor.ts, line 4743, so a subagent of a subagent is below its root's directory too); a
-- file below no such directory, a top-level session, is its own root. Of each root, once however
-- many listed sessions it is the root of (`digests` is materialized), the file row's `content`
-- and each part of public.omp_session_files_parts, each part with the last 400 characters of the
-- text before it (the part before it, or the file row's `content` for the first part), so a line
-- a cut splits is read whole; of those, only the pieces that hold the text
-- `"customType":"system-prompt-digest"`, each split into its lines, as count.sql splits a piece,
-- so the pattern runs over one line at a time: Postgres's regexp_matches takes 4 bytes a byte of
-- its input in one allocation, which it refuses past 1 GB, so a match over a whole piece of 256
-- MiB or more (a file row that holds a whole older file can be one) would fail the query. Of the
-- lines that hold that text, only the `timestamp` and the `data` (the prompt's digest) of each
-- `system-prompt-digest` line (a custom entry Oh My Pi writes when the prompt changes, just before
-- the first reply under the new prompt: session/session-tools.ts, lines 1818-1827), found by one
-- pattern. Oh My Pi writes each entry as one JSON line, so no digest line spans two lines. No
-- other text leaves the database.
--
-- Rows, one JSON value per listed session the store holds (psql prints each on one line), in hash
-- order:
--   {"hash": the session's path hash,
--    "root": its root's path hash,
--    "rootHeld": whether the store holds the root's file,
--    "digests": each of the root's digest lines, earliest first, as {"at": its timestamp,
--      "digest": its data}}
WITH wanted AS MATERIALIZED (
  SELECT DISTINCT unnest({{hashes}}) AS hash
),
sessions AS MATERIALIZED (
  SELECT
    w.hash,
    coalesce(
      substring(h.path FROM '^(.*?/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9-]+Z_[0-9a-f-]+)/') || '.jsonl',
      h.path
    ) AS root
  FROM (
    SELECT f.path, encode(sha256(convert_to(f.path, 'UTF8')), 'hex') AS hash
    FROM public.omp_session_files f
  ) h
  JOIN wanted w ON w.hash = h.hash
),
roots AS MATERIALIZED (
  SELECT DISTINCT s.root FROM sessions s
),
pieces AS (
  SELECT r.root, x.piece
  FROM roots r
  CROSS JOIN LATERAL (
    SELECT f.content AS piece
    FROM public.omp_session_files f
    WHERE f.path = r.root AND coalesce(f.content, '') <> ''
    UNION ALL
    SELECT coalesce(
      right(lag(pt.content) OVER (ORDER BY pt.start_offset), 400),
      right(coalesce((SELECT f.content FROM public.omp_session_files f WHERE f.path = r.root), ''), 400)
    ) || pt.content
    FROM public.omp_session_files_parts pt
    WHERE pt.path = r.root
  ) x
),
digests AS MATERIALIZED (
  SELECT DISTINCT p.root, m[2] AS at, m[1] AS digest
  FROM pieces p
  CROSS JOIN LATERAL string_to_table(p.piece, E'\n') t(ln)
  CROSS JOIN LATERAL regexp_matches(
    t.ln,
    '"customType":"system-prompt-digest","data":"([^"]*)","id":"[^"]*","parentId":(?:"[^"]*"|null),"timestamp":"([^"]+)"',
    'g'
  ) m
  WHERE strpos(p.piece, '"customType":"system-prompt-digest"') > 0
    AND strpos(t.ln, '"customType":"system-prompt-digest"') > 0
)
SELECT json_build_object(
  'hash', s.hash,
  'root', encode(sha256(convert_to(s.root, 'UTF8')), 'hex'),
  'rootHeld', EXISTS (SELECT 1 FROM public.omp_session_files f WHERE f.path = s.root),
  'digests', coalesce((
    SELECT json_agg(
      json_build_object('at', d.at, 'digest', d.digest)
      ORDER BY d.at::timestamptz, d.digest COLLATE "C"
    )
    FROM digests d
    WHERE d.root = s.root
  ), '[]'::json)
)::text AS digest_times
FROM sessions s
ORDER BY s.hash COLLATE "C";
