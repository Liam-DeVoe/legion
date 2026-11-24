-- 20261124143026_issue_release_teardown.up.sql — a release's teardown records the release once
-- (dispatch://INFRA-3149).
--
-- What this changes, first: issue_releases gains teardown, '' or 'reached'. The teardown of the
-- release of a pod no role can use writes 'reached' just before the runtime's first write to the
-- issue's Sandbox, and records its release when the runtime took a Sandbox or the mark says
-- 'reached': an attempt that took the pod and then failed, or whose daemon died, before its record
-- leaves its retry no Sandbox to take, and the retry still records the release. The rule and the
-- window it leaves are stated once, on store.ReleaseTeardown.
--
-- Why every standing unowned mark starts at 'reached': such a mark is a release in flight, and the
-- daemon this migration replaces may have begun its teardown — it stops a row in progress at its
-- next settle, so a teardown that had its delete accepted, or its kept Sandbox's record written,
-- leaves the mark standing — and that daemon recorded a release whatever its teardown found. Read
-- as '', the new daemon would retry it, find no Sandbox and record nothing for a release that took
-- the pod. Read as 'reached', it records the release whatever the runtime answers, which is the
-- rule the mark was made under. An idle mark keeps '': the idle release records its release
-- whatever the runtime answers, and reads no note.
--
-- What this changes, second: pod_releases gains issue_release_id, the id of the release's mark
-- (issue_releases.id) that the record stands for, and one record per mark at most
-- (pod_releases_one_per_issue_release). A teardown records the release and then clears the mark
-- in a transaction of its own, so a teardown that fails, or whose daemon dies, between the two runs
-- again over the standing mark and records the release again; its insert now names the mark and
-- takes the record it already wrote (Store.RecordPodRelease). A record of a release before this
-- migration, and one an earlier release writes, names no mark: the column is null, and the index
-- holds none of them.
--
-- There is no down file: migrations here are forward-only. An earlier release names its columns in
-- every read and write of issue_releases and pod_releases, and its inserts leave both new columns
-- to their defaults, so it reads and writes both tables as it did and records a release whatever
-- its teardown found, as it always did; a rollback needs nothing put back.
alter table issue_releases add column teardown text not null default ''
  check (teardown in ('', 'reached'));
update issue_releases set teardown = 'reached' where kind = 'unowned';
alter table pod_releases add column issue_release_id bigint;
create unique index pod_releases_one_per_issue_release on pod_releases (issue_release_id)
  where issue_release_id is not null;
