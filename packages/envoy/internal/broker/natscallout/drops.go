package natscallout

import (
	"log/slog"
	"sync/atomic"
	"time"
)

// The reasons a request is dropped unanswered, the reason label of
// broker_nats_callout_dropped_total. The hub refuses the client of each at its own authorization
// timeout.
const (
	// dropPastBounds: the request reached a replica already holding maxWaiting requests waiting
	// for a decision slot and maxRefusing being refused (deliver).
	dropPastBounds = iota
	// dropHubGaveUp: the hub had already given up on the request when the replica read it, by the
	// hub's own clock (hubClock).
	dropHubGaveUp
	// dropUnopened: the request did not open with the service's curve key.
	dropUnopened
	// dropUnreadable: the opened request is no authorization request JWT.
	dropUnreadable
	dropReasons
)

// dropLabels are the reason labels, by reason.
var dropLabels = [dropReasons]string{"past_bounds", "hub_gave_up", "unopened", "not_a_request"}

// pastBoundsDetail is the first-drop detail of a drop past both bounds.
const pastBoundsDetail = "past both bounds of this replica"

// drops counts the requests the service dropped unanswered. Each drop is an atomic add, and the
// first detail since the last log line a compare-and-swap, so deliver can record one without
// blocking (deliver); the reporter (reportDropped) moves the counts into the metric and writes the
// log line, so neither a counter's lock nor a write is taken where a request is dropped.
type drops struct {
	// pending counts each reason's drops since the last report.
	pending [dropReasons]atomic.Int64
	// first is the detail of the first drop since the last log line.
	first atomic.Pointer[string]
	// unlogged counts each reason's drops since the last log line, and logged is when that line was
	// written; only reportDropped reads or writes them, from the reporter alone.
	unlogged [dropReasons]int64
	logged   time.Time
}

// add counts one drop for reason, and reports whether the drop's detail is wanted: whether none
// waits for the next log line yet. A caller builds the detail, and gives it to note, only then, so
// a flood of drops between two log lines builds one string rather than one a drop; two drops at
// once can both be told yes, and the second's note then keeps nothing.
func (d *drops) add(reason int) bool {
	d.pending[reason].Add(1)
	return d.first.Load() == nil
}

// note keeps detail as the first drop's since the last log line, unless one is kept already.
func (d *drops) note(detail string) {
	d.first.CompareAndSwap(nil, &detail)
}

// reportDropped moves the drops counted since the last report into broker_nats_callout_dropped_total
// and, when any wait to be logged and logInterval has passed since the last line, logs them by
// reason with the first one's detail. The reporter runs it every tick and once more at the end
// (reportWhileRunning).
func (s *Service) reportDropped() {
	d := s.answer.drops
	total := int64(0)
	for reason := range dropReasons {
		n := d.pending[reason].Swap(0)
		s.answer.deps.Metrics.dropped(dropLabels[reason], n)
		d.unlogged[reason] += n
		total += d.unlogged[reason]
	}
	if total == 0 || time.Since(d.logged) < logInterval {
		return
	}
	fields := []any{"dropped", total}
	for reason := range dropReasons {
		fields = append(fields, dropLabels[reason], d.unlogged[reason])
		d.unlogged[reason] = 0
	}
	if first := d.first.Swap(nil); first != nil {
		fields = append(fields, "first", *first)
	}
	fields = append(fields, "waiting_bound", s.settings.maxWaiting, "refusing_bound", s.settings.maxRefusing)
	d.logged = time.Now()
	s.answer.logAt(slog.LevelWarn, "natscallout: dropped authorization requests unanswered; the hub refuses each client at its authorization timeout", fields...)
}
