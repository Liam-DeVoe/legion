// Package consumernames is `envoy-infra consumer-names`, the operator's commands for the
// selection records behind the fresh JetStream consumer names (packages/envoy/consumernames):
// the cutover from the fixed names (prepare, create-bucket, convert), the recovery of a lost
// record or bucket (replay, create-bucket --after-loss), the rollback to the fixed names
// (restore), the replacement of one listener's durable (replace), the enrolment of a daemon store
// made after the cutover (enroll), and the reads and cleanup an operator runs between
// (status, cleanup).
//
// Every command runs as the deployment's one operator identity, whose NATS grant is the writer
// hold's phase's (natsgrant.Operator), and reads the hold row through the broker's routes first
// (holdrecord.Client): status runs while the row is released, prepare while it is prepared, and
// every command that changes state only while the row is held, inside a hold, each writing its
// intent and its done or stop entry in the hold's journal (holdrecord.Client.Admit, AppendDone,
// AppendStop). The selection records themselves are the families' stores: the session consumers'
// and each listener's durable's in the KV bucket consumernames.SessionBucket, Dispatch's two
// durables' in Dispatch's intake_consumers table, and each Legion daemon's in its own database's
// intake_consumers table, which the operator reaches with its own database credential.
package consumernames
