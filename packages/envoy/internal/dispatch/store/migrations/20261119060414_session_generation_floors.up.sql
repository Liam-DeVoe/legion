-- 20261119060414_session_generation_floors.up.sql — the generation an address's first session row
-- starts at, so an address with no session row can be recovered after a Dispatch database restored
-- or made again alone.
--
-- The secrets broker refuses a machine-login approve whose session generation is below one a
-- revocation recorded at the address, and answers a revocation below the greatest it applied there
-- superseded. A Dispatch database made again, or restored to an earlier point, holds generations
-- below the broker's, and the runbook's recovery ("Signing people in") raises each listed address's
-- user_sessions.generation past every generation the lost history can have issued, the broker's
-- plus a million. An address with no user_sessions row had nothing to
-- raise: the row cannot be made by hand (user_sessions_login_fkey needs a people row, which only a
-- sign-in makes), the raise matched no row, and the first sign-in there made the row at generation
-- 1 (end_people_sessions_on_new_account) — below the broker's, so every approve at the address was
-- refused 403 APPROVER_REPLACED and every revocation answered superseded, with nothing counting
-- either. After a database made again that is every address the broker holds a generation for.
--
-- So the recovery writes that same number here instead, for an address with a row and for one
-- without, and every statement that makes a user_sessions row starts the row at that floor:
-- start_session_generation_at_floor, before insert on user_sessions, which the sign-in's trigger,
-- PgSessionStore.EnsureSession and every other writer pass through, so no writer can make the row
-- below the floor, whichever runs first and however long after the recovery. A row that already
-- exists is raised by the recovery's own update, and a generation only rises
-- (user_sessions_binding_columns), so a floor never lowers one. The sweep reads the floor beside
-- the row (greatest of the two), so an address the recovery has floored is no longer listed.
--
-- The floor is what the recovery writes, which is above the broker's generation, not the broker's
-- own number. At the broker's own number every approve would be admitted (the broker refuses an
-- approve whose point is *below* a replacement's generation and supersedes a revocation *below*
-- the greatest it applied), but every cookie the replacement that left that generation issued
-- would match the new row as well, a signed-out one included, so the runbook raises past them
-- (its "past every generation the lost history can have issued"). This table holds whatever
-- number that raise writes.
--
--   session_generation_floors.login        written once, at the insert.
--   session_generation_floors.generation   only rises: the recovery raises a floor it wrote before
--                                          when the broker's generation has moved since, and a
--                                          floor lowered would let the next row start below the
--                                          broker's generation again.
--   session_generation_floors row          never deleted or truncated, for the same reason.
--
-- guard_session_generation_floors refuses every other write with SQLSTATE 23514 (check_violation).
-- Both functions name tables and functions without a schema, so the DO block below sets each one's
-- search_path to pg_catalog, the schema this migration runs in, then pg_temp, as 20261119021826
-- does for the clock guards: a temporary table named session_generation_floors would otherwise
-- decide where a session row starts. The migration makes an empty table, two functions and three
-- triggers, written into the catalogue alone, and reads, refuses and rewrites no row. The function
-- names are Dispatch's own: the broker's migrations may share this schema
-- (TestTheBrokerAndDispatchMigrationsCreateNoNameInCommon).
create table session_generation_floors (
  login text primary key check (login <> '' and login = lower(login)),
  generation bigint not null check (generation > 0)
);

create function guard_session_generation_floors() returns trigger language plpgsql as $$
begin
  if tg_op in ('DELETE', 'TRUNCATE') then
    raise exception 'session_generation_floors holds the generation an address''s session row starts at, which the runbook''s recovery wrote from the generation the secrets broker recorded, and its rows are never removed: a row removed lets the next sign-in start below the broker''s generation again'
      using errcode = 'check_violation';
  end if;
  if new.login is distinct from old.login then
    raise exception 'session_generation_floors.login names the address a floor belongs to, and it never changes'
      using errcode = 'check_violation';
  end if;
  if new.generation < old.generation then
    raise exception 'session_generation_floors.generation is the generation an address''s session row starts at, which the runbook''s recovery wrote from the generation the secrets broker recorded, and it only rises'
      using errcode = 'check_violation';
  end if;
  return new;
end
$$;

create trigger session_generation_floors_guarded before update or delete on session_generation_floors
  for each row execute function guard_session_generation_floors();

create trigger session_generation_floors_not_truncated before truncate on session_generation_floors
  for each statement execute function guard_session_generation_floors();

create function start_session_generation_at_floor() returns trigger language plpgsql as $$
declare
  start_at bigint;
begin
  select generation into start_at from session_generation_floors where login = new.login;
  if start_at > new.generation then
    new.generation := start_at;
  end if;
  return new;
end
$$;

create trigger user_sessions_start_at_floor before insert on user_sessions
  for each row execute function start_session_generation_at_floor();

do $$
begin
  execute format('alter function %I.guard_session_generation_floors() set search_path = pg_catalog, %I, pg_temp', current_schema(), current_schema());
  execute format('alter function %I.start_session_generation_at_floor() set search_path = pg_catalog, %I, pg_temp', current_schema(), current_schema());
end
$$;
