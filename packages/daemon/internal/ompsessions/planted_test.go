package ompsessions

import (
	"context"
	"slices"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
)

// The labels the planted objects record in public.plant_runs when a statement runs them.
const (
	plantedRight = "public.right(text, double precision)"
	plantedFiles = "the view omp_session_files in the schema named for the login"
	plantedParts = "the view omp_session_files_parts in the schema named for the login"
)

// planted is a database another login has planted objects in, and what reads it.
type planted struct {
	// conn is the package's own client, ConnectURL, signed in as the test's login.
	conn *pgx.Conn
	// admin is a second connection as the same login, for the test's own reads and writes.
	admin *pgx.Conn
}

// runs is the labels public.plant_runs holds, each the record of one run of a planted object.
func (p planted) runs(t *testing.T) []string {
	t.Helper()
	rows, err := p.admin.Query(context.Background(), "SELECT what FROM public.plant_runs")
	if err != nil {
		t.Fatal(err)
	}
	ran, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return ran
}

// plantDatabase is a new database that a role of its own, other than the test's login, plants
// objects in: the role holds CREATE on the database and on its public schema, as the session
// database's agent login and its owner do, and it owns public.plant_runs and the function
// public.plant_ran(what), which records what in that table as the role, whoever calls it. With
// tables the database holds Oh My Pi's two tables in public first, as Oh My Pi makes them
// (createTables). plant is the role's statements, given the test's login as an identifier. The
// role and everything it made are dropped when the test ends.
func plantDatabase(t *testing.T, prefix string, tables bool, plant func(login string) []string) planted {
	t.Helper()
	ctx := context.Background()
	dsn := testdb.Database(t, prefix)
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	conn, err := ConnectURL(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	var login, database string
	if err := admin.QueryRow(ctx, "SELECT current_user, current_database()").Scan(&login, &database); err != nil {
		t.Fatal(err)
	}
	planter, err := testdb.RandomName("legion_ompsessions_planter")
	if err != nil {
		t.Fatal(err)
	}
	role := pgx.Identifier{planter}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE ROLE "+role+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	// What the role made is in this database alone, so dropping it here frees the role to be
	// dropped, before testdb.Database's cleanup, registered earlier and so run later, drops the
	// database.
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP OWNED BY "+role); err != nil {
			t.Errorf("drop what %s planted: %v", planter, err)
		}
		if _, err := admin.Exec(context.Background(), "DROP ROLE "+role); err != nil {
			t.Errorf("drop %s: %v", planter, err)
		}
	})
	var statements []string
	if tables {
		statements = append(statements, createTables...)
	}
	statements = append(statements,
		// A login that is no superuser may SET ROLE only to a role it is a member of with the SET
		// option, which creating the role does not give it from PostgreSQL 16 on.
		"GRANT "+role+" TO CURRENT_USER WITH INHERIT FALSE",
		"GRANT CREATE ON DATABASE "+pgx.Identifier{database}.Sanitize()+" TO "+role,
		"GRANT CREATE ON SCHEMA public TO "+role,
		"SET ROLE "+role,
		"CREATE TABLE public.plant_runs (what text NOT NULL)",
		"GRANT SELECT ON public.plant_runs TO PUBLIC",
		"CREATE FUNCTION public.plant_ran(what text) RETURNS boolean LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog "+
			"AS $$ INSERT INTO public.plant_runs VALUES (what) RETURNING true $$")
	statements = append(statements, plant(pgx.Identifier{login}.Sanitize())...)
	statements = append(statements, "RESET ROLE")
	for _, statement := range statements {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatalf("plant the database: %s: %v", statement, err)
		}
	}
	return planted{conn: conn, admin: admin}
}

// added is what after holds beyond before, each label as often as it is new.
func added(before, after []string) []string {
	var more []string
	left := slices.Clone(before)
	for _, label := range after {
		if i := slices.Index(left, label); i >= 0 {
			left = slices.Delete(left, i, i+1)
			continue
		}
		more = append(more, label)
	}
	return more
}

// A login that may create in the session database's public schema, or in the database, as its
// agent login and its owner may, plants two kinds of object that a statement naming its objects
// bare runs, as whoever runs the statement. One is public.right(text, double precision):
// FindByID's suffix compare, right(path, $1), resolves to it over pg_catalog's right(text,
// integer), wherever pg_catalog is on the search_path, because an argument of no declared type,
// which a statement's parameter is, takes the preferred type of the category every candidate
// accepts, and double precision is the numeric category's. (A plant of right(text, integer)
// itself loses to pg_catalog's, which the path searches first; one of right(text, text) wins, an
// untyped argument preferring a string type, but pgx then cannot send the Go int it binds as
// text, so FindByID fails rather than runs it.) The other is a schema named for the login that
// runs the statement, which the default search_path, "$user", public, searches before public,
// holding a view of each of Oh My Pi's two tables that records each row it reads or writes. The
// control is two of the package's statements in the form they had before their pin, naming each
// table bare: each runs a plant, so the plants are live in this database. Every statement the
// package runs reads and writes Oh My Pi's own tables in public, and runs none of them.
func TestNoStatementRunsAnObjectAnotherLoginPlanted(t *testing.T) {
	ctx := context.Background()

	// A table of the name in a schema the search_path reaches first stands in for the one in
	// public, so a bare CREATE TABLE IF NOT EXISTS makes nothing and every later statement finds
	// no table of Oh My Pi's.
	t.Run("CreateTables makes the tables in public whatever a planted schema holds", func(t *testing.T) {
		db := plantDatabase(t, "legion_ompsessions_plant_create", false, func(login string) []string {
			return []string{
				"CREATE SCHEMA " + login,
				"GRANT USAGE ON SCHEMA " + login + " TO PUBLIC",
				"CREATE TABLE " + login + ".omp_session_files (path text)",
				"CREATE TABLE " + login + ".omp_session_files_parts (path text)",
			}
		})
		if err := CreateTables(ctx, db.conn); err != nil {
			t.Fatal(err)
		}
		var files, parts bool
		if err := db.admin.QueryRow(ctx, "SELECT pg_catalog.to_regclass('public.omp_session_files') IS NOT NULL, "+
			"pg_catalog.to_regclass('public.omp_session_files_parts') IS NOT NULL").Scan(&files, &parts); err != nil {
			t.Fatal(err)
		}
		if !files || !parts {
			t.Errorf("after CreateTables public holds omp_session_files %t and omp_session_files_parts %t, want both: "+
				"the planted schema's table of each name stood in for it", files, parts)
		}
	})

	db := plantDatabase(t, "legion_ompsessions_plant", true, func(login string) []string {
		return []string{
			"CREATE FUNCTION public.right(s text, n double precision) RETURNS text LANGUAGE sql " +
				"AS $$ SELECT CASE WHEN public.plant_ran('" + plantedRight + "') THEN pg_catalog.right(s, n::integer) END $$",
			"CREATE SCHEMA " + login,
			"GRANT USAGE ON SCHEMA " + login + " TO PUBLIC",
			"CREATE VIEW " + login + ".omp_session_files WITH (security_invoker = true) AS SELECT * FROM public.omp_session_files " +
				"WHERE public.plant_ran('" + plantedFiles + "') WITH LOCAL CHECK OPTION",
			"CREATE VIEW " + login + ".omp_session_files_parts WITH (security_invoker = true) AS SELECT * FROM public.omp_session_files_parts " +
				"WHERE public.plant_ran('" + plantedParts + "') WITH LOCAL CHECK OPTION",
			"GRANT SELECT, INSERT, UPDATE, DELETE ON " + login + ".omp_session_files, " + login + ".omp_session_files_parts TO PUBLIC",
		}
	})
	if _, err := db.admin.Exec(ctx, "INSERT INTO public.omp_session_files (path, content, mtime_ms, byte_len) VALUES ($1, $2, $3, $4)",
		podSessionFile, session, podSessionWritten, len(session)); err != nil {
		t.Fatal(err)
	}

	live := t.Run("control: the statements as they were before their pin run the plants", func(t *testing.T) {
		before := db.runs(t)
		var mtime int64
		if err := db.conn.QueryRow(ctx, "SELECT mtime_ms FROM omp_session_files WHERE path = $1", podSessionFile).Scan(&mtime); err != nil {
			t.Fatal(err)
		}
		suffix := "_" + podSessionID + ".jsonl"
		var path string
		if err := db.conn.QueryRow(ctx, "SELECT path FROM omp_session_files WHERE right(path, $1) = $2 ORDER BY mtime_ms DESC, path ASC LIMIT 1",
			utf8.RuneCountInString(suffix), suffix).Scan(&path); err != nil {
			t.Fatal(err)
		}
		ran := added(before, db.runs(t))
		for _, plant := range []string{plantedFiles, plantedRight} {
			if !slices.Contains(ran, plant) {
				t.Errorf("the bare statements ran %q and not %s", ran, plant)
			}
		}
	})
	if !live {
		t.Fatal("a plant the bare statements do not run is no plant: the rows below would pass whatever the package runs")
	}

	first, second := "{\"n\":1}\n", "{\"n\":2}\n"
	for _, client := range []struct {
		name string
		run  func(t *testing.T)
	}{
		{"Written", func(t *testing.T) {
			if at, found, err := Written(ctx, db.conn, podSessionFile); err != nil || !found || at.UnixMilli() != podSessionWritten {
				t.Errorf("Written = %v, %t, %v; want the row's %d", at, found, err, podSessionWritten)
			}
		}},
		{"FindByID", func(t *testing.T) {
			if path, found, err := FindByID(ctx, db.conn, podSessionID); err != nil || !found || path != podSessionFile {
				t.Errorf("FindByID = %q, %t, %v; want %s", path, found, err, podSessionFile)
			}
		}},
		{"Read", func(t *testing.T) {
			if content, found, err := Read(ctx, db.conn, podSessionFile); err != nil || !found || string(content) != session {
				t.Errorf("Read = %q, %t, %v; want the row's content", content, found, err)
			}
		}},
		{"Import", func(t *testing.T) {
			for _, want := range []Outcome{Copied, Identical} {
				if outcome, err := Import(ctx, db.conn, sessionPath, []byte(session), time.Now()); err != nil || outcome != want {
					t.Errorf("Import = %q, %v; want %q", outcome, err, want)
				}
			}
		}},
		{"Replace", func(t *testing.T) {
			if err := Replace(ctx, db.conn, claudeTranscript, Absent, []byte(first), time.Now()); err != nil {
				t.Errorf("Replace = %v", err)
			}
		}},
		{"Append", func(t *testing.T) {
			if err := Append(ctx, db.conn, claudeTranscript, int64(len(first)), []byte(second), time.Now()); err != nil {
				t.Errorf("Append = %v", err)
			}
			var parts int
			if err := db.admin.QueryRow(ctx, "SELECT count(*) FROM public.omp_session_files_parts WHERE path = $1", claudeTranscript).Scan(&parts); err != nil {
				t.Fatal(err)
			}
			if parts != 2 {
				t.Errorf("public.omp_session_files_parts holds %d parts of %s, want Replace's and Append's", parts, claudeTranscript)
			}
		}},
		{"CreateTables", func(t *testing.T) {
			if err := CreateTables(ctx, db.conn); err != nil {
				t.Errorf("CreateTables = %v", err)
			}
		}},
	} {
		t.Run(client.name, func(t *testing.T) {
			before := db.runs(t)
			client.run(t)
			if ran := added(before, db.runs(t)); len(ran) > 0 {
				t.Errorf("%s ran what another login planted: %q", client.name, ran)
			}
		})
	}
}
