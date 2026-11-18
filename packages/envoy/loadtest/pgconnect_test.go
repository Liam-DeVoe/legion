package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/pgsetting"
)

// A -pg value is read as every opener of a Postgres connection string reads it (pgsetting): one
// whose user part pgx may read apart is refused by its length and cause before any connect, by the
// connection and the pool alike, and a connect that fails names no user. The control is a token as
// the whole user part on a port nothing listens on, whose failure names the host. The user, the
// password and the token are made for this test.
func TestAPGValueIsReadWithNoPartOfItsUserPartPrinted(t *testing.T) {
	const (
		user     = "made-up-user-3f61"
		password = "not-a-secret-8c0b"
		token    = "not-a-secret-tok5e2"
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	misread := "postgres://" + user + ":" + password + "@" + token + "@127.0.0.1:1/dispatch"
	want := fmt.Sprintf("-pg, a value of %d characters, %s", len(misread), pgsetting.AtsBeforePath)
	if _, err := connectPG(ctx, misread); err == nil || err.Error() != want {
		t.Errorf("connectPG: %v, want %q", err, want)
	}
	if _, err := newPGPool(ctx, misread); err == nil || err.Error() != want {
		t.Errorf("newPGPool: %v, want %q", err, want)
	}
	_, err := connectPG(ctx, "postgres://"+token+"@127.0.0.1:1/dispatch?connect_timeout=3")
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("a connect that fails: %v, want it to name the host", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("a connect that fails names the user: %q", err)
	}
}
