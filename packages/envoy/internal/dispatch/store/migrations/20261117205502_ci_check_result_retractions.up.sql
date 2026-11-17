-- 20261117205502_ci_check_result_retractions.up.sql
--
-- One row per retracted result a CI job recorded (ci_check_results): the record that says such a
-- result is wrong (POST /api/v1/results/{id}/retract), as check_result_retractions is for a
-- result a role recorded, in the same shape. Nothing in ci_check_results is updated or deleted,
-- so the result and its retraction stay readable together, each with its own reason and actor.
--   result_id    the retracted result, and this table's key. One retraction per result: a
--                second retraction of one result is refused by this primary key and by the
--                route before it.
--   reason       why the result is wrong, which the person or session who retracted it wrote.
--   session      the session that retracted it ('' for a person).
--   actor        the actor that retracted it, in the shape every other table stores an actor in.
--   created_at   when the retraction was written.
--   event_seq    the seq of the result.retracted event on the check's issue.
--
-- A table of its own rather than rows of check_result_retractions, whose result_id is a foreign
-- key into check_results, the table of a role's results: a row here goes with its result, by
-- the cascade its own foreign key takes, as a role's retraction goes with a role's result.
--
-- A new table: nothing existing is read, locked or rewritten, and the release before this one
-- neither reads nor writes it, so it rolls beside this one (cmd/dispatch/README.md, "Database
-- migrations"). The census answers 0, and rolling back past it is an image rollback alone: the
-- release before reads neither this table nor ci_check_results.
create table ci_check_result_retractions (
    result_id uuid primary key references ci_check_results (id) on delete cascade,
    reason text not null check (reason <> ''),
    session text not null default '',
    actor jsonb not null,
    created_at timestamptz not null default now(),
    event_seq bigint
);
