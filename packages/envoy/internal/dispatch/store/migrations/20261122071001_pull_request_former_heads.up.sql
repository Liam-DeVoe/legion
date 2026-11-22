-- 20261122071001_pull_request_former_heads.up.sql
--
-- The former heads of each open pull request: each head the pull request had while it was open
-- and has since left, which the check states settler computes a state at as it does at an open
-- head, so a tester's result at the head it tested still decides after a push moves the pull
-- request on (api/check_states.go, formerHeadsOf).
--
--   pull_request_former_heads  one row per pull request (repo, number, as pull_request_heads keys
--                              it) and head it left while open, with when it last left that head.
--                              verified is false on a candidate, a commit this migration seeded or
--                              a result recorded since left (below), which no read of the pull
--                              request's commits has confirmed yet: the settler computes no state
--                              there until one does. No foreign key: a row takes
--                              no lock on the pull request's row. The rows of a pull request go
--                              when it closes or merges, under any of its names, or when its row
--                              is deleted: its heads then stop being computed, and a pull request
--                              reopened later has none of the heads it left before. A row deleted
--                              because the pull request is stored again under its repository's
--                              canonical name carries its heads to that name instead (below).
--   pull_request_heads_ended   one row per row of pull_request_heads that was open when its pull
--                              request closed or merged under another of its names: the head it
--                              stood at then, a head of the lifecycle that close ended, which
--                              neither the row's next move nor its re-key makes a former head.
--                              The row goes when that row moves on, closes or is deleted.
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
-- The names of one pull request share one lifecycle. A row stored under an old name can stay
-- open, unread, while the row under the canonical name closes and reopens: the history the old row
-- kept and the head it stands at are of the lifecycle the close ended, and a re-key after the
-- reopen must carry neither. So the close or merge of an open row drops the former heads of every
-- other open row of the pull request (the rows of its number whose repositories have one canonical
-- name) and records the head each stands at in pull_request_heads_ended; a move of such a row on
-- from that head keeps nothing of it and ends the record, since the row moves in the new
-- lifecycle; and a re-key carries the deleted row's head only where no such record names it. Each
-- firing of the function that reads or writes a pull request's history takes that pull request's
-- lock, one for all its names (pull_request_former_heads_lock), after the row locks its statement
-- holds: so a close under one name, a head move under another and the carry of a re-key take the
-- history in one order, and none of them reads it while another writes it. A row's update holds
-- that row and then the lock; the re-key's delete holds the deleted row, then the canonical row,
-- then the lock; a close reads the other rows and locks none of them. No firing waits for a row
-- while it holds the lock, so the lock closes no cycle with the row locks.
--
-- A head an open pull request left before this release is in no write the triggers saw. Such a
-- head needs a state only where someone recorded a result at it that the settle of the pull
-- request's issue reads: a result recorded, on any issue, against a check of that issue's
-- Verification document (api/results_rows.go, loadCheckResults, which the settle's computation
-- reads the results by), in check_results or ci_check_results. So the install seeds, for each open
-- pull request attributed to an issue, each distinct commit of such a result other than the pull
-- request's head, as a candidate: verified false, since a result there does not show that the
-- commit was ever this pull request's. The Verification document is the issue's document of that
-- role, by the predicate the server reads it by (api/artifacts.go, documentRolePredicate), which
-- check_states_owe_check_block (20261121000327) states too. The settler asks GitHub once which of
-- each such pull request's candidates are its own commits and keeps those (api/check_states.go,
-- verifyFormerHeads), and each issue holding a candidate is marked owed here, so that read comes
-- at the settler's first pass. A head left before this release with no result at it is not known,
-- and needs no state.
--
-- The same holds for a result recorded after this release at such a head: no trigger saw the
-- pull request leave it, and the pull request now names another head. So
-- pull_request_former_heads_of_result(), an insert trigger on each result table, leaves the
-- result's commit as a candidate of each open pull request of the issue whose settle reads the
-- result, other than one at that commit, where the pull request holds no row of the commit yet,
-- and marks that issue owed when it left one; the settler confirms it as it confirms the seed's.
-- A head an open pull request left since this release already holds its row, verified, so a
-- result there leaves nothing to read. The seed and the trigger draw from one population, the
-- results of the issue's Verification document, so a result is a candidate whether it was
-- committed before the install, while it ran, or after it.
--
-- One transaction: the tables, the functions, the triggers and the seed commit together, and the
-- install holds the three tables they read and watch from the lock below to its commit: a writer
-- of pull_request_heads, check_results or ci_check_results either committed before the seed's
-- statement, which reads what it wrote, or waits for the install's commit and then fires the
-- triggers. So no head move and no result lands between the seed's read and the triggers. The
-- lock is SHARE ROW EXCLUSIVE, the one the triggers' catalog writes take anyway, taken in one
-- statement in one order: a read of any of the three is not held, every write of them waits.
--
-- No writer of one of the three writes another of them in its transaction: a result's write
-- (api/results.go, recordResultUnderOwnerLocks, a role's or a CI job's) reads pull_request_heads
-- only, under ACCESS SHARE, which the lock leaves free, and a head's write
-- (delivery.UpsertPullRequestHead and the re-key's delete) writes no result. The one write that
-- holds two of them is the delete of an issue or a document, whose foreign keys cascade into both
-- result tables and set pull_request_heads.issue_key to null, in an order of their own; the install
-- holds each lock while it waits for the next, so such a delete can close a cycle with it. Half a
-- second of lock_timeout, shorter than deadlock_timeout (1 s by default), makes the install give
-- up first, before either deadlock check runs: it fails with 55P03 having applied nothing, the
-- delete finishes, and the runner applies it again (store/store.go, RetryLockTimeouts), as
-- 20261101182522 does for its five tables. The seed reads the open rows and the result tables, and
-- writes only the new tables and check_states_owed. The census answers 0: nothing existing is
-- refused or rewritten.
set local lock_timeout = '500ms';

create table pull_request_former_heads (
    repo text not null,
    number integer not null,
    head_sha text not null,
    left_at timestamptz not null default now(),
    verified boolean not null default true,
    primary key (repo, number, head_sha)
);

create table pull_request_heads_ended (
    repo text not null,
    number integer not null,
    head_sha text not null,
    primary key (repo, number)
);

-- The canonical name of a repository's name: the expression of refs.CanonicalRepoSQL, over
-- repo_projects.renamed_to.
create function pull_request_canonical_repo(text) returns text language sql stable as $$
  select coalesce((select max(renamed.renamed_to) from repo_projects renamed
    where renamed.renamed_to is not null and lower(renamed.repo) = lower($1)), $1)
$$;

-- The lock of a pull request, its repository's name and its number, under all its names, to the
-- end of the transaction: the history of one pull request is read and written under it (above).
create function pull_request_former_heads_lock(text, integer) returns void language sql as $$
  select pg_advisory_xact_lock(hashtext('pull-request-former-heads'),
    hashtext(lower(pull_request_canonical_repo($1)) || '#' || $2))
$$;

-- A head row moved from one commit to another while its pull request stays open: the commit it
-- left is kept, verified, and its time is the newest leave, unless a close under another name
-- ended the lifecycle the row stood at it in. A row closed or merged: the former heads of every
-- other open row of its pull request go too, and the head each stands at is recorded as ended. A
-- row closed, merged or keyed anew by an update: the heads kept for its pull request go. A row
-- deleted: its heads go, carried first to the canonical row where the delete re-keys an open pull
-- request (above).
create function pull_request_former_heads_keep() returns trigger language plpgsql as $$
declare
  canonical text := pull_request_canonical_repo(old.repo);
  target record;
  carried boolean := false;
  ended text;
begin
  if tg_op = 'DELETE' and old.state = 'open' and canonical <> old.repo then
    select head.repo, head.number, head.head_sha, head.issue_key into target from pull_request_heads head
    where head.repo = canonical and head.number = old.number and head.state = 'open'
    for no key update;
    carried := found;
  end if;
  perform pull_request_former_heads_lock(old.repo, old.number);
  if tg_op = 'UPDATE' and new.state = 'open' and (new.repo, new.number) = (old.repo, old.number) then
    if old.state = 'open' and new.head_sha <> old.head_sha then
      delete from pull_request_heads_ended where repo = old.repo and number = old.number returning head_sha into ended;
      if ended is distinct from old.head_sha then
        insert into pull_request_former_heads (repo, number, head_sha) values (old.repo, old.number, old.head_sha)
        on conflict (repo, number, head_sha) do update set left_at = excluded.left_at, verified = true;
      end if;
    end if;
    return null;
  end if;
  if tg_op = 'UPDATE' and old.state = 'open' and new.state <> 'open' and (new.repo, new.number) = (old.repo, old.number) then
    delete from pull_request_former_heads former
    using pull_request_heads other
    where other.number = old.number and other.repo <> old.repo and other.state = 'open'
      and lower(pull_request_canonical_repo(other.repo)) = lower(canonical)
      and former.repo = other.repo and former.number = other.number;
    insert into pull_request_heads_ended (repo, number, head_sha)
    select other.repo, other.number, other.head_sha from pull_request_heads other
    where other.number = old.number and other.repo <> old.repo and other.state = 'open'
      and lower(pull_request_canonical_repo(other.repo)) = lower(canonical)
    on conflict (repo, number) do update set head_sha = excluded.head_sha;
  end if;
  if carried then
    -- A pull request back at a head it left holds that head twice here, once as left.
    insert into pull_request_former_heads (repo, number, head_sha, left_at, verified)
    select target.repo, target.number, kept.head_sha, max(kept.left_at), bool_or(kept.verified)
    from (
      select former.head_sha, former.left_at, former.verified from pull_request_former_heads former
      where former.repo = old.repo and former.number = old.number
      union all
      select old.head_sha, now(), true
      where not exists (select 1 from pull_request_heads_ended ended
        where ended.repo = old.repo and ended.number = old.number and ended.head_sha = old.head_sha)
    ) kept
    where kept.head_sha <> target.head_sha
    group by kept.head_sha
    on conflict (repo, number, head_sha) do update set
      left_at = greatest(pull_request_former_heads.left_at, excluded.left_at),
      verified = pull_request_former_heads.verified or excluded.verified;
    perform check_states_owe(target.issue_key);
  end if;
  delete from pull_request_former_heads where repo = old.repo and number = old.number;
  delete from pull_request_heads_ended where repo = old.repo and number = old.number;
  return null;
end $$;

-- A result recorded, a role's or a CI job's: its commit is a candidate of each open pull request
-- of the issue whose settle reads it, the issue of the Verification document its check is a block
-- of, where that pull request is at another commit and holds no row of this one; that issue is
-- marked owed where one was left (above). Each pull request's candidate is written under its lock,
-- the locks taken in the order of their keys, so two results of one issue wait for each other in
-- one order, and a close of the pull request either comes first, when the row read under the lock
-- is closed and takes nothing, or after, when it drops what the result left.
create function pull_request_former_heads_of_result() returns trigger language plpgsql as $$
declare
  owner text;
  pull record;
  left_one boolean := false;
begin
  select document.issue_key into owner from artifacts document
  where document.id = new.check_artifact_id
    and (document.role = 'verification' or (document.role is null and document.issue_key is not null and document.slug = 'verification-md'));
  if owner is null then
    return null;
  end if;
  for pull in
    select head.repo, head.number from pull_request_heads head
    where head.issue_key = owner and head.state = 'open' and head.head_sha <> new.commit_sha
    order by hashtext(lower(pull_request_canonical_repo(head.repo)) || '#' || head.number), head.repo, head.number
  loop
    perform pull_request_former_heads_lock(pull.repo, pull.number);
    insert into pull_request_former_heads (repo, number, head_sha, verified)
    select head.repo, head.number, new.commit_sha, false from pull_request_heads head
    where head.repo = pull.repo and head.number = pull.number and head.issue_key = owner
      and head.state = 'open' and head.head_sha <> new.commit_sha
    on conflict (repo, number, head_sha) do nothing;
    left_one := left_one or found;
  end loop;
  if left_one then
    perform check_states_owe(owner);
  end if;
  return null;
end $$;

lock table pull_request_heads, check_results, ci_check_results in share row exclusive mode;

create trigger pull_request_former_heads_keep_update after update on pull_request_heads
  for each row when ((old.repo, old.number, old.head_sha, old.state) is distinct from (new.repo, new.number, new.head_sha, new.state))
  execute function pull_request_former_heads_keep();
create trigger pull_request_former_heads_keep_delete after delete on pull_request_heads
  for each row execute function pull_request_former_heads_keep();
create trigger pull_request_former_heads_of_check_results after insert on check_results
  for each row execute function pull_request_former_heads_of_result();
create trigger pull_request_former_heads_of_ci_check_results after insert on ci_check_results
  for each row execute function pull_request_former_heads_of_result();

insert into pull_request_former_heads (repo, number, head_sha, verified)
select distinct head.repo, head.number, result.commit_sha, false
from pull_request_heads head
join artifacts document on document.issue_key = head.issue_key
  and (document.role = 'verification' or (document.role is null and document.issue_key is not null and document.slug = 'verification-md'))
join (
  select check_artifact_id, commit_sha from check_results
  union
  select check_artifact_id, commit_sha from ci_check_results
) result on result.check_artifact_id = document.id
where head.state = 'open' and result.commit_sha <> head.head_sha;

select check_states_owe(owed.issue_key)
from (
  select distinct head.issue_key from pull_request_heads head
  join pull_request_former_heads former on former.repo = head.repo and former.number = head.number
  where head.state = 'open' and not former.verified
) owed;
