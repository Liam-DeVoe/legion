//go:build !race

// The guard is static, as lock_level_test.go's is and for the reason given there: it reads the
// module's source, so the race detector adds nothing to it.

package api

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// brokerEventInsert is the one source file allowed to insert an event.
const brokerEventInsert = "internal/dispatch/events/broker.go"

var eventInsertStatement = regexp.MustCompile(`\binsert into events\b`)

// TestOnlyTheBrokerInsertsEvents enforces what every reader of the event log by id rests on: every
// row of it is written by events.Broker.Append, which keeps two invariants no other insert would.
//
// The writer invariant: Append's transaction holds its transaction id before the insert draws the
// row's id from events_id_seq (lockEventOwner takes it with pg_current_xact_id). The safe horizon's
// proof needs it (events/horizon.go): a transaction that drew an id at or below one an observer saw
// committed holds a transaction id below the observer's own, so once no event writer of this
// database holding a transaction id below the observer's is running, that id is final. A bare
// insert, in a transaction that has written nothing before it, takes its transaction id only as it
// writes the row, after its default drew the id, so an observation taken between the two could
// prove a horizon above an id still open. The
// relay (event_stream_relay.go), the delivery hub and `GET /references?since=` would then read past
// that id, and its row, committed later, would reach no open subscription until the page subscribed
// again -- a missing comment or ask on an open page, with nothing to show for it in any log.
//
// Owner sequence allocation: Append locks the event's owner row and takes the owner's next seq under
// that lock, so one owner's events commit in id order and their seqs run without a gap or a repeat.
// An insert elsewhere could write a seq out of that order, and the Legion daemon, which fences an
// issue's facts on their seq, would drop as stale an event whose seq it had already passed.
//
// Its reach is the statement as the module scan can assemble it (TestNoForUpdateOnOwnerTables):
// string literals and chains of literals and package-level constants, in every non-test Go file
// of the module. A test inserts events directly on purpose -- a backlog to page through, a row
// shape to serve -- so test sources are outside it, as they are outside that scan.
func TestOnlyTheBrokerInsertsEvents(t *testing.T) {
	sources := moduleSources(t)
	constants, err := stringConstants(sources)
	if err != nil {
		t.Fatal(err)
	}
	var offences []string
	broker := 0
	for _, source := range sources {
		path := filepath.ToSlash(source.path)
		for _, statement := range sqlStatements(source.file, constants) {
			if !eventInsertStatement.MatchString(statement) {
				continue
			}
			if strings.HasSuffix(path, brokerEventInsert) {
				broker++
				continue
			}
			offences = append(offences, path+": "+statement)
		}
	}
	if len(offences) > 0 {
		t.Fatalf("only %s may insert into events, and these statements also do:\n\t%s\n"+
			"Append the event through events.Broker.Append instead. It takes its transaction id "+
			"before the insert draws the event's id, which the safe horizon every reader of the log "+
			"by id reads through needs (events/horizon.go): a row inserted without it can commit "+
			"behind a horizon that passed its id and reach no open subscription. It also allocates the "+
			"owner's seq under the owner's row lock.", brokerEventInsert, strings.Join(offences, "\n\t"))
	}
	if broker != 1 {
		t.Fatalf("%s holds %d event inserts the scan can see, want 1: if the insert moved or was "+
			"written around a value this scan cannot resolve, this guard now passes by seeing "+
			"nothing", brokerEventInsert, broker)
	}
}

// eventInsertColumns is an `insert into events` and its column list, when it has one: the
// statements the scan reads are lowercased with their whitespace collapsed (sqlStatements).
var eventInsertColumns = regexp.MustCompile(`\binsert into events\b ?(\(([^)]*)\))?`)

// eventIDSequence names the sequence the events id default draws from, by its name or through
// pg_get_serial_sequence.
var eventIDSequence = regexp.MustCompile(`\bevents_id_seq\b|pg_get_serial_sequence\( ?'(public\.)?events'`)

// seedSequenceReset is the one source file allowed to name events_id_seq: dispatch-seed's setval
// after it fills an empty database offline, with no Dispatch serving it (cmd/dispatch-seed).
const seedSequenceReset = "cmd/dispatch-seed/write_events.go"

// TestOnlyAnEventInsertsDefaultDrawsAnEventID enforces the other half of what the safe horizon's
// proof rests on (events/horizon.go, "Safe horizon"): an event id is drawn only by the column
// default of the insert that writes its row, so the writer holds RowExclusiveLock on events
// before it holds the id. Postgres takes that lock when the insert is parsed or its cached plan
// locked, before the executor calls nextval, and the horizon dates a writer by the transaction
// holding that lock. A writer that drew its id first (a select of nextval('events_id_seq'), then
// an insert naming the id) would hold an id the lock table cannot yet show, and the horizon would
// pass it while it is open. So no statement may name events_id_seq but dispatch-seed's offline
// setval, and every insert into events lists its columns, without id: a list that leaves id out
// leaves it to the default, and an insert with no list fills id by position.
//
// Its reach is TestOnlyTheBrokerInsertsEvents's: the module's non-test Go sources as the scan
// assembles their statements. SQL in other files is outside it: the migrations, none of which
// inserts an event, and loadtest/stack.sh's extend_events, which lets the default draw each id and
// runs only on a database no Dispatch serves (the seed template, or after the seed stopped
// Dispatch).
func TestOnlyAnEventInsertsDefaultDrawsAnEventID(t *testing.T) {
	sources := moduleSources(t)
	constants, err := stringConstants(sources)
	if err != nil {
		t.Fatal(err)
	}
	var offences []string
	seen := map[string]bool{}
	offend := func(offence string) {
		// A declared constant reaches the scan twice, by its name and by its value.
		if !seen[offence] {
			seen[offence] = true
			offences = append(offences, offence)
		}
	}
	lists := 0
	for _, source := range sources {
		path := filepath.ToSlash(source.path)
		for _, statement := range sqlStatements(source.file, constants) {
			if eventIDSequence.MatchString(statement) && !(strings.HasSuffix(path, seedSequenceReset) && strings.Contains(statement, "setval(")) {
				offend(path + ": names the events id sequence: " + statement)
			}
			for _, insert := range eventInsertColumns.FindAllStringSubmatch(statement, -1) {
				if insert[1] == "" {
					offend(path + ": inserts into events with no column list, which fills id by position: " + statement)
					continue
				}
				lists++
				for _, column := range strings.Split(insert[2], ",") {
					if strings.Trim(strings.TrimSpace(column), `"`) == "id" {
						offend(path + ": inserts an explicit event id: " + statement)
					}
				}
			}
		}
	}
	if len(offences) > 0 {
		t.Fatalf("an event id is drawn outside the column default of the insert that writes its row:\n\t%s\n"+
			"Let the insert's default draw the id (events.Broker.Append): the insert holds RowExclusiveLock "+
			"on events before its default draws, and the safe horizon dates a writer by that lock "+
			"(events/horizon.go). An id drawn before the insert is held where the lock table cannot show it, "+
			"and the horizon would pass it while it is open.", strings.Join(offences, "\n\t"))
	}
	if lists == 0 {
		t.Fatal("the scan saw no column list of an insert into events: if the broker's insert moved or " +
			"was written around a value this scan cannot resolve, this guard now passes by seeing nothing")
	}
}
