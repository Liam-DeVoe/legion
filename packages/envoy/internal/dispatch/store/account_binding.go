package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// AccountBindingProtocol is the account binding this build serves under, which it registers in
// server_processes (account_binding; processes.Register) and names in the dashboard_builds row it
// publishes (dashboard.Publish), migration 20261120222130_account_binding_fleet: 1, a build that
// holds every credential of an address to the Google account Dispatch records for it (people.sub
// and sub_recorded_at: api.tokenAccountReplaced, api.brokerAccountReplaced, api.accountHolder),
// records the account a mint's credential is bound to (agent_tokens.subject), and revokes the
// address's tokens and owes the broker a revocation when another account signs in
// (PgPeopleStore.SignInSubject). A row at 0, the column's default, is written by a build without
// it, which checks no account, records none and revokes nothing at a replacement.
const AccountBindingProtocol = 1

// AccountBindingGateKey is the second key of the account binding's gate: OwnerLockGateNamespace
// ("OWNG") and 1, a transaction advisory lock the migration's functions take and nothing in Go
// does. A build without the binding takes it exclusively as it arrives, at its dashboard publish
// (public.account_binding_legacy_arrival) and its registration (for at most 2 seconds,
// public.account_binding_legacy_registration); a transaction of this build that reads the fleet as
// having no such build in it tries it shared and holds it to its end (AccountBindingHolds). The
// owner lock gate is key 0 of the same namespace, so the two never wait on each other. Exported,
// and spelled here as the migration spells it, so a test reads the gate out of pg_locks.
const AccountBindingGateKey int32 = 1

// AccountBindingHoldsSQL is the SQL expression of whether the account binding holds over the
// fleet: no build without it is in the fleet (public.account_binding_fleet, migration
// 20261120222130_account_binding_fleet), over legacyWindow and markWindow, the expressions of the
// statement that are AccountBindingWindows' two values. It is the same fence the owner lock reads
// (OwnerLockLegacyWindow and OwnerLockMarkWindow): such a build is in the fleet while a
// server_processes row at account_binding 0 heartbeated within the one window, or while the mark
// its last arrival stamped (its dashboard publish, which its boot makes before it writes anything
// else, or its registration) is younger than the other. When it finds none it tries the binding's
// gate shared (AccountBindingGateKey) and reads again, and a try that fails answers that such a
// build is in the fleet. Run in a transaction, a holding answer keeps the gate shared to the
// transaction's end, so such a build's arrival commits only after it; run alone, a statement's
// gate is gone when it ends, which a read that writes nothing of the binding needs no more of.
//
// While such a build is in the fleet, it checks no account and records none, and a credential of
// a replaced account acts on it whatever this build does; so this build does as it does, and
// writes nothing of the binding that an account it lets take the address back would leave
// standing. Each caller ANDs this with whether the deployment's sign-in records accounts at all.
func AccountBindingHoldsSQL(legacyWindow, markWindow string) string {
	return "(not public.account_binding_fleet(" + legacyWindow + "::float8, " + markWindow + "::float8))"
}

// AccountBindingWindows is the two windows a statement passes for AccountBindingHoldsSQL, in
// seconds: OwnerLockLegacyWindow and OwnerLockMarkWindow, the owner lock's own, since the two read
// one fence.
func AccountBindingWindows() (legacyWindow, markWindow float64) {
	return OwnerLockLegacyWindow.Seconds(), OwnerLockMarkWindow.Seconds()
}

// AccountBindingHolds reads in tx, once, whether the account binding holds over the fleet
// (AccountBindingHoldsSQL), for a transaction that writes what the binding records or decides on
// it: a sign-in, a mint, a decision's re-read. A holding answer keeps the binding's gate shared to
// tx's end, so a build without the binding that arrives meanwhile waits for tx to commit; every
// statement after it in tx reads the answer it gave.
func AccountBindingHolds(ctx context.Context, tx pgx.Tx) (bool, error) {
	legacy, mark := AccountBindingWindows()
	var holds bool
	if err := tx.QueryRow(ctx, `select `+AccountBindingHoldsSQL("$1", "$2"), legacy, mark).Scan(&holds); err != nil {
		return false, fmt.Errorf("read whether a build without the account binding is in the fleet: %w", err)
	}
	return holds, nil
}
