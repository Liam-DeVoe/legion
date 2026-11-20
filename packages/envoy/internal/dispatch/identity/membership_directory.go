package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/sjawhar/envoy/internal/dispatch/auth"
)

// Under IAP identity (DISPATCH_IDENTITY=iap) Identity-Aware Proxy signs people in and Dispatch
// keeps no refresh token, so Google's refusal of a refresh cannot reach it. Membership follows each
// person's Google Workspace account in the Directory instead (Membership.Directory): an Active
// answer confirms the person, releases a hold and fixes a deadline 24 hours later; a Held answer
// records a hold; and a Failed check changes nothing but the time of the check.

// DirectoryPeople is what the people store records of a Directory check:
// store.PgPeopleStore's writes of an Active, a Held and a Failed outcome. Under IAP identity
// Membership.People must be one.
type DirectoryPeople interface {
	RecordDirectoryActive(ctx context.Context, email, userID string) (notHeldUntil time.Time, err error)
	RecordDirectoryHeld(ctx context.Context, email string) error
	RecordDirectoryFailed(ctx context.Context, email string) error
}

// errNoDirectoryPeople is a Membership under IAP identity whose People records no Directory
// outcome: every check is Failed, so nobody is served past their deadline.
var errNoDirectoryPeople = errors.New("identity: the people store records no Directory outcome")

// directoryStanding is Standing under IAP identity, on read, Standing's first read of email's row
// (found false for no row), from begun, Standing's call. These steps, in order, the first that
// applies answering:
//
//  1. no row: unavailable, and the Directory is asked nothing (IAP's sign-in records a row for every
//     person it admits, so a credential naming a login with none is nobody Dispatch can confirm);
//  2. a recorded hold (auth.PersonMembership.DirectoryHeld): held, whatever the deadline says;
//  3. an Active answer younger than MembershipMaxAge: not held, with its deadline;
//  4. within unavailablePause after a Failed check of this person in this process, or while a check
//     of this person in this process has run for more than standingWait: no new check, and the
//     answer by the deadline;
//  5. otherwise it starts, or joins, the person's check (the flight ConfirmDirectory shares), waits
//     for it at most standingWait from begun, and reads the row again: the check, or another
//     process, may have recorded an outcome since. A check the wait gave up on goes on to write its
//     outcome within its own DirectoryFlight.
//
// The answer by a read's deadline (byDeadline) is held for a recorded hold; not held, with the
// read's not_held_until, while the read's time is before it; and unavailable at or after it, and
// for a row with no Active answer.
func (m *Membership) directoryStanding(ctx context.Context, email string, read auth.PersonMembership, found bool, begun time.Time) (Standing, error) {
	if !found || read.DirectoryHeld || directoryFresh(read) {
		return byDeadline(read), nil
	}
	if paused, ok := m.unavailable.Load(email); ok && time.Since(paused.(unavailableOutcome).at) < unavailablePause {
		return byDeadline(read), nil
	}
	if started, ok := m.started.Load(email); ok && time.Since(started.(time.Time)) > standingWait {
		return byDeadline(read), nil
	}
	flight := m.directoryFlight(ctx, email)
	wait := time.NewTimer(max(standingWait-time.Since(begun), 0))
	defer wait.Stop()
	select {
	case <-flight:
	case <-wait.C:
	}
	reread, _, err := m.People.Membership(ctx, email)
	if err != nil {
		return Standing{}, err
	}
	return byDeadline(reread), nil
}

// ConfirmDirectory checks email's Workspace account for the membership pass under IAP identity,
// starting or joining the one check of this person in flight in this process, and waits for it.
// Its RefreshOutcome is the pass's count: RefreshConfirmed for an Active outcome, RefreshHeld for a
// Held one, RefreshFailed for a Failed check and for an outcome whose write failed or missed the
// check's deadline, and RefreshNone when the row it read under the flight needed no check (an
// Active answer younger than MembershipMaxAge, nil) or is gone (ErrNoIdentity).
func (m *Membership) ConfirmDirectory(ctx context.Context, email string) (RefreshOutcome, error) {
	return outcomeOf(<-m.directoryFlight(ctx, email))
}

// directoryFlight starts, or joins, the one Directory check of email in flight in this process.
// Like a refresh, the check belongs to the person, not to the request that found it due: it runs on
// a context of its own, apart from the caller's cancellation, bounded by DirectoryFlight, the one
// deadline the check and the write of its outcome share, so a caller that stops waiting (Standing)
// leaves it to write its outcome, and a write that misses the deadline is Failed.
func (m *Membership) directoryFlight(ctx context.Context, email string) <-chan singleflight.Result {
	return m.checks.DoChan(email, func() (any, error) {
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), DirectoryFlight)
		defer cancel()
		outcome, err := m.checkDirectory(checkCtx, email)
		return outcome, err
	})
}

// checkDirectory checks email's Workspace account once, within ctx, and records the outcome: Active
// RecordDirectoryActive, with the id of the account that answered, Held RecordDirectoryHeld, and
// Failed RecordDirectoryFailed. It hands Check the Directory user id the row stores, so an answer
// for another account than the person's own is Held. It reads the row again first, under the
// flight: a check that finished just before this one began has already confirmed the person. A held
// row is always checked, since only an Active answer releases it. An Active or Held outcome whose
// write fails is Failed, and records nothing more: its deadline has passed, or the database is
// not answering. It records its start in m.started while it runs, and, as it ends, its end in
// m.unavailable when it was Failed, deleting any entry otherwise, as a refresh does.
func (m *Membership) checkDirectory(ctx context.Context, email string) (outcome RefreshOutcome, err error) {
	m.started.Store(email, time.Now())
	defer func() {
		m.started.Delete(email)
		if outcome == RefreshFailed {
			m.unavailable.Store(email, unavailableOutcome{at: time.Now(), err: err})
		} else {
			m.unavailable.Delete(email)
		}
	}()
	people, ok := m.People.(DirectoryPeople)
	if !ok {
		slog.Error("dispatch: a Workspace Directory check cannot record its outcome; the person is not confirmed", "email", email, "error", errNoDirectoryPeople)
		return RefreshFailed, errNoDirectoryPeople
	}
	read, found, err := m.People.Membership(ctx, email)
	if err != nil {
		return RefreshFailed, err
	}
	if !found {
		return RefreshNone, ErrNoIdentity
	}
	if !read.DirectoryHeld && directoryFresh(read) {
		return RefreshNone, nil
	}
	answered, userID, err := m.Directory.Check(ctx, email, read.DirectoryUserID)
	switch answered {
	case DirectoryActive:
		if _, err := people.RecordDirectoryActive(ctx, email, userID); err != nil {
			slog.Warn("dispatch: a Workspace Directory check answered Active, and its write failed; the check is Failed", "email", email, "error", err)
			return RefreshFailed, err
		}
		return RefreshConfirmed, nil
	case DirectoryHeld:
		if err := people.RecordDirectoryHeld(ctx, email); err != nil {
			slog.Warn("dispatch: a Workspace Directory check answered Held, and its write failed; the check is Failed", "email", email, "error", err)
			return RefreshFailed, err
		}
		slog.Info("dispatch: the Workspace Directory reports a person's account suspended, missing or another's; their credentials are held", "email", email)
		return RefreshHeld, nil
	}
	slog.Warn("dispatch: a Workspace Directory check failed; the person's confirmation, deadline and hold stay as they were", "email", email, "error", err)
	if recordErr := people.RecordDirectoryFailed(ctx, email); recordErr != nil {
		err = errors.Join(err, fmt.Errorf("record the failed check: %w", recordErr))
	}
	return RefreshFailed, err
}

// directoryFresh reports whether read's last Active Directory answer is younger than
// MembershipMaxAge, by the database's clock (the read's own time against the stored answer).
func directoryFresh(read auth.PersonMembership) bool {
	return read.DirectoryConfirmedAt != nil && read.ReadAt.Sub(*read.DirectoryConfirmedAt) < MembershipMaxAge
}

// byDeadline is Standing's answer under IAP identity on read: held for a recorded hold; not held,
// with read's not_held_until, while read's time is before it; and unavailable (NotHeldUntil equal
// to AsOf) at or after it, and for a row with no Active answer or no row at all.
func byDeadline(read auth.PersonMembership) Standing {
	answer := Standing{AsOf: read.ReadAt}
	switch {
	case read.DirectoryHeld:
		answer.Held = true
	case read.NotHeldUntil != nil && read.ReadAt.Before(*read.NotHeldUntil):
		answer.NotHeldUntil = *read.NotHeldUntil
	default:
		answer.NotHeldUntil = read.ReadAt
	}
	return answer
}
