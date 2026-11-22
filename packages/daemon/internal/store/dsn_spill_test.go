package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// spilledDSNs are postgres URLs part of whose made-up password stands where pgx reads the host
// list, the port or the database: a password holding an unencoded "@" (its tail in the host list),
// and one holding an unencoded "/" whose head is digits (its head read as the port and its tail as
// the database, or as a query key). Each value is the part of the password the DSN spills.
var spilledDSNs = map[string]struct{ dsn, spilled string }{
	"a password holding an @":                 {"postgres://legion:madeuphead0123" + "@madeuptail0123@db.invalid:5432/legion", "madeuptail"},
	"a password holding a / after digits":     {"postgres://legion:48213579/madeuptail0123" + "@db.invalid:5432/legion", "48213579"},
	"a password holding a / and a ? after it": {"postgresql://legion:48213579/x?madeuptail0123" + "@db.invalid/legion", "48213579"},
}

// DSNRefusal refuses each spilled DSN by its key alone, and keeps a DSN whose "@" is only the
// userinfo's, one whose query value holds an "@" (a Cloud SQL IAM user), one whose password holds a
// "?" or a "#" (which pgx reads as the password), and a keyword DSN.
func TestDSNRefusalNamesTheKeyOfADSNWhosePasswordSpills(t *testing.T) {
	for name, tc := range spilledDSNs {
		t.Run(name, func(t *testing.T) {
			err := DSNRefusal("postgres_dsn", tc.dsn)
			if err == nil || !strings.HasPrefix(err.Error(), "postgres_dsn cannot be read: part of its password stands where Postgres reads") {
				t.Fatalf("DSNRefusal = %v, want a refusal naming postgres_dsn", err)
			}
			for _, part := range []string{tc.spilled, "madeuphead", "madeuptail", "db.invalid"} {
				if strings.Contains(err.Error(), part) {
					t.Errorf("DSNRefusal = %v, which holds %q", err, part)
				}
			}
		})
	}
	// Built from parts, so no source line holds a URL with userinfo and its "@".
	at := "@"
	for _, dsn := range []string{
		"postgres://legion:legion" + at + "127.0.0.1:5432/legion",
		"postgres://legion@db:5432/legion?user=sa@project.iam",
		"postgres://legion:pa?ss" + at + "db:5432/legion",
		"postgres://legion:pa#ss" + at + "db:5432/legion",
		"postgres:///legion?host=/cloudsql/p:r:i&user=sa@project.iam",
		"host=db user=legion password=p@ss/word dbname=legion",
	} {
		if err := DSNRefusal("postgres_dsn", dsn); err != nil {
			t.Errorf("DSNRefusal(%q) = %v, want nil", dsn, err)
		}
	}
}

// A connect error of a spilled DSN, which the loader refuses before a connection is tried, names
// "the configured Postgres" and no detail of the driver's, whose text names the host and the
// database: Open's own and ConnectError's of a made-up driver error that quotes them.
func TestAConnectErrorOfASpilledDSNPrintsNoPartOfThePassword(t *testing.T) {
	for name, tc := range spilledDSNs {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, openErr := Open(ctx, tc.dsn)
			driver := errors.New("failed to connect to `user=legion database=" + tc.spilled + "`: " + tc.spilled + ":5432 (" + tc.spilled + "): dial error")
			for what, err := range map[string]error{"Open": openErr, "ConnectError": ConnectError(tc.dsn, driver)} {
				if err == nil {
					t.Fatalf("%s = nil, want the failed connection", what)
				}
				if !strings.HasPrefix(err.Error(), "connect to the configured Postgres: ") {
					t.Errorf("%s = %v, want the configured Postgres named", what, err)
				}
				for _, part := range []string{tc.spilled, "madeuphead", "madeuptail"} {
					if strings.Contains(err.Error(), part) {
						t.Errorf("%s = %v, which holds %q of the password", what, err, part)
					}
				}
			}
		})
	}
}
