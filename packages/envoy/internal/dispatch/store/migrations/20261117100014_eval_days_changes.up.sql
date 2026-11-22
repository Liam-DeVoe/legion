-- 20261117100014_eval_days_changes.up.sql
--
-- eval_days logs its changes (dispatch://INFRA-2916, R10), so a computed eval's view wakes once its
-- day rows are rewritten. The input's own change (a pull request, a run, an issue's facts) wakes
-- the views before the refresher has rewritten the day rows it touches; a view that read then
-- would answer the old rows, and with no change of the rows' own it would never read again. Its
-- own migration, as 20261117100002 is for issue_facts: the transaction holds eval_days's lock
-- alone, for milliseconds.
--
-- One change row per eval for every transaction that writes its day rows. The refresher writes a
-- series' days with one upsert, `insert ... on conflict ... do update`, and Postgres fires an
-- upsert's statement-level triggers once for its inserted rows and once for its updated rows, each
-- with a transition table of its own half. So two statement-level triggers read the two halves,
-- and both hand each eval's span to eval_days_change_log, which records the eval's first change of
-- the transaction in the change log's outbox through delivery_change_log_pending (20261117100001),
-- which takes no lock, so no write of the day rows takes the change log's lock (6900, 0100); a
-- drain moves the row into delivery_changes after the commit (R19, delivery.DrainPending). Every
-- later write of the eval in the transaction, the other half of the upsert or another statement,
-- widens that same pending row's span. The row it widens is this transaction's own, uncommitted
-- and so invisible to every drain until the commit, which delivers the one notification. The id
-- of each eval's pending row, which delivery_change_log_pending answers, is kept in the
-- transaction-local setting dispatch.eval_days_pending, a JSON object of eval id to pending id:
-- set_config(..., true) reverts it when the transaction ends, and with a subtransaction that
-- rolls back, as it does the row.
--
-- repo holds the project key and ref the eval id, as an eval_readings row does (20261117100012).
-- t_old and t_new are midnight UTC of the first and the last day the eval's written rows hold, so
-- the span names those days and the days between them.
--
-- An update logs only the rows whose content moved: as 0100's and 20261117100002's triggers ignore
-- the columns a pass rewrites whatever it finds, the update half ignores updated_at and built_by,
-- so a new build that rebuilds a row to the same values logs nothing. The old and the new rows are
-- matched by the primary key, and a row whose key an update moved matches nothing on either side,
-- so both its old day and its new one are logged. A delete logs nothing: a day the refresher
-- finds no input for keeps an explicit zero row rather than losing its row, and the rows a
-- project's deletion takes go with every view of that project.
--
-- The census answers 0: three functions and two triggers, reading and rewriting no row.
create function eval_days_change_log(p_project text, p_eval text, p_first date, p_last date)
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

create function eval_days_inserted() returns trigger language plpgsql as $$
declare
  span record;
begin
  for span in
    select project_key, eval_id, min(day) as first_day, max(day) as last_day
    from written
    group by project_key, eval_id
    order by project_key, eval_id
  loop
    perform eval_days_change_log(span.project_key, span.eval_id, span.first_day, span.last_day);
  end loop;
  return null;
end $$;

create function eval_days_updated() returns trigger language plpgsql as $$
declare
  ignored constant text[] := array['updated_at', 'built_by'];
  span record;
begin
  for span in
    select project_key, eval_id, min(day) as first_day, max(day) as last_day
    from written new_row
    full join replaced old_row using (project_key, eval_id, writer, series, repo, day)
    where (to_jsonb(old_row) - ignored) is distinct from (to_jsonb(new_row) - ignored)
    group by project_key, eval_id
    order by project_key, eval_id
  loop
    perform eval_days_change_log(span.project_key, span.eval_id, span.first_day, span.last_day);
  end loop;
  return null;
end $$;

create trigger eval_days_inserted after insert on eval_days
  referencing new table as written
  for each statement execute function eval_days_inserted();

create trigger eval_days_updated after update on eval_days
  referencing old table as replaced new table as written
  for each statement execute function eval_days_updated();
