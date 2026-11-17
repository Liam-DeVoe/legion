-- 20261117064152_step_starts_roll_forward.up.sql — the roll forward of a rollback of the release
-- that records each step: each run of an issue that an earlier release ended gets its end, and
-- each place of an issue where it may have started a run reads 'uncounted', so no row this
-- release wrote before the rollback is taken for a run it did not count.
--
-- The count in step_starts carries a record's id from a run's start to its end, and its mark
-- decides that a run gets one end. Both hold only while this release starts and ends every run of
-- the place. An earlier release starts and ends runs and touches no row, so at the roll forward a
-- row can stand for a run that is over: an end that read it as it stands would write nothing for a
-- run whose place's last counted run had ended, or end that older record with the later run's
-- moment and result where it was still open. The rollback keeps, for each issue with a row,
-- step_run_witness of the issue (20261117004117_step_starts, which says what it reads); this
-- compares it with the witness now, for each row of the issue.
--
-- A run the row held open at the rollback is over where the earlier release did what ends it in
-- this release: the issue moved (its phase, its generation or its pass), the role that works the
-- place had its entry raised, which a start of that place does after the run before it ended, or,
-- for a step that is no merge, its root lingered or was re-admitted. A merge runs on through the
-- linger and the root's re-admission, until its merger starts again, it lands, or the linger's
-- close retires it: a close the release applied, at a generation of the root from the rollback's
-- to now's, marks its fact processed ('linger:<root>:<generation>', internal/daemon's outbox), and
-- it ends the merge of a member it did not hand over whose pull request is not open, or whose pull
-- request closed after it (workflow's lingerExpired and closed). Such a run gets its end here,
-- stopped, at the moment of the rollback, when Legion stopped recording it, as one step_record
-- row of the identity the row kept (record) — the moment the run really ended is nowhere to be
-- read, and a record left without an end would read running for good.
--
-- A place reads 'uncounted' where the earlier release may have started a run there: the role
-- that works it had its entry raised; it is accepting or the merge, whose starts can raise no
-- entry (the architect's, and a merge GitHub had made already), and the issue moved on from
-- another phase or moved back; or the issue's generation or its root's changed, whose
-- re-admission or promotion may start any place again. An end of a run there writes a record of
-- its own at the next count (record.Store.EndUncountedStep), and a start counts on as before. A
-- run that still runs is no such place: nothing starts a place while its run is open. A run the
-- earlier release ended at a place it started none reads 'ended', so a late completion of that
-- run still writes its report on the record this release counted (workflow's reportEndedStep),
-- and an end with no run open there writes nothing. A run the earlier release did not end stands
-- as the one the row counted and ends under its own id. The kept witnesses go, so a later
-- rollback keeps its own.
--
-- On a database no rollback touched step_starts_rollback is empty and this changes nothing. The
-- way back below is what makes it run again: it keeps the witnesses and deletes this migration's
-- row, so the roll forward's Migrate applies this file again (store.apply decides each migration
-- on its own row). Run it with the daemon stopped, before the earlier image is pinned. A second
-- rollback before any roll forward keeps the witnesses of the first, which the earlier release has
-- moved since. docs/kubernetes.md, "Rolling back the release that records each step", carries the
-- same block, and internal/textcheck holds the two to one text.
--
-- insert into step_starts_rollback (issue, tree, witness, taken_at)
-- select key, tree, step_run_witness(key, tree), now() from issues
-- where key in (select issue from step_starts)
-- on conflict (issue) do nothing;
-- delete from schema_version where version = 20261117064152;
with witnessed as (
  select s.issue, s.task, s.kind, s.place, s.run, s.record, r.tree, r.taken_at, r.witness as was,
    step_run_witness(r.issue, r.tree) as now,
    -- The role whose entry each start of the place raises (workflow's stepOf): accepting's, the
    -- architect's, raises none.
    case s.kind when 'merge' then 'merger' when 'retro' then 'implementer'
      else (array['planner', 'implementer', 'tester', 'reviewer'])[s.place] end as worker,
    -- The phase of a place whose start may raise no entry.
    case when s.kind = 'merge' then 'merging' when s.kind = 'step' and s.place = 5 then 'accepting' end as entryless
  from step_starts s join step_starts_rollback r on r.issue = s.issue
), judged as (
  select w.*, w.run = 'open' and (
      w.was->'at' is distinct from w.now->'at' or w.was->'generation' is distinct from w.now->'generation'
      or w.was->'pass' is distinct from w.now->'pass'
      or w.was->'entries'->w.worker is distinct from w.now->'entries'->w.worker
      or w.kind <> 'merge' and (w.was->'root_generation' is distinct from w.now->'root_generation'
        or w.was->'lingers' is distinct from w.now->'lingers')
      or w.kind = 'merge' and exists (
          select 1 from generate_series((w.was->>'root_generation')::bigint, (w.now->>'root_generation')::bigint) g
          join processed_events p on p.source = 'outbox' and p.event_id = 'linger:' || w.tree || ':' || g
          where not exists (select 1 from handovers h where h.issue = w.issue and h.tree = w.tree and h.linger_generation = g))
        and not exists (select 1 from pull_requests pr where pr.issue = w.issue and pr.state = 'open')) as ended
  from witnessed w
), counted as (
  select j.*, (j.run <> 'open' or j.ended) and (
      j.was->'generation' is distinct from j.now->'generation'
      or j.was->'root_generation' is distinct from j.now->'root_generation'
      or j.was->'entries'->j.worker is distinct from j.now->'entries'->j.worker
      or j.entryless is not null
        and (j.was->'at' is distinct from j.now->'at' or j.was->'pass' is distinct from j.now->'pass')
        and not (j.was->>'at' is not distinct from j.entryless and j.was->'pass' is not distinct from j.now->'pass')) as uncounted
  from judged j
), ends as (
  insert into outbox (kind, issue, payload, attempts, next_at, last_error)
  select 'step_record', issue, record || jsonb_build_object('ended_at', taken_at, 'result', 'stopped'), 0, now(), ''
  from counted where ended and record is not null
)
update step_starts s set run = case when c.uncounted then 'uncounted' else 'ended' end
from counted c
where s.issue = c.issue and s.task = c.task and s.kind = c.kind and s.place = c.place
  and s.run <> 'uncounted' and (c.uncounted or c.ended);

delete from step_starts_rollback;
