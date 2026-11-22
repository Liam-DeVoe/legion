-- 20261121001600_intake_consumers.up.sql -- the selection records of the daemon's durable
-- consumers, which a daemon whose legion.yaml says `consumer_names: fresh` reads and writes, and
-- whose cutover marker a daemon on the fixed names (the default) reads at its boot and again after
-- each reconnect of its NATS connection, and writes nothing.
--
-- Each project's daemon has a store of its own in the table, its family daemon/<project>
-- (consumernames.DaemonFamily), and reads and writes only that family's rows:
--   - daemon/<project>/<logical> (consumernames.PGKey), one row per logical name,
--     legion-go-<project>-dispatch, -github and -wake, never deleted: record is the record
--     github.com/sjawhar/envoy/consumernames keeps for it (consumernames.Record as JSON: the physical
--     name selected, the operation in flight, the saved start, the last ack floor read and the
--     retired names);
--   - envoy.sentinel/daemon/<project> (consumernames.PGSentinelKey), the project's sentinel: the
--     cutover marker and the stream identity the operator's convert writes, which a fresh daemon
--     reads before it calls the server at all.
-- revision is what each conditional write compares, 1 at the insert and one more at each update
-- (consumernames.PGStore). Several projects' daemons share one database, as the checkpoints of
-- consumer_positions do, and one project's cutover leaves another's daemon as it was. No row is
-- written here: with no sentinel a fresh daemon refuses every consumer call, and a fixed one opens
-- the fixed names.
--
-- There is no down file: migrations here are forward-only. A daemon of the earlier release never
-- reads the table, so it runs beside it unchanged.
create table intake_consumers (
  logical text primary key,
  record jsonb not null,
  revision bigint not null check (revision > 0)
);
