-- 20261123210331_unowned_release_fences.up.sql — when the release of a pod no claim owns last
-- finished, for each issue.
--
-- The release of a pod no claim owns (workflow.UnownedPodRelease) changes no claim, and it asks the
-- runtime whether the issue's Sandbox is still there from the runtime's informer store, which can
-- still hold a Sandbox the release's teardown deleted a moment before. On main's CI the next pass
-- of the rule, 45 ms after a teardown's delete, read that Sandbox present, found no mark (the
-- teardown had cleared it) and released the issue a second time (internal/daemon's
-- TestAStartAfterTheReleaseOfAnIssueWithNoLiveClaimMakesThePodAgain, job 137803188625).
--
-- released_at is the database's clock when the finish cleared the release's mark, written in that
-- same transaction (store.ClearIssueRelease, for a mark of the unowned kind), so a pass that reads
-- no mark reads the fence. The rule holds a pod whose fence is at or after its claims' newest
-- write (claims.updated_at, the same clock): that release took the pod the claims knew of, and a
-- pod made again since would have been made by a claim's start, which writes the claim.
--
-- No row is written for a release finished before this migration. Such a release finished at least
-- a pass of the rule ago, so the runtime's store no longer holds the Sandbox it deleted.
--
-- There is no down file. This directory keeps none, so the way back is the one transaction that
-- closes this header, run with every daemon of the database stopped and before the release before
-- starts, through psql '<postgres_dsn>' -X -v ON_ERROR_STOP=1. The release before this one never
-- names the table, so it needs neither the drop nor the delete to run; the delete is what makes
-- the version the store reports its own again, and with the row gone store.apply runs this file
-- again on the roll forward, which creates the table empty.
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
