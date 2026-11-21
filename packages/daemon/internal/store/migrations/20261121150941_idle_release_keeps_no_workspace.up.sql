-- 20261121150941_idle_release_keeps_no_workspace.up.sql — the daemon keeps no saved workspace:
-- workspace_bundles goes, and idle_release_declines records the idle release the daemon last
-- declined for each issue, so the idle pass does not ask the issue's pod again until a role of it
-- takes a turn.
--
-- What this takes out: the table workspace_bundles, which 20261101222725_workspace_bundles.up.sql
-- made and 20261102174500, 20261102224250 and 20261103214558 changed. An idle release saved a
-- released pod's unpushed work there as a git bundle, and the next pod's launcher restored it.
-- This release saves nothing and restores nothing: an idle release takes a pod only when the
-- workspace read finds no commit that no branch or tag on origin holds and no working-copy change
-- (internal/daemon's outbox, checkIssueWorkspace). Every row the table still holds is work that
-- no pod restored, or a confirmation that never arrived. An operator copies out the rows worth
-- keeping before this release runs (docs/kubernetes.md, "Rolling back the release that keeps no
-- saved workspace", says how); the drop deletes the rest with the table.
--
-- What this adds: idle_release_declines, one row per issue whose release the idle check declined,
-- written in the transaction that cancels that release (store.Store.DeclineIssueRelease). The row
-- names the role the release was read through and told, why it declined (reason: unpushed_work, or
-- workspace_unread where the read failed), what the read found (head, commits, changed), and
-- started_row, the newest start row of the issue's claims at the decline. The idle rule keeps the
-- pod under that reason, with no new mark and no new read, until a role of the issue starts a turn
-- after declined_at (claims.last_turn_start) or a role process is started again (a start row above
-- started_row); a workspace_unread decline also ends an hour after declined_at
-- (workflow.IdleRelease).
--
-- The release before reads and writes workspace_bundles at every idle pass and every role start,
-- and reads no row of idle_release_declines. A rollback to it therefore runs the way back, the
-- last paragraph of this header (migrations.WayBack), with every daemon of the database stopped,
-- before the earlier image is pinned. It makes workspace_bundles again, empty and in the shape the
-- release before reads, drops idle_release_declines, which the release before never reads, and
-- deletes this migration's version row, so a roll forward runs this file again. Every daemon that
-- shares the database moves together, on the way up and on the way back, since both tables hold
-- every project's rows. docs/kubernetes.md, under the heading above, carries the same statements,
-- and internal/store's TestTheWayBackOfTheReleaseThatKeepsNoSavedWorkspace runs these.
--
-- create table public.workspace_bundles (issue text primary key, bundle bytea not null, size_bytes bigint not null, at timestamptz not null, head text not null, tips jsonb not null default '[]'::jsonb, bookmarks jsonb not null default '{}'::jsonb, release_id bigint not null check (release_id > 0), attempt_pod_uid text, attempt_at timestamptz, source_pod_uid text not null check (source_pod_uid <> ''), attempt_outcome text, constraint workspace_bundles_attempt_whole check ((attempt_pod_uid is null) = (attempt_at is null)), constraint workspace_bundles_attempt_outcome check (case when attempt_pod_uid is null then attempt_outcome is null else attempt_outcome in ('sent', 'refused_older') end));
-- drop table public.idle_release_declines;
-- delete from public.schema_version where version = 20261121150941;
drop table workspace_bundles;
create table idle_release_declines (
    project     text        not null,
    issue       text        not null,
    declined_at timestamptz not null,
    role        text        not null check (role <> ''),
    reason      text        not null check (reason in ('unpushed_work', 'workspace_unread')),
    head        text        not null default '',
    commits     integer     not null default 0 check (commits >= 0),
    changed     boolean     not null default false,
    started_row bigint      not null default 0 check (started_row >= 0),
    primary key (project, issue)
);
