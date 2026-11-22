-- 20261122045825_verdicts.up.sql — the records of the daemon's writes of the required check
-- legion-verdict: the pull requests of a verdict repository that no Legion issue owns, with their
-- heads; the reviews of a verdict repository's pull requests; the reviewer session a controller
-- names for such a pull request at a head; the verdicts reviewers give on a head; and each check
-- run and commit status named legion-verdict that the daemon writes. store/verdicts.go reads and
-- writes each table.
--
-- outside_pull_requests records each sighting of such a pull request at a head, from webhook
-- events. pull_requests cannot hold it, because its primary key is the Legion issue that owns the
-- pull request (0004_record). verdict_reviews records each review of a verdict repository's pull
-- request at a head, delivered by webhook, of a Legion head or an outside one: the head it read,
-- its author, whether GitHub gives the author write access, its state, and when GitHub says it was
-- submitted (null when the event did not say). side_reviewers records each naming of a reviewer
-- session for a pull request at a head. verdicts records each verdict, approve or changes, with
-- the session that gave it and the commit of the tester's pass it carries to the head
-- (tested_commit, empty for none); issue is the Legion issue whose head it is, empty for a pull
-- request no Legion issue owns. verdict_writes records each check run (kind check_run) and commit
-- status (kind status) the daemon wrote on a head: GitHub's id of it, its conclusion, the id of
-- the run or status of another writer it went over (foreign_id, 0 for none) and when it was
-- written.
--
-- A row is a record that nothing changes after its insert, and a read of the newest row of a key
-- takes the highest id. So no table has a primary key or a unique index, and none has a check:
-- the store's writes refuse a row that leaves out its repository, its head, its pull request's
-- number where the table has one, or its time, and a verdict, a write's kind or a conclusion its
-- reader does not know. Every column has a default but submitted_at, which is nullable, and each
-- index keys on lower(repository), as the store's reads compare a repository's names without case.
--
-- This migration only adds tables and indexes that are not unique, and it adds no outbox kind, so
-- a release before this one runs on the schema it leaves: that release names none of these tables
-- in any statement, so it neither reads nor writes them, and its Migrate finds every version it
-- embeds recorded and applies nothing.
--
-- There is no down file: migrations here are forward-only. Nothing of this migration need come
-- off to roll the daemon back either: the tables stand unread under the earlier release, and at
-- the roll forward Migrate finds this version recorded and runs nothing again, so the rows written
-- before the rollback read as they were written.
create table outside_pull_requests (
  id bigserial,
  repository text not null default '',
  number integer not null default 0,
  head text not null default '',
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
  verdict text not null default '',
  tested_commit text not null default '',
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
