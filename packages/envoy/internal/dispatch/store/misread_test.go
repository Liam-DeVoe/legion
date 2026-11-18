package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Every DATABASE_URL a process opens goes through Open, the server's boot and each subcommand's
// alike, so Open refuses one whose user part pgx may read apart from the rest of it, named by its
// length and before any connect: pgx would take a piece of that part for a host or a database,
// which its connect error prints. The control is the same value with that part percent-encoded,
// which Open takes to pgx, whose connect to a port nothing listens on then fails naming no part of
// the password (pgx names the user, which is the role). The user, the passwords and the token are
// made for this test.
func TestOpenRefusesAMisreadURLByItsLength(t *testing.T) {
	const (
		user   = "made-up-user-2c5e"
		first  = "not-a-secret-7d10"
		second = "not-a-secret-8e21"
		token  = "not-a-secret-tok4f3"
	)
	for _, tc := range []struct{ name, value, encoded string }{
		{"a / in the password", "postgres://" + user + ":" + first + "/" + second + "@127.0.0.1:1/x?connect_timeout=3", "postgres://" + user + ":" + first + "%2F" + second + "@127.0.0.1:1/x?connect_timeout=3"},
		{"a token and a / as the user part", "postgres://" + token + "/" + first + "@127.0.0.1:1/x?connect_timeout=3", "postgres://" + token + "%2F" + first + "@127.0.0.1:1/x?connect_timeout=3"},
		{"an @ in the password", "postgresql://" + user + ":" + first + "@" + second + "@127.0.0.1:1/x?connect_timeout=3", "postgresql://" + user + ":" + first + "%40" + second + "@127.0.0.1:1/x?connect_timeout=3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Open(context.Background(), tc.value)
			want := fmt.Sprintf("DATABASE_URL, a value of %d characters,", len(tc.value))
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want a refusal that names %q", err, want)
			}
			for _, part := range []string{user, first, second, token} {
				if strings.Contains(err.Error(), part) {
					t.Errorf("the refusal holds a part of the user part: %q", err)
				}
			}
			_, err = Open(context.Background(), tc.encoded)
			if err == nil || strings.Contains(err.Error(), want) {
				t.Fatalf("the control with the part percent-encoded: err = %v, want pgx's connect failure", err)
			}
			if strings.Contains(err.Error(), second) {
				t.Errorf("the control: pgx's error holds the password: %q", err)
			}
		})
	}
}

// A database Open cannot reach fails naming what failed at the host it tried, and no part of the
// URL's user part: pgx names the user in front of its connect error, and a token can stand where
// the user does. The control is a URL with no user part, whose error names the host the same way.
// The user, the password and the token are made for this test.
func TestOpenNamesNoPartOfTheUserPartWhenItCannotConnect(t *testing.T) {
	const (
		user     = "made-up-user-5a3d"
		password = "not-a-secret-6b02"
		token    = "not-a-secret-tok7c1"
	)
	for _, userPart := range []string{user + ":" + password + "@", token + "@", ""} {
		_, err := Open(context.Background(), "postgres://"+userPart+"127.0.0.1:1/x?connect_timeout=3")
		if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
			t.Fatalf("user part %q: err = %v, want the failed connect naming the host", userPart, err)
		}
		for _, part := range []string{user, password, token} {
			if strings.Contains(err.Error(), part) {
				t.Errorf("user part %q: the error holds a part of the user part: %q", userPart, err)
			}
		}
	}
}
