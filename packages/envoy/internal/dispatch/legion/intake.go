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
// says how far the project's observations have followed its daemon (cursor).

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/delivery"
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
// is how many messages may wait on their runners at once: one project's messages run one at a
// time, so a burst of 256 is about four hours of INFRA's measured 53,000 events a day arriving at
// once.
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
// take-over, and never sooner than this after the last try.
const ReadRetryAfter = time.Minute

// The reasons a whole read is made, which legion_reads.reason records and the whole read's log
// line names. There are no others: no timer reads a daemon.
const (
	// ReadReasonStart is a project with a setting and no starting point: no cursor, or one that
	// follows another daemon than the setting names. It is read at the intake's take-over (which
	// covers the durable's first creation), on a settings change, and at the project's next
	// message if neither has read it yet.
	ReadReasonStart = "start"
	// ReadReasonBoot is a boot event that does not follow the cursor: its sequence is not the next
	// one, or it is at or below the cursor under another run (a daemon whose counter went back).
	ReadReasonBoot = "boot"
	// ReadReasonMigrated is a boot event that applied a migration, which may have rewritten what
	// Dispatch observes outside the writes that publish events.
	ReadReasonMigrated = "migrated"
	// ReadReasonGap is a message whose first sequence is past the next one: events the stream no
	// longer holds.
	ReadReasonGap = "gap"
	// ReadReasonRetry is a read owed since the last one failed.
	ReadReasonRetry = "retry"
)

// Why a whole read the daemon answered is a failed read all the same. Each leaves the read owed;
// the last three record the read's observations, since the state is the project's own.
var (
	// ErrStateOfAnotherProject is a state whose daemon.project is not the setting's project.
	ErrStateOfAnotherProject = errors.New("the daemon serves another project's state")
	// ErrNoStateSeq is a state with no daemon.stateSeq: a daemon without state events.
	ErrNoStateSeq = errors.New("the daemon serves no daemon.stateSeq, so it publishes no state events")
	// ErrStateSeqZero is a state whose daemon.stateSeq is 0: a daemon that has committed no state
	// event, which a daemon with a workflow never serves, since its boot event commits first.
	ErrStateSeqZero = errors.New("the daemon serves daemon.stateSeq 0: it has published no state event")
	// ErrNoDispatchURL is a state whose settings carry no dispatch_url: a daemon that runs no
	// workflow now, so it publishes no state events, whatever it published before.
	ErrNoDispatchURL = errors.New("the daemon's settings carry no dispatch_url: it runs no workflow, so it publishes no state events")
)

// wholeReadLine is the log line of every whole read, at INFO.
const wholeReadLine = "legion intake: whole read"

// droppedLine is the log line of a message for a project with no Legion setting, at INFO, once
// per project per dropLogEvery.
const droppedLine = "legion intake: a state message for a project with no Legion setting is dropped"

// dropLogEvery bounds how often one project's dropped messages are logged, and pruneEvery how
// often one process prunes legion_reads (PruneReads).
const (
	dropLogEvery = time.Hour
	pruneEvery   = time.Hour
)

// A database step that fails is tried again in place, after applyFirstRetry and twice as long
// after each failure after it, up to applyLastRetry, for as long as the handling lasts. Vars so a
// test can shrink them.
var (
	applyFirstRetry = time.Second
	applyLastRetry  = 30 * time.Second
)

// startingPointsWithin bounds the settings read a take-over's callback makes, on the run loop of
// the durable (delivery.HeldDurable), before it queues each project's starting point.
const startingPointsWithin = 10 * time.Second

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
	// dropped is when each project's dropped message was last logged (droppedLine).
	dropped map[string]time.Time
	// pruned is when this process last pruned legion_reads.
	pruned time.Time
}

// NewIntake builds the intake on natsClient, a client opened for it alone (a bus.Client holds one
// JetStream subscription), holding its durable under a hold and a consumer lease it takes on leases
// (ReserveLeases), and reading the settings again on each signal of settingsWake: in the server,
// the shared listener's wake on SettingsChannel.
func NewIntake(natsClient *bus.Client, pool *store.Pool, leases delivery.DurableLeases, settingsWake <-chan struct{}) *Intake {
	in := &Intake{pool: pool, settingsWake: settingsWake, now: time.Now, dropped: map[string]time.Time{}}
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

// SettingsChanged is what a write of a Legion setting wakes (SettingsChannel): every configured
// project's starting point is checked on its runner (startingPoint), so a project whose cursor the
// write deleted (PutSettings, DeleteSettings) is read whole once, and every other project, whose
// cursor stands, is not read at all. A process that does not hold the durable checks nothing: the
// process that holds it heard the same notification.
func (in *Intake) SettingsChanged(ctx context.Context) {
	in.queueStartingPoints(ctx)
}

// takenOver is the durable's take-over callback (delivery.NewHeldDurable): once a take-over has
// bound the durable, its first creation included, every configured project's starting point is
// checked on its runner.
func (in *Intake) takenOver() {
	ctx, cancel := context.WithTimeout(context.Background(), startingPointsWithin)
	defer cancel()
	in.queueStartingPoints(ctx)
}

// queueStartingPoints queues startingPoint on the runner of each project that has a setting. A
// settings read that fails queues nothing: a project with no starting point is read at its next
// message all the same, and the listener's wake fires again once it listens again.
func (in *Intake) queueStartingPoints(ctx context.Context) {
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

// startingPoint reads project's daemon whole where its observations have no starting point
// (ReadReasonStart), or where a read is owed and due (ReadReasonRetry), and reads nothing
// otherwise. It runs on the project's runner; a database step that fails is tried again in place
// until it succeeds or the handling ends.
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
	// outcomeSkipped is a message at or below the cursor: nothing is written.
	outcomeSkipped
	// outcomeDropped is a message for a project with no Legion setting.
	outcomeDropped
	// outcomeAgain is an attempt the project's setting moved under: it is made again at once.
	outcomeAgain
)

// deliver applies one state message on its project's runner and acknowledges it (applyMessage).
// A message that does not decode is acknowledged and logged: the next one of its project is then
// a gap, which a whole read covers. A message the runner could not apply before the handling
// ended is handed back rather than acknowledged, so the next holder applies it.
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
	switch result {
	case outcomeDone:
		if err := NotifyRead(ctx, in.pool, message.Project); err != nil {
			slog.Warn("legion intake: announce a batch of state events", "project", message.Project, "error", err)
		}
		in.pruneHourly(ctx)
	case outcomeDropped:
		in.reportDropped(message.Project)
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
}

// applyMessage is one attempt at a message: the whole read it calls for first, if any
// (messageReason), then the batch (applyBatch). A message is applied while a read is owed, and the
// cursor follows it; the read is retried at the next message once it is due.
func (in *Intake) applyMessage(ctx context.Context, attempt *messageAttempt) (outcome, error) {
	project := attempt.message.Project
	setting, current, found, err := in.position(ctx, project)
	if err != nil {
		return outcomeDone, err
	}
	if !found {
		return outcomeDropped, nil
	}
	if reason := messageReason(current, setting.StateURL, attempt.message, in.now(), attempt.readMade); reason != "" {
		// A read takes up to RequestTimeout: the message is still in hand.
		inProgress(attempt.msg)
		recorded, err := in.readWhole(ctx, setting, reason, &attempt.pending)
		if err != nil {
			return outcomeDone, err
		}
		if !recorded {
			return outcomeAgain, nil
		}
		attempt.readMade = true
	}
	return in.applyBatch(ctx, project, attempt.message, attempt.bytes)
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

// reportDropped logs a message for project, which has no Legion setting, once per dropLogEvery.
func (in *Intake) reportDropped(project string) {
	now := in.now()
	in.mu.Lock()
	last, logged := in.dropped[project]
	if logged && now.Sub(last) < dropLogEvery {
		in.mu.Unlock()
		return
	}
	in.dropped[project] = now
	in.mu.Unlock()
	slog.Info(droppedLine, "project", project)
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
}

// retryDue reports whether an owed read may be tried at now: ReadRetryAfter after the last try.
func (c cursor) retryDue(now time.Time) bool {
	return c.readTriedAt == nil || !now.Before(c.readTriedAt.Add(ReadRetryAfter))
}

// cursorColumns is the legion_event_cursors select list scanCursor reads, in scan order.
const cursorColumns = `state_url, run, last_seq, read_owed, read_tried_at`

// scanCursor decodes one cursorColumns row, or no row as a cursor that does not exist.
func scanCursor(row pgx.Row) (cursor, error) {
	current := cursor{exists: true}
	err := row.Scan(&current.stateURL, &current.run, &current.lastSeq, &current.readOwed, &current.readTriedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return cursor{}, nil
	}
	return current, err
}

// position reads project's setting and its cursor in one statement, outside any transaction, for
// the decision whether to read the daemon whole first; found is false for a project with no
// setting.
func (in *Intake) position(ctx context.Context, project string) (setting Settings, current cursor, found bool, err error) {
	var cursorURL *string
	var readOwed *bool
	err = in.pool.QueryRow(ctx, `
		select s.state_url, c.state_url, c.run, c.last_seq, c.read_owed, c.read_tried_at
		from legion_settings s
		left join legion_event_cursors c on c.project = s.project
		where s.project = $1
	`, project).Scan(&setting.StateURL, &cursorURL, &current.run, &current.lastSeq, &readOwed, &current.readTriedAt)
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

// startReason is the whole read a project's starting point calls for, at a take-over or a settings
// change: ReadReasonStart where the cursor is missing or follows another daemon, ReadReasonRetry
// where a read is owed and due, and none otherwise.
func startReason(current cursor, stateURL string, now time.Time) string {
	switch {
	case !current.exists || current.stateURL != stateURL:
		return ReadReasonStart
	case current.readOwed && current.retryDue(now):
		return ReadReasonRetry
	}
	return ""
}

// messageReason is the whole read a message calls for before it is applied. A project with no
// starting point is read whatever else holds (ReadReasonStart). Otherwise a message is read for
// once (readMade): a boot or a sequence that does not follow it (stateMessage.unfollowed), or a
// read owed since one failed (ReadReasonRetry) - and while a read is owed, never sooner than
// ReadRetryAfter after the last try, whatever the message.
func messageReason(current cursor, stateURL string, message stateMessage, now time.Time, readMade bool) string {
	switch {
	case !current.exists || current.stateURL != stateURL:
		return ReadReasonStart
	case readMade || (current.readOwed && !current.retryDue(now)):
		return ""
	}
	if reason := message.unfollowed(current); reason != "" {
		return reason
	}
	if current.readOwed {
		return ReadReasonRetry
	}
	return ""
}

// applyBatch applies message in one transaction, the setting held `for share` and the cursor
// `for update`: the events above the cursor, each field at its last value in sequence order,
// written where it differs from legion_current's (recordEvents); one legion_reads row of kind
// `events` at the message's `at`; and the cursor moved to the message's sequence and run. A
// message at or below the cursor writes nothing (outcomeSkipped), and one whose project has no
// setting nothing either (outcomeDropped). A cursor that is gone, or follows another daemon, is a
// setting written since the message's read was decided: the attempt is made again (outcomeAgain).
func (in *Intake) applyBatch(ctx context.Context, project string, message stateMessage, bytes int) (outcome, error) {
	tx, err := in.pool.Begin(ctx)
	if err != nil {
		return outcomeDone, fmt.Errorf("open the transaction of the Legion state events of %s: %w", project, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var stateURL string
	switch err := tx.QueryRow(ctx, `select state_url from legion_settings where project = $1 for share`, project).Scan(&stateURL); {
	case errors.Is(err, pgx.ErrNoRows):
		return outcomeDropped, nil
	case err != nil:
		return outcomeDone, fmt.Errorf("read the Legion setting of %s: %w", project, err)
	}
	current, err := scanCursor(tx.QueryRow(ctx, `select `+cursorColumns+` from legion_event_cursors where project = $1 for update`, project))
	if err != nil {
		return outcomeDone, fmt.Errorf("read where the Legion events of %s stand: %w", project, err)
	}
	if !current.exists || current.stateURL != stateURL {
		return outcomeAgain, nil
	}
	events := message.eventsAfter(current.lastSeq)
	if len(events) == 0 {
		return outcomeSkipped, nil
	}
	if _, err := recordEvents(ctx, tx, project, message.At, lastValues(events)); err != nil {
		return outcomeDone, err
	}
	if _, err := tx.Exec(ctx, insertRead, project, message.At, true, nil, bytes, readKindEvents, nil); err != nil {
		return outcomeDone, fmt.Errorf("store the Legion state events of %s: %w", project, err)
	}
	var run *string
	if message.Run != "" {
		run = &message.Run
	}
	if _, err := tx.Exec(ctx, `update legion_event_cursors set last_seq = $2, run = $3 where project = $1`,
		project, message.Seq, run); err != nil {
		return outcomeDone, fmt.Errorf("move where the Legion events of %s stand: %w", project, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return outcomeDone, fmt.Errorf("commit the Legion state events of %s: %w", project, err)
	}
	return outcomeDone, nil
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
// each row 1 µs after its field's newest where that is later than the read's time
// (recordObservations), since the daemon's clock can as well be past Dispatch's. So whichever of
// the two Dispatch writes last is each field's newest, whatever the two clocks say.
func recordEvents(ctx context.Context, tx pgx.Tx, project string, at time.Time, observations []Observation) (int64, error) {
	issueKeys := make([]string, len(observations))
	fields := make([]string, len(observations))
	values := make([]string, len(observations))
	for index, observation := range observations {
		issueKeys[index], fields[index], values[index] = observation.IssueKey, observation.Field, string(observation.Value)
	}
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
	state    State
	bytes    int
	// at is Dispatch's clock when the daemon had answered: the read's rows' time.
	at   time.Time
	took time.Duration
	// failure is why the read is a failed one, nil for a good read.
	failure error
	// observed is whether the read's observations are recorded: the daemon answered with the
	// setting's project's state, failed read or not.
	observed bool
}

// stateSeq is the state sequence the read's state serves, nil for none.
func (r *wholeRead) stateSeq() *int64 {
	if r.state.Daemon == nil {
		return nil
	}
	return r.state.Daemon.StateSeq
}

// fetchWhole reads setting's daemon's state and judges it: a read the daemon did not answer, or
// answered with another project's state, or with a state that cannot be a starting point
// (ErrNoStateSeq, ErrStateSeqZero, ErrNoDispatchURL), is a failed read. The read's time is now's
// once the daemon has answered. A call presents no credential: `GET /legion/v1/state`
// authenticates nobody. Every Client shares the default transport, so a client per read still
// reuses the connection the last read left.
func fetchWhole(ctx context.Context, setting Settings, now func() time.Time) *wholeRead {
	started := time.Now()
	state, bytes, err := New(setting.StateURL).State(ctx)
	read := &wholeRead{stateURL: setting.StateURL, state: state, bytes: bytes, at: now().UTC(), took: time.Since(started)}
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
	read.observed = true
	switch seq := state.Daemon.StateSeq; {
	case seq == nil:
		read.failure = ErrNoStateSeq
	case *seq == 0:
		read.failure = ErrStateSeqZero
	case JSONString(state.Settings["dispatch_url"]) == "":
		read.failure = ErrNoDispatchURL
	}
	return read
}

// ReadWhole reads setting's project's daemon state whole and records it (D9's whole read, the
// sampler's read without its loop): the state through Client.State, its observations at
// Dispatch's now or just after each field's newest row (recordObservations, as RecordRead writes
// them), a legion_reads row of kind `read` at Dispatch's now naming reason, and the project's
// cursor set to the daemon's own daemon.stateSeq, which takes the cursor back too after a database
// restore took the daemon's counter back. A read that fails, and one fetchWhole judges failed,
// records a failed legion_reads row naming why, its observations where the state is the project's
// own, and leaves the read owed. Each read is announced on StateChannel and logged at INFO
// (wholeReadLine).
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

// finishWhole records read (recordWhole), then announces it on StateChannel and logs it. A record
// Postgres refused is recorded as a failed read instead, its failure `record: <why>` and none of
// its observations, as the sampler's read did, so the last good observations stand and the read
// is owed rather than its project's runner held on a value the database will never take. An error
// is answered only when that record is refused too: the database itself is failing, and the
// caller tries the record again in place.
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
	if err := NotifyRead(ctx, pool, project); err != nil {
		slog.Warn("legion intake: announce a whole read", "project", project, "error", err)
	}
	attributes := []any{"project", project, "reason", reason, "stateSeq", read.stateSeq(), "bytes", read.bytes,
		"duration", read.took, "ok", read.failure == nil, "rows", written}
	if read.failure != nil {
		attributes = append(attributes, "error", read.failure.Error())
	}
	slog.Info(wholeReadLine, attributes...)
	return true, nil
}

// recordWhole writes read in one transaction, the setting held `for share` and the cursor `for
// update`: the read's observations where it observed them, its legion_reads row, and the cursor.
// A good read sets the cursor to its stateSeq and clears the read owed; a failed one owes the read
// and keeps the cursor's sequence where it follows the same daemon. Either way the cursor follows
// the setting's daemon from here, and read_tried_at is the read's time. The cursor's run is kept
// where it follows the same daemon: a whole read names no run, so the run is the newest applied
// message's, which is what tells a replayed boot from a daemon whose counter went back
// (stateMessage.unfollowed). recorded is false, with nothing written, when the setting no longer
// names the daemon read.
func recordWhole(ctx context.Context, pool *store.Pool, project, reason string, read *wholeRead) (recorded bool, written int64, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, 0, fmt.Errorf("open the transaction of the Legion state read of %s: %w", project, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var stateURL string
	switch err := tx.QueryRow(ctx, `select state_url from legion_settings where project = $1 for share`, project).Scan(&stateURL); {
	case errors.Is(err, pgx.ErrNoRows):
		return false, 0, nil
	case err != nil:
		return false, 0, fmt.Errorf("read the Legion setting of %s: %w", project, err)
	}
	if stateURL != read.stateURL {
		return false, 0, nil
	}
	current, err := scanCursor(tx.QueryRow(ctx, `select `+cursorColumns+` from legion_event_cursors where project = $1 for update`, project))
	if err != nil {
		return false, 0, fmt.Errorf("read where the Legion events of %s stand: %w", project, err)
	}
	if read.observed {
		if written, err = recordObservations(ctx, tx, project, read.at, read.state.Observations()); err != nil {
			return false, 0, err
		}
	}
	var failure *string
	if read.failure != nil {
		text := read.failure.Error()
		failure = &text
	}
	if _, err := tx.Exec(ctx, insertRead, project, read.at, read.failure == nil, failure, read.bytes, readKindRead, reason); err != nil {
		return false, 0, fmt.Errorf("store the Legion state read of %s: %w", project, err)
	}
	var lastSeq *int64
	var run *string
	if current.exists && current.stateURL == stateURL {
		lastSeq, run = current.lastSeq, current.run
	}
	if read.failure == nil {
		lastSeq = read.stateSeq()
	}
	if _, err := tx.Exec(ctx, `
		insert into legion_event_cursors (project, state_url, run, last_seq, read_owed, read_tried_at)
		values ($1, $2, $3, $4, $5, $6)
		on conflict (project) do update set
			state_url = excluded.state_url, run = excluded.run, last_seq = excluded.last_seq,
			read_owed = excluded.read_owed, read_tried_at = excluded.read_tried_at
	`, project, stateURL, run, lastSeq, read.failure != nil, read.at); err != nil {
		return false, 0, fmt.Errorf("set where the Legion events of %s stand: %w", project, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, 0, fmt.Errorf("commit the Legion state read of %s: %w", project, err)
	}
	return true, written, nil
}

// stateMessage is one message of a daemon's state lane (packages/contracts' legionStateMessage):
// every state event one transaction of the daemon committed, which share the transaction's
// project, run and commit time. Every member is read loosely: a member or an event kind a newer
// daemon adds is ignored, as every consumer of the bus ignores what it does not know.
type stateMessage struct {
	// Project is the Dispatch project key the daemon publishes for.
	Project string `json:"project"`
	// Run is the run id (a UUID) of the daemon process that wrote the events.
	Run string `json:"run"`
	// FirstSeq and Seq are the first and the last event's state sequence.
	FirstSeq int64 `json:"firstSeq"`
	Seq      int64 `json:"seq"`
	// At is the transaction's commit time on the daemon's clock.
	At     time.Time    `json:"at"`
	Events []stateEvent `json:"events"`
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
// settings the state route serves and whether the boot applied a migration.
type stateEvent struct {
	Seq       int64                      `json:"seq"`
	Kind      string                     `json:"kind"`
	Issue     string                     `json:"issue"`
	Changes   []stateChange              `json:"changes"`
	Admission *Admission                 `json:"admission"`
	Settings  map[string]json.RawMessage `json:"settings"`
	Migrated  bool                       `json:"migrated"`

	// observations is what the event observes, each value through its field's one conversion
	// (state.go's phaseValue and the rest), which decodeStateMessage fills.
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
// payload, and converts each event into the observations it makes. A message with no project, no
// sequence, sequences that do not run forwards or no time, or an event whose value is not of its
// field's type, does not decode.
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
	case message.FirstSeq < 1 || message.Seq < message.FirstSeq:
		return stateMessage{}, fmt.Errorf("the message's sequences %d to %d do not run forwards from 1", message.FirstSeq, message.Seq)
	case message.At.IsZero():
		return stateMessage{}, errors.New("the message carries no time")
	}
	slices.SortStableFunc(message.Events, func(a, b stateEvent) int { return cmp.Compare(a.Seq, b.Seq) })
	for index := range message.Events {
		if err := message.Events[index].observe(); err != nil {
			return stateMessage{}, fmt.Errorf("event %d: %w", message.Events[index].Seq, err)
		}
	}
	return message, nil
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

// eventsAfter is the message's events above lastSeq, all of them while the cursor holds no
// sequence.
func (m stateMessage) eventsAfter(lastSeq *int64) []stateEvent {
	if lastSeq == nil {
		return m.Events
	}
	var after []stateEvent
	for _, event := range m.Events {
		if event.Seq > *lastSeq {
			after = append(after, event)
		}
	}
	return after
}

// unfollowed is the whole read a message calls for because it does not follow current, or "" for
// one that does. A cursor that holds no sequence is followed by any message. A boot above the
// cursor that applied a migration is ReadReasonMigrated, and one that is not the next sequence
// ReadReasonBoot. A boot at or below the cursor is a replay of one the cursor already passed when
// its run is the run of the newest message the cursor applied, and otherwise a daemon whose
// sequence went back (a database restore), which is ReadReasonBoot too. A message whose first
// sequence is past the next one is ReadReasonGap: events the stream no longer holds.
func (m stateMessage) unfollowed(current cursor) string {
	if current.lastSeq == nil {
		return ""
	}
	last := *current.lastSeq
	for _, event := range m.Events {
		if event.Kind != eventBoot {
			continue
		}
		switch {
		case event.Seq > last && event.Migrated:
			return ReadReasonMigrated
		case event.Seq > last && event.Seq != last+1:
			return ReadReasonBoot
		case event.Seq <= last && (current.run == nil || *current.run != m.Run):
			return ReadReasonBoot
		}
	}
	if m.FirstSeq > last+1 {
		return ReadReasonGap
	}
	return ""
}
