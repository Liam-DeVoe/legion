-- 20261117100021_eval_days_refresher.up.sql
--
-- The day-row refresher's own state (dispatch://INFRA-2916, R12; evals.Store.RunRefresher), which
-- keeps every project's eval_days true to its inputs: where it stands in the two feeds it reads,
-- and how far each of its walks over the days has come.
--
-- eval_days_cursor holds one row per feed the refresher reads, by name: `delivery_changes` (the
-- seq of the last change a committed pass read) and `events` (the id of the last event, at or
-- below the event log's safe horizon, a committed pass read). at is, on the `delivery_changes`
-- row, when the change at that seq was recorded (its delivery_changes.at), and null on the
-- `events` row and for a seq no change holds: a pass that finds the change at its cursor gone from
-- the log reads it to tell a change pruned after its 24 hours (delivery.PruneChanges), which loses
-- nothing, from a log that lost changes the cursor never read. updated_at is, on the `events` row,
-- the moment the pass that last wrote the row ran as of, which every pass stamps whether or not its
-- feed held anything new, and on the `delivery_changes` row when a page last read the change log to
-- its end (delivery.ChangeCursor's ReadAt), which a full page leaves where it stood; so
-- evals.Store.RefresherAge reads how long ago the last pass ran or a pass last read the whole
-- change log, whichever is older. It is a table of its own rather than rows of
-- issue_facts_cursor: legionfacts.CursorAge reads that table's oldest row as the age of the Legion
-- facts, and a row of another loop would stand in its answer.
--
-- eval_days_walks holds one row per walk the refresher makes over a span of days: the delivery and
-- CI rows of every project (family `delivery`, project_key ''), since one read of a day's delivery
-- rows builds every project's; the Legion rows of one project (family `legion`); the zero rows of
-- one project's logged evals (family `logged`); and one project's unowned-p0 row of today (family
-- `unowned`), a walk of one day at a time that stands at tomorrow once today's row is written.
--   built_by   the build whose walk this is, part of the walk's key: each build makes walks of
--              its own, begun at its start, which is how a release rebuilds every row of a family
--              an older build wrote, and while two builds run (a rolling deploy), each continues its
--              own walk rather than beginning again the one the other recorded, and neither stops
--              the other from reaching today. A build whose walk reaches today drops another
--              build's record of it that has not moved for two days, a build no process runs.
--   next_day   the first day the walk has not built yet; null while the walk is to begin, at the
--              first input day of what it walks. A walk that has built today stands at tomorrow,
--              and the first pass of a new UTC day builds that day.
--   updated_at when the walk last moved.
--
-- Two new tables, both empty: nothing existing is read or rewritten, and the release before this
-- one neither reads nor writes either, so the two builds roll together (cmd/dispatch/README.md,
-- "Database migrations"). Neither holds a foreign key, as issue_facts_computed holds none: the
-- delivery walk's project_key is '' and names no project. The census answers 0, and rolling back
-- past it is an image rollback alone.
create table eval_days_cursor (
    name text primary key,
    position bigint not null default 0,
    at timestamptz,
    updated_at timestamptz not null default now()
);

create table eval_days_walks (
    family text not null check (family in ('delivery', 'legion', 'logged', 'unowned')),
    project_key text not null default '',
    built_by text not null,
    next_day date,
    updated_at timestamptz not null default now(),
    primary key (family, project_key, built_by)
);
