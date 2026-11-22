-- 20261122042244_pull_request_former_heads.up.sql
--
-- The former heads of each open pull request: each head the pull request had while it was open
-- and has since left, which the check states settler computes a state at as it does at an open
-- head, so a tester's result at the head it tested still decides after a push moves the pull
-- request on (api/check_states.go, formerHeadsOf).
--
--   pull_request_former_heads  one row per pull request (repo, number, as pull_request_heads keys
--                              it) and head it left while open, with when it last left that head.
--                              No foreign key: a row takes no lock on the pull request's row. The
--                              rows of a pull request go when it closes or merges, or when its row
--                              is deleted: its heads then stop being computed, and a pull request
--                              reopened later has none of the heads it left before.
--
-- pull_request_former_heads_keep() keeps it from the writes of pull_request_heads, in each
-- writer's own transaction; the migration after this one attaches it. So a head is kept whether or
-- not a settle ran while the pull request was there, every writer is covered, a write of the
-- release before this one too, and a writer a later change adds is covered with no code of its own.
--
-- A new table and a new function: nothing existing is read, locked or rewritten, and the release
-- before this one neither reads nor writes them, so it rolls beside this one (cmd/dispatch/README.md,
-- "Database migrations"). The census answers 0.
create table pull_request_former_heads (
    repo text not null,
    number integer not null,
    head_sha text not null,
    left_at timestamptz not null default now(),
    primary key (repo, number, head_sha)
);

-- A head row moved from one commit to another while its pull request stays open: the commit it
-- left is kept, and its time is the newest leave. A row closed, merged, deleted or keyed anew: the
-- heads kept for its pull request go.
create function pull_request_former_heads_keep() returns trigger language plpgsql as $$
begin
  if tg_op = 'DELETE' then
    delete from pull_request_former_heads where repo = old.repo and number = old.number;
  elsif new.state <> 'open' or (new.repo, new.number) is distinct from (old.repo, old.number) then
    delete from pull_request_former_heads where repo = old.repo and number = old.number;
  elsif old.state = 'open' and new.head_sha <> old.head_sha then
    insert into pull_request_former_heads (repo, number, head_sha) values (old.repo, old.number, old.head_sha)
    on conflict (repo, number, head_sha) do update set left_at = excluded.left_at;
  end if;
  return null;
end $$;
