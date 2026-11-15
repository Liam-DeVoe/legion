-- 20261115155339_delivery_changes_pending.up.sql
--
-- The change log's outbox (dispatch://INFRA-2916, R19): the eval and Legion writes record their
-- change rows here, taking no lock, and one short transaction of its own moves them into
-- delivery_changes under the change log's lock (6900, 0100).
--
-- Every insert into delivery_changes takes the advisory lock 6900 and holds it to its commit, so a
-- row's seq is allocated only after every row below it has committed or rolled back, and a reader
-- whose cursor stands at a seq never meets a row below it later: a reader's cursor never skips a
-- change row. The eval and Legion writes are long (a correction of 1,400 readings refolds its days,
-- a facts pass writes a project's rows) and took 6900 first, to keep the lock order free of cycles,
-- so they held it for the whole write: a median 1.53 s at a correction of 1,400 readings, which an
-- intake statement waited out (632 ms median against 5 ms). So their change rows wait here instead,
-- and only the drain takes 6900 for them.
--
-- delivery_changes_pending holds exactly the columns a delivery_changes row takes: kind, run_kind,
-- repo, ref, t_old, t_new and at, set by clock_timestamp() when the pending row is written, which
-- the move keeps. Its kinds are the five the eval and Legion tables' triggers write, which
-- 20261115155340 points here: issue_facts, eval_readings, eval_versions, eval_days and
-- issue_filings. The delivery intake's kinds (pull_request, run, run_job, session_group, settings)
-- are still written into delivery_changes directly, by one-statement writes or by transactions
-- that take 6900 first or in their last statement (delivery.LockChanges).
--
-- delivery_change_log_pending(kind, run_kind, repo, ref, t_old, t_new) inserts one pending row,
-- answers its id (eval_days_change_log widens its own transaction's row by it), and notifies
-- 'delivery_changes', which Postgres delivers at the writer's commit. It takes no lock.
--
-- delivery_changes_drain(batch) moves up to batch committed pending rows, the lowest ids, into
-- delivery_changes and answers how many it moved. It reads first whether any row is pending and,
-- where none is, returns at once, taking no lock. Otherwise it takes 6900 and, in one statement,
-- deletes those pending rows and inserts them into delivery_changes in id order, keeping each
-- row's at; where it moved any, it notifies 'delivery_changes', as every insert into
-- delivery_changes does at its commit. Its caller runs it as a statement of its own, again while a
-- whole batch moved (delivery.DrainPending, 250 rows a batch), so each call holds 6900 for one
-- batch's statement and its commit alone, whatever the burst: on the pod it was measured on, a
-- batch of 1,000 answered in up to 94 ms under load, and 10,000 rows in one statement held 6900
-- 118-328 ms. Each statement of the function reads a snapshot taken when it starts, under read
-- committed, so the move reads every row committed before it took the lock: two drains take turns
-- on the lock, the second finds the rows the first deleted gone, and each pending row is moved
-- exactly once. A pending row's id is drawn at its insert, not its commit, so two writers can
-- commit out of id order and a drain between their commits moves the higher id first: seq follows
-- the order the drains move rows in, not the writers' commit order, and is allocated under the
-- lock all the same, so no reader's cursor skips a moved row either.
--
-- Who drains: the writer, right after its own commit and before its route answers; the Delivery
-- page's live hub, before each read of the log, which the pending row's notification wakes in
-- every live process at the writer's commit; the day-row refresher, at the start of each pass;
-- and the server's boot, once, before it serves.
--
-- A writer killed between its commit and its drain loses nothing: its pending row committed with
-- its rows, and its notification reached every live process's hub at that commit, which drains on
-- that wake. With no process alive, the next server's boot drains before it serves, its drain's
-- notification wakes every hub listening by then, and that process's refresher reads the moved
-- rows on its first pass. So a view a lost drain would have woken is woken by the next wake of any
-- live process, within milliseconds, or else by the boot of the next one.
--
-- The release before this one never reads this table, and writes the five kinds through the
-- triggers 20261115155340 points here, so its rows are moved by this release's drains: its own
-- hub never drains, and every process of this release that is alive during the roll, the one that
-- applied this migration among them, drains on the notification each of those rows sends.
--
-- A new table and two new functions: no existing table is locked, read or rewritten, and the
-- census answers 0.
create table delivery_changes_pending (
  id       bigserial primary key,
  at       timestamptz not null default clock_timestamp(),
  kind     text not null check (kind in ('issue_facts', 'eval_readings', 'eval_versions', 'eval_days', 'issue_filings')),
  run_kind text,
  repo     text,
  ref      text,
  t_old    timestamptz,
  t_new    timestamptz
);

create function delivery_change_log_pending(p_kind text, p_run_kind text, p_repo text, p_ref text, p_old timestamptz, p_new timestamptz)
returns bigint language plpgsql as $$
declare
  pending_id bigint;
begin
  insert into delivery_changes_pending (kind, run_kind, repo, ref, t_old, t_new)
    values (p_kind, p_run_kind, p_repo, p_ref, p_old, p_new)
    returning id into pending_id;
  perform pg_notify('delivery_changes', '');
  return pending_id;
end $$;

create function delivery_changes_drain(p_batch integer) returns bigint language plpgsql as $$
declare
  moved bigint;
begin
  if not exists (select 1 from delivery_changes_pending) then
    return 0;
  end if;
  perform pg_advisory_xact_lock(6900, 0);
  with taken as (
    delete from delivery_changes_pending
    where id in (select id from delivery_changes_pending order by id limit p_batch)
    returning *
  )
  insert into delivery_changes (at, kind, run_kind, repo, ref, t_old, t_new)
  select at, kind, run_kind, repo, ref, t_old, t_new from taken order by id;
  get diagnostics moved = row_count;
  if moved > 0 then
    perform pg_notify('delivery_changes', '');
  end if;
  return moved;
end $$;
