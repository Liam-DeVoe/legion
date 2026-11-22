-- 20261119211852_merge_holds_changed.up.sql — the outbox kind that tells the merger of an issue in
-- merging that the project's merge holds document changed (merge_holds_changed).
--
-- Each project's merge holds are one Dispatch document of the project, merge-holds-md, which only
-- the controller writes. A merger that a hold stops ends its turn and subscribes to nothing, so
-- the daemon tells it of each event of the document: the architect of an issue in accepting by a
-- notice, and the merger of an issue in merging by a row of this kind, since a notice reaches only
-- an architect. The row wakes a released merger and waits until the merger's session holds its
-- role. It rides the role-message lane (internal/record's lanes) and holds the issue's pod (the
-- store's idleWaitingKinds).
--
-- The check is one list, so this migration names every kind, as 20261119203047 did: its thirty
-- kinds, in its order, and then this one, thirty-one in all. Its version stands above
-- 20261119203047, the design news's list (dispatch://INFRA-175), 20261119003157, the list with the
-- conflict's second read and the merge forward, 20261117023932, the plan record's, and
-- 20261117000528, the step record's, so on any database this list is the last word: Migrate
-- applies every unrecorded migration in version order, and at a lower version any of those lists,
-- none of which names merge_holds_changed, would be applied after this one and refuse the kind.
--
-- This list must name step_record, plan_record, conflict_confirm, merge_forward, design_changes
-- and phase_ended, because a database main's release migrated can hold a row of each of those
-- kinds when this migration is applied over it, and a list without the kind would fail its ADD
-- CONSTRAINT on that row (SQLSTATE 23514), and Migrate with it (store's
-- TestADatabaseMainMigratedWithRowsOfItsNewestKindsUpgrades). Main's daemon queues the first five.
-- It queues no phase_ended row, and 20261119003157 deletes the ones still queued, but main keeps
-- the kind in its list for a rollback: the release before it queued a phase_ended row for the role
-- whose phase a conflict ended, when that conflict sent the issue back to implementing, and after
-- a rollback that release runs on the last list written, since it has no migration of its own left
-- to apply. A list without phase_ended would refuse the first such row it queues, and the
-- transaction of the move that queued it with it. That holds of this list as well, now the last
-- word, and a database that release ran on can still hold such a row when this migration is
-- applied over it.
-- On a fresh database the design news's list is applied first and this one after it, so a list
-- here without design_changes would refuse every reconcile of a tree's news of its approved design.
--
-- This migration was numbered 20261117113130 on earlier builds of this branch, which no deployment
-- ran. That version stands below 20261119003157, whose list does not name merge_holds_changed, so
-- on a fresh database that list would have been the last word and refused the kind. Its list gained
-- design_changes at the forward merge that brought 20261119203047 in, before any release carried
-- this file; a database a build of this branch migrated before that merge records this version
-- without the kind, and it is a test's own, to make again.
alter table outbox drop constraint if exists outbox_kind_check;
alter table outbox add constraint outbox_kind_check check (kind in (
  'dispatch_status', 'dispatch_message', 'notice', 'controller_notice', 'supervise', 'gate_seed',
  'linger_close', 'workspace_remove', 'issue_branch', 'issue_suspend', 'check_wake',
  'check_look', 'check_bound', 'role_message', 'phase_ended', 'issue_bundle', 'issue_release',
  'gated_steps_publish', 'claim_release', 'hook_prompt', 'design_gates_publish', 'update_ask',
  'merge_queue_read', 'merge_queue_removal', 'role_holders_publish', 'step_record', 'plan_record',
  'conflict_confirm', 'merge_forward', 'design_changes',
  'merge_holds_changed'));
