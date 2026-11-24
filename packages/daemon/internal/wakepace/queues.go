package wakepace

import (
	"slices"
	"sync"
	"time"
)

// Queues are the two queues in front of the daemon's one limiter (dispatch://INFRA-5682 R1): a
// classifier with priority queues in front of one rate limiter, with a share for the lower queue
// so that it does not starve, which is how Kubernetes API Priority and Fairness and Linux tc-prio
// under a shaper meet the same need. A paced wake of a role with a message waiting for it (Message)
// goes ahead of every other paced wake (Other), and the limiter, today's, still decides when each
// turn comes, so the paced wakes of a minute are as many as before.
//
// A waiting wake holds a place, not a moment. The turns are the queues': each wake that waits for
// one draws one from the limiter (Reserve, charged as it always was), and at each turn the queues
// let out the head of the message queue, except that while both queues hold wakes the turn after
// three message wakes in a row goes to the head of the other queue (share). The count starts again
// at that turn, and the other queue is owed nothing for turns when it was empty. So a message wake
// with k message wakes ahead of it runs within k + 2 + ⌊k/3⌋ turns of its mark, and a wake with no
// message runs at least one turn in four while both queues hold wakes. A wake taken out of its
// queue (Ticket.Stop) gives its turn to the next head, and the latest turn the queues hold goes,
// its token left spent, as a dropped wake's always was.
//
// Every caller passes nothing but what it holds: the queues read their own clock (now, after),
// which the daemon builds from the machines' clock, so a test that moves that clock moves every
// turn. A let-out runs the wake's own function: on the timer's goroutine for the turn whose timer
// came, and through the clock, armed to fire at once, for every other wake that turn lets out,
// since a caller of Wait may hold a lock the wake's work takes. The clock is what carries them so
// that every let-out of a turn reaches its wake on the clock that gave the turn, which is what a
// test that moves its own clock reads. Queues are safe for use from several goroutines.
type Queues struct {
	reserve  func(now time.Time) time.Time
	interval time.Duration
	now      func() time.Time
	after    func(d time.Duration, f func()) (stop func() bool)

	mu sync.Mutex
	// waits is each queue's wakes in the order they got their place, Message's and Other's.
	waits [2][]*Ticket
	// run is the message wakes let out in a row since the other queue last had a turn.
	run int
	// turns is how many wakes the queues have let out, which a wake's let-out reads its wait from.
	turns uint64
	// slots is the turns drawn from the limiter and still to come, earliest first: one for each
	// wake that waits for its turn (Ticket.Wait), none for a place nothing waits on yet.
	slots []*slot
}

// Queue is which of the two queues a wake waits in.
type Queue int

const (
	// Other is the queue of every paced wake that carries no message.
	Other Queue = iota
	// Message is the queue of the paced wakes of a role with a message waiting for it: a wake whose
	// start row's kind is role_message or agent_message, or one the Envoy listener reported such a
	// wake for while it waited (dispatch://INFRA-5682 R1).
	Message
)

// String is the queue's name as the line a wake writes spells it (supervise's WakeRanFields).
func (q Queue) String() string {
	if q == Message {
		return "message"
	}
	return "other"
}

// share is how many message wakes in a row the queues let out while the other queue holds a wake
// before the next turn goes to the other queue: one turn in four is the other queue's.
const share = 3

// slot is one turn drawn from the limiter: its moment and the timer armed for it.
type slot struct {
	at   time.Time
	stop func() bool
}

// Ticket is one wake's place in the queues. Its run is nil while the place is held for a wake that
// has not reached its gate yet (Queues.Place): such a place keeps its order and draws no turn, and
// a turn passes over it to the next head that waits.
type Ticket struct {
	q      *Queues
	owner  string
	queue  Queue
	placed time.Time
	marked time.Time
	// from is the queues' turns when the wake got its place.
	from  uint64
	run   func()
	state ticketState
	out   LetOut
}

type ticketState int

const (
	queued ticketState = iota
	letOut
	stopped
)

// LetOut is what the queues say of a wake they let out, for the line it writes as it runs: the
// queue it came from, how many other wakes the queues let out while it waited, when it got its
// place, when its message was marked (the zero time for a wake of the other queue) and when it was
// let out.
type LetOut struct {
	Queue  Queue
	Turns  uint64
	Placed time.Time
	Marked time.Time
	At     time.Time
}

// NewQueues builds the two queues over a limiter's Reserve and its Interval (Limiter), on a clock:
// now is its time and after arms f to run d from now, answering the stop of that timer.
func NewQueues(reserve func(now time.Time) time.Time, interval time.Duration, now func() time.Time,
	after func(d time.Duration, f func()) (stop func() bool)) *Queues {
	return &Queues{reserve: reserve, interval: interval, now: now, after: after}
}

// Interval is what one wake of the queues' limiter costs, the least spacing between two turns.
func (q *Queues) Interval() time.Duration { return q.interval }

// Place gives owner's wake its place now, ahead of the wake itself: the boot's pass places each
// relaunch in its queue in the order it decides and only then posts the launches, which reach their
// gates in no order (Ticket.Wait). marked is when the wake's message was marked, the zero time for
// a wake of the other queue.
func (q *Queues) Place(owner string, marked time.Time) *Ticket {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.place(owner, marked)
}

// Wait places owner's wake and waits it for its turn (Place, Ticket.Wait).
func (q *Queues) Wait(owner string, marked time.Time, run func()) (*Ticket, bool) {
	t := q.Place(owner, marked)
	return t, t.Wait(run)
}

func (q *Queues) place(owner string, marked time.Time) *Ticket {
	t := &Ticket{q: q, owner: owner, marked: marked, placed: q.now(), from: q.turns}
	if !marked.IsZero() {
		t.queue = Message
	}
	q.waits[t.queue] = append(q.waits[t.queue], t)
	return t
}

// Mark moves every wake of owner still waiting in the other queue to the message queue's tail, its
// message marked at at: the Envoy listener reported a message wake for owner's claim while it
// waited.
func (q *Queues) Mark(owner string, at time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, t := range slices.Clone(q.waits[Other]) {
		if t.owner == owner {
			q.mark(t, at)
		}
	}
}

// Estimate is when the first wake of owner still waiting is likely to be let out, and whether the
// queues hold one (Ticket.Estimate).
func (q *Queues) Estimate(owner string) (time.Time, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, waits := range q.waits {
		for _, t := range waits {
			if t.owner == owner {
				return q.estimate(t), true
			}
		}
	}
	return time.Time{}, false
}

// Wait waits t for its turn, run being the wake's work: it answers true when the turn is now, and
// then the caller runs the wake itself, and otherwise run is called when the turn comes. A place
// stopped or let out already is placed again at its queue's tail.
//
// A turn that has come for another wake while these queues settled lets that wake out through the
// clock, armed to fire at once (letOutSoon): the caller may hold a lock that wake's work takes.
func (t *Ticket) Wait(run func()) bool {
	q := t.q
	q.mu.Lock()
	t.run = run
	if t.state != queued {
		t.state, t.placed, t.from = queued, q.now(), q.turns
		q.waits[t.queue] = append(q.waits[t.queue], t)
	}
	out := q.settle(q.now())
	now := false
	others := make([]func(), 0, len(out))
	for _, other := range out {
		if other == t {
			now = true
			continue
		}
		others = append(others, other.run)
	}
	q.mu.Unlock()
	q.letOutSoon(others)
	return now
}

// letOutSoon arms each let-out to run at once on the queues' clock, one timer each, in the order
// the turns let them out. The caller holds no lock of the queues, and each wake's work takes
// whatever lock it needs on the clock's own goroutine.
func (q *Queues) letOutSoon(runs []func()) {
	for _, run := range runs {
		q.after(0, run)
	}
}

// Unbind keeps t's place and lets it draw no turn, as a place Place gave does: the wake is taken off
// its machine for a decision that may give it back (supervise's dropWakeFor, reparkWake). Its turn,
// if the queues held one for it, goes to the next head — and back to t itself where the decision
// gives the wake back before that turn comes, which is why the turns the queues hold are left as
// they are rather than trimmed: a wake that waits must lose nothing, and a decision that launched
// nothing leaves its claim the launch it found waiting. A turn no wake waits for when it comes is
// spent on nothing, as a dropped wake's token always was (settle, turnCame).
func (t *Ticket) Unbind() {
	q := t.q
	q.mu.Lock()
	defer q.mu.Unlock()
	t.run = nil
}

// Stop takes t out of its queue and answers whether it was waiting there. The turn it would have
// had goes to the next head, and the latest turn the queues hold goes with its timer.
func (t *Ticket) Stop() bool {
	q := t.q
	q.mu.Lock()
	defer q.mu.Unlock()
	if t.state != queued {
		return false
	}
	q.waits[t.queue] = slices.DeleteFunc(q.waits[t.queue], func(w *Ticket) bool { return w == t })
	t.state, t.run = stopped, nil
	q.trim()
	return true
}

// Mark moves t to the message queue's tail if it waits in the other queue, its message marked at
// at; a wake already in the message queue keeps the earlier mark.
func (t *Ticket) Mark(at time.Time) {
	t.q.mu.Lock()
	defer t.q.mu.Unlock()
	t.q.mark(t, at)
}

func (q *Queues) mark(t *Ticket, at time.Time) {
	if t.state != queued {
		return
	}
	if t.queue == Message {
		if t.marked.IsZero() || at.Before(t.marked) {
			t.marked = at
		}
		return
	}
	q.waits[Other] = slices.DeleteFunc(q.waits[Other], func(w *Ticket) bool { return w == t })
	t.queue, t.marked = Message, at
	q.waits[Message] = append(q.waits[Message], t)
}

// Waiting is whether t still waits in its queue.
func (t *Ticket) Waiting() bool {
	t.q.mu.Lock()
	defer t.q.mu.Unlock()
	return t.state == queued
}

// Bound is whether a wake waits on t for its turn (Wait), rather than t being a place held ahead of
// its wake (Queues.Place).
func (t *Ticket) Bound() bool {
	t.q.mu.Lock()
	defer t.q.mu.Unlock()
	return t.state == queued && t.run != nil
}

// Queue is the queue t waits in, or came from.
func (t *Ticket) Queue() Queue {
	t.q.mu.Lock()
	defer t.q.mu.Unlock()
	return t.queue
}

// Out is what the queues said of t when they let it out, and whether they have.
func (t *Ticket) Out() (LetOut, bool) {
	t.q.mu.Lock()
	defer t.q.mu.Unlock()
	return t.out, t.state == letOut
}

// Estimate is when t is likely to be let out, read from its place: the turn the queues hold at its
// place in the order they let wakes out, or, past the turns drawn so far, one interval per place
// after the last. It is exact while nothing joins or leaves the queues ahead of it, and moves as
// they do; a wake has no moment of its own until it is let out. A wake let out answers its let-out,
// and one stopped the zero time.
func (t *Ticket) Estimate() time.Time {
	t.q.mu.Lock()
	defer t.q.mu.Unlock()
	return t.q.estimate(t)
}

func (q *Queues) estimate(t *Ticket) time.Time {
	switch t.state {
	case letOut:
		return t.out.At
	case stopped:
		return time.Time{}
	}
	i := slices.Index(q.order(t.run == nil), t)
	if i < len(q.slots) {
		return q.slots[i].at
	}
	if n := len(q.slots); n > 0 {
		return q.slots[n-1].at.Add(time.Duration(i-n+1) * q.interval)
	}
	return q.now().Add(time.Duration(i) * q.interval)
}

// Ahead is whether t is let out before other, both waiting in these queues, as the queues stand.
func (t *Ticket) Ahead(other *Ticket) bool {
	q := t.q
	if other == nil || other.q != q {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if t.state != queued || other.state != queued {
		return false
	}
	order := q.order(true)
	return slices.Index(order, t) < slices.Index(order, other)
}

// order is the waiting wakes in the order the queues will let them out as they stand, by the
// share: every place when places is true, and otherwise only the wakes that wait for a turn.
func (q *Queues) order(places bool) []*Ticket {
	waiting := func(queue Queue) []*Ticket {
		if places {
			return q.waits[queue]
		}
		return slices.DeleteFunc(slices.Clone(q.waits[queue]), func(t *Ticket) bool { return t.run == nil })
	}
	message, other := waiting(Message), waiting(Other)
	run := q.run
	order := make([]*Ticket, 0, len(message)+len(other))
	for len(message) > 0 || len(other) > 0 {
		if len(other) > 0 && (len(message) == 0 || run >= share) {
			order, other, run = append(order, other[0]), other[1:], 0
			continue
		}
		order, message, run = append(order, message[0]), message[1:], run+1
	}
	return order
}

// head is the first wake of queue that waits for a turn, nil for none.
func (q *Queues) head(queue Queue) *Ticket {
	for _, t := range q.waits[queue] {
		if t.run != nil {
			return t
		}
	}
	return nil
}

// pick is the wake a turn lets out, by the share, and counts it: the message queue's head, unless
// the other queue holds a wake and the message queue holds none or has had three turns in a row.
func (q *Queues) pick() *Ticket {
	message, other := q.head(Message), q.head(Other)
	if other != nil && (message == nil || q.run >= share) {
		q.run = 0
		return other
	}
	if message != nil {
		q.run++
	}
	return message
}

// bound is how many wakes wait for a turn.
func (q *Queues) bound() int {
	n := 0
	for _, waits := range q.waits {
		for _, t := range waits {
			if t.run != nil {
				n++
			}
		}
	}
	return n
}

// letOut takes t out of its queue at now.
func (q *Queues) letOut(t *Ticket, now time.Time) {
	q.waits[t.queue] = slices.DeleteFunc(q.waits[t.queue], func(w *Ticket) bool { return w == t })
	t.state = letOut
	t.out = LetOut{Queue: t.queue, Turns: q.turns - t.from, Placed: t.placed, Marked: t.marked, At: now}
	q.turns++
}

// settle draws a turn from the limiter for each wake that waits for one and holds none, lets out
// every turn that has come, and arms a timer for each turn still to come. It answers the wakes it
// let out, for its caller to run once the lock is let go.
func (q *Queues) settle(now time.Time) []*Ticket {
	var out []*Ticket
	for {
		for bound := q.bound(); len(q.slots) < bound; {
			q.slots = append(q.slots, &slot{at: q.reserve(now)})
		}
		if len(q.slots) == 0 || q.slots[0].at.After(now) {
			break
		}
		s := q.slots[0]
		q.slots = q.slots[1:]
		if s.stop != nil {
			s.stop()
		}
		if t := q.pick(); t != nil {
			q.letOut(t, now)
			out = append(out, t)
		}
	}
	for _, s := range q.slots {
		if s.stop == nil {
			s.stop = q.after(s.at.Sub(now), func() { q.turnCame(s) })
		}
	}
	return out
}

// trim gives back the latest turns the queues hold past one for each wake that waits for one, each
// timer stopped: the limiter charged them as it drew them, so the wakes drawn after keep the turns
// they hold.
func (q *Queues) trim() {
	for bound := q.bound(); len(q.slots) > bound; {
		last := q.slots[len(q.slots)-1]
		q.slots = q.slots[:len(q.slots)-1]
		if last.stop != nil {
			last.stop()
		}
	}
}

// turnCame is s's timer: the turn lets out the head the share picks, and the queues settle. The
// head's own work runs here, on the timer's goroutine, and any further wake the settle lets out
// through the clock (letOutSoon).
func (q *Queues) turnCame(s *slot) {
	q.mu.Lock()
	i := slices.Index(q.slots, s)
	if i < 0 {
		q.mu.Unlock()
		return
	}
	q.slots = slices.Delete(q.slots, i, i+1)
	now := q.now()
	var out []func()
	if t := q.pick(); t != nil {
		q.letOut(t, now)
		out = append(out, t.run)
	}
	for _, t := range q.settle(now) {
		out = append(out, t.run)
	}
	q.mu.Unlock()
	if len(out) == 0 {
		return
	}
	q.letOutSoon(out[1:])
	out[0]()
}
