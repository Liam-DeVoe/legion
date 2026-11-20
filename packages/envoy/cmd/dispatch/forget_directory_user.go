package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/auth"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// forgetDirectoryUserUsage is what `envoy-dispatch forget-directory-user` prints for arguments it
// cannot take.
const forgetDirectoryUserUsage = "forget-directory-user: takes one address; run `envoy-dispatch forget-directory-user <address>` " +
	"once you know the Workspace account now at the address is the person whose people row it is"

// forgetDirectoryUser runs `envoy-dispatch forget-directory-user <address>`, the deployment
// operator's step under IAP identity for an address the Workspace deleted and created again, or
// gave to a new account: the address's people row stores the Directory user id of its first Active
// answer, and an answer for the address that carries another id is Held, so the row stays held. The
// command forgets the stored id (store.PgPeopleStore.ForgetDirectoryUser) for the address as
// model.CanonicalLogin spells it, so the next Directory check compares the answer by address, as
// for a row never confirmed, and an Active answer then stores the new id and releases the row. It
// changes nothing else and releases no hold itself: the membership pass's next check of the row
// does, within the hour. It exits 0 once it forgot a stored id, 1 when no row holds the address or
// the row stores no id, and when the database cannot be opened or written, and 2 for anything but
// one non-empty address.
func forgetDirectoryUser(ctx context.Context, args []string, databaseURL string, stdout, stderr io.Writer) int {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintln(stderr, forgetDirectoryUserUsage)
		return 2
	}
	database, ok := openMigrated(ctx, "forget-directory-user", databaseURL, stderr)
	if !ok {
		return 1
	}
	defer database.Pool.Close()
	// The people store seals refresh tokens under a signing key, and the forget reads and writes
	// only directory_user_id: a key of this run alone, which seals and opens nothing, so the command
	// needs no DISPATCH_SIGNING_KEY.
	key, err := auth.NewSigningKey()
	if err != nil {
		fmt.Fprintf(stderr, "forget-directory-user: %v\n", err)
		return 1
	}
	email := model.CanonicalLogin(args[0])
	forgot, err := store.NewPgPeopleStore(database.Pool, auth.SingleSigningKey(key), nil).ForgetDirectoryUser(ctx, email)
	if err != nil {
		fmt.Fprintf(stderr, "forget-directory-user: %v\n", err)
		return 1
	}
	if !forgot {
		fmt.Fprintf(stderr, "forget-directory-user: %s stores no Directory user id; nothing changed\n", email)
		return 1
	}
	fmt.Fprintf(stdout, "forget-directory-user: %s no longer stores a Directory user id; its next Directory check compares the answer by address\n", email)
	return 0
}
