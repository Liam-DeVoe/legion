package store

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// migrationEventIDSequence names the sequence the events id default draws from: by its name, bare,
// quoted or schema-qualified, or through pg_get_serial_sequence with the table in any of its
// spellings or with a table argument that is no string literal (a variable, a dollar-quoted
// string). It is api.eventIDSequence's pattern, and api.TestOnlyAnEventInsertsDefaultDrawsAnEventID
// fails when this declaration binds another.
var migrationEventIDSequence = regexp.MustCompile(`\bevents_id_seq\b|pg_get_serial_sequence\(\s*'\s*(?:"?public"?\s*\.\s*)?"?events"?\s*'|pg_get_serial_sequence\(\s*[^'\s]`)

// migrationEventIDCurrval is a read of the id this session's nextval last drew from
// events_id_seq, which draws no id and moves no counter: Postgres's own currval, bare or as
// pg_catalog.currval, and no function whose name only ends in currval or that another schema
// holds. It is api.eventIDCurrval's pattern, held to it the same way, and
// migrationEventIDOffences reads the SQL with each such read blanked out.
var migrationEventIDCurrval = regexp.MustCompile(`(?:^|[^\w.])(?:pg_catalog\s*\.\s*)?currval\(\s*(?:'\s*(?:"?public"?\s*\.\s*)?"?events_id_seq"?\s*'(?:\s*::\s*regclass)?|pg_get_serial_sequence\(\s*'\s*(?:"?public"?\s*\.\s*)?"?events"?\s*'\s*,\s*'\s*"?id"?\s*'\s*\))\s*\)`)

// migrationEventIDOffences is each place in a migration's SQL that names the events id sequence
// other than to read currval, as "<path>:<line>: <match>". Each currval read is blanked out
// before the sequence is looked for, its newlines kept, so the lines named stay the file's own.
func migrationEventIDOffences(path, sql string) []string {
	var offences []string
	lowered := migrationEventIDCurrval.ReplaceAllStringFunc(strings.ToLower(sql), func(read string) string {
		return strings.Map(func(r rune) rune {
			if r == '\n' {
				return r
			}
			return ' '
		}, read)
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
// it is still open. No migration may name the sequence; one that must (to alter it) needs its own
// reading of why the horizon still holds, and an exemption here that says so. A migration can
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
				"begin new.opened := currval(pg_get_serial_sequence('events', 'id')); return new; end $$;", 0},
			{"a draw beside a currval read", "select nextval('events_id_seq'), currval('events_id_seq');", 1},
			{"a currval read at a line's start, and pg_catalog's", "select\ncurrval('events_id_seq'), pg_catalog.currval('events_id_seq');", 0},
			{"a function whose name ends in currval", "select draw_currval('events_id_seq');", 1},
			{"another schema's currval", "select util.currval('events_id_seq');", 1},
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
