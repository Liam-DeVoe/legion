-- 20261123213816_verdicts.up.sql — the records of the daemon's writes of the required check
-- legion-verdict: the pull requests of a verdict repository that no Legion issue owns, with their
-- heads; the reviews of a verdict repository's pull requests; the reviewer session a controller
-- names for such a pull request at a head; the verdicts reviewers give on a head; and each check
-- run and commit status named legion-verdict that the daemon writes. store/verdicts.go reads and
-- writes each table. And the head a reviewer's launch gave it, on the claim's row. This is the one
-- migration of this release (B).
--
-- outside_pull_requests records each sighting of such a pull request at a head, from webhook
-- events and the writers' sweep, with GitHub's updated_at of the pull request the sighting carried
-- (null where it carried none), by which the newest sighting is the current head. pull_requests
-- cannot hold it, because its primary key is the Legion issue that owns the pull request
-- (0004_record). verdict_reviews records each review of a verdict repository's pull
-- request at a head, delivered by webhook, of a Legion head or an outside one: the head it read,
-- its author, whether GitHub gives the author write access, its state, and when GitHub says it was
-- submitted (null when the event did not say). side_reviewers records each naming of a reviewer
-- session for a pull request at a head. verdicts records each verdict, approve or changes, with
-- the session that gave it; issue is the Legion issue whose head it is, empty for a pull request
-- no Legion issue owns, and task and step are the review step of that issue's task whose agent
-- gave it, by the task's id and the step's place from 1 ('' and 0 for no Legion issue's verdict).
-- verdict_writes records each check run (kind check_run) and commit
-- status (kind status) the daemon wrote on a head: GitHub's id of it, its conclusion, the id of
-- the run or status of another writer it went over (foreign_id, 0 for none) and when it was
-- written.
--
-- A row is a record that nothing changes after its insert, and a read of the newest row of a key
-- takes the highest id, of the newest updated_at first for outside_pull_requests. So no table has a
-- primary key or a unique index, and none has a check:
-- the store's writes refuse a row that leaves out its repository, its head, its pull request's
-- number where the table has one, or its time, and a verdict, a write's kind or a conclusion its
-- reader does not know. Every column has a default but submitted_at and updated_at, which are
-- nullable, and each index keys on lower(repository), as the store's reads compare a repository's
-- names without case.
--
-- claims.review_launch_head and claims.review_launch_generation are supervise.Claim's
-- ReviewLaunchHead and ReviewLaunchGeneration: the head the launch that started a reviewer's
-- process gave it (runtime.SpawnSpec.ReviewHead) and that launch's generation, at which the launch
-- made or moved the reviewer's own clone; nothing moves that clone while the process runs. The
-- machine sets both as it builds a reviewer's launch (spawnSpec), and they reach the store with the
-- write that records the process the launch started (launchNow). Before each send to a reviewer,
-- and before each message the daemon holds for one, the machine compares the head the record holds
-- for the issue's pull request with this one, read only where the generation is the claim's own
-- (reviewHeadHolds), and starts the process again at the record's head where they differ. Kept on
-- the row, the comparison survives a restart of the daemon, which re-adopts a live reviewer and
-- hands it its next task in the process it found where the head has not moved. Both columns are
-- nullable and have no default, so the alter writes no row. The store reads a row that holds
-- neither (one written before this migration, or by a release before it) as no head at generation
-- 0, which names no launch: the next task of such a reviewer waits for a launch at the record's
-- head, which brings its clone there.
--
-- This migration only adds tables, indexes that are not unique and two nullable columns, and it
-- adds no outbox kind, so a release before this one runs on the schema it leaves: that release
-- names none of these tables and neither column in any statement, so it neither reads nor writes
-- them, its writes of a claim leave the two columns as they were, and a claim it inserts holds
-- neither. Its Migrate finds every version it embeds recorded and applies nothing.
--
-- There is no down file: migrations here are forward-only. Nothing of this migration need come
-- off to roll the daemon back either: the tables and the two columns stand unread under the
-- earlier release, and at the roll forward Migrate finds this version recorded and runs nothing
-- again, so the rows written before the rollback read as they were written. Every launch the
-- earlier release makes moves a claim's generation past the one the columns record, so at the
-- roll forward a head they record is read for no process the earlier release started.
create table outside_pull_requests (
  id bigserial,
  repository text not null default '',
  number integer not null default 0,
  head text not null default '',
  updated_at timestamptz,
  recorded_at timestamptz not null default now());
create index outside_pull_requests_number on outside_pull_requests (lower(repository), number, id);

create table verdict_reviews (
  id bigserial,
  repository text not null default '',
  number integer not null default 0,
  github_id bigint not null default 0,
  head text not null default '',
  author text not null default '',
  author_can_write boolean not null default false,
  state text not null default '',
  submitted_at timestamptz,
  recorded_at timestamptz not null default now());
create index verdict_reviews_number on verdict_reviews (lower(repository), number, id);

create table side_reviewers (
  id bigserial,
  repository text not null default '',
  number integer not null default 0,
  head text not null default '',
  session text not null default '',
  recorded_at timestamptz not null default now());
create index side_reviewers_head on side_reviewers (lower(repository), number, head, id);

create table verdicts (
  id bigserial,
  repository text not null default '',
  number integer not null default 0,
  head text not null default '',
  issue text not null default '',
  task text not null default '',
  step integer not null default 0,
  verdict text not null default '',
  session text not null default '',
  recorded_at timestamptz not null default now());
create index verdicts_head on verdicts (lower(repository), number, head, id);

create table verdict_writes (
  id bigserial,
  repository text not null default '',
  head text not null default '',
  kind text not null default '',
  github_id bigint not null default 0,
  conclusion text not null default '',
  foreign_id bigint not null default 0,
  written_at timestamptz not null default now());
create index verdict_writes_head on verdict_writes (lower(repository), head, id);

alter table claims add column review_launch_head text;
alter table claims add column review_launch_generation bigint;
