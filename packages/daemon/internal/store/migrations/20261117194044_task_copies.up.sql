-- 20261117194044_task_copies.up.sql — the branch of each task of a plan, the needs whose join
-- GitHub refused, the names a generation gave to its tasks, and the outbox kind that makes a task's
-- branch on GitHub (task_branch).
--
-- plan_tasks.branch is the GitHub branch of the task (record.PlanTask.Branch), written once, when
-- the task first starts (record's AssignTaskBranch): legion/<KEY> for the first task of the
-- generation to start, legion/<KEY>.<slug> for each other. '' reads as legion/<KEY>
-- (record.PlanTask.BranchOf), which is the branch of every task the upgrade finds, since no release
-- before this one gave a task a branch of its own. plan_tasks.join_conflicts is the id of each need
-- of the task whose merge into the task's branch GitHub refused as a conflict, which the task's
-- first step joins by hand (record's SetJoinConflicts).
--
-- plan_branches is each branch name a generation of an issue gave to one of its tasks. GitHub keeps
-- a branch and its commits after the task that held it was stopped and a later version removed it,
-- so a name given is never given to another task of the generation, and the deleted task's row
-- cannot say it was given. Only AssignTaskBranch reads the table; it names no task, so a task's
-- branch has one place, plan_tasks.branch.
--
-- The outbox check is one list, so this migration names every kind, as 20261117023932 did: its
-- twenty-six kinds and task_branch. Its version stands above 20261117023932 on purpose: Migrate
-- applies every unrecorded migration in version order, so the list with the higher version is the
-- last word on any database.
--
-- There is no down file. This directory keeps none, so the way back is the transaction below, run
-- with the daemon stopped, before the earlier image is pinned. It deletes each task_branch row
-- first, since the check written again without the kind refuses a row that holds it, and the
-- earlier release decodes no such row:
--
--   psql '<postgres_dsn>' -X -v ON_ERROR_STOP=1 <<'SQL'
--   begin;
--   delete from outbox where kind = 'task_branch';
--   alter table outbox drop constraint if exists outbox_kind_check;
--   alter table outbox add constraint outbox_kind_check check (kind in (
--     'dispatch_status', 'dispatch_message', 'notice', 'controller_notice', 'supervise', 'gate_seed',
--     'linger_close', 'workspace_remove', 'issue_branch', 'issue_suspend', 'check_wake',
--     'check_look', 'check_bound', 'role_message', 'phase_ended', 'issue_bundle', 'issue_release',
--     'gated_steps_publish', 'claim_release', 'hook_prompt', 'design_gates_publish', 'update_ask',
--     'merge_queue_read', 'merge_queue_removal', 'step_record', 'plan_record'));
--   alter table plan_tasks drop column if exists branch, drop column if exists join_conflicts;
--   drop table if exists plan_branches;
--   delete from schema_version where version = 20261117194044;
--   commit;
--   SQL
--
-- The release before this one boots without the statements: its plan_tasks reads and writes name
-- each column they touch, and it names neither new column nor the new table. They are owed to the
-- roll forward. With the columns and the table dropped and the row deleted, store.apply runs this
-- file again, and each task the roll forward finds reads the issue's own branch. What the way back
-- costs is the branch of each task that started on a branch of its own: the roll forward reads the
-- issue's branch for it, so its next step works on the issue's branch, not on the task's.
--
-- The way back drops plan_branches with the two columns (`drop table if exists plan_branches`
-- above): the earlier release names it in no statement, and the roll forward creates it again
-- empty. What that costs is the names the generations gave: after the roll forward, a task that
-- starts takes a name again by the rows alone, so it can take the name of a task that a later
-- version removed before the rollback, and that branch's commits.
alter table plan_tasks add column branch text not null default '',
  add column join_conflicts text[] not null default '{}';

create table plan_branches (
  issue text not null,
  generation bigint not null,
  branch text not null,
  primary key (issue, generation, branch)
);

alter table outbox drop constraint if exists outbox_kind_check;
alter table outbox add constraint outbox_kind_check check (kind in (
  'dispatch_status', 'dispatch_message', 'notice', 'controller_notice', 'supervise', 'gate_seed',
  'linger_close', 'workspace_remove', 'issue_branch', 'issue_suspend', 'check_wake',
  'check_look', 'check_bound', 'role_message', 'phase_ended', 'issue_bundle', 'issue_release',
  'gated_steps_publish', 'claim_release', 'hook_prompt', 'design_gates_publish', 'update_ask',
  'merge_queue_read', 'merge_queue_removal', 'step_record', 'plan_record', 'task_branch'));
