package outbox

// The idle row's witness of the test process (TestTheOutboxDoesNotScanWhileIdle): its heartbeat,
// the renewals of the publisher hold the outbox sent, the renewal deadlines the hold's row committed,
// and the judgment of whether a stall of the process explains a hold the outbox took back.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// processWatch is the row's witness of the test process itself, as databaseWitness is of the
// database: a heartbeat that beats every witnessPeriod and records each gap longer than the
// publisher hold's Renew as a stall, with the hold's loss deadline as the beat before it read it,
// and the moment the outbox sent each renewal of the hold, from the outbox's own pool
// (renewalTrace). A process that runs nothing - stopped, or given no CPU - for the 4 s to the
// hold's loss deadline sends no renewal, so the hold lapses with no failure of the database's
// behind it, and the outbox takes it back and scans, as it must (heldScans).
type processWatch struct {
	mu       sync.Mutex
	stalls   []processStall
	renewals []time.Time
	lastBeat time.Time
}

// processStall is a gap of the heartbeat, from the beat before it to the beat that ended it, or the
// part of one in which nothing the row watches ran (explains), with the publisher hold's loss
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
// is recorded, and returns the stalls and the renewals recorded by then.
func (p *processWatch) beatAfter(t *testing.T, since time.Time, what string) (stalls []processStall, renewals []time.Time) {
	t.Helper()
	waitFor(t, outboxHangGuard, what, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		if !p.lastBeat.After(since) {
			return false
		}
		stalls, renewals = slices.Clone(p.stalls), slices.Clone(p.renewals)
		return true
	})
	return stalls, renewals
}

// renewalTrace is the tracer of the outbox's own pool: it records the moment each renewal of the
// publisher hold leaves.
type renewalTrace struct{ process *processWatch }

func (r renewalTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, renewalStatement) {
		r.process.mu.Lock()
		r.process.renewals = append(r.process.renewals, time.Now())
		r.process.mu.Unlock()
	}
	return ctx
}

func (renewalTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// committedHoldDeadline is the loss deadline a renewal wrote to the publisher hold's row, and
// the database time at which that write committed. The row is the source of truth for a renewal:
// a query starting is not proof that the renewal affected it.
type committedHoldDeadline struct {
	deadline, committedAt time.Time
}

// holdDeadlineWitness records each committed renewal of the publisher hold in this test's
// database. The trigger records the database expiry, and seen derives the corresponding loss
// deadline from the same hold margin production uses.
type holdDeadlineWitness struct{ database *store.Store }

func watchHoldDeadlines(t *testing.T, database *store.Store) *holdDeadlineWitness {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []string{
		`create table idle_outbox_hold_deadlines (
			expires_at timestamptz not null,
			committed_at timestamptz not null
		)`,
		`create function record_idle_outbox_hold_deadline() returns trigger language plpgsql as $$
			begin
				if new.name = 'dispatch-outbox' and new.expires_at > old.expires_at then
					insert into idle_outbox_hold_deadlines (expires_at, committed_at)
					values (new.expires_at, clock_timestamp());
				end if;
				return new;
			end
		$$`,
		`create trigger record_idle_outbox_hold_deadline
			after update of expires_at on duty_holds
			for each row execute function record_idle_outbox_hold_deadline()`,
	} {
		if _, err := database.Pool.Exec(ctx, statement); err != nil {
			t.Fatalf("install the publisher hold deadline witness: %v", err)
		}
	}
	return &holdDeadlineWitness{database: database}
}

func (w *holdDeadlineWitness) seen(t *testing.T, until time.Time) []committedHoldDeadline {
	t.Helper()
	rows, err := w.database.Pool.Query(context.Background(), `
		select expires_at - make_interval(secs => $1), committed_at
		from idle_outbox_hold_deadlines
		where committed_at <= $2 order by committed_at
	`, publisherHoldOptions.Margin.Seconds(), until)
	if err != nil {
		t.Fatalf("read committed publisher hold deadlines: %v", err)
	}
	defer rows.Close()
	var deadlines []committedHoldDeadline
	for rows.Next() {
		var deadline committedHoldDeadline
		if err := rows.Scan(&deadline.deadline, &deadline.committedAt); err != nil {
			t.Fatalf("read a committed publisher hold deadline: %v", err)
		}
		deadlines = append(deadlines, deadline)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("finish reading committed publisher hold deadlines: %v", err)
	}
	return deadlines
}

// explains reports whether s explains the lapse of a hold whose loss deadline was deadline, the
// outbox having sent each renewal of the hold at one of renewals, the database having confirmed
// each resulting loss deadline in confirmed, and the witness each question at one of asked. It
// returns the span of s in which the process ran nothing. It does when:
//
//   - the deadline did not move back after the beat before s read it: it either still equals that
//     deadline, or exactly equals the deadline a committed renewal of the hold recorded;
//   - in the longest span of s in which the outbox sent no renewal and the witness no question -
//     so that the heartbeat's, the witness's and the renewal's goroutines all ran nothing, and
//     the heartbeat was not held back alone - the process stopped before the second renewal due
//     after the last confirmed one, and the deadline passed before it ran again, or within
//     witnessBound after, since the renewal it sends as it wakes can be answered too late. The
//     last confirmed renewal was sent TTL - Margin before the deadline, and the next is due a
//     Renew after it, so a process that ran past that one would have sent it; one Renew more
//     allows for the tick and its renewal coming late under load.
func (s processStall) explains(deadline time.Time, renewals, confirmed, asked []time.Time) (processStall, bool) {
	if deadline.Before(s.deadline) {
		return processStall{}, false
	}
	points := []time.Time{s.from, s.to}
	for _, at := range slices.Concat(renewals, asked) {
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
	if deadline.After(s.deadline) && !slices.Contains(confirmed, deadline) {
		return processStall{}, false
	}
	window := publisherHoldOptions.TTL - publisherHoldOptions.Margin
	stoppedBy := deadline.Add(-window + 2*publisherHoldOptions.Renew)
	return nothing, nothing.from.Before(stoppedBy) && deadline.Before(nothing.to.Add(witnessBound))
}

// A heartbeat can read the deadline just before a renewal succeeds, then see the process stop
// before it beats again. The final deadline is the committed renewal's, not the old heartbeat's.
func TestProcessStallExplainsTheDeadlineRenewedAfterItsLastBeat(t *testing.T) {
	lastBeat := time.Date(2026, time.November, 20, 1, 0, 0, 0, time.UTC)
	window := publisherHoldOptions.TTL - publisherHoldOptions.Margin
	renewed := lastBeat.Add(witnessPeriod)
	deadline := renewed.Add(window)
	stall := processStall{
		from:     lastBeat,
		to:       deadline.Add(witnessBound / 2),
		deadline: lastBeat.Add(window),
	}

	nothing, ok := stall.explains(deadline, []time.Time{renewed}, []time.Time{deadline}, nil)
	if !ok {
		t.Fatalf("the stall from %s to %s did not explain the renewal's deadline %s", stall.from, stall.to, deadline)
	}
	if !nothing.from.Equal(renewed) {
		t.Fatalf("the process ran nothing from %s, want the renewal at %s to end the preceding span", nothing.from, renewed)
	}
}

// A hold given up early pulls its deadline back. A process stall never explains that release,
// even when a renewal had moved the earlier deadline forward.
func TestProcessStallDoesNotExplainADeadlineMovedBack(t *testing.T) {
	lastBeat := time.Date(2026, time.November, 20, 1, 0, 0, 0, time.UTC)
	window := publisherHoldOptions.TTL - publisherHoldOptions.Margin
	renewed := lastBeat.Add(witnessPeriod)
	stall := processStall{
		from:     lastBeat,
		to:       renewed.Add(window + witnessBound/2),
		deadline: lastBeat.Add(window),
	}
	pulledBack := lastBeat.Add(window / 2)

	if _, ok := stall.explains(pulledBack, []time.Time{renewed}, []time.Time{renewed.Add(window)}, nil); ok {
		t.Fatalf("the stall explained the hold's deadline %s after it was pulled back from %s", pulledBack, stall.deadline)
	}
}

// A later deadline needs the database's record of the renewal that committed it; a query start
// alone is not proof that its update reached the hold's row.
func TestProcessStallDoesNotExplainALaterDeadlineWithoutACommittedRenewal(t *testing.T) {
	lastBeat := time.Date(2026, time.November, 20, 1, 0, 0, 0, time.UTC)
	window := publisherHoldOptions.TTL - publisherHoldOptions.Margin
	renewed := lastBeat.Add(witnessPeriod)
	deadline := renewed.Add(window)
	stall := processStall{
		from:     lastBeat,
		to:       deadline.Add(witnessBound / 2),
		deadline: lastBeat.Add(window),
	}

	if _, ok := stall.explains(deadline, []time.Time{renewed}, nil, nil); ok {
		t.Fatalf("the stall explained the later deadline %s with no committed renewal", deadline)
	}
}

// A hold can be released after a renewal advanced its deadline. Its final deadline is later than
// the last heartbeat's but earlier than the committed renewal's, so it is still a release the
// stall cannot explain.
func TestProcessStallDoesNotExplainADeadlineReleasedAfterRenewal(t *testing.T) {
	lastBeat := time.Date(2026, time.November, 20, 1, 0, 0, 0, time.UTC)
	window := publisherHoldOptions.TTL - publisherHoldOptions.Margin
	renewed := lastBeat.Add(witnessPeriod)
	renewedDeadline := renewed.Add(window)
	stall := processStall{
		from:     lastBeat,
		to:       renewedDeadline.Add(witnessBound / 2),
		deadline: lastBeat.Add(window),
	}
	released := renewed.Add(window - witnessPeriod/2)

	if _, ok := stall.explains(released, []time.Time{renewed}, []time.Time{renewedDeadline}, nil); ok {
		t.Fatalf("the stall explained the hold's deadline %s after the hold released it before the renewal's deadline %s", released, renewedDeadline)
	}
}

// stalledTake is a hold taken back after a lapse that a stall of the test process explains, as the
// idle row records it, with the evidence that explains it.
func stalledTake(claims, published int64) (heldScan, evidence) {
	deadline := time.Date(2026, time.November, 20, 1, 0, 0, 0, time.UTC)
	window := publisherHoldOptions.TTL - publisherHoldOptions.Margin
	stall := processStall{from: deadline.Add(-window), to: deadline.Add(witnessBound / 2), deadline: deadline}
	scan := heldScan{claims: claims, published: published,
		cause: cause{what: "the take of a hold that lapsed", lapsed: deadline}}
	return scan, evidence{stalls: []processStall{stall}}
}

// A take that a stall explains, and that read only its own catch-up, is left out whole.
func TestATakeAStallExplainsIsLeftOutWithItsOwnCatchUp(t *testing.T) {
	scan, saw := stalledTake(1, 0)
	if judged := scan.judge(saw); judged.plain() {
		t.Fatalf("the take's own catch-up was counted: %d by %s", judged.claims, judged.woken)
	}
	scan, saw = stalledTake(3, 2)
	if judged := scan.judge(saw); judged.plain() {
		t.Fatalf("a catch-up whose two batches published, and so read two more, was counted: %d by %s", judged.claims, judged.woken)
	}
}

// A wake queued as the hold was lost or taken back is taken by the take's catch-up, which reads a
// batch more than the take explains. That batch is counted, by name.
func TestAWakeTheTakesCatchUpTookIsCounted(t *testing.T) {
	scan, saw := stalledTake(2, 0)
	judged := scan.judge(saw)
	if !judged.plain() || judged.claims != 1 {
		t.Fatalf("a take that read 2 batches and published none was judged %+v, want 1 claim counted", judged)
	}
	if !strings.Contains(judged.woken, "a wake the take's catch-up took") {
		t.Fatalf("the counted claim was named %q, want the wake the catch-up took", judged.woken)
	}
}
