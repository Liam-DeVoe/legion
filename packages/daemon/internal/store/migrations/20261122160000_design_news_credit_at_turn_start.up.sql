-- 20261122160000_design_news_credit_at_turn_start.up.sql — a delivery's copy of the news of the
-- approved design counts as its audience's credit only while the turn of the take that carried it
-- has started (dispatch://INFRA-2811 meeting dispatch://INFRA-175).
--
-- 20261119203047_design_news reads every confirmed delivery holding a copy whose turn's result has
-- not said the turn ran as a standing credit (design_news_told_in's standing). Under the take
-- (20261113175649_task_numbers), confirmed_at is the accepted take, and the accept writes the copy
-- (pending_task_deliveries.carried_news) in its own write, before the take's turn starts. A credit
-- read from the accept would let a gate that closes and opens again before that turn find the
-- holder owed nothing, so the opening it owes would go by the gate's role message while the task's
-- text, already handed to the worker, tells none of it; the push path read the confirmation at the
-- turn's start, which is where a holder is first known to read the text. So this file writes
-- design_news_told_in again with one condition more: a standing credit is a confirmed delivery whose
-- take's turn has started (pending_task_deliveries.turn_started, which the task numbers added),
-- holding a copy no finish has made final (not carried_ran). The copy is still written at the
-- accept and kept with the take. A no-number report that puts the take back sets turn_started false
-- again (supervise's putBack), so the credit stops counting until the take's own turn starts; a
-- take-back ends the copy; a finish by a clean report naming the take's number, or by the end frame
-- below supervise.TaskTakeContract, marks carried_ran, whose write folds the copy into
-- design_news_told (the claim store's settleDesignNews), unchanged. Every other part of the function
-- is 20261119203047's, so record.Postgres.TreeTold and every reader of what an audience took weigh
-- the same answer as before, less the credit of a take whose turn has not started.
--
-- Its version stands above 20261119203047, which created the function, and above every migration
-- main had numbered when this one was written; it adds no table, column or kind, and rewrites no
-- kind list.
--
-- A migration that writes design_news_told_in again must be versioned after this one and keep the
-- standing clause on turn_started. The store applies every migration it has not recorded, in
-- version order, whatever it recorded above it (Store.Migrate), so a migration versioned between
-- 20261119203047 and this one and merged after it would run last on a store that recorded this one,
-- put back the credit at confirmed_at, and fail no test. The comment this file sets on the function
-- says the same to whoever reads the schema.
--
-- The way back is the block below, run with the daemon stopped before the earlier image is pinned,
-- and before the way back of 20261113175649_task_numbers, which drops turn_started (at a rollback
-- to contract 217 or to any release before it, the way back in the header of
-- 20261117034837_issue_plans runs between the two, and at a rollback to a release before
-- cybermonklabs/legion#4903, the way back in the header of
-- 20261121170714_dispatch_issue_applied_seq runs between the two as well, before that one): it
-- writes design_news_told_in back as 20261119203047 wrote it, drops the comment this file set
-- (that migration set none), and deletes this migration's version row, so the roll forward runs
-- this file again. The release before this one, contract 220 (the one whose
-- internal/api/version.go carries the entry for cybermonklabs/legion#4930's assignment plans) as
-- main releases it with #4930 on contract 219, whose newest migration is #4930's
-- 20261123165139_experiment_assignment_plan, carries 20261119203047 and not the task numbers, and
-- then reads the function it wrote; a rollback to contract 219 or before crosses 220 and first
-- takes that release's one step, before this block, which takes `plan` out of legion.yaml and runs
-- no statement (docs/kubernetes.md, "Rolling back the release whose experiment sets how each issue
-- is done"), a rollback to contract 217 or before also crosses 219 and takes that release's own
-- steps (docs/kubernetes.md, "Rolling back the release that runs a task's steps in any order", and
-- the way back of 20261117034837_issue_plans), and a rollback to contract 213 or before also
-- crosses 217 and takes that release's own steps (docs/kubernetes.md, "Rolling back below the
-- release that guards the controller's pod's memory"). Nothing else is
-- reversed: no row changes here, and a copy on a confirmed row that the earlier function counts
-- at once, its take's turn not yet started, is the credit that release would have written at the
-- confirmation of the same turn. docs/kubernetes.md gives this block under "Rolling back the
-- release that credits a holder's news at its take's turn".
--
-- begin;
-- create or replace function design_news_told_in(tree text)
--   returns table (issue text, audience text, versions jsonb, opening_owed boolean, opening_seq integer, told_at timestamptz)
--   language sql stable as $$
--   with member as (select owner.key from issues owner where owner.tree = $1),
--   standing as (
--     select delivery.carried_news from pending_task_deliveries delivery
--     where delivery.confirmed_at is not null and not delivery.carried_ran and delivery.carried_news is not null),
--   credit as (
--     select credited.issue, credited.audience, credited.link
--     from standing cross join design_news_credits(standing.carried_news) credited
--     where credited.issue in (select member.key from member)),
--   cleared as (
--     select opened.issue, opened.audience, opened.seq
--     from standing cross join design_news_openings(standing.carried_news) opened
--     where opened.issue in (select member.key from member)),
--   pair as (
--     select told.issue, told.audience from design_news_told told where told.issue in (select member.key from member)
--     union
--     select credit.issue, credit.audience from credit)
--   select pair.issue, pair.audience,
--     design_news_known(coalesce(told.versions, '{}'::jsonb), coalesce(told.unreached, '[]'::jsonb)
--       || coalesce((select jsonb_agg(credit.link) from credit
--         where credit.issue = pair.issue and credit.audience = pair.audience), '[]'::jsonb)),
--     coalesce(told.opening_owed, false) and not exists (select 1 from cleared
--       where cleared.issue = pair.issue and cleared.audience = pair.audience and cleared.seq = told.opening_seq),
--     coalesce(told.opening_seq, 0), told.told_at
--   from pair left join design_news_told told on told.issue = pair.issue and told.audience = pair.audience
--   order by pair.issue, pair.audience
-- $$;
-- comment on function design_news_told_in(text) is null;
-- delete from schema_version where version = 20261122160000;
-- commit;
create or replace function design_news_told_in(tree text)
  returns table (issue text, audience text, versions jsonb, opening_owed boolean, opening_seq integer, told_at timestamptz)
  language sql stable as $$
  with member as (select owner.key from issues owner where owner.tree = $1),
  standing as (
    select delivery.carried_news from pending_task_deliveries delivery
    where delivery.confirmed_at is not null and delivery.turn_started and not delivery.carried_ran
      and delivery.carried_news is not null),
  credit as (
    select credited.issue, credited.audience, credited.link
    from standing cross join design_news_credits(standing.carried_news) credited
    where credited.issue in (select member.key from member)),
  cleared as (
    select opened.issue, opened.audience, opened.seq
    from standing cross join design_news_openings(standing.carried_news) opened
    where opened.issue in (select member.key from member)),
  pair as (
    select told.issue, told.audience from design_news_told told where told.issue in (select member.key from member)
    union
    select credit.issue, credit.audience from credit)
  select pair.issue, pair.audience,
    design_news_known(coalesce(told.versions, '{}'::jsonb), coalesce(told.unreached, '[]'::jsonb)
      || coalesce((select jsonb_agg(credit.link) from credit
        where credit.issue = pair.issue and credit.audience = pair.audience), '[]'::jsonb)),
    coalesce(told.opening_owed, false) and not exists (select 1 from cleared
      where cleared.issue = pair.issue and cleared.audience = pair.audience and cleared.seq = told.opening_seq),
    coalesce(told.opening_seq, 0), told.told_at
  from pair left join design_news_told told on told.issue = pair.issue and told.audience = pair.audience
  order by pair.issue, pair.audience
$$;

comment on function design_news_told_in(text) is
  'A standing credit is a confirmed delivery whose take''s turn has started (turn_started), holding a copy no finish has made final (not carried_ran); 20261122160000_design_news_credit_at_turn_start wrote this. A migration that writes this function again must be versioned after 20261122160000 and keep the standing clause on turn_started: one versioned below it runs last on a store that recorded 20261122160000 and would put back a credit at the accept (confirmed_at).';
