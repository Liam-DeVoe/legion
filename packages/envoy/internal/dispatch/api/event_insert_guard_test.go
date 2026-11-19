//go:build !race

// The guard is static, as lock_level_test.go's is and for the reason given there: it reads the
// module's source, so the race detector adds nothing to it.

package api

import (
	"go/ast"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/eventidtest"
)

// brokerEventInsert is the one source file allowed to insert an event.
const brokerEventInsert = "internal/dispatch/events/broker.go"

// brokerEventInserts is how many event inserts that file holds: the one a write phase queues
// (events.Broker.QueueAppendWithLogins). It keeps the invariants below, and the count is here so
// that an insert moving out of this scan's reach reds rather than passing unseen.
const brokerEventInserts = 1

// eventsTable names events in each spelling Postgres resolves to it: bare, schema-qualified, and
// either part quoted. The statements the scan reads are lowercased with their whitespace collapsed
// (sqlStatements), so one optional space stands for any.
const eventsTable = `(?:"?public"? ?\. ?)?"?events\b`

// eventInsertStatement is a statement that writes rows into events: an insert, a merge, or a copy
// from a client or a file, with the table in each of its spellings (eventsTable). A copy to a
// client reads, and is not one.
var eventInsertStatement = regexp.MustCompile(`\b(?:insert into|merge into) ` + eventsTable + `|\bcopy ` + eventsTable + `"?(?: ?\([^)]*\))? from\b`)

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
	t.Run("the pattern reads every statement that writes rows into events", func(t *testing.T) {
		for _, row := range []struct {
			statement string
			writes    bool
		}{
			{"insert into events (seq, type) values (0, 't')", true},
			{`insert into "public"."events" (seq) values (0)`, true},
			{"merge into events e using (select $1::bigint as id) s on false when not matched then insert (id, seq) values (s.id, 0)", true},
			{"merge into public.events e using staged s on e.id = s.id when not matched then insert (seq) values (0)", true},
			{"copy events (id, seq, type) from stdin", true},
			{`copy "events" from '/tmp/events.csv'`, true},
			{"copy events to stdout", false},
			{"copy (select id from events) to stdout", false},
			{"insert into events_archive (id) values (1)", false},
			{"select id from events where id > $1", false},
		} {
			if got := eventInsertStatement.MatchString(row.statement); got != row.writes {
				t.Errorf("%s: read as a write into events: %t, want %t", row.statement, got, row.writes)
			}
		}
	})

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
	if broker != brokerEventInserts {
		t.Fatalf("%s holds %d event inserts the scan can see, want %d: if an insert moved or was "+
			"written around a value this scan cannot resolve, this guard now passes by seeing "+
			"nothing", brokerEventInsert, broker, brokerEventInserts)
	}
}

// eventInsertColumns is an insert into events, in each of its spellings (eventsTable), and its
// column list when it has one.
var eventInsertColumns = regexp.MustCompile(`\binsert into ` + eventsTable + `"? ?(\(([^)]*)\))?`)

// eventIDSequence names the sequence the events id default draws from, in each spelling
// eventidtest.SequencePattern reads. The migrations' guard compiles the same pattern.
var eventIDSequence = regexp.MustCompile(eventidtest.SequencePattern)

// eventIDCurrval is pg_catalog.currval of the events id sequence, the one read of it that draws no
// id (eventidtest.CurrvalPattern says which spellings are that read and why no other is):
// eventIDOffences reads a statement with each such read taken out. The migrations' guard compiles
// the same pattern.
var eventIDCurrval = regexp.MustCompile(eventidtest.CurrvalPattern)

// eventIDPatterns is the two patterns' own text as the scan reads a string: lowercased, with its
// whitespace collapsed. eventidtest declares them in a file that is no test, so the scan reads each
// declaration as a statement, and CurrvalPattern spells the sequence's name. A statement that is
// one of these two strings, and nothing more, is a pattern rather than SQL Postgres can run, so
// eventIDOffences finds no offence in it.
var eventIDPatterns = map[string]bool{
	sqlWhitespace.ReplaceAllString(strings.ToLower(eventidtest.SequencePattern), " "): true,
	sqlWhitespace.ReplaceAllString(strings.ToLower(eventidtest.CurrvalPattern), " "):  true,
}

// eventDerivedTable is a table made from events: one that inherits from it (inherits (...), or
// alter table ... inherit events) or is a partition of it (partition of events, or alter table
// events attach partition ...), whose rows are rows of events written under a lock on that table
// and not on events, or one made like events, alone or after other columns, which can take
// events' id default. An attach partition names its parent, so another table's attach is no
// offence. The migrations' schema is held to the same by
// store.TestOnlyTheEventsIDDefaultDrawsFromItsSequence, which reads the catalog.
var eventDerivedTable = regexp.MustCompile(`\binherits ?\([^)]*\bevents\b|\binherit ` + eventsTable + `|\bpartition of ` + eventsTable +
	`|\balter table (?:if exists )?` + eventsTable + `"? attach partition\b|[(,] ?like ` + eventsTable)

// seedSequenceReset is the one source file allowed to name events_id_seq: dispatch-seed's setval
// after it fills an empty database offline, with no Dispatch serving it (cmd/dispatch-seed).
const seedSequenceReset = "cmd/dispatch-seed/write_events.go"

// seedTool is dispatch-seed's directory, the one place allowed to copy rows into events: it fills
// an empty database offline, copying each event with its id, and then sets the sequence past them.
const seedTool = "cmd/dispatch-seed/"

// eventIDOffences is what a statement of the file at path does that draws or writes an event id
// outside the column default of the insert that writes its row, and how many column lists of an
// insert into events it holds.
func eventIDOffences(path, statement string) (offences []string, lists int) {
	if eventIDPatterns[statement] {
		return nil, 0
	}
	if eventIDSequence.MatchString(eventIDCurrval.ReplaceAllStringFunc(statement, eventidtest.CurrvalReadKept)) && !(strings.HasSuffix(path, seedSequenceReset) && strings.Contains(statement, "setval(")) {
		offences = append(offences, path+": names the events id sequence: "+statement)
	}
	if eventDerivedTable.MatchString(statement) {
		offences = append(offences, path+": makes a table from events, which takes its id default or holds its rows: "+statement)
	}
	for _, insert := range eventInsertColumns.FindAllStringSubmatch(statement, -1) {
		if insert[1] == "" {
			// An alias between the table and its list (insert into events as e (...)) reads as
			// no list too, and is refused with it.
			offences = append(offences, path+": inserts into events with no column list, which fills id by position: "+statement)
			continue
		}
		lists++
		for _, column := range strings.Split(insert[2], ",") {
			if strings.Trim(strings.TrimSpace(column), `"`) == "id" {
				offences = append(offences, path+": inserts an explicit event id: "+statement)
			}
		}
	}
	return offences, lists
}

// eventCopies counts the pgx.Identifier literals in file whose table is events, as a CopyFrom into
// events names it: a copy writes rows through no insert, with their ids when its columns name id,
// so neither guard's reading of the insert can hold it.
func eventCopies(file *ast.File, constants map[string]string) int {
	copies := 0
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok || len(literal.Elts) == 0 {
			return true
		}
		selector, ok := literal.Type.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Identifier" {
			return true
		}
		if qualifier, ok := selector.X.(*ast.Ident); !ok || qualifier.Name != "pgx" {
			return true
		}
		table, ok := literalText(literal.Elts[len(literal.Elts)-1], file.Name.Name, constants)
		if ok && strings.ToLower(table) == "events" {
			copies++
		}
		return true
	})
	return copies
}

// TestOnlyAnEventInsertsDefaultDrawsAnEventID enforces the other half of what the safe horizon's
// proof rests on (events/horizon.go, "Safe horizon"): an event id is drawn only by the column
// default of the insert that writes its row, so the writer holds RowExclusiveLock on events
// before it holds the id. Postgres takes that lock when the insert is parsed or its cached plan
// locked, before the executor calls nextval, and the horizon dates a writer by the transaction
// holding that lock. A writer that drew its id first (a select of nextval('events_id_seq'), then
// an insert naming the id) would hold an id the lock table cannot yet show, and the horizon would
// pass it while it is open. So no statement may name events_id_seq but dispatch-seed's offline
// setval and a currval of the id the statement's own insert drew (eventIDCurrval), none may use
// nextval or setval but in a call of one string literal of a sequence's name (one of digits alone
// is an OID), or their C code, which can draw from events_id_seq without spelling it, and every
// insert into events lists its columns, without id: a list that leaves id out leaves it to the
// default, and an insert with no list fills id by position. No source but dispatch-seed's copies
// rows into events, which writes them through no insert at all. And no statement makes a table
// from events (inherits, partition of, like): a child's or a partition's rows are rows of events
// written under a lock on another table, and a table made like events can take its id default.
//
// Its reach is TestOnlyTheBrokerInsertsEvents's: the module's non-test Go sources as the scan
// assembles their statements. The migrations' SQL, which the server also runs, is
// store.TestNoMigrationDrawsAnEventIDOutsideTheInsertDefault's, and the schema they build is
// store.TestOnlyTheEventsIDDefaultDrawsFromItsSequence's. Other SQL is outside all three:
// loadtest/stack.sh's extend_events lists its columns without id and runs only on a database no
// Dispatch serves (the seed template, or after the seed stopped Dispatch).
func TestOnlyAnEventInsertsDefaultDrawsAnEventID(t *testing.T) {
	t.Run("the patterns read every spelling of the table and of its sequence", func(t *testing.T) {
		// The two patterns as the scan reads eventidtest's declarations of them.
		sequencePattern := sqlWhitespace.ReplaceAllString(strings.ToLower(eventidtest.SequencePattern), " ")
		currvalPattern := sqlWhitespace.ReplaceAllString(strings.ToLower(eventidtest.CurrvalPattern), " ")
		for _, row := range []struct {
			path, statement string
			refused         []string
		}{
			{"a.go", "select nextval('events_id_seq')", []string{"names the events id sequence"}},
			{"a.go", `select nextval('"public"."events_id_seq"'::regclass)`, []string{"names the events id sequence"}},
			{"a.go", "select nextval(pg_get_serial_sequence('events', 'id'))", []string{"names the events id sequence"}},
			{"a.go", `select nextval(pg_get_serial_sequence('"events"', 'id'))`, []string{"names the events id sequence"}},
			{"a.go", `select nextval(pg_get_serial_sequence( ' "public" . "events" ' , 'id'))`, []string{"names the events id sequence"}},
			{"a.go", "select setval('events_id_seq', 1)", []string{"names the events id sequence"}},
			{"a.go", "insert into events (id, seq, type) values ($1, 0, 't')", []string{"inserts an explicit event id"}},
			{"a.go", "insert into public.events (id, seq, type) values ($1, 0, 't')", []string{"inserts an explicit event id"}},
			{"a.go", `insert into "events" (id, seq, type) values ($1, 0, 't')`, []string{"inserts an explicit event id"}},
			{"a.go", `insert into "public"."events" ("id", seq) values ($1, 0)`, []string{"inserts an explicit event id"}},
			{"a.go", "insert into public . events (id, seq) values ($1, 0)", []string{"inserts an explicit event id"}},
			{"a.go", "insert into events select * from staged", []string{"no column list"}},
			{"a.go", "insert into events as e (seq, type) values (0, 't')", []string{"no column list"}},
			{"a.go", "with d as (select nextval('events_id_seq') as id) insert into events (id, seq) select d.id, 0 from d",
				[]string{"names the events id sequence", "inserts an explicit event id"}},
			{"a.go", "insert into events (issue_key, seq, type, actor, payload, notify) select issue_key, seq, type, actor, payload, notify from unnest($1::text[])", nil},
			{"a.go", "insert into events_archive (id, seq) values ($1, 0)", nil},
			{"a.go", "select id, seq from events where id > $1", nil},
			{"../../../" + seedSequenceReset, "select setval('events_id_seq', $1)", nil},
			{"a.go", "select nextval(pg_get_serial_sequence($1, 'id'))", []string{"names the events id sequence"}},
			{"a.go", "select nextval(pg_get_serial_sequence($$events$$, 'id'))", []string{"names the events id sequence"}},
			{"a.go", "select pg_get_serial_sequence(table_name, 'id') from targets", []string{"names the events id sequence"}},
			{"a.go", "select pg_get_serial_sequence('issues', 'id')", nil},
			{"a.go", "create table event_children () inherits (events)", []string{"makes a table from events"}},
			{"a.go", `create table staged (note text) inherits (issues, "public"."events")`, []string{"makes a table from events"}},
			{"a.go", "create table events_2027 partition of events for values from (1) to (2)", []string{"makes a table from events"}},
			{"a.go", "create table staged (like events including defaults)", []string{"makes a table from events"}},
			{"a.go", "create table staged (like public.events including all)", []string{"makes a table from events"}},
			{"a.go", "create table staged (like events_archive including all)", nil},
			{"a.go", "select count(*) from events where type like 'events.%'", nil},
			{"a.go", "alter table rt_x inherit events", []string{"makes a table from events"}},
			{"a.go", `alter table rt_x inherit "public"."events"`, []string{"makes a table from events"}},
			{"a.go", "create table rt_x (note text, like events including defaults)", []string{"makes a table from events"}},
			{"a.go", "alter table events attach partition rt_x for values from (1) to (2)", []string{"makes a table from events"}},
			{"a.go", `alter table "public"."events" attach partition rt_x for values from (1) to (2)`, []string{"makes a table from events"}},
			{"a.go", "alter table if exists events attach partition rt_x for values from (1) to (2)", []string{"makes a table from events"}},
			{"a.go", "alter table delivery_log attach partition delivery_log_2027 for values from (1) to (2)", nil},
			{"a.go", "alter table rt_x no inherit events_archive", nil},
			{"a.go", "update events set payload = jsonb_set(payload, '{opened_event_id}', to_jsonb(id)) where id = pg_catalog.currval(pg_catalog.pg_get_serial_sequence('events', 'id'))", nil},
			{"a.go", "select pg_catalog.currval('events_id_seq')", nil},
			{"a.go", `select pg_catalog . currval( '"public"."events_id_seq"' )`, nil},
			{"a.go", `select pg_catalog.currval(pg_catalog.pg_get_serial_sequence('"public"."events"', 'id'))`, nil},
			{"a.go", "pg_catalog.currval(pg_catalog.pg_get_serial_sequence('events', 'id'))", nil},
			{"a.go", "where id=pg_catalog.currval('events_id_seq')", nil},
			{"a.go", "select nextval('events_id_seq'), pg_catalog.currval('events_id_seq')", []string{"names the events id sequence"}},
			{"a.go", "select setval('events_id_seq', pg_catalog.currval('events_id_seq'))", []string{"names the events id sequence"}},
			{"a.go", "select pg_catalog.currval(pg_get_serial_sequence($1, 'id'))", []string{"names the events id sequence"}},
			{"a.go", "select currval('events_id_seq')", []string{"names the events id sequence"}},
			{"a.go", "where id = currval(pg_get_serial_sequence('events', 'id'))", []string{"names the events id sequence"}},
			{"a.go", "select pg_catalog.currval(pg_get_serial_sequence('events', 'id'))", []string{"names the events id sequence"}},
			{"a.go", "select pg_catalog.currval('events_id_seq'::regclass)", []string{"names the events id sequence"}},
			{"a.go", "select draw_currval('events_id_seq')", []string{"names the events id sequence"}},
			{"a.go", "select util.currval('events_id_seq')", []string{"names the events id sequence"}},
			{"a.go", `select "util".currval(pg_get_serial_sequence('events', 'id'))`, []string{"names the events id sequence"}},
			{"a.go", "select util. currval('events_id_seq')", []string{"names the events id sequence"}},
			{"a.go", "select util./**/currval('events_id_seq')", []string{"names the events id sequence"}},
			{"a.go", "select util.\ncurrval('events_id_seq')", []string{"names the events id sequence"}},
			{"a.go", "select draw$currval('events_id_seq')", []string{"names the events id sequence"}},
			{"a.go", "select \u00e9currval('events_id_seq')", []string{"names the events id sequence"}},
			{"a.go", "select a$pg_catalog.currval('events_id_seq')", []string{"names the events id sequence"}},
			{"a.go", "select util.pg_catalog.currval('events_id_seq')", []string{"names the events id sequence"}},
			{"a.go", `select "pg_catalog".currval('events_id_seq')`, []string{"names the events id sequence"}},
			{"a.go", "select nextval('events_' || 'id_seq')", []string{"names the events id sequence"}},
			{"a.go", "select nextval(('events_' || 'id_seq')::regclass)", []string{"names the events id sequence"}},
			{"a.go", `select nextval(e'events\x5fid\x5fseq')`, []string{"names the events id sequence"}},
			{"a.go", `select nextval(u&'events\005fid\005fseq')`, []string{"names the events id sequence"}},
			{"a.go", "select nextval(format('%s_id_seq', 'events'))", []string{"names the events id sequence"}},
			{"a.go", "select nextval(pg_get_serial_sequence('event' || 's', 'id'))", []string{"names the events id sequence"}},
			{"a.go", "select setval('events_' || 'id_seq', 1)", []string{"names the events id sequence"}},
			{"a.go", "select nextval('events_id' || '_seq'::text)", []string{"names the events id sequence"}},
			{"a.go", "select nextval($1)", []string{"names the events id sequence"}},
			{"a.go", "select nextval('16402')", []string{"names the events id sequence"}},
			{"a.go", "select nextval('16402'::regclass)", []string{"names the events id sequence"}},
			{"a.go", "select setval( ' 16402 ' , 1)", []string{"names the events id sequence"}},
			{"a.go", "select nextval('other_seq'::text)", []string{"names the events id sequence"}},
			{"a.go", `select "nextval"('16402')`, []string{"names the events id sequence"}},
			{"a.go", `select "pg_catalog"."setval"('16402', 1)`, []string{"names the events id sequence"}},
			{"a.go", "select nextval/**/('16402')", []string{"names the events id sequence"}},
			{"a.go", "select nextval -- a comment ('16402')", []string{"names the events id sequence"}},
			{"a.go", `select u&"\006eextval"('16402')`, []string{"names the events id sequence"}},
			{"a.go", "select nextval(pg_catalog.currval('events_id_seq') * 0 + 16402)", []string{"names the events id sequence"}},
			{"a.go", "create function draw(regclass) returns bigint language internal as 'nextval_oid'", []string{"names the events id sequence"}},
			{"a.go", "select nextval('other_seq')", nil},
			{"a.go", "select nextval('other_seq'::regclass)", nil},
			{"a.go", "select nextval ( 'other_seq' :: regclass )", nil},
			{"a.go", "select setval('other_seq', 1), setval('other_seq', 1, false)", nil},
			{"a.go", sequencePattern, nil},
			{"a.go", currvalPattern, nil},
			{"a.go", currvalPattern + " select nextval('events_id_seq')", []string{"names the events id sequence"}},
		} {
			offences, _ := eventIDOffences(row.path, row.statement)
			if len(offences) != len(row.refused) {
				t.Errorf("%s: %d offences %q, want %d (%q)", row.statement, len(offences), offences, len(row.refused), row.refused)
				continue
			}
			for i, reason := range row.refused {
				if !strings.Contains(offences[i], reason) {
					t.Errorf("%s: offence %q, want one that %s", row.statement, offences[i], reason)
				}
			}
		}
	})

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
	lists, seedCopies := 0, 0
	for _, source := range sources {
		path := filepath.ToSlash(source.path)
		for _, statement := range sqlStatements(source.file, constants) {
			found, held := eventIDOffences(path, statement)
			for _, offence := range found {
				offend(offence)
			}
			lists += held
		}
		if copies := eventCopies(source.file, constants); copies > 0 {
			if strings.Contains(path, seedTool) {
				seedCopies += copies
			} else {
				offend(path + ": copies rows into events (a pgx.Identifier naming events), through no insert")
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
	if seedCopies == 0 {
		t.Fatalf("the walk saw no pgx.Identifier naming events, not even %s's offline copies: if those "+
			"moved or name the table another way, the copy check now passes by seeing nothing", seedTool)
	}
}
