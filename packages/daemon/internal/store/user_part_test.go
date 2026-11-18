package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/pgsetting"
)

// LEGION_POSTGRES_DSN is read as every opener of a Postgres connection string reads it
// (pgsetting): a DSN whose user part pgx and net/url read apart is refused by its length and cause
// before any connect, since the address ConnectError names would be a piece of that user part; one
// pgx cannot parse is refused by its length too, since pgx's parse error quotes the user; and a
// connect that fails names the address and no user, which can be a token written where the user
// stands. The control is the same DSN with its / percent-encoded, which reaches the connect and is
// refused naming the address. The user, the password and the token are made for this test.
func TestOpenRefusesAMisreadDSNByItsLength(t *testing.T) {
	const (
		user     = "made-up-user-0e93"
		password = "not-a-secret-5a7c"
		token    = "not-a-secret-tok1b4"
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for raw, why := range map[string]any{
		"postgres://" + user + ":" + password + "/x@127.0.0.1:1/legion":               pgsetting.AtAfterPath,
		"postgres://" + token + "@127.0.0.1:1/legion?connect_timeout=3&sslmode=bogus": pgsetting.NotParsed,
	} {
		want := fmt.Sprintf("LEGION_POSTGRES_DSN, a value of %d characters, %s", len(raw), why)
		if _, err := Open(ctx, raw); err == nil || err.Error() != want {
			t.Errorf("%d characters: %v, want %q", len(raw), err, want)
		}
	}
	encoded := "postgres://" + user + ":" + password + "%2Fx@127.0.0.1:1/legion?connect_timeout=3"
	_, err := Open(ctx, encoded)
	if err == nil || !strings.Contains(err.Error(), "connect to Postgres at 127.0.0.1:1") {
		t.Fatalf("the control: %v, want the connect's refusal naming the address", err)
	}
	_, err = Open(ctx, "postgres://"+token+"@127.0.0.1:1/legion?connect_timeout=3")
	if err == nil || !strings.Contains(err.Error(), "connect to Postgres at 127.0.0.1:1") || strings.Contains(err.Error(), token) {
		t.Errorf("a token as the user: %v, want the connect's refusal naming the address and not the token", err)
	}
}
