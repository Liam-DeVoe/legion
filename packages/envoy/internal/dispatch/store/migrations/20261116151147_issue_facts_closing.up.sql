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
-- The columns in one statement, so issue_facts is locked once. Each is nullable or carries a
-- constant default, which Postgres records in the catalog and writes into no stored row. The
-- index is a plain build in the runner's transaction, as 20261113082157's on the same table is:
-- issue_facts holds one row per issue Legion worked, 8.5 MB for 28,000 rows in the measurement
-- that migration cites, far below the census's 1 GiB bound on a table a migration locks
-- (pgmigrate.CensusTableLimit), and the ACCESS EXCLUSIVE the alter takes, which the build holds
-- on to, lasts as long as the catalog change and one read of the table, under the runner's
-- five-second bound on the wait. The statements read no row to refuse it and rewrite none, and
-- the census answers 0.
--
-- No release before this one writes issue_facts: the table ships in this release
-- (20261111090000), so no build that names its columns without these three runs beside it. A
-- row a build of this release wrote before these columns existed holds the defaults, which read
-- as a row with no growth closed at its updated_at, and the first pass of this build reads every
-- project whole (its record names another build, or none), which writes every column of every
-- row. Rolling back past this migration is an image rollback alone.
alter table issue_facts
    add column as_of timestamptz,
    add column growth jsonb not null default '{}'::jsonb,
    add column written_build text not null default '';
create index issue_facts_written_build on issue_facts (project_key, written_build);
