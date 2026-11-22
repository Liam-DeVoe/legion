package main

import (
	"bytes"
	"strings"
	"testing"
)

// A --database-url whose password leaves part of itself past its user part (an "@" in it) is
// refused by the flag's own name, before the store, whose refusal names the server's DATABASE_URL,
// and before anything connects; the refusal holds no part of the password. The URL is built from
// parts, so no source line holds a URL with userinfo.
func TestRunRefusesASpillingDatabaseURLByTheFlagsName(t *testing.T) {
	at := "@"
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"--database-url", "postgres://seed:madeuphead" + at + "zqtail0123" + at + "db.invalid:5432/dispatch"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run = %d, want 2; stderr %q", code, stderr.String())
	}
	if !strings.HasPrefix(stderr.String(), "dispatch-seed: --database-url cannot be read: it holds an \"@\" after its user name and password") {
		t.Errorf("stderr = %q, want the refusal naming --database-url", stderr.String())
	}
	for _, part := range []string{"madeuphead", "zqtail0123"} {
		if strings.Contains(stderr.String(), part) {
			t.Errorf("stderr = %q, which holds %q of the password", stderr.String(), part)
		}
	}
}

// A --database-url pgx cannot parse (a keyword DSN with spaces around password's "=") is named by
// the flag, the one the operator set, and not by DATABASE_URL, which the store names, and with
// none of pgx's text, which quotes the password.
func TestRunNamesADatabaseURLPgxCannotParseByTheFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"--database-url", "host=db.invalid user=seed password = zqsecret0123 dbname=dispatch sslmode=requir"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run = %d, want 2; stderr %q", code, stderr.String())
	}
	if want := "dispatch-seed: connect to Postgres: --database-url cannot be parsed as a Postgres DSN"; !strings.HasPrefix(stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
	if strings.Contains(stderr.String(), "DATABASE_URL") || strings.Contains(stderr.String(), "zqsecret") {
		t.Errorf("stderr = %q, which names DATABASE_URL or holds part of the password", stderr.String())
	}
}
