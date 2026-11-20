//go:build !race

// The guard is static, as lock_level_test.go's is and for the reason given there: it reads the
// module's source, so the race detector adds nothing to it.

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
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
	t.Run("the parser reads every statement that writes rows into events", func(t *testing.T) {
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
			analysis, err := eventidtest.Analyze(row.statement)
			if err != nil {
				t.Errorf("%s: parse: %v", row.statement, err)
				continue
			}
			if got := analysis.EventInserts > 0; got != row.writes {
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
		for _, statement := range sqlTemplatesByFunction(source.file, constants) {
			analysis, err := eventidtest.Analyze(statement.statement)
			if err != nil || analysis.EventInserts == 0 {
				continue
			}
			if strings.HasSuffix(path, brokerEventInsert) {
				broker += analysis.EventInserts
				continue
			}
			offences = append(offences, path+": "+statement.statement)
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

// seedSequenceReset is the one source file allowed to reset events_id_seq after it fills an empty
// database offline, with no Dispatch serving it (cmd/dispatch-seed).
const seedSequenceReset = "cmd/dispatch-seed/write_events.go"

// seedTool is the one place allowed to copy rows into events: it fills an empty database offline.
const seedTool = "cmd/dispatch-seed/"

// eventIDOffences reads a complete SQL string with the PostgreSQL parser. It returns each draw or
// unsafe construct outside the broker's insert path and the number of event inserts with columns.
func eventIDOffences(path, statement string) (offences []string, lists int) {
	analysis, err := eventidtest.Analyze(statement)
	if err != nil {
		return []string{path + ": cannot parse SQL (sha256=" + sqlDigest(statement) + "): " + statement}, 0
	}
	for _, finding := range analysis.Findings {
		switch finding.Kind {
		case eventidtest.SequenceCall:
			if finding.Name == "setval" && strings.Contains(path, seedSequenceReset) {
				continue
			}
			if finding.Name == "currval" && strings.Contains(strings.ToLower(statement), "pg_catalog.currval(pg_catalog.pg_get_serial_sequence('events', 'id'))") {
				continue
			}
			offences = append(offences, path+": calls "+finding.Name+" outside the broker's event insert path: "+statement)
		case eventidtest.DynamicSQL:
			offences = append(offences, path+": runs SQL a static guard cannot read ("+finding.Name+"): "+statement)
		case eventidtest.EventInsertWithoutColumns:
			offences = append(offences, path+": inserts into events with no column list, which fills id by position: "+statement)
		case eventidtest.EventInsertExplicitID:
			offences = append(offences, path+": inserts an explicit event id: "+statement)
		case eventidtest.DerivedEventTable:
			offences = append(offences, path+": makes a table from events, which takes its id default or holds its rows: "+statement)
		}
	}
	return offences, analysis.EventInsertColumnLists
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

func scratchSourceEventIDOffences(t *testing.T, sql string) []string {
	t.Helper()
	source, err := parser.ParseFile(token.NewFileSet(), "scratch.go", "package scratch\nfunc planted() { database.Query(ctx, `"+sql+"`) }\n", 0)
	if err != nil {
		t.Fatalf("parse scratch source: %v", err)
	}
	templates := sqlTemplatesByFunction(source, map[string]string{})
	if len(templates) != 1 {
		t.Fatalf("the scratch source held %d query templates, want one", len(templates))
	}
	offences, _ := eventIDOffences("scratch.go:planted", templates[0].statement)
	return offences
}

// sqlLooking identifies the SQL statements that can create an event-id draw. A source string is
// SQL-looking when it starts with a query form or one of the structural forms below; set, create
// and alter require their SQL-only second word so CLI help and prose do not enter the reader.
func sqlLooking(statement string) bool {
	words := strings.Fields(strings.ToLower(statement))
	if len(words) == 0 {
		return false
	}
	switch words[0] {
	case "begin", "copy", "delete", "do", "explain", "insert", "merge", "select", "update", "values":
		return true
	case "with":
		return len(words) > 1 && (words[1] == "recursive" || strings.Contains(statement, "select "))
	case "create":
		return len(words) > 1 && (words[1] == "function" || words[1] == "procedure" || words[1] == "table" || words[1] == "trigger" || words[1] == "view")
	case "alter":
		return len(words) > 1 && (words[1] == "function" || words[1] == "procedure" || words[1] == "table")
	case "set":
		return len(words) > 1 && (words[1] == "local" || words[1] == "session" || words[1] == "transaction" || words[1] == "constraints" || words[1] == "role" || words[1] == "search_path" || words[1] == "standard_conforming_strings")
	case "prepare":
		return strings.Contains(statement, " as ")
	}
	return false
}

// eventIDSQLLooking narrows the parsed source strings to this guard's risk: a sequence or dynamic
// SQL function, or a fmt.Sprintf query. The latter is always unreadable at compile time, even if
// the format contains no sequence name, and so must be recorded or refused.
func eventIDSQLLooking(statement string, complete bool) bool {
	lower := strings.ToLower(statement)
	for _, name := range []string{"nextval", "setval", "currval", "query_to_xml", "cursor_to_xml"} {
		if strings.Contains(lower, name) {
			return true
		}
	}
	return strings.Contains(lower, "insert into events") || (!complete && strings.Contains(statement, "%"))
}

// unreadableEventIDSQL is the checked-in account of SQL-looking strings a static reader cannot
// parse. A string assembled from run-time values is unreadable to a static check; it must stay here
// with its file, function and review reason. The test fails both for an unlisted parse error and
// for a stale entry.
type unreadableEventIDSQL struct {
	file, function, digest, reason string
}

var knownUnreadableEventIDSQL = []unreadableEventIDSQL{
	{"../../../internal/dispatch/api/comment_queries.go", "loadOwnerComments", "f9cc6c9af72d4cd418a4d0569f0001b0f3f050c4018aad2bcacdff5096e6e69e", "ownerColumn comes only from the closed issue_key/artifact_id switch"},
	{"../../../internal/dispatch/api/comment_queries.go", "loadReplyChain", "2cf132bbeff0915d8bc5b319c9c3bed1b0ce69b8198b0a18c07d03e7dd934549", "the reply owner column comes only from the closed reply-chain selector"},
	{"../../../internal/dispatch/api/duplicates.go", "readDuplicateCandidates", "296514f180e5344466c912468f1abe88fbae6fa9eb49201d6fc81fe78286620d", "the timeout format is an integer millisecond value"},
	{"../../../internal/dispatch/docs/anchor_blocks.go", "anchorBlockCandidates", "f76465317b0ad434e9dfe1c910c53ccf53863951a0009760f2b35db17bd19742", "table comes from the closed anchor target set"},
	{"../../../internal/dispatch/docs/anchor_blocks.go", "backfillAnchorBlock", "26e1d2ef880961709520fc7890e8bd858b68ee156c14500b5741807f957b4283", "table comes from the closed anchor target set"},
	{"../../../internal/dispatch/docs/anchor_blocks.go", "backfillAnchorBlock", "fd2688abb569be5d3d1e8bf1bb0c1a9ced2a342da1568946ffb50ef595c2f30c", "table comes from the closed anchor target set"},
	{"../../../internal/dispatch/docs/anchor_refresh.go", "refreshAnchors", "1b6941263ffee2016536d24837d4879585ce54f5b10c59ff6ccc469033032861", "table and predicates come from each closed anchor target"},
	{"../../../internal/dispatch/events/broker.go", "QueueAppendWithLogins", "13f336feaef55cb2a2f23a01bcaa79c98e1818c3822eb700becd8b594ac9e63e", "stored payload and logins are built only by the broker's fixed write paths"},
}

func sqlDigest(statement string) string {
	sum := sha256.Sum256([]byte(statement))
	return hex.EncodeToString(sum[:])
}

func unreadableEventIDSQLIndex(path, function, statement string) int {
	digest := sqlDigest(statement)
	for index, known := range knownUnreadableEventIDSQL {
		if known.file == path && known.function == function && known.digest == digest {
			return index
		}
	}
	return -1
}

func unreadableSQLTemplate(path, function string, template sqlTemplate) (index int, offence string) {
	index = unreadableEventIDSQLIndex(path, function, template.statement)
	if !template.complete && index < 0 {
		offence = path + ":" + function + ": cannot statically read SQL assembled at run time (sha256=" + sqlDigest(template.statement) + "): " + template.statement
	}
	return index, offence
}

func staleUnreadableSQL(used []bool) []string {
	var stale []string
	for index, known := range knownUnreadableEventIDSQL {
		if !used[index] {
			stale = append(stale, known.file+":"+known.function+": stale unreadable SQL entry: "+known.reason)
		}
	}
	return stale
}

// TestOnlyAnEventInsertsDefaultDrawsAnEventID enforces the other half of the safe horizon's proof:
// the event insert holds RowExclusiveLock before its default draws the id. It parses every
// SQL-looking source string and every substatement, including SQL in routine bodies.
func TestOnlyAnEventInsertsDefaultDrawsAnEventID(t *testing.T) {
	t.Run("the parser reads sequence calls and nested statements", func(t *testing.T) {
		for _, row := range []struct {
			statement string
			want      int
		}{
			{"select nextval('events_id_seq')", 1},
			{`select "pg_catalog"."setval"('16402', 1)`, 1},
			{`select pg_catalog.currval('other_seq')`, 1},
			{"insert into events (id, seq) values ($1, 0)", 1},
			{"insert into events select $1, 0", 1},
			{"create table event_children () inherits (events)", 1},
			{"alter table events attach partition event_children for values from (1) to (2)", 1},
			{"select pg_catalog.currval(pg_catalog.pg_get_serial_sequence('events', 'id'))", 0},
			{`create trigger events_before before insert on events for each row execute function f()`, 0},
			{`select query_to_xml('select next' || 'val(16402)', false, false, '')`, 1},
			{`select query_to_xmlschema('select 1', false, false, '', '')`, 1},
			{`select cursor_to_xml('cursor', 1, false, false, '')`, 1},
			{`select cursor_to_xmlschema('cursor', 1, false, false, '', '')`, 1},
			{`do $$ begin execute 'select nextval(''events_id_seq'')'; end $$`, 1},
			{`do $$ declare query text := 'select 1'; begin execute query; end $$`, 1},
			{"prepare read_events as select 1; execute read_events", 0},
			{"explain (format json) execute list_open_asks('nonexistent-issue')", 0},
			{"explain (format json) execute owed_keyed_sends(60, 86400, 100)", 0},
			{"explain (costs off) execute recorded_marks('00000000-0000-4000-8000-00000000000a')", 0},
			{"explain execute session_answers('session-1', now() - interval '72 hours', 'notifications.agent.session-1')", 0},
			{"explain (analyze, format json) execute generic_listing('LIST', '{}', '', null, '{}', false, '{}', false, null, null, '{}', false, false, true)", 0},
		} {
			offences, _ := eventIDOffences("a.go", row.statement)
			if len(offences) != row.want {
				t.Errorf("%s: %d offences %q, want %d", row.statement, len(offences), offences, row.want)
			}
		}
	})

	t.Run("the scratch Go source refuses every wrapped dynamic statement", testEventIDGuardReadsWrappedDynamicSQL)
	t.Run("an unlisted Sprintf query fails closed", testEventIDGuardRefusesAnUnlistedFormattedQuery)
	t.Run("a stale unreadable SQL entry fails closed", testEventIDGuardRefusesAStaleUnreadableSQLEntry)

	sources := moduleSources(t)
	constants, err := stringConstants(sources)
	if err != nil {
		t.Fatal(err)
	}
	var offences []string
	seen := map[string]bool{}
	offend := func(offence string) {
		if !seen[offence] {
			seen[offence] = true
			offences = append(offences, offence)
		}
	}
	usedUnreadable := make([]bool, len(knownUnreadableEventIDSQL))
	lists, seedCopies := 0, 0
	// The event-id proof applies to the Dispatch database. Other module packages keep separate
	// stores; dispatch-seed is the one command that intentionally resets this database's sequence.
	for _, source := range sources {
		path := filepath.ToSlash(source.path)
		if !strings.Contains(path, "/internal/dispatch/") && !strings.HasSuffix(path, seedSequenceReset) {
			continue
		}
		for _, located := range sqlTemplatesByFunction(source.file, constants) {
			if !sqlLooking(located.statement) || !eventIDSQLLooking(located.statement, located.complete) {
				continue
			}
			function := located.function
			if function == "" {
				function = located.value
			}
			if function == "" {
				function = "<package>"
			}
			index, unreadable := unreadableSQLTemplate(path, function, located)
			if unreadable != "" {
				offend(unreadable)
			} else if index >= 0 {
				usedUnreadable[index] = true
			}
			found, held := eventIDOffences(path+":"+function, located.statement)
			if len(found) == 1 && strings.Contains(found[0], "cannot parse SQL") {
				if index >= 0 {
					usedUnreadable[index] = true
					continue
				}
			}
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
	for _, stale := range staleUnreadableSQL(usedUnreadable) {
		offend(stale)
	}
	if len(offences) > 0 {
		t.Fatalf("an event id is drawn outside the column default of the insert that writes its row:\n\t%s\n"+
			"Let the insert's default draw every event id (events.Broker.Append): the safe horizon dates a "+
			"writer by the RowExclusiveLock its insert takes before the default draws (events/horizon.go), "+
			"and an id drawn any other way is held where the lock table cannot show it.", strings.Join(offences, "\n\t"))
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

// testEventIDGuardReadsWrappedDynamicSQL plants each review spelling in a Go source string. The
// migration guard has the same cases in its scratch-migration table.
func testEventIDGuardReadsWrappedDynamicSQL(t *testing.T) {
	for _, sql := range []string{
		`explain analyze select query_to_xml('select next' || 'val(16402)', false, false, '')`,
		`set local statement_timeout = '1s'; select query_to_xml('select next' || 'val(16402)', false, false, '')`,
		`values (query_to_xml('select next' || 'val(16402)', false, false, ''))`,
		`copy (select query_to_xml('select next' || 'val(16402)', false, false, '')) to stdout`,
		`begin; do $$ declare query text := 'select 1'; begin execute query; end $$; commit`,
		`do $$ begin execute 'select nextval(' || 16402 || ')'; end $$`,
		`set local standard_conforming_strings = off; select nextval('events\137id\137seq')`,
		`select query_to_xml('select next' || 'val(16402)', false, false, '')`,
	} {
		if offences := scratchSourceEventIDOffences(t, sql); len(offences) == 0 {
			t.Errorf("the guard did not refuse dynamic SQL wrapped in %q", sql)
		}
	}

	source, err := parser.ParseFile(token.NewFileSet(), "scratch.go", "package scratch\nfunc planted() { query := `select query_to_xml('select next' || 'val(16402)', false, false, '')`; database.Query(ctx, query) }\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	templates := sqlTemplatesByFunction(source, map[string]string{})
	if len(templates) != 1 {
		t.Fatalf("the local SQL source held %d query templates, want one", len(templates))
	}
	if offences, _ := eventIDOffences("scratch.go:planted", templates[0].statement); len(offences) == 0 {
		t.Fatal("the guard did not read a SQL string held in a local query variable")
	}
}

func testEventIDGuardRefusesAnUnlistedFormattedQuery(t *testing.T) {
	source, err := parser.ParseFile(token.NewFileSet(), "scratch.go", "package scratch\nfunc planted() { database.Query(ctx, fmt.Sprintf(`select %s`, column)) }\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	templates := sqlTemplatesByFunction(source, map[string]string{})
	if len(templates) != 1 {
		t.Fatalf("the scratch source held %d query templates, want one", len(templates))
	}
	if !sqlLooking(templates[0].statement) || !eventIDSQLLooking(templates[0].statement, templates[0].complete) {
		t.Fatalf("the Sprintf query did not read as SQL-looking: %#v", templates[0])
	}
	if _, offence := unreadableSQLTemplate("scratch.go", "planted", templates[0]); offence == "" {
		t.Fatal("the unlisted Sprintf query passed the fail-closed reader")
	}
}

func testEventIDGuardRefusesAStaleUnreadableSQLEntry(t *testing.T) {
	stale := staleUnreadableSQL(make([]bool, len(knownUnreadableEventIDSQL)))
	if len(stale) != len(knownUnreadableEventIDSQL) {
		t.Fatalf("removing every listed source left %d stale entries, want %d", len(stale), len(knownUnreadableEventIDSQL))
	}
}
