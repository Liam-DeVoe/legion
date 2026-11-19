-- 20261118212431_legion_events.up.sql — where Dispatch's intake of each Legion daemon's state
-- events stands.
--
-- dispatch://INFRA-3931: a Legion daemon publishes each change of its state as a state event, one
-- message per committed transaction on notifications.legion.<token>.state, and Dispatch's intake
-- (internal/dispatch/legion/intake.go, on the held durable `legion-state-events`) writes the
-- project's observations from those messages. It reads the daemon's whole state only where the
-- events cannot carry it: a project with no starting point, a boot that does not follow, a gap the
-- stream no longer holds, and a read owed since one failed. This migration gives legion_reads
-- (20261103170923) what tells those reads from the batches of events, and creates the table that
-- holds how far each project's observations have followed its daemon.
--
-- legion_reads gains three columns:
--
-- - kind is what the row records: `read`, a whole read of the daemon's state, or `events`, one
--   message of state events the intake applied, whose read_at is the message's `at` (the daemon's
--   commit time) and whose bytes are the message's.
-- - reason is why a whole read was made (legion's ReadReason constants: `start`, `boot`,
--   `migrated`, `gap`, `retry`), and null for a batch of events and for the sampler's minute.
-- - recorded_at is when Dispatch wrote the row. Beside read_at it is how long an event took to
--   reach Dispatch, since a batch's read_at is the daemon's own time.
--
-- legion_reads_project_recorded_at orders a project's rows as Dispatch wrote them, by recorded_at
-- and then id: the Legion board's header reads a project's newest row by it, and
-- legion.PruneReads keeps each project's newest row by it. A batch's read_at is the daemon's clock
-- and a whole read's is Dispatch's, so legion_reads_project_read_at (20261103170923) does not order
-- the two kinds.
--
-- legion_event_cursors is one row per project whose events the intake has applied or whose state
-- it has read whole, read `for update` and written in the transaction of each batch and each read:
--
-- - project is the project's own key and the row's identity, and goes with the project.
-- - state_url is the daemon the row follows: the setting's state_url when the row was written. A
--   row naming another daemon than the setting is no starting point, so the intake reads the
--   setting's daemon whole. The settings routes delete the row with every write of the setting
--   (api/settings_legion.go), which is what makes a PUT of the same URL a fresh whole read.
-- - run is the daemon run (the UUID it minted at boot) of the newest message the intake applied,
--   null before the first. A boot at or below last_seq under another run is a daemon whose
--   sequence went back, which the intake reads whole.
-- - last_seq is the newest state sequence the project's observations reflect: the stateSeq a
--   successful whole read served, or the seq of the newest message applied. Null while the row
--   holds none, which is only while a read is owed.
-- - read_owed is set by a whole read that failed and cleared by one that succeeded.
-- - read_tried_at is when the last whole read was made, which keeps an owed read's retries a minute
--   apart.
--
-- The defaults are what let a pod of the release before this one go on writing its sampler's rows
-- during the rolling release: its insert names none of the three columns and reads as a whole read
-- (`kind = 'read'`) with no reason, recorded at its own transaction's time, and it never names
-- legion_event_cursors.
--
-- Each column is added with a default every existing row meets: a constant one, and now(), which
-- is stable, so every row stored already reads this migration's moment as its recorded_at and none
-- is rewritten. The check holds the new column alone, which every existing row meets at its
-- default, so it refuses nothing; it is validated by a scan of legion_reads (a week of reads at the
-- most, legion.ReadRetention) under the ACCESS EXCLUSIVE lock the three columns take. The index is
-- built over the same rows under the same lock. The new table no earlier release names, and its
-- foreign key holds projects in SHARE ROW EXCLUSIVE until the migration commits. Nothing existing
-- is refused or rewritten, and the census answers 0. The identifier is the UTC moment this was
-- written, as `migrationid` requires of every migration past the numbered series.
alter table legion_reads
  add column kind text not null default 'read' check (kind in ('read', 'events')),
  add column reason text,
  add column recorded_at timestamptz not null default now();

create index legion_reads_project_recorded_at on legion_reads (project, recorded_at desc, id desc);

create table legion_event_cursors (
  project text primary key references projects(key) on delete cascade,
  state_url text not null,
  run text,
  last_seq bigint,
  read_owed boolean not null default false,
  read_tried_at timestamptz
);
