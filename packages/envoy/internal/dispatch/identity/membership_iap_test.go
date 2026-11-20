package identity

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/identity/directorytest"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// The IAP Directory tests (R9, c-iap-directory): Standing and the membership pass under IAP
// identity, on the real people store, with the Directory reader reaching t-23's fake Google hosts.
// Ada's address is directoryAda, the address the fake's rows answer for.

// directoryStandingRig is the people store on a database of its own, the fake Google hosts, and a
// Membership that follows the Directory as one IAP Dispatch process holds it.
type directoryStandingRig struct {
	t          *testing.T
	database   *store.Store
	people     *store.PgPeopleStore
	google     *directorytest.Google
	membership *Membership
}

func newDirectoryStandingRig(t *testing.T) *directoryStandingRig {
	t.Helper()
	database := storetest.Open(t)
	r := &directoryStandingRig{
		t:        t,
		database: database,
		people:   store.NewPgPeopleStore(database.Pool, testSigningKey, nil),
		google:   directorytest.ServeGoogle(t),
	}
	r.newProcess()
	return r
}

// newProcess is another Dispatch process over the same database and Google: a reader holding no
// minted token, and a Membership with no check in flight and no pause.
func (r *directoryStandingRig) newProcess() {
	r.t.Helper()
	reader, err := NewDirectoryReader(r.t.Context(), directorySigner, directoryAdmin, r.google.ClientOptions()...)
	if err != nil {
		r.t.Fatalf("NewDirectoryReader: %v", err)
	}
	r.membership = &Membership{People: r.people, Rule: workspace, Directory: reader}
}

// pass is one membership pass over the rig's Membership, telling observe of each claim attempt.
func (r *directoryStandingRig) pass(observe func(MembershipPassEvent)) MembershipPass {
	return MembershipPass{Membership: r.membership, People: r.people, Observe: observe}
}

// record records email as IAP's sign-in does (IAPIdentity.Login, PeopleStore.Record): a full
// member's row with no refresh token and no Directory answer.
func (r *directoryStandingRig) record(email string) {
	r.t.Helper()
	if err := r.people.Record(context.Background(), email); err != nil {
		r.t.Fatalf("record %s: %v", email, err)
	}
}

// active records an Active outcome for email under the Directory user id, and dates every
// Directory column age back, so the row's confirmation is age old and its deadline 24 hours after.
func (r *directoryStandingRig) active(email, id string, age time.Duration) {
	r.t.Helper()
	if _, err := r.people.RecordDirectoryActive(context.Background(), email, id); err != nil {
		r.t.Fatal(err)
	}
	r.age(email, age)
}

// held records a Held outcome for email after its Active one, as a suspension does.
func (r *directoryStandingRig) held(email string) {
	r.t.Helper()
	if err := r.people.RecordDirectoryHeld(context.Background(), email); err != nil {
		r.t.Fatal(err)
	}
}

// age dates every Directory column of email, and its hold, age back, keeping their order.
func (r *directoryStandingRig) age(email string, age time.Duration) {
	r.t.Helper()
	if _, err := r.database.Pool.Exec(context.Background(), `
		update people set directory_confirmed_at = directory_confirmed_at - make_interval(secs => $2),
			directory_checked_at = directory_checked_at - make_interval(secs => $2),
			not_held_until = not_held_until - make_interval(secs => $2),
			held_since = held_since - make_interval(secs => $2)
		where email = $1`, email, age.Seconds()); err != nil {
		r.t.Fatalf("date %s's Directory columns %s back: %v", email, age, err)
	}
}

// directoryRow is what people holds of email's Directory checks.
type directoryRow struct {
	confirmedAt, checkedAt, notHeldUntil, heldSince *time.Time
	userID                                          *string
}

func (r *directoryStandingRig) row(email string) directoryRow {
	r.t.Helper()
	var row directoryRow
	if err := r.database.Pool.QueryRow(context.Background(), `
		select directory_confirmed_at, directory_checked_at, not_held_until, held_since, directory_user_id
		from people where email = $1`, email).Scan(&row.confirmedAt, &row.checkedAt, &row.notHeldUntil, &row.heldSince, &row.userID); err != nil {
		r.t.Fatalf("read %s's Directory columns: %v", email, err)
	}
	return row
}

// standing is the rig's Standing of email, failing the test on an error.
func (r *directoryStandingRig) standing(email string) Standing {
	r.t.Helper()
	answer, err := r.membership.Standing(context.Background(), email)
	if err != nil {
		r.t.Fatalf("Standing(%s): %v", email, err)
	}
	return answer
}

// reads is how many users.get requests reached the fake Directory.
func (r *directoryStandingRig) reads() int { return len(r.google.Reads()) }

// sameTime reports whether two stored times are one instant, both absent included.
func sameTime(a, b *time.Time) bool {
	return (a == nil) == (b == nil) && (a == nil || a.Equal(*b))
}

// stamp is a stored time as a test failure names it.
func stamp(at *time.Time) string {
	if at == nil {
		return "null"
	}
	return at.Format(time.RFC3339Nano)
}

// adaActive makes the fake Directory answer ada's own account, active.
func adaActive(g *directorytest.Google) {
	g.SetUser(directoryAda, directorytest.User{ID: "u-ada", PrimaryEmail: directoryAda})
}

// TestIAPStandingFollowsTheDirectory: under IAP identity Standing answers from the Workspace
// Directory and never refreshes. A login with no people row is unavailable and asks the Directory
// nothing. A recorded hold is held, whatever its deadline says, and asks nothing. An Active answer
// younger than an hour is not held with its deadline and asks nothing. An older one asks once, and
// an Active outcome answers not held with the deadline it fixed, 24 hours after it. Concurrent calls
// share one check. A check that has not ended answers by the deadline within the 2-second wait,
// and goes on to write its outcome. After a Failed check, Standing asks nothing for a minute.
func TestIAPStandingFollowsTheDirectory(t *testing.T) {
	t.Run("no row", func(t *testing.T) {
		r := newDirectoryStandingRig(t)
		adaActive(r.google)
		answer := r.standing(directoryAda)
		if !answer.Unavailable() || answer.Held || !answer.NotHeldUntil.Equal(answer.AsOf) || r.reads() != 0 {
			t.Fatalf("Standing of a login with no row = %+v after %d Directory reads; want unavailable (NotHeldUntil = AsOf) and no read", answer, r.reads())
		}
	})
	t.Run("a recorded hold", func(t *testing.T) {
		r := newDirectoryStandingRig(t)
		adaActive(r.google)
		r.record(directoryAda)
		r.active(directoryAda, "u-ada", 0)
		r.held(directoryAda)
		r.age(directoryAda, 30*time.Hour)
		if answer := r.standing(directoryAda); !answer.Held || !answer.NotHeldUntil.IsZero() || r.reads() != 0 {
			t.Fatalf("Standing of a recorded hold past its deadline = %+v after %d reads; want held, no deadline, no read", answer, r.reads())
		}
	})
	t.Run("a fresh Active answer", func(t *testing.T) {
		r := newDirectoryStandingRig(t)
		adaActive(r.google)
		r.record(directoryAda)
		r.active(directoryAda, "u-ada", 50*time.Minute)
		stored := r.row(directoryAda).notHeldUntil
		answer := r.standing(directoryAda)
		if answer.Held || answer.Unavailable() || !answer.NotHeldUntil.Equal(*stored) || r.reads() != 0 {
			t.Fatalf("Standing of an Active answer 50 minutes old = %+v after %d reads; want not held until the stored %s, and no read", answer, r.reads(), stamp(stored))
		}
	})
	t.Run("an older Active answer", func(t *testing.T) {
		r := newDirectoryStandingRig(t)
		adaActive(r.google)
		r.record(directoryAda)
		r.active(directoryAda, "u-ada", 2*time.Hour)
		before := r.row(directoryAda)
		var wg sync.WaitGroup
		answers := make([]Standing, 5)
		for i := range answers {
			wg.Go(func() { answers[i] = r.standing(directoryAda) })
		}
		wg.Wait()
		after := r.row(directoryAda)
		if r.reads() != 1 || !after.confirmedAt.After(*before.confirmedAt) {
			t.Fatalf("five concurrent Standings of an Active answer two hours old read the Directory %d times, confirmation %s -> %s; want one shared check that confirms", r.reads(), stamp(before.confirmedAt), stamp(after.confirmedAt))
		}
		for _, answer := range answers {
			if answer.Held || !answer.NotHeldUntil.Equal(*after.notHeldUntil) || !after.notHeldUntil.Equal(after.confirmedAt.Add(24*time.Hour)) {
				t.Fatalf("Standing = %+v; want not held until the new deadline %s, 24 hours after the confirmation %s", answer, stamp(after.notHeldUntil), stamp(after.confirmedAt))
			}
		}
	})
	t.Run("a check longer than the wait", func(t *testing.T) {
		r := newDirectoryStandingRig(t)
		r.record(directoryAda)
		r.active(directoryAda, "u-ada", 2*time.Hour)
		before := r.row(directoryAda)
		release := r.google.HoldSigner()
		started := time.Now()
		answer := r.standing(directoryAda)
		if took := time.Since(started); took > standingWait+time.Second || answer.Held || !answer.NotHeldUntil.Equal(*before.notHeldUntil) {
			t.Fatalf("Standing while the check is held = %+v after %s; want not held until the old deadline %s within the %s wait", answer, took, stamp(before.notHeldUntil), standingWait)
		}
		if again := r.standing(directoryAda); r.reads() != 0 || again.Held {
			t.Fatalf("a second Standing while the held check runs = %+v; want the deadline's answer at once, and no read", again)
		}
		adaActive(r.google)
		release()
		waitFor(t, "the held check's Active outcome", func() bool {
			return r.row(directoryAda).confirmedAt.After(*before.confirmedAt)
		})
	})
	t.Run("a minute after a Failed check", func(t *testing.T) {
		r := newDirectoryStandingRig(t)
		directoryRefusal(http.StatusInternalServerError)(r.google)
		r.record(directoryAda)
		r.active(directoryAda, "u-ada", 2*time.Hour)
		if first := r.standing(directoryAda); first.Held || first.Unavailable() || r.reads() != 1 {
			t.Fatalf("Standing over a failing Directory = %+v after %d reads; want not held before the deadline, after one read", first, r.reads())
		}
		adaActive(r.google)
		if second := r.standing(directoryAda); second.Held || r.reads() != 1 {
			t.Fatalf("Standing a moment after a Failed check = %+v after %d reads; want the deadline's answer and no new read", second, r.reads())
		}
	})
}

// TestIAPDirectoryFailureChangesNothing (c-iap-directory): each Failed kind of a Directory check -
// a reply later than the check's 15 seconds, 429, 500, 401, 403, a body that does not decode, an
// answer with no suspended, a signJwt refusal and a token-exchange refusal - run by the membership
// pass, leaves directory_confirmed_at, not_held_until and the hold as they were, and moves
// directory_checked_at. A held person whose deadline passes during the failures stays held.
//
// Control: let a Failed outcome set directory_confirmed_at or clear the hold (record a Failed
// outcome as an Active one); the test fails naming the kind.
func TestIAPDirectoryFailureChangesNothing(t *testing.T) {
	stalled := directorytest.Answer{Stall: true}
	kinds := []struct {
		name string
		fail func(*directorytest.Google)
	}{
		{"a reply later than the check's 15 seconds", func(g *directorytest.Google) { g.AnswerUser(directoryAda, stalled) }},
		{"429", directoryRefusal(http.StatusTooManyRequests)},
		{"500", directoryRefusal(http.StatusInternalServerError)},
		{"401", directoryRefusal(http.StatusUnauthorized)},
		{"403", directoryRefusal(http.StatusForbidden)},
		{"a body that does not decode", func(g *directorytest.Google) {
			g.AnswerUser(directoryAda, directorytest.Answer{Status: http.StatusOK, Body: "<html>" + directorytest.Planted + "</html>"})
		}},
		{"an answer with no suspended", func(g *directorytest.Google) {
			g.AnswerUser(directoryAda, directorytest.Answer{Status: http.StatusOK, Body: `{"id":"u-ada","primaryEmail":"ada@example.com"}`})
		}},
		{"a signJwt refusal", func(g *directorytest.Google) {
			g.AnswerSigner(&directorytest.Answer{Status: http.StatusForbidden, Body: `{"error":{"code":403,"message":"` + directorytest.Planted + `"}}`})
		}},
		{"a token-exchange refusal", func(g *directorytest.Google) {
			g.AnswerScope(directorytest.DirectoryScope, &directorytest.Answer{Status: http.StatusBadRequest, Body: `{"error":"invalid_grant"}`})
		}},
	}
	for _, held := range []bool{false, true} {
		name := "a confirmed person"
		if held {
			name = "a held person"
		}
		t.Run(name, func(t *testing.T) {
			r := newDirectoryStandingRig(t)
			r.record(directoryAda)
			r.active(directoryAda, "u-ada", 0)
			if held {
				r.held(directoryAda)
			}
			for _, kind := range kinds {
				// A process of its own, holding no minted token, so the signer and the token
				// exchange are asked, and the row due again.
				r.newProcess()
				r.google.AnswerSigner(nil)
				r.google.AnswerScope(directorytest.DirectoryScope, nil)
				adaActive(r.google)
				kind.fail(r.google)
				r.age(directoryAda, 2*time.Hour)
				before := r.row(directoryAda)
				var counted []MembershipPassEvent
				if err := r.pass(func(e MembershipPassEvent) { counted = append(counted, e) }).Run(context.Background()); err != nil {
					t.Fatalf("%s: the pass: %v", kind.name, err)
				}
				after := r.row(directoryAda)
				if len(counted) == 0 || !counted[0].Claimed || counted[0].Email != directoryAda {
					t.Fatalf("%s: the pass claimed %+v; want ada's row first", kind.name, counted)
				}
				if !sameTime(after.confirmedAt, before.confirmedAt) || !sameTime(after.notHeldUntil, before.notHeldUntil) || !sameTime(after.heldSince, before.heldSince) {
					t.Fatalf("%s: confirmed %s -> %s, deadline %s -> %s, held since %s -> %s; want each as it was",
						kind.name, stamp(before.confirmedAt), stamp(after.confirmedAt), stamp(before.notHeldUntil), stamp(after.notHeldUntil), stamp(before.heldSince), stamp(after.heldSince))
				}
				if after.checkedAt == nil || !after.checkedAt.After(*before.checkedAt) {
					t.Fatalf("%s: directory_checked_at %s -> %s; want it moved", kind.name, stamp(before.checkedAt), stamp(after.checkedAt))
				}
			}
			// The failures above aged the row 18 hours; 7 more put it past its deadline.
			r.age(directoryAda, 7*time.Hour)
			r.newProcess()
			if answer := r.standing(directoryAda); held && !answer.Held {
				t.Fatalf("a held person whose deadline passed during Directory failures = %+v; want held, never unavailable", answer)
			} else if !held && !answer.Unavailable() {
				t.Fatalf("a confirmed person whose deadline passed during Directory failures = %+v; want unavailable", answer)
			}
		})
	}
}

// TestIAPDirectoryDeadlineIncludesTheWrite (c-iap-directory): a check and the write of its outcome
// share one 15-second deadline. While a transaction holds ada's row locked past it, an Active
// answer's write cannot commit: the check ends Failed at the deadline, the lock's release finds no
// write waiting, and the row holds no confirmation, no deadline and no stored id. Standing does not
// wait on it past its 2 seconds.
//
// Control: leave the write out of the 15-second deadline (record the outcome on a context without
// it); the write commits once the lock is released, and the test fails naming the confirmation.
func TestIAPDirectoryDeadlineIncludesTheWrite(t *testing.T) {
	r := newDirectoryStandingRig(t)
	adaActive(r.google)
	r.record(directoryAda)
	ctx := context.Background()
	lock, err := r.database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Exec(ctx, `select 1 from people where email = $1 for update`, directoryAda); err != nil {
		t.Fatal(err)
	}
	held := DirectoryFlight + 3*time.Second
	go func() {
		time.Sleep(held)
		_ = lock.Rollback(ctx)
	}()
	started := time.Now()
	waited := make(chan Standing, 1)
	go func() {
		answer, _ := r.membership.Standing(ctx, directoryAda)
		waited <- answer
	}()
	select {
	case answer := <-waited:
		if took := time.Since(started); took > standingWait+time.Second || !answer.Unavailable() {
			t.Fatalf("Standing while the write waits on the lock = %+v after %s; want unavailable within the %s wait", answer, took, standingWait)
		}
	case <-time.After(standingWait + 5*time.Second):
		t.Fatalf("Standing waited on the check past %s", standingWait+5*time.Second)
	}
	outcome, err := r.membership.ConfirmDirectory(ctx, directoryAda)
	took := time.Since(started)
	if outcome != RefreshFailed || err == nil || took > DirectoryFlight+2*time.Second {
		t.Fatalf("the check whose write waits on the lock = %v, %v after %s; want Failed by the %s deadline", outcome, err, took, DirectoryFlight)
	}
	time.Sleep(time.Until(started.Add(held + time.Second)))
	if row := r.row(directoryAda); row.confirmedAt != nil || row.notHeldUntil != nil || row.userID != nil {
		t.Fatalf("after the lock's release ada's row holds confirmation %s, deadline %s, id %v; want none: the Active write missed its deadline",
			stamp(row.confirmedAt), stamp(row.notHeldUntil), row.userID)
	}
}

// TestIAPNeverConfirmedRowIsUnavailable (c-iap-directory): a row the Directory never answered
// Active for, whose checks fail, is unavailable: not held, with NotHeldUntil equal to AsOf. A
// first-ever check that is Held records a hold on that row, and it answers held, not unavailable.
func TestIAPNeverConfirmedRowIsUnavailable(t *testing.T) {
	r := newDirectoryStandingRig(t)
	directoryRefusal(http.StatusInternalServerError)(r.google)
	r.record(directoryAda)
	if answer := r.standing(directoryAda); !answer.Unavailable() || r.reads() != 1 {
		t.Fatalf("a never-confirmed row whose check fails = %+v after %d reads; want unavailable after one check", answer, r.reads())
	}
	r.newProcess()
	r.google.AnswerUser(directoryAda, directorytest.Answer{Status: http.StatusNotFound})
	answer := r.standing(directoryAda)
	if !answer.Held || answer.Unavailable() || r.row(directoryAda).heldSince == nil {
		t.Fatalf("a never-confirmed row whose first check is Held = %+v, held since %s; want held, with the hold recorded", answer, stamp(r.row(directoryAda).heldSince))
	}
}

// TestIAPMembershipPassChecksHeldIdleAndTokenlessRows (c-iap-directory): under IAP the membership
// pass checks every full member's row that is due, a held row, an idle row (an Active answer two
// hours old whose person made no request, here one whose row still holds a refresh token from an
// earlier cookie sign-in) and a row with no refresh token that was never checked, and runs no
// sign-out step. An Active outcome releases the held row, with no request from the person. A
// restricted member's row is not claimed. The pass counts each outcome, K9's confirmed for Active.
//
// Control: skip held rows in the pass; the held row is not checked, and the test fails naming it.
func TestIAPMembershipPassChecksHeldIdleAndTokenlessRows(t *testing.T) {
	r := newDirectoryStandingRig(t)
	ctx := context.Background()
	const (
		heldEmail      = directoryAda
		idleEmail      = directoryBob
		tokenlessEmail = "carol@example.com"
		restricted     = "dana@outside.example"
	)
	for _, email := range []string{heldEmail, idleEmail, tokenlessEmail} {
		r.google.SetUser(email, directorytest.User{ID: "u-" + email, PrimaryEmail: email})
	}
	r.record(heldEmail)
	r.active(heldEmail, "u-"+heldEmail, 0)
	r.held(heldEmail)
	r.age(heldEmail, 2*time.Hour)
	if err := r.people.SignInSubject(ctx, idleEmail, "g-bob", "bob-refresh", "example.com", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	r.active(idleEmail, "u-"+idleEmail, 2*time.Hour)
	r.record(tokenlessEmail)
	if err := r.people.SignInSubject(ctx, restricted, "g-dana", "dana-refresh", "", model.MemberRestricted); err != nil {
		t.Fatal(err)
	}
	if !r.standing(heldEmail).Held {
		t.Fatal("ada is not held before the pass")
	}

	var events []MembershipPassEvent
	if err := r.pass(func(e MembershipPassEvent) { events = append(events, e) }).Run(ctx); err != nil {
		t.Fatalf("the pass: %v", err)
	}
	claimed := map[string]bool{}
	for _, e := range events {
		if e.Step != 2 {
			t.Errorf("the pass under IAP made a claim attempt of step %d (%+v); want the Directory step alone", e.Step, e)
		}
		if e.Claimed {
			claimed[e.Email] = true
		}
	}
	read := map[string]bool{}
	for _, got := range r.google.Reads() {
		read[got.UserKey] = true
	}
	for _, email := range []string{heldEmail, idleEmail, tokenlessEmail} {
		if !claimed[email] || !read[email] {
			t.Errorf("the pass claimed %s %t and read it in the Directory %t; want both", email, claimed[email], read[email])
		}
	}
	if claimed[restricted] || read[restricted] {
		t.Errorf("the pass claimed the restricted member %s %t, read %t; want neither", restricted, claimed[restricted], read[restricted])
	}
	if answer := r.standing(heldEmail); answer.Held || answer.Unavailable() || r.row(heldEmail).heldSince != nil {
		t.Errorf("ada after the pass's Active outcome = %+v, held since %s; want released, with no request from her", answer, stamp(r.row(heldEmail).heldSince))
	}
	for _, email := range []string{idleEmail, tokenlessEmail} {
		if row := r.row(email); row.confirmedAt == nil || row.notHeldUntil == nil || row.userID == nil || *row.userID != "u-"+email {
			t.Errorf("%s after the pass: confirmed %s, deadline %s, id %v; want an Active outcome recorded", email, stamp(row.confirmedAt), stamp(row.notHeldUntil), row.userID)
		}
	}
	events = nil
	if err := r.pass(func(e MembershipPassEvent) { events = append(events, e) }).Run(ctx); err != nil || len(events) != 1 || events[0].Claimed {
		t.Errorf("a second pass at once claimed %+v, %v; want none due", events, err)
	}
}

// TestForgetDirectoryUserLetsTheNextAnswerCompareByAddress: a row that stores the Directory user id
// of its first Active answer is held when its address answers with another id, as for an address
// deleted and created again for another person. Once `envoy-dispatch forget-directory-user` forgets
// the stored id (store.PgPeopleStore.ForgetDirectoryUser, the command's one write), the next answer
// for the address is compared by address: Active, and its id is stored.
//
// Control: a forget that leaves the stored id; the next answer is Held again, and the test fails.
func TestForgetDirectoryUserLetsTheNextAnswerCompareByAddress(t *testing.T) {
	r := newDirectoryStandingRig(t)
	ctx := context.Background()
	r.record(directoryAda)
	r.active(directoryAda, "u-ada", 2*time.Hour)
	r.google.SetUser(directoryAda, directorytest.User{ID: "u-ada-2", PrimaryEmail: directoryAda})
	if outcome, err := r.membership.ConfirmDirectory(ctx, directoryAda); outcome != RefreshHeld || err != nil || !r.standing(directoryAda).Held {
		t.Fatalf("the address answered with another id = %v, %v; want Held, and ada held", outcome, err)
	}
	if changed, err := r.people.ForgetDirectoryUser(ctx, directoryAda); err != nil || !changed {
		t.Fatalf("ForgetDirectoryUser = %t, %v; want the stored id forgotten", changed, err)
	}
	outcome, err := r.membership.ConfirmDirectory(ctx, directoryAda)
	if outcome != RefreshConfirmed || err != nil {
		t.Fatalf("the next answer after the forget = %v, %v; want Active, compared by address", outcome, err)
	}
	if row, answer := r.row(directoryAda), r.standing(directoryAda); answer.Held || row.userID == nil || *row.userID != "u-ada-2" {
		t.Fatalf("after the forget and an Active answer ada = %+v with id %v; want released, with the new id stored", answer, row.userID)
	}
}
