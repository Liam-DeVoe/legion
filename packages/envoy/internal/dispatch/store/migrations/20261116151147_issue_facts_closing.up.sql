-- 20261116151147_issue_facts_closing.up.sql
--
-- Gives each stored row of the per-issue Legion facts (20261111090000, dispatch://INFRA-2916,
-- R21) what a read needs to give each fact at the read's own clock, and the build that wrote it
-- (R27), so no timer rewrites a row:
--   as_of          the moment the row's values are closed at: the newest input the issue's facts
--                  read (legion/metrics.ProjectFactsClosed). Every part of the issue still
--                  running then - its current phase, its current segment, its current wait -
--                  began at or before it, and no clock moves the values, so a recomputation whose
--                  inputs did not move writes the same row and logs no change. Null for a row no
--                  build of this release wrote, which a reader closes at the row's updated_at.
--   growth         how fast each fact grows after as_of, in hours an hour, by the fact's name
--                  (metrics.Growth: `work_hours`, `phase_hours.implementing`,
--                  `bars.implementer.segments.idle`, `idle_by_role.reviewer`, and `work_last_at`
--                  at 1 for a bar still open, whose end runs with the clock). A read adds each
--                  rate times the hours from as_of to its own clock (legionfacts.Row.At), which
--                  is the one computation a read makes. {} for a closed issue, whose facts stand
--                  still, but for a slot it never gave up.
--   written_build  the build that wrote the row (legionfacts.Deps.Build), '' for a row no build
--                  of this release wrote. The build a project's record of its whole read names
--                  (issue_facts_computed.computed_build, 20261112193821) computes again every row
--                  of that project another build wrote, which is how a roll ends with no timer:
--                  the older build's last write logs a change, whose drain wakes the newer
--                  process, whose pass computes that row again (legionfacts.otherBuildsRows).
-- and issue_facts_written_build, (project_key, written_build), which that read takes as two
-- ranges of each project: the rows whose writer sorts before the running build and those that
-- sort after it. A settled deployment holds neither, so the read is two probes a project.
--
-- issue_filings (20261113082155), which the same passes write, gains its own written_build, the
-- build that wrote the filing, '' for a filing no build of this release wrote, for the same reason:
-- the rule that decides whose filing an issue is lives in the build, so the build a project's
-- record names computes again every filing of that project another build wrote
-- (legionfacts.otherBuildsFilings), through issue_filings_written_build, (project_key,
-- written_build), read as the rows' index is.
--
-- It drops issue_facts_open (20261111090000), the partial index over the open rows that served the
-- half-minute sweep's read of them and nothing else. The columns above let a read give an open
-- issue's facts at its own clock, so this change ends that sweep, no statement reads through the
-- index any more, and keeping it would cost a write on every upsert of an open row. The drop takes
-- the ACCESS EXCLUSIVE the alter already holds, and reads and rewrites no row.
--
-- The columns of each table in one statement, so each table is locked once. Each column is
-- nullable or carries a constant default, which Postgres records in the catalog and writes into
-- no stored row. Each index is a plain build in the runner's transaction, as 20261113082157's on
-- issue_facts is: issue_facts holds one row per issue Legion worked, 8.5 MB for 28,000 rows in the
-- measurement that migration cites, and issue_filings one row per issue a Legion session filed,
-- fewer, both far below the census's 1 GiB bound on a table a migration locks
-- (pgmigrate.CensusTableLimit), and the ACCESS EXCLUSIVE each alter takes, which its build holds
-- on to, lasts as long as the catalog change and one read of the table, under the runner's
-- five-second bound on the wait. It takes issue_facts before issue_filings, the order the one
-- writer of both takes them in (legionfacts' writePass writes a project's rows before its
-- filings), so it closes no cycle with a write. The statements read no row to refuse it and
-- rewrite none, and the census answers 0.
--
-- No release before this one writes issue_facts or issue_filings: both tables ship in this
-- release (20261111090000, 20261113082155), so no build that names their columns without these
-- runs beside it. A row a build of this release wrote before these columns existed holds the
-- defaults, which read as a row with no growth closed at its updated_at and written by another
-- build, and the first pass of this build reads every project whole (its record names another
-- build, or none), which writes every column of every row and every filing. Rolling back past
-- this migration is an image rollback alone.
alter table issue_facts
    add column as_of timestamptz,
    add column growth jsonb not null default '{}'::jsonb,
    add column written_build text not null default '';
create index issue_facts_written_build on issue_facts (project_key, written_build);
alter table issue_filings add column written_build text not null default '';
create index issue_filings_written_build on issue_filings (project_key, written_build);
drop index issue_facts_open;
