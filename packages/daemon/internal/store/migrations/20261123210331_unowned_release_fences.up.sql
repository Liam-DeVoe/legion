-- 20261123210331_unowned_release_fences.up.sql — the fence of the release of a pod no claim owns,
-- for each issue.
--
-- released_at is the database's clock when the store authorized the teardown of the issue's last
-- release of a pod no claim owns, under the runtime's launch turn of the pod
-- (store.ReleaseDecision.At). The release's finish writes it in the transaction that clears the
-- release's mark (store.ClearIssueRelease, for a mark of the unowned kind). The rule reads it beside
-- the newest write to the issue's claims (claims.updated_at, the same clock), and
-- workflow.UnownedPodRelease says why.
--
-- No row is written for a release finished before this migration. Such a release finished at least
-- a pass of the rule ago, so the runtime's store no longer holds the Sandbox it deleted.
--
-- There is no down file. This directory keeps none, so the way back is the one transaction that
-- closes this header, run with every daemon of the database stopped and before the release before
-- starts, through psql '<postgres_dsn>' -X -v ON_ERROR_STOP=1. The release before this one never
-- names the table, so it needs neither the drop nor the delete to run. The way back does not move
-- the version the store reports: the store reports its highest recorded version
-- (store.Store.SchemaVersion), and 20261123212127, which every release with this file carries,
-- stands above this one before the delete and after it. The delete is there for the roll forward
-- alone: store.apply runs this file again only once its row is gone, and that run creates the
-- table again, empty.
--
-- begin;
-- drop table if exists unowned_release_fences;
-- delete from schema_version where version = 20261123210331;
-- commit;
create table unowned_release_fences (
  project text not null,
  issue text not null,
  released_at timestamptz not null,
  primary key (project, issue)
);
