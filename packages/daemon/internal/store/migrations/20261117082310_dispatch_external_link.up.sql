-- 20261117082310_dispatch_external_link.up.sql — the outbox kind that writes a pull request's URL
-- into its issue's Dispatch external links (dispatch_external_link).
--
-- The workflow queues one such row as it first records a pull request for an issue
-- (workflow's pullRequestOpened), and the row's delivery reads the issue from Dispatch, adds the
-- URL to the links it already holds and writes the list back. Dispatch attributes a pull request to
-- the issue whose external link is the pull request's own URL before any other source, so a pull
-- request on a keyless issue branch (20261117082309_issue_branches), whose title and body name no
-- key either, is still the issue's in Dispatch, and `dispatch results` compares the issue's checks
-- with its head. Before keyless branches, Dispatch found the issue by the key in the branch name.
-- The row rides the board lane beside the daemon's other Dispatch writes and holds no pod.
--
-- The check is one list, so the migration that sets it names every kind, as 20261114031356 did:
-- that file's twenty-four kinds, which are the kinds the daemon writes, and this one. Its version
-- stands above 20261114031356 on purpose: Migrate applies every unrecorded migration in version
-- order, so the list with the higher version is the last word on any database.
--
-- The way back, with the daemon stopped and before the earlier image is pinned, deletes the rows of
-- this kind (each a link Dispatch does not hold yet, which a person can add to the issue by hand)
-- and puts 20261114031356's list back:
--   delete from outbox where kind = 'dispatch_external_link';
--   delete from schema_version where version = 20261117082310;
--   then the two statements of 20261114031356_merge_queue_removal.up.sql.
-- Without the second delete, a later roll forward skips this file and refuses every row of this
-- kind under the list the way back put in place.
alter table outbox drop constraint if exists outbox_kind_check;
alter table outbox add constraint outbox_kind_check check (kind in (
  'dispatch_status', 'dispatch_message', 'notice', 'controller_notice', 'supervise', 'gate_seed',
  'linger_close', 'workspace_remove', 'issue_branch', 'issue_suspend', 'check_wake',
  'check_look', 'check_bound', 'role_message', 'phase_ended', 'issue_bundle', 'issue_release',
  'gated_steps_publish', 'claim_release', 'hook_prompt', 'design_gates_publish', 'update_ask',
  'merge_queue_read', 'merge_queue_removal', 'dispatch_external_link'));
