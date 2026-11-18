package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/pgsetting"
)

// BROKER_DATABASE_URL is read as every opener of a Postgres connection string reads it
// (pgsetting): a value whose user part pgx may read apart is refused by its length and cause, a
// value pgx cannot parse is too, and a ping that cannot connect names no user. The control is a
// value with a token as its whole user part on a port nothing listens on, whose ping fails naming
// the host. The user, the password and the token are made for this test.
func TestOpenNamesNoPartOfTheBrokerDatabaseURLsUserPart(t *testing.T) {
	const (
		user     = "made-up-user-8a4f"
		password = "not-a-secret-1c7e"
		token    = "not-a-secret-tok2d9"
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for raw, why := range map[string]string{
		"postgres://" + user + ":" + password + "/x@127.0.0.1:1/broker":             string(pgsetting.AtAfterPath),
		"postgres://" + user + ":" + password + "@127.0.0.1:1/broker?sslmode=bogus": string(pgsetting.NotParsed),
	} {
		_, err := Open(ctx, raw)
		want := fmt.Sprintf("BROKER_DATABASE_URL, a value of %d characters, %s", len(raw), why)
		if err == nil || err.Error() != want {
			t.Errorf("%d characters: %v, want %q", len(raw), err, want)
		}
	}
	_, err := Open(ctx, "postgres://"+token+"@127.0.0.1:1/broker?connect_timeout=3")
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("a ping that cannot connect: %v, want it to name the host", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("a ping that cannot connect names the user: %q", err)
	}
}
