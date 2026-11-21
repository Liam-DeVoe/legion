package store

import (
	"context"
	"fmt"
	"slices"
	"time"

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

// keepAccountChangeSQL keeps the change of $1's Google account that a sign-in made in its
// transaction while it read the account binding as off (PgPeopleStore.SignInSubject), for the
// catch-up (CatchUpAccountBinding): the account it replaced, $2, empty for the address's first
// recording, and the recording time the database stamped on the change (people.sub_recorded_at),
// in account_binding_replacements (migration 20261121121717). It announces it on $3,
// BrokerRevocationsChannel, so the revocation loop wakes at the commit and watches for the binding
// to hold. A row the trigger people_account_binding_replacement kept for the same change has the
// same key, so it adds nothing then.
//
// The sign-in keeps the change by the reading it revoked nothing on, not by the trigger's own
// reading, which comes later and can differ: an arrival of a build without the binding holds the
// binding's gate exclusively, uncommitted, through its whole dashboard publish (or its
// registration's wait), so the sign-in's try of the gate fails while the trigger sees no such
// build in what is committed; and a process without the binding whose mark has aged out can
// deregister between the two readings. Left to the trigger, such a change would be revoked by
// nobody and kept for nobody.
const keepAccountChangeSQL = `
	with kept as (
		insert into account_binding_replacements (email, replaced_subject, replaced_at)
		select email, nullif($2, ''), sub_recorded_at from people where email = $1
		on conflict do nothing
	)
	select pg_notify($3, '')`

// AccountBindingCatchUp is what one catch-up of an address did (CatchUpAccountBinding): the
// accounts its kept changes replaced, in the order they were replaced; whether one of them was the
// address's first recording, which replaced none; whether a personal token the binding refuses
// outlived the address's recording (RefusedLive, refusedTokenOutlivedSQL); how many of its personal
// tokens it revoked; the accounts it recorded a broker revocation of; and the account that holds
// the address, which each revocation names as the holder.
type AccountBindingCatchUp struct {
	Replaced       []string
	FirstRecording bool
	RefusedLive    bool
	TokensRevoked  int64
	Owed           []string
	Holder         string
}

// Found reports whether the catch-up found anything to catch up.
func (c AccountBindingCatchUp) Found() bool {
	return len(c.Replaced) > 0 || c.FirstRecording || c.RefusedLive
}

// refusedTokenOutlivedSQL is the aggregate, over the rows refusedTokenSweepFromSQL reads (an
// address's people row, people, joined to each of its personal tokens, t), that a token the
// binding refuses outlived the address's recording: one refused by its own recorded account or its
// creation time (api.tokenAccountReplaced's first two arms: it records an account people no longer
// records, however and whenever it was minted, or it records none and was made before
// people.sub_recorded_at) that is live, or was revoked after that recording and so was live after
// it. While a build without the binding was in the fleet, every build admitted it then, so it may
// have acted, minted personal tokens that record no account and approved machine logins that record
// none; and while it stays live, every later such time admits it again.
const refusedTokenOutlivedSQL = `bool_or(((t.subject is not null and t.subject <> people.sub)
			or (t.subject is null and people.sub_recorded_at is not null and t.created_at < people.sub_recorded_at))
		and (t.revoked_at is null or t.revoked_at > people.sub_recorded_at))`

// otherAccountTokenLiveSQL is the aggregate, over the same rows, that a live personal token of the
// address records an account other than people's, or none: what revokeOtherAccountsTokensSQL
// revokes.
const otherAccountTokenLiveSQL = `bool_or(t.revoked_at is null and t.subject is distinct from people.sub)`

// refusedTokenSweepFromSQL is the rows refusedTokenOutlivedSQL and otherAccountTokenLiveSQL read:
// every personal token of each address that records a Google account (people.sub set), joined to
// its people row by the owner in the form people.email holds (canonical_login), as
// revokeAddressTokensSQL compares owners. agent_tokens keeps no link from a token to the credential
// that minted it, so a token's descendants are read by what they record and when they were made.
const refusedTokenSweepFromSQL = `
	from people join agent_tokens t on canonical_login(t.owner) = people.email
	where people.sub is not null`

// CatchUpAccountBinding catches up, in tx, what the account binding did not revoke at email while
// it was off, doing what a sign-in with the binding on would have done: for a caller that holds
// email's lock (addressLockSQL) and read the binding as holding in tx (AccountBindingHolds). email
// is the address as people.email holds it. It catches up two things.
//
// The changes of email's Google account the database kept while the binding was off
// (account_binding_replacements, migration 20261121121717): it revokes every personal token of
// email that records no account and was made at or after the earliest kept change, whichever build
// minted it and whichever account's credential did, the holder's own included: none of them can be
// told from one a credential from before the change minted. For a kept first recording it also
// revokes every token that records an account other than the holder's or none, as a first
// recording does (revokeOtherAccountsTokens). It deletes the address's rows, so a second catch-up
// finds none and does nothing.
//
// A personal token the binding refuses that outlived the address's recording
// (refusedTokenOutlivedSQL): the binding refuses it while it holds, but every build admits it
// while a build without the binding is in the fleet, so it can act and mint again then, and
// whatever it minted then records no account and was made after the recording, which the binding
// admits as the holder's. So while any token of email that records an account other than the
// holder's, or none, is live (otherAccountTokenLiveSQL), it revokes every such token, as a first
// recording does (revokeOtherAccountsTokens): the refused tokens themselves, which no build admits
// once revoked, so no later such time admits them again, and every token recording no account made
// since the recording, which is everything they could have minted, the holder's own overlap mints
// included (they mint again). A token that records the holder's account is left as it is.
//
// When issuer is set (WithBrokerRevocations: a broker is configured), it records a broker
// revocation (recordBrokerRevocationSQL) of each account replaced, or recorded by a token of email
// (otherAccountsSQL), that does not hold email now, naming the holder, after it settles what is
// still owed for the holder (settleReclaimedBrokerRevocationsSQL), as a sign-in does; an account
// that took the address back holds it, and is owed nothing. The broker ends every credential such
// an account holds under the address, and every one recording no account, a machine login whose
// approval named none included. A token recording no account names no account to owe, so an
// address whose refused tokens all record none, and no token of which records another account,
// owes the broker nothing.
func CatchUpAccountBinding(ctx context.Context, tx pgx.Tx, email, issuer string) (AccountBindingCatchUp, error) {
	var caught AccountBindingCatchUp
	rows, err := tx.Query(ctx, `
		select replaced_subject, replaced_at from account_binding_replacements where email = $1 order by replaced_at`, email)
	if err != nil {
		return caught, fmt.Errorf("read the account changes of %q kept while the account binding was off: %w", email, err)
	}
	var earliest time.Time
	kept := 0
	for rows.Next() {
		var subject *string
		var at time.Time
		if err := rows.Scan(&subject, &at); err != nil {
			rows.Close()
			return caught, fmt.Errorf("read the account changes of %q kept while the account binding was off: %w", email, err)
		}
		if kept == 0 {
			earliest = at
		}
		kept++
		if subject == nil {
			caught.FirstRecording = true
		} else {
			caught.Replaced = append(caught.Replaced, *subject)
		}
	}
	if err := rows.Err(); err != nil {
		return caught, fmt.Errorf("read the account changes of %q kept while the account binding was off: %w", email, err)
	}
	var owed []string
	if kept > 0 {
		if err := tx.QueryRow(ctx, `select coalesce(sub, '') from people where email = $1`, email).Scan(&caught.Holder); err != nil {
			return caught, fmt.Errorf("read the Google account %q records: %w", email, err)
		}
		revoked, err := tx.Exec(ctx, `
			update agent_tokens set revoked_at = now()
			where canonical_login(owner) = $1 and revoked_at is null and subject is null and created_at >= $2`, email, earliest)
		if err != nil {
			return caught, fmt.Errorf("revoke the personal tokens of %q made since an account change kept while the account binding was off: %w", email, err)
		}
		caught.TokensRevoked = revoked.RowsAffected()
		owed = caught.Replaced
		if caught.FirstRecording && caught.Holder != "" {
			others, accounts, err := revokeOtherAccountsTokens(ctx, tx, email, caught.Holder)
			if err != nil {
				return caught, err
			}
			caught.TokensRevoked += others
			owed = append(slices.Clone(owed), accounts...)
		}
	}
	var holder string
	if err := tx.QueryRow(ctx, `
		select coalesce(`+refusedTokenOutlivedSQL+` and `+otherAccountTokenLiveSQL+`, false), coalesce(min(people.sub), '')`+
		refusedTokenSweepFromSQL+` and people.email = $1`, email).Scan(&caught.RefusedLive, &holder); err != nil {
		return caught, fmt.Errorf("read whether a personal token of %q the account binding refuses outlived its recording: %w", email, err)
	}
	if caught.RefusedLive {
		caught.Holder = holder
		others, accounts, err := revokeOtherAccountsTokens(ctx, tx, email, holder)
		if err != nil {
			return caught, err
		}
		caught.TokensRevoked += others
		owed = append(slices.Clone(owed), accounts...)
	}
	if !caught.Found() {
		return caught, nil
	}
	for _, subject := range owed {
		if subject != caught.Holder && !slices.Contains(caught.Owed, subject) {
			caught.Owed = append(caught.Owed, subject)
		}
	}
	if issuer != "" && caught.Holder != "" && len(caught.Owed) > 0 {
		if _, err := tx.Exec(ctx, settleReclaimedBrokerRevocationsSQL, email, issuer, caught.Holder); err != nil {
			return caught, fmt.Errorf("settle the broker revocations of %q owed for the Google account that holds it: %w", email, err)
		}
		for _, account := range caught.Owed {
			if _, err := tx.Exec(ctx, recordBrokerRevocationSQL, email, issuer, account, caught.Holder); err != nil {
				return caught, fmt.Errorf("record the broker revocation of %q owed for an account change the account binding did not revoke for: %w", email, err)
			}
		}
	} else {
		caught.Owed = nil
	}
	if kept > 0 {
		if _, err := tx.Exec(ctx, `delete from account_binding_replacements where email = $1`, email); err != nil {
			return caught, fmt.Errorf("delete the account changes of %q kept while the account binding was off: %w", email, err)
		}
	}
	return caught, nil
}

// CatchUpAccountBindingOf catches up email in a transaction of its own (CatchUpAccountBinding),
// after it takes email's lock and reads whether the binding holds, as a sign-in does: held is
// false, and nothing is written, while a build without the binding is in the fleet. The revocation
// loop runs it for every address AccountBindingCatchUpAddresses answers.
func CatchUpAccountBindingOf(ctx context.Context, pool *Pool, email, issuer string) (held bool, caught AccountBindingCatchUp, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, caught, fmt.Errorf("catch up the account binding of %q: %w", email, err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, addressLockSQL, email); err != nil {
		return false, caught, fmt.Errorf("catch up the account binding of %q: lock the address: %w", email, err)
	}
	if held, err = AccountBindingHolds(ctx, tx); err != nil || !held {
		return false, caught, err
	}
	if caught, err = CatchUpAccountBinding(ctx, tx, email, issuer); err != nil {
		return false, caught, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, caught, fmt.Errorf("catch up the account binding of %q: %w", email, err)
	}
	return true, caught, nil
}

// AccountBindingCatchUpAddresses answers every address the account binding has something to catch
// up for (CatchUpAccountBinding): one with an account change kept while it was off
// (account_binding_replacements), and one with a live personal token that records an account other
// than the holder's or none while a token the binding refuses outlived its recording
// (refusedTokenOutlivedSQL and otherAccountTokenLiveSQL), the earliest change or recording first.
// The second reads every personal token once, in one join with people.
func AccountBindingCatchUpAddresses(ctx context.Context, pool *Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `
		select email from (
			select email, replaced_at as since from account_binding_replacements
			union all
			select people.email, min(people.sub_recorded_at)`+refusedTokenSweepFromSQL+`
			group by people.email
			having `+refusedTokenOutlivedSQL+` and `+otherAccountTokenLiveSQL+`
		) due group by email order by min(since) nulls last, email`)
	if err != nil {
		return nil, fmt.Errorf("read the addresses the account binding has something to catch up for: %w", err)
	}
	emails, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the addresses the account binding has something to catch up for: %w", err)
	}
	return emails, nil
}
