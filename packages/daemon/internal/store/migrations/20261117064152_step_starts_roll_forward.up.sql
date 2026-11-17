-- 20261117064152_step_starts_roll_forward.up.sql — the roll forward of a rollback of the release
-- that records each step: each issue an earlier release moved has its step_starts rows read
-- 'uncounted', so no row this release wrote before the rollback is taken for a run it did not
-- count.
--
-- The count in step_starts carries a record's id from a run's start to its end, and its mark
-- decides that a run gets one end. Both hold only while this release starts and ends every run of
-- the place. An earlier release starts and ends runs and touches no row, so at the roll forward a
-- row can stand for a run that is over: an end that read it as it stands would write nothing for a
-- run whose place's last counted run had ended, or end that older record with the later run's
-- moment and result where it was still open. The rollback keeps, for each issue with a row,
-- step_run_witness of the issue (20261117004117_step_starts, which says what it reads); this
-- compares it with the witness now. Where they differ the earlier release ended or started a run
-- of the issue. A run the row held open at the rollback is over, ended by that release, which
-- wrote no end: its record gets one here, stopped, at the moment of the rollback, when Legion
-- stopped recording it, as one step_record row of the identity the row kept (record) — the
-- moment the run really ended is nowhere to be read, and a record left without an end would read
-- running for good. Then each row of the issue reads 'uncounted': an end of a run there writes a
-- record of its own at the next count (record.Store.EndUncountedStep), and a start counts on as
-- before. Where the witnesses agree the earlier release did nothing this release counts as a run,
-- so a run still open stands as the one the row counted and ends under its own id. The kept
-- witnesses go, so a later rollback keeps its own.
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
insert into outbox (kind, issue, payload, attempts, next_at, last_error)
select 'step_record', s.issue, s.record || jsonb_build_object('ended_at', r.taken_at, 'result', 'stopped'), 0, now(), ''
from step_starts s join step_starts_rollback r on r.issue = s.issue
where s.run = 'open' and s.record is not null
  and step_run_witness(r.issue, r.tree) is distinct from r.witness;

update step_starts s set run = 'uncounted'
from step_starts_rollback r
where s.issue = r.issue and s.run <> 'uncounted'
  and step_run_witness(r.issue, r.tree) is distinct from r.witness;

delete from step_starts_rollback;
