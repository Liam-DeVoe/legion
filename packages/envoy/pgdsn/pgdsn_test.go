package pgdsn

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
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
// hosts, a keyword DSN, and a scheme pgx reads as no URL (POSTGRES://), which pgx refuses itself.
func TestRefusalKeepsADSNWithNoAtPastItsUserPart(t *testing.T) {
	for _, dsn := range []string{
		urlDSN("legion"),
		urlDSN("pa?ss"),
		urlDSN("pa#ss"),
		urlDSN("p%40ss%2Fword"),
		"postgres://legion" + "@[fe80::1%25eth0]:5432/legion",
		"postgres://[::1]:5432,db.invalid/legion?sslmode=disable",
		"postgres:///legion?host=/var/run/postgresql",
		"postgres:///legion?host=/cloudsql/p:r:i&user=sa%40project.iam",
		"postgres://h/my%40db",
		"host=db.invalid user=legion password=p@ss/word dbname=legion",
		"POSTGRES://legion:" + "x" + "@db.invalid/legion",
	} {
		if err := Refusal("postgres_dsn", dsn); err != nil {
			t.Errorf("Refusal(%q) = %v, want nil", dsn, err)
		}
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
// a space after it, or a password of several words print. The error still unwraps to the
// *pgconn.ParseConfigError.
func TestConnectErrorOfAKeywordDSNPgxCannotParsePrintsNothingOfIt(t *testing.T) {
	for name, dsn := range map[string]string{
		"spaces around the =":         "host=db.invalid user=legion password = zqtailsecret0123 dbname=legion sslmode=requir",
		"a space after the =":         "host=db.invalid user=legion password= zqtailsecret0123 dbname=legion sslmode=requir",
		"a password of several words": "host=db.invalid user=legion password=zqtailone zqtailtwo zqtailthree dbname=legion",
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
