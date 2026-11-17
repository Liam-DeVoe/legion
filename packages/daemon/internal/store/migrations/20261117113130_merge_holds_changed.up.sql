-- 20261117113130_merge_holds_changed.up.sql — the outbox kind that tells the merger of an issue in
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
-- The check is one list, so this migration names every kind, as 20261116165926 did: its
-- twenty-five kinds, which are the kinds the daemon writes, and this one. Its version stands above
-- 20261116165926, the role-holders publication's list, so on any database this list is the last
-- word: at a lower version, that list, which does not name merge_holds_changed, would be applied
-- after this one and refuse the kind.
alter table outbox drop constraint if exists outbox_kind_check;
alter table outbox add constraint outbox_kind_check check (kind in (
  'dispatch_status', 'dispatch_message', 'notice', 'controller_notice', 'supervise', 'gate_seed',
  'linger_close', 'workspace_remove', 'issue_branch', 'issue_suspend', 'check_wake',
  'check_look', 'check_bound', 'role_message', 'phase_ended', 'issue_bundle', 'issue_release',
  'gated_steps_publish', 'claim_release', 'hook_prompt', 'design_gates_publish', 'update_ask',
  'merge_queue_read', 'merge_queue_removal', 'role_holders_publish', 'merge_holds_changed'));
