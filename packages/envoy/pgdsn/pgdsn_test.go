package pgdsn

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// urlDSN is a postgres URL with the user legion and password, built from parts, so no source line
// holds a URL with userinfo and its "@".
func urlDSN(password string) string {
	return "postgres://legion:" + password + "@" + "db.invalid:5432/legion"
}

// madeUp are the parts of the made-up passwords below; no refusal and no connect error of a DSN
// that spills holds any of them.
var madeUp = []string{"madeuphead", "zqtail", "48213", "db.invalid"}

// spilled are DSNs whose made-up password leaves part of itself after pgx's user part, each named
// by its shape: the "@" shapes leave the password's tail in the host list, the database or the
// query, the "/" shapes end pgx's search for the userinfo before the password's "@". The round-8
// shapes (`@…/` and after) passed the rule before it read every "@" after the user part, which cut
// the host list at the first "/" or "?" and read nothing after it once a userinfo was there.
var spilled = map[string]string{
	"@":              urlDSN("madeuphead0123@zqtail0123"),
	"/ after digits": urlDSN("48213579/zqtail0123"),
	"/ and ?":        "postgresql://legion:48213579/x?zqtail0123" + "@db.invalid/legion",
	"@…/":            urlDSN("madeuphead0123@zqtailone/zqtailtwo"),
	"@…?":            urlDSN("madeuphead0123@zqtailone?zqtailtwo"),
	"@…?k=":          urlDSN("madeuphead0123@zqtailone?k="),
	"@…?k=…":         urlDSN("madeuphead0123@zqtailone?k=zqtailtwo"),
	"@…,…/":          urlDSN("madeuphead0123@zqtailone,zqtailtwo/zqtailthree"),
	"@[…/…]":         urlDSN("madeuphead0123@[zqtailone/zqtailtwo]"),
	"48213/…?k=v":    urlDSN("48213/zqtailone?k=v"),
}

// Refusal refuses each spilled DSN by the setting alone, says to write the "@" as %40, and holds
// no part of the password.
func TestRefusalNamesTheSettingOfEachSpilledShape(t *testing.T) {
	for shape, dsn := range spilled {
		t.Run(shape, func(t *testing.T) {
			err := Refusal("postgres_dsn", dsn)
			if err == nil {
				t.Fatalf("Refusal = nil, want the %s shape refused", shape)
			}
			if !strings.HasPrefix(err.Error(), `postgres_dsn cannot be read: it holds an "@" after its user name and password`) ||
				!strings.Contains(err.Error(), "part of its user name, password or database name") || !strings.Contains(err.Error(), "%40") {
				t.Errorf("Refusal = %v, want it named by postgres_dsn with the %%40 remedy", err)
			}
			for _, part := range madeUp {
				if strings.Contains(err.Error(), part) {
					t.Errorf("Refusal = %v, which holds %q", err, part)
				}
			}
		})
	}
}

// Refusal keeps a DSN whose only "@" ends its userinfo, one whose "@" and "/" are percent-encoded,
// one whose password holds a "?" or a "#" (which pgx reads as the password), IPv6 and unix-socket
// hosts, keyword DSNs (a database whose name begins with "postgres", a password holding
// "postgres", a Cloud SQL IAM user holding an "@" among them), the empty DSN, which takes PG*
// from the environment, and the shapes the deployments write (the broker's, the daemon's and
// Dispatch's URL with its query, and the session database's, whose password holds a %40).
func TestRefusalKeepsADSNWithNoAtPastItsUserPart(t *testing.T) {
	at := "@"
	for _, dsn := range []string{
		urlDSN("legion"),
		urlDSN("pa?ss"),
		urlDSN("pa#ss"),
		urlDSN("p%40ss%2Fword"),
		"postgres://legion" + at + "[fe80::1%25eth0]:5432/legion",
		"postgres://[::1]:5432,db.invalid/legion?sslmode=disable",
		"postgres:///legion?host=/var/run/postgresql",
		"postgres:///legion?host=/cloudsql/p:r:i&user=sa%40project.iam",
		"postgres://h/my%40db",
		"postgres://broker:Abc123def456" + at + "127.0.0.1:5432/cipher?sslmode=disable&pool_max_conns=4",
		"postgres://sessions_w:Abc%40123" + at + "10.1.2.3:5432/sessions?sslmode=require&application_name=omp-sessions%20w",
		"postgresql://legion:Abc123def456" + at + "db.example:5432/legion",
		"host=db.invalid user=legion password=p@ss/word dbname=legion",
		"host=db.invalid user=postgres dbname=postgres sslmode=disable",
		"dbname=postgres_main host=/var/run/postgresql",
		"host=/var/run/postgresql dbname=legion",
		"host=db.invalid user=x password=postgresABC dbname=x",
		"host=/cloudsql/p:r:i user=sa" + at + "project.iam dbname=legion",
		"host = db.invalid  port = 5432 dbname = legion password = 'with space'",
		"",
	} {
		if err := Refusal("postgres_dsn", dsn); err != nil {
			t.Errorf("Refusal(%q) = %v, want nil", dsn, err)
		}
	}
}

// printedShapes are DSNs Postgres or pgx prints with their made-up password, each with the start
// of the refusal that names it. A value that names the postgres scheme but that pgx reads as no
// URL (upper or mixed case, a space before it, or no "//"), and any value with a key no keyword or
// parameter name has (a misspelt scheme, a SQLAlchemy or JDBC URL, a BOM before the scheme, a key
// holding a space), pgx reads up to its first "=" as one keyword, sends that name to the server as
// a parameter's, and a server that admits the client answers `invalid configuration parameter name
// "<the URL>"`. A keyword DSN with the postgres scheme in a value, `//` or not, pgx names in its
// connect error as the database or the user; one whose host holds an "@" it names as the host.
// Each DSN that names host=127.0.0.1 port=1 is pointed at the echo server by its port.
var printedShapes = map[string]struct{ dsn, want string }{
	"an upper-case scheme":     {"POSTGRES://legion:madeuphead0123" + "@" + "db.invalid:5432/legion?sslmode=require", "it names the postgres scheme"},
	"a mixed-case scheme":      {"Postgresql://legion:madeuphead0123" + "@" + "db.invalid/legion?sslmode=require", "it names the postgres scheme"},
	"a space before it":        {" postgres://legion:madeuphead0123" + "@" + "db.invalid/legion?sslmode=require", "it names the postgres scheme"},
	"a tab before it":          {"\tpostgresql://legion:madeuphead0123" + "@" + "db.invalid/legion?sslmode=require", "it names the postgres scheme"},
	"no // after it":           {"postgres:legion:madeuphead0123" + "@" + "db.invalid/legion?sslmode=require", "it names the postgres scheme"},
	"postgresql+psycopg2://":   {"postgresql+psycopg2://app:madeuphead0123" + "@" + "db.invalid/app?sslmode=disable", keyMalformedWords},
	"postgresql+asyncpg://":    {"postgresql+asyncpg://app:madeuphead0123" + "@" + "db.invalid/app?sslmode=disable", keyMalformedWords},
	"postgress://":             {"postgress://app:madeuphead0123" + "@" + "db.invalid/app?sslmode=disable", keyMalformedWords},
	"postgre://":               {"postgre://app:madeuphead0123" + "@" + "db.invalid/app?sslmode=disable", keyMalformedWords},
	"pgsql://":                 {"pgsql://app:madeuphead0123" + "@" + "db.invalid/app?sslmode=disable", keyMalformedWords},
	"pg://":                    {"pg://app:madeuphead0123" + "@" + "db.invalid/app?sslmode=disable", keyMalformedWords},
	"postgres//":               {"postgres//app:madeuphead0123" + "@" + "db.invalid/app?sslmode=disable", keyMalformedWords},
	"postgres;//":              {"postgres;//app:madeuphead0123" + "@" + "db.invalid/app?sslmode=disable", keyMalformedWords},
	"jdbc:postgresql://":       {"jdbc:postgresql://db.invalid/app?user=app&password=madeuphead0123&sslmode=disable", keyMalformedWords},
	"a BOM before postgres:":   {"\ufeffpostgres:legion:madeuphead0123" + "@" + "db.invalid/legion?sslmode=disable", keyMalformedWords},
	"a BOM before postgres://": {"\ufeffpostgres://legion:madeuphead0123" + "@" + "db.invalid/legion?sslmode=disable", keyMalformedWords},
	"a misspelt second key":    {"host=127.0.0.1 port=1 postgress://app:madeuphead0123" + "@" + "db.invalid/app?sslmode=disable", keyMalformedWords},
	"a password of two words":  {"host=127.0.0.1 port=1 user=legion password=madeuphead0123 zqtailtwo dbname=legion", keyMalformedWords},
	"a URL as dbname":          {"host=127.0.0.1 port=1 dbname=postgres://u:madeuphead0123" + "@" + "db.invalid/x", urlInKeywordWords},
	"a URL as user":            {"host=127.0.0.1 port=1 user=Postgresql://u:madeuphead0123" + "@" + "db.invalid/x dbname=x", urlInKeywordWords},
	"postgres:/ as dbname":     {"host=127.0.0.1 port=1 dbname=postgres:/app:madeuphead0123" + "@" + "db/app", urlInKeywordWords},
	"postgres: as dbname":      {"host=127.0.0.1 port=1 dbname=postgres:u:madeuphead0123" + "@" + "db.invalid", urlInKeywordWords},
	"a user and password host": {"host=app:madeuphead0123" + "@" + "127.0.0.1 port=1 dbname=app", "it is a keyword DSN whose host holds an \"@\""},
	"a hostaddr holding @":     {"hostaddr=app:madeuphead0123" + "@" + "127.0.0.1 port=1 dbname=app", "it is a keyword DSN whose host holds an \"@\""},
}

const (
	keyMalformedWords = "it is no postgres:// or postgresql:// URL, and a key of it holds a character"
	urlInKeywordWords = "it is a keyword DSN, and a value of it holds the postgres scheme"
)

// Each shape Postgres or pgx would print is refused by the setting alone, and ConnectError of it
// names "the configured Postgres", with none of the driver's text.
func TestRefusalRefusesEachShapePostgresWouldPrint(t *testing.T) {
	for name, row := range printedShapes {
		t.Run(name, func(t *testing.T) {
			err := Refusal("postgres_dsn", row.dsn)
			if err == nil || !strings.HasPrefix(err.Error(), "postgres_dsn cannot be read: "+row.want) {
				t.Fatalf("Refusal = %v, want it refused by postgres_dsn: %s", err, row.want)
			}
			driver := errors.New("server error: FATAL: invalid configuration parameter name \"" + row.dsn + "\" (SQLSTATE 42602)")
			shown := ConnectError("postgres_dsn", row.dsn, driver)
			if !strings.HasPrefix(shown.Error(), "connect to the configured Postgres: ") || !errors.Is(shown, driver) {
				t.Errorf("ConnectError = %v, want the configured Postgres named, unwrapping to the driver's error", shown)
			}
			for _, printed := range []string{err.Error(), shown.Error()} {
				if strings.Contains(printed, "madeuphead") || strings.Contains(printed, "zqtail") {
					t.Errorf("%q holds part of the password", printed)
				}
			}
		})
	}
}

// echoUser is the one role the echo server knows.
const echoUser = "legion"

// serverParameterName is the shape of a parameter name a Postgres server knows.
var serverParameterName = regexp.MustCompile(`^[a-z_][a-z0-9_.]*$`)

// echoServer is a Postgres server for these rows, on a port of this machine: it admits every
// client without a password, as a trust-auth server does, and answers each startup with the FATAL
// error such a server gives, the text it echoes: `invalid configuration parameter name "<name>"`
// for a parameter no server has, `role "<user>" does not exist` for a user other than echoUser,
// and `database "<database>" does not exist` otherwise. It declines TLS and GSS encryption.
func echoServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveEcho(conn)
		}
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	return port
}

// serveEcho answers one client's startup as echoServer does.
func serveEcho(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	backend := pgproto3.NewBackend(conn, conn)
	for {
		message, err := backend.ReceiveStartupMessage()
		if err != nil {
			return
		}
		startup, ok := message.(*pgproto3.StartupMessage)
		if !ok {
			if _, err := conn.Write([]byte("N")); err != nil {
				return
			}
			continue
		}
		backend.Send(echoOf(startup.Parameters))
		_ = backend.Flush()
		return
	}
}

// echoOf is the FATAL error echoServer answers a startup's parameters with.
func echoOf(parameters map[string]string) *pgproto3.ErrorResponse {
	fatal := func(code, message string) *pgproto3.ErrorResponse {
		return &pgproto3.ErrorResponse{Severity: "FATAL", SeverityUnlocalized: "FATAL", Code: code, Message: message}
	}
	for name := range parameters {
		if !serverParameterName.MatchString(name) {
			return fatal("42602", "invalid configuration parameter name \""+name+"\"")
		}
	}
	if user := parameters["user"]; user != echoUser {
		return fatal("28000", "role \""+user+"\" does not exist")
	}
	return fatal("3D000", "database \""+parameters["database"]+"\" does not exist")
}

// Against a server that admits the client without a password, pgx's own connect error of each
// shape it sends to the server prints the made-up password, in the parameter name, the user or
// the database the server echoes (the row's control: a shape whose echo holds none of it is
// named, since the row would test nothing for it); ConnectError of each holds none of it. The
// shapes pgx refuses to parse, or cannot connect with, are the next row's; the JDBC URL's password
// stands in the value of its misspelt key, which no server echoes, so it is refused for the key
// alone.
func TestConnectErrorOfAShapeAServerEchoesPrintsNoneOfIt(t *testing.T) {
	port := echoServer(t)
	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", port)
	t.Setenv("PGUSER", echoUser)
	t.Setenv("PGSSLMODE", "disable")
	t.Setenv("PGCONNECT_TIMEOUT", "5")
	for name, row := range printedShapes {
		if row.want == "it is a keyword DSN whose host holds an \"@\"" || name == "a password of two words" || name == "jdbc:postgresql://" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			dsn := strings.Replace(row.dsn, "port=1 ", "port="+port+" ", 1)
			_, err := pgconn.Connect(t.Context(), dsn)
			if err == nil {
				t.Fatal("pgconn.Connect to the echo server connected")
			}
			if !strings.Contains(err.Error(), "madeuphead0123") {
				t.Fatalf("pgx's own error %q holds none of the password, so this row tests nothing for it", err)
			}
			if shown := ConnectError("postgres_dsn", dsn, err); strings.Contains(shown.Error(), "madeuphead") {
				t.Errorf("ConnectError = %q, which holds part of the password", shown)
			}
		})
	}
}

// With nothing listening (port 1 on this machine refuses at once), pgx's own error of a keyword
// DSN still prints a password the DSN puts where pgx names a value: the database or user that is a
// URL, a host holding the user name and password, a password of two words, whose second word pgx
// refuses as a keyword, and a user missing the space before password= (`user=apppassword=…`, which
// pgx reads as the user, so the password it parsed is empty). Each is the row's control: one whose
// pgx error holds none of the password is named. ConnectError of each holds none of it: the first
// four are refused, and the last is kept, with every password= masked whatever pgx read as the
// password (redactPassword).
func TestConnectErrorOfAShapePgxPrintsWithNoServerPrintsNoneOfIt(t *testing.T) {
	t.Setenv("PGCONNECT_TIMEOUT", "5")
	shapes := map[string]string{
		"a missing space before password=": "host=127.0.0.1 port=1 user=apppassword=madeuphead0123 dbname=app",
	}
	for _, name := range []string{"a URL as dbname", "a URL as user", "postgres:/ as dbname", "postgres: as dbname", "a user and password host", "a password of two words"} {
		shapes[name] = printedShapes[name].dsn
	}
	for name, dsn := range shapes {
		t.Run(name, func(t *testing.T) {
			_, err := pgconn.Connect(t.Context(), dsn)
			if err == nil {
				t.Fatal("pgconn.Connect to port 1 connected")
			}
			if !strings.Contains(err.Error(), "madeuphead0123") && !strings.Contains(err.Error(), "zqtailtwo") {
				t.Fatalf("pgx's own error %q holds none of the password, so this row tests nothing for it", err)
			}
			shown := ConnectError("postgres_dsn", dsn, err)
			if strings.Contains(shown.Error(), "madeuphead") || strings.Contains(shown.Error(), "zqtail") {
				t.Errorf("ConnectError = %q, which holds part of the password", shown)
			}
		})
	}
	if err := Refusal("postgres_dsn", shapes["a missing space before password="]); err != nil {
		t.Errorf("Refusal of a user missing a space = %v, want it kept: its key and values are a keyword DSN's", err)
	}
}

// An "@" after the user part that is no password's is refused too, by design: the rule cannot
// tell a database name or a query value holding an "@" from a password's tail. Each loads written
// with %40, which pgx decodes in the database name and in a query value.
func TestRefusalRefusesAnAtInADatabaseOrAQueryValueAndPercent40LoadsIt(t *testing.T) {
	for refused, decoded := range map[string]struct{ dsn, user, database string }{
		"postgres://db.invalid:5432?sslrootcert=/etc/ssl/ca.pem&user=sa@project.iam": {"postgres://db.invalid:5432?user=sa%40project.iam", "sa@project.iam", ""},
		"postgres://?host=/tmp&user=sa@project.iam":                                  {"postgres://?host=/tmp&user=sa%40project.iam", "sa@project.iam", ""},
		"postgres:///legion?host=/cloudsql/p:r:i&user=sa@project.iam":                {"postgres:///legion?host=/cloudsql/p:r:i&user=sa%40project.iam", "sa@project.iam", "legion"},
		"postgres://legion@db.invalid:5432/legion?user=sa@project.iam":               {"postgres://legion@db.invalid:5432/legion?user=sa%40project.iam", "sa@project.iam", "legion"},
		"postgres://h/my@db": {"postgres://h/my%40db", "", "my@db"},
	} {
		if err := Refusal("postgres_dsn", refused); err == nil {
			t.Errorf("Refusal(%q) = nil, want it refused", refused)
		}
		if err := Refusal("postgres_dsn", decoded.dsn); err != nil {
			t.Errorf("Refusal(%q) = %v, want the %%40 form kept", decoded.dsn, err)
		}
		config, err := pgconn.ParseConfig(decoded.dsn)
		if err != nil {
			t.Fatalf("ParseConfig(%q) = %v", decoded.dsn, err)
		}
		if decoded.user != "" && config.User != decoded.user {
			t.Errorf("ParseConfig(%q).User = %q, want %q", decoded.dsn, config.User, decoded.user)
		}
		if decoded.database != "" && config.Database != decoded.database {
			t.Errorf("ParseConfig(%q).Database = %q, want %q", decoded.dsn, config.Database, decoded.database)
		}
	}
}

// pgURLCorpus is the libpq split's corpus, which the Dispatch e2e (TypeScript), the shell and the
// search-measure (Python) splits share: each case checked against libpq's own PQconninfoParse.
type pgURLCorpus struct {
	Accepted []struct {
		URL string `json:"url"`
	} `json:"accepted"`
	Refused []struct {
		URL   string `json:"url"`
		Fault string `json:"fault"`
	} `json:"refused"`
}

// spillFaults are the corpus's faults of a URL part of whose credential stands in the address.
var spillFaults = map[string]bool{
	"a host holds a raw @":                    true,
	"an @ sits between the first / and the ?": true,
}

// queryValueAt names the corpus's accepted URLs holding an unencoded "@" in a query value, which
// Refusal refuses by design (TestRefusalRefusesAnAtInADatabaseOrAQueryValueAndPercent40LoadsIt).
// The corpus holds none today.
var queryValueAt = map[string]bool{}

// Refusal holds to the corpus: every URL it marks spilled is refused, and every URL it accepts is
// kept, but one holding an unencoded "@" in a query value, which queryValueAt names.
func TestRefusalHoldsToTheSharedPgURLCorpus(t *testing.T) {
	body, err := os.ReadFile("../scripts/fixtures/pg-url.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus pgURLCorpus
	if err := json.Unmarshal(body, &corpus); err != nil {
		t.Fatal(err)
	}
	spills := 0
	for _, refused := range corpus.Refused {
		if !spillFaults[refused.Fault] {
			continue
		}
		spills++
		if Refusal("postgres_dsn", refused.URL) == nil {
			t.Errorf("Refusal(%q) = nil, want the corpus's spill fault %q refused", refused.URL, refused.Fault)
		}
	}
	if spills != len(spillFaults) {
		t.Fatalf("the corpus holds %d spill faults, want %d: it no longer names them as this row reads", spills, len(spillFaults))
	}
	if len(corpus.Accepted) == 0 {
		t.Fatal("the corpus accepts no URL")
	}
	for _, accepted := range corpus.Accepted {
		err := Refusal("postgres_dsn", accepted.URL)
		switch {
		case queryValueAt[accepted.URL] && err == nil:
			t.Errorf("Refusal(%q) = nil, want the unencoded @ in its query value refused", accepted.URL)
		case !queryValueAt[accepted.URL] && err != nil:
			t.Errorf("Refusal(%q) = %v, want the corpus's accepted URL kept", accepted.URL, err)
		}
	}
}

// ConnectError of a spilled DSN names "the configured Postgres" and no detail of the driver's,
// whose text names the host and the database, and still unwraps to the driver's error.
func TestConnectErrorOfASpilledDSNPrintsNoPartOfThePassword(t *testing.T) {
	for shape, dsn := range spilled {
		t.Run(shape, func(t *testing.T) {
			driver := errors.New("failed to connect to `user=legion database=zqtailtwo@db.invalid`: zqtailone:5432 (48213579): lookup madeuphead0123: dial error")
			err := ConnectError("postgres_dsn", dsn, driver)
			if !strings.HasPrefix(err.Error(), "connect to the configured Postgres: ") {
				t.Errorf("ConnectError = %v, want the configured Postgres named", err)
			}
			for _, part := range madeUp {
				if strings.Contains(err.Error(), part) {
					t.Errorf("ConnectError = %v, which holds %q of the password", err, part)
				}
			}
			if !errors.Is(err, driver) {
				t.Errorf("ConnectError = %v, which does not unwrap to the driver's error", err)
			}
		})
	}
}

// A keyword DSN pgx cannot parse is named by its setting alone, with none of pgx's text, which
// quotes the DSN: pgx masks a password only written `password=<value>`, so spaces around the "=",
// or a space after it, print. The error still unwraps to the *pgconn.ParseConfigError. A password
// of several words is refused before pgx reads it (printedShapes): its second word is a key no
// keyword has.
func TestConnectErrorOfAKeywordDSNPgxCannotParsePrintsNothingOfIt(t *testing.T) {
	for name, dsn := range map[string]string{
		"spaces around the =": "host=db.invalid user=legion password = zqtailsecret0123 dbname=legion sslmode=requir",
		"a space after the =": "host=db.invalid user=legion password= zqtailsecret0123 dbname=legion sslmode=requir",
	} {
		t.Run(name, func(t *testing.T) {
			_, parseErr := pgconn.ParseConfig(dsn)
			if parseErr == nil {
				t.Fatalf("ParseConfig(%q) = nil, want pgx's refusal", dsn)
			}
			err := ConnectError("BROKER_DATABASE_URL", dsn, parseErr)
			if want := "connect to Postgres: BROKER_DATABASE_URL cannot be parsed as a Postgres DSN; the driver's error is not printed, since it quotes the DSN"; err.Error() != want {
				t.Errorf("ConnectError = %q, want %q", err, want)
			}
			var unparsed *pgconn.ParseConfigError
			if !errors.As(err, &unparsed) {
				t.Errorf("ConnectError = %v, which does not unwrap to the *pgconn.ParseConfigError", err)
			}
		})
	}
}

// The driver's own text is not ours to trust: whatever it says on a future version, the password
// the DSN carries does not reach the error, and the address it points at does.
func TestConnectErrorRedactsThePasswordOutOfWhatItWraps(t *testing.T) {
	dsn := "postgres://legion:" + "hunter2" + "@db.internal:5432/legion"
	for _, detail := range []string{
		"failed to connect to `host=db.internal password=hunter2`",
		"cannot parse postgres://legion:hunter2" + "@db.internal:5432/legion",
	} {
		err := ConnectError("postgres_dsn", dsn, errors.New(detail))
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("error leaks the password its driver printed: %v", err)
		}
		if !strings.Contains(err.Error(), "db.internal:5432") {
			t.Errorf("error does not name the host:port it failed to reach: %v", err)
		}
	}
}

// The ordinary devbox DSN's user, password and database name are all `legion` (urlDSN("legion")).
// Redaction that blanked every occurrence would hide the two fields an operator reads to diagnose
// the refusal.
func TestConnectErrorKeepsTheFieldsThatMerelyMatchThePassword(t *testing.T) {
	err := ConnectError("postgres_dsn", urlDSN("legion"), errors.New("failed to connect to `user=legion database=legion`: connection refused"))
	if !strings.Contains(err.Error(), "user=legion database=legion") {
		t.Errorf("error hides the user and database the daemon was connecting as: %v", err)
	}
}
