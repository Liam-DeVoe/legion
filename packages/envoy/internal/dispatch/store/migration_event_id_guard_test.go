package store

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/eventidtest"
)

// migrationEventIDSequence names the sequence the events id default draws from, in each spelling
// eventidtest.SequencePattern reads. The Go sources' guard (api.eventIDSequence) compiles the same
// pattern.
var migrationEventIDSequence = regexp.MustCompile(eventidtest.SequencePattern)

// migrationEventIDCurrval is pg_catalog.currval of the events id sequence, the one read of it that
// draws no id (eventidtest.CurrvalPattern says which spellings are that read and why no other is):
// migrationEventIDOffences reads the SQL with each such read blanked out. The Go sources' guard
// (api.eventIDCurrval) compiles the same pattern.
var migrationEventIDCurrval = regexp.MustCompile(eventidtest.CurrvalPattern)

// migrationEventIDOffences is each place in a migration's SQL that names the events id sequence
// other than to read currval, as "<path>:<line>: <match>". Each currval read is blanked out
// before the sequence is looked for, its newlines kept, so the lines named stay the file's own.
func migrationEventIDOffences(path, sql string) []string {
	var offences []string
	lowered := migrationEventIDCurrval.ReplaceAllStringFunc(strings.ToLower(sql), func(read string) string {
		kept := eventidtest.CurrvalReadKept(read)
		return kept + strings.Map(func(r rune) rune {
			if r == '\n' {
				return r
			}
			return ' '
		}, read[len(kept):])
	})
	for _, span := range migrationEventIDSequence.FindAllStringIndex(lowered, -1) {
		line := strings.Count(lowered[:span[0]], "\n") + 1
		offences = append(offences, path+":"+strconv.Itoa(line)+": "+lowered[span[0]:span[1]])
	}
	return offences
}

// TestNoMigrationDrawsAnEventIDOutsideTheInsertDefault is the migrations' half of
// api.TestOnlyAnEventInsertsDefaultDrawsAnEventID, which reads only Go sources. The server runs
// this SQL too (Store.Migrate applies migrationFiles), and a function, a view, a trigger or another
// table's column default defined here that drew from events_id_seq would hand out an event id
// before its writer holds RowExclusiveLock on events. The safe horizon dates a writer by that lock
// (events/horizon.go, "Safe horizon"), so it would pass such an id while the transaction holding
// it is still open. No migration may name the sequence, or use nextval or setval but in a call of
// one string literal of a sequence's name (one of digits alone is an OID), or their C code, which
// can draw from events_id_seq without spelling it; one that must name it (to alter it) needs its
// own reading of why the horizon still holds, and an exemption here that says so. A migration can
// also hand events' id default to another table without naming the sequence (like events
// including defaults, inherits, partition of), which no reading of the text sees:
// TestOnlyTheEventsIDDefaultDrawsFromItsSequence reads the migrated catalog for that.
func TestNoMigrationDrawsAnEventIDOutsideTheInsertDefault(t *testing.T) {
	t.Run("the pattern reads each shape that draws from the sequence", func(t *testing.T) {
		for _, row := range []struct {
			shape, sql string
			found      int
		}{
			{"a function that draws, then inserts", "create function f() returns bigint language plpgsql as $$\n" +
				"declare i bigint := nextval('events_id_seq');\nbegin\n  insert into events (id) values (i);\n  return i;\nend $$;", 1},
			{"a view", "create view next_ids as select nextval('public.events_id_seq') as id;", 1},
			{"another table's default", "alter table issues add column reserved bigint default nextval('\"events_id_seq\"'::regclass);", 1},
			{"a trigger through pg_get_serial_sequence, quoted", "create function r() returns trigger language plpgsql as $$\n" +
				"begin new.reserved := nextval(pg_get_serial_sequence('\"events\"', 'id')); return new; end $$;", 1},
			{"pg_get_serial_sequence, qualified and spaced", "select pg_get_serial_sequence( ' \"public\" . \"events\" ', 'id');", 1},
			{"the table's own definition", "create table events (\n  id bigserial primary key,\n  seq integer not null\n);", 0},
			{"another table's sequence", "select nextval('events_archive_id_seq');", 0},
			{"a view through pg_get_serial_sequence, dollar-quoted", "create view next_ids as select nextval(pg_get_serial_sequence($$events$$, $$id$$)) as id;", 1},
			{"a function through pg_get_serial_sequence of a variable", "create function d(t text) returns bigint language sql as $$\n" +
				"select nextval(pg_get_serial_sequence(t, 'id'))\n$$;", 1},
			{"pg_get_serial_sequence of another table", "select pg_get_serial_sequence('issues', 'id');", 0},
			{"a trigger that reads the id its insert drew", "create function o() returns trigger language plpgsql as $$\n" +
				"begin new.opened := pg_catalog.currval(pg_catalog.pg_get_serial_sequence('events', 'id')); return new; end $$;", 0},
			{"a draw beside a currval read", "select nextval('events_id_seq'), pg_catalog.currval('events_id_seq');", 1},
			{"pg_catalog's currval at a line's start", "select\npg_catalog.currval('events_id_seq');", 0},
			{"a bare currval, which a currval(text) of another schema takes over", "select currval('events_id_seq');", 1},
			{"a bare currval of the serial sequence", "select currval(pg_get_serial_sequence('events', 'id'));", 1},
			{"pg_catalog's currval of an unqualified pg_get_serial_sequence", "select pg_catalog.currval(pg_get_serial_sequence('events', 'id'));", 1},
			{"pg_catalog's currval through a regclass cast", "select pg_catalog.currval('events_id_seq'::regclass);", 1},
			{"a function whose name ends in currval", "select draw_currval('events_id_seq');", 1},
			{"another schema's currval", "select util.currval('events_id_seq');", 1},
			{"another schema's currval, spaced", "select util. currval('events_id_seq');", 1},
			{"another schema's currval, behind a comment", "select util./**/currval('events_id_seq');", 1},
			{"another schema's currval, across a line", "select util.\ncurrval('events_id_seq');", 1},
			{"a name carrying $ that ends in currval", "select draw$currval('events_id_seq');", 1},
			{"a name with a letter past ASCII that ends in currval", "select \u00e9currval('events_id_seq');", 1},
			{"a name carrying $ that ends in pg_catalog", "select a$pg_catalog.currval('events_id_seq');", 1},
			{"a currval(text) that draws", "create function currval(text) returns bigint language sql as $$ select nextval($1::regclass) $$;\n" +
				"select currval('events_id_seq');", 2},
			{"a name built from parts", "select nextval('events_' || 'id_seq');", 1},
			{"a name built from parts, cast", "select nextval(('events_' || 'id_seq')::regclass);", 1},
			{"an escape string", `select nextval(E'events\x5fid\x5fseq');`, 1},
			{"a Unicode escape string", `select nextval(U&'events\005fid\005fseq');`, 1},
			{"a name from format()", "select nextval(format('%s_id_seq', 'events'));", 1},
			{"a table name built from parts", "select nextval(pg_get_serial_sequence('event' || 's', 'id'));", 1},
			{"a setval of a name built from parts", "select setval('events_' || 'id_seq', 1);", 1},
			{"a literal cast through text", "select nextval('other_seq'::text);", 1},
			{"a setval of a sequence by its OID", "select setval('16402', 1);", 1},
			{"a nextval of a sequence by its OID, cast", "select nextval('16402'::regclass);", 1},
			{"a quoted nextval of an OID", `select "nextval"('16402');`, 1},
			{"a quoted, qualified setval of an OID", `select "pg_catalog"."setval"('16402', 1);`, 1},
			{"a nextval behind a comment", "select nextval/**/('16402');", 1},
			{"a nextval across a line comment", "select nextval -- a comment\n('16402');", 1},
			{"a nextval in Unicode escapes", `select U&"\006Eextval"('16402');`, 1},
			{"a nextval around a currval read", "select nextval(pg_catalog.currval('events_id_seq') * 0 + 16402);", 1},
			{"nextval's C function under another name", "create function draw(regclass) returns bigint language internal as 'nextval_oid';", 1},
			{"another sequence, by literal", "select nextval('other_seq'), nextval('other_seq'::regclass), setval('other_seq', 1, false);", 0},
			{"another table's serial default", "alter table issues alter column n set default nextval('issues_n_seq'::regclass);", 0},
		} {
			if got := migrationEventIDOffences("m.sql", row.sql); len(got) != row.found {
				t.Errorf("%s: %d matches %q, want %d", row.shape, len(got), got, row.found)
			}
		}
	})

	var offences []string
	read := 0
	if err := fs.WalkDir(migrationFiles, "migrations", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".sql") {
			return err
		}
		data, err := migrationFiles.ReadFile(path)
		if err != nil {
			return err
		}
		read++
		offences = append(offences, migrationEventIDOffences(path, string(data))...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if read == 0 {
		t.Fatal("read no migration: this guard would pass by seeing nothing")
	}
	if len(offences) > 0 {
		t.Fatalf("a migration names the events id sequence, so the SQL the server runs can draw an event "+
			"id outside the column default of the insert that writes its row:\n\t%s\n"+
			"Let the insert's default draw every event id (events.Broker.Append): the safe horizon dates a "+
			"writer by the RowExclusiveLock its insert takes before the default draws (events/horizon.go), "+
			"and an id drawn any other way is held where the lock table cannot show it.", strings.Join(offences, "\n\t"))
	}
	t.Logf("read %d migration files", read)
}
