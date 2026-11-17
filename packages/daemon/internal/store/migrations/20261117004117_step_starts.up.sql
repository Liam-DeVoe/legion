-- 20261117004117_step_starts.up.sql — the count of starts of each step of an issue, which carries a
-- step record's id from the step's start to its end (internal/workflow's StepID), and what a
-- rollback keeps of each issue for the roll forward to compare.
--
-- A record's id is a uuid of the issue, its run, the task, the kind, the place and this count. The
-- start of a step raises the count by one in the transaction that writes the start, and the write
-- that ends the step reads it unchanged, maybe in another transaction of another daemon after a
-- restart: no start of the same place comes between the two, because the step runs until one of
-- the ends ends it. agent and started_at are what that latest start wrote, which the end writes
-- again, so the two writes agree whichever reaches Dispatch first: a merge GitHub already made
-- starts no merger and names no agent, and a start that raised no phase entry has no other
-- record of its moment.
--
-- run is where the latest run of the place stands (record.StepRun). 'open': the latest start's
-- run has no end yet, and the next end of the step running there is its end. 'ended': that run has
-- its end, written in the transaction that set the mark, and a later end of it writes nothing:
-- two writes can each end the same run, as a tree's linger and the hand-over at its close both end
-- a member's step, and the second finds the mark, so Dispatch never takes the later end of a run as
-- its end. 'uncounted': the run the row counted is over, and a run the place holds now started
-- under an earlier release, which counts no start: its end is a record of its own at the next
-- count. Only the roll forward of a rollback writes it (20261117064152_step_starts_roll_forward).
-- The next start of the place sets 'open' whatever stood before. record is the identity of the
-- latest start's record — the fields each write of it carries, its id among them, as the start
-- wrote them (record.Store.RaiseStepStarts) — kept for the roll forward of a rollback, which
-- writes that record's end where an earlier release ended its run; null on a row only an end of an
-- uncounted run wrote, which is ended.
--
-- The count is keyed by task (slice 1 writes the one task, default, of today's fixed phases), and a
-- re-admission does not clear it (record.Store.ClearGeneration keeps it): the generation is part of
-- the id already, and the count only rises. A step running when this release deploys has no row:
-- its end writes one at the count 0, marked ended, with the agent its start would have named and
-- its moment where its role's phase entry records one (null where none does: accepting's).
--
-- An earlier release reads and writes none of this, so after a rollback it moves issues, and starts
-- and ends their steps, with each row standing as this release left it. step_starts_rollback is
-- what the rollback keeps of each issue with a row, run with the daemon stopped
-- (docs/kubernetes.md, "Rolling back the release that records each step"): the issue's tree,
-- step_run_witness of the issue then, and the moment of the rollback. At the roll forward
-- 20261117064152 reads the witness again; an issue whose witness changed had a run of this release
-- ended under it, or a run started that this release did not count: a run open at the rollback
-- gets its end, stopped at that moment, and its rows read 'uncounted' from then on.
--
-- step_run_witness is what this release's own model ends or starts a run on, read from the
-- columns every earlier release writes as it does those things:
--   * the phase the issue stands in, the phase its hold is from where it is held (record.StandsAt):
--     a transition, a leave and a merge's close move it;
--   * the issue's generation: a re-admission, a child's re-entry into its tree and an adoption
--     each open the next one;
--   * the implementer's pass, which every move back raises: an issue that left a phase and came
--     back to it stands where it stood, with its pass raised;
--   * the generation of the tree's root: a re-admission of the root, whose promotion starts its
--     members' steps again with their own generations unchanged;
--   * whether the root lingers while the issue stands in a phase other than merging: the linger
--     ends every member's step but a merge awaiting its pull request (workflow's beginLinger).
-- The tree is the one the rollback found the issue in, so a hand-over, which keeps the record of a
-- merge it hands over open until the re-admission ends it, changes nothing here. An issue with no
-- row in issues reads null, and a null witness against a null one reads as unchanged.
create table step_starts (
  issue text not null,
  task text not null,
  kind text not null,
  place integer not null,
  starts bigint not null,
  agent text not null,
  started_at timestamptz,
  run text not null check (run in ('open', 'ended', 'uncounted')),
  record jsonb,
  primary key (issue, task, kind, place)
);

create table step_starts_rollback (
  issue text primary key,
  tree text not null,
  witness jsonb,
  taken_at timestamptz not null
);

create function step_run_witness(issue_key text, tree_key text) returns jsonb language sql stable as $$
  select jsonb_build_array(
    coalesce(case when i.phase = 'held' then i.held_from end, i.phase),
    i.generation,
    (select p.pass from phases p where p.issue = i.key and p.role = 'implementer'),
    root.generation,
    root.linger_until is not null and coalesce(case when i.phase = 'held' then i.held_from end, i.phase) <> 'merging')
  from issues i left join issues root on root.key = tree_key
  where i.key = issue_key
$$;
