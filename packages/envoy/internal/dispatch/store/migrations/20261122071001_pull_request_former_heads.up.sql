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
-- (pull_request_former_heads_drain), and under the pull request's lock
-- (pull_request_former_heads_lock), which no writer takes.
-- So these triggers add no lock to a writer's transaction, and none can close a cycle with the
-- locks the writers take themselves, each in its own order: a result's write takes the owner locks
-- and then inserts (api/results.go, recordResultUnderOwnerLocks), a head's write takes its row and
-- then the owner locks (delivery.UpsertPullRequestHead, appendAttributionChange), a change of an
-- issue's links takes the owner lock and then the rows (delivery.LinksChanged), and the re-key's
-- delete takes the old row alone. A trigger that took a lock one writer holds while another waits
-- for it in the other order would close such a cycle, which Postgres breaks by failing one writer.
--
-- One pull request has one identity, a number Dispatch draws for it (pull), under every name its
-- repository goes by: a name it is stored under, or a name its repository was renamed away from
-- (repo_projects.renamed_to, the rule of refs.CanonicalRepoSQL). Its history, its records, its
-- lifecycle, its lock and the claim of its read all go by that identity; a name chooses only the
-- GitHub route a read takes and the repository an event names. A writer appends its record under
-- the name it wrote, whatever Dispatch knows of renames then, and the settler finds the record's
-- identity when it applies it, so a record appended before Dispatch learned of a rename is of the
-- same pull request as every record after.
--
--   pull_request_former_heads_pulls
--                              one row per pull request: its identity, its number, and ended, the
--                              place in pull_request_former_heads_order of the last close or merge
--                              of it under any of its names, null where none is known.
--   pull_request_former_heads_aliases
--                              each name (lowercased) and number the settler has found a pull
--                              request under, and its identity. A name a rename maps to another
--                              name of a known pull request is that pull request: the settler adds
--                              it to the pull request's names the first time it meets it
--                              (pull_request_former_heads_resolve), and a name never leaves.
--   pull_request_former_heads  one row per pull request (its identity) and head it left while
--                              open, with when it last left that head and since, the place at
--                              which the pull request reached that head, or at which the row was
--                              made, null for a head reached before this release. verified is false
--                              on a candidate, a commit of the results of the pull request's issue
--                              (below), which no read of the pull request's commits has confirmed
--                              yet: the settler computes no state there until one does. No foreign
--                              key, and only the settler writes it.
--   pull_request_former_heads_moves
--                              the writers' record: one row for each write of pull_request_heads
--                              that can move a pull request's history, with its place in
--                              pull_request_former_heads_order, the name and number of the row
--                              written, and its head and state before and after, which
--                              pull_request_former_heads_keep() appends in the writer's own
--                              transaction, whichever build wrote the row. No key: an insert waits
--                              for nothing. The settler deletes each row it applied.
--   pull_request_former_heads_pending
--                              the commits and the rows owed to an issue's candidates: a result's
--                              commit, or a row that starts an open lifecycle at the issue, which
--                              the triggers below append and the issue's settle drains. No key.
--   pull_request_former_heads_rows
--                              the settler's own state: for each open row of a pull request it
--                              applied a record of, by its name, the head it stands at and the
--                              place at which it reached that head.
--   pull_request_former_heads_reads
--                              one row per pull request whose candidates a settle is reading
--                              GitHub about now, by its identity, which is the same under every
--                              issue it moves to and every name it is stored under, and until when
--                              the claim holds. The settle that holds it makes the one read; a
--                              settle of any other issue leaves the candidates to it
--                              (api/check_states.go, verifyFormerHeads). The settle deletes the row
--                              once it wrote the answer; one its process left on stopping is taken
--                              over once its time has passed.
--
-- pull_request_former_heads_keep() records each insert of an open row, each update that moves a
-- row's key, head or state while it is or becomes open, and each delete, by an insert, an update
-- and a delete trigger, each record under the name of the row it is of: an update that keys the
-- row anew is the delete of the row under its old name and the insert of the row under its new
-- one. The update trigger fires only where the key, head_sha or state moved, since every writer of
-- the row names each column in its `do update set` on each write, a read-through included. So a
-- head is recorded whether or not a settle ran while the pull request was there, every writer is
-- recorded, a write of the release before this one too, and a writer a later change adds is
-- recorded with no code of its own. The records of one row are in the order of their places: a
-- writer holds the row's lock from its write to its commit, so the next writer of the row draws
-- its place after that commit.
--
-- pull_request_former_heads_apply(pull request) applies a pull request's records, the records of
-- every name it goes by, under its lock, in the order of their places, before any read of its
-- former heads: an open row that moves on from a head keeps that head, verified, with the place
-- at which it reached it; an open row that goes, as the re-key's delete of the row stored under a
-- name the repository was renamed away from, keeps the head it stood at the same way, since the
-- pull request it stood for is the one its other name stands for; a close or a merge under any
-- name ends the pull request's lifecycle, so it raises the pull request's ended place and drops
-- every row of history reached before it; and a head reached before the last end is of a
-- lifecycle that ended, so a move on from it keeps nothing. A row stored under an old name can stay
-- open, unread, while the row under the canonical name closes and reopens, and the head it stands
-- at is of the lifecycle the close ended. A record of another name can commit after a record of a
-- later place was applied; whatever the order, a row of history stays only where its place is
-- after the pull request's last end, so the settler answers as it would have, had every record
-- committed in the order of its places.
--
-- pull_request_former_heads_resolve(name, number) is the identity of a pull request a name and a
-- number stand for today: the identity of that name where the settler met it before, else the
-- identity of any name of the same number that the rename rule makes the same repository, else a
-- new one. Where two identities turn out to be one pull request, because each name's records were
-- applied before Dispatch learned the rename that joins them, it joins them into the older:
-- pull_request_former_heads_join moves the younger's names, history and rows to the older, keeps
-- the later of their ends, and drops whatever that end ended. Creating and joining identities runs
-- under a lock of its own (the pull request lock's namespace and key 0, which no identity holds),
-- taken before any pull request's lock, and a settle takes pull requests' locks in the order of
-- their identities, so no two settles wait for each other in a cycle.
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
--     each pull request an open row of the issue stands for it applies that pull request's
--     records, and then leaves each pending result's commit, and the issue's whole population
--     (pull_request_former_heads_population) for a row pending as started;
--   - the install leaves the population for each open row, by the same function the drain calls.
-- Each leaves its candidates by pull_request_former_heads_leave(), unverified, where the pull
-- request holds no row of the commit yet; the settler confirms them as it confirms the install's.
-- A head an open pull request left since this release has its row once applied, verified, so a
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
create sequence pull_request_former_heads_pull_ids as integer;

create table pull_request_former_heads_pulls (
    pull integer primary key default nextval('pull_request_former_heads_pull_ids'),
    number integer not null,
    ended bigint
);

create table pull_request_former_heads_aliases (
    repo text not null,
    number integer not null,
    pull integer not null,
    primary key (repo, number)
);
create index pull_request_former_heads_aliases_pull on pull_request_former_heads_aliases (pull);

create table pull_request_former_heads (
    pull integer not null,
    head_sha text not null,
    left_at timestamptz not null default now(),
    verified boolean not null default true,
    since bigint default nextval('pull_request_former_heads_order'),
    primary key (pull, head_sha)
);

create table pull_request_former_heads_moves (
    seq bigint not null default nextval('pull_request_former_heads_order'),
    moved_at timestamptz not null default now(),
    repo text not null,
    number integer not null,
    op text not null,
    old_head text,
    old_open boolean,
    new_head text,
    new_open boolean
);
create index pull_request_former_heads_moves_name on pull_request_former_heads_moves (number, repo, seq);

create table pull_request_former_heads_pending (
    issue_key text not null,
    repo text,
    number integer,
    commit_sha text
);
create index pull_request_former_heads_pending_issue on pull_request_former_heads_pending (issue_key);

create table pull_request_former_heads_rows (
    pull integer not null,
    repo text not null,
    head_sha text not null,
    since bigint not null,
    primary key (pull, repo)
);

create table pull_request_former_heads_reads (
    pull integer primary key,
    until timestamptz not null
);

-- The canonical name of a repository's name: the expression of refs.CanonicalRepoSQL, over
-- repo_projects.renamed_to.
create function pull_request_canonical_repo(text) returns text language sql stable as $$
  select coalesce((select max(renamed.renamed_to) from repo_projects renamed
    where renamed.renamed_to is not null and lower(renamed.repo) = lower($1)), $1)
$$;

-- The lock of a pull request's identity, to the end of the transaction, under which the settler
-- reads and writes its history (above). Every holder first takes the registry's lock, key 0 of
-- the namespace, which no identity holds, shared: pull_request_former_heads_resolve takes it alone
-- to make or join identities, so no identity is made or joined while a transaction holds the lock
-- of one, and no transaction that holds the lock of one waits for the registry.
create function pull_request_former_heads_lock(integer) returns void language sql as $$
  select pg_advisory_xact_lock_shared(hashtext('pull-request-former-heads'), 0);
  select pg_advisory_xact_lock(hashtext('pull-request-former-heads'), $1)
$$;

-- The identities of the names the settler has met that stand for the pull request a name and a
-- number stand for today: that name's, and each of a name of the same number the rename rule
-- makes the same repository, least first. It reads only.
create function pull_request_former_heads_named(text, integer) returns integer[] language sql stable as $$
  select coalesce(array_agg(distinct alias.pull order by alias.pull), '{}') from pull_request_former_heads_aliases alias
  where alias.number = $2 and (alias.repo = lower($1)
    or lower(pull_request_canonical_repo(alias.repo)) = lower(pull_request_canonical_repo($1)))
$$;

-- The identity a name and a number stand for today, null where the settler has met none of its
-- names: the least of pull_request_former_heads_named. It reads only, so a read of former heads
-- calls it; the settler first finds each name it applies (pull_request_former_heads_resolve).
create function pull_request_former_heads_pull_of(text, integer) returns integer language sql stable as $$
  select (pull_request_former_heads_named($1, $2))[1]
$$;

-- Joins identity $2 into $1: $2's names, rows of history and rows of the settler's state become
-- $1's, the later of their ends stands, and every row of history reached before it goes. Called
-- by pull_request_former_heads_resolve alone, which holds the registry's lock alone, so no other
-- transaction holds the lock of either identity.
create function pull_request_former_heads_join(integer, integer) returns void language plpgsql as $$
declare
  last_end bigint;
begin
  update pull_request_former_heads_aliases set pull = $1 where pull = $2;
  insert into pull_request_former_heads (pull, head_sha, left_at, verified, since)
  select $1, former.head_sha, former.left_at, former.verified, former.since
  from pull_request_former_heads former where former.pull = $2
  on conflict (pull, head_sha) do update set
    left_at = greatest(pull_request_former_heads.left_at, excluded.left_at),
    verified = pull_request_former_heads.verified or excluded.verified,
    since = greatest(pull_request_former_heads.since, excluded.since);
  delete from pull_request_former_heads where pull = $2;
  update pull_request_former_heads_rows set pull = $1 where pull = $2;
  delete from pull_request_former_heads_reads where pull = $2;
  select greatest(kept.ended, joined.ended) into last_end
  from pull_request_former_heads_pulls kept, pull_request_former_heads_pulls joined
  where kept.pull = $1 and joined.pull = $2;
  delete from pull_request_former_heads_pulls where pull = $2;
  update pull_request_former_heads_pulls set ended = last_end where pull = $1;
  if last_end is not null then
    delete from pull_request_former_heads where pull = $1 and (since is null or since < last_end);
  end if;
end $$;

-- The identity of the pull request a name and a number stand for today (above), which it makes
-- where the settler has met none of its names, joins where it has met two, and adds the name to.
-- Where it must write, it does so under the registry's lock alone, read again under it; its
-- caller holds no pull request's lock yet, nor the registry's shared.
create function pull_request_former_heads_resolve(text, integer) returns integer language plpgsql as $$
declare
  known integer[] := pull_request_former_heads_named($1, $2);
  kept integer;
  other integer;
begin
  if cardinality(known) = 1 and exists (
    select 1 from pull_request_former_heads_aliases alias where alias.repo = lower($1) and alias.number = $2
  ) then
    return known[1];
  end if;
  perform pg_advisory_xact_lock(hashtext('pull-request-former-heads'), 0);
  known := pull_request_former_heads_named($1, $2);
  if cardinality(known) = 0 then
    insert into pull_request_former_heads_pulls (number) values ($2) returning pull into kept;
  else
    kept := known[1];
    foreach other in array known[2:] loop
      perform pull_request_former_heads_join(kept, other);
    end loop;
  end if;
  insert into pull_request_former_heads_aliases (repo, number, pull) values (lower($1), $2, kept)
  on conflict (repo, number) do nothing;
  return kept;
end $$;

-- A write of pull_request_heads that can move a pull request's history (its triggers' when says
-- which): one record of it under the name of the row written, with its head and state before and
-- after; an update that keys the row anew is two, the delete of the row under its old name and the
-- insert of it under its new one. The delete of an open row whose repository the rename rule
-- names otherwise, the re-key, marks owed the issue of the open row of the canonical name, which
-- may be written with the head, state and issue it already holds and so mark nothing, so the
-- settle that publishes the head the delete keeps comes soon. It reads and appends, and marks an
-- issue owed: it waits for no lock.
create function pull_request_former_heads_keep() returns trigger language plpgsql as $$
declare
  canonical text;
begin
  if tg_op = 'DELETE' or (tg_op = 'UPDATE' and (old.repo, old.number) is distinct from (new.repo, new.number)) then
    if old.state = 'open' and pull_request_canonical_repo(old.repo) <> old.repo then
      select head.issue_key into canonical from pull_request_heads head
      where head.repo = pull_request_canonical_repo(old.repo) and head.number = old.number and head.state = 'open';
      if canonical is not null then
        perform check_states_owe(canonical);
      end if;
    end if;
    insert into pull_request_former_heads_moves (repo, number, op, old_head, old_open)
    values (lower(old.repo), old.number, 'DELETE', old.head_sha, old.state = 'open');
    if tg_op = 'UPDATE' and new.state = 'open' then
      insert into pull_request_former_heads_moves (repo, number, op, new_head, new_open)
      values (lower(new.repo), new.number, 'INSERT', new.head_sha, true);
    end if;
    return null;
  end if;
  if tg_op = 'INSERT' then
    insert into pull_request_former_heads_moves (repo, number, op, new_head, new_open)
    values (lower(new.repo), new.number, 'INSERT', new.head_sha, new.state = 'open');
    return null;
  end if;
  if old.state <> 'open' and new.state <> 'open' then
    return null;
  end if;
  insert into pull_request_former_heads_moves (repo, number, op, old_head, old_open, new_head, new_open)
  values (lower(old.repo), old.number, 'UPDATE', old.head_sha, old.state = 'open', new.head_sha, new.state = 'open');
  return null;
end $$;

-- Applies the records of the pull request of an identity, the records of each of its names, in the
-- order of their places, and deletes them (above); the caller holds its lock. Answers how many it
-- applied.
create function pull_request_former_heads_apply(integer) returns integer language plpgsql as $$
declare
  taken pull_request_former_heads_moves[];
  seqs bigint[];
  fact pull_request_former_heads_moves;
  last_end bigint;
  entered bigint;
begin
  select coalesce(array_agg(applied order by applied.seq), '{}'), coalesce(array_agg(applied.seq), '{}')
  into taken, seqs
  from pull_request_former_heads_moves applied
  join pull_request_former_heads_aliases alias on alias.repo = applied.repo and alias.number = applied.number
  where alias.pull = $1;
  if cardinality(taken) = 0 then
    return 0;
  end if;
  delete from pull_request_former_heads_moves where seq = any(seqs);
  select pulls.ended into last_end from pull_request_former_heads_pulls pulls where pulls.pull = $1;
  foreach fact in array taken loop
    -- The place at which the row reached the head it leaves, null where no applied record shows it.
    entered := null;
    select reached.since into entered from pull_request_former_heads_rows reached
    where reached.pull = $1 and reached.repo = fact.repo and reached.head_sha = fact.old_head;
    if fact.old_open and (fact.op = 'DELETE' or (fact.new_open and fact.old_head <> fact.new_head)) then
      -- An open row that moves on from a head, or goes, keeps the head where it was reached in the
      -- lifecycle that has not ended.
      if last_end is null or entered > last_end then
        insert into pull_request_former_heads (pull, head_sha, left_at, verified, since)
        values ($1, fact.old_head, fact.moved_at, true, entered)
        on conflict (pull, head_sha) do update set left_at = greatest(pull_request_former_heads.left_at, excluded.left_at),
          verified = true, since = greatest(pull_request_former_heads.since, excluded.since);
      end if;
    elsif fact.op = 'UPDATE' and fact.old_open and not fact.new_open then
      last_end := greatest(last_end, fact.seq);
      delete from pull_request_former_heads former where former.pull = $1 and (former.since is null or former.since < last_end);
    end if;
    if fact.op = 'DELETE' or not fact.new_open then
      delete from pull_request_former_heads_rows where pull = $1 and repo = fact.repo;
    elsif fact.op = 'INSERT' or not fact.old_open or fact.old_head <> fact.new_head then
      insert into pull_request_former_heads_rows (pull, repo, head_sha, since) values ($1, fact.repo, fact.new_head, fact.seq)
      on conflict (pull, repo) do update set head_sha = excluded.head_sha, since = excluded.since;
    end if;
  end loop;
  update pull_request_former_heads_pulls set ended = last_end where pull = $1;
  return cardinality(taken);
end $$;

-- Applies the records of the pull request a name and a number stand for, and finds first the
-- identity of every name a record of that number was written under, so a record written under a
-- name before Dispatch learned of a rename is applied with the rest (above). It reads the identity
-- again once it holds the registry's lock shared, when no identity is joined any more. Answers
-- how many it applied.
create function pull_request_former_heads_fold(text, integer) returns integer language plpgsql as $$
declare
  named text;
  pull integer;
begin
  for named in select distinct moves.repo from pull_request_former_heads_moves moves where moves.number = $2 loop
    perform pull_request_former_heads_resolve(named, $2);
  end loop;
  perform pull_request_former_heads_resolve($1, $2);
  perform pg_advisory_xact_lock_shared(hashtext('pull-request-former-heads'), 0);
  pull := pull_request_former_heads_pull_of($1, $2);
  perform pull_request_former_heads_lock(pull);
  return pull_request_former_heads_apply(pull);
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

-- Leaves each of the commits given as a candidate of the pull request of an identity, where a row
-- of it is open at the issue given and no open row of it stands at the commit, unverified, where it
-- holds no row of the commit yet; answers whether it left one. It takes no lock itself: the
-- install holds the tables, and the drain takes the pull request's lock first.
create function pull_request_former_heads_leave(integer, text, text[]) returns boolean language plpgsql as $$
begin
  insert into pull_request_former_heads (pull, head_sha, verified)
  select $1, candidate.commit_sha, false
  from unnest($3) as candidate (commit_sha)
  where exists (
      select 1 from pull_request_heads head
      join pull_request_former_heads_aliases alias on alias.repo = lower(head.repo) and alias.number = head.number
      where alias.pull = $1 and head.issue_key = $2 and head.state = 'open')
    and not exists (
      select 1 from pull_request_heads head
      join pull_request_former_heads_aliases alias on alias.repo = lower(head.repo) and alias.number = head.number
      where alias.pull = $1 and head.state = 'open' and head.head_sha = candidate.commit_sha)
  on conflict (pull, head_sha) do nothing;
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
-- it takes the issue's pending rows; finds the identity of each pull request an open row of the
-- issue stands for, and of every name a record of their numbers was written under, before it
-- takes any lock of a pull request; then, holding the registry's lock shared, so no identity is
-- joined any more, reads those identities again and, for each in the order of the identities,
-- under its lock, applies its records and leaves the commits of the pending results, and the
-- issue's whole population where a row of it is pending as started. Answers how many pending rows
-- it took.
create function pull_request_former_heads_drain(text) returns integer language plpgsql as $$
declare
  taken integer;
  results text[];
  starts text[];
  head record;
  named text;
  held integer;
begin
  with pending as (
    delete from pull_request_former_heads_pending where issue_key = $1 returning repo, number, commit_sha
  )
  select count(*), coalesce(array_agg(distinct commit_sha) filter (where commit_sha is not null), '{}'),
    coalesce(array_agg(distinct lower(repo) || '#' || number) filter (where commit_sha is null), '{}')
  into taken, results, starts from pending;
  for head in
    select distinct stored.repo, stored.number from pull_request_heads stored where stored.issue_key = $1 and stored.state = 'open'
  loop
    for named in select distinct moves.repo from pull_request_former_heads_moves moves where moves.number = head.number loop
      perform pull_request_former_heads_resolve(named, head.number);
    end loop;
    perform pull_request_former_heads_resolve(head.repo, head.number);
  end loop;
  perform pg_advisory_xact_lock_shared(hashtext('pull-request-former-heads'), 0);
  for held in
    select distinct pull_request_former_heads_pull_of(stored.repo, stored.number) from pull_request_heads stored
    where stored.issue_key = $1 and stored.state = 'open' order by 1
  loop
    perform pull_request_former_heads_lock(held);
    perform pull_request_former_heads_apply(held);
    if taken > 0 then
      perform pull_request_former_heads_leave(held, $1, results ||
        case when exists (
          select 1 from pull_request_former_heads_aliases alias where alias.pull = held and alias.repo || '#' || alias.number = any(starts)
        ) then array(select pull_request_former_heads_population($1)) else '{}'::text[] end);
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
select pull_request_former_heads_leave(pull_request_former_heads_resolve(head.repo, head.number), head.issue_key,
  array(select pull_request_former_heads_population(head.issue_key)))
from pull_request_heads head
where head.state = 'open' and head.issue_key is not null;

select check_states_owe(owed.issue_key)
from (
  select distinct head.issue_key from pull_request_heads head
  join pull_request_former_heads_aliases alias on alias.repo = lower(head.repo) and alias.number = head.number
  join pull_request_former_heads former on former.pull = alias.pull
  where head.state = 'open' and not former.verified
) owed;
