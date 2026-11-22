-- 20261120095128_issue_facts.up.sql — each issue's own facts, where the release before read them
-- off the tree it stored (dispatch://INFRA-4730, R17 and R22 of dispatch://INFRA-3320).
--
-- The issue's columns. retained_until is when a linger the release before converted ends for the
-- issue (record.Issue.RetainedUntil), carried_generation and carried_gate what a hand-over of that
-- release carried into the root it made, copied off handed_over_roots by the first boot's
-- conversion (record.Issue.CarriedGeneration, CarriedGate), parked_by_architect the mark that the
-- architect above parked the issue (record.Issue.ParkedByArchitect), pending_close a close
-- Dispatch refused while a child was not done, written again once every child is
-- (record.Issue.PendingClose), and seen_seq the newest Dispatch sequence of the issue whose event
-- the consumer has handled or dropped (record.Issue.SeenSeq). pending_close is one nullable jsonb
-- of the four parts of one mark, its run, its phase entry, the close path that decided it and its
-- reason, because the mark is set and cleared whole: four columns could hold half of one, and the
-- kinds of close are the record's to name.
--
-- The claim's columns. launch_tree is the issue's gate root at the claim's last launch, which the
-- spawn spec and the registration's answer name, beside claims.tree, which stays the tree the
-- claim's issue stored, for the release before (supervise.Claim.LaunchTree, decision 13 of
-- dispatch://INFRA-4730). issue_epoch is the epoch of the claim's issue's own lifecycle it bound
-- to, and holds_gate whether the claim's issue held its own gate at its last launch
-- (supervise.Claim.IssueEpoch, HoldsGate).
--
-- The issue lifecycle. issue_lifecycles is the admission and cleanup barrier of one issue, column
-- for column what tree_lifecycles is for a tree (0033_tree_lifecycle), keyed by the normalized
-- project token claims use and by the issue: a claim binds an open epoch, a cleanup reserves it,
-- and only a fresh open after the cleanup confirmed opens the next one (internal/treelifecycle,
-- given the unit it guards). tree_lifecycles stays, and stays written: the release before binds
-- its claims to it.
--
-- The claims that stand at the upgrade keep running. Each issue a claim not retired names, whose
-- claim's tree has a lifecycle whose cleanup has not started, opens its own at epoch one under that
-- tree's authority, the workflow's where its claims' trees differ in it, and every claim of an issue
-- that now has a lifecycle binds epoch one; without that, the first relaunch of a live claim would
-- be refused as unbound. An issue of a tree whose cleanup is reserved opens none here: the first
-- boot's conversion moves that reservation onto each of its issues. The project controller's
-- claim, on no issue, opens and binds nothing.
--
-- The conversion's record. tree_conversions holds, for each project the first boot converted, the
-- last numbered step of the conversion that committed, and when the last step ran (converted_at,
-- written by that step alone), so a boot after a crash starts at the next step.
--
-- The outbox. attempt_key and attempt_body are the request key and the body of the attempt a
-- status row with an option sent on Dispatch's status-change route, which no payload carries
-- (decision 6 of dispatch://INFRA-4730). retention_end is the kind that does for one issue, at its
-- retained_until, what the linger's expiry did for a tree (record.RetentionEnd). The check is one
-- list, so this migration names every kind, as 20261119185013 did: its thirty-one kinds with
-- retention_end added. Its version stands above 20261119185013, so this list is the last word on a
-- fresh database and on one an earlier build of this branch migrated.
--
-- A daemon of the release before names none of these columns and tables in any statement — it
-- neither selects the new columns nor inserts them, and an insert of its takes each one's default —
-- and refuses no retention_end row at the database, since this list admits the kind, and runs
-- none: none of its outbox lanes lists it (record.LaneKinds). There is no down file: migrations
-- here are forward-only.
--
-- A rollback of one project to the release before first runs, with its daemon stopped, the way
-- back below (R22 of dispatch://INFRA-3320, decision 15 of dispatch://INFRA-4730), and only then
-- the way back of each earlier migration a rollback across them names. The release before decodes
-- an outbox payload strictly (record.DecodeOutboxPayload, DisallowUnknownFields) and refuses a
-- kind and a notice kind it does not know, at every attempt. So the way back deletes the rows
-- that release cannot run: each retention_end row (the release before's own linger serves the
-- deadline it carries, from the linger_until the conversion left), each reparented notice and each
-- status-scope controller notice (told again by no one), and each status write that carries an
-- option of Dispatch's status-change route (written without its option it would break R24; a
-- person makes that write again). It takes off each payload key this release adds to a kind that
-- release reads, so those effects run after the rollback: the run and the phase entry each effect
-- names, the pod a stop, a cleanup, a save or a release names, the architect a notice names, the
-- row a status write waits for and the controller that asked for it, and the parts of R18's seed
-- beside the parent, the tree and the words that release requires of it. It gives each issue the
-- first boot's conversion made a root of its own from a hand-over that had landed (a record with
-- carried_generation set whose stored tree is another issue's) what the release before's own
-- re-admission and promotion would have written, since that release re-admits only a record whose
-- key is its tree: the issue stores its own key as its tree, the tree it left where it still stands
-- at admitted and no tree where it has started (handed_over_from), its claims name that tree and
-- are bound to an open lifecycle of it, a handed_over_roots row says what it carried, and its
-- unfinished rows name that tree, at the issue's generation where they name a tree's generation.
-- It deletes the project's record of the conversion, which clears the state's daemon.convertedAt,
-- so the next boot on this release converts what the release before wrote in between. Last it
-- prints each issue that has a claim not retired with the tree it stores: a Sandbox whose
-- legion.dev/tree label differs from that tree is relabelled with it before the release before
-- boots, since that release deletes a Sandbox by its tree label and sweeps one whose label is not
-- its issue's stored tree. Daemons of several projects can share the database, and each runs only
-- its own project's rows (split_part(issue, '-', 1)), so each statement names the project,
-- <PROJECT>, which the operator replaces with its key; the claims and the lifecycles name it by
-- its claim token, the key in lower case (claim.ProjectToken). The way back is the last paragraph
-- of this header (migrations.WayBack), and internal/store's
-- TestTheConversionsWayBackLeavesWhatTheReleaseBeforeReads runs it.
--
-- -- The rows of one project the release before cannot run.
-- delete from outbox where split_part(issue, '-', 1) = '<PROJECT>' and (kind = 'retention_end'
--   or (kind = 'notice' and payload->>'kind' = 'reparented')
--   or (kind = 'controller_notice' and payload->>'kind' = 'status-scope')
--   or (kind = 'dispatch_status' and (payload ? 'includeChildren' or payload ? 'unlinkChildren' or payload ? 'includeParent')));
-- -- The keys this release adds to the payloads of the kinds the release before reads.
-- update outbox set payload = payload - 'generation' - 'entry' - 'after' - 'controller'
--   where kind = 'dispatch_status' and split_part(issue, '-', 1) = '<PROJECT>';
-- update outbox set payload = payload - 'generation' - 'entry' - 'to'
--   where kind in ('notice', 'controller_notice') and split_part(issue, '-', 1) = '<PROJECT>';
-- update outbox set payload = payload - 'generation' - 'entry'
--   where kind in ('dispatch_message', 'role_message', 'phase_ended', 'hook_prompt', 'update_ask', 'merge_forward',
--     'merge_queue_removal', 'claim_release') and split_part(issue, '-', 1) = '<PROJECT>';
-- update outbox set payload = payload - 'generation'
--   where kind in ('check_wake', 'check_look', 'check_bound') and split_part(issue, '-', 1) = '<PROJECT>';
-- update outbox set payload = payload - 'entry' - 'podUid'
--   where kind in ('issue_suspend', 'issue_bundle', 'issue_release') and split_part(issue, '-', 1) = '<PROJECT>';
-- update outbox set payload = payload - 'podUid' where kind = 'supervise' and split_part(issue, '-', 1) = '<PROJECT>';
-- update outbox set payload = payload - 'generation' - 'entry' - 'oldParent' - 'phase' - 'slot' - 'steps'
--   where kind = 'reparent_seed' and split_part(issue, '-', 1) = '<PROJECT>';
-- -- Each root the conversion made of a hand-over that had landed, as the release before's re-admission leaves one.
-- insert into handed_over_roots (issue, generation, gate_carried)
--   select key, carried_generation, carried_gate from issues
--   where project = '<PROJECT>' and carried_generation <> 0 and tree <> key
--   on conflict (issue) do update set generation = excluded.generation, gate_carried = excluded.gate_carried;
-- insert into tree_lifecycles (project, tree, epoch, authority)
--   select lower('<PROJECT>'), key, 1, 'workflow' from issues
--   where project = '<PROJECT>' and carried_generation <> 0 and tree <> key
--   on conflict (project, tree) do update set epoch = tree_lifecycles.epoch + 1, authority = 'workflow',
--     cleanup_started = false, cleanup_confirmed_at = null, updated_at = now()
--   where tree_lifecycles.cleanup_started and tree_lifecycles.cleanup_confirmed_at is not null;
-- update claims c set tree = i.key, tree_epoch = l.epoch from issues i
--   join tree_lifecycles l on l.project = lower('<PROJECT>') and l.tree = i.key
--   where c.project = lower('<PROJECT>') and c.issue = i.key
--     and i.project = '<PROJECT>' and i.carried_generation <> 0 and i.tree <> i.key;
-- update outbox o set payload = o.payload || jsonb_build_object('tree', i.key)
--     || case when o.payload ? 'treeGeneration' then jsonb_build_object('treeGeneration', i.generation) else '{}'::jsonb end
--   from issues i where o.issue = i.key and o.payload ? 'tree'
--     and i.project = '<PROJECT>' and i.carried_generation <> 0 and i.tree <> i.key;
-- update issues set handed_over_from = case when phase = 'admitted' then tree else '' end, tree = key
--   where project = '<PROJECT>' and carried_generation <> 0 and tree <> key;
-- -- The record of the conversion of one project.
-- delete from tree_conversions where project = '<PROJECT>';
-- -- Each issue of one project with a claim not retired, and the tree it stores, for the relabel of its Sandbox.
-- select key, tree from issues where project = '<PROJECT>'
--   and exists (select 1 from claims c where c.issue = issues.key and c.state <> 'retired') order by key;
alter table issues
    add column retained_until timestamptz,
    add column carried_generation bigint not null default 0 check (carried_generation >= 0),
    add column carried_gate boolean not null default false,
    add column parked_by_architect boolean not null default false,
    add column pending_close jsonb,
    add column seen_seq bigint not null default 0 check (seen_seq >= 0);

alter table claims
    add column launch_tree text not null default '',
    add column issue_epoch bigint not null default 0 check (issue_epoch >= 0),
    add column holds_gate boolean not null default false;

create table issue_lifecycles (
    project text not null,
    issue text not null,
    epoch bigint not null check (epoch > 0),
    authority text not null check (authority in ('workflow', 'operator')),
    cleanup_started boolean not null default false,
    cleanup_confirmed_at timestamptz,
    updated_at timestamptz not null default now(),
    primary key (project, issue)
);

insert into issue_lifecycles (project, issue, epoch, authority)
select distinct on (c.project, c.issue) c.project, c.issue, 1, l.authority
from claims c join tree_lifecycles l on l.project = c.project and l.tree = c.tree
where c.issue <> '' and c.state <> 'retired' and not l.cleanup_started
order by c.project, c.issue, l.authority = 'workflow' desc;

update claims c set issue_epoch = 1
where exists (select 1 from issue_lifecycles l where l.project = c.project and l.issue = c.issue);

create table tree_conversions (
    project text primary key,
    step integer not null check (step > 0),
    converted_at timestamptz
);

alter table outbox
    add column attempt_key text,
    add column attempt_body jsonb;

alter table outbox drop constraint if exists outbox_kind_check;
alter table outbox add constraint outbox_kind_check check (kind in (
  'dispatch_status', 'dispatch_message', 'notice', 'controller_notice', 'supervise', 'gate_seed',
  'linger_close', 'workspace_remove', 'issue_branch', 'issue_suspend', 'check_wake',
  'check_look', 'check_bound', 'role_message', 'phase_ended', 'issue_bundle', 'issue_release',
  'gated_steps_publish', 'claim_release', 'hook_prompt', 'design_gates_publish', 'update_ask',
  'merge_queue_read', 'merge_queue_removal', 'role_holders_publish', 'step_record', 'plan_record',
  'conflict_confirm', 'merge_forward', 'pending_basis_seed', 'reparent_seed', 'retention_end'));
