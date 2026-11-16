package natscallout

import (
	"log/slog"
	"sync"
	"time"
)

// logInterval is the least time between two log lines of one kind: the first event after a quiet
// interval is logged at the reporter's next tick, and those that follow it inside the interval are
// counted and wait for the next line.
const logInterval = time.Minute

// tally counts events by key between two log lines, and keeps the detail of the first. add holds
// its lock only to count, never across a write, so it never waits on the log, and what it keeps
// between two lines is a count per key and one detail, whatever the log does meanwhile.
type tally struct {
	mu     sync.Mutex
	counts map[string]int64
	first  string
	logged time.Time
}

// add counts one event under key, and keeps detail's string when it is the first since the last
// line; detail is called only then.
func (t *tally) add(key string, detail func() string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.counts) == 0 {
		if t.counts == nil {
			t.counts = make(map[string]int64)
		}
		t.first = detail()
	}
	t.counts[key]++
}

// take returns the counts since the last line and the first one's detail, and starts the next,
// when any are counted and logInterval has passed since the last line; otherwise nothing.
func (t *tally) take() (map[string]int64, string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.counts) == 0 || time.Since(t.logged) < logInterval {
		return nil, "", false
	}
	counts, first := t.counts, t.first
	t.counts, t.first, t.logged = make(map[string]int64), "", time.Now()
	return counts, first, true
}

// reportWhileRunning is the service's one log writer. No request's goroutine, the delivery
// goroutine and the heartbeat among them, writes to the log: a log line is a write, and a write
// can block, as the broker's stdout does when the reader of the pod's log falls behind. Written
// where it is made, a refusal's line held the refusal's answer, its place in the refusing bound
// and, for a refusal made in a decision slot, that slot, so a stalled log stopped the replica
// answering; written on the heartbeat, a line would stop the beat, and a stalled log would read
// as a stalled replica. Each of them counts what it would log (drops, refusals, failures), and
// every livenessTick this goroutine writes, at most once a logInterval for each kind, the counts
// and the first one's detail. A log that stalls holds this goroutine alone, and what waits for it
// is counts. Close stops it once every request has been answered, and waits for its last report
// for drainBound at most.
func (s *Service) reportWhileRunning() {
	defer close(s.reported)
	ticker := time.NewTicker(s.settings.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.report()
		case <-s.stopReport:
			s.report()
			return
		}
	}
}

// report writes what each tally holds, when its interval allows.
func (s *Service) report() {
	s.reportDropped()
	if counts, first, ok := s.answer.refusals.take(); ok {
		level := slog.LevelWarn
		message := "natscallout: refused NATS connections"
		if counts[labelUnsealed] > 0 {
			// A request in the clear means the hub's callout lost its curve key and every token
			// and proof is crossing the wire unencrypted: said loudly.
			level = slog.LevelError
			message = "natscallout: refused NATS connections, among them requests that arrived in the clear: the hub's auth_callout names no xkey, so every token and proof crosses the wire unencrypted"
		}
		s.answer.logAt(level, message, countFields(counts, first)...)
	}
	if counts, first, ok := s.answer.failures.take(); ok {
		s.answer.logAt(slog.LevelWarn, "natscallout: answers that could not be sent", countFields(counts, first)...)
	}
}

// countFields is a tally's counts as log fields, with their total and the first one's detail.
func countFields(counts map[string]int64, first string) []any {
	total := int64(0)
	fields := []any{}
	for key, n := range counts {
		total += n
		fields = append(fields, key, n)
	}
	return append([]any{"count", total}, append(fields, "first", first)...)
}
