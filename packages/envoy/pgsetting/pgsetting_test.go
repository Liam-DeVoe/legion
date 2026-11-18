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
// cause, an @ in the database name among them, and each honest form whose other @ is in its query,
// or in its database name written %40, is read; a value pgx cannot parse is refused by its length
// too, and a failed connect names no user and no database. No refusal holds a part of the value.
// The user, the passwords and the token are made for this test.
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
		{"a # in the password, which pgx keeps in the user part", "postgres://" + user + ":" + first + "#" + second + "@127.0.0.1:1/x", ""},
		{"a ? in the password, which pgx keeps in the user part", "postgres://" + user + ":" + first + "?" + second + "@127.0.0.1:1/x", ""},
		{"a token with a ? as the user part", "postgres://" + token + "?" + second + "@127.0.0.1:1/x", ""},
		{"a mistyped option", "postgres://" + user + ":" + first + "@127.0.0.1:1/x?sslmode=bogus", pgsetting.NotParsed},
		{"an @ in the query", "postgres://" + user + ":" + first + "@127.0.0.1:1/x?application_name=a@b", ""},
		{"an @ in the database name", "postgres://" + user + ":" + first + "@127.0.0.1:1/dis@patch", pgsetting.AtInDatabaseName},
		{"an @ and a / in the password, read as a host and a database name", "postgres://" + user + ":" + first + "@" + second + "/x@127.0.0.1:1/x", pgsetting.AtInDatabaseName},
		{"an @ in the database name written %40", "postgres://" + user + ":" + first + "@127.0.0.1:1/dis%40patch", ""},
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

// pgx reads a ?, # or \ before the @ that ends the user part as a part of it, so the openers admit
// such a value; net/url ends the host there, so URLParserReadsApart finds each, and nothing else.
// What a reader prints of such a value names no part of its user part: a failed connect
// (ConnectFailure, pgx's own error naming the token as the user, which is the control), and the
// refusal of a reader that parses it with net/url (redact.Refusal with EndBeforeAt). The user, the
// passwords and the token are made for this test.
func TestAUserPartWithARawQuestionMarkOrHashIsReadByPgxAndNamedByNoReader(t *testing.T) {
	const (
		user   = "made-up-user-3a8f"
		first  = "not-a-secret-2b7c"
		second = "not-a-secret-1c6d"
		token  = "not-a-secret-tok0d5"
	)
	apart := []string{
		"postgres://" + user + ":" + first + "?" + second + "@127.0.0.1:1/x?connect_timeout=3",
		"postgres://" + user + ":" + first + "#" + second + "@127.0.0.1:1/x?connect_timeout=3",
		"postgres://" + user + ":" + first + `\` + second + "@127.0.0.1:1/x?connect_timeout=3",
		"postgres://" + token + "?" + second + "@127.0.0.1:1/x?connect_timeout=3",
		"postgres://" + token + "#" + second + "@127.0.0.1:1/x?connect_timeout=3",
	}
	alike := []string{
		"postgres://" + user + ":" + first + "@127.0.0.1:1/x?application_name=a@b",
		"postgres://" + user + ":" + first + "@127.0.0.1:1/x?sslmode=disable#y",
		"postgres://127.0.0.1:1/x?user=" + user,
		"host=127.0.0.1 user=" + user,
	}
	for _, raw := range alike {
		if pgsetting.URLParserReadsApart(raw) {
			t.Errorf("%d characters: URLParserReadsApart, want a value pgx and net/url read alike", len(raw))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, raw := range apart {
		if !pgsetting.URLParserReadsApart(raw) {
			t.Errorf("%d characters: not URLParserReadsApart, want it found", len(raw))
		}
		config, err := pgsetting.ParseConfig("DATABASE_URL", raw)
		if err != nil {
			t.Errorf("%d characters: ParseConfig %v, want pgx's reading", len(raw), err)
			continue
		}
		if config.Host != "127.0.0.1" {
			t.Errorf("%d characters: pgx read the host as a piece of the user part", len(raw))
		}
		_, err = pgx.ConnectConfig(ctx, config)
		texts := []string{pgsetting.ConnectFailure(err).Error(), redact.Refusal("DATABASE_URL", raw, pgsetting.EndBeforeAt).Error()}
		for _, text := range texts {
			for _, part := range []string{user, first, second, token} {
				if strings.Contains(text, part) {
					t.Errorf("%d characters: %q holds a part of the user part", len(raw), text)
				}
			}
		}
	}
}
