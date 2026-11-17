-- 20261117004117_step_starts.up.sql — the count of starts of each step of an issue, which carries a
-- step record's id from the step's start to its end (internal/workflow's StepID).
--
-- A record's id is a uuid of the issue, its run, the task, the kind, the place and this count. The
-- start of a step raises the count by one in the transaction that writes the start, and the write
-- that ends the step reads it unchanged, maybe in another transaction of another daemon after a
-- restart: no start of the same place comes between the two, because the step runs until one of
-- the ends ends it. agent is the agent that latest start named, which the end names again: a merge
-- GitHub already made starts no merger and names none.
--
-- The count is keyed by task (slice 1 writes the one task, default, of today's fixed phases), and a
-- re-admission does not clear it (record.Store.ClearGeneration keeps it): the generation is part of
-- the id already, and the count only rises. A step running when this release deploys has no row,
-- and its end reads the count 0. Nothing of an earlier release reads or writes the table, so a
-- rollback leaves it in place.
create table step_starts (
  issue text not null,
  task text not null,
  kind text not null,
  place integer not null,
  starts bigint not null,
  agent text not null,
  primary key (issue, task, kind, place)
);
