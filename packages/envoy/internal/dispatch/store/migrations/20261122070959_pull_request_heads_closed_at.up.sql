-- 20261122070959_pull_request_heads_closed_at.up.sql
--
-- When Dispatch first stored a pull request's row closed or merged, on the database's own clock
-- (pull_request_heads.closed_at). A trigger sets it on the write that makes the row closed or
-- merged, its insert or an update from open, whichever writer and whichever build wrote it, and
-- keeps it on every later write of the row while the row stays closed or merged, a write dated
-- with a later time GitHub gave included; a write that makes the row open clears it.
-- pull_request_heads.event_at is not that time: it is the pull request's updated_at on GitHub's
-- clock, which each later edit of the closed pull request moves, and a write that names none keeps
-- an earlier one.
--
-- The former heads install (migration 20261122071001) orders each result it finds, by when
-- Dispatch recorded it (created_at), against the close of each pull request closed when it runs,
-- by this time alone: one clock, the database's. The time is the clock's at the row's write
-- (clock_timestamp()), which comes after Dispatch read the close from GitHub, and so after the
-- close, so a result whose transaction began later began after the close.
--
-- Null on every row written closed before this migration: no record of Dispatch's says when such a
-- pull request closed, and the install keeps out every result it finds of it (that migration says
-- why and what it costs).
--
-- Adding a nullable column with no default is a catalog change that rewrites no row; the function
-- and the trigger are catalog writes. The ALTER takes ACCESS EXCLUSIVE on pull_request_heads and
-- the CREATE TRIGGER SHARE ROW EXCLUSIVE, each for the catalog write alone, after waiting for each
-- transaction that holds the table, a wait the runner bounds at pgmigrate.LockTimeout and tries
-- again. The trigger only sets a column of the row written: it reads nothing and waits for no lock.
-- The release before this one neither reads nor writes the column, and its insert names its
-- columns, so the two roll side by side, and the trigger dates that release's writes too. The
-- census answers 0.
alter table pull_request_heads add column closed_at timestamptz;

create function pull_request_heads_closed_at() returns trigger language plpgsql as $$
begin
  if new.state = 'open' then
    new.closed_at := null;
  elsif tg_op = 'UPDATE' and old.state <> 'open' then
    new.closed_at := old.closed_at;
  else
    new.closed_at := clock_timestamp();
  end if;
  return new;
end $$;

create trigger pull_request_heads_closed_at before insert or update on pull_request_heads
  for each row execute function pull_request_heads_closed_at();
