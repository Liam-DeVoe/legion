package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/duty"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/retry"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/logtest"
	"github.com/sjawhar/envoy/internal/testnats"
)

// process is one envoy-dispatch process's outbox under test: its own pool, its own events broker
// and its own NATS client, which counts every envelope it sends.
type process struct {
	name    string
	outbox  *Outbox
	store   *store.Store
	broker  *events.Broker
	sending *sendCounter
	stop    func()
}

// sendCounter counts the envelopes every process of a test sends, by event and subject.
type sendCounter struct {
	mu    sync.Mutex
	sends map[string]int
}

func (c *sendCounter) add(item contracts.Envelope) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sends == nil {
		c.sends = map[string]int{}
	}
	c.sends[item.SourceEventID+" "+item.Topic]++
}

// of is how many envelopes of event were sent, and the most sent to any one subject.
func (c *sendCounter) of(event int64) (total, most int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, count := range c.sends {
		if strings.HasPrefix(key, fmt.Sprint(event)+" ") {
			total += count
			most = max(most, count)
		}
	}
	return total, most
}

// countingClient is a bus client whose windows count every envelope they send.
type countingClient struct {
	*bus.Client
	counter *sendCounter
}

func (c countingClient) WindowUntil(deadline func() time.Time) bus.Sender {
	return countingSender{Sender: c.Client.WindowUntil(deadline), counter: c.counter}
}

type countingSender struct {
	bus.Sender
	counter *sendCounter
}

func (s countingSender) PublishAsync(item contracts.Envelope) <-chan error {
	s.counter.add(item)
	return s.Sender.PublishAsync(item)
}

// startProcess runs an outbox on database's own outbox pool, publishing through a client of its own
// connected to url, until the test ends or stop is called.
func startProcess(t *testing.T, name string, database *store.Store, url string, counter *sendCounter) *process {
	t.Helper()
	client, err := bus.ConnectOwningStream([]string{url})
	if err != nil {
		t.Fatalf("connect %s's NATS client: %v", name, err)
	}
	t.Cleanup(client.Close)
	pool, err := database.Pool.Outbox()
	if err != nil {
		t.Fatalf("open %s's outbox pool: %v", name, err)
	}
	p := &process{name: name, store: database, broker: events.NewBroker(), sending: counter}
	p.outbox = New(Deps{
		Store:     &store.Store{Pool: pool},
		Publisher: countingClient{Client: client, counter: counter},
		Broker:    p.broker,
		Listen:    database.Pool.Listener,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.outbox.Run(ctx); close(done) }()
	p.stop = sync.OnceFunc(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("%s's outbox did not stop", name)
		}
	})
	t.Cleanup(p.stop)
	return p
}

// publishing is the processes that publish now.
func publishing(processes ...*process) []string {
	var names []string
	for _, p := range processes {
		if p.outbox.Publishing() {
			names = append(names, p.name)
		}
	}
	return names
}

// waitForOnePublisher waits until exactly one of processes publishes, and returns it.
func waitForOnePublisher(t *testing.T, within time.Duration, processes ...*process) *process {
	t.Helper()
	var holder *process
	waitFor(t, within, "exactly one outbox publishing", func() bool {
		holder = nil
		count := 0
		for _, p := range processes {
			if p.outbox.Publishing() {
				holder = p
				count++
			}
		}
		return count == 1
	})
	return holder
}

// writeMessage commits one message event on issue T-1 through p's store and broker, as a request to
// p would, and returns it.
func writeMessage(t *testing.T, p *process, body string) model.Event {
	t.Helper()
	event := appendEvent(t, p.store, p.broker, model.Event{
		IssueKey: new("T-1"), Type: "message.created", Actor: model.Actor{Kind: "user", ID: "alice"},
		Payload: model.Message{ID: "5a660655-04ad-4ce0-8a9b-93dd03c412b7", IssueKey: new("T-1"), Body: body},
	})
	p.broker.Publish(event)
	return event
}

// waitForPublished waits until event is marked published and was sent to every subject once.
func waitForPublished(t *testing.T, within time.Duration, database *store.Store, counter *sendCounter, event model.Event) {
	t.Helper()
	waitFor(t, within, fmt.Sprintf("event %d published", event.ID), func() bool {
		return publishedAt(t, database, event.ID) != nil
	})
	if total, most := counter.of(event.ID); total == 0 || most != 1 {
		t.Fatalf("event %d: %d envelopes sent, at most %d to one subject, want each of its subjects sent once", event.ID, total, most)
	}
}

// outboxHangGuard bounds every wait the row below makes. It is a hang guard, not a budget: the row
// asserts what the outbox did and in what order, never how fast it did it, so load only makes the
// same reading take longer to reach, and a wait that reaches this bound means the thing it waited
// for never happened at all.
const outboxHangGuard = 60 * time.Second

// backOffDue is how long the back-off of the row below is due in, and the one bound the row puts
// on the outbox's own work: the insert's notification must reach the holder, and the scan it
// wakes must read next_attempt_at, inside it. A scan later than that finds the back-off already
// due and publishes it there and then, arming no wait at all, which the row catches where it
// requires exactly one wait armed between the insert and the publish - so it still fails on what
// happened rather than on a clock, but it would be failing for the machine's slowness and not
// the outbox's, which is why this is ten seconds against the single-digit milliseconds that work
// takes on an idle pod.
const backOffDue = 10 * time.Second

// The words of the log lines heldScans reads: a renewal of a hold the database left unanswered
// (duty.Hold's renew), a Postgres listener whose connection is gone and that dials again
// (pglisten.Listener's Run), the outbox taking the publisher hold (publish), and the prefix of
// every line the outbox writes itself, whose errors are a statement that failed.
const (
	renewFailedLine = "renew hold failed"
	redialLine      = "a Postgres listener has no connection; redialing"
	tookHoldLine    = "dispatch outbox: took the publisher hold"
	outboxLine      = "dispatch outbox: "
)

// witnessPeriod is how often the row's witness (databaseWitness) asks the database a question,
// and witnessBound how long it waits for each answer: half the one second a renewal of the
// publisher hold waits, so a database that leaves a renewal unanswered leaves the witness's
// questions unanswered too.
const (
	witnessPeriod = 50 * time.Millisecond
	witnessBound  = 500 * time.Millisecond
)

// witnessQuestion asks what an idle outbox's own statements need of the database: an answer; a
// commit written to disk, since txid_current gives the transaction an id and the commit of a
// transaction with one waits for its flush, as a renewal's does; the publisher hold's row free of a
// lock that holds its renewals back (for key share waits on a for update and on a delete, and never
// on a renewal's update); and the tables the claim, the fallback pass and the retry timer read.
const witnessQuestion = `select txid_current(),
	(select 1 from duty_holds where name = $1 for key share),
	exists (select from events, issues, artifacts, comment_fallback_copies where false)`

// failureWindow is a span in which the database answered none of the witness's questions: from
// the moment the witness sent the last question answered before it to the moment the first one
// after it was answered. err is the first failure in it.
type failureWindow struct {
	from, to time.Time
	err      string
}

func (w failureWindow) String() string {
	return fmt.Sprintf("from %s to %s (%s)", w.from.UTC().Format("15:04:05.000"), w.to.UTC().Format("15:04:05.000"), w.err)
}

// holds reports whether a failure the outbox met at at falls in w. A failure of a statement the
// outbox sent inside the span can surface after the database answers again, at the end of that
// statement's own wait, and a loaded runner can be slow to log it, so the span is read up to
// witnessBound past its end.
func (w failureWindow) holds(at time.Time) bool {
	return w.from.Before(at) && at.Before(w.to.Add(witnessBound))
}

// databaseWitness is TestTheOutboxDoesNotScanWhileIdle's own connection to its database, outside
// every pool of the outbox's: it asks the database witnessQuestion every witnessPeriod and records
// each window in which no answer came within witnessBound. A failure the row leaves out must fall
// in such a window (heldScans), so the row leaves out what the database did, and never a failure
// the outbox made itself: a connection it closed, a renewal or a statement it cut short. In the
// forced cases it reaches the database through the same proxy as the outbox.
type databaseWitness struct {
	mu sync.Mutex
	// windows is every window that has closed, and open the one still open, if one is.
	windows []failureWindow
	open    *failureWindow
	// lastSent is when the last question the database answered was sent.
	lastSent time.Time
}

// watchDatabase starts the witness on database's own database, until the test ends, and waits for
// its first answer, so that no window opens on a witness that never reached the database.
func watchDatabase(t *testing.T, database *store.Store) *databaseWitness {
	t.Helper()
	w := &databaseWitness{}
	config := database.Pool.Config().ConnConfig.Copy()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.watch(ctx, config)
	}()
	t.Cleanup(func() { cancel(); <-done })
	w.answeredAfter(t, time.Time{}, "the witness's first answer from the database")
	return w
}

// watch asks the database until ctx ends, on one connection, which it dials again after a failure.
func (w *databaseWitness) watch(ctx context.Context, config *pgx.ConnConfig) {
	var conn *pgx.Conn
	defer func() {
		if conn != nil {
			closeWithin(conn, witnessBound)
		}
	}()
	for ctx.Err() == nil {
		sent := time.Now()
		var err error
		conn, err = ask(ctx, conn, config)
		if ctx.Err() != nil {
			return
		}
		w.record(sent, time.Now(), err)
		select {
		case <-ctx.Done():
		case <-time.After(time.Until(sent.Add(witnessPeriod))):
		}
	}
}

// ask puts witnessQuestion to the database once, within witnessBound, on conn or on a connection
// it dials when conn is nil, and returns the connection to ask on next: none after a failure,
// which closes conn, since pgx closes a connection whose statement was cut short.
func ask(ctx context.Context, conn *pgx.Conn, config *pgx.ConnConfig) (*pgx.Conn, error) {
	bounded, cancel := context.WithTimeout(ctx, witnessBound)
	defer cancel()
	if conn == nil {
		dialled, err := pgx.ConnectConfig(bounded, config)
		if err != nil {
			return nil, fmt.Errorf("dial: %w", err)
		}
		conn = dialled
	}
	if _, err := conn.Exec(bounded, witnessQuestion, publisherHold); err != nil {
		closeWithin(conn, witnessBound)
		return nil, err
	}
	return conn, nil
}

// closeWithin closes conn, giving up after bound, so a connection a frozen network holds cannot
// hold the witness in its Terminate.
func closeWithin(conn *pgx.Conn, bound time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	_ = conn.Close(ctx)
}

// record writes down one question, sent at sent: answered at at, or failed with err. A failure
// before the first answer opens no window: until then the witness has not reached the database at
// all, and the outbox has not started (watchDatabase).
func (w *databaseWitness) record(sent, at time.Time, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case err != nil && w.open == nil && !w.lastSent.IsZero():
		w.open = &failureWindow{from: w.lastSent, err: err.Error()}
	case err == nil:
		if w.open != nil {
			w.open.to = at
			w.windows = append(w.windows, *w.open)
			w.open = nil
		}
		w.lastSent = sent
	}
}

// answeredAfter waits until the database has answered a question the witness sent after since, so
// that every window open at since has closed, and returns every window closed by then.
func (w *databaseWitness) answeredAfter(t *testing.T, since time.Time, what string) []failureWindow {
	t.Helper()
	var windows []failureWindow
	waitFor(t, outboxHangGuard, what, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		windows = slices.Clone(w.windows)
		return w.lastSent.After(since)
	})
	return windows
}

// armedWait is one wait serve armed its retry timer for, and the moment it was armed.
type armedWait struct {
	wait time.Duration
	at   time.Time
}

// cause is what the row knows started a scan: a reason heldScans leaves out, once the witness saw
// the failure of the database it rests on, or a wake or a timer, which the row judges. failedAt is
// each moment the outbox met that failure, every one of which a window of the witness's must hold,
// and someFailedAt are moments at least one of which a window must hold.
type cause struct {
	what                   string
	reason                 bool
	failedAt, someFailedAt []time.Time
}

// heldScan is one scan of the holder's as TestTheOutboxDoesNotScanWhileIdle reads it: the claims
// serve made between two of its waits, since serve answers every take, wake and timer with one
// scan and then waits again.
type heldScan struct {
	// claims is how many batches the scan read, and upTo the outbox's claims after them.
	claims, upTo int64
	// armed is the wait the scan armed the retry timer for, when it armed one.
	armed []armedWait
	// failed is the first error the outbox logged during the scan, when a statement of it failed,
	// and failedAt the moment it logged it.
	failed   string
	failedAt time.Time
	cause    cause
}

// judgedScan is a heldScan judged against the witness's windows. fault is the reason the row leaves
// the scan out for, with the windows that hold its failures, and empty for a scan the row judges;
// woken is what started a judged one.
type judgedScan struct {
	heldScan
	fault, woken string
}

// judge leaves s out when its cause is a reason and the witness saw the failure behind it.
func (s heldScan) judge(windows []failureWindow) judgedScan {
	judged := judgedScan{heldScan: s, woken: s.cause.what}
	if !s.cause.reason {
		return judged
	}
	seen := func(at time.Time) (failureWindow, bool) {
		for _, window := range windows {
			if window.holds(at) {
				return window, true
			}
		}
		return failureWindow{}, false
	}
	unseen := func(at time.Time) judgedScan {
		judged.woken = fmt.Sprintf("%s, though the witness saw no failure of the database at %s", s.cause.what, at.UTC().Format("15:04:05.000"))
		return judged
	}
	var held []string
	for _, at := range s.cause.failedAt {
		window, ok := seen(at)
		if !ok {
			return unseen(at)
		}
		held = append(held, window.String())
	}
	if len(s.cause.someFailedAt) > 0 {
		found := false
		for _, at := range s.cause.someFailedAt {
			if window, ok := seen(at); ok {
				held, found = append(held, window.String()), true
				break
			}
		}
		if !found {
			return unseen(s.cause.someFailedAt[0])
		}
	}
	judged.fault, judged.woken = s.cause.what, ""
	if len(held) > 0 {
		judged.fault += "; the witness saw the database answer nothing " + strings.Join(slices.Compact(held), ", ")
	}
	return judged
}

// renewFailure is one renewal of the publisher hold that the database left unanswered: the loss
// deadline the hold had then, the moment the outbox logged it, and the renewal's error.
type renewFailure struct {
	deadline, at time.Time
	err          string
}

// start is a moment inside the holder's claims after which the claims are a new part's: a take of
// the publisher hold, or a LISTEN that succeeded, at the claims the outbox had made by then. hold
// is the hold a take took, and nil for a LISTEN, whose catch-up is cause.
type start struct {
	claims int64
	hold   *duty.Hold
	cause  cause
}

// heldScans is the record TestTheOutboxDoesNotScanWhileIdle reads its outbox by: every scan the
// holder ran, read at the wait that ends it, with what started it, and the claims the outbox had
// made when it last rested.
//
// An outbox with nothing to do still scans when the database fails it, and it must. In one CI job
// Postgres cancelled two renewals of the publisher hold at their one-second bound while they waited
// on the hold's row, the hold reached its loss deadline, and the outbox took the hold back and
// scanned at once, 9 s into the row's 12 s idle window (run 44434907128, job envoy-go, 19
// November). That scan is the take's catch-up, which no holder may skip: while it did not hold the
// hold, another process could have held it and left events unpublished, and a standby reads none.
// A LISTEN connection that is lost and dialled again catches up the same way, since a notification
// sent while it had none is never reported, and a statement that failed is tried again a
// retry.BaseDelay later. Each of those is a scan with a reason, and none is the scan on a period
// this row is there to catch.
//
// So the row records each one when it happens, and leaves a scan out only when the row's own
// witness (databaseWitness) saw the failure of the database behind it: an outbox that closes its
// own connection, fails its own renewals or cuts its own statements short scans for no failure of
// the database's, and the row counts those scans. A scan is left out when it is:
//
//   - the first scan of a hold taken after the last one lapsed, when a window of the witness's holds
//     the lapse's deadline and at least one renewal the database left unanswered while that
//     deadline stood, whose warning names it. A hold the outbox gave up early has its deadline
//     moved to the moment it was given up, which no warning names, and a hold that lapsed because
//     no renewal was sent has no warning: both count against the outbox;
//   - a LISTEN connection's catch-up, when a window of the witness's holds the loss of the
//     connection before it: the claims that started after its LISTEN succeeded (listenTrace), to
//     the end of the scan the first of them is in. The listener sends the catch-up's wake only
//     after the LISTEN succeeds, so a scan that started before then is no catch-up;
//   - the one scan after a scan that armed its retry and logged a failed statement in a window of
//     the witness's.
//
// The first take and the first connection's catch-up are the outbox's start, which the row waits
// out before it judges. A timer armed by a scan with a reason gives its own scan none: a hold taken
// back arms its timer for a back-off that is really due, and that timer's scan is judged as the
// back-off's. Nor does a wake queued behind such a scan: the failure gave the outbox the one scan.
type heldScans struct {
	outbox  *Outbox
	witness *databaseWitness
	// restingAt is the claims the outbox had made when the holder last entered a wait with nothing
	// left to do, and -1 until it enters one (atRest).
	restingAt atomic.Int64

	// mu guards the rest. Every hook and every log line take it, and waited reads the wake queue
	// under it rather than being handed it, because the facts must be read together: a connection
	// reported up and the wake it sent, or a scan's end and the wakes still queued behind it.
	mu    sync.Mutex
	scans []heldScan
	// open is what the scan serve is running now has armed and logged so far, and starts the takes
	// and LISTENs no scan has ended after yet.
	open   heldScan
	starts []start
	// At the holder's last wait: the claims made by then, the hold it held, whether a wake was
	// queued, and whether the scan it ended armed a wait.
	lastClaims int64
	lastHold   *duty.Hold
	lastQueued bool
	lastArmed  bool
	// listens counts the LISTENs that succeeded, and up is whether the connection of the last one
	// is still up and has reported itself (listening). lost and lostAt are the error and moment of
	// the last loss of a connection that was up.
	listens int
	up      bool
	lost    string
	lostAt  time.Time
	// renewFailures is every renewal of the publisher hold the database left unanswered, and
	// windowsLogged how many of the witness's windows the row has logged.
	renewFailures []renewFailure
	windowsLogged int
}

// install sets h's hooks on outbox, before Run starts it, and makes h's handler the default
// logger's until the test ends; the handler still writes every record to stderr.
func (h *heldScans) install(t *testing.T, outbox *Outbox) {
	h.outbox = outbox
	h.restingAt.Store(-1)
	outbox.afterRetryArmed = h.armed
	outbox.afterListening = h.listening
	outbox.afterServeWait = h.waited
	logtest.SetDefault(t, heldScansLog{Handler: slog.NewTextHandler(os.Stderr, nil), scans: h})
}

func (h *heldScans) armed(wait time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.open.armed = append(h.open.armed, armedWait{wait: wait, at: time.Now()})
}

// listenTrace is the tracer of the outbox's LISTEN connections: it tells scans of each LISTEN that
// succeeded, as the statement returns, before the listener sends the connection's catch-up wake.
type listenTrace struct{ scans *heldScans }

func (l listenTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

func (l listenTrace) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err == nil && data.CommandTag.String() == "LISTEN" {
		l.scans.listened()
	}
}

// listened marks a LISTEN that succeeded: the claims after the ones made by now are its catch-up.
func (h *heldScans) listened() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listens++
	catchUp := cause{what: "the LISTEN connection's first catch-up", reason: true}
	if h.listens > 1 {
		catchUp = cause{
			what:   fmt.Sprintf("a new LISTEN connection's catch-up, after the last one was lost (%s)", h.lost),
			reason: true, failedAt: []time.Time{h.lostAt},
		}
	}
	h.starts = append(h.starts, start{claims: h.outbox.claims.Load(), cause: catchUp})
}

// listening runs once a LISTEN connection is up, after the catch-up wake it sends. The holder can
// have taken that wake, scanned and parked before this, while the connection did not count as up
// yet: that wait is a rest now, when nothing has happened since.
func (h *heldScans) listening() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.up = true
	if len(h.starts) == 0 && !h.lastQueued && !h.lastArmed && len(h.outbox.wake) == 0 && h.outbox.claims.Load() == h.lastClaims {
		h.restingAt.Store(h.lastClaims)
	}
}

// took is the cause of the first scan of a hold taken after lost, or after none.
func (h *heldScans) took(lost *duty.Hold) cause {
	if lost == nil {
		return cause{what: "the publisher hold's first take", reason: true}
	}
	// lost has been released, so its deadline is final.
	deadline := lost.Deadline()
	var failed []time.Time
	last := ""
	for _, failure := range h.renewFailures {
		if failure.deadline.Equal(deadline) {
			failed, last = append(failed, failure.at), failure.err
		}
	}
	if len(failed) == 0 {
		return cause{what: "the take of a hold that did not lapse after renewals the database left unanswered"}
	}
	return cause{
		what: fmt.Sprintf("the publisher hold taken back after it lapsed at %s, the database having left %d of its renewals unanswered before then (%s)",
			deadline.UTC().Format("15:04:05.000"), len(failed), last),
		reason: true, failedAt: []time.Time{deadline}, someFailedAt: failed,
	}
}

// reason is the cause of a scan that no take or LISTEN started, from what the holder's last wait
// left.
func (h *heldScans) reason() cause {
	var before *heldScan
	if n := len(h.scans); n > 0 {
		before = &h.scans[n-1]
	}
	switch {
	case before != nil && before.failed != "" && len(before.armed) > 0:
		return cause{what: fmt.Sprintf("the retry of a statement that failed (%s)", before.failed), reason: true, failedAt: []time.Time{before.failedAt}}
	case h.lastQueued:
		return cause{what: "a wake queued when the scan before it ended"}
	case before != nil && len(before.armed) > 0:
		return cause{what: "the timer the scan before it armed"}
	}
	return cause{what: "a wake"}
}

// waited ends the scan serve has just run, as serve is about to wait, with the claims made since
// its last wait. A start since then splits them: the claims before it are the last part's, and the
// claims after it are the take's first scan or the connection's catch-up. A start no claim has
// followed yet is left to the scan that follows it. It records a rest when nothing is left to do.
func (h *heldScans) waited() {
	h.mu.Lock()
	defer h.mu.Unlock()
	claims := h.outbox.claims.Load()
	from, held := h.lastClaims, h.lastHold
	current := h.reason()
	var later []start
	for _, next := range h.starts {
		if next.claims >= claims {
			later = append(later, next)
			continue
		}
		if next.claims > from {
			h.scans = append(h.scans, heldScan{claims: next.claims - from, upTo: next.claims, cause: current})
			from = next.claims
		}
		current = next.cause
		if next.hold != nil {
			current = h.took(held)
			held = next.hold
		}
	}
	h.starts = later
	scan := h.open
	h.open = heldScan{}
	scan.claims, scan.upTo, scan.cause = claims-from, claims, current
	queued := len(h.outbox.wake) > 0
	h.scans = append(h.scans, scan)
	h.lastClaims, h.lastHold, h.lastQueued, h.lastArmed = claims, held, queued, len(scan.armed) > 0
	if h.up && !queued && !h.lastArmed && len(h.starts) == 0 {
		h.restingAt.Store(claims)
	}
}

// logged reads the lines heldScans keeps: a failed renewal of the publisher hold, a lost LISTEN
// connection, a take of the hold, and an error the outbox writes during a scan.
func (h *heldScans) logged(record slog.Record) {
	attrs := map[string]slog.Value{}
	record.Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value
		return true
	})
	text := func(key string) string {
		if value, ok := attrs[key]; ok {
			return value.String()
		}
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case strings.HasPrefix(record.Message, renewFailedLine) && text("hold") == publisherHold:
		if deadline := attrs["deadline"]; deadline.Kind() == slog.KindTime {
			h.renewFailures = append(h.renewFailures, renewFailure{deadline: deadline.Time(), at: record.Time, err: text("error")})
		}
	case strings.HasSuffix(record.Message, redialLine) && strings.Contains(text("channels"), eventsChannel):
		if h.up {
			h.lost, h.lostAt = text("error"), record.Time
		}
		h.up = false
	case strings.HasPrefix(record.Message, tookHoldLine):
		// Written after the store of the hold and before its first scan, on the goroutine that
		// scans: the hold and the claims read here are the take's own. What the standby logged
		// before it is no failure of a scan.
		h.starts = append(h.starts, start{claims: h.outbox.claims.Load(), hold: h.outbox.hold.Load()})
		h.open.failed = ""
	case record.Level >= slog.LevelError && strings.HasPrefix(record.Message, outboxLine) && h.open.failed == "":
		h.open.failed, h.open.failedAt = strings.TrimPrefix(record.Message, outboxLine)+": "+text("error"), record.Time
	}
}

// atRest waits until the holder is waiting with nothing left to do - no wake queued, no retry
// armed, its LISTEN connection up and reported, no take or LISTEN without its scan - and nothing
// scanned since. That is a state rather than an edge: it holds for as long as the outbox has
// nothing to do, so a reader cannot arrive too late to see it.
func (h *heldScans) atRest(t *testing.T, what string) {
	t.Helper()
	// restingAt is read first, so the claims it is compared with are never older than it: an
	// equal pair is a wait no scan has followed, never a scan this read straddled.
	waitFor(t, outboxHangGuard, what, func() bool { return h.restingAt.Load() == h.outbox.claims.Load() })
}

// mark is how many scans have ended: a part of the row reads the scans after it.
func (h *heldScans) mark() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.scans)
}

// judged is the scans that ended after mark, judged against the witness's windows. It first waits
// for the database to answer a question the witness sent after the call, so every window open
// while those scans ran has closed, and it logs each window the row has not logged yet, under part.
func (h *heldScans) judged(t *testing.T, part string, mark int) []judgedScan {
	t.Helper()
	windows := h.witness.answeredAfter(t, time.Now(), "the witness's answer after "+part)
	h.mu.Lock()
	scans := slices.Clone(h.scans[mark:])
	logged := h.windowsLogged
	h.windowsLogged = len(windows)
	h.mu.Unlock()
	for _, window := range windows[logged:] {
		t.Logf("%s: the witness saw the database answer nothing %s", part, window)
	}
	judged := make([]judgedScan, len(scans))
	for i, scan := range scans {
		judged[i] = scan.judge(windows)
	}
	return judged
}

// heldScansLog writes every record on, and gives heldScans each one first.
type heldScansLog struct {
	slog.Handler
	scans *heldScans
}

func (l heldScansLog) Handle(ctx context.Context, record slog.Record) error {
	l.scans.logged(record)
	return l.Handler.Handle(ctx, record)
}

func (l heldScansLog) WithAttrs(attrs []slog.Attr) slog.Handler {
	return heldScansLog{Handler: l.Handler.WithAttrs(attrs), scans: l.scans}
}

func (l heldScansLog) WithGroup(name string) slog.Handler {
	return heldScansLog{Handler: l.Handler.WithGroup(name), scans: l.scans}
}

// plain is whether a scan is judged and claimed: one no failure the witness saw explains.
func (s judgedScan) plain() bool { return s.fault == "" && s.claims > 0 }

// withoutFault is the claims of the judged scans, and what started each of them.
func withoutFault(scans []judgedScan) (claims int64, woken []string) {
	for _, scan := range scans {
		if scan.plain() {
			claims += scan.claims
			woken = append(woken, fmt.Sprintf("%d by %s", scan.claims, scan.woken))
		}
	}
	return claims, woken
}

// armedIn is every wait the scans armed, in order, split by whether a reason the row leaves out
// gave the scan that armed it.
func armedIn(scans []judgedScan) (plain, faulted []armedWait) {
	for _, scan := range scans {
		if scan.fault == "" {
			plain = append(plain, scan.armed...)
		} else {
			faulted = append(faulted, scan.armed...)
		}
	}
	return plain, faulted
}

// logFaults writes each reason that gave the outbox scans during one part of the row, with the
// claims they made and the window the witness saw, so a run that met one says so.
func logFaults(t *testing.T, part string, scans []judgedScan) {
	t.Helper()
	for _, scan := range scans {
		if scan.fault != "" && scan.claims > 0 {
			t.Logf("%s: %d claims came with a reason the row leaves out: %s", part, scan.claims, scan.fault)
		}
	}
}

// An idle outbox scans nothing: no claim runs while no event is written and no retry falls due,
// unless the database fails it and so gives it a reason to (heldScans says which reasons, why the
// row leaves them out, and how its witness of the database must have seen each failure). An event
// another process commits - which reaches this one only through the database, not its broker - is
// claimed by the LISTEN on dispatch_events, and a back-off is claimed when it falls due, by the one
// timer armed to it, with nothing else waking the outbox in between. Every step is read from what
// the outbox and the database recorded - the scans and what started each, the waits armed, the
// moment the database marked the event published - and never from how long a step took.
//
// One case the row cannot tell from a fault of the outbox's: a stall of the test process itself
// that sends no renewal of the publisher hold for the 4 s to its loss deadline. The hold lapses
// with no renewal warning and no window of the witness's, and the take that follows is counted. A
// log of the runner's clock tells that case apart: it shows the process stopped for that long.
func TestTheOutboxDoesNotScanWhileIdle(t *testing.T) {
	database := storetest.Open(t)
	other := storetest.SecondProcess(t, database)
	seedIssue(t, database, "T-1", nil)
	held := &heldScans{witness: watchDatabase(t, database)}
	// The outbox's LISTEN connections are traced, so the row knows when each LISTEN succeeded
	// (listenTrace).
	outbox := New(Deps{Store: database, Publisher: &recordingPublisher{}, Broker: events.NewBroker(),
		Listen: storetest.Traced(t, database.Pool, listenTrace{scans: held}, nil).Listener})
	held.install(t, outbox)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { outbox.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, outboxHangGuard, "the outbox taking the publisher hold", outbox.Publishing)
	// The first scan and the LISTEN connection's catch-up run as the hold is taken; the row reads
	// that they are over rather than allowing them a span of time. This row's publisher never
	// disconnects, so a rest is always one the holder reached with nothing to scan, never one it
	// reached with NATS down.
	held.atRest(t, "the outbox resting after the hold's first scan and the LISTEN connection's catch-up")
	idleFrom := held.mark()
	// A lower bound on quiet, not an upper bound on work: the row asserts nothing scanned in this
	// window without a reason, and load can only make an idle outbox quieter, never busier.
	time.Sleep(12 * time.Second)
	// A scan is read once it has ended, so one still running as the window closes is read at the
	// rest that follows it.
	held.atRest(t, "the outbox resting at the end of its idle window")
	idle := held.judged(t, "the idle window", idleFrom)
	if claims, woken := withoutFault(idle); claims != 0 {
		t.Fatalf("an idle outbox ran %d claims in 12 s, want 0: %s", claims, strings.Join(woken, "; "))
	}
	logFaults(t, "the idle window", idle)

	// Committed on another process's pool, and published to no broker this outbox hears.
	freshFrom := held.mark()
	fresh := appendEvent(t, other, events.NewBroker(), model.Event{
		IssueKey: new("T-1"), Type: "message.created", Actor: model.Actor{Kind: "user", ID: "alice"},
		Payload: model.Message{ID: "fresh", IssueKey: new("T-1"), Body: "fresh"},
	})
	// A local clock for the reading below alone: no assertion reads it.
	committed := time.Now()
	// The window above showed this outbox scans on nothing periodic, so what publishes this event
	// is the notification's scan, or one a failure of the database gave, which is logged below; the
	// bound only stops the row hanging when nothing publishes it at all.
	waitFor(t, outboxHangGuard, "the other process's event published", func() bool { return publishedAt(t, database, fresh.ID) != nil })
	t.Logf("the event another process committed was published %s after its commit", time.Since(committed).Round(time.Millisecond))
	// The sequence below starts from an outbox at rest: the wait above returns as soon as the
	// other process's event is marked published, while the batch that published it has still to
	// start the batch that follows it and the scan has still to arm whatever comes of that.
	held.atRest(t, "the outbox resting after the other process's event")
	logFaults(t, "the other process's event", held.judged(t, "the other process's event", freshFrom))

	// A back-off due in backOffDue, committed by the other process: its insert wakes the outbox,
	// whose scan finds nothing due and arms its one retry timer to the back-off's own moment, and
	// the timer's scan is the one that publishes it.
	backOffFrom := held.mark()
	// due is the moment the database computed for this back-off; every assertion about it below is
	// made against that value, on the database's own clock.
	backedOff, due := appendBackedOffEvent(t, other, backOffDue)
	// A local clock for this loop's hang guard and the readings below alone: no assertion reads it.
	inserted := time.Now()
	var published time.Time
	// The claims the outbox had made at the last reading taken while the back-off was still ahead:
	// every claim up to it is of a scan that started before the back-off fell due.
	startedBeforeDue := int64(-1)
	for {
		// Read oldest first, so nothing is judged against a reading younger than itself: the
		// claims, then the database's answer, whose now() is therefore no earlier than every claim
		// read.
		claims := outbox.claims.Load()
		at, beforeDue := backOffState(t, database, backedOff.ID)
		if at != nil {
			published = *at
			break
		}
		if beforeDue {
			startedBeforeDue = claims
		}
		// The one clock left in this loop is a hang guard, 60 s against the backOffDue the work
		// waits out, so no load reaches it: it fires only when nothing was published at all. A
		// hang guard needs no agreement between clocks, so the local one serves.
		if time.Since(inserted) > outboxHangGuard {
			plain, faulted := armedIn(held.judged(t, "the back-off", backOffFrom))
			t.Fatalf("the back-off due in %s was never published: %s after its insert the outbox had armed %v, and %v after a failure",
				backOffDue, time.Since(inserted).Round(time.Millisecond), plain, faulted)
		}
		// How often this loop reads, bounding nothing: a coarser poll would only read the same
		// sequence later.
		time.Sleep(5 * time.Millisecond)
	}
	publishedAfter := time.Since(inserted)
	// Every scan up to the publish is read once it has ended.
	held.atRest(t, "the outbox resting after it published the back-off")
	scans := held.judged(t, "the back-off", backOffFrom)
	logFaults(t, "the back-off", scans)
	// Nothing but the insert's own notification wakes the outbox before the back-off falls due,
	// failures apart: one scan with no reason that started while the back-off was ahead. A scan
	// whose first claim the loop above saw while the database put the due moment still ahead
	// started before it fell due, so its wake was not the back-off's timer. The timer's own scan
	// cannot be one of them: the timer fires no sooner than the due moment, and the loop read each
	// claims count before a database answer that put the due moment still ahead.
	var early []string
	for _, scan := range scans {
		if scan.plain() && scan.upTo-scan.claims < startedBeforeDue {
			early = append(early, fmt.Sprintf("%d by %s", scan.claims, scan.woken))
		}
	}
	if len(early) > 1 {
		t.Fatalf("the outbox started %d scans after the back-off's insert and before it fell due, want only the one its insert woke: %s",
			len(early), strings.Join(early, "; "))
	}
	// One wait armed, by the scan the insert woke. A failure can arm another in that scan's place -
	// a hold taken back reads the back-off afresh and arms the wait the lost one had, and a failed
	// statement arms its retry - but nothing else arms one.
	plain, faulted := armedIn(scans)
	if len(plain) > 1 || len(plain)+len(faulted) == 0 {
		t.Fatalf("the outbox armed %d waits between the back-off's insert and its publish, and %d more after a failure, want 1 in all: %v %v",
			len(plain), len(faulted), plain, faulted)
	}
	// The wait is a value the database computed from its own next_attempt_at, not an elapsed time
	// this test measured, so a loaded runner can only make it smaller: the only way past
	// backOffDue is a wait armed for something that is not this back-off. The lower end is the
	// cheaper half of the same statement: nextRetry clamps a back-off already due to zero, so a
	// wait of zero is a timer armed to fire at once, which is not the timer this row is about.
	// A scan so late that the back-off is already due does not arm even that - it publishes the
	// back-off in that scan and arms nothing - and the assertion above, a wait armed at all, is
	// what catches that. A wait a failure armed may be zero, since a hold taken back just as the
	// back-off fell due arms the zero the clamp gives it, and is held to the same upper end: a
	// failed statement's retry, retry.BaseDelay, is below it too.
	for _, armed := range plain {
		if armed.wait <= 0 || armed.wait > backOffDue {
			t.Fatalf("the outbox armed a %s wait for a back-off due in %s, want one above zero and no more than %s",
				armed.wait, backOffDue, backOffDue)
		}
	}
	for _, armed := range faulted {
		if armed.wait < 0 || armed.wait > backOffDue {
			t.Fatalf("after a failure the outbox armed a %s wait for a back-off due in %s, want one no more than %s",
				armed.wait, backOffDue, backOffDue)
		}
	}
	// Exact where a margin was a guess: the publication time and the moment the back-off fell due
	// are both the database's own values, so no clock is compared with another and no load can
	// move either. A publication before that moment would be the outbox publishing a back-off that
	// was not yet due.
	if published.Before(due) {
		t.Fatalf("the back-off was published at %s, %s before the %s the database made it due, want no sooner than due",
			published, due.Sub(published).Round(time.Millisecond), due)
	}
	// A reading for the record, not an assertion.
	first := slices.MinFunc(append(plain, faulted...), func(a, b armedWait) int { return a.at.Compare(b.at) })
	t.Logf("the outbox armed a %s wait %s after the insert, and the back-off was published %s after it and %s after the database made it due",
		first.wait.Round(time.Millisecond), first.at.Sub(inserted).Round(time.Millisecond),
		publishedAfter.Round(time.Millisecond), published.Sub(due).Round(time.Millisecond))
}

// appendBackedOffEvent commits an event on database whose next attempt is due after wait on the
// database's clock, as a back-off leaves it. It returns the event and the next_attempt_at the
// database computed: the moment it falls due, which only the database's own clock can be read
// against.
func appendBackedOffEvent(t *testing.T, database *store.Store, wait time.Duration) (model.Event, time.Time) {
	t.Helper()
	ctx := context.Background()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin event transaction: %v", err)
	}
	defer tx.Rollback(ctx)
	event, err := events.NewBroker().Append(ctx, tx, model.Event{
		IssueKey: new("T-1"), Type: "message.created", Actor: model.Actor{Kind: "user", ID: "alice"},
		Payload: model.Message{ID: "backed-off", IssueKey: new("T-1"), Body: "backed off"},
	})
	if err != nil {
		t.Fatalf("append event: %v", err)
	}
	var due time.Time
	if err := tx.QueryRow(ctx, `update events set next_attempt_at = now() + make_interval(secs => $2), attempt_count = 1 where id = $1 returning next_attempt_at`,
		event.ID, wait.Seconds()).Scan(&due); err != nil {
		t.Fatalf("back the event off: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit event: %v", err)
	}
	return event, due
}

// backOffState reads whether the event has been published and whether its back-off is still
// ahead, both on the database's own clock and in one statement: next_attempt_at is a value the
// database computed, so only the database can say whether it has passed. A published event has no
// next_attempt_at left (the publisher clears it), and nothing ahead of the clock either.
func backOffState(t *testing.T, database *store.Store, eventID int64) (published *time.Time, beforeDue bool) {
	t.Helper()
	var ahead *bool
	if err := database.Pool.QueryRow(context.Background(),
		`select published_at, now() < next_attempt_at from events where id = $1`, eventID).Scan(&published, &ahead); err != nil {
		t.Fatalf("read the event's back-off state: %v", err)
	}
	return published, ahead != nil && *ahead
}

// Two processes' outboxes on one database: exactly one publishes at every moment, and every event
// either process writes is sent once, by whichever holds the publisher hold.
func TestOnlyOneOutboxPublishesAtATime(t *testing.T) {
	_, url := testnats.Start(t)
	database := storetest.Open(t)
	seedIssue(t, database, "T-1", nil)
	counter := &sendCounter{}
	first := startProcess(t, "first", database, url, counter)
	second := startProcess(t, "second", storetest.SecondProcess(t, database), url, counter)
	waitForOnePublisher(t, 10*time.Second, first, second)

	var written []model.Event
	deadline := time.Now().Add(10 * time.Second)
	for sample := 0; time.Now().Before(deadline); sample++ {
		if names := publishing(first, second); len(names) != 1 {
			t.Fatalf("sample %d: publishing %v, want exactly one outbox", sample, names)
		}
		if sample%10 == 0 {
			writer := []*process{first, second}[(sample/10)%2]
			written = append(written, writeMessage(t, writer, fmt.Sprintf("message %d", sample)))
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, event := range written {
		waitForPublished(t, 5*time.Second, database, counter, event)
	}
}

// A process that stops gracefully releases the publisher hold, and the other takes it over at once
// rather than when the hold would have lapsed.
func TestAStoppedOutboxHandsThePublisherToTheOtherAtOnce(t *testing.T) {
	_, url := testnats.Start(t)
	database := storetest.Open(t)
	seedIssue(t, database, "T-1", nil)
	counter := &sendCounter{}
	first := startProcess(t, "first", database, url, counter)
	second := startProcess(t, "second", storetest.SecondProcess(t, database), url, counter)
	holder := waitForOnePublisher(t, 10*time.Second, first, second)
	standby := first
	if holder == first {
		standby = second
	}

	holder.stop()
	stopped := time.Now()
	waitFor(t, time.Second, "the standby publishing once the holder stopped", standby.outbox.Publishing)
	t.Logf("the standby took the publisher hold %s after the holder stopped", time.Since(stopped).Round(time.Millisecond))
	event := writeMessage(t, standby, "after the hand-over")
	waitForPublished(t, time.Second, database, counter, event)
}

// A holder whose NATS connection is cut releases the publisher hold once it has been without NATS
// for natsLossGrace, and the other process, whose NATS works, takes the hold and publishes what is
// written after the cut.
func TestAnOutboxThatLosesNATSReleasesThePublisher(t *testing.T) {
	_, url := testnats.Start(t)
	proxy := testnats.StartProxy(t, url)
	proxied := proxy.URL()
	database := storetest.Open(t)
	seedIssue(t, database, "T-1", nil)
	counter := &sendCounter{}
	cut := startProcess(t, "cut", database, proxied, counter)
	waitFor(t, 10*time.Second, "the first process taking the publisher hold", cut.outbox.Publishing)
	working := startProcess(t, "working", storetest.SecondProcess(t, database), url, counter)

	proxy.Cut()
	downAt := time.Now()
	waitFor(t, natsLossGrace+500*time.Millisecond, "the cut process releasing the publisher hold", func() bool {
		return cut.outbox.hold.Load() == nil
	})
	t.Logf("the cut process released the publisher hold %s after the cut", time.Since(downAt).Round(time.Millisecond))
	event := writeMessage(t, working, "after the cut")
	waitForPublished(t, 10*time.Second-time.Since(downAt), database, counter, event)
	if !working.outbox.Publishing() || cut.outbox.Publishing() {
		t.Fatalf("publishing %v after the cut, want only the process whose NATS works", publishing(cut, working))
	}
}

// A holder whose NATS connection goes silent - still connected, nothing answering - finds out when
// a batch's sends go unanswered and a probe of their connection fails, and releases the publisher
// hold. It does not take the hold back while its connection stays silent, although the connection
// still reports itself connected: the other process, whose NATS works, takes it and publishes the
// event, within 10 s of the silence.
func TestAnOutboxWhoseNATSGoesSilentGivesThePublisherToTheOther(t *testing.T) {
	_, url := testnats.Start(t)
	proxy := testnats.StartProxy(t, url)
	database := storetest.Open(t)
	seedIssue(t, database, "T-1", nil)
	counter := &sendCounter{}
	silent := startProcess(t, "silent", database, proxy.URL(), counter)
	waitFor(t, 10*time.Second, "the first process taking the publisher hold", silent.outbox.Publishing)
	working := startProcess(t, "working", storetest.SecondProcess(t, database), url, counter)

	proxy.Freeze()
	t.Cleanup(proxy.Restore)
	frozenAt := time.Now()
	event := writeMessage(t, working, "after the silence")
	waitFor(t, 10*time.Second, "the working process publishing the event", func() bool {
		return working.outbox.Publishing() && publishedAt(t, database, event.ID) != nil
	})
	t.Logf("the working process published the event %s after the silence", time.Since(frozenAt).Round(time.Millisecond))
	for range 20 {
		if silent.outbox.Publishing() {
			t.Fatalf("publishing %v while the silent process's connection stays silent, want only the process whose NATS works",
				publishing(silent, working))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A standby waits for the publisher hold to be released or to lapse, or for its NATS connection to
// change, and for nothing else: the events the holder writes and publishes do not make it try the
// hold. 100 events written one after another while the holder holds send at most the few takes and
// lapse reads the standby's timer to the holder's lapse makes over that span (each renewal moves the
// lapse 6 s on), not one take and one read per event, which would put a write on duty_holds for
// every event on every standby.
func TestAStandbyDoesNotTryTheHoldForEveryEventTheHolderWrites(t *testing.T) {
	_, url := testnats.Start(t)
	database := storetest.Open(t)
	seedIssue(t, database, "T-1", nil)
	counter := &sendCounter{}
	holder := startProcess(t, "holder", database, url, counter)
	waitFor(t, 10*time.Second, "the holder taking the publisher hold", holder.outbox.Publishing)

	client, err := bus.ConnectOwningStream([]string{url})
	if err != nil {
		t.Fatalf("connect the standby's NATS client: %v", err)
	}
	t.Cleanup(client.Close)
	tracer := &statementTracer{}
	standby := New(Deps{
		Store:     tracedStore(t, storetest.SecondProcess(t, database), tracer),
		Publisher: client,
		Broker:    events.NewBroker(),
		Listen:    database.Pool.Listener,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { standby.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, 5*time.Second, "the standby trying the publisher hold once", func() bool {
		return len(tracer.matching("insert into duty_holds")) > 0
	})
	// The standby's LISTEN connection's catch-up wake comes as it starts.
	time.Sleep(time.Second)

	takes, reads := len(tracer.matching("insert into duty_holds")), len(tracer.matching("from duty_holds where name"))
	var last model.Event
	for index := range 100 {
		last = writeMessage(t, holder, fmt.Sprintf("message %d", index))
	}
	waitForPublished(t, 10*time.Second, database, counter, last)
	if standby.Publishing() {
		t.Fatal("the standby publishes while the holder holds the publisher hold")
	}
	takes = len(tracer.matching("insert into duty_holds")) - takes
	reads = len(tracer.matching("from duty_holds where name")) - reads
	t.Logf("while 100 events were written and published, the standby sent %d takes of the hold and %d reads of its lapse", takes, reads)
	if takes > 3 || reads > 3 {
		t.Fatalf("the standby sent %d takes of the publisher hold and %d reads of its lapse while the holder wrote 100 events, want at most 3 of each: its timer to the lapse alone",
			takes, reads)
	}
}

// fenceAfterFirstSend is a publisher hold whose deadline passes as the batch's first envelope is
// sent, as a process that froze mid-batch past its deadline finds when it resumes.
type fenceAfterFirstSend struct {
	recordingPublisher
	passed atomic.Bool
}

func (p *fenceAfterFirstSend) WindowUntil(deadline func() time.Time) bus.Sender {
	return deadlineWindow{Sender: p, deadline: deadline}
}

func (p *fenceAfterFirstSend) PublishAsync(item contracts.Envelope) <-chan error {
	p.passed.Store(true)
	return p.recordingPublisher.PublishAsync(item)
}

func (p *fenceAfterFirstSend) Deadline() time.Time {
	if p.passed.Load() {
		return time.Now().Add(-time.Second)
	}
	return time.Now().Add(time.Hour)
}

// A batch checks the publisher hold's deadline before every send: once a send has met it the
// batch sends nothing more, counts no attempt, writes nothing, and reports the hold lost. The
// record is not checked against the in-process clock a second time - a batch whose sends all
// went out under the hold records what they came to - and whether its back-offs may still be
// scheduled is the record's own check, on the database's clock (recordBatch).
func TestABatchPastThePublisherHoldsDeadlineSendsAndRecordsNothingMore(t *testing.T) {
	database := storetest.Open(t)
	broker := events.NewBroker()
	seedIssue(t, database, "T-1", nil)
	for index := range 3 {
		appendEvent(t, database, broker, model.Event{
			IssueKey: new("T-1"), Type: "message.created", Actor: model.Actor{Kind: "user", ID: "alice"},
			Payload: model.Message{ID: fmt.Sprintf("message-%d", index), IssueKey: new("T-1"), Body: "fenced"},
		})
	}
	publisher := &fenceAfterFirstSend{}
	batch, err := scanBatch(context.Background(), Deps{Store: database, Publisher: publisher}, publisher, uuid.New())
	if err != nil {
		t.Fatalf("scan the batch: %v", err)
	}
	if !batch.fenced || batch.sent != 1 {
		t.Fatalf("batch = %+v, want it fenced after its first event", batch)
	}
	if sent := publisher.all(); len(sent) != 1 {
		t.Fatalf("the batch sent %d envelopes, want only the one before the deadline passed", len(sent))
	}
	var published, attempted int
	if err := database.Pool.QueryRow(context.Background(), `
		select count(*) filter (where published_at is not null), count(*) filter (where attempt_count <> 0) from events
	`).Scan(&published, &attempted); err != nil {
		t.Fatalf("read the events: %v", err)
	}
	if published != 0 || attempted != 0 {
		t.Fatalf("%d events marked published and %d backed off past the deadline, want none written", published, attempted)
	}
	end := New(Deps{Store: database, Publisher: &recordingPublisher{}}).scan(context.Background(), publisher)
	if !end.fenced {
		t.Fatalf("a scan under a hold past its deadline ended %+v, want fenced before any claim", end)
	}
}

// reconnectedQuietly is a publisher whose client reads connected again without its connection
// change ever being reported, as a reconnect whose event waits in nats.go's callback queue behind
// a slow callback finds it: ConnectionChanged never closes, and nothing wakes the holder.
type reconnectedQuietly struct {
	recordingPublisher
	connected atomic.Bool
}

func (p *reconnectedQuietly) Connected() bool { return p.connected.Load() }

func (p *reconnectedQuietly) WindowUntil(deadline func() time.Time) bus.Sender {
	return deadlineWindow{Sender: p, deadline: deadline}
}

// A holder whose client reads connected again when natsLossGrace runs out serves on, though no
// connection event came: it reads the client's status as the grace ends, keeps the hold and scans,
// rather than giving the hold up for a loss that is over.
func TestAHolderConnectedAgainWhenTheGraceEndsKeepsTheHold(t *testing.T) {
	database := storetest.Open(t)
	publisher := &reconnectedQuietly{}
	outbox := New(Deps{Store: database, Publisher: publisher})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hold, _ := outbox.tryHold(ctx)
	if hold == nil {
		t.Fatal("the outbox did not take the publisher hold")
	}
	t.Cleanup(func() { _ = hold.Release(context.Background()) })

	ended := make(chan holdEnd, 1)
	go func() { ended <- outbox.serve(ctx, hold) }()
	time.Sleep(200 * time.Millisecond)
	if outbox.claims.Load() != 0 {
		t.Fatal("the holder scanned while its client read disconnected")
	}
	publisher.connected.Store(true)
	select {
	case end := <-ended:
		t.Fatalf("the holder stopped serving (%s) though its client read connected when the grace ended", end)
	case <-time.After(natsLossGrace + time.Second):
	}
	if outbox.claims.Load() == 0 {
		t.Fatal("the holder did not scan once it found its client connected at the grace's end")
	}
	cancel()
	if end := <-ended; end != stopping {
		t.Fatalf("serve ended %s once ctx ended, want %s", end, stopping)
	}
}

// A record that finds the publisher hold another's ends the holder as one that lost the hold,
// with the hold's loss deadline still ahead. The holder stops that hold's renewal there and then:
// its Lost is closed, it sends no renewal across the first tick that follows, and the other's row
// is left alone. This is also the one path that touches the hold not at all, so its deadline says
// nothing about when the process stopped publishing: the line that ends the turn has to date the
// stop at serve's return rather than at a deadline still minutes ahead. That makes it the one
// path in this package's rows where the line's two bounds, stopped and stopped_by, are not both
// that deadline but bracket the store that turned Publishing() false, so this is the row that
// holds their order.
func TestAHolderWhoseRecordFindsTheHoldAnothersStopsItsRenewal(t *testing.T) {
	recorder := &logtest.Recorder{}
	logtest.SetDefault(t, recorder)

	ctx := context.Background()
	database := storetest.Open(t)
	broker := events.NewBroker()
	seedIssue(t, database, "T-1", nil)
	tracer := &statementTracer{}
	outbox := New(Deps{Store: tracedStore(t, database, tracer), Publisher: &recordingPublisher{}})

	// The row takes the hold with duty.TryHold under options of its own rather than through
	// outbox.tryHold, which uses publisherHoldOptions. What it tests is what publish does and
	// logs when a recordBatch finds the hold another's before any renewal has noticed, and
	// that property is not the publisher hold's production numbers; binding the row's slack to
	// those numbers made every change of them a change of this row's flakiness. Halving Renew
	// from 2 s to 1 s left the take, the seed, the append and a whole scan one second to run in
	// before the guard below reports a correct product as a defect - on a runner that, by this
	// change's own evidence, stalled for 3.1 s.
	//
	// These numbers leave seconds instead, inside what duty.TryHold accepts (0 <= Margin < TTL
	// and 0 < Renew < TTL - Margin): the first renewal tick falls five seconds after the take, so
	// the take, the seed, the append and a whole scan have those five seconds to run in before
	// any renewal of this hold can reach the record path, and the deadline - the take plus
	// TTL - Margin - stays five minutes ahead for the whole row. Five seconds is also short
	// enough for the count below to wait that tick out and see whether the lost hold's renewal
	// stopped. The hold is the outbox's own, under its token and instance, so the batch that
	// records under it finds the row another's exactly as it would in production.
	recordPath := duty.HoldOptions{TTL: 6 * time.Minute, Renew: 5 * time.Second, Margin: time.Minute, Token: outbox.token}
	taken := time.Now()
	hold, err := duty.TryHold(ctx, outbox.deps.Store.Pool, publisherHold, outbox.instance, recordPath)
	if err != nil {
		t.Fatalf("take the publisher hold under the row's own options: %v", err)
	}
	if hold == nil {
		t.Fatal("the outbox did not take the publisher hold")
	}
	deadline := hold.Deadline()
	other := uuid.New()
	holdRow(t, database, other)
	appendEvent(t, database, broker, model.Event{
		IssueKey: new("T-1"), Type: "message.created", Actor: model.Actor{Kind: "user", ID: "alice"},
		Payload: model.Message{ID: uuid.NewString(), IssueKey: new("T-1"), Body: "recorded under another's hold"},
	})

	began := time.Now()
	if end := outbox.publish(ctx, hold); end != holdLost {
		t.Fatalf("publish ended %s, want %s", end, holdLost)
	}
	returned := time.Now()
	// A publish slow enough to outrun the first renewal would reach holdLost by that renewal
	// finding the row another's instead, where loseLocked pulls the deadline back to the moment
	// it closed Lost and stopped is that deadline, so say which path ran rather than let the
	// assertions below fail as if the product were wrong.
	if elapsed := returned.Sub(taken); elapsed >= recordPath.Renew {
		t.Fatalf("publish took %s, past the first renewal of the row's own hold: the row is about a record finding the hold another's before any renewal does", elapsed)
	}
	select {
	case <-hold.Lost():
	default:
		t.Fatal("the lost hold's Lost is still open: its renewal runs on")
	}
	var holder uuid.UUID
	if err := database.Pool.QueryRow(ctx, `select holder from duty_holds where name = $1`, publisherHold).Scan(&holder); err != nil || holder != other {
		t.Fatalf("the hold's row names %s (%v), want the other process's %s left alone", holder, err, other)
	}

	stopped, ok := timeIn(t, recorder, "lost the publisher hold", "stopped")
	if !ok {
		t.Fatalf("no record of the log says %q; it says %q", "lost the publisher hold", loggedMessages(recorder))
	}
	// timeIn has already found that record, and it fails the row itself when a key it is asked
	// for is missing, so there is nothing left for a second !ok to catch.
	stoppedBy, _ := timeIn(t, recorder, "lost the publisher hold", "stopped_by")
	// The four comparisons below hold the whole order a reader of the log depends on:
	// began <= stopped <= stopped_by <= returned, with stopped_by still short of the deadline.
	// began is read just ahead of publish and returned just after it, so the pair is held inside
	// the turn itself, which is what separates serve's return from a deadline minutes further on;
	// a stopped that is too early - a zero value, or one dated before this turn began - would
	// pass every bound from the future and is caught by the first. The middle comparison is the
	// reason this row reads stopped_by at all: this is the one path here where the two are not
	// the same capped value, so the two clocks read in the wrong order, or the two attributes
	// swapped, leave an upper bound earlier than the lower one and fail here; two reads that land
	// on the same nanosecond pass, and leave bounds that are still honest to the clock.
	//
	// What no comparison here can catch is which side of o.hold.Store(nil) each clock was read
	// on. Moving the second read above the store, and leaving it after the first, keeps the two
	// in order and every bound below holding; the line is written after both reads and after the
	// store, so nothing it carries says where the store fell between them. No row holds that
	// placement: publish's doc comment records it, and the one reader that compares /healthz with
	// stopped_by, the two-process row, sees only turns that end at the hold's deadline, where
	// stopped_by is the deadline wherever the read sits.
	if stopped.Before(began) {
		t.Errorf("the line dates the stop at %s, before publish was called at %s",
			stopped.Format(time.RFC3339Nano), began.Format(time.RFC3339Nano))
	}
	if stoppedBy.Before(stopped) {
		t.Errorf("the line bounds the stop above by %s, before the %s it bounds it below by",
			stoppedBy.Format(time.RFC3339Nano), stopped.Format(time.RFC3339Nano))
	}
	if returned.Before(stoppedBy) {
		t.Errorf("the line bounds the stop above by %s, after the turn ended at %s",
			stoppedBy.Format(time.RFC3339Nano), returned.Format(time.RFC3339Nano))
	}
	if !stoppedBy.Before(deadline) {
		t.Errorf("the line bounds the stop above by %s, at or past the loss deadline of %s this path leaves minutes ahead",
			stoppedBy.Format(time.RFC3339Nano), deadline.Format(time.RFC3339Nano))
	}

	// This count is the whole of the proof that the renewal of the hold just lost stopped when
	// publish released it: nothing else in the row watches that renewal. Its window starts here,
	// with publish returned and the stop asserted, and runs past that hold's first renewal tick
	// at taken + Renew, which no earlier part of the row reaches: a renewal still running sends
	// its one statement there - that is what a duty.Hold whose Release does not stop the renewal,
	// and whose renew does not return when Lost closes, does - and nothing else in this process
	// renews a hold, so a statement in the window could only be this one's. Half a Renew of slack
	// past the tick, so a statement a loaded runner was slow to start still lands inside the
	// window rather than after it.
	renewalsBefore := len(tracer.matching("update duty_holds set expires_at"))
	window := recordPath.Renew + recordPath.Renew/2
	time.Sleep(time.Until(taken.Add(window)))
	if got := len(tracer.matching("update duty_holds set expires_at")) - renewalsBefore; got != 0 {
		t.Fatalf("%s after the hold was taken - past the first tick of its renewal at %s - %d renewals of the lost hold were sent, want none: publish left its renewal running", window, recordPath.Renew, got)
	}
}

// heldRelease is a pgx tracer that holds the hold's release statement until the test lets it go,
// so a row can read the log while the release is still running. Every other statement passes.
// A row defers let: without it a t.Fatalf in the middle would leave the statement blocked, and
// the pool's own close - a cleanup, which runs after every defer - would wait on it until the
// package's timeout instead of the row failing.
type heldRelease struct {
	started   chan struct{}
	goAhead   chan struct{}
	onceStart sync.Once
	onceGo    sync.Once
}

func newHeldRelease() *heldRelease {
	return &heldRelease{started: make(chan struct{}), goAhead: make(chan struct{})}
}

// let lets the release run, whether or not it has reached the tracer yet.
func (h *heldRelease) let() { h.onceGo.Do(func() { close(h.goAhead) }) }

func (h *heldRelease) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "delete from duty_holds") {
		h.onceStart.Do(func() { close(h.started) })
		<-h.goAhead
	}
	return ctx
}

func (h *heldRelease) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// timeIn is the key attribute, read as the RFC3339Nano string the code logs, of the first record
// the recorder kept whose message holds message, and false when no record says it yet, so a row
// can both wait for the line and read a moment it carries. It reads the records the code wrote
// rather than a rendering of them: slog's text handler truncates a record's own time to the
// millisecond and quotes what it writes, so a row that cut its output on "stopped=" and trimmed
// the quotes off asserted on a layer that is not the code under test.
//
// It takes the attribute's name because the lost and released lines carry two bounds on the
// moment the process stopped publishing, stopped and stopped_by, and a row that reads only the
// first leaves their order, their place inside the turn and their distance from the deadline
// unheld.
func timeIn(t *testing.T, recorder *logtest.Recorder, message, key string) (time.Time, bool) {
	t.Helper()
	for _, record := range recorder.Records() {
		if !strings.Contains(record.Message, message) {
			continue
		}
		value, found := "", false
		record.Attrs(func(attribute slog.Attr) bool {
			if attribute.Key != key {
				return true
			}
			value, found = attribute.Value.String(), true
			return false
		})
		if !found {
			t.Fatalf("the record %q carries no %s attribute", record.Message, key)
		}
		moment, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatalf("read the %s attribute %q of the record %q: %v", key, value, record.Message, err)
		}
		return moment, true
	}
	return time.Time{}, false
}

// loggedMessages are the messages of every record the recorder kept so far, for a row's failure
// to say what was logged instead of what it waited for.
func loggedMessages(recorder *logtest.Recorder) []string {
	records := recorder.Records()
	messages := make([]string, 0, len(records))
	for _, record := range records {
		messages = append(messages, record.Message)
	}
	return messages
}

// A holder whose row is taken from under it loses the hold at once: the deadline is pulled back
// to that moment, and the line that ends the turn says so. The line is written before the
// release, which is a database round trip the job this came from took a second over, so it is
// already in the log while the release's own statement is still running, and its stopped
// attribute names the moment the process stopped publishing rather than the moment it was
// written.
func TestTheLostLineIsWrittenBeforeTheReleaseAndDatesTheStop(t *testing.T) {
	recorder := &logtest.Recorder{}
	logtest.SetDefault(t, recorder)

	ctx := context.Background()
	database := storetest.Open(t)
	held := newHeldRelease()
	defer held.let()
	outbox := New(Deps{Store: tracedStore(t, database, held), Publisher: &recordingPublisher{}})
	hold, _ := outbox.tryHold(ctx)
	if hold == nil {
		t.Fatal("the outbox did not take the publisher hold")
	}
	ended := make(chan holdEnd, 1)
	go func() { ended <- outbox.publish(ctx, hold) }()

	// The row goes, so the next renewal affects no row and the hold is lost there and then.
	if _, err := database.Pool.Exec(ctx, `delete from duty_holds where name = $1`, publisherHold); err != nil {
		t.Fatalf("take the hold's row away: %v", err)
	}
	select {
	case <-hold.Lost():
	case <-time.After(2 * publisherHoldOptions.Renew):
		t.Fatalf("the hold was not lost within %v of its row going", 2*publisherHoldOptions.Renew)
	}
	lostAt := time.Now()

	select {
	case <-held.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the holder never ran the release")
	}
	if _, ok := timeIn(t, recorder, "lost the publisher hold", "stopped"); !ok {
		t.Fatalf("the holder's release is running and it has not logged that it lost the hold yet; it logged %q", loggedMessages(recorder))
	}
	held.let()
	if end := <-ended; end != holdLost {
		t.Fatalf("publish ended %s, want %s", end, holdLost)
	}

	stopped, ok := timeIn(t, recorder, "lost the publisher hold", "stopped")
	if !ok {
		t.Fatalf("no record of the log says %q; it says %q", "lost the publisher hold", loggedMessages(recorder))
	}
	if stopped.After(lostAt) || lostAt.Sub(stopped) > time.Second {
		t.Errorf("the line dates the stop at %s, and the hold reported itself lost at %s",
			stopped.Format(time.RFC3339Nano), lostAt.Format(time.RFC3339Nano))
	}
}

// seedFallbackBacklog seeds one comment on issue T-1, whose comment.created event the outbox has
// published already, and count copies of it held as comment_fallback_copies rows, each on an
// agent subject of its own and none of them claimed yet (next_attempt_at null, the state a row
// the batch just held back is in). It returns the comment's id.
func seedFallbackBacklog(t *testing.T, database *store.Store, broker *events.Broker, count int) string {
	t.Helper()
	const body = "a comment the outbox held copies of"
	ctx := context.Background()
	seedIssue(t, database, "T-1", nil)
	alice := model.Actor{Kind: "user", ID: "alice"}
	commentID := seedComment(t, database, "T-1", alice, body, nil)
	event := appendEvent(t, database, broker, model.Event{
		IssueKey: new("T-1"), Type: "comment.created", Actor: alice,
		Payload: model.CommentEventPayload{Comment: model.Comment{ID: commentID, IssueKey: new("T-1"), Body: body}},
	})
	if _, err := database.Pool.Exec(ctx, `update events set published_at = now() where id = $1`, event.ID); err != nil {
		t.Fatalf("publish the comment's event: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into comment_fallback_copies (comment_id, destination, event_id, session_id)
		select $1, 'notifications.agent.s' || i, $2, 's' || i from generate_series(1, $3::int) i
	`, commentID, event.ID, count); err != nil {
		t.Fatalf("hold %d fallback copies: %v", count, err)
	}
	return commentID
}

// publishedCopies is how many of commentID's held copies a pass has published.
func publishedCopies(t *testing.T, database *store.Store, commentID string) int {
	t.Helper()
	var count int
	if err := database.Pool.QueryRow(context.Background(), `
		select count(*) from comment_fallback_copies
		where comment_id = $1 and settled_at is not null and published
	`, commentID).Scan(&count); err != nil {
		t.Fatalf("count the published copies: %v", err)
	}
	return count
}

// A pass whose claim filled its batch leaves the holder due again. A row held back by a batch has
// no next_attempt_at, so no retry timer is armed to it (Outbox.nextRetry): were the pass to report
// nothing, the rows past the first batchSize its claim took would wait for an unrelated event's
// insert to wake the holder. The holder scans and passes again instead, as it does after a full
// batch, until nothing is left.
func TestAFallbackPassThatFilledItsClaimLeavesTheHolderDueAgain(t *testing.T) {
	ctx, cancel := context.WithCancel(store.WithTransactionTracking(context.Background()))
	defer cancel()
	database := storetest.Open(t)
	deps := Deps{Store: database, Publisher: &recordingPublisher{}, Broker: events.NewBroker()}
	const copies = batchSize + 1
	commentID := seedFallbackBacklog(t, database, deps.Broker, copies)

	outbox := New(deps)
	hold, _ := outbox.tryHold(ctx)
	if hold == nil {
		t.Fatal("the outbox did not take the free publisher hold")
	}
	t.Cleanup(func() { _ = hold.Release(context.Background()) })
	served := make(chan holdEnd, 1)
	go func() { served <- outbox.serve(ctx, hold) }()

	waitFor(t, 30*time.Second, fmt.Sprintf("all %d held copies published", copies), func() bool {
		return publishedCopies(t, database, commentID) == copies
	})
	cancel()
	select {
	case end := <-served:
		if end != stopping {
			t.Fatalf("serve ended %q, want it to stop with its context", end)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return once its context ended")
	}
}

// A pass whose claim failed backs the holder off rather than leaving it to pass again at once:
// the claim read no row, so no row's next_attempt_at holds the wait, and a claim that keeps
// failing would re-pass on every wake. The holder waits retry.BaseDelay, as it does after a scan
// whose statement failed.
func TestAFallbackPassWhoseClaimFailedBacksTheHolderOff(t *testing.T) {
	database := storetest.Open(t)
	deps := Deps{Store: database, Publisher: &recordingPublisher{}, Broker: events.NewBroker()}
	outbox := New(deps)
	// A claim the pool refuses, as it refuses every read a caller makes while it holds a
	// connection of its own (store.ErrNestedAcquire).
	held, release := store.HoldsConnection(store.WithTransactionTracking(context.Background()))
	defer release()

	full, failed := publishFallbackCopies(held, deps, unfenced{}, outbox.instance)

	if full || !failed {
		t.Fatalf("a pass whose claim failed reports full=%v failed=%v, want a failure and no full claim", full, failed)
	}
	if wait, ok := outbox.nextRetry(context.Background(), failed); !ok || wait != retry.BaseDelay {
		t.Fatalf("the holder's next pass is armed to %s (armed=%v), want retry.BaseDelay = %s", wait, ok, retry.BaseDelay)
	}
}

// failEveryFallbackClaim makes every update of comment_fallback_copies in database raise, which
// fails the fallback pass's claim and nothing else: the claim is that one update ... returning
// (claimFallbackCopies), while every read of the table still answers, Outbox.nextRetry's among
// them.
func failEveryFallbackClaim(t *testing.T, database *store.Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := database.Pool.Exec(ctx, `
		create function fail_the_fallback_claim() returns trigger language plpgsql as $$
		begin
			raise exception 'this test fails every claim of a fallback copy';
		end
		$$
	`); err != nil {
		t.Fatalf("write the function behind the failing claim: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		create trigger fail_the_fallback_claim before update on comment_fallback_copies
		for each row execute function fail_the_fallback_claim()
	`); err != nil {
		t.Fatalf("make every fallback claim fail: %v", err)
	}
}

// failedClaims is how many fallback claims the log says failed: the pass logs one Error line for
// each of them (publishFallbackCopies).
func failedClaims(logged *lockedBuffer) int {
	var count int
	for line := range strings.SplitSeq(logged.String(), "\n") {
		if strings.Contains(line, "dispatch outbox: claim fallback copies") {
			count++
		}
	}
	return count
}

// The holder passes again retry.BaseDelay after a pass whose claim failed, rather than waiting
// for a wake that never comes. A row the claim never reached keeps the next_attempt_at it had -
// none at all, for a row a batch has just held back - so no retry timer is armed to it
// (Outbox.nextRetry), and nothing in this test inserts an event or changes a connection: a serve
// that read the failed claim as an idle pass would leave the held copy unpublished until some
// unrelated event woke it. The pass writes one Error line for every claim that fails, so a
// second such line is the second pass and one line alone is a holder that armed nothing.
//
// The claim fails on a trigger of this test's own database rather than on a table nothing can
// read at all, because nextRetry reads comment_fallback_copies too: a read of it that fails arms
// the timer by itself, its error path answering retry.BaseDelay, whatever serve made of the
// pass's failure.
func TestAFallbackPassWhoseClaimFailedHasTheHolderPassAgain(t *testing.T) {
	var logged lockedBuffer
	logtest.SetDefault(t, slog.NewTextHandler(&logged, nil))
	ctx, cancel := context.WithCancel(store.WithTransactionTracking(context.Background()))
	defer cancel()
	database := storetest.Open(t)
	deps := Deps{Store: database, Publisher: &recordingPublisher{}, Broker: events.NewBroker()}
	seedFallbackBacklog(t, database, deps.Broker, 1)
	failEveryFallbackClaim(t, database)

	outbox := New(deps)
	hold, _ := outbox.tryHold(ctx)
	if hold == nil {
		t.Fatal("the outbox did not take the free publisher hold")
	}
	t.Cleanup(func() { _ = hold.Release(context.Background()) })
	served := make(chan holdEnd, 1)
	started := time.Now()
	go func() { served <- outbox.serve(ctx, hold) }()

	waitFor(t, 30*time.Second, "a second pass of the claim that keeps failing", func() bool {
		return failedClaims(&logged) >= 2
	})
	if waited := time.Since(started); waited < retry.BaseDelay {
		t.Fatalf("the holder passed again %s after it started, want it to wait retry.BaseDelay = %s rather than spin",
			waited, retry.BaseDelay)
	}
	cancel()
	select {
	case end := <-served:
		if end != stopping {
			t.Fatalf("serve ended %q, want it to stop with its context", end)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return once its context ended")
	}
}
