-- 20261117110810_outbox_kinds_with_the_basis_seeds.up.sql — the two outbox kinds that read
-- Dispatch for a root's design-gate basis and for a re-parented root (dispatch://INFRA-3320).
--
-- pending_basis_seed reads the ancestor's open approval request a root's pending step waits on
-- (record.PendingBasisSeed) and applies what it finds: the person's approval opens the step, a
-- changes request, a newer version or the request's close with no approval ends the wait, and a
-- request still waiting changes nothing. reparent_seed reads the new Dispatch parent of a root that
-- ran as a tree of its own (record.ReparentSeed): the engine does no I/O, so the message to that
-- root and the notice to its parent's tree's architect wait on this read of whether the parent
-- marks requirement blocks and holds a person's approval of its latest version.
--
-- The check is one list, so the migration that sets it names every kind, as 20261106041346,
-- 20261108000545, 20261110235320, 20261113082214, 20261114031356 and 20261116165926 did:
-- whichever of two open branches merges last admits both their kinds. The list is
-- 20261116165926's twenty-five kinds, merge_queue_read, merge_queue_removal and
-- role_holders_publish among them, with pending_basis_seed and reparent_seed added. It drops the
-- constraint if it exists before adding it, so it is safe on a database at either branch. Its
-- version stands above 20261116165926, and Migrate applies every unrecorded migration in version
-- order, so this list is the last word on a fresh database and on one main migrated. No release
-- carries this file, so no deployment's database records it.
--
-- A database a build of this branch migrated before 2026-11-17 recorded this list under the version
-- 20261114064235, which stood above main's newest kind list then, and recorded neither
-- 20261116165926 nor this version, and one migrated before 20261114031356 came in recorded that
-- version neither. Its next upgrade applies each of the two it lacks before this one, and neither
-- list names a kind of this one, so a queued row of either stops the upgrade at the first of them
-- (SQLSTATE 23514). On such a database, with every daemon of it stopped, an operator first deletes
-- the queued rows of both kinds, with the statement of docs/kubernetes.md's rollback of this
-- release (`delete from public.outbox where kind in ('pending_basis_seed', 'reparent_seed');`),
-- then runs the upgrade, which ends with this file and admits both kinds again; the row of
-- 20261114064235 stays recorded and names no file, which Migrate passes over. The delete costs
-- little: the next boot queues a pending-basis seed again for each step that still waits, and a
-- re-parented root and its new parent's architect are not told of that one move.
--
-- A daemon of an earlier release refuses neither row at the database, since this list admits
-- them, and runs neither: none of its outbox lanes lists either kind (record.LaneKinds), and its
-- sweep wakes no lane for a kind in no lane (record.Store.UnfinishedLanes), so a row of either
-- stays queued until this release runs it.
--
-- There is no down file: migrations here are forward-only.
alter table outbox drop constraint if exists outbox_kind_check;
alter table outbox add constraint outbox_kind_check check (kind in (
  'dispatch_status', 'dispatch_message', 'notice', 'controller_notice', 'supervise', 'gate_seed',
  'linger_close', 'workspace_remove', 'issue_branch', 'issue_suspend', 'check_wake',
  'check_look', 'check_bound', 'role_message', 'phase_ended', 'issue_bundle', 'issue_release',
  'gated_steps_publish', 'claim_release', 'hook_prompt', 'design_gates_publish', 'update_ask',
  'merge_queue_read', 'merge_queue_removal', 'role_holders_publish', 'pending_basis_seed',
  'reparent_seed'));
