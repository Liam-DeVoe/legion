-- 20261123234016_workspace_bundle_pods.up.sql — the pods that took an idle release's work, one row
-- per issue and pod, so the issue's update round over such a pod reads from the store which pod
-- holds that work (dispatch://INFRA-4425).
--
-- An idle release saves the unpushed work of the workspace that every role of the issue shares as
-- a bundle (workspace_bundles). The issue's next pod takes it: the pod a restore is sent to
-- (store.TakeWorkspaceBundleForRestore, attempt_outcome `sent`), or the pod whose Sandbox owns the
-- issue's kept volume, which mounts the volume the work was taken off, so the store drops the saved
-- copy (store.DropHeldWorkspaceBundle). Either way that pod's disk holds the whole shared
-- workspace's work before any role starts in it, the work of a role that is not running included.
-- The bundle's own row cannot say which pod that was for as long as the update round asks: the
-- confirmed restore deletes it (store.DeleteRestoredWorkspaceBundle) and so does the drop, each
-- before any role of the pod starts, and each must, since a row still held stops every release of
-- the issue (workspaceBundleUnrestored).
--
-- pod_uid is that pod, release_id the idle release whose work it took (issue_releases.id, as the
-- bundle carried it), and at when it took it, by the database's clock. The row is written in the
-- transaction of the take that sent the bundle, or of the drop, and in no other: a take refused as
-- older than the save sends nothing, and a take or a drop that found no bundle of the asking
-- project's touches nothing. Each write is an upsert on (issue, pod_uid), so a second take or drop
-- for the same issue and pod does not fail its transaction. internal/daemon's update round reads
-- it at each end of a round over the pod (store.PodTookWorkspaceBundle): at a round over such a
-- pod, every end names every member of the issue that has a session or a pod in its set and had
-- not answered (workOnRoundPod), but for one exception. The replacement through a process in the
-- pod names, of the members the round passed over, only those with a session (notWaitedFor,
-- notAskedToPush), as docs/kubernetes.md says of the read of the workspace. The store holds the
-- record, so a daemon restart between the take and the round's end loses none of it.
--
-- The rows of an issue go when the issue's work is over: the outbox's dropReleasedWork, at the
-- issue's close and at its tree's close, which deletes the issue's bundle too
-- (store.DeleteWorkspaceBundlePods). A release that saved nothing unpushed deletes the bundle and
-- none of these rows: the pod that took an earlier release's work still holds it.
--
-- The migration adds a table and changes none that an earlier release reads, so a daemon of the
-- release before runs over the migrated database, as a rollback leaves it: it writes no row of the
-- table and deletes none. After a roll forward, a pod that took a release's work while that
-- daemon ran has no row, so an end over it names a role by its set alone; the roll forward's
-- restart reads the set of every claim that showed a session as unknown, so such a claim is named.
-- The rows of an issue whose work ended while it ran stay, and only an end over one of that
-- issue's pods reads them. No statement is owed on the way back, so this header carries none.
create table workspace_bundle_pods (
    issue      text        not null,
    pod_uid    text        not null check (pod_uid <> ''),
    release_id bigint      not null check (release_id > 0),
    at         timestamptz not null,
    primary key (issue, pod_uid)
);
