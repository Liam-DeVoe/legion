-- 20261116082000_reach_notice_kinds.up.sql — the outbox kind of the notice this change adds:
-- reach_notice, the architect's notice of a claim of its tree out of reach (internal/record's
-- OutboxKindReachNotice).
--
-- It is a kind of its own so that the release before it never runs one: that release decodes a
-- row strictly and knows neither that notice kind nor its claim field, so it would fail on such a
-- row under notice at every attempt, and it runs a row only through a lane that lists the row's
-- kind. A rollback onto that release therefore leaves each such row where it stands, and the next
-- daemon of this one delivers it. It rides the board lane, as the other notice kinds do, and holds
-- the issue's pod as notice does (the store's idleWaitingKinds).
--
-- That release starts on a database this file has migrated: it applies every embedded migration
-- its database has not recorded and asks nothing of a version it does not embed, and this file
-- changes only which kinds an outbox row may have, a list that admits every kind that release
-- writes. So a rollback needs no down migration, and none is written, since migrations here are
-- forward-only. scripts/e2e/rollback-notices.sh runs that release on such a database.
--
-- The check is one list, so it names every kind, as 20261114031356 did: its twenty-four, this
-- one, role_holders_publish, which 20261116165926_role_holders_publish adds, step_record, which
-- 20261117000528_outbox_kinds_with_the_step_record adds, plan_record, which
-- 20261117023932_outbox_kinds_with_the_plan_record adds, and conflict_confirm and merge_forward,
-- which 20261119003157_conflict_reads_and_merge_forward adds. Those versions stand above this one,
-- so a fresh database applies 20261119003157's list last, and that list names this one. A
-- database that recorded those four before this file applies this list last instead, over the
-- rows of their kinds its outbox holds, which a list without any of them would refuse, so the add
-- constraint below would fail and the daemon would not boot.
--
-- A database a build of this change migrated before this kind existed can hold such a notice
-- under notice, where this release refuses to decode it, because a row's kind and its notice's
-- kind must agree (internal/record's DecodeOutboxPayload). The update moves each such row to its
-- own kind. No release wrote one, so a deployment's database moves nothing.
alter table outbox drop constraint if exists outbox_kind_check;
alter table outbox add constraint outbox_kind_check check (kind in (
  'dispatch_status', 'dispatch_message', 'notice', 'controller_notice', 'supervise', 'gate_seed',
  'linger_close', 'workspace_remove', 'issue_branch', 'issue_suspend', 'check_wake',
  'check_look', 'check_bound', 'role_message', 'phase_ended', 'issue_bundle', 'issue_release',
  'gated_steps_publish', 'claim_release', 'hook_prompt', 'design_gates_publish', 'update_ask',
  'merge_queue_read', 'merge_queue_removal', 'reach_notice',
  'role_holders_publish', 'step_record', 'plan_record', 'conflict_confirm', 'merge_forward'));

update outbox set kind = 'reach_notice'
  where kind = 'notice' and payload->>'kind' = 'claim-out-of-reach';
