-- 20261123180305_sub_owners.up.sql — a task that stands at its parts or at its report, the parts
-- whose join GitHub refused, and the outbox kind that joins the parts of a task (parts_join).
--
-- plan_tasks.running_kind takes two kinds of place more (record.PlaceParts, record.PlaceReported),
-- each the place of a task whose sub-owner does its work: parts, where the sub-owner writes, gives
-- and watches the tasks that are its parts, and reported, where the sub-owner reported the task
-- (complete_task) and the join of its parts runs. A task stands at either at position 0, as at its
-- retro and its merge: plan_tasks_place_check, unchanged, refuses a position from 1 at any kind but
-- step. The check is written whole again, with the kinds of 20261117034837, which last wrote it.
-- plan_tasks.parts_conflicts is the id of each part whose join into the task's branch GitHub
-- refused as a conflict, at the last join of the task's parts (record.PlanTask.PartsConflicts),
-- beside join_conflicts, which holds the join of the task's needs alone. Every task the upgrade
-- finds takes the default, no part, since no release before this one gave a task parts.
--
-- The outbox check is one list, so this migration names every kind, as 20261123072431 did: its
-- thirty-two kinds and parts_join. Its version stands above 20261123072431 on purpose: Migrate
-- applies every unrecorded migration in version order, so the list with the higher version is the
-- last word on any database.
--
-- The release before this one boots without the statements below: its plan_tasks reads and writes
-- name each column they touch, and none names parts_conflicts. It takes no plan whose task names
-- owner (plan.Supported), so it writes no task at parts or at reported, and no part.
--
-- There is no down file. This directory keeps none, so the way back is the transaction that ends
-- this header, its last paragraph (migrations.WayBack), run with the daemon stopped, before the
-- earlier image is pinned, through psql '<postgres_dsn>' -X -v ON_ERROR_STOP=1, before the way back
-- of any earlier migration. The release before decodes no parts_join row, and no notice of the
-- kinds parts-done, report-refused and merge-returned (record.DecodeOutboxPayload refuses an
-- unknown kind), so it would fail such a row at every attempt: the block deletes each one first,
-- and the parts_join rows before the check written again without the kind, which refuses a row
-- that holds it. Each task at parts or at reported then stands at no act, which the check written
-- again takes: a running or a held one is stopped for the reason 'rolled back below sub-owners',
-- with no hold and no wait of its step, as a stop leaves a task (record's StopTask); a waiting,
-- stopped, done or left one keeps its state. Then the block writes the check of 20261117034837
-- and the outbox check of 20261123072431 again, drops the column and deletes its version row,
-- which is owed to the roll-forward, so that it runs this file again.
--
-- What the way back costs is the place of each task with a sub-owner and the parts whose join
-- conflicted. A task it stopped stays stopped under the release before, which takes no later
-- version that names owner: its owner gives a version without owner, or stops the work. That
-- release starts each waiting part whose needs are done as it starts any task, since its start of
-- a task reads its needs alone (the workflow's startReady), not the task the part is of.
--
-- begin;
-- delete from outbox where kind = 'parts_join';
-- delete from outbox where kind in ('notice', 'controller_notice') and payload->>'kind' in ('parts-done', 'report-refused', 'merge-returned');
-- update plan_tasks set running_kind = '', hold_reason = '', step_wait = '', step_wait_since = null,
--   stopped_reason = case when state in ('running', 'held') then 'rolled back below sub-owners' else stopped_reason end,
--   state = case when state in ('running', 'held') then 'stopped' else state end
--   where running_kind in ('parts', 'reported');
-- alter table plan_tasks drop constraint if exists plan_tasks_running_kind_check;
-- alter table plan_tasks add constraint plan_tasks_running_kind_check check (running_kind in ('', 'step', 'retro', 'merge'));
-- alter table outbox drop constraint if exists outbox_kind_check;
-- alter table outbox add constraint outbox_kind_check check (kind in (
--   'dispatch_status', 'dispatch_message', 'notice', 'controller_notice', 'supervise', 'gate_seed',
--   'linger_close', 'workspace_remove', 'issue_branch', 'issue_suspend', 'check_wake',
--   'check_look', 'check_bound', 'role_message', 'phase_ended', 'issue_bundle', 'issue_release',
--   'gated_steps_publish', 'claim_release', 'hook_prompt', 'design_gates_publish', 'update_ask',
--   'merge_queue_read', 'merge_queue_removal', 'role_holders_publish', 'step_record', 'plan_record',
--   'conflict_confirm', 'merge_forward', 'task_branch', 'task_pull_requests_close', 'task_copy_remove'));
-- alter table plan_tasks drop column if exists parts_conflicts;
-- delete from schema_version where version = 20261123180305;
-- commit;
alter table plan_tasks drop constraint plan_tasks_running_kind_check,
  add constraint plan_tasks_running_kind_check check (running_kind in ('', 'step', 'retro', 'merge', 'parts', 'reported'));
alter table plan_tasks add column parts_conflicts text[] not null default '{}';

alter table outbox drop constraint if exists outbox_kind_check;
alter table outbox add constraint outbox_kind_check check (kind in (
  'dispatch_status', 'dispatch_message', 'notice', 'controller_notice', 'supervise', 'gate_seed',
  'linger_close', 'workspace_remove', 'issue_branch', 'issue_suspend', 'check_wake',
  'check_look', 'check_bound', 'role_message', 'phase_ended', 'issue_bundle', 'issue_release',
  'gated_steps_publish', 'claim_release', 'hook_prompt', 'design_gates_publish', 'update_ask',
  'merge_queue_read', 'merge_queue_removal', 'role_holders_publish', 'step_record', 'plan_record',
  'conflict_confirm', 'merge_forward', 'task_branch', 'task_pull_requests_close', 'task_copy_remove',
  'parts_join'));
