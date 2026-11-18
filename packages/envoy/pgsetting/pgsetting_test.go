package pgsetting_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/redact"
	"github.com/sjawhar/envoy/pgsetting"
)

// Each misread form of a Postgres connection string is refused naming its cause, one row per
// cause, and each honest form whose other @ is in its query or its database name is read; a value
// pgx cannot parse is refused by its length too, and a failed connect names no user and no
// database. No refusal holds a part of the value. The user, the passwords and the token are made
// for this test.
func TestEachOpenerReadsItsConnectionStringWithNoPartOfItsUserPartPrinted(t *testing.T) {
	const (
		user   = "made-up-user-71ce"
		first  = "not-a-secret-4e02"
		second = "not-a-secret-9b3a"
		token  = "not-a-secret-tok6d8"
	)
	for _, tc := range []struct {
		name, raw string
		why       redact.Misreading
	}{
		{"a / in the password", "postgres://" + user + ":" + first + "/" + second + "@127.0.0.1:1/x", pgsetting.AtAfterPath},
		{"a token and a / as the user part", "postgres://" + token + "/" + first + "@127.0.0.1:1/x", pgsetting.AtAfterPath},
		{"an @ in the password", "postgresql://" + user + ":" + first + "@" + second + "@127.0.0.1:1/x", pgsetting.AtsBeforePath},
		{"a # in the password", "postgres://" + user + ":" + first + "#" + second + "@127.0.0.1:1/x", pgsetting.EndBeforeAt},
		{"a mistyped option", "postgres://" + user + ":" + first + "@127.0.0.1:1/x?sslmode=bogus", pgsetting.NotParsed},
		{"an @ in the query", "postgres://" + user + ":" + first + "@127.0.0.1:1/x?application_name=a@b", ""},
		{"an @ in the database name", "postgres://" + user + ":" + first + "@127.0.0.1:1/dis@patch", ""},
		{"the key=value form", "host=127.0.0.1 port=1 user=" + user + " password=" + first, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := ""
			if tc.why != "" {
				want = fmt.Sprintf("DATABASE_URL, a value of %d characters, %s", len(tc.raw), tc.why)
			}
			_, connErr := pgsetting.ParseConfig("DATABASE_URL", tc.raw)
			_, poolErr := pgsetting.ParsePoolConfig("DATABASE_URL", tc.raw)
			for name, err := range map[string]error{"ParseConfig": connErr, "ParsePoolConfig": poolErr} {
				got := ""
				if err != nil {
					got = err.Error()
				}
				if got != want {
					t.Errorf("%s: %q, want %q", name, got, want)
				}
				for _, part := range []string{user, first, second, token} {
					if strings.Contains(got, part) {
						t.Errorf("%s: the refusal holds a part of the value: %q", name, got)
					}
				}
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	config, err := pgsetting.ParseConfig("DATABASE_URL", "postgres://"+token+"@127.0.0.1:1/x?connect_timeout=3")
	if err != nil {
		t.Fatalf("ParseConfig of a token as the user: %v", err)
	}
	_, err = pgx.ConnectConfig(ctx, config)
	if err == nil || !strings.Contains(err.Error(), token) {
		t.Fatalf("UNKNOWN: pgx's own connect error does not name the user, so this row saw nothing: %v", err)
	}
	failed := pgsetting.ConnectFailure(err)
	if !strings.Contains(failed.Error(), "127.0.0.1") || strings.Contains(failed.Error(), token) {
		t.Errorf("ConnectFailure: %q, want what failed at 127.0.0.1 and no user", failed)
	}
}
