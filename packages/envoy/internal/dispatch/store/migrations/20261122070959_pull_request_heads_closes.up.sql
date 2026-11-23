-- 20261122070959_pull_request_heads_closes.up.sql
--
-- Every close of a pull request Dispatch stores, on the database's own clock, kept for good
-- (pull_request_heads_closes): one row for each write that makes a row of pull_request_heads
-- closed or merged, its insert or an update from open, its re-key included, whichever writer and
-- whichever build wrote it, under the name of the row written (lowercased), with the clock's time
-- at that write (closed_at, clock_timestamp()), which comes after Dispatch read the close from
-- GitHub, and so after the close. No write of pull_request_heads changes or deletes a row of it:
-- a reopen, a re-key and a delete of the pull request's row leave every close recorded before
-- them. pull_request_heads.event_at is not that time: it is the pull request's updated_at on
-- GitHub's clock, which each later edit of the closed pull request moves, and a write that names
-- none keeps an earlier one.
--
-- The former heads install (migration 20261122071001) commits in a transaction of its own after
-- this one, and a writer of the release before it can close a pull request and reopen it between
-- the two, which leaves its row open. So the install reads this record, not the rows' states
-- alone: a close before it ends the lifecycle it closed, and no result recorded before that close
-- crosses a reopen, whatever reopen, re-key or delete came after it (that migration says how).
--
-- This migration also records each pull request closed or merged when it runs, at the time it
-- runs: nothing of Dispatch's says when such a pull request closed, only that it closed before
-- now, so a result recorded after now was recorded after its close, and the install takes each
-- result recorded before now as recorded before it. A pull request is closed where the row of its
-- canonical name (pull_request_canonical_repo, below) is not open, or, with no row of that name,
-- where no row of it is open: the canonical row is the one Dispatch reads, and a row stored under
-- an old name may be a stale copy, so an open pull request whose old name's row reads closed is
-- recorded as no close here, the rule the install reads a pull request by. A write that closes
-- such an old name's row later is recorded as every close is, as the settler, once the install
-- runs, ends the lifecycle at a row written closed under any name of an open pull request.
--
-- pull_request_heads_closes holds no key and its index is not unique, so the trigger's insert
-- waits for no other transaction, and the trigger reads nothing: it adds no lock to a writer's
-- transaction. The first CREATE TRIGGER takes SHARE ROW EXCLUSIVE on pull_request_heads, after
-- waiting for each transaction that holds the table, a wait the runner bounds at
-- pgmigrate.LockTimeout and tries again, and holds it to the commit, so a write of the table either
-- committed before the record of the rows closed now reads it or waits for the commit and fires
-- the triggers: no close is in neither. The release before this one neither reads nor writes the
-- new table, and its writes fire the triggers too, so the two roll side by side. Nothing existing
-- is refused or rewritten: the census answers 0.
create table pull_request_heads_closes (
    repo text not null,
    number integer not null,
    closed_at timestamptz not null default clock_timestamp()
);
create index pull_request_heads_closes_number on pull_request_heads_closes (number, repo);

-- The canonical name of a repository's name: the expression of refs.CanonicalRepoSQL, over
-- repo_projects.renamed_to. The install (20261122071001) and the settler read it too.
create function pull_request_canonical_repo(text) returns text language sql stable as $$
  select coalesce((select max(renamed.renamed_to) from repo_projects renamed
    where renamed.renamed_to is not null and lower(renamed.repo) = lower($1)), $1)
$$;

-- A write of pull_request_heads that makes its row closed or merged (its triggers' when says
-- which): one record of the close, under the name of the row written. It appends: it waits for no
-- lock.
create function pull_request_heads_closes_keep() returns trigger language plpgsql as $$
begin
  insert into pull_request_heads_closes (repo, number) values (lower(new.repo), new.number);
  return null;
end $$;

create trigger pull_request_heads_closes_insert after insert on pull_request_heads
  for each row when (new.state <> 'open')
  execute function pull_request_heads_closes_keep();
create trigger pull_request_heads_closes_update after update on pull_request_heads
  for each row when (new.state <> 'open' and old.state = 'open')
  execute function pull_request_heads_closes_keep();

-- Each pull request closed or merged now, by the rule above, at this time: one record of each of
-- its closed rows that decide it. Each name's canonical name is read once.
with named as materialized (
  select lower(head.repo) as repo, head.number, head.state <> 'open' as closed,
    lower(pull_request_canonical_repo(head.repo)) as pull_repo
  from pull_request_heads head
)
insert into pull_request_heads_closes (repo, number)
select head.repo, head.number from named head
where head.closed and not exists (
  select 1 from named other
  where other.number = head.number and other.pull_repo = head.pull_repo and not other.closed
    and (other.repo = other.pull_repo or head.repo <> head.pull_repo));
