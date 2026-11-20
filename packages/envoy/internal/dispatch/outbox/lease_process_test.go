package outbox

// The idle row's witness of the test process (TestTheOutboxDoesNotScanWhileIdle): its heartbeat,
// the statements of the publisher hold's that set its loss deadline as the outbox sent them, and
// the judgment of whether a stall of the process explains the lapse of a hold the outbox took
// back, alone or with a failure of the database's.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/internal/dispatch/duty"
)

// processWatch is the row's witness of the test process itself, as databaseWitness is of the
// database: a heartbeat that beats every witnessPeriod and records each gap longer than the
// publisher hold's Renew as a stall, with the hold's loss deadline as the beat before it read it,
// and each statement of the hold's that sets its loss deadline, with the clock reading the hold
// took just before it sent it (holdTrace). A process that runs nothing - stopped, or given
// no CPU - for the 4 s to the hold's loss deadline sends no renewal, so the hold lapses with no
// failure of the database's behind it, and the outbox takes it back and scans, as it must
// (heldScans).
type processWatch struct {
	mu         sync.Mutex
	stalls     []processStall
	statements []holdStatement
	// untimed names each statement of the hold's whose context carried no clock reading of the
	// hold's (holdTrace).
	untimed  []string
	lastBeat time.Time
}

// processStall is a gap of the heartbeat, from the beat before it to the beat that ended it, or the
// part of one in which nothing the row watches ran (quiet), with the publisher hold's loss
// deadline as it stood just before the gap's first beat (zero when the outbox held no hold).
type processStall struct {
	from, to, deadline time.Time
}

func (s processStall) String() string {
	return fmt.Sprintf("from %s to %s (%s)", s.from.UTC().Format("15:04:05.000"), s.to.UTC().Format("15:04:05.000"), s.to.Sub(s.from).Round(time.Millisecond))
}

// watch starts p's heartbeat on outbox until the test ends.
func (p *processWatch) watch(t *testing.T, outbox *Outbox) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.beat(ctx, outbox)
	}()
	t.Cleanup(func() { cancel(); <-done })
}

// beat is the heartbeat, until ctx ends. Each beat reads the hold's deadline before the clock, so
// the deadline a gap carries was read before the process stopped.
func (p *processWatch) beat(ctx context.Context, outbox *Outbox) {
	deadline, last := holdDeadline(outbox), time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(witnessPeriod):
		}
		next, now := holdDeadline(outbox), time.Now()
		p.mu.Lock()
		if now.Sub(last) > publisherHoldOptions.Renew {
			p.stalls = append(p.stalls, processStall{from: last, to: now, deadline: deadline})
		}
		p.lastBeat = now
		p.mu.Unlock()
		deadline, last = next, now
	}
}

// holdDeadline is the loss deadline of the publisher hold outbox holds, zero when it holds none.
func holdDeadline(outbox *Outbox) time.Time {
	if hold := outbox.hold.Load(); hold != nil {
		return hold.Deadline()
	}
	return time.Time{}
}

// beatAfter waits until the heartbeat has beaten after since, so that every stall that ended by then
// is recorded, and returns the stalls and the hold's statements recorded by then.
func (p *processWatch) beatAfter(t *testing.T, since time.Time, what string) (stalls []processStall, statements []holdStatement) {
	t.Helper()
	var untimed []string
	waitFor(t, outboxHangGuard, what, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		if !p.lastBeat.After(since) {
			return false
		}
		stalls, statements, untimed = slices.Clone(p.stalls), slices.Clone(p.statements), slices.Clone(p.untimed)
		return true
	})
	if len(untimed) > 0 {
		t.Fatalf("these statements of the publisher hold's carried no clock reading of the hold's, so the row cannot read the deadlines they set: %s",
			strings.Join(untimed, "; "))
	}
	return stalls, statements
}

// holdStatement is a statement of the publisher hold's that sets its loss deadline when it affects
// the hold's row - the take (duty.TryHold) or a renewal (duty.Hold's renew) - with the clock
// reading the hold took just before it sent it, which the statement's context carries
// (duty.SentAt). One that affects the row sets the loss deadline to exactly that reading plus TTL
// - Margin (duty.TryHold, duty.Hold's confirm), so the row compares deadlines with it exactly: no
// second reading, and so no lag between the two, exists to bound.
type holdStatement struct {
	at   time.Time
	take bool
}

// holdTrace is the tracer of the outbox's own pool: it records each statement of the publisher
// hold's that sets the hold's loss deadline, with the hold's own clock reading from its context. A
// take or renewal whose context carries none is recorded as untimed, which fails the row by name
// (processWatch.beatAfter) rather than be timed by any other clock.
type holdTrace struct{ process *processWatch }

func (r holdTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	take := strings.HasPrefix(data.SQL, takeStatement)
	if !take && !strings.HasPrefix(data.SQL, renewalStatement) {
		return ctx
	}
	sent, ok := duty.SentAt(ctx)
	r.process.mu.Lock()
	defer r.process.mu.Unlock()
	if !ok {
		r.process.untimed = append(r.process.untimed, fmt.Sprintf("%.40q under a %T, with no clock reading of the hold's", data.SQL, ctx))
		return ctx
	}
	r.process.statements = append(r.process.statements, holdStatement{at: sent, take: take})
	return ctx
}

func (holdTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// sets reports whether deadline is the loss deadline a statement of the lost hold's own set:
// exactly that statement's clock reading plus TTL - Margin. The lost hold's own statements are its
// take - the last take before deadline, since a hold is taken before it can be lost and the next
// take comes only after its loss - and the renewals after it. A hold given up early has its
// deadline pulled back to that moment (duty.Hold's lose), which no statement of it set.
func sets(deadline time.Time, statements []holdStatement) bool {
	own := 0
	for i, statement := range statements {
		if statement.take && statement.at.Before(deadline) {
			own = i
		}
	}
	for _, statement := range statements[own:] {
		if statement.at.Before(deadline) && statement.at.Add(publisherHoldOptions.TTL-publisherHoldOptions.Margin).Equal(deadline) {
			return true
		}
	}
	return false
}

// admits reports whether deadline can be the deadline of a hold that lapsed during s: the deadline
// the beat before s read, or a later one a statement of the lost hold's own set after that beat
// (sets) - a renewal confirmed, or the hold taken, between the beat and the stop. A deadline
// earlier than the beat's was pulled back by a hold given up early, and so was a later one no
// statement set, after a renewal had moved it: neither is a lapse. A beat that read no hold read
// no deadline, so the deadline must be one the hold's own take or renewal set.
func (s processStall) admits(deadline time.Time, statements []holdStatement) bool {
	switch {
	case deadline.Before(s.deadline):
		return false
	case deadline.After(s.deadline):
		return sets(deadline, statements)
	}
	return true
}

// quiet is the longest span of s in which the outbox sent no statement of the hold's and the
// witness no question, so that the heartbeat's, the witness's and the hold's goroutines all ran
// nothing and the heartbeat was not held back alone, with deadline as its deadline.
func (s processStall) quiet(deadline time.Time, statements []holdStatement, asked []time.Time) processStall {
	points := []time.Time{s.from, s.to}
	for _, statement := range statements {
		if s.from.Before(statement.at) && statement.at.Before(s.to) {
			points = append(points, statement.at)
		}
	}
	for _, at := range asked {
		if s.from.Before(at) && at.Before(s.to) {
			points = append(points, at)
		}
	}
	slices.SortFunc(points, time.Time.Compare)
	nothing := processStall{from: s.from, to: s.from, deadline: deadline}
	for i := 1; i < len(points); i++ {
		if points[i].Sub(points[i-1]) > nothing.to.Sub(nothing.from) {
			nothing.from, nothing.to = points[i-1], points[i]
		}
	}
	return nothing
}

// stoppedBy is the moment by which a process that let a hold lapse at deadline had stopped: the
// statement that set the deadline left TTL - Margin before it, and the next renewal was due a
// Renew after that, so a process that ran past that renewal would have sent it; one Renew more
// allows for the tick and its renewal coming late under load.
func stoppedBy(deadline time.Time) time.Time {
	return deadline.Add(-(publisherHoldOptions.TTL - publisherHoldOptions.Margin) + 2*publisherHoldOptions.Renew)
}

// explains reports whether s explains the lapse of a hold whose loss deadline was deadline - the
// deadline the hold had when it was lost, which nothing is read in place of - the outbox having
// sent the hold's statements and the witness its questions at asked, and returns the span of s in
// which the process ran nothing (quiet). It does when s admits the deadline (admits), the process
// stopped by stoppedBy, and the deadline passed before the process ran again, or within
// witnessBound after, since the renewal it sends as it wakes can be answered too late.
func (s processStall) explains(deadline time.Time, statements []holdStatement, asked []time.Time) (processStall, bool) {
	if !s.admits(deadline, statements) {
		return processStall{}, false
	}
	nothing := s.quiet(deadline, statements, asked)
	return nothing, nothing.from.Before(stoppedBy(deadline)) && deadline.Before(nothing.to.Add(witnessBound))
}

// explainsWith reports whether s and window together explain the lapse of a hold whose loss
// deadline was deadline, each covering one end of it: s the start and window the end, or window
// the start and s the end, with no moment between them at which the process ran and the database
// answered. s covers the start when the process stopped by stoppedBy, and the end when it ran
// again within witnessBound of the deadline (explains). window covers the start when the last
// question the database answered before it left by stoppedBy, and the end when it holds the
// deadline (failureWindow.holds). They meet when that last question left no later than the
// process ran again, and the process stopped before the database answered again, read up to
// witnessBound past it as failureWindow.holds reads it. The deadline must be one s admits.
func (s processStall) explainsWith(window failureWindow, deadline time.Time, statements []holdStatement, asked []time.Time) (processStall, bool) {
	if !s.admits(deadline, statements) {
		return processStall{}, false
	}
	nothing := s.quiet(deadline, statements, asked)
	by := stoppedBy(deadline)
	meet := !window.from.After(nothing.to) && nothing.from.Before(window.to.Add(witnessBound))
	stallFirst := nothing.from.Before(by) && window.holds(deadline)
	databaseFirst := window.from.Before(by) && deadline.Before(nothing.to.Add(witnessBound))
	return nothing, meet && (stallFirst || databaseFirst)
}

// The cases below are judged where the row judges them, by heldScan.judge, with the evidence as
// the row records it: the scan of a hold taken back, whose cause carries the deadline the lost
// hold had (heldScans.took), the heartbeat's stalls, the hold's statements as the trace records
// them, and the witness's windows and questions. Each clock reading the hold takes before a
// statement is a whole moment, which the statement carries to the trace (duty.SentAt).

// lossWindow is TTL - Margin: how long after a statement of the hold's the deadline it sets falls.
var lossWindow = publisherHoldOptions.TTL - publisherHoldOptions.Margin

// fixtureStart is the moment the cases below count from.
var fixtureStart = time.Date(2026, time.November, 20, 1, 0, 0, 0, time.UTC)

// traced is a statement of the hold's sent at sent, as the trace records it.
func traced(sent time.Time, take bool) holdStatement {
	return holdStatement{at: sent, take: take}
}

// renewals is a hold taken at taken and renewed every Renew for n renewals, as the trace sees it.
func renewals(taken time.Time, n int) []holdStatement {
	statements := []holdStatement{traced(taken, true)}
	for i := 1; i <= n; i++ {
		statements = append(statements, traced(taken.Add(time.Duration(i)*publisherHoldOptions.Renew), false))
	}
	return statements
}

// takenBack is the first scan of a hold taken back after the last one ended at lapsed with no
// renewal the database left unanswered, as heldScans.took names it.
func takenBack(lapsed time.Time) heldScan {
	return heldScan{claims: 1, cause: cause{
		what:   fmt.Sprintf("the take of a hold that lapsed or was given up at %s with no renewal the database left unanswered", lapsed.UTC().Format("15:04:05.000")),
		lapsed: lapsed,
	}}
}

// leftOut fails t unless the row leaves scan out whole, and returns the fault it names.
func leftOut(t *testing.T, scan heldScan, saw evidence, what string) string {
	t.Helper()
	judged := scan.judge(saw)
	if judged.plain() {
		t.Fatalf("%s: the row counted %d claims by %s, want the scan left out", what, judged.claims, judged.woken)
	}
	return judged.fault
}

// counted fails t unless the row counts claims of scan, and returns what it names as their cause.
func counted(t *testing.T, scan heldScan, saw evidence, claims int64, what string) string {
	t.Helper()
	judged := scan.judge(saw)
	if !judged.plain() || judged.claims != claims {
		t.Fatalf("%s: the row judged %d claims plain=%v (fault %q, woken %q), want %d counted", what, judged.claims, judged.plain(), judged.fault, judged.woken, claims)
	}
	return judged.woken
}

// A renewal the hold confirmed after the heartbeat's last beat moves the deadline past the one
// that beat read. A stop right after it lapses the hold at the renewal's own deadline, which the
// row reads from the hold and accepts because a statement of the hold's set it.
func TestAStallExplainsTheDeadlineARenewalSetAfterItsLastBeat(t *testing.T) {
	statements := renewals(fixtureStart, 3)
	renewed := fixtureStart.Add(3 * publisherHoldOptions.Renew)
	lastBeat := renewed.Add(-witnessPeriod / 2)
	stall := processStall{from: lastBeat, to: renewed.Add(lossWindow + witnessBound/2), deadline: renewed.Add(lossWindow - publisherHoldOptions.Renew)}
	saw := evidence{stalls: []processStall{stall}, statements: statements}

	fault := leftOut(t, takenBack(renewed.Add(lossWindow)), saw, "a stop right after a renewal the heartbeat had not read")
	if want := "ran nothing from " + traced(renewed, false).at.UTC().Format("15:04:05.000"); !strings.Contains(fault, want) {
		t.Fatalf("the row named %q, want the span from the renewal on (%s)", fault, want)
	}
	if fault := leftOut(t, takenBack(stall.deadline), saw, "the same stop with the deadline the beat read"); fault == "" {
		t.Fatal("no fault named")
	}
}

// The database can commit a renewal the stopped process never reads the answer to: the hold then
// lapses at the deadline it had, which the beat read, and the row reads that deadline from the
// hold, never the later one the unconfirmed renewal would have set.
func TestAStallExplainsTheDeadlineARenewalLeftUnconfirmed(t *testing.T) {
	statements := renewals(fixtureStart, 3)
	unconfirmed := fixtureStart.Add(3 * publisherHoldOptions.Renew)
	deadline := unconfirmed.Add(lossWindow - publisherHoldOptions.Renew)
	for _, stop := range []time.Duration{3100 * time.Millisecond, 3250 * time.Millisecond, 3400 * time.Millisecond} {
		stall := processStall{from: unconfirmed.Add(-witnessPeriod / 2), to: unconfirmed.Add(stop), deadline: deadline}
		leftOut(t, takenBack(deadline), evidence{stalls: []processStall{stall}, statements: statements},
			fmt.Sprintf("a %s stop between a renewal's commit and its confirm", stop))
	}
}

// After a hold the database lapsed, the outbox takes a new one, and a stop after the heartbeat
// read the new hold's deadline and before its first renewal lapses it at its take's deadline. The
// last hold's renewals are no deadline of the new hold's. A beat that read no hold - the stop
// came before the heartbeat beat after the take - is explained by the take's own deadline.
func TestAStallExplainsANewHoldsFirstDeadline(t *testing.T) {
	statements := renewals(fixtureStart, 3)
	taken := fixtureStart.Add(9 * publisherHoldOptions.Renew)
	statements = append(statements, traced(taken, true))
	deadline := taken.Add(lossWindow)
	stall := processStall{from: taken.Add(200 * time.Millisecond), to: deadline.Add(300 * time.Millisecond), deadline: deadline}
	leftOut(t, takenBack(deadline), evidence{stalls: []processStall{stall}, statements: statements},
		"a stop before the new hold's first renewal")
	stall.from, stall.deadline = taken.Add(-witnessPeriod/2), time.Time{}
	leftOut(t, takenBack(deadline), evidence{stalls: []processStall{stall}, statements: statements},
		"a stop before the heartbeat read the new hold")
}

// A hold given up early pulls its deadline back to that moment. A stall that the timing alone
// would accept never explains it.
func TestAStallDoesNotExplainADeadlineMovedBack(t *testing.T) {
	statements := renewals(fixtureStart, 3)
	renewed := fixtureStart.Add(3 * publisherHoldOptions.Renew)
	deadline := renewed.Add(lossWindow)
	givenUp := deadline.Add(-300 * time.Millisecond)
	stall := processStall{from: renewed.Add(witnessPeriod), to: givenUp, deadline: deadline}
	saw := evidence{stalls: []processStall{stall}, statements: statements}

	if nothing := stall.quiet(givenUp, statements, nil); !nothing.from.Before(stoppedBy(givenUp)) || !givenUp.Before(nothing.to.Add(witnessBound)) {
		t.Fatalf("the fixture's stall %s does not meet the timing for %s: the case would not reach the rule it is for", nothing, givenUp)
	}
	counted(t, takenBack(givenUp), saw, 1, "a hold given up 300 ms before the deadline the beat read")
}

// A hold given up after a renewal moved its deadline past the beat's has a deadline later than
// the beat's and earlier than the renewal's own, which no statement of it set (accept-6 D1, review
// 6 F2): the row counts its take, though the timing alone would accept the stall.
func TestAStallDoesNotExplainAHoldGivenUpAfterARenewal(t *testing.T) {
	statements := renewals(fixtureStart, 3)
	renewed := fixtureStart.Add(3 * publisherHoldOptions.Renew)
	for _, before := range []time.Duration{250 * time.Millisecond, 400 * time.Millisecond} {
		givenUp := renewed.Add(lossWindow - before)
		stall := processStall{from: renewed.Add(100 * time.Millisecond), to: givenUp, deadline: renewed.Add(lossWindow - publisherHoldOptions.Renew)}
		saw := evidence{stalls: []processStall{stall}, statements: statements}
		if nothing := stall.quiet(givenUp, statements, nil); !nothing.from.Before(stoppedBy(givenUp)) || !givenUp.Before(nothing.to.Add(witnessBound)) {
			t.Fatalf("the fixture's stall %s does not meet the timing for %s", nothing, givenUp)
		}
		counted(t, takenBack(givenUp), saw, 1, fmt.Sprintf("a hold given up %s before its renewal's deadline", before))
	}
}

// A hold taken after a stall lapsed the last one, then given up for no reason while the process
// runs, is counted even when the moment it was given up is a deadline the last hold's renewal
// would have set: that renewal is no statement of the new hold's (deep audit 6, F1 run 1).
func TestAStallDoesNotExplainAHoldGivenUpAtAnotherHoldsDeadline(t *testing.T) {
	statements := renewals(fixtureStart, 3)
	unconfirmed := fixtureStart.Add(3 * publisherHoldOptions.Renew)
	lapsed := unconfirmed.Add(lossWindow - publisherHoldOptions.Renew)
	stall := processStall{from: unconfirmed.Add(time.Millisecond), to: lapsed.Add(650 * time.Millisecond), deadline: lapsed}
	taken := stall.to.Add(10 * time.Millisecond)
	givenUp := unconfirmed.Add(lossWindow)
	statements = append(statements, traced(taken, true))
	saw := evidence{stalls: []processStall{stall}, statements: statements}

	leftOut(t, takenBack(lapsed), saw, "the first hold's lapse")
	counted(t, takenBack(givenUp), saw, 1, "the second hold given up while the process ran")
}

// Two stops, the second soon after the hold the first lapsed is taken back: the second take is
// left out with the stall that lapsed its hold, and named by it.
func TestTheSecondOfTwoStallsExplainsTheSecondLapse(t *testing.T) {
	statements := renewals(fixtureStart, 3)
	renewed := fixtureStart.Add(3 * publisherHoldOptions.Renew)
	first := processStall{from: renewed.Add(200 * time.Millisecond), to: renewed.Add(4700 * time.Millisecond), deadline: renewed.Add(lossWindow)}
	taken := first.to.Add(10 * time.Millisecond)
	statements = append(statements, traced(taken, true))
	deadline := taken.Add(lossWindow)
	second := processStall{from: taken.Add(100 * time.Millisecond), to: taken.Add(4600 * time.Millisecond), deadline: deadline}
	saw := evidence{stalls: []processStall{first, second}, statements: statements}

	leftOut(t, takenBack(first.deadline), saw, "the first take")
	fault := leftOut(t, takenBack(deadline), saw, "the second take")
	if want := second.from.UTC().Format("15:04:05.000"); !strings.Contains(fault, "ran nothing from "+want) {
		t.Fatalf("the second take was left out as %q, want it named by the stall from %s", fault, want)
	}
}

// A stop that ends too long before the deadline to explain the lapse, and a failure of the
// database from before it ran again to past the deadline, together cover the lapse, as do a
// failure that ends while the process is stopped and a stop that lasts past the deadline. Each
// explains part of it and neither all, and the row leaves the take out naming both (round 5's
// note on two causes in one lapse).
func TestAStallAndAFailureTogetherExplainALapse(t *testing.T) {
	statements := renewals(fixtureStart, 3)
	renewed := fixtureStart.Add(3 * publisherHoldOptions.Renew)
	deadline := renewed.Add(lossWindow)

	stall := processStall{from: renewed.Add(500 * time.Millisecond), to: deadline.Add(-750 * time.Millisecond), deadline: deadline}
	window := failureWindow{from: renewed.Add(450 * time.Millisecond), to: deadline.Add(100 * time.Millisecond), err: "timeout"}
	saw := evidence{witnessed: witnessed{windows: []failureWindow{window}}, stalls: []processStall{stall}, statements: statements}
	if _, alone := stall.explains(deadline, statements, nil); alone {
		t.Fatal("the stop alone explains the lapse: the case would not reach the rule it is for")
	}
	fault := leftOut(t, takenBack(deadline), saw, "a stop, then a failure to past the deadline")
	if !strings.Contains(fault, "ran nothing "+stall.String()) || !strings.Contains(fault, window.String()) {
		t.Fatalf("the row left the take out as %q, want both the stop and the failure named", fault)
	}

	window = failureWindow{from: renewed.Add(300 * time.Millisecond), to: deadline.Add(-1200 * time.Millisecond), err: "timeout"}
	stall = processStall{from: deadline.Add(-1300 * time.Millisecond), to: deadline.Add(200 * time.Millisecond), deadline: deadline}
	saw = evidence{witnessed: witnessed{windows: []failureWindow{window}}, stalls: []processStall{stall}, statements: statements}
	leftOut(t, takenBack(deadline), saw, "a failure, then a stop to past the deadline")
}

// The same stop and failure with a span between them in which the process ran and the database
// answered do not cover the lapse: a renewal sent then would have kept the hold, and the row
// counts the take.
func TestAStallAndAFailureWithAGapBetweenThemDoNotExplainALapse(t *testing.T) {
	statements := renewals(fixtureStart, 3)
	renewed := fixtureStart.Add(3 * publisherHoldOptions.Renew)
	deadline := renewed.Add(lossWindow)
	stall := processStall{from: renewed.Add(500 * time.Millisecond), to: deadline.Add(-750 * time.Millisecond), deadline: deadline}
	window := failureWindow{from: deadline.Add(-550 * time.Millisecond), to: deadline.Add(100 * time.Millisecond), err: "timeout"}
	saw := evidence{witnessed: witnessed{windows: []failureWindow{window}}, stalls: []processStall{stall}, statements: statements}
	counted(t, takenBack(deadline), saw, 1, "a stop, 200 ms of a running process and an answering database, then a failure")
}

// A take explains its own catch-up, one batch, and a scan of it that reads another took a wake
// queued as the hold was lost or taken back: that batch is counted, by name.
func TestATakeExplainsOnlyItsOwnCatchUp(t *testing.T) {
	statements := renewals(fixtureStart, 3)
	renewed := fixtureStart.Add(3 * publisherHoldOptions.Renew)
	deadline := renewed.Add(lossWindow)
	stall := processStall{from: renewed.Add(200 * time.Millisecond), to: deadline.Add(witnessBound / 2), deadline: deadline}
	saw := evidence{stalls: []processStall{stall}, statements: statements}

	leftOut(t, takenBack(deadline), saw, "a take that read its own catch-up")
	scan := takenBack(deadline)
	scan.claims = 2
	if woken := counted(t, scan, saw, 1, "a take whose scan read a second batch"); !strings.Contains(woken, "a wake the take's catch-up took") {
		t.Fatalf("the counted batch was named %q, want the wake the take's catch-up took", woken)
	}
}

// A LISTEN connection's catch-up explains its own batch, the one its wake starts, as a take's does:
// a wake that lands while that batch runs reads one more, which the row counts, by name.
func TestALISTENCatchUpExplainsOnlyItsOwnBatch(t *testing.T) {
	lost := fixtureStart.Add(time.Second)
	window := failureWindow{from: fixtureStart, to: lost.Add(time.Second), err: "connection refused"}
	scan := heldScan{claims: 1, cause: cause{
		what:   "a new LISTEN connection's catch-up, after the last one was lost (connection refused)",
		reason: true, failedAt: []time.Time{lost},
	}}
	saw := evidence{witnessed: witnessed{windows: []failureWindow{window}}}

	leftOut(t, scan, saw, "a catch-up that read its own batch")
	scan.claims = 2
	if woken := counted(t, scan, saw, 1, "a catch-up whose scan read a second batch"); !strings.Contains(woken, "a wake the scan took") {
		t.Fatalf("the counted batch was named %q, want the wake the scan took", woken)
	}
}

// A LISTEN connection that comes back while the outbox holds no hold sends its catch-up wake to a
// holder that has none, and the scan of the hold taken next takes it (Outbox.listen): that scan
// reads the take's batch and the connection's, and is left out when the witness saw the
// connection's loss. A loss no window holds counts the connection's batch, by name.
func TestATakeAlsoExplainsTheCatchUpOfALISTENConnectionBackBeforeIt(t *testing.T) {
	statements := renewals(fixtureStart, 3)
	renewed := fixtureStart.Add(3 * publisherHoldOptions.Renew)
	deadline := renewed.Add(lossWindow)
	lost := renewed.Add(300 * time.Millisecond)
	window := failureWindow{from: renewed.Add(100 * time.Millisecond), to: deadline.Add(200 * time.Millisecond), err: "connection refused"}
	scan := heldScan{claims: 2, cause: cause{
		what:   "the publisher hold taken back after it lapsed, the database having left 3 of its renewals unanswered before then",
		reason: true, failedAt: []time.Time{deadline}, someFailedAt: []time.Time{deadline.Add(-time.Second)}, lapsed: deadline,
	}, listen: &cause{what: listenBackWhat("connection refused"), reason: true, failedAt: []time.Time{lost}}}
	saw := evidence{witnessed: witnessed{windows: []failureWindow{window}}, statements: statements}

	leftOut(t, scan, saw, "a take whose scan read the catch-up of a connection that came back before it")
	scan.claims = 3
	counted(t, scan, saw, 1, "a take whose scan read its own batch, the connection's and one more")
	scan.claims = 2
	scan.listen.failedAt = []time.Time{deadline.Add(time.Second)}
	if woken := counted(t, scan, saw, 1, "the same take with the connection's loss outside every window"); !strings.Contains(woken, "LISTEN") {
		t.Fatalf("the counted batch was named %q, want the LISTEN connection's", woken)
	}
}

// The trace records each take and renewal of the hold with the clock reading the hold itself took
// before it sent it, so the deadline it set is exactly that reading plus TTL - Margin; a take or
// renewal whose context carries no reading is named as untimed. No renewal is answered, so the
// deadline stays the take's however late the test reads it.
func TestTheHoldTraceRecordsTheHoldsOwnReading(t *testing.T) {
	p := &processWatch{}
	trace := holdTrace{process: p}
	hold, err := duty.TryHold(context.Background(), tracedHolds{trace}, publisherHold, uuid.New(), publisherHoldOptions)
	if err != nil || hold == nil {
		t.Fatalf("take a hold with no database: %v", err)
	}
	defer func() { _ = hold.Release(context.Background()) }()
	p.mu.Lock()
	statements := slices.Clone(p.statements)
	p.mu.Unlock()
	if len(statements) == 0 || !statements[0].take || !statements[0].at.Add(lossWindow).Equal(hold.Deadline()) {
		t.Fatalf("the take was recorded as %+v, want one take whose reading plus TTL - Margin is the hold's deadline %s", statements, hold.Deadline())
	}
	if !sets(hold.Deadline(), statements) || sets(hold.Deadline().Add(-time.Microsecond), statements) {
		t.Fatal("sets does not tie the take's deadline, and that deadline alone, to the take")
	}
	trace.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: renewalStatement + " = now()"})
	if len(p.untimed) != 1 {
		t.Fatalf("a renewal with no reading in its context was named untimed %d times, want once: %q", len(p.untimed), p.untimed)
	}
}

// tracedHolds runs a hold's statements through trace with no database. It answers the take as one
// that affected the hold's row, and each renewal as a failure, which leaves the deadline the take
// set (duty.Hold's renew).
type tracedHolds struct{ trace holdTrace }

func (h tracedHolds) Exec(ctx context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	h.trace.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: sql})
	if strings.HasPrefix(sql, renewalStatement) {
		return pgconn.CommandTag{}, errNoDatabase
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (tracedHolds) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

// errNoDatabase is tracedHolds' answer to a renewal.
var errNoDatabase = errors.New("no database answers this renewal")
