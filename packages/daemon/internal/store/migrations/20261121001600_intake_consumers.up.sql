-- 20261121001600_intake_consumers.up.sql -- the selection records of the daemon's durable
-- consumers, which a daemon whose legion.yaml says `consumer_names: fresh` reads and writes, and
-- whose cutover marker a daemon on the fixed names (the default) reads at its boot and nothing else.
--
-- One row per logical name of each project, legion-go-<project>-dispatch, -github and -wake, keyed
-- daemon/<project>/<logical> (consumernames.PGKey of consumernames.DaemonFamily(project)) and never
-- deleted: record is the record github.com/sjawhar/envoy/consumernames keeps for it
-- (consumernames.Record as JSON, carrying its row's key: the physical name selected, the operation
-- in flight, the saved start, the last ack floor read and the retired names), and revision is what
-- each conditional write compares, 1 at the insert and one more at each update
-- (consumernames.PGStore). Each project's row envoy.sentinel/daemon/<project>
-- (consumernames.PGSentinelKey) is its sentinel, naming its family: the cutover marker the
-- operator's convert writes, which a fresh daemon reads before it calls the server at all. No
-- family holds a '.', so no sentinel's key is a record's. No row is written here: with no sentinel
-- a fresh daemon refuses every consumer call, and a fixed one reads nothing. The table holds every
-- project of the database, as the checkpoints of consumer_positions do; each project's rows and
-- sentinel are its own.
--
-- There is no down file: migrations here are forward-only. A daemon of the earlier release never
-- reads the table, so it runs beside it unchanged.
create table intake_consumers (
  logical text primary key,
  record jsonb not null,
  revision bigint not null check (revision > 0)
);
