-- 20261123234016_issue_pod_disks.up.sql — the disks of two kinds of pod of an issue, one row per
-- issue and pod: a pod that a take sent a release's bundle, and a pod of the issue's kept volume in
-- which a role started. Each row says whether the pod's Sandbox owns the issue's kept volume, and
-- since when, and whether the pod's disk holds work from before the pod, which the issue's update
-- round reads from the store (dispatch://INFRA-4425).
--
-- An idle release saves the unpushed work of the workspace that every role of the issue shares as
-- a bundle (workspace_bundles). The issue's next pod takes it before any role starts in it: the
-- pod a restore is sent to (store.TakeWorkspaceBundleForRestore, attempt_outcome `sent`), or the
-- pod whose Sandbox owns the issue's kept volume and was made before the save, which mounts the
-- volume the work was taken off, so the store drops the saved copy (store.StartKeptPod).
-- A kept volume outlives its pod with no release too: an eviction, a launch's replacement and an
-- update round's replacement keep it for the issue's next pod, until a reset mark or a change of
-- the disk's shape replaces the Sandbox with its volume. So a pod that mounts a kept volume an
-- earlier pod of the issue mounted holds what the roles of that earlier pod left unpushed. The
-- bundle's own row cannot say which pod holds what for as long as the update round asks: the
-- confirmed restore deletes it (store.DeleteRestoredWorkspaceBundle) and so does the drop, each
-- before any role of the pod starts, and each must, since a row still held stops every release of
-- the issue (workspaceBundleUnrestored).
--
-- pod_uid is the pod. kept_since is when the API server made the pod's Sandbox, where that Sandbox
-- owns the issue's kept volume, and it names the volume, since the volume goes only with its
-- Sandbox; it is null for a pod the store records only as the pod a restore was sent to.
-- holds_earlier_work is whether the pod's disk holds work from before the pod: a release's restore,
-- a kept volume's drop, or a kept volume that an earlier pod of the issue mounted. Two statements
-- write a row, each in the transaction of the bundle's own statement. The take that sends a bundle
-- writes the pod with no kept_since and marked, or marks the pod's row; a take refused as older
-- than the save, and one that found no bundle of the asking project's, write nothing. A role's
-- start in a pod whose Sandbox owns a kept volume (store.StartKeptPod) runs before the take, at
-- every start, whether or not the issue has a bundle, and at every call whose project records the
-- issue it records the pod with its kept_since, marked where it deleted a bundle or where another
-- pod of the issue has a row with the same kept_since; a start whose record fails goes no further,
-- so no role runs in a pod of a kept volume that has no row. The mark is decided at that write: a
-- Sandbox runs one pod at a time, so at a pod's first record every other row of its volume is an
-- earlier pod's, while the update round can read a pod after the next pod of its volume has
-- recorded. A later pod of a volume never marks an earlier one, so the first pod of a volume is
-- marked only for its own drop or take. No write clears a mark, and every row has a kept_since or
-- a mark, since the start writes a kept_since and the take a mark: the table refuses a row with
-- neither. internal/daemon's update round reads the mark of its pod (store.PodHoldsEarlierWork,
-- workOnRoundPod).
--
-- The rows of an issue go when the issue's work is over: the outbox's dropReleasedWork, at the
-- issue's close and at its tree's close, which deletes the issue's bundle too
-- (store.DeleteIssuePodDisks). A release that saved nothing unpushed deletes the bundle and none of
-- these rows: a pod that holds an earlier release's work still holds it.
--
-- The migration adds a table and changes none that an earlier release reads, so a daemon of the
-- release before runs over the migrated database, as a rollback leaves it: it writes no row of the
-- table and deletes none. After a roll forward, a pod that took a release's work while that daemon
-- ran has no row, and neither has a pod of a kept volume that ran under it, so neither is marked;
-- where the earlier pods of a kept volume ran before the roll forward, the first pod of that
-- volume that this release records is not marked for them, only for its own drop or take. An end
-- over a pod that is not marked names a role by its set alone; the roll forward's restart reads
-- the set of every claim that showed a session as unknown, so such a claim is named. The rows of
-- an issue whose work ended while that daemon ran stay, and only the records and the rounds of
-- that issue's own pods read them. No statement is owed on the way back, so this header carries
-- none.
create table issue_pod_disks (
    issue              text        not null,
    pod_uid            text        not null check (pod_uid <> ''),
    kept_since         timestamptz,
    holds_earlier_work boolean     not null,
    primary key (issue, pod_uid),
    check (kept_since is not null or holds_earlier_work)
);
