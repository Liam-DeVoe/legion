package legion

// intake.go is Dispatch's intake of each Legion daemon's state events (dispatch://INFRA-3931, the
// plan's D9). A daemon publishes one message per transaction it committed that moved what
// Dispatch observes, on notifications.legion.<token>.state: the project, the daemon's run, the
// first and the last state sequence of the transaction, its commit time and its events (a change
// of an issue's fields, the admission block, a boot). The intake takes them from one held durable
// (bus.DispatchLegionStateDurable, delivery.HeldDurable), so of every envoy-dispatch process one
// applies them, and writes legion_observations from them as a whole read would have written them.
//
// Each project's messages run one at a time on that project's runner (messageKey), and so does
// every whole read of the project (ReadWhole): a take-over's and a settings change's are queued on
// the same runner (delivery.HeldDurable.Queue), so a read never interleaves with the project's
// messages and its snapshot is never older than a message already applied. legion_event_cursors
// says how far the project's observations have followed its daemon (cursor), and
// legion_event_parts holds the messages of a transaction the daemon split over several until the
// last one arrives (part).

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/processes"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// intakeConnectionName is the name StartIntake asks connect for, so the NATS server's connection
// listings tell the intake's connection from the delivery intakes' and from Dispatch's publisher.
const intakeConnectionName = "dispatch-legion-intake"

// stateSubject is the durable's one filter subject: every Legion deployment's state lane, from
// the generated contract the daemon's publish names too (contracts.LegionStateSubject).
const stateSubject = contracts.LegionNoticeTopicPrefix + "*" + contracts.LegionStateSuffix

// The durable's flow control. A message is applied in one short transaction, and a message whose
// apply waits on the database says so every applyLastRetry at the most (InProgress), so
// stateAckWait only redelivers a message the runner never reached in two minutes. stateMaxAckPending
// is how many messages may wait on their runners at once. One project's messages run one at a
// time, and INFRA's measured rate is about 53,000 messages a day, so 256 messages are about seven
// minutes of INFRA's traffic. A runner held that long (a database that refuses its writes, a whole
// read that waits on its daemon) fills the window, and NATS then delivers no message of any
// project until that runner moves again. The messages wait in the stream meanwhile: none is lost.
const (
	stateAckWait       = 2 * time.Minute
	stateMaxAckPending = 256
)

// consumerConfig is the policy a take-over creates the durable with: one push consumer over every
// state lane, acknowledged by hand, delivering new messages at its first creation, since a whole
// read of each configured project is the starting point and a message older than the durable says
// nothing that read does not. A take-over of a durable that exists creates it again at its ack
// floor (delivery's takeOverConfig), so a stopped process's messages are replayed.
func consumerConfig() natsgo.ConsumerConfig {
	return natsgo.ConsumerConfig{
		Durable:        bus.DispatchLegionStateDurable,
		DeliverSubject: natsgo.NewInbox(),
		FilterSubject:  stateSubject,
		DeliverPolicy:  natsgo.DeliverNewPolicy,
		AckPolicy:      natsgo.AckExplicitPolicy,
		AckWait:        stateAckWait,
		MaxAckPending:  stateMaxAckPending,
	}
}

// ReadRetryAfter is how long an owed whole read waits after the one before it was tried: a read
// that failed is tried again at the project's next message, its next settings change or the next
// take-over, and never sooner than this after the last try. A message of another store than the
// cursor's (ReadReasonStore) is read for no sooner than this after the last whole read either.
const ReadRetryAfter = time.Minute

// The reasons a whole read is made, which legion_reads.reason records and the whole read's log
// line names. There are no others: no timer reads a daemon.
const (
	// ReadReasonStart is a project with a setting and no starting point: no cursor, or one that
	// follows another daemon than the setting names. It is read at the intake's take-over (which
	// covers the durable's first creation), on a settings change, and at the project's next
	// message if neither has read it yet, and at none of them while a Dispatch process of an
	// earlier build is in the fleet (processes.EarlierLegionIntake).
	ReadReasonStart = "start"
	// ReadReasonBoot is a boot event that does not follow the cursor: its sequence is not the next
	// one, or it is at or below the cursor under another run (a daemon whose counter went back).
	ReadReasonBoot = "boot"
	// ReadReasonSchema is a boot event above the cursor whose schemaVersion is not the one the
	// cursor holds: the boot moved the store's schema, and a migration may have rewritten what
	// Dispatch observes outside the writes that publish events.
	ReadReasonSchema = "schema"
	// ReadReasonGap is a message whose first sequence is past the next one: events the stream no
	// longer holds. A message that does not continue the parts of a split transaction the intake
	// holds (part) is one too, since the transaction cannot be completed.
	ReadReasonGap = "gap"
	// ReadReasonRetry is a read owed since the last one failed.
	ReadReasonRetry = "retry"
	// ReadReasonStore is a message whose stateId is not the cursor's: it is counted in another
	// store than the one the starting point read, so its sequences say nothing of the cursor's.
	// The read finds the store the daemon the setting names counts in now.
	ReadReasonStore = "store"
	// ReadReasonRefused is a message the database refused for good (refusedForGood): it is
	// finished unapplied, and the read puts the project's observations at the daemon's state.
	ReadReasonRefused = "refused"
)

// Why a whole read the daemon answered is a failed read all the same. Each leaves the read owed;
// all but the first record the read's observations, since the state is the project's own.
var (
	// ErrStateOfAnotherProject is a state whose daemon.project is not the setting's project.
	ErrStateOfAnotherProject = errors.New("the daemon serves another project's state")
	// ErrNoStateSeq is a state with no daemon.stateSeq: a daemon without state events.
	ErrNoStateSeq = errors.New("the daemon serves no daemon.stateSeq, so it publishes no state events")
	// ErrStateSeqZero is a state whose daemon.stateSeq is 0: a daemon that has committed no state
	// event, which a daemon with a workflow never serves, since its boot event commits first.
	ErrStateSeqZero = errors.New("the daemon serves daemon.stateSeq 0: it has published no state event")
	// ErrNoDispatchURL is a state whose settings carry no dispatch_url: a daemon that runs no
	// workflow now, so it publishes no state events, whatever it published before. A daemon that
	// never ran one serves daemon.stateSeq 0 too, and this is the reason its read names.
	ErrNoDispatchURL = errors.New("the daemon's settings carry no dispatch_url: it runs no workflow, so it publishes no state events")
	// ErrNoStateID is a state with a daemon.stateSeq and no daemon.stateId: no message can be
	// matched to the store that sequence is counted in.
	ErrNoStateID = errors.New("the daemon serves no daemon.stateId, so its state messages cannot be matched to its store")
)

// wholeReadLine is the log line of every whole read, at INFO.
const wholeReadLine = "legion intake: whole read"

// The log lines of a state message the intake finishes and does not apply, and of a starting point
// it does not take, each at INFO, once per line and project per dropLogEvery (reportEvery).
const (
	// droppedLine is a message for a project with no Legion setting.
	droppedLine = "legion intake: a state message for a project with no Legion setting is dropped"
	// otherStoreLine is a message whose stateId is not the cursor's, after the read it called
	// for or inside ReadRetryAfter of the last read.
	otherStoreLine = "legion intake: a state message of another store than the starting point's is dropped"
	// beyondLine is a message whose first sequence is still past the next one after a good whole
	// read it called for: the read holds every event the daemon published before it.
	beyondLine = "legion intake: a state message past the sequence of the whole read it called for is dropped"
	// earlierBuildLine is a starting point not taken while a Dispatch process of an earlier build
	// is in the fleet, and a message of such a project, which is dropped.
	earlierBuildLine = "legion intake: a Dispatch process of an earlier build is in the fleet, so no starting point is taken"
)

// refusedLine is the log line of a message the database refused for good, at WARN, once for each
// such message.
const refusedLine = "legion intake: the database refuses a state message for good, so it is finished and the project is read whole"

// dropLogEvery bounds how often one line of reportEvery is logged for one project, and pruneEvery
// how often one process prunes legion_reads (PruneReads).
const (
	dropLogEvery = time.Hour
	pruneEvery   = time.Hour
)

// A database step that fails is tried again in place, after applyFirstRetry and twice as long
// after each failure after it, up to applyLastRetry, for as long as the handling lasts, unless the
// database refused it for good (refusedForGood). Vars so a test can shrink them.
var (
	applyFirstRetry = time.Second
	applyLastRetry  = 30 * time.Second
)

// settingsKey is the runner key a take-over queues its settings read on (takenOver). No message
// uses it: messageKey keys a message by its project or by its subject, and neither is empty.
const settingsKey = ""

// Intake applies every Legion daemon's state events to legion_observations from the durable
// bus.DispatchLegionStateDurable, while this process holds the durable's hold, and reads a
// daemon's state whole where the events cannot carry it (ReadReasonStart and the rest). It runs
// whenever Dispatch has NATS, whether or not the GitHub App is configured.
type Intake struct {
	pool    *store.Pool
	durable *delivery.HeldDurable
	// settingsWake is signalled on each write of a Legion setting (SettingsChannel); nil reads the
	// settings only at each take-over.
	settingsWake <-chan struct{}
	// now is Dispatch's clock: a whole read's rows are stamped by it, and an owed read's retry is
	// spaced by it. A test moves it.
	now func() time.Time

	mu sync.Mutex
	// reported is when each line of reportEvery was last logged for each project, for the lines
	// logged in the last dropLogEvery: reportEvery deletes each older entry.
	reported map[reportKey]time.Time
	// pruned is when this process last pruned legion_reads.
	pruned time.Time
}

// reportKey is one line of reportEvery for one project.
type reportKey struct {
	line, project string
}

// NewIntake builds the intake on natsClient, a client opened for it alone (a bus.Client holds one
// JetStream subscription), holding its durable under a hold and a consumer lease it takes on leases
// (ReserveLeases), and reading the settings again on each signal of settingsWake: in the server,
// the shared listener's wake on SettingsChannel.
func NewIntake(natsClient *bus.Client, pool *store.Pool, leases delivery.DurableLeases, settingsWake <-chan struct{}) *Intake {
	in := &Intake{pool: pool, settingsWake: settingsWake, now: time.Now, reported: map[reportKey]time.Time{}}
	in.durable = delivery.NewHeldDurable(natsClient, leases, consumerConfig, messageKey, in.deliver, in.takenOver)
	return in
}

// ReserveLeases reserves the intake's callers of the lease pool: three, as every held durable's
// (delivery.ReserveDurableLeases) - the consumer lease its take-over runs under, its hold's
// statements and its hold's renewal, each of bus.DispatchLegionStateDurable. The server reserves
// them while it wires itself, before the lease pool's callers are complete, whether or not it then
// starts the intake, so every configuration reserves the same callers.
func ReserveLeases(callers *store.LeaseCallers) (delivery.DurableLeases, error) {
	return delivery.ReserveDurableLeases(callers, bus.DispatchLegionStateDurable)
}

// StartIntake runs an Intake until ctx is cancelled, on a bus client of its own that connect opens
// under intakeConnectionName, and returns the error of a connect that fails with nothing started.
// leases is the intake's reservation of the lease pool (ReserveLeases), settingsWake the wake
// NewIntake reads the settings again on.
//
// The returned stop, called once ctx is cancelled, waits for Run, which returns once the intake has
// stopped delivering and released its hold under the intakes' stop budget from ctx's end, and then
// closes the client. A caller that closes the database pool after stop returns never closes it
// under one of the intake's writes. A second call returns once the first has.
func StartIntake(ctx context.Context, connect func(name string) (*bus.Client, error), pool *store.Pool, leases delivery.DurableLeases, settingsWake <-chan struct{}) (stop func(), err error) {
	client, err := connect(intakeConnectionName)
	if err != nil {
		return nil, fmt.Errorf("connect the Legion state intake: %w", err)
	}
	intake := NewIntake(client, pool, leases, settingsWake)
	done := make(chan struct{})
	go func() {
		defer close(done)
		intake.Run(ctx)
	}()
	var stopped sync.Once
	return func() {
		stopped.Do(func() {
			<-done
			client.Close()
		})
	}, nil
}

// Run delivers the state events from the durable while this process holds its hold, and stands by
// for it otherwise, until ctx ends; meanwhile each signal of the settings wake queues every
// configured project's starting point (SettingsChanged). It returns once the durable's delivery
// has stopped and its hold is released (delivery.HeldDurable.Run).
func (in *Intake) Run(ctx context.Context) {
	var wakes sync.WaitGroup
	if in.settingsWake != nil {
		wakes.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-in.settingsWake:
					in.SettingsChanged(ctx)
				}
			}
		})
	}
	in.durable.Run(ctx)
	wakes.Wait()
}

// SettingsChanged queues each configured project's starting point on the project's runner
// (startingPoint). A write of a Legion setting wakes it (SettingsChannel), and each take-over
// queues it (takenOver): a project whose cursor is gone (PutSettings, DeleteSettings, or no read
// yet) is read whole once, and every other project, whose cursor stands, is not read at all. A
// settings read that fails queues nothing: a project with no starting point is read at its next
// message all the same, and the listener's wake fires again once it listens again. A process that
// does not hold the durable queues nothing: the process that holds it heard the same notification.
func (in *Intake) SettingsChanged(ctx context.Context) {
	settings, err := ListSettings(ctx, in.pool)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("legion intake: read the Legion settings to check each project's starting point", "error", err)
		}
		return
	}
	for _, setting := range settings {
		project := setting.Project
		in.durable.Queue(project, func(ctx context.Context) { in.startingPoint(ctx, project) })
	}
}

// takenOver is the durable's take-over callback (delivery.NewHeldDurable): once a take-over has
// bound the durable, its first creation included, it queues SettingsChanged on settingsKey's
// runner. The callback runs on the durable's run loop, which waits for it, so it only queues the
// settings read and does not make it.
func (in *Intake) takenOver() {
	in.durable.Queue(settingsKey, in.SettingsChanged)
}

// startingPoint reads project's daemon whole where its observations have no starting point
// (ReadReasonStart), or where a read is owed and due (ReadReasonRetry), and reads nothing
// otherwise. It takes no starting point while a Dispatch process of an earlier build is in the
// fleet (startBlocked). It runs on the project's runner; a database step that fails is tried again
// in place until it succeeds or the handling ends.
func (in *Intake) startingPoint(ctx context.Context, project string) {
	var pending *wholeRead
	in.untilDone(ctx, nil, project, func(ctx context.Context) (outcome, error) {
		setting, current, found, err := in.position(ctx, project)
		if err != nil || !found {
			return outcomeDone, err
		}
		reason := startReason(current, setting.StateURL, in.now())
		if reason == "" {
			return outcomeDone, nil
		}
		if blocked, err := in.startBlocked(ctx, reason); err != nil || blocked {
			if blocked {
				in.reportEvery(earlierBuildLine, project)
			}
			return outcomeDone, err
		}
		recorded, err := in.readWhole(ctx, setting, reason, &pending)
		if err != nil {
			return outcomeDone, err
		}
		if !recorded {
			return outcomeAgain, nil
		}
		return outcomeDone, nil
	})
}

// startBlocked reports whether a whole read made for reason is a starting point the intake does
// not take now: ReadReasonStart while a Dispatch process of an earlier build is in the fleet
// (processes.EarlierLegionIntake). Such a build reads every daemon whole each minute and writes
// its rows at its own clock, so a row of it could land after the starting point and after the
// events applied on it. The next message, settings change or take-over after that process has left
// the fleet takes the starting point; no timer does.
func (in *Intake) startBlocked(ctx context.Context, reason string) (bool, error) {
	if reason != ReadReasonStart {
		return false, nil
	}
	return processes.EarlierLegionIntake(ctx, in.pool)
}

// messageKey keys a state message by the project its payload names, so one runner applies a
// project's messages one at a time. A message whose envelope or payload does not decode, or that
// names no project, is keyed by its subject, and deliver reports it.
func messageKey(msg *natsgo.Msg) (string, *contracts.Envelope, bool) {
	var envelope contracts.Envelope
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		return msg.Subject, nil, true
	}
	var head struct {
		Project string `json:"project"`
	}
	if err := json.Unmarshal([]byte(envelope.Payload), &head); err != nil || head.Project == "" {
		return msg.Subject, &envelope, true
	}
	return head.Project, &envelope, true
}

// outcome is what one attempt at a message, or at a project's starting point, came to.
type outcome int

const (
	// outcomeDone is a message applied, or work finished.
	outcomeDone outcome = iota
	// outcomeSkipped is a message at or below the cursor, or a part of a split transaction held
	// already: nothing is written.
	outcomeSkipped
	// outcomeHeld is a part of a split transaction held until its last part (part): no
	// observation is written and the cursor does not move.
	outcomeHeld
	// outcomeDropped is a message for a project with no Legion setting.
	outcomeDropped
	// outcomeOtherStore is a message whose stateId is not the cursor's (verdictOtherStore).
	outcomeOtherStore
	// outcomeBeyond is a message still past the next sequence after a good whole read it called
	// for (verdictBeyond).
	outcomeBeyond
	// outcomeEarlierBuild is a message of a project with no starting point while a Dispatch
	// process of an earlier build is in the fleet (startBlocked).
	outcomeEarlierBuild
	// outcomeAgain is an attempt to make again at once: a whole read the project's setting moved
	// under, or a message whose whole read was just made (applyMessage).
	outcomeAgain
)

// deliver applies one state message on its project's runner and acknowledges it (applyMessage).
// A message that does not decode is acknowledged and logged: the next one of its project is then
// a gap, which a whole read covers. A message the runner could not apply before the handling
// ended is handed back rather than acknowledged, so the next holder applies it. A message the
// intake finishes unapplied is logged once per reason and project per dropLogEvery (reportEvery).
func (in *Intake) deliver(m delivery.HeldMessage) {
	message, err := decodeStateMessage(m.Envelope, m.Msg.Data)
	if err != nil {
		slog.Error("legion intake: decode a state message; acknowledging it", "subject", m.Msg.Subject, "error", err)
		m.Ack()
		return
	}
	ctx := m.Context()
	attempt := messageAttempt{message: message, bytes: len(m.Msg.Data), msg: m.Msg}
	result, finished := in.untilDone(ctx, m.Msg, message.Project, func(ctx context.Context) (outcome, error) {
		return in.applyMessage(ctx, &attempt)
	})
	if !finished {
		slog.Warn("legion intake: the intake stopped before a state message was applied; handing it back",
			"project", message.Project, "seq", message.Seq)
		m.HandBack()
		return
	}
	m.Ack()
	sequences := []any{"run", message.Run, "firstSeq", message.FirstSeq, "seq", message.Seq}
	switch result {
	case outcomeDone:
		in.pruneHourly(ctx)
	case outcomeDropped:
		in.reportEvery(droppedLine, message.Project)
	case outcomeOtherStore:
		in.reportEvery(otherStoreLine, message.Project, append(sequences,
			"stateId", message.StateID, "cursorStateId", logValue(attempt.cursor.stateID))...)
	case outcomeBeyond:
		in.reportEvery(beyondLine, message.Project, append(sequences, "stateSeq", logValue(attempt.cursor.lastSeq))...)
	case outcomeEarlierBuild:
		in.reportEvery(earlierBuildLine, message.Project, sequences...)
	}
}

// messageAttempt is one message on its runner, across the attempts untilDone makes at it.
type messageAttempt struct {
	message stateMessage
	bytes   int
	msg     *natsgo.Msg
	// pending is a whole read this message made that is not recorded yet (readWhole), and readMade
	// whether one was recorded: a message is read for once, but for a starting point.
	pending  *wholeRead
	readMade bool
	// cursor is the project's cursor as the last batch read it (applyBatch), which the log line
	// of a message finished unapplied names.
	cursor cursor
	// refusal is the database's refusal for good of the message's batch, once there is one: the
	// message is not tried again, and one whole read is made of its project (readRefused).
	refusal *pgconn.PgError
}

// applyMessage is one attempt at a message: its batch (applyBatch), or the whole read the batch
// found the message calls for first (judge). The read waits on the daemon, so it is made outside
// the batch's transaction, and the attempt is then made again: the batch decides once more on the
// cursor the read left. A message is applied while a read is owed, and the cursor follows it; the
// read is retried at the next message once it is due. A starting point is not taken while a
// Dispatch process of an earlier build is in the fleet (startBlocked): the message is finished
// unapplied. A batch the database refuses for good (refusedForGood) is logged with its refusal,
// is not tried again, and makes one whole read of the project (readRefused).
func (in *Intake) applyMessage(ctx context.Context, attempt *messageAttempt) (outcome, error) {
	if attempt.refusal != nil {
		return in.readRefused(ctx, attempt)
	}
	result, owed, err := in.applyBatch(ctx, attempt)
	if refusal := refusedForGood(err); refusal != nil {
		message := attempt.message
		slog.Warn(refusedLine, "project", message.Project, "run", message.Run, "firstSeq", message.FirstSeq,
			"seq", message.Seq, "sqlstate", refusal.Code, "error", refusal.Message)
		attempt.refusal = refusal
		return in.readRefused(ctx, attempt)
	}
	if err != nil || owed == nil {
		return result, err
	}
	if blocked, err := in.startBlocked(ctx, owed.reason); err != nil || blocked {
		return outcomeEarlierBuild, err
	}
	// A read takes up to RequestTimeout: the message is still in hand.
	inProgress(attempt.msg)
	recorded, err := in.readWhole(ctx, owed.setting, owed.reason, &attempt.pending)
	if err != nil {
		return outcomeDone, err
	}
	if recorded {
		attempt.readMade = true
	}
	return outcomeAgain, nil
}

// readRefused is the one whole read of a message the database refused for good
// (ReadReasonRefused), which finishes the message whatever the read comes to: the read's rows then
// hold the daemon's state, the refused message's events among it, since the daemon commits each
// event before it publishes it. Nothing is read where the project has no setting, or a setting
// whose starting point its change queues (a cursor that does not follow it), or where the database
// refuses for good the read's record as well, or the project's position itself (a key it does not
// take): the message is finished all the same. Any other failure of the database is tried again in
// place, as every database step is.
func (in *Intake) readRefused(ctx context.Context, attempt *messageAttempt) (outcome, error) {
	setting, current, found, err := in.position(ctx, attempt.message.Project)
	if err != nil || !found || !current.follows(setting.StateURL) {
		if refusedForGood(err) != nil {
			err = nil
		}
		return outcomeDone, err
	}
	inProgress(attempt.msg)
	if _, err := in.readWhole(ctx, setting, ReadReasonRefused, &attempt.pending); err != nil && refusedForGood(err) == nil {
		return outcomeDone, err
	}
	return outcomeDone, nil
}

// refusedForGood is the Postgres error err carries where it is one no retry can write: SQLSTATE
// class 22, a data exception (a value its column's type does not take), or class 23, an
// integrity constraint violation. nil for every other error, a lost connection, a lock timeout
// or a statement a trigger raised among them, which a retry may write.
func refusedForGood(err error) *pgconn.PgError {
	var refusal *pgconn.PgError
	if errors.As(err, &refusal) && (strings.HasPrefix(refusal.Code, "22") || strings.HasPrefix(refusal.Code, "23")) {
		return refusal
	}
	return nil
}

// owedRead is the whole read a message calls for before it is applied (judge): the setting whose
// daemon is read, and why.
type owedRead struct {
	setting Settings
	reason  string
}

// untilDone runs step until it answers with no error, at once again for outcomeAgain, and after a
// wait for an error: a database step that fails is retried in place, so a runner never moves past
// a message it could not apply. While it waits it tells NATS the message is still in hand
// (InProgress), so the ack wait does not redeliver it. It reports false when ctx, the handling,
// ended first.
func (in *Intake) untilDone(ctx context.Context, msg *natsgo.Msg, project string, step func(context.Context) (outcome, error)) (outcome, bool) {
	wait := applyFirstRetry
	for ctx.Err() == nil {
		result, err := step(ctx)
		if err == nil {
			if result == outcomeAgain {
				continue
			}
			return result, true
		}
		if ctx.Err() != nil {
			break
		}
		slog.Warn("legion intake: a database step failed; trying it again", "project", project, "retry_in", wait, "error", err)
		inProgress(msg)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		wait = min(2*wait, applyLastRetry)
	}
	return outcomeDone, false
}

// inProgress tells NATS a message is still in hand, which restarts its ack wait. nil is work that
// holds no message.
func inProgress(msg *natsgo.Msg) {
	if msg == nil {
		return
	}
	if err := msg.InProgress(); err != nil {
		slog.Warn("legion intake: tell NATS a state message is still in hand", "subject", msg.Subject, "error", err)
	}
}

// reportEvery logs line at INFO for project, with attributes, at most once per dropLogEvery for
// each line and project. Any session may publish on a state subject, so the projects it remembers
// would grow with every name ever published: each entry older than dropLogEvery is deleted when an
// entry is written, and the map holds only the lines and projects logged in the last dropLogEvery.
func (in *Intake) reportEvery(line, project string, attributes ...any) {
	now := in.now()
	key := reportKey{line: line, project: project}
	in.mu.Lock()
	last, logged := in.reported[key]
	if logged && now.Sub(last) < dropLogEvery {
		in.mu.Unlock()
		return
	}
	maps.DeleteFunc(in.reported, func(_ reportKey, last time.Time) bool { return now.Sub(last) >= dropLogEvery })
	in.reported[key] = now
	in.mu.Unlock()
	slog.Info(line, append([]any{"project", project}, attributes...)...)
}

// logValue is *value as a log line's attribute, or "none" for nil, which a text handler would
// otherwise print as the pointer.
func logValue[T any](value *T) any {
	if value == nil {
		return "none"
	}
	return *value
}

// pruneHourly prunes legion_reads (PruneReads) after a batch, at most once per pruneEvery in this
// process.
func (in *Intake) pruneHourly(ctx context.Context) {
	now := in.now()
	in.mu.Lock()
	if !in.pruned.IsZero() && now.Sub(in.pruned) < pruneEvery {
		in.mu.Unlock()
		return
	}
	in.pruned = now
	in.mu.Unlock()
	if pruned, err := PruneReads(ctx, in.pool); err != nil {
		slog.Warn("legion intake: prune the Legion state reads", "error", err)
	} else if pruned > 0 {
		slog.Info("legion intake: pruned Legion state reads", "rows", pruned)
	}
}

// cursor is one project's legion_event_cursors row: how far its observations have followed its
// daemon (migration 20261118212431). exists is false for a project with no row.
type cursor struct {
	exists      bool
	stateURL    string
	run         *string
	lastSeq     *int64
	readOwed    bool
	readTriedAt *time.Time
	// stateID is the store the cursor's sequence is counted in, nil while a read is owed and no
	// good read set it; schemaVersion the store's schema version as the cursor last saw it, nil for
	// none.
	stateID       *string
	schemaVersion *int64
}

// retryDue reports whether an owed read may be tried at now: ReadRetryAfter after the last try.
func (c cursor) retryDue(now time.Time) bool {
	return c.readTriedAt == nil || !now.Before(c.readTriedAt.Add(ReadRetryAfter))
}

// follows reports whether the cursor follows the daemon at stateURL. A cursor that is missing, or
// that follows another daemon than the setting names, is no starting point.
func (c cursor) follows(stateURL string) bool {
	return c.exists && c.stateURL == stateURL
}

// cursorColumns is the legion_event_cursors select list scanCursor reads, in scan order.
const cursorColumns = `state_url, run, last_seq, read_owed, read_tried_at, state_id, schema_version`

// scanCursor decodes one cursorColumns row, or no row as a cursor that does not exist.
func scanCursor(row pgx.Row) (cursor, error) {
	current := cursor{exists: true}
	err := row.Scan(&current.stateURL, &current.run, &current.lastSeq, &current.readOwed, &current.readTriedAt,
		&current.stateID, &current.schemaVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return cursor{}, nil
	}
	return current, err
}

// position reads project's setting and its cursor in one statement, outside any transaction, for
// a decision whether to read the daemon whole (startingPoint, readRefused); found is false for a
// project with no setting.
func (in *Intake) position(ctx context.Context, project string) (setting Settings, current cursor, found bool, err error) {
	var cursorURL *string
	var readOwed *bool
	err = in.pool.QueryRow(ctx, `
		select s.state_url, c.state_url, c.run, c.last_seq, c.read_owed, c.read_tried_at, c.state_id, c.schema_version
		from legion_settings s
		left join legion_event_cursors c on c.project = s.project
		where s.project = $1
	`, project).Scan(&setting.StateURL, &cursorURL, &current.run, &current.lastSeq, &readOwed, &current.readTriedAt,
		&current.stateID, &current.schemaVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, cursor{}, false, nil
	}
	if err != nil {
		return Settings{}, cursor{}, false, fmt.Errorf("read where the Legion events of %s stand: %w", project, err)
	}
	setting.Project = project
	if cursorURL != nil {
		current.exists, current.stateURL, current.readOwed = true, *cursorURL, *readOwed
	}
	return setting, current, true, nil
}

// lockPosition reads project's setting `for share` and its cursor `for update` inside tx, which is
// how each write of the project's observations opens (applyBatch, recordWhole): a write of the
// setting (settingWritten) waits for the transaction, and so does each other write of the cursor.
// found is false for a project with no setting.
func lockPosition(ctx context.Context, tx pgx.Tx, project string) (stateURL string, current cursor, found bool, err error) {
	switch err := tx.QueryRow(ctx, `select state_url from legion_settings where project = $1 for share`, project).Scan(&stateURL); {
	case errors.Is(err, pgx.ErrNoRows):
		return "", cursor{}, false, nil
	case err != nil:
		return "", cursor{}, false, fmt.Errorf("read the Legion setting of %s: %w", project, err)
	}
	current, err = scanCursor(tx.QueryRow(ctx, `select `+cursorColumns+` from legion_event_cursors where project = $1 for update`, project))
	if err != nil {
		return "", cursor{}, false, fmt.Errorf("read where the Legion events of %s stand: %w", project, err)
	}
	return stateURL, current, true, nil
}

// startReason is the whole read a project's starting point calls for, at a take-over or a settings
// change: ReadReasonStart where the cursor is missing or follows another daemon, ReadReasonRetry
// where a read is owed and due, and none otherwise.
func startReason(current cursor, stateURL string, now time.Time) string {
	switch {
	case !current.follows(stateURL):
		return ReadReasonStart
	case current.readOwed && current.retryDue(now):
		return ReadReasonRetry
	}
	return ""
}

// verdict is what a message comes to on the position its project's transaction holds (judge).
type verdict int

const (
	// verdictTake is a message whose events above the position are applied, or held while more
	// of its transaction is to come (applyBatch).
	verdictTake verdict = iota
	// verdictRead is a message that calls for a whole read first, for the reason judge answers.
	verdictRead
	// verdictOtherStore is a message whose stateId is not the cursor's, once the one read it may
	// call for is made or not due: it is finished unapplied.
	verdictOtherStore
	// verdictBeyond is a message whose first sequence is still past the next one after a good
	// whole read it called for: it is finished unapplied.
	verdictBeyond
)

// judge is what a message calls for before its events are taken, on the project's cursor and the
// parts of a split transaction held for it:
//
//   - A project with no starting point is read whatever else holds (ReadReasonStart).
//   - A message whose stateId is not the cursor's is counted in another store, and nothing of it
//     is applied: it makes one whole read (ReadReasonStore) where no read was made in the last
//     ReadRetryAfter, so a daemon whose store was replaced is found again, and is dropped where
//     the read still holds another id, or where no read is due (verdictOtherStore).
//   - After a good whole read the message called for (readMade), a message whose first sequence
//     is still past the next one is no event of the daemon read: the daemon commits each event
//     before it publishes it, so the read holds every event published before it. It is dropped
//     (verdictBeyond). After a read that failed it is taken, as any message is while a read is
//     owed.
//   - Otherwise a message is read for once: a boot, a sequence or a part that does not follow the
//     position (stateMessage.unfollowed), or a read owed since one failed (ReadReasonRetry) - and
//     while a read is owed, never sooner than ReadRetryAfter after the last try, whatever the
//     message.
func judge(current cursor, held []part, stateURL string, message stateMessage, now time.Time, readMade bool) (verdict, string) {
	switch {
	case !current.follows(stateURL):
		return verdictRead, ReadReasonStart
	case current.stateID != nil && *current.stateID != message.StateID:
		if readMade || !current.retryDue(now) {
			return verdictOtherStore, ""
		}
		return verdictRead, ReadReasonStore
	case readMade:
		if !current.readOwed && current.lastSeq != nil && message.FirstSeq > *current.lastSeq+1 {
			return verdictBeyond, ""
		}
		return verdictTake, ""
	case current.readOwed && !current.retryDue(now):
		return verdictTake, ""
	}
	if reason := message.unfollowed(current, held); reason != "" {
		return verdictRead, reason
	}
	if current.readOwed {
		return verdictRead, ReadReasonRetry
	}
	return verdictTake, ""
}

// applyBatch takes the attempt's message in one transaction that holds the project's position
// (lockPosition, and the parts held for it, heldParts). A message that calls for a whole read
// first (judge, decided on the setting, the cursor and the parts the transaction holds) writes
// nothing and answers that read: the caller makes it outside the transaction and tries again. A
// message judge drops writes nothing either (outcomeOtherStore, outcomeBeyond), and neither does
// one whose project has no setting (outcomeDropped), one at or below the position
// (outcomeSkipped) or one with more of its transaction to come, which is held (outcomeHeld,
// holdPart).
//
// The last message of a transaction applies every event of it above the cursor, the held parts'
// and its own, each field at its last value in sequence order, written where it differs from
// legion_current's (recordEvents); one legion_reads row of kind `events` at the message's `at`
// with the bytes of every message of the transaction; the cursor moved to the message's sequence,
// run and store, and to the schema version of a boot among the events; the parts deleted; and the
// batch's announcement on StateChannel, which Postgres sends at the commit.
func (in *Intake) applyBatch(ctx context.Context, attempt *messageAttempt) (outcome, *owedRead, error) {
	message := attempt.message
	project := message.Project
	tx, err := in.pool.Begin(ctx)
	if err != nil {
		return outcomeDone, nil, fmt.Errorf("open the transaction of the Legion state events of %s: %w", project, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	stateURL, current, found, err := lockPosition(ctx, tx, project)
	if err != nil {
		return outcomeDone, nil, err
	}
	if !found {
		return outcomeDropped, nil, nil
	}
	attempt.cursor = current
	held, err := heldParts(ctx, tx, project)
	if err != nil {
		return outcomeDone, nil, err
	}
	switch verdict, reason := judge(current, held, stateURL, message, in.now(), attempt.readMade); verdict {
	case verdictRead:
		return outcomeDone, &owedRead{setting: Settings{Project: project, StateURL: stateURL}, reason: reason}, nil
	case verdictOtherStore:
		return outcomeOtherStore, nil, nil
	case verdictBeyond:
		return outcomeBeyond, nil, nil
	}
	if n := len(held); n > 0 {
		switch last := held[n-1]; {
		case message.Seq <= last.lastSeq:
			return outcomeSkipped, nil, nil
		case !message.continues(last):
			// Only while an owed read waits out ReadRetryAfter, when a message is taken whatever
			// it follows: the transaction the parts began cannot be completed, so they go.
			if _, err := tx.Exec(ctx, `delete from legion_event_parts where project = $1`, project); err != nil {
				return outcomeDone, nil, fmt.Errorf("drop the held state events of %s: %w", project, err)
			}
			held = nil
		}
	}
	// The transaction's events in sequence order: the held parts', then the message's own.
	var transaction []stateEvent
	bytes := attempt.bytes
	for _, one := range held {
		transaction = append(transaction, one.events...)
		bytes += one.bytes
	}
	transaction = append(transaction, message.Events...)
	events := eventsAfter(transaction, current.lastSeq)
	if len(events) == 0 {
		return outcomeSkipped, nil, nil
	}
	if message.More {
		if err := holdPart(ctx, tx, project, message, attempt.bytes); err != nil {
			return outcomeDone, nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return outcomeDone, nil, fmt.Errorf("commit the held state events of %s: %w", project, err)
		}
		return outcomeHeld, nil, nil
	}
	if _, err := recordEvents(ctx, tx, project, message.At, lastValues(events)); err != nil {
		return outcomeDone, nil, err
	}
	if _, err := tx.Exec(ctx, insertRead, project, message.At, true, nil, bytes, readKindEvents, nil); err != nil {
		return outcomeDone, nil, fmt.Errorf("store the Legion state events of %s: %w", project, err)
	}
	var run *string
	if message.Run != "" {
		run = &message.Run
	}
	var schemaVersion *int64
	for _, event := range events {
		if event.Kind == eventBoot {
			schemaVersion = &event.SchemaVersion
		}
	}
	if _, err := tx.Exec(ctx, `
		update legion_event_cursors
		set last_seq = $2, run = $3, state_id = $4, schema_version = coalesce($5, schema_version)
		where project = $1
	`, project, message.Seq, run, message.StateID, schemaVersion); err != nil {
		return outcomeDone, nil, fmt.Errorf("move where the Legion events of %s stand: %w", project, err)
	}
	if len(held) > 0 {
		if _, err := tx.Exec(ctx, `delete from legion_event_parts where project = $1`, project); err != nil {
			return outcomeDone, nil, fmt.Errorf("delete the held state events of %s: %w", project, err)
		}
	}
	if err := NotifyRead(ctx, tx, project); err != nil {
		return outcomeDone, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return outcomeDone, nil, fmt.Errorf("commit the Legion state events of %s: %w", project, err)
	}
	return outcomeDone, nil, nil
}

// part is one held message of a transaction the daemon split over several (legion_event_parts,
// migration 20261118212431): every message of the transaction but its last carries `more`, and
// none of its events is applied until the last arrives (applyBatch), so no observation holds a
// value of a transaction Dispatch has not read whole. A part is held only where it follows the
// cursor or the part before it (stateMessage.continues), so the parts of a project are one run of
// sequences under one run and one store. A whole read deletes them (recordWhole), and so does
// every write of the project's setting, which deletes the cursor they belong to (settingWritten).
type part struct {
	firstSeq, lastSeq int64
	run, stateID      string
	bytes             int
	events            []stateEvent
}

// heldParts reads project's held parts inside tx, which holds the project's cursor `for update`
// (lockPosition), in sequence order, each part's events decoded and observed as its message's
// were.
func heldParts(ctx context.Context, tx pgx.Tx, project string) ([]part, error) {
	rows, err := tx.Query(ctx, `
		select first_seq, last_seq, run, state_id, bytes, events from legion_event_parts
		where project = $1 order by first_seq
	`, project)
	if err != nil {
		return nil, fmt.Errorf("read the held state events of %s: %w", project, err)
	}
	held, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (part, error) {
		var one part
		var events string
		if err := row.Scan(&one.firstSeq, &one.lastSeq, &one.run, &one.stateID, &one.bytes, &events); err != nil {
			return part{}, err
		}
		one.events, err = decodeEvents([]byte(events))
		return one, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the held state events of %s: %w", project, err)
	}
	return held, nil
}

// holdPart holds message, a part of a split transaction, inside tx: its sequences, run, store,
// commit time, bytes and its events as the daemon sent them. A part held already, delivered
// again, is held once.
func holdPart(ctx context.Context, tx pgx.Tx, project string, message stateMessage, bytes int) error {
	if _, err := tx.Exec(ctx, `
		insert into legion_event_parts (project, first_seq, last_seq, run, state_id, at, bytes, events)
		values ($1, $2, $3, $4, $5, $6, $7, $8)
		on conflict (project, first_seq) do nothing
	`, project, message.FirstSeq, message.Seq, message.Run, message.StateID, message.At, bytes, string(message.RawEvents)); err != nil {
		return fmt.Errorf("hold the state events of %s: %w", project, err)
	}
	return nil
}

// recordEvents appends, inside a batch's transaction, a row for each observation whose value
// differs from legion_current's for its field, and answers how many it wrote. A first `null` writes
// nothing, as a whole read's does (recordObservations), and a field the batch does not name is left
// as it was: an event names what moved and nothing else.
//
// A row is observed at the batch's `at`, the daemon's commit time, or 1 µs after the field's newest
// row where that is later: a whole read stamps its rows with Dispatch's own clock, which can be
// past the daemon's, and legion_current moves only forward, so an event after the read must land
// after it to be the field's newest. The rule holds the other way round too: a whole read stamps
// all its rows at one moment, 1 µs after the project's newest row where that is later than the
// read's time (recordObservations), since the daemon's clock can as well be past Dispatch's. So
// whichever of the two Dispatch writes last is each field's newest, whatever the two clocks say.
// A batch's rows keep their own times: the batch is one daemon transaction, at one `at`.
func recordEvents(ctx context.Context, tx pgx.Tx, project string, at time.Time, observations []Observation) (int64, error) {
	issueKeys, fields, values := observationColumns(observations)
	tag, err := tx.Exec(ctx, `
		with moved as (
			select moved.issue_key, moved.field, moved.value::jsonb as value
			from unnest($2::text[], $3::text[], $4::text[]) as moved(issue_key, field, value)
		)
		insert into legion_observations (project, issue_key, field, value, observed_at)
		select $1, moved.issue_key, moved.field, moved.value,
			greatest($5::timestamptz, newest.observed_at + interval '1 microsecond')
		from moved
		left join legion_current newest
			on newest.project = $1 and newest.issue_key = moved.issue_key and newest.field = moved.field
		where coalesce(newest.value, 'null'::jsonb) is distinct from moved.value
	`, project, issueKeys, fields, values, at)
	if err != nil {
		return 0, fmt.Errorf("store the Legion observations of %s's state events: %w", project, err)
	}
	return tag.RowsAffected(), nil
}

// lastValues is each field the events name at its last value, in sequence order: one fact can
// write a field twice in one transaction (a slot released and granted again), and only the value
// the transaction committed is one the daemon's state ever held.
func lastValues(events []stateEvent) []Observation {
	type field struct{ issue, name string }
	at := map[field]int{}
	var values []Observation
	for _, event := range events {
		for _, observation := range event.observations {
			key := field{observation.IssueKey, observation.Field}
			if index, seen := at[key]; seen {
				values[index].Value = observation.Value
				continue
			}
			at[key] = len(values)
			values = append(values, observation)
		}
	}
	return values
}

// wholeRead is one whole read of a daemon's state as fetchWhole made it, held until it is recorded
// (recordWhole), so a record the database refuses is made again without reading the daemon again.
type wholeRead struct {
	stateURL string
	// stateSeq is the daemon.stateSeq the state serves, stateID its daemon.stateId and
	// schemaVersion its daemon.schemaVersion, each nil for none.
	stateSeq      *int64
	stateID       *string
	schemaVersion *int64
	// observations is what the state observes (State.Observations), recorded where observed is
	// true: the daemon answered with the setting's project's state, failed read or not.
	observations []Observation
	observed     bool
	bytes        int
	// at is Dispatch's clock when the daemon had answered: the read's time.
	at   time.Time
	took time.Duration
	// failure is why the read is a failed one, nil for a good read.
	failure error
}

// fetchWhole reads setting's daemon's state and judges it: a read the daemon did not answer, or
// answered with another project's state, or with a state that cannot be a starting point
// (ErrNoDispatchURL, ErrNoStateSeq, ErrStateSeqZero, ErrNoStateID, judged in that order), is a
// failed read. A daemon that runs no workflow is judged by its settings first, whatever stateSeq it
// serves: it commits no state event, so it serves stateSeq 0 too. The read's time is now's once
// the daemon has answered. A call presents no credential: `GET /legion/v1/state` authenticates
// nobody. Every Client shares the default transport, so a client per read still reuses the
// connection the last read left.
func fetchWhole(ctx context.Context, setting Settings, now func() time.Time) *wholeRead {
	started := time.Now()
	state, bytes, err := New(setting.StateURL).State(ctx)
	read := &wholeRead{stateURL: setting.StateURL, bytes: bytes, at: now().UTC(), took: time.Since(started)}
	if state.Daemon != nil {
		read.stateSeq, read.schemaVersion = state.Daemon.StateSeq, state.Daemon.SchemaVersion
		if state.Daemon.StateID != "" {
			read.stateID = &state.Daemon.StateID
		}
	}
	switch {
	case err != nil:
		read.failure = err
		return read
	case state.Daemon == nil || state.Daemon.Project != setting.Project:
		project := ""
		if state.Daemon != nil {
			project = state.Daemon.Project
		}
		read.failure = fmt.Errorf("%w: it serves %q, and the setting is %s's", ErrStateOfAnotherProject, project, setting.Project)
		return read
	}
	read.observations, read.observed = state.Observations(), true
	switch seq := read.stateSeq; {
	case JSONString(state.Settings["dispatch_url"]) == "":
		read.failure = ErrNoDispatchURL
	case seq == nil:
		read.failure = ErrNoStateSeq
	case *seq == 0:
		read.failure = ErrStateSeqZero
	case read.stateID == nil:
		read.failure = ErrNoStateID
	}
	return read
}

// ReadWhole reads setting's project's daemon state whole and records it (D9's whole read, made
// for a reason and never on a timer): the state through Client.State, its observations at one
// moment, Dispatch's now or just after the project's newest row (recordObservations), a
// legion_reads row of kind `read` at Dispatch's now naming reason, and the project's cursor set to
// the daemon's own daemon.stateSeq, daemon.stateId and daemon.schemaVersion, which takes the cursor
// back too after a database restore took the daemon's counter back. A read that fails, and one
// fetchWhole judges failed, records a failed legion_reads row naming why, its observations where
// the state is the project's own, and leaves the read owed. Each read is announced on StateChannel
// and logged at INFO (wholeReadLine).
//
// It answers an error only for a record the database refused, and records nothing when the
// setting no longer names setting.StateURL by the time it is written.
func ReadWhole(ctx context.Context, pool *store.Pool, setting Settings, reason string) error {
	read := fetchWhole(ctx, setting, time.Now)
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := finishWhole(ctx, pool, setting.Project, reason, read)
	return err
}

// readWhole is ReadWhole on the intake's clock, keeping the read it fetched in pending until it is
// recorded, so an attempt made again after a refused record records that read rather than reading
// the daemon again. It reports whether the read was recorded: not when the setting moved meanwhile.
func (in *Intake) readWhole(ctx context.Context, setting Settings, reason string, pending **wholeRead) (bool, error) {
	read := *pending
	if read == nil || read.stateURL != setting.StateURL {
		read = fetchWhole(ctx, setting, in.now)
		if err := ctx.Err(); err != nil {
			// The read was cut short by the stop, not by the daemon: nothing is recorded.
			return false, err
		}
		*pending = read
	}
	recorded, err := finishWhole(ctx, in.pool, setting.Project, reason, read)
	if err != nil {
		return false, err
	}
	*pending = nil
	return recorded, nil
}

// finishWhole records read (recordWhole, which announces it on StateChannel), then logs it. A
// record Postgres refused is recorded as a failed read instead, its failure `record: <why>` and
// none of its observations, so the last good observations stand and the read is owed rather than
// its project's runner held on a value the database will never take. An error is answered only
// when that record is refused too: the database itself is failing, and the caller tries the record
// again in place.
func finishWhole(ctx context.Context, pool *store.Pool, project, reason string, read *wholeRead) (bool, error) {
	recorded, written, err := recordWhole(ctx, pool, project, reason, read)
	if err != nil && ctx.Err() == nil {
		slog.Warn("legion intake: record a whole read; recording it as a failed read", "project", project, "reason", reason, "error", err)
		refused := *read
		refused.failure, refused.observed = fmt.Errorf("record: %w", err), false
		if recorded, written, err = recordWhole(ctx, pool, project, reason, &refused); err == nil {
			read = &refused
		}
	}
	if err != nil || !recorded {
		if err == nil {
			slog.Info("legion intake: the Legion setting changed during a whole read; it is not recorded", "project", project, "reason", reason)
		}
		return recorded, err
	}
	attributes := []any{"project", project, "reason", reason, "stateSeq", logValue(read.stateSeq),
		"stateId", logValue(read.stateID), "bytes", read.bytes, "duration", read.took, "ok", read.failure == nil, "rows", written}
	if read.failure != nil {
		attributes = append(attributes, "error", read.failure.Error())
	}
	slog.Info(wholeReadLine, attributes...)
	return true, nil
}

// recordWhole writes read in one transaction that holds the project's position (lockPosition):
// the step every whole read's record takes (recordRead: its observations where it observed them,
// its legion_reads row and its announcement), then the cursor, and it deletes the project's held
// parts (part), which the read holds or makes a gap of. A good read sets the cursor to its
// stateSeq, its stateId and its schemaVersion and clears the read owed; a failed one owes the read
// and keeps the cursor's where it follows the same daemon. Either way the cursor follows the
// setting's daemon from here, and read_tried_at is the read's time. The cursor's run is kept where
// it follows the same daemon: a whole read names no run, so the run is the newest applied
// message's, which is what tells a replayed boot from a daemon whose counter went back
// (stateMessage.unfollowed). recorded is false, with nothing written, when the setting no longer
// names the daemon read.
func recordWhole(ctx context.Context, pool *store.Pool, project, reason string, read *wholeRead) (recorded bool, written int64, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, 0, fmt.Errorf("open the transaction of the Legion state read of %s: %w", project, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	stateURL, current, found, err := lockPosition(ctx, tx, project)
	if err != nil {
		return false, 0, err
	}
	if !found || stateURL != read.stateURL {
		return false, 0, nil
	}
	if written, err = recordRead(ctx, tx, project, reason, read); err != nil {
		return false, 0, err
	}
	var lastSeq, schemaVersion *int64
	var run, stateID *string
	if current.follows(stateURL) {
		lastSeq, run, stateID, schemaVersion = current.lastSeq, current.run, current.stateID, current.schemaVersion
	}
	if read.failure == nil {
		lastSeq, stateID, schemaVersion = read.stateSeq, read.stateID, read.schemaVersion
	}
	if _, err := tx.Exec(ctx, `
		insert into legion_event_cursors (project, state_url, run, last_seq, read_owed, read_tried_at, state_id, schema_version)
		values ($1, $2, $3, $4, $5, $6, $7, $8)
		on conflict (project) do update set
			state_url = excluded.state_url, run = excluded.run, last_seq = excluded.last_seq,
			read_owed = excluded.read_owed, read_tried_at = excluded.read_tried_at,
			state_id = excluded.state_id, schema_version = excluded.schema_version
	`, project, stateURL, run, lastSeq, read.failure != nil, read.at, stateID, schemaVersion); err != nil {
		return false, 0, fmt.Errorf("set where the Legion events of %s stand: %w", project, err)
	}
	if _, err := tx.Exec(ctx, `delete from legion_event_parts where project = $1`, project); err != nil {
		return false, 0, fmt.Errorf("delete the held state events of %s: %w", project, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, 0, fmt.Errorf("commit the Legion state read of %s: %w", project, err)
	}
	return true, written, nil
}

// stateMessage is one message of a daemon's state lane (packages/contracts' legionStateMessage):
// the state events of one transaction of the daemon, or of one part of a transaction the daemon
// split over several (More), which share the transaction's project, run, store and commit time.
// Every member is read loosely: a member or an event kind a newer daemon adds is ignored, as every
// consumer of the bus ignores what it does not know.
type stateMessage struct {
	// Project is the Dispatch project key the daemon publishes for.
	Project string `json:"project"`
	// Run is the run id (a UUID) of the daemon process that wrote the events.
	Run string `json:"run"`
	// StateID is the id of the store the events are counted in (the state route's
	// daemon.stateId).
	StateID string `json:"stateId"`
	// FirstSeq and Seq are the first and the last event's state sequence.
	FirstSeq int64 `json:"firstSeq"`
	Seq      int64 `json:"seq"`
	// At is the transaction's commit time on the daemon's clock.
	At time.Time `json:"at"`
	// More is a part of a transaction whose next part is still to come.
	More bool `json:"more"`
	// RawEvents is the message's events as the daemon sent them, which a held part keeps
	// (holdPart), and Events the same decoded and observed (decodeEvents).
	RawEvents json.RawMessage `json:"events"`
	Events    []stateEvent    `json:"-"`
}

// continues reports whether the message is the next part of the transaction whose last part held
// is last: its first sequence the one after last's, under last's run and store.
func (m stateMessage) continues(last part) bool {
	return m.FirstSeq == last.lastSeq+1 && m.Run == last.run && m.StateID == last.stateID
}

// The kinds of state event (packages/daemon's state lane).
const (
	eventChange    = "change"
	eventAdmission = "admission"
	eventBoot      = "boot"
)

// stateEvent is one state event, in the members of its three kinds Dispatch reads: a change names
// its issue and the fields that moved; an admission event carries the whole admission block the
// state route serves, of which Dispatch keeps its own members (Admission); a boot carries the
// settings the state route serves and the schema version of the daemon's store once the boot's
// migrations ran.
type stateEvent struct {
	Seq           int64                      `json:"seq"`
	Kind          string                     `json:"kind"`
	Issue         string                     `json:"issue"`
	Changes       []stateChange              `json:"changes"`
	Admission     *Admission                 `json:"admission"`
	Settings      map[string]json.RawMessage `json:"settings"`
	SchemaVersion int64                      `json:"schemaVersion"`

	// observations is what the event observes, each value through its field's one conversion
	// (state.go's phaseValue and the rest), which decodeEvents fills.
	observations []Observation
}

// stateChange is one field of a change event: the field and its new value, the value the state
// route serves at that issue. A claim names its role. The old value is not read: legion_current
// holds what Dispatch observed last.
type stateChange struct {
	Field string          `json:"field"`
	Role  string          `json:"role"`
	New   json.RawMessage `json:"new"`
}

// The fields a change event names (packages/contracts' legionStateChange).
const (
	changePhase     = "phase"
	changeTree      = "tree"
	changeSlot      = "slot"
	changeWaitingOn = "waitingOn"
	changeClaim     = "claim"
)

// decodeStateMessage decodes one message's envelope, unless keying decoded it already, and its
// payload, and converts each event into the observations it makes (decodeEvents). A message with
// no project, no store, no sequence, sequences that do not run forwards or no time, or an event
// whose value is not of its field's type, does not decode.
func decodeStateMessage(envelope *contracts.Envelope, data []byte) (stateMessage, error) {
	if envelope == nil {
		var decoded contracts.Envelope
		if err := json.Unmarshal(data, &decoded); err != nil {
			return stateMessage{}, fmt.Errorf("decode the envelope: %w", err)
		}
		envelope = &decoded
	}
	var message stateMessage
	if err := json.Unmarshal([]byte(envelope.Payload), &message); err != nil {
		return stateMessage{}, fmt.Errorf("decode the payload: %w", err)
	}
	switch {
	case message.Project == "":
		return stateMessage{}, errors.New("the message names no project")
	case message.StateID == "":
		return stateMessage{}, errors.New("the message names no store (stateId)")
	case message.FirstSeq < 1 || message.Seq < message.FirstSeq:
		return stateMessage{}, fmt.Errorf("the message's sequences %d to %d do not run forwards from 1", message.FirstSeq, message.Seq)
	case message.At.IsZero():
		return stateMessage{}, errors.New("the message carries no time")
	}
	events, err := decodeEvents(message.RawEvents)
	if err != nil {
		return stateMessage{}, err
	}
	message.Events = events
	return message, nil
}

// decodeEvents decodes the JSON array of a message's events, in sequence order, and fills each
// event's observations (observe): a message's as it arrives, and a held part's as it was held.
func decodeEvents(raw json.RawMessage) ([]stateEvent, error) {
	var events []stateEvent
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &events); err != nil {
			return nil, fmt.Errorf("decode the events: %w", err)
		}
	}
	slices.SortStableFunc(events, func(a, b stateEvent) int { return cmp.Compare(a.Seq, b.Seq) })
	for index := range events {
		if err := events[index].observe(); err != nil {
			return nil, fmt.Errorf("event %d: %w", events[index].Seq, err)
		}
	}
	return events, nil
}

// observe fills the event's observations. An event of a kind this build does not know, and a
// change of a field it does not know, observes nothing.
func (e *stateEvent) observe() error {
	switch e.Kind {
	case eventChange:
		if e.Issue == "" {
			return errors.New("a change names no issue")
		}
		for _, change := range e.Changes {
			observation, known, err := changeObservation(e.Issue, change)
			if err != nil {
				return fmt.Errorf("%s of %s: %w", change.Field, e.Issue, err)
			}
			if known {
				e.observations = append(e.observations, observation)
			}
		}
	case eventAdmission:
		e.observations = []Observation{{IssueKey: AdmissionKey, Field: FieldAdmission, Value: admissionValue(e.Admission)}}
	case eventBoot:
		e.observations = []Observation{{IssueKey: AdmissionKey, Field: FieldSettings, Value: settingsValue(e.Settings)}}
	}
	return nil
}

// changeObservation is one change as the observation of its field, its new value decoded into the
// type the state document carries that field in and converted by the field's one conversion, so
// it is the value a whole read of the same state records. A `null` is the field the issue carries
// none of, which a whole read records as `null` too. known is false for a field this build does not
// observe.
func changeObservation(issue string, change stateChange) (observation Observation, known bool, err error) {
	observation.IssueKey = issue
	switch change.Field {
	case changePhase:
		var phase *string
		if err := decodeNew(change.New, &phase); err != nil {
			return Observation{}, false, err
		}
		observation.Field, observation.Value = FieldPhase, encodeValue(nil)
		if phase != nil {
			observation.Value = phaseValue(*phase)
		}
	case changeTree:
		var tree *string
		if err := decodeNew(change.New, &tree); err != nil {
			return Observation{}, false, err
		}
		observation.Field, observation.Value = FieldTree, treeValue(tree)
	case changeSlot:
		var index *int
		if err := decodeNew(change.New, &index); err != nil {
			return Observation{}, false, err
		}
		var slot *Slot
		if index != nil {
			slot = &Slot{Index: *index}
		}
		observation.Field, observation.Value = FieldSlot, slotValue(slot)
	case changeWaitingOn:
		observation.Field, observation.Value = FieldWaitingOn, waitingOnValue(change.New)
	case changeClaim:
		if change.Role == "" {
			return Observation{}, false, errors.New("a claim names no role")
		}
		var state *string
		if err := decodeNew(change.New, &state); err != nil {
			return Observation{}, false, err
		}
		claim := ""
		if state != nil {
			claim = *state
		}
		observation.Field, observation.Value = ClaimFieldPrefix+change.Role, claimValue(claim)
	default:
		return Observation{}, false, nil
	}
	return observation, true, nil
}

// decodeNew decodes a change's new value into target, leaving it nil where the value is absent.
func decodeNew(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, target)
}

// eventsAfter is each of events above lastSeq, all of them while the cursor holds no sequence.
func eventsAfter(events []stateEvent, lastSeq *int64) []stateEvent {
	if lastSeq == nil {
		return events
	}
	var after []stateEvent
	for _, event := range events {
		if event.Seq > *lastSeq {
			after = append(after, event)
		}
	}
	return after
}

// unfollowed is the whole read a message calls for because it does not follow the position
// current and the parts held for it stand at, or "" for one that does. The position is the last
// held part's sequence and run where parts are held, and the cursor's otherwise; a position that
// holds no sequence is followed by any message. A boot above the position whose schemaVersion is
// not the cursor's (where the cursor holds one) is ReadReasonSchema, and one that is not the next
// sequence ReadReasonBoot. A boot at or below the position is a replay of one the cursor already
// passed when its run is the run of the newest message applied or held, and otherwise a daemon
// whose sequence went back (a database restore), which is ReadReasonBoot too. A boot that follows
// the position with the cursor's schema version makes no read, whatever run the cursor holds. A
// message whose first sequence is past the next one is ReadReasonGap: events the stream no longer
// holds. So is a message above the held parts that does not continue them (stateMessage.continues):
// a part of another run or another store, or one that overlaps them, after which the transaction
// they began cannot be completed.
func (m stateMessage) unfollowed(current cursor, held []part) string {
	last, run := current.lastSeq, current.run
	if n := len(held); n > 0 {
		last, run = &held[n-1].lastSeq, &held[n-1].run
	}
	if last == nil {
		return ""
	}
	for _, event := range m.Events {
		if event.Kind != eventBoot {
			continue
		}
		switch {
		case event.Seq > *last && current.schemaVersion != nil && event.SchemaVersion != *current.schemaVersion:
			return ReadReasonSchema
		case event.Seq > *last && event.Seq != *last+1:
			return ReadReasonBoot
		case event.Seq <= *last && (run == nil || *run != m.Run):
			return ReadReasonBoot
		}
	}
	if m.FirstSeq > *last+1 {
		return ReadReasonGap
	}
	if n := len(held); n > 0 && m.Seq > *last && !m.continues(held[n-1]) {
		return ReadReasonGap
	}
	return ""
}
