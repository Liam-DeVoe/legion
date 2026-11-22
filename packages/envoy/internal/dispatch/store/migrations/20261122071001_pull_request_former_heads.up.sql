-- 20261122071001_pull_request_former_heads.up.sql
--
-- The former heads of each open pull request: each head the pull request had while it was open
-- and has since left, which the check states settler computes a state at as it does at an open
-- head, so a tester's result at the head it tested still decides after a push moves the pull
-- request on (api/check_states.go, formerHeadsOf).
--
--   pull_request_former_heads  one row per pull request (repo, number, as pull_request_heads keys
--                              it) and head it left while open, with when it last left that head.
--                              verified is false on a head this migration seeded and no read of
--                              the pull request's commits has confirmed yet (below): the settler
--                              computes no state there until one does. No foreign key: a row takes
--                              no lock on the pull request's row. The rows of a pull request go
--                              when it closes or merges, or when its row is deleted: its heads then
--                              stop being computed, and a pull request reopened later has none of
--                              the heads it left before. A row deleted because the pull request is
--                              stored again under its repository's canonical name carries its
--                              heads to that name instead (below).
--
-- pull_request_former_heads_keep() keeps it from the writes of pull_request_heads, in each
-- writer's own transaction, by an update trigger and a delete trigger. There is no insert trigger:
-- a new row has left no head yet. The update trigger fires only where the key, head_sha or state
-- moved: every writer of the row names each column in its `do update set` on each write, a
-- read-through included. So a head is kept whether or not a settle ran while the pull request was
-- there, every writer is covered, a write of the release before this one too, and a writer a later
-- change adds is covered with no code of its own.
--
-- A delete of an open row whose repository the canonical rule names otherwise (the expression of
-- refs.CanonicalRepoSQL, over repo_projects.renamed_to) is the re-key of a pull request stored
-- under a name its repository was renamed away from, whichever build deletes it
-- (delivery.upsertHeadUnderCanonicalName, and the release before this one, which carries nothing
-- itself): the function locks the canonical row, and where that row is open it carries the
-- deleted row's former heads, and the deleted row's own head where the canonical row is at
-- another, to the canonical key, and marks the canonical row's issue owed (check_states_owe), all
-- in the deleting transaction: the canonical row may be written with the head, state and issue it
-- already holds, which marks nothing, and the deleted row may have no issue, so no other trigger
-- owes the settle that publishes the carried heads. The delete holds the deleted row's lock and
-- the function takes the canonical row's before either's heads are read, so a head write to
-- either row commits before the carry reads it or waits until the carry has committed, and a close
-- of the canonical row comes either first, when nothing is carried, or after, when it drops what
-- was carried.
--
-- A head an open pull request left before this release is in no write the triggers saw. Such a
-- head needs a state only where someone recorded a result at it, so the install seeds, for each
-- open pull request attributed to an issue, each distinct commit of a result recorded on that
-- issue (check_results, ci_check_results) other than the pull request's head, as a candidate:
-- verified false, since a result on the issue does not show that the commit was ever this pull
-- request's. The settler reads each such pull request's commits once and keeps the candidates
-- among them (api/check_states.go, verifyFormerHeads), and each issue holding a candidate is
-- marked owed here, so that read comes at the settler's first pass. A head left before this
-- release with no result at it is not known, and needs no state.
--
-- One transaction: the table, the function, both triggers and the seed commit together. The
-- triggers' catalog write takes pull_request_heads' SHARE ROW EXCLUSIVE, held to the commit, so a
-- head write waits for the seed rather than landing between it and the triggers. The seed reads
-- the open rows and the result tables, and writes only the new table and check_states_owed. The
-- census answers 0: nothing existing is refused or rewritten.
create table pull_request_former_heads (
    repo text not null,
    number integer not null,
    head_sha text not null,
    left_at timestamptz not null default now(),
    verified boolean not null default true,
    primary key (repo, number, head_sha)
);

-- A head row moved from one commit to another while its pull request stays open: the commit it
-- left is kept, verified, and its time is the newest leave. A row closed, merged or keyed anew by
-- an update: the heads kept for its pull request go. A row deleted: its heads go, carried first to
-- the canonical row where the delete re-keys an open pull request (above).
create function pull_request_former_heads_keep() returns trigger language plpgsql as $$
declare
  canonical text;
  target record;
begin
  if tg_op = 'UPDATE' and new.state = 'open' and (new.repo, new.number) = (old.repo, old.number) then
    if old.state = 'open' and new.head_sha <> old.head_sha then
      insert into pull_request_former_heads (repo, number, head_sha) values (old.repo, old.number, old.head_sha)
      on conflict (repo, number, head_sha) do update set left_at = excluded.left_at, verified = true;
    end if;
    return null;
  end if;
  if tg_op = 'DELETE' and old.state = 'open' then
    select coalesce((select max(renamed.renamed_to) from repo_projects renamed
      where renamed.renamed_to is not null and lower(renamed.repo) = lower(old.repo)), old.repo) into canonical;
    if canonical <> old.repo then
      select head.repo, head.number, head.head_sha, head.issue_key into target from pull_request_heads head
      where head.repo = canonical and head.number = old.number and head.state = 'open'
      for no key update;
      if found then
        -- A pull request back at a head it left holds that head twice here, once as left.
        insert into pull_request_former_heads (repo, number, head_sha, left_at, verified)
        select target.repo, target.number, kept.head_sha, max(kept.left_at), bool_or(kept.verified)
        from (
          select former.head_sha, former.left_at, former.verified from pull_request_former_heads former
          where former.repo = old.repo and former.number = old.number
          union all
          select old.head_sha, now(), true
        ) kept
        where kept.head_sha <> target.head_sha
        group by kept.head_sha
        on conflict (repo, number, head_sha) do update set
          left_at = greatest(pull_request_former_heads.left_at, excluded.left_at),
          verified = pull_request_former_heads.verified or excluded.verified;
        perform check_states_owe(target.issue_key);
      end if;
    end if;
  end if;
  delete from pull_request_former_heads where repo = old.repo and number = old.number;
  return null;
end $$;

create trigger pull_request_former_heads_keep_update after update on pull_request_heads
  for each row when ((old.repo, old.number, old.head_sha, old.state) is distinct from (new.repo, new.number, new.head_sha, new.state))
  execute function pull_request_former_heads_keep();
create trigger pull_request_former_heads_keep_delete after delete on pull_request_heads
  for each row execute function pull_request_former_heads_keep();

insert into pull_request_former_heads (repo, number, head_sha, verified)
select distinct head.repo, head.number, result.commit_sha, false
from pull_request_heads head
join (
  select issue_key, commit_sha from check_results
  union
  select issue_key, commit_sha from ci_check_results
) result on result.issue_key = head.issue_key
where head.state = 'open' and result.commit_sha <> head.head_sha;

select check_states_owe(owed.issue_key)
from (
  select distinct head.issue_key from pull_request_heads head
  join pull_request_former_heads former on former.repo = head.repo and former.number = head.number
  where head.state = 'open' and not former.verified
) owed;
