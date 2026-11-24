package wakepace

// The two queues in front of one limiter (dispatch://INFRA-5682 R1), read here as the daemon's
// lanes read them: a wake with a message waiting for its role goes ahead of the wakes with none,
// the other queue still takes one turn in four, a wake taken out of its queue gives its turn to the
// next head, and a wake that waits has no moment of its own but the estimate its place gives.
//
// Each test drives the queues on a clock of its own (testClock): nothing here sleeps, and a turn is
// a moment the test moves that clock to.

import (
	"slices"
	"testing"
	"time"
)

// testClock is the clock the queues read: a now the test moves and the timers armed on it, fired in
// order as it moves (step). It is for one goroutine at a time, which is what these tests are.
type testClock struct {
	now    time.Time
	timers []*testTimer
}

type testTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func newTestClock() *testClock { return &testClock{now: origin} }

func (c *testClock) Now() time.Time { return c.now }

func (c *testClock) after(d time.Duration, f func()) func() bool {
	t := &testTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		if t.stopped || t.fired {
			return false
		}
		t.stopped = true
		return true
	}
}

// step moves the clock to at, firing every timer that falls due on the way, earliest first.
func (c *testClock) step(at time.Time) {
	for {
		var next *testTimer
		for _, t := range c.timers {
			if t.stopped || t.fired || t.at.After(at) {
				continue
			}
			if next == nil || t.at.Before(next.at) {
				next = t
			}
		}
		if next == nil {
			c.now = at
			return
		}
		next.fired = true
		c.now = next.at
		next.f()
	}
}

// queuesOn builds the queues over a limiter of the operator's rate in bursts of one, with no
// jitter, on c: a test reads an exact schedule, one turn every interval.
func queuesOn(c *testClock) *Queues {
	l := New(rate, 1, nil)
	return NewQueues(l.Reserve, l.Interval(), c.Now, c.after)
}

// letOuts is the owners of the wakes the queues let out, in order, as a test waits them: each wake
// appends its owner when its turn comes.
type letOuts struct {
	ran []string
}

// wait places and waits owner's wake, marked at marked, and answers its ticket. A wake whose turn
// is now runs here, as the caller of Wait runs it (Ticket.Wait).
func (l *letOuts) wait(q *Queues, owner string, marked time.Time) *Ticket {
	t := q.Place(owner, marked)
	if t.Wait(func() { l.ran = append(l.ran, owner) }) {
		l.ran = append(l.ran, owner)
	}
	return t
}

// A wake with a message waiting for its role goes ahead of the wakes with none, and while both
// queues hold wakes the turn after three message wakes goes to the other queue's head. The other
// queue is owed nothing for the turns it was empty: it takes one turn when it fills, and three
// message turns follow.
func TestTheShareGivesTheOtherQueueOneTurnInFourAndBuildsNoCredit(t *testing.T) {
	c := newTestClock()
	q := queuesOn(c)
	out := &letOuts{}
	marked := c.Now()

	// The burst lets the first wake out at once, so every wake after it waits for a turn.
	out.wait(q, "first", time.Time{})
	for _, owner := range []string{"other-1", "other-2"} {
		out.wait(q, owner, time.Time{})
	}
	for _, owner := range []string{"message-1", "message-2", "message-3", "message-4", "message-5"} {
		out.wait(q, owner, marked)
	}

	c.step(c.Now().Add(7 * interval))

	want := []string{"first", "message-1", "message-2", "message-3", "other-1", "message-4", "message-5", "other-2"}
	if !slices.Equal(out.ran, want) {
		t.Fatalf("the queues let out %v, want the message wakes first with one turn in four for the other queue, %v", out.ran, want)
	}
}

// The other queue builds no credit while it is empty: a wake placed in it after ten message turns
// takes the next turn, and the three message turns after that one come before its next.
func TestTheOtherQueueTakesOneTurnWhenItFillsAndNoMore(t *testing.T) {
	c := newTestClock()
	q := queuesOn(c)
	out := &letOuts{}
	marked := c.Now()

	out.wait(q, "first", time.Time{})
	for i := range 13 {
		out.wait(q, messageOwner(i), marked)
	}
	c.step(c.Now().Add(10 * interval))
	if want := append([]string{"first"}, messageOwners(0, 10)...); !slices.Equal(out.ran, want) {
		t.Fatalf("with the other queue empty the queues let out %v, want the message wakes in their order %v", out.ran, want)
	}

	out.wait(q, "other-1", time.Time{})
	out.wait(q, "other-2", time.Time{})
	c.step(c.Now().Add(5 * interval))

	want := append([]string{"first"}, messageOwners(0, 10)...)
	want = append(want, "other-1", messageOwner(10), messageOwner(11), messageOwner(12), "other-2")
	if !slices.Equal(out.ran, want) {
		t.Fatalf("once the other queue filled the queues let out %v, want one turn for it, three message turns, then its next, %v",
			out.ran, want)
	}
}

// A wake taken out of its queue gives the turn it would have had to the next head, and the latest
// turn the queues hold goes with it: the wake behind it runs at that turn rather than at one of its
// own, and the wakes let out are as many as the turns the limiter gave.
func TestAStoppedWakeGivesItsTurnToTheNextHead(t *testing.T) {
	c := newTestClock()
	q := queuesOn(c)
	out := &letOuts{}

	out.wait(q, "first", time.Time{})
	stopped := out.wait(q, "stopped", time.Time{})
	out.wait(q, "behind", time.Time{})
	if !stopped.Stop() {
		t.Fatal("the wake was not waiting in its queue, want it stopped there")
	}

	c.step(c.Now().Add(interval))

	if want := []string{"first", "behind"}; !slices.Equal(out.ran, want) {
		t.Fatalf("the queues let out %v, want the wake behind the stopped one at its turn, %v", out.ran, want)
	}
}

// A wake that waits has no moment of its own: its estimate is the turn the queues hold at its place
// in the order they let wakes out, and it moves as the queues do. A place a message mark moves
// takes the estimate of its new place, and a wake let out answers the moment it was let out at.
func TestAnEstimateIsReadFromThePlaceAndMovesWithIt(t *testing.T) {
	c := newTestClock()
	q := queuesOn(c)
	out := &letOuts{}
	marked := c.Now()

	out.wait(q, "first", time.Time{})
	early := out.wait(q, "early", time.Time{})
	late := out.wait(q, "late", time.Time{})
	if want := c.Now().Add(interval); !early.Estimate().Equal(want) {
		t.Fatalf("the first waiting wake is estimated at %s, want the next turn, %s", early.Estimate(), want)
	}
	if want := c.Now().Add(2 * interval); !late.Estimate().Equal(want) {
		t.Fatalf("the second waiting wake is estimated at %s, want the turn after, %s", late.Estimate(), want)
	}

	late.Mark(marked)

	if want := c.Now().Add(interval); !late.Estimate().Equal(want) {
		t.Fatalf("the marked wake is estimated at %s, want the next turn, %s: a message wake goes ahead", late.Estimate(), want)
	}
	if want := c.Now().Add(2 * interval); !early.Estimate().Equal(want) {
		t.Fatalf("the wake with no message is estimated at %s, want the turn after the marked one, %s", early.Estimate(), want)
	}

	at := c.Now().Add(interval)
	c.step(at)

	if got, ok := late.Out(); !ok || !got.At.Equal(at) || got.Queue != Message || got.Turns != 0 {
		t.Fatalf("the marked wake was let out as %+v (held %v), want the message queue at %s with no wake let out while it waited", got, ok, at)
	}
	if !late.Estimate().Equal(at) {
		t.Fatalf("the wake let out is estimated at %s, want the moment it was let out, %s", late.Estimate(), at)
	}
}

// An unbound place holds its order and draws no turn: a turn passes over it to the next head that
// waits, and the wake that takes the place up runs at the next turn of its own place.
func TestAPlaceNoWakeWaitsOnDrawsNoTurn(t *testing.T) {
	c := newTestClock()
	q := queuesOn(c)
	out := &letOuts{}

	out.wait(q, "first", time.Time{})
	held := q.Place("held", time.Time{})
	out.wait(q, "behind", time.Time{})
	if held.Bound() {
		t.Fatal("the place no wake waits on reads as bound, want it holding its order alone")
	}

	c.step(c.Now().Add(interval))

	if want := []string{"first", "behind"}; !slices.Equal(out.ran, want) {
		t.Fatalf("the queues let out %v, want the turn to pass over the place no wake waits on, %v", out.ran, want)
	}

	if held.Wait(func() { out.ran = append(out.ran, "held") }) {
		t.Fatal("the place's wake was let out where it bound, want it waiting for a turn of its own")
	}
	c.step(c.Now().Add(interval))

	if want := []string{"first", "behind", "held"}; !slices.Equal(out.ran, want) {
		t.Fatalf("the queues let out %v, want the place's wake at the next turn, %v", out.ran, want)
	}
}

// messageOwner is the owner of the nth message wake of a test, and messageOwners those from first
// for count.
func messageOwner(n int) string { return "message-" + string(rune('a'+n)) }

func messageOwners(first, count int) []string {
	owners := make([]string, count)
	for i := range owners {
		owners[i] = messageOwner(first + i)
	}
	return owners
}
