-- 20261115155340_eval_changes_pending.up.sql
--
-- The eval and Legion tables' change triggers record their changes in the outbox
-- (delivery_changes_pending, 20261115155339) instead of delivery_changes (dispatch://INFRA-2916,
-- R19), so no eval write, refresher write or Legion facts pass takes the change log's lock (6900,
-- 0100): each change row reaches delivery_changes through the drain, in a short transaction of its
-- own after the write commits (delivery_changes_drain, delivery.DrainPending).
--
-- Each function is replaced with `create or replace` and keeps its body as the migration that
-- created it wrote it, every call of delivery_change_log (0100) now a call of
-- delivery_change_log_pending with the same arguments:
--   issue_facts_changed (20261111093100), one change per changed issue_facts row;
--   eval_readings_changed (20261113082200), one change per eval a statement wrote readings of;
--   eval_versions_changed and eval_series_changed (20261113082201), one change per version;
--   eval_days_change_log (20261113082202), one change per eval a transaction wrote day rows of,
--     whose span later writes of the eval in that transaction widen;
--   issue_filings_changed (20261113170751), one change per filing moved, two where it moved
--     project.
--
-- eval_days_change_log widens its transaction's own pending row, by the id
-- delivery_change_log_pending answered, kept in the transaction-local setting
-- dispatch.eval_days_pending as a JSON object of eval id to pending id. It is a setting of its
-- own rather than the dispatch.eval_days_changes the old body kept delivery_changes seqs in, so a
-- transaction open across this migration's commit, holding a seq there, never widens a pending row
-- of another transaction that happens to carry that number. The row it widens is uncommitted and
-- so invisible to every drain until its transaction commits, which no drain can move before it.
--
-- The triggers stay as they are: each calls its function by name, and the replaced body is what
-- every statement after this commit runs. A trigger of the release before this one is the same
-- trigger, so its writes record pending rows too, which this release's drains move (20261115155339).
--
-- Replacing a function locks no table and reads no row; the census answers 0.
create or replace function issue_facts_changed() returns trigger language plpgsql as $$
declare
  ignored constant text[] := array['updated_at'];
begin
  if tg_op = 'INSERT' then
    perform delivery_change_log_pending('issue_facts', null, new.project_key, new.issue_key, new.work_first_at, new.work_last_at);
  elsif tg_op = 'DELETE' then
    perform delivery_change_log_pending('issue_facts', null, old.project_key, old.issue_key, old.work_first_at, old.work_last_at);
  elsif (to_jsonb(old) - ignored) is distinct from (to_jsonb(new) - ignored) then
    perform delivery_change_log_pending('issue_facts', null, new.project_key, new.issue_key,
      least(old.work_first_at, new.work_first_at), greatest(old.work_last_at, new.work_last_at));
  end if;
  return null;
end $$;

create or replace function eval_readings_changed() returns trigger language plpgsql as $$
declare
  written record;
begin
  for written in
    select eval_id, min(measured_at) as first_at, max(measured_at) as last_at
    from inserted
    group by eval_id
    order by eval_id
  loop
    perform delivery_change_log_pending('eval_readings', null, split_part(written.eval_id, '/', 1), written.eval_id,
      written.first_at, written.last_at);
  end loop;
  return null;
end $$;

create or replace function eval_versions_changed() returns trigger language plpgsql as $$
begin
  perform delivery_change_log_pending('eval_versions', null, new.project_key, new.eval_id, null, null);
  return null;
end $$;

create or replace function eval_series_changed() returns trigger language plpgsql as $$
begin
  perform delivery_change_log_pending('eval_versions', null, split_part(new.eval_id, '/', 1), new.eval_id, null, null);
  return null;
end $$;

create or replace function eval_days_change_log(p_project text, p_eval text, p_first date, p_last date)
returns void language plpgsql as $$
declare
  logged constant jsonb := coalesce(nullif(current_setting('dispatch.eval_days_pending', true), ''), '{}')::jsonb;
  logged_id constant bigint := (logged ->> p_eval)::bigint;
  first_at constant timestamptz := p_first::timestamp at time zone 'UTC';
  last_at constant timestamptz := p_last::timestamp at time zone 'UTC';
  pending_id bigint;
begin
  if logged_id is not null then
    update delivery_changes_pending
       set t_old = least(t_old, first_at), t_new = greatest(t_new, last_at)
     where id = logged_id;
    if found then
      return;
    end if;
  end if;
  pending_id := delivery_change_log_pending('eval_days', null, p_project, p_eval, first_at, last_at);
  perform set_config('dispatch.eval_days_pending', (logged || jsonb_build_object(p_eval, pending_id))::text, true);
end $$;

create or replace function issue_filings_changed() returns trigger language plpgsql as $$
begin
  if tg_op = 'INSERT' then
    perform delivery_change_log_pending('issue_filings', null, new.project_key, new.issue_key, null, new.filed_at);
  elsif tg_op = 'DELETE' then
    perform delivery_change_log_pending('issue_filings', null, old.project_key, old.issue_key, old.filed_at, null);
  elsif to_jsonb(old) is distinct from to_jsonb(new) then
    if old.project_key = new.project_key then
      perform delivery_change_log_pending('issue_filings', null, new.project_key, new.issue_key, old.filed_at, new.filed_at);
    else
      perform delivery_change_log_pending('issue_filings', null, old.project_key, old.issue_key, old.filed_at, null);
      perform delivery_change_log_pending('issue_filings', null, new.project_key, new.issue_key, null, new.filed_at);
    end if;
  end if;
  return null;
end $$;
