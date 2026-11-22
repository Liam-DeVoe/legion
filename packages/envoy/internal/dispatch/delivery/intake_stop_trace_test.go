package delivery

import (
	"bytes"
	"cmp"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/testnats"
)

// stopTrace is the trace that a stop row prints when it fails. It gives what the row's stop did,
// in time order, so one failing run names its cause with no second capture. It reads the row's
// capture of the log (captureLogs), DEBUG records included, and keeps the records of this package
// and the records about an intake's hold (traced). These are the trace lines of each hand-back,
// seal, settle read, hold release, finalizer, client close and take-over bind, and the hold,
// take-over and drain lines that the package logs at other levels. Next to them it puts each chunk
// that the row's link relayed to the server with a -NAK or a +ACK in it (watchLink), the durable's
// events of a release row (watchDurable), and the states that it reads at the failure (dump). It
// prints each pointer that the row named (name) as its name.
type stopTrace struct {
	logs func() []map[string]any

	mu sync.Mutex
	// names is the name that the row gave each pointer, by the pointer as tracePointer prints it.
	names map[string]tracedName
	// relayed is each chunk with a -NAK or a +ACK in it that a link relayed to the server.
	relayed []relayedChunk
	// states are what the trace reads at the failure.
	states []traceState
	// durable is the record of the durable's events of a release row, and stopFrom is how many
	// events it held when the stop began.
	durable  *durableEvents
	stopFrom int
	// printed is set when the trace is printed, so that the trace is printed one time only.
	printed bool
}

// tracedName is the name that a row gave a pointer, and the pointer's type.
type tracedName struct {
	name string
	kind string
}

// relayedChunk is a chunk of a client's stream that a link relayed to the server: the time, the
// link, and the lines of the chunk that answer a message (answerLines).
type relayedChunk struct {
	at      time.Time
	link    string
	answers []string
}

// traceState is a state that the trace reads at the failure, with its label.
type traceState struct {
	label string
	read  func() string
}

// traceEntry is one line of the trace, with its time.
type traceEntry struct {
	at   time.Time
	line string
}

// traceTime is the layout of the trace's times: RFC 3339 with nanoseconds. It prints every digit,
// so the times line up.
const traceTime = "2006-01-02T15:04:05.000000000Z07:00"

// traceKeys are the attributes that a line of the trace prints first, in this order. It prints the
// other attributes after them, in the order of their keys.
var traceKeys = []string{
	"name", "delivery", "client", "conn", "conn_closed", "subject", "reply", "delay", "taken", "sealed",
	"ended", "confirmed", "published_on", "published_reconnects", "published_on_many",
	"lapse", "settled", "in_doubt", "outcome", "reason", "answered", "connected", "stopping",
	"hold_covers", "call", "waited", "error",
}

// tracePointerKeys are the attributes whose value is a pointer that the row can name.
var tracePointerKeys = map[string]bool{"delivery": true, "client": true, "conn": true, "published_on": true}

// traceStops starts the trace of t's stop row. It captures t's log (captureLogs) and lowers the
// capture's level to DEBUG, so the code under test makes its trace lines, and when t has failed by
// the time its cleanups run, it prints the trace. A row calls it before it starts the processes or
// the subscription that it stops, so that the capture has their records, and before any read of a
// DEBUG record. The row then calls printAtFailure after it registers its own cleanups.
func traceStops(t *testing.T) *stopTrace {
	t.Helper()
	capture := captureOf(t)
	capture.level.Set(slog.LevelDebug)
	trace := &stopTrace{logs: capture.logs, names: map[string]tracedName{}}
	t.Cleanup(func() { trace.print(t) })
	return trace
}

// printAtFailure prints the trace when t has failed, before the cleanups registered until now run.
// So the stops and the closes of those cleanups do not change what the trace reads at the failure.
// For a failure before this call, the cleanup of traceStops prints the trace after those cleanups.
func (s *stopTrace) printAtFailure(t *testing.T) {
	t.Cleanup(func() { s.print(t) })
}

// name gives pointer, which is a client, a connection or a delivery of the row, the name name in
// the trace. A nil pointer gets no name.
func (s *stopTrace) name(pointer any, name string) {
	key := tracePointer(pointer)
	if pointer == nil || key == "0x0" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.names[key] = tracedName{name: name, kind: fmt.Sprintf("%T", pointer)}
}

// watchLink keeps each chunk of a client's stream with a -NAK or a +ACK in it that hold relays to
// the server, with the time of the relay's write (testnats.HandshakeHold.Observe). link is the
// name of the client whose link it is.
func (s *stopTrace) watchLink(hold *testnats.HandshakeHold, link string) {
	hold.Observe(func(at time.Time, chunk []byte) {
		if !bytes.Contains(chunk, []byte("-NAK")) && !bytes.Contains(chunk, []byte("+ACK")) {
			return
		}
		answers := answerLines(chunk)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.relayed = append(s.relayed, relayedChunk{at: at.Round(0), link: link, answers: answers})
	})
}

// answerLines are the lines of chunk, a part of a client's NATS protocol stream, whose payload is a
// -NAK or a +ACK. Each comes after the line before it, when chunk holds that line: the PUB that
// carries the answer, with the reply subject that the answer goes to.
func answerLines(chunk []byte) []string {
	lines := strings.Split(string(chunk), "\r\n")
	var answers []string
	for i, line := range lines {
		if !strings.HasPrefix(line, "-NAK") && !strings.HasPrefix(line, "+ACK") {
			continue
		}
		if i > 0 {
			line = lines[i-1] + " " + line
		}
		answers = append(answers, line)
	}
	return answers
}

// watchDurable puts the events of events, the record of a release row, in the trace, each with its
// time. A -NAK event names the client whose hand-back published it.
func (s *stopTrace) watchDurable(events *durableEvents) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.durable = events
}

// stopBegins records from, the count of the durable's events when the stop began. A -NAK event
// after the first creation of the durable from there on reached the standby's consumer, and the
// trace says so.
func (s *stopTrace) stopBegins(from int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopFrom = from
}

// dump has the trace read state at the failure, with the label label.
func (s *stopTrace) dump(label string, state func() string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states = append(s.states, traceState{label: label, read: state})
}

// print prints the trace one time, when t has failed, with the states that it reads at that
// moment.
func (s *stopTrace) print(t *testing.T) {
	if !t.Failed() {
		return
	}
	s.mu.Lock()
	if s.printed {
		s.mu.Unlock()
		return
	}
	s.printed = true
	states := slices.Clone(s.states)
	s.mu.Unlock()
	failedAt := time.Now().Round(0)
	read := make([]string, len(states))
	for i, state := range states {
		read[i] = state.label + ": " + state.read()
	}
	t.Log(s.render(failedAt, read))
}

// render is the trace: the traced records, the relayed chunks, the durable's events and the states
// read at failedAt, one on each line, in time order. A header line gives each name and its pointer.
func (s *stopTrace) render(failedAt time.Time, states []string) string {
	s.mu.Lock()
	names := maps.Clone(s.names)
	relayed := slices.Clone(s.relayed)
	durable, from := s.durable, s.stopFrom
	s.mu.Unlock()
	named := func(pointer string) string {
		if name, ok := names[pointer]; ok {
			return name.name
		}
		return pointer
	}

	var entries []traceEntry
	// publishedBy is the name of the client whose hand-back's publish began, by the reply subject
	// that the hand-back answers.
	publishedBy := map[string]string{}
	for _, record := range s.logs() {
		if !traced(record) {
			continue
		}
		stamp, _ := record["time"].(string)
		at, _ := time.Parse(time.RFC3339Nano, stamp)
		entries = append(entries, traceEntry{at: at, line: recordLine(record, named)})
		if record["msg"] == traceHandBackPublishing {
			reply, _ := record["reply"].(string)
			client, _ := record["client"].(string)
			publishedBy[reply] = named(client)
		}
	}
	for _, chunk := range relayed {
		line := "RELAY the " + chunk.link + " link sends the server"
		if len(chunk.answers) == 0 {
			line += " a chunk whose -NAK or +ACK is not on a line of its own"
		} else {
			line += ": " + strings.Join(chunk.answers, " | ")
		}
		entries = append(entries, traceEntry{at: chunk.at, line: line})
	}
	if durable != nil {
		created := false
		for i, event := range durable.timed() {
			line := "EVENT " + event.event
			if event.event == durableCreated {
				created = created || i >= from
			} else {
				line += " on " + event.subject
			}
			if strings.HasPrefix(event.event, "-NAK") {
				by, ok := publishedBy[event.subject]
				if !ok {
					by = "no traced hand-back"
				}
				line += ", published by " + by
				if created {
					line += ", after the durable's first creation since the stop began"
				}
			}
			entries = append(entries, traceEntry{at: event.at.Round(0), line: line})
		}
	}
	for _, state := range states {
		entries = append(entries, traceEntry{at: failedAt, line: "FAILED " + state})
	}
	slices.SortStableFunc(entries, func(a, b traceEntry) int { return a.at.Compare(b.at) })

	var trace strings.Builder
	trace.WriteString("the stop's trace, in time order (UTC, RFC 3339, nanoseconds); names:")
	for _, pointer := range slices.Sorted(maps.Keys(names)) {
		fmt.Fprintf(&trace, " %s %s=%s", names[pointer].name, names[pointer].kind, pointer)
	}
	for _, entry := range entries {
		trace.WriteString("\n" + entry.at.UTC().Format(traceTime) + " " + entry.line)
	}
	return trace.String()
}

// traced reports whether record goes in the trace: a line of this package, or a line about an
// intake's hold (duty.Hold logs the failure of a renewal and the loss of the hold with the hold's
// name).
func traced(record map[string]any) bool {
	message, _ := record["msg"].(string)
	hold, _ := record["hold"].(string)
	return strings.HasPrefix(message, "dispatch delivery: ") || strings.HasPrefix(hold, intakeHoldName(""))
}

// recordLine is record as one line of the trace, after its time: its level, its message and its
// attributes. The keys in traceKeys come first, in that order, and the other keys after them, in
// order. It prints a delivery, a client or a connection as the name that the row gave it (named).
func recordLine(record map[string]any, named func(string) string) string {
	level, _ := record["level"].(string)
	message, _ := record["msg"].(string)
	var line strings.Builder
	line.WriteString(level + " " + message)
	rank := func(key string) int {
		if at := slices.Index(traceKeys, key); at >= 0 {
			return at
		}
		return len(traceKeys)
	}
	keys := slices.Sorted(maps.Keys(record))
	slices.SortStableFunc(keys, func(a, b string) int { return cmp.Compare(rank(a), rank(b)) })
	for _, key := range keys {
		switch key {
		case "time", "level", "msg":
			continue
		}
		value := record[key]
		if pointer, ok := value.(string); ok && tracePointerKeys[key] {
			value = named(pointer)
		}
		line.WriteString(" " + key + "=" + traceValue(value))
	}
	return line.String()
}

// traceValue is value, an attribute's value as the JSON handler wrote it, as the trace prints it:
// a string with a space, a quote or an equals sign in it is quoted, and a number is printed in
// full.
func traceValue(value any) string {
	switch value := value.(type) {
	case nil:
		return "nil"
	case string:
		if value == "" || strings.ContainsAny(value, " \t\"=") {
			return strconv.Quote(value)
		}
		return value
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

// namedDeliveryState names delivery name in trace and is its state (deliveryState). A process can
// subscribe again after the row named its first delivery, so the state names the delivery that it
// reads.
func namedDeliveryState(trace *stopTrace, delivery *intakeDelivery, name string) string {
	trace.name(delivery, name)
	return deliveryState(delivery)
}

// deliveryState is delivery's state, read under its lock and its hand-back record's lock: whether
// its stop has begun (stopping), whether that stop has handed back the messages it held
// (released), whether the stop lets hand-backs out (handBackAllowed), how many hand-backs are
// taken and not settled (taken), whether it is sealed, how many hand-back publishes have ended and
// how many of those a flush confirmed (ended, confirmed), how many messages it holds for the stop
// to hand back (held), and whether its hold covers it (holdCovers).
func deliveryState(delivery *intakeDelivery) string {
	if delivery == nil {
		return "no delivery"
	}
	covers := delivery.holdCovers()
	delivery.mu.Lock()
	defer delivery.mu.Unlock()
	delivery.handBacks.mu.Lock()
	defer delivery.handBacks.mu.Unlock()
	return fmt.Sprintf("stopping=%t released=%t handBackAllowed=%t taken=%d sealed=%t ended=%d confirmed=%d held=%d holdCovers=%t",
		delivery.stopping.Load(), delivery.released, delivery.handBackAllowed, delivery.handBacks.handingBack,
		delivery.handBacks.sealed, delivery.handBacks.publishes.ended, delivery.handBacks.publishes.confirmed, len(delivery.held), covers)
}
