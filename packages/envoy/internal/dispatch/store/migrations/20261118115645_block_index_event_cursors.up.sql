-- 20261118115645_block_index_event_cursors.up.sql
-- How far each build's block-index reconciliation has read the event log for the documents another
-- build wrote: one row per build commit, which every block-index tick of that build reads and
-- moves before it lists the owed documents (docs' markVersionsAnotherBuildWrote). The release
-- before this one writes a version's rows, and a new document's, the spec of an issue it creates
-- included, with no node and states nothing in the node and tuple store, and it neither reads nor
-- sets this build's own mark (store_owed_at, migration 20261116221539). So a document it edits or
-- creates after this build's drain has cleared that mark is owed this build a pass that nothing
-- lists. While a live server process of another build serves (server_processes, by commit), each
-- tick therefore marks owed every document that an artifact.version or artifact.created event
-- after its build's event_id names, and every document of each issue an issue.created event after
-- it names, and once the fleet serves its build alone the ticks read on only until they have
-- passed every event the other build can have written, and then stop. A rollback or a roll
-- forward that stops one build before it starts the other never has two builds serving at once,
-- so each process's first tick that reads a proven horizon also reads once, from the newest
-- event_id of every row through the newest event.
--
--   build_commit        the commit the build was stamped with (cmd/dispatch's buildCommit), the empty
--                       string for an unstamped build. The build's first tick that reads a proven
--                       safe horizon inserts the row at the highest event_id any build's row holds,
--                       where the build before it stopped reading, or at that horizon where no row
--                       exists, so the cursor never starts at the beginning of the log.
--   event_id            the highest events.id the build's ticks have read or passed. A tick reads the
--                       events after it and at or below the event log's safe horizon
--                       (events.Horizon), at most one page of them, and never moves it past that
--                       horizon: event ids are not commit order, so an uncapped `id > event_id` read
--                       could pass an event that is still committing.
--   another_build_seen  whether the last tick found a live process of another build.
--   read_through        the newest events.id the log held when a tick found another build serving,
--                       found the fleet serving its build alone right after a tick that did not, or
--                       was its process's first tick with a proven horizon. A process leaves the
--                       registry only after every event it wrote has committed, so each of them is
--                       at or below it, and the ticks read on until event_id reaches it, however far
--                       the safe horizon lags the log. A tick that finds no other build, after one
--                       that found none either, with event_id at or past it, moves event_id to the
--                       horizon and reads no event.
--
-- A new table of one short row per build deployed; the previous release never reads it.
create table block_index_event_cursors (
  build_commit text primary key,
  event_id bigint not null,
  another_build_seen boolean not null default false,
  read_through bigint not null default 0
);
