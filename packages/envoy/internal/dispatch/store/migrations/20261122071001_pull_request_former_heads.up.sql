-- 20261122071001_pull_request_former_heads.up.sql
--
-- The former heads of each open pull request: each head the pull request had while it was open
-- and has since left, which the check states settler computes a state at as it does at an open
-- head, so a tester's result at the head it tested still decides after a push moves the pull
-- request on (api/check_states.go, formerHeadsOf).
--
-- No trigger here makes a writer wait for a lock. Each trigger this migration puts on a writer's
-- table (pull_request_heads, check_results, ci_check_results) only reads and appends one row to a
-- table that has no unique key and no foreign key, and marks an issue owed (check_states_owe, whose
-- mark has a key a sequence draws and whose notification queues at the commit, after every lock
-- the transaction takes); an insert into such a table waits for no other transaction, and a read
-- under READ COMMITTED waits for none either. Everything that reads and rewrites the record of
-- former heads runs in the check states settler: under the issue's lease
-- (pull_request_former_heads_drain), and under the pull request's lock, one for all its names
-- (pull_request_former_heads_fold), which no writer takes.
-- So these triggers add no lock to a writer's transaction, and none can close a cycle with the
-- locks the writers take themselves, each in its own order: a result's write takes the owner locks
-- and then inserts (api/results.go, recordResultUnderOwnerLocks), a head's write takes its row and
-- then the owner locks (delivery.UpsertPullRequestHead, appendAttributionChange), a change of an
-- issue's links takes the owner lock and then the rows (delivery.LinksChanged), and the re-key's
-- delete takes the old row alone. A trigger that took a lock one writer holds while another waits
-- for it in the other order would close such a cycle, which Postgres breaks by failing one writer.
--
--   pull_request_former_heads  one row per pull request (repo, number, as pull_request_heads keys
--                              it) and head it left while open, with when it last left that head
--                              and since, the place in pull_request_former_heads_order at which the
--                              pull request reached that head, or at which the row was made, null
--                              for a head reached before this release. verified is false on a
--                              candidate, a commit of the results of the pull request's issue
--                              (below), which no read of the pull request's commits has confirmed
--                              yet: the settler computes no state there until one does. No foreign
--                              key, and only the settler writes it.
--   pull_request_former_heads_moves
--                              the writers' record: one row for each write of pull_request_heads
--                              that can move a pull request's history, with its place in
--                              pull_request_former_heads_order and the row before and after, which
--                              pull_request_former_heads_keep() appends in the writer's own
--                              transaction, whichever build wrote the row. No key: an insert waits
--                              for nothing. The fold deletes each row it applied.
--   pull_request_former_heads_pending
--                              the commits and the rows owed to an issue's candidates: a result's
--                              commit, or a row that starts an open lifecycle at the issue, which
--                              the triggers below append and the issue's settle drains. No key.
--   pull_request_former_heads_rows
--                              the fold's own state: for each open row it applied a record of, the
--                              head it stands at and the place at which it reached that head.
--   pull_request_former_heads_ended
--                              for each pull request, its lowercased canonical name and number, the
--                              place of the last close or merge of it under any of its names.
--   pull_request_former_heads_reads
--                              one row per pull request whose candidates a settle is reading
--                              GitHub about now: its canonical name, lowercased, and its number,
--                              which are the same under every issue it moves to and every name it
--                              is stored under, and until when the claim holds. The settle that
--                              holds it makes the one read; a settle of any other issue leaves
--                              the candidates to it (api/check_states.go, verifyFormerHeads). The
--                              settle deletes the row once it wrote the answer; one its process
--                              left on stopping is taken over once its time has passed.
--
-- pull_request_former_heads_keep() records each insert of an open row, each update that moves a
-- row's key, head or state while it is or becomes open, and each delete, by an insert, an update
-- and a delete trigger; the update trigger fires only where the key, head_sha or state moved,
-- since every writer of the row names each column in its `do update set` on each write, a
-- read-through included. So a head is recorded whether or not a settle ran while the pull request
-- was there, every writer is recorded, a write of the release before this one too, and a writer a
-- later change adds is recorded with no code of its own. The records of one row are in the order
-- of their places: a writer holds the row's lock from its write to its commit, so the next writer
-- of the row draws its place after that commit.
--
-- pull_request_former_heads_fold(pull request) applies a pull request's records, under its lock,
-- in the order of their places, before any read of its former heads: an open row that moves on
-- from a head keeps that head, verified, with the place at which it reached it; a close or a merge
-- under any name ends the pull request's lifecycle under every name, so it raises the pull
-- request's ended place and drops every row of history reached before it, under every name; and
-- a head reached before the last end is of a lifecycle that ended, so a move on from it keeps
-- nothing. The names of one pull request so share one lifecycle: a row stored under an old name
-- can stay open, unread, while the row under the canonical name closes and reopens, and the
-- history the old row kept and the head it stands at are of the lifecycle the close ended. A
-- record of another name can commit after a record of a later place was applied; whatever the
-- order, a row of history stays only where its place is after the pull request's last end, so the
-- fold answers as it would have, had every record committed in the order of its places.
--
-- A delete of an open row whose repository the canonical rule names otherwise (the expression of
-- refs.CanonicalRepoSQL, over repo_projects.renamed_to) is the re-key of a pull request stored
-- under a name its repository was renamed away from, whichever build deletes it
-- (delivery.upsertHeadUnderCanonicalName, and the release before this one, which carries nothing
-- itself): the trigger reads the canonical row, and where it is open records that row and its
-- head with the delete, and marks that row's issue owed (check_states_owe), since the canonical
-- row may be written with the head, state and issue it already holds, which marks nothing, and the
-- deleted row may have no issue. The fold then carries the deleted row's history, and its own
-- head, each where it is of the lifecycle that has not ended, to the canonical row, but the
-- canonical row's head. Any other delete, a close, a merge and an update that keys the row anew
-- drop the row's own history.
--
-- A head an open pull request left before this release is in no record. Such a head needs a state
-- only where someone recorded a result at it that the settle of the pull request's issue reads: a
-- result recorded, on any issue, against a check of that issue's Verification document
-- (api/results_rows.go, loadCheckResults, which the settle's computation reads the results by), in
-- check_results or ci_check_results. So the install seeds, for each open pull request attributed
-- to an issue, each distinct commit of such a result other than the pull request's head, as a
-- candidate: verified false, since a result there does not show that the commit was ever this
-- pull request's. The Verification document is the issue's document of that role, by the
-- predicate the server reads it by (api/artifacts.go, documentRolePredicate), which
-- check_states_owe_check_block (20261121000327) states too. The settler asks GitHub once which of
-- each such pull request's candidates are its own commits and keeps those (api/check_states.go,
-- verifyFormerHeads), and each issue holding a candidate is marked owed here, so that read comes
-- at the settler's first pass. A head left before this release with no result at it is not known,
-- and needs no state.
--
-- The same holds for a result recorded after this release at such a head, and for a head an open
-- pull request left while Dispatch held no open row of it at the issue: no record shows the pull
-- request leaving it, and the pull request now names another head. Dispatch's intake of GitHub's
-- webhooks can be off, and then the read-through of a result's commit writes a pull request's row
-- at GitHub's head after the result's own transaction, so a pull request first written at its
-- issue after a result at a head it already left is the common case, not a rare one. So a commit
-- of the population above, the results of an issue's Verification document, is a candidate of
-- each open pull request of that issue at another commit whenever either side is written:
--   - pull_request_former_heads_of_result(), an insert trigger on each result table, appends the
--     result's commit as pending for the issue whose settle reads the result
--     (pull_request_former_heads_reader);
--   - pull_request_former_heads_begin(), an insert and an update trigger on pull_request_heads,
--     appends as pending a row that starts an open lifecycle at an issue: an open row inserted, a
--     row reopened, a row keyed anew, and an open row that moves to another issue; it owes the
--     issue a settle;
--   - the settle of the issue first drains what is pending (pull_request_former_heads_drain): for
--     each open row of the issue it folds that pull request's records, and then leaves each pending
--     result's commit, and the issue's whole population (pull_request_former_heads_population) for
--     a row pending as started;
--   - the install leaves the population for each open row, by the same function the drain calls.
-- Each leaves its candidates by pull_request_former_heads_leave(), unverified, where the pull
-- request holds no row of the commit yet; the settler confirms them as it confirms the install's.
-- A head an open pull request left since this release has its row once folded, verified, so a
-- result there leaves nothing to read. They draw from one population through those functions, so
-- they cannot drift apart. A result and a row's start written at once in two transactions are
-- never both missed: the settles of an issue run under its lease, one at a time, and each pending
-- row is drained by a settle that starts after its writer committed, which owes it; of the two
-- drains, the later starts after both writers committed and reads both.
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
-- result tables and set pull_request_heads.issue_key to null, in an order of their own; an install
-- that held one lock while it waited for the next could close a cycle with such a delete, and
-- Postgres's deadlock check, which runs deadlock_timeout (1 s by default) into a wait, would then
-- refuse one of them: the delete, answered 500, or the install, whose 40P01 the runner does not
-- try again, failing the boot. So the install asks for the three in one statement, in one order,
-- waiting at most half a second (lock_timeout, shorter than deadlock_timeout), inside a block of
-- its own: a try that runs out gives back what it took, so a delete that waits for it goes on, and
-- the install asks again at once. It asks for as long as the runner lets any migration wait for a
-- lock (pgmigrate.LockTimeout, 5 s), and past that fails with 55P03, having applied nothing, and
-- the runner applies it again after a pause, up to its attempts (store/store.go applyMigration,
-- pgmigrate.RetryLockTimeouts). So a writer that holds one of the three for a while is waited out
-- as any migration waits one out, and a cycle with a delete costs the install half a second and
-- fails neither side. The seed reads the open rows and the result tables, and writes only the new
-- tables and check_states_owed. The census answers 0: nothing existing is refused or rewritten.
set local lock_timeout = '500ms';

create sequence pull_request_former_heads_order;

create table pull_request_former_heads (
    repo text not null,
    number integer not null,
    head_sha text not null,
    left_at timestamptz not null default now(),
    verified boolean not null default true,
    since bigint default nextval('pull_request_former_heads_order'),
    primary key (repo, number, head_sha)
);

create table pull_request_former_heads_moves (
    seq bigint not null default nextval('pull_request_former_heads_order'),
    moved_at timestamptz not null default now(),
    pull_repo text not null,
    number integer not null,
    op text not null,
    old_repo text,
    old_head text,
    old_open boolean,
    new_repo text,
    new_head text,
    new_open boolean,
    carry_repo text,
    carry_head text
);
create index pull_request_former_heads_moves_pull on pull_request_former_heads_moves (pull_repo, number, seq);

create table pull_request_former_heads_pending (
    issue_key text not null,
    repo text,
    number integer,
    commit_sha text
);
create index pull_request_former_heads_pending_issue on pull_request_former_heads_pending (issue_key);

create table pull_request_former_heads_rows (
    repo text not null,
    number integer not null,
    head_sha text not null,
    since bigint not null,
    primary key (repo, number)
);

create table pull_request_former_heads_ended (
    repo text not null,
    number integer not null,
    seq bigint not null,
    primary key (repo, number)
);

create table pull_request_former_heads_reads (
    repo text not null,
    number integer not null,
    until timestamptz not null,
    primary key (repo, number)
);

-- The canonical name of a repository's name: the expression of refs.CanonicalRepoSQL, over
-- repo_projects.renamed_to.
create function pull_request_canonical_repo(text) returns text language sql stable as $$
  select coalesce((select max(renamed.renamed_to) from repo_projects renamed
    where renamed.renamed_to is not null and lower(renamed.repo) = lower($1)), $1)
$$;

-- The name a pull request's records and lock go by under all its names: the canonical name of the
-- repository name given, lowercased.
create function pull_request_former_heads_key(text) returns text language sql stable as $$
  select lower(pull_request_canonical_repo($1))
$$;

-- The lock of a pull request, a name of its repository and its number, under all its names, to
-- the end of the transaction: the settler reads and writes its history under it (above).
create function pull_request_former_heads_lock(text, integer) returns void language sql as $$
  select pg_advisory_xact_lock(hashtext('pull-request-former-heads'),
    hashtext(pull_request_former_heads_key($1) || '#' || $2))
$$;

-- A write of pull_request_heads that can move a pull request's history (its triggers' when says
-- which): one record of it, with the row before and after, and for the re-key of an open row the
-- canonical row it read open (above). It reads and appends, and marks the canonical row's issue
-- owed where it read one: it waits for no lock.
create function pull_request_former_heads_keep() returns trigger language plpgsql as $$
declare
  target record;
  carried boolean := false;
begin
  if tg_op = 'DELETE' then
    if old.state = 'open' and pull_request_canonical_repo(old.repo) <> old.repo then
      select head.repo, head.head_sha, head.issue_key into target from pull_request_heads head
      where head.repo = pull_request_canonical_repo(old.repo) and head.number = old.number and head.state = 'open';
      carried := found;
      if carried then
        perform check_states_owe(target.issue_key);
      end if;
    end if;
    insert into pull_request_former_heads_moves (pull_repo, number, op, old_repo, old_head, old_open, carry_repo, carry_head)
    values (pull_request_former_heads_key(old.repo), old.number, tg_op, old.repo, old.head_sha, old.state = 'open',
      case when carried then target.repo end, case when carried then target.head_sha end);
    return null;
  end if;
  if tg_op = 'INSERT' then
    insert into pull_request_former_heads_moves (pull_repo, number, op, new_repo, new_head, new_open)
    values (pull_request_former_heads_key(new.repo), new.number, tg_op, new.repo, new.head_sha, new.state = 'open');
    return null;
  end if;
  if old.state <> 'open' and new.state <> 'open' and (old.repo, old.number) = (new.repo, new.number) then
    return null;
  end if;
  insert into pull_request_former_heads_moves (pull_repo, number, op, old_repo, old_head, old_open, new_repo, new_head, new_open)
  values (pull_request_former_heads_key(old.repo), old.number, tg_op, old.repo, old.head_sha, old.state = 'open',
    new.repo, new.head_sha, new.state = 'open');
  return null;
end $$;

-- Applies the records of the pull request a lowercased canonical name and a number name, under its
-- lock, in the order of their places, and deletes them (above). Answers how many it applied.
create function pull_request_former_heads_fold(text, integer) returns integer language plpgsql as $$
declare
  taken pull_request_former_heads_moves[];
  seqs bigint[];
  fact pull_request_former_heads_moves;
  ended bigint;
  entered bigint;
begin
  perform pg_advisory_xact_lock(hashtext('pull-request-former-heads'), hashtext($1 || '#' || $2));
  select coalesce(array_agg(applied order by applied.seq), '{}'), coalesce(array_agg(applied.seq), '{}')
  into taken, seqs
  from pull_request_former_heads_moves applied where applied.pull_repo = $1 and applied.number = $2;
  if cardinality(taken) = 0 then
    return 0;
  end if;
  delete from pull_request_former_heads_moves where pull_repo = $1 and number = $2 and seq = any(seqs);
  select closed.seq into ended from pull_request_former_heads_ended closed where closed.repo = $1 and closed.number = $2;
  foreach fact in array taken loop
    -- The place at which the row reached the head it leaves, null where no applied record shows it.
    select reached.since into entered from pull_request_former_heads_rows reached
    where reached.repo = fact.old_repo and reached.number = fact.number and reached.head_sha = fact.old_head;
    if fact.op = 'UPDATE' and fact.old_repo = fact.new_repo and fact.old_open and fact.new_open then
      if fact.old_head <> fact.new_head and (ended is null or entered > ended) then
        insert into pull_request_former_heads (repo, number, head_sha, left_at, verified, since)
        values (fact.old_repo, fact.number, fact.old_head, fact.moved_at, true, entered)
        on conflict (repo, number, head_sha) do update set left_at = excluded.left_at, verified = true, since = excluded.since;
      end if;
    elsif fact.op = 'UPDATE' and fact.old_repo = fact.new_repo and fact.old_open then
      ended := greatest(ended, fact.seq);
      delete from pull_request_former_heads former
      where former.number = fact.number and (former.repo = fact.old_repo
        or (pull_request_former_heads_key(former.repo) = $1 and (former.since is null or former.since < ended)));
    elsif fact.op = 'DELETE' and fact.old_open and fact.carry_repo is not null then
      -- A pull request back at a head it left holds that head twice here, once as left.
      insert into pull_request_former_heads (repo, number, head_sha, left_at, verified, since)
      select fact.carry_repo, fact.number, kept.head_sha, max(kept.left_at), bool_or(kept.verified), max(kept.since)
      from (
        select former.head_sha, former.left_at, former.verified, former.since from pull_request_former_heads former
        where former.repo = fact.old_repo and former.number = fact.number
        union all
        select fact.old_head, fact.moved_at, true, entered where ended is null or entered > ended
      ) kept
      where kept.head_sha <> fact.carry_head
      group by kept.head_sha
      on conflict (repo, number, head_sha) do update set
        left_at = greatest(pull_request_former_heads.left_at, excluded.left_at),
        verified = pull_request_former_heads.verified or excluded.verified,
        since = greatest(pull_request_former_heads.since, excluded.since);
    end if;
    if fact.op = 'DELETE' or fact.old_repo <> fact.new_repo or (fact.old_open and not fact.new_open) then
      delete from pull_request_former_heads where repo = fact.old_repo and number = fact.number;
      delete from pull_request_former_heads_rows where repo = fact.old_repo and number = fact.number;
    end if;
    if fact.new_open then
      insert into pull_request_former_heads_rows (repo, number, head_sha, since)
      values (fact.new_repo, fact.number, fact.new_head, fact.seq)
      on conflict (repo, number) do update set head_sha = excluded.head_sha, since = excluded.since;
    end if;
  end loop;
  if ended is not null then
    insert into pull_request_former_heads_ended (repo, number, seq) values ($1, $2, ended)
    on conflict (repo, number) do update set seq = excluded.seq;
  end if;
  return cardinality(taken);
end $$;

-- The issue whose settle reads the results recorded against a check of a document: the issue
-- the document is the Verification document of, by the role predicate the server reads it by
-- (above), and null for any other document. The one statement of that rule here: the population
-- below and the result's trigger both read it.
create function pull_request_former_heads_reader(uuid) returns text language sql stable as $$
  select document.issue_key from artifacts document
  where document.id = $1
    and (document.role = 'verification' or (document.role is null and document.issue_key is not null and document.slug = 'verification-md'))
$$;

-- The population of an issue's candidates: each distinct commit of a result, a role's or a CI
-- job's, whose document's results the settle of the issue reads (pull_request_former_heads_reader),
-- whichever issue recorded it.
create function pull_request_former_heads_population(text) returns setof text language sql stable as $$
  select result.commit_sha from artifacts document
  join check_results result on result.check_artifact_id = document.id
  where document.issue_key = $1 and pull_request_former_heads_reader(document.id) = $1
  union
  select result.commit_sha from artifacts document
  join ci_check_results result on result.check_artifact_id = document.id
  where document.issue_key = $1 and pull_request_former_heads_reader(document.id) = $1
$$;

-- Leaves each of the commits given as a candidate of the row (repo, number) where that row is
-- open at the issue given and at another commit, unverified, where the pull request holds no row
-- of the commit yet; answers whether it left one. It takes no lock itself: the install holds the
-- tables, and the drain takes the pull request's lock first.
create function pull_request_former_heads_leave(text, integer, text, text[]) returns boolean language plpgsql as $$
begin
  insert into pull_request_former_heads (repo, number, head_sha, verified)
  select head.repo, head.number, candidate.commit_sha, false
  from pull_request_heads head cross join unnest($4) as candidate (commit_sha)
  where head.repo = $1 and head.number = $2 and head.issue_key = $3 and head.state = 'open'
    and candidate.commit_sha <> head.head_sha
  on conflict (repo, number, head_sha) do nothing;
  return found;
end $$;

-- A result recorded, a role's or a CI job's, whose settle an issue reads: its commit is pending
-- for that issue, which the settle drains (pull_request_former_heads_drain). It reads and appends:
-- it waits for no lock.
create function pull_request_former_heads_of_result() returns trigger language plpgsql as $$
declare
  owner text := pull_request_former_heads_reader(new.check_artifact_id);
begin
  if owner is not null then
    insert into pull_request_former_heads_pending (issue_key, commit_sha) values (owner, new.commit_sha);
    if owner is distinct from new.check_issue_key then
      perform check_states_owe(owner);
    end if;
  end if;
  return null;
end $$;

-- A row that starts an open lifecycle at an issue (its triggers' when says which): the row is
-- pending for that issue, which the settle drains, and the issue is owed that settle. It appends:
-- it waits for no lock.
create function pull_request_former_heads_begin() returns trigger language plpgsql as $$
begin
  insert into pull_request_former_heads_pending (issue_key, repo, number) values (new.issue_key, new.repo, new.number);
  perform check_states_owe(new.issue_key);
  return null;
end $$;

-- What a settle of an issue runs before it reads former heads (api/check_states.go, settleRound):
-- it takes the issue's pending rows, and for each open row of the issue, in the order of the pull
-- requests' locks, folds that pull request's records and then leaves the commits of the pending
-- results, and the issue's whole population where that row is pending as started. Answers how many
-- pending rows it took.
create function pull_request_former_heads_drain(text) returns integer language plpgsql as $$
declare
  taken integer;
  results text[];
  starts text[];
  pull record;
begin
  with pending as (
    delete from pull_request_former_heads_pending where issue_key = $1 returning repo, number, commit_sha
  )
  select count(*), coalesce(array_agg(distinct commit_sha) filter (where commit_sha is not null), '{}'),
    coalesce(array_agg(distinct repo || '#' || number) filter (where commit_sha is null), '{}')
  into taken, results, starts from pending;
  for pull in
    select head.repo, head.number, pull_request_former_heads_key(head.repo) as pull_key from pull_request_heads head
    where head.issue_key = $1 and head.state = 'open'
    order by hashtext(pull_request_former_heads_key(head.repo) || '#' || head.number), head.repo, head.number
  loop
    perform pull_request_former_heads_fold(pull.pull_key, pull.number);
    if taken > 0 then
      perform pull_request_former_heads_leave(pull.repo, pull.number, $1, results ||
        case when pull.repo || '#' || pull.number = any(starts) then array(select pull_request_former_heads_population($1)) else '{}'::text[] end);
    end if;
  end loop;
  return taken;
end $$;

-- The three tables' locks, by tries of half a second each for at most five seconds (above): the
-- block's exception handler rolls a try that ran out back, which gives back the locks it took.
do $$
declare
  started timestamptz := clock_timestamp();
begin
  loop
    begin
      lock table pull_request_heads, check_results, ci_check_results in share row exclusive mode;
      exit;
    exception when lock_not_available then
      if clock_timestamp() - started >= interval '5 seconds' then
        raise;
      end if;
    end;
  end loop;
end $$;

create trigger pull_request_former_heads_begin_insert after insert on pull_request_heads
  for each row when (new.state = 'open' and new.issue_key is not null)
  execute function pull_request_former_heads_begin();
create trigger pull_request_former_heads_begin_update after update on pull_request_heads
  for each row when (new.state = 'open' and new.issue_key is not null and (old.state <> 'open'
    or (old.repo, old.number) is distinct from (new.repo, new.number) or old.issue_key is distinct from new.issue_key))
  execute function pull_request_former_heads_begin();
create trigger pull_request_former_heads_keep_insert after insert on pull_request_heads
  for each row when (new.state = 'open')
  execute function pull_request_former_heads_keep();
create trigger pull_request_former_heads_keep_update after update on pull_request_heads
  for each row when ((old.repo, old.number, old.head_sha, old.state) is distinct from (new.repo, new.number, new.head_sha, new.state))
  execute function pull_request_former_heads_keep();
create trigger pull_request_former_heads_keep_delete after delete on pull_request_heads
  for each row execute function pull_request_former_heads_keep();
create trigger pull_request_former_heads_of_check_results after insert on check_results
  for each row execute function pull_request_former_heads_of_result();
create trigger pull_request_former_heads_of_ci_check_results after insert on ci_check_results
  for each row execute function pull_request_former_heads_of_result();

-- The seed: each open row attributed to an issue starts with the issue's population, by the
-- function the drain of a row's start calls, and each issue holding a candidate is owed.
select pull_request_former_heads_leave(head.repo, head.number, head.issue_key,
  array(select pull_request_former_heads_population(head.issue_key)))
from pull_request_heads head
where head.state = 'open' and head.issue_key is not null;

select check_states_owe(owed.issue_key)
from (
  select distinct head.issue_key from pull_request_heads head
  join pull_request_former_heads former on former.repo = head.repo and former.number = head.number
  where head.state = 'open' and not former.verified
) owed;
