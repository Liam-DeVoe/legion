-- 20261121043859_request_key_steps.up.sql
--
-- INFRA-4731: each committed step's entry of a keyed status change (POST
-- /api/v1/issues/{key}/status-change), in the order the steps committed, beside its request key's
-- recovery point. The point's head stays on the key's row (request_keys.recovery): the ids of the
-- events the steps appended on the issue the call names, the ids of the asks its close withdrew,
-- and the mark that every step ran with the answer's issue. Each step that writes adds its row
-- here in its own transaction, in the statement that adds to the head, right after the step's
-- write phase (api's writeIssue, through docs.Ledger.AfterWrites), and every reader of the point
-- reads the rows back under its changed member (api's recoveryPointRead), so a repeat answers each
-- entry's issue as its step's write answered it rather than as it stands then. A row goes with its
-- key, by the sweep of keys past their window or the release of a key that committed nothing,
-- through the cascade.
--
--   id                the order the steps committed in, which the readers aggregate by.
--   login             whose key it is, as request_keys names it.
--   idempotency_key   the key, as request_keys names it.
--   entry             the step's entry as the route answers it: the issue's key, its status and
--                     parent after the step, its last_seq before the step, and the issue as the
--                     step's write answered it (api's statusChangeEntry).
--
-- A table of its own rather than a member of the head: each step adds one row, where rewriting a
-- growing jsonb value at every step costs the square of the steps. request_key_steps_key serves
-- the readers' aggregate of one key's rows, in order, and the cascade's delete of them.
--
-- A new table nothing before this release names: the release before this one neither reads nor
-- writes it, so the two roll side by side (cmd/dispatch/README.md, "Database migrations"), and
-- the census answers 0 because the table holds no row at this schema. Rolling back past it is an
-- image rollback alone. The identifier is the UTC moment this file was created, as every
-- migration past the numbered series is named.
create table request_key_steps (
    id bigint generated always as identity primary key,
    login text not null,
    idempotency_key text not null,
    entry jsonb not null,
    foreign key (login, idempotency_key) references request_keys (login, idempotency_key) on delete cascade
);
create index request_key_steps_key on request_key_steps (login, idempotency_key, id);
