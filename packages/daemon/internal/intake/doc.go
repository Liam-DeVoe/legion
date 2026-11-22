// Package intake turns durable Envoy notifications and Go API observations into one transactional
// fact path.
//
// # Durable consumers
//
// The daemon of project P reads the notification stream, ENVOY_NOTIFICATIONS, through three durable
// consumers, whose names are legion-go-<P>-dispatch, legion-go-<P>-github and legion-go-<P>-wake
// (dispatchConsumerName, githubConsumerName, wakeConsumerName). How those names reach the server
// is legion.yaml's `consumer_names` (config.Config.ConsumerNames, ConsumerSpec.Names):
//
// The daemon's rows in intake_consumers are its project's family, daemon/<P>
// (consumernames.DaemonFamily): its sentinel is the row envoy.sentinel/daemon/<P> and each record
// the row daemon/<P>/<logical>. Several projects' daemons share one database, and each reads only
// its own family's rows, so one project's cutover leaves another's daemon as it was.
//
//   - `fixed`, the default: the three names are the consumers' own names, opened as the daemon has
//     always opened them (openConsumer, fixed_names.go). The daemon reads its family's cutover
//     marker at its boot and again after every reconnect of its NATS connection
//     (fixedNaming.rereadMarker, fixedNaming.watch), and stops once the marker reads `done`: a
//     converted store must not run the fixed names.
//   - `fresh`: the three names are logical names. Each creation of a durable is a physical name of
//     its own, <logical>~<nuid>, never made twice, carrying the metadata envoy.logical=<logical>,
//     and the logical name's record (consumernames.PGStore) selects the one the daemon reads
//     (durable_names.go). Every consumer call is refused until the operator's convert has marked
//     the family's sentinel `done` and recorded the stream's identity in it. Every call on a
//     physical name goes on a NATS connection of its own, named <logical>/<nuid>
//     (GenerationConnection), which the auth service may grant that one consumer; a daemon NATS
//     connection keeps no reconnect buffer (natsauth.Connect), so nothing sent while it is cut is
//     sent after the reconnect. The daemon reads nothing of the stream for these names and deletes
//     no consumer: a name it retires is the operator's `envoy-infra consumer-names cleanup` to
//     delete.
//
// Any other value of `consumer_names` refuses the daemon's start, naming it. The daemon logs its mode
// first at its boot, before any NATS connect (package daemon's consumerNamesLine).
//
// # Checkpoints
//
// The position the daemon applied through is kept per durable in consumer_positions, keyed by the
// logical name (withStreamPosition, consumernames.LogicalOf): a fixed name is its own logical name,
// and on fresh names the checkpoint is the durable's, whichever creation applied the message.
//
// # The ERROR line
//
// A name step that fails on fresh names writes one ERROR line and no metric:
//
//	{"msg":"intake: a durable consumer's physical name could not be settled","consumer":<logical>,"physical":<name>,"op":<op>,"error":<error>}
//
// physical is "" when no name was read. op is select (a refusal before anything was made, and a
// reader's), create, missing_current, recover, update, connect, info or last_floor. At the boot every
// step but last_floor stops the boot; the same line from a later repositories change, lag read or
// held-release poll does not stop the daemon.
package intake
