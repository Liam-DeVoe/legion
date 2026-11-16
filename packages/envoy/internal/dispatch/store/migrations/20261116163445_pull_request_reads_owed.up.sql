-- 20261116163445_pull_request_reads_owed.up.sql
--
-- The pull requests a `pull_request` event named whose read of GitHub the intake could not
-- finish, held until a read of GitHub answers for them (dispatch://INFRA-3992, R1 and R2).
--
--   repo, number  the pull request, as pull_request_heads names one: the repository under its
--                 canonical name (refs.CanonicalRepo) and the number.
--   owed_at       when the read was last owed; recording it again moves it.
--
-- R2 makes GitHub's answer the only source of a pull request's head, so an event the intake gives
-- up on would leave its pull request with no head: the intake acknowledges an envelope after 30
-- failed deliveries (delivery.headWriteMaxDeliveries), which a GitHub outage of about five
-- minutes reaches, and acknowledges GitHub's 404 for a pull request, which a read made a moment
-- after GitHub's own webhook can get. The reconcile reads only the pull_request_heads rows that
-- exist, never reads a merged or closed row again, and the read-through runs only for a result's
-- commit. So the intake records the pull request here before it acknowledges such an envelope,
-- and the reconcile's head pass reads each one from GitHub every pass until GitHub answers
-- (delivery.Reconcile.readOwedPullRequests). A row holds no head, no state and no issue, and no
-- reader of a head reads this table, so an owed read is never a head, a basis or a merge.
--
-- No index beside the primary key: the table holds a row only while GitHub has not answered for
-- an event, so it is empty but for an outage, and the reconcile reads all of it, oldest first.
--
-- A new table that names no other: the transaction locks only it, which no other session can
-- reach before the commit, and nothing existing is read, locked or rewritten. The release before
-- this one neither reads nor writes it, so it rolls beside this one (cmd/dispatch/README.md,
-- "Database migrations"). The census answers 0. Rolling back past it is an image rollback alone;
-- the reads still owed then wait for the next event of each pull request, as they did before
-- this release.
create table pull_request_reads_owed (
    repo text not null,
    number integer not null,
    owed_at timestamptz not null default now(),
    primary key (repo, number)
);
