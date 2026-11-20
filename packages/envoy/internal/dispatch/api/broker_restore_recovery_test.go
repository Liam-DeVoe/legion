package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/broker/brokertest"
	"github.com/sjawhar/envoy/internal/broker/wire"
	"github.com/sjawhar/envoy/internal/dispatch/agentsecrets"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// recoveryRig is a Dispatch server and a real broker (brokertest.NewRig) for the runbook's
// recovery: the pass the server runs is the one the loop runs, and the broker is the one the
// recovery's reads read.
type recoveryRig struct {
	rig         *brokertest.Rig
	dispatch    *store.Store
	served      *server
	people      *store.PgPeopleStore
	revocations *store.BrokerRevocations
	broker      *agentsecrets.Client
}

// recoveryIssuer is the sign-in issuer every account of these tests belongs to.
const recoveryIssuer = "https://accounts.google.com"

// newRecoveryRig builds the pair.
func newRecoveryRig(t *testing.T) *recoveryRig {
	t.Helper()
	rig := brokertest.NewRig(t)
	dispatch := storetest.Open(t)
	deps, err := NewDeps(DepsInput{
		Store: dispatch, Identity: headerIdentity(dispatch), ServerURL: "https://dispatch.example", Events: events.NewBroker(),
		AgentSecretsURL: rig.URL, AgentSecretsTokenFile: accountTokenFile(t, rig.UIToken),
	})
	if err != nil {
		t.Fatalf("new API dependencies: %v", err)
	}
	return &recoveryRig{
		rig: rig, dispatch: dispatch,
		served:      &server{deps: deps, lifetime: t.Context()},
		people:      store.NewPgPeopleStore(dispatch.Pool, testSigningKey, nil, store.WithBrokerRevocations(recoveryIssuer)),
		revocations: store.NewBrokerRevocations(dispatch.Pool),
		broker:      agentsecrets.New(rig.URL, accountTokenFile(t, rig.UIToken)),
	}
}

// refused reports whether the broker refuses subject under email.
func (r *recoveryRig) refused(t *testing.T, email, subject string) bool {
	t.Helper()
	var refused bool
	if err := r.rig.Store.Pool.QueryRow(context.Background(), `select exists (select 1 from replaced_person_subjects where person = $1 and subject = $2)`,
		email, subject).Scan(&refused); err != nil {
		t.Fatal(err)
	}
	return refused
}

// revoke tells the broker that subject no longer holds email, which holder holds now, at
// generation, as Dispatch's loop does.
func (r *recoveryRig) revoke(t *testing.T, email, subject, holder string, generation int64) {
	t.Helper()
	answer, err := r.broker.RevokePersonSubject(context.Background(), wire.RevokePersonSubjectRequest{
		Person: email, Issuer: recoveryIssuer, Subject: subject, HolderSubject: holder, SessionGeneration: &generation,
	})
	if err != nil || answer.State != "revoked" {
		t.Fatalf("the broker's revoke of %s, holder %s, at %s: %+v, %v", subject, holder, email, answer, err)
	}
}

// signInMachine gives account a live sign-in of email on a machine at the broker, the credential a
// restore brings back: `agent-secrets login` writes one, and the revoke ends it.
func (r *recoveryRig) signInMachine(t *testing.T, email, account, thumbprint string) string {
	t.Helper()
	var id string
	if err := r.rig.Store.Pool.QueryRow(context.Background(), `insert into person_signins (person, thumbprint, machine_account, issuer, subject)
		values ($1, $2, 'machine', $3, $4) returning id::text`, email, thumbprint, recoveryIssuer, account).Scan(&id); err != nil {
		t.Fatalf("the sign-in of %s at %s on a machine: %v", account, email, err)
	}
	return id
}

// machineSignInLive reports whether the sign-in id is still live at the broker.
func (r *recoveryRig) machineSignInLive(t *testing.T, id string) bool {
	t.Helper()
	var live bool
	if err := r.rig.Store.Pool.QueryRow(context.Background(), `select revoked_at is null from person_signins where id = $1::uuid`, id).Scan(&live); err != nil {
		t.Fatal(err)
	}
	return live
}

// ageClaims puts every claim an hour back, so the next pass may claim the address again at once.
func (r *recoveryRig) ageClaims(t *testing.T) {
	t.Helper()
	if _, err := r.dispatch.Pool.Exec(context.Background(), `update broker_revocations set tried_at = tried_at - interval '1 hour' where tried_at is not null`); err != nil {
		t.Fatal(err)
	}
}

// TestABrokerRestoredAloneIsRecoveredByTellingItAgainWhatWasSettledSince: the broker's database,
// restored alone to a moment before revocations Dispatch paid, holds those replaced accounts'
// credentials live again and no record refusing them. Nothing at Dispatch sees it: each
// revocation is settled, so the loop claims none and the owed gauges read zero. The runbook's
// other block records again every revocation settled at or after the broker's backup less an
// hour, and the loop then tells the broker each: it refuses the replaced account again and ends
// what it holds.
//
// Two addresses. At lostbroker@, account-b replaced account-a. At takeback@, account-t2 replaced
// account-t1 and then account-t1 took the address back, so both of its revocations were settled
// after the backup, and account-t1 holds it again: the block records account-t2's revocation
// again, naming account-t1 as its holder, and leaves account-t1's own alone, since the broker must
// not be told that the holder was replaced. account-t1 stays able to trade and account-t2 is
// refused.
//
// The moment the test gives the block is two minutes after the settlements, as an operator's is
// when the backup's record rounds it to the minute, names the backup's end, or comes from a clock
// ahead of Dispatch's. The hour's margin is what reaches back over that; the rows would otherwise
// be left out and the accounts would keep their credentials with nothing showing it.
//
// Control, in the test: before the block, a pass changes nothing and the owed count is zero - the
// state the recovery exists for. Control in the runbook, a break restored by checksum: the block
// without its exclusion of a revocation whose account holds its address again fails, since it
// would record that account as its own holder's replaced account.
func TestABrokerRestoredAloneIsRecoveredByTellingItAgainWhatWasSettledSince(t *testing.T) {
	ctx := context.Background()
	r := newRecoveryRig(t)
	const email, takeback = "lostbroker@example.com", "takeback@example.com"
	signIn := func(email, account string) {
		t.Helper()
		if err := r.people.SignInSubject(ctx, email, account, "refresh-"+account, model.MemberFull); err != nil {
			t.Fatalf("the sign-in of %s at %s: %v", account, email, err)
		}
	}
	pay := func() {
		t.Helper()
		r.ageClaims(t)
		r.served.revokeOwedBrokerCredentials(ctx)
	}
	signIn(email, "account-a")
	machine := r.signInMachine(t, email, "account-a", strings.Repeat("a", 43))
	signIn(email, "account-b")
	signIn(takeback, "account-t1")
	signIn(takeback, "account-t2")
	pay()
	t2Machine := r.signInMachine(t, takeback, "account-t2", strings.Repeat("t", 43))
	signIn(takeback, "account-t1")
	pay()
	for _, address := range []string{email, takeback} {
		if owing, err := r.revocations.Owing(ctx, address); err != nil || len(owing) != 0 {
			t.Fatalf("after the passes, %s owes %+v, %v; want every revocation settled", address, owing, err)
		}
	}
	if !r.refused(t, email, "account-a") || r.machineSignInLive(t, machine) || !r.refused(t, takeback, "account-t2") || r.refused(t, takeback, "account-t1") || r.machineSignInLive(t, t2Machine) {
		t.Fatalf("after the passes the broker refuses account-a %t (its sign-in live %t), account-t2 %t (its sign-in live %t) and account-t1 %t; want account-a and account-t2 refused and ended, account-t1 not refused",
			r.refused(t, email, "account-a"), r.machineSignInLive(t, machine), r.refused(t, takeback, "account-t2"), r.machineSignInLive(t, t2Machine), r.refused(t, takeback, "account-t1"))
	}
	// The moment the operator gives the block, as the backup's own record writes it: after the
	// settlements rather than before them, by rounding or by the other server's clock. Only the
	// hour's margin in the block reaches back over it.
	backup := time.Now().Add(2 * time.Minute)

	// The broker's database is restored to the backup: its record of the replacements is gone and
	// the replaced accounts' sign-ins are live again. A restore writes rows the guards refuse, so
	// they are off for these statements.
	for _, statement := range []string{
		`alter table replaced_person_subjects disable trigger replaced_person_subjects_guarded`,
		`alter table person_replacement_generations disable trigger person_replacement_generations_guarded`,
		`delete from replaced_person_subjects where person in ('` + email + `', '` + takeback + `')`,
		`delete from person_replacement_generations where person in ('` + email + `', '` + takeback + `')`,
		`update person_signins set revoked_at = null where id in ('` + machine + `'::uuid, '` + t2Machine + `'::uuid)`,
		`alter table replaced_person_subjects enable trigger replaced_person_subjects_guarded`,
		`alter table person_replacement_generations enable trigger person_replacement_generations_guarded`,
	} {
		if _, err := r.rig.Store.Pool.Exec(ctx, statement); err != nil {
			t.Fatalf("restore the broker to the backup, %q: %v", statement, err)
		}
	}

	count, oldest, err := r.revocations.Owed(ctx)
	if err != nil || count != 0 || oldest != 0 {
		t.Fatalf("control: after the broker's restore Dispatch owes %d (oldest %s), %v; want nothing owed, which is why the gauges cannot see it", count, oldest, err)
	}
	pay()
	if r.refused(t, email, "account-a") || !r.machineSignInLive(t, machine) || r.refused(t, takeback, "account-t2") {
		t.Fatalf("control: a pass alone recovered the broker (refuses account-a %t, its sign-in live %t, refuses account-t2 %t); want it to change nothing",
			r.refused(t, email, "account-a"), r.machineSignInLive(t, machine), r.refused(t, takeback, "account-t2"))
	}

	block := runbookBlockAfter(t, brokerRestoredLead)
	runStatements(t, r.dispatch.Pool, block.statements, pgx.NamedArgs{"backup": backup})
	pay()
	for _, ended := range []struct {
		email, account, signIn string
	}{{email, "account-a", machine}, {takeback, "account-t2", t2Machine}} {
		if !r.refused(t, ended.email, ended.account) {
			t.Errorf("after the runbook's recovery of a broker restored alone, the broker does not refuse %s at %s; want it refused again", ended.account, ended.email)
		}
		if r.machineSignInLive(t, ended.signIn) {
			t.Errorf("after the runbook's recovery of a broker restored alone, %s's sign-in on a machine at %s is live; want it ended again", ended.account, ended.email)
		}
	}
	if r.refused(t, takeback, "account-t1") {
		t.Errorf("after the recovery the broker refuses account-t1, which holds %s again; want it to trade", takeback)
	}
	for _, address := range []string{email, takeback} {
		if owing, err := r.revocations.Owing(ctx, address); err != nil || len(owing) != 0 {
			t.Errorf("after the recovery and a pass, %s owes %+v, %v; want each revocation settled again", address, owing, err)
		}
	}
}

// TestABrokerRestoredBeforeTheResetIsRecoveredByRepeatingTheResetCredentialBlocks covers two
// restores in sequence. Restored Dispatch still records A, because B's replacement was owed when
// Dispatch's database was lost. The reset ends A's broker sign-in, but a broker backup taken before
// the reset brings it back. No revocation names A yet, so record-again has nothing to write. The
// runbook therefore runs the reset's credentials and directory blocks again after record-again:
// A's sign-in is ended before Dispatch starts, then B's sign-in pays the revocation that refuses A.
//
// Control in the runbook: without that second credentials block, the restored broker leaves A's
// sign-in live before Dispatch starts.
func TestABrokerRestoredBeforeTheResetIsRecoveredByRepeatingTheResetCredentialBlocks(t *testing.T) {
	ctx := context.Background()
	runbook := strings.Join(runbookLines(t), "\n")
	repeatResetBlocks := strings.Contains(runbook, "run **End every person's credential at the broker** and **Clear a\nrefusal only for the directory's holder** again")
	for _, want := range []string{
		`select to_timestamp(generation / 1000000.0) from session_generation_floors where login = '*'`,
		"run **End every person's credential at the broker** and **Clear a\nrefusal only for the directory's holder** again",
		"If the deployment floor row `*`\nexists, a reset ran before this broker was made again",
	} {
		if !strings.Contains(runbook, want) {
			t.Errorf("the broker-alone recovery is missing %q", want)
		}
	}
	r := newRecoveryRig(t)
	const email = "reset-then-broker@example.com"
	if err := r.people.SignInSubject(ctx, email, "a-reset", "refresh-a", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	signIn := r.signInMachine(t, email, "a-reset", strings.Repeat("z", 43))
	backup := time.Now()

	r.resetSessions(t)
	if left := r.endBrokerCredentials(t); len(left) != 0 {
		t.Fatalf("STILL LIVE after the reset's credentials block: %v", left)
	}
	if r.machineSignInLive(t, signIn) {
		t.Fatal("the reset left A's sign-in live")
	}

	// Restore the broker to the backup before the reset: its record of B replacing A remains, and
	// A's sign-in the reset ended is live again.
	if _, err := r.rig.Store.Pool.Exec(ctx, `update person_signins set revoked_at = null where id = $1::uuid`, signIn); err != nil {
		t.Fatalf("restore A's broker sign-in from before the reset: %v", err)
	}
	if !r.machineSignInLive(t, signIn) {
		t.Fatalf("setup: broker restored before the reset leaves A's sign-in live %t; want live", r.machineSignInLive(t, signIn))
	}
	block := runbookBlockAfter(t, brokerRestoredLead)
	runStatements(t, r.dispatch.Pool, block.statements, pgx.NamedArgs{"backup": backup})
	if owing, err := r.revocations.Owing(ctx, email); err != nil || len(owing) != 0 {
		t.Fatalf("the broker-alone record-again with no revocation naming A owes %+v, %v; want nothing", owing, err)
	}
	if repeatResetBlocks {
		if left := r.endBrokerCredentials(t); len(left) != 0 {
			t.Fatalf("STILL LIVE after the reset's second credentials block: %v", left)
		}
		output, err := r.clearHoldersRefusals(t, map[string]directoryAnswer{
			email: {User: directoryUser{ID: "b-reset", PrimaryEmail: email}},
		})
		if err != nil {
			t.Fatalf("the reset's second directory block: %v\n%s", err, output)
		}
	}
	if r.machineSignInLive(t, signIn) {
		t.Errorf("after the broker-alone recovery and the reset blocks again, A's sign-in is live; want ended before Dispatch starts")
	}
	if err := r.people.SignInSubject(ctx, email, "b-reset", "refresh-b", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	r.ageClaims(t)
	r.served.revokeOwedBrokerCredentials(ctx)
	if r.machineSignInLive(t, signIn) || !r.refused(t, email, "a-reset") {
		t.Errorf("after B signs in and Dispatch pays the revocation, A's sign-in is live %t and the broker refuses A %t; want ended and refused", r.machineSignInLive(t, signIn), r.refused(t, email, "a-reset"))
	}
}

// TestABrokerDatabaseMadeAgainIsToldEveryRevocationSettled: a broker database made again, rather
// than restored, has forgotten every refusal and every replacement, and has no backup moment to
// give the block. The runbook says to give it `-infinity`, which records again every revocation
// Dispatch ever settled, whenever it was; the loop then tells the broker each, and the accounts
// those revocations named are refused again.
//
// Control, in the test: the same empty broker with a restore's own kind of moment, now(), records
// nothing again, since every settlement here is older.
func TestABrokerDatabaseMadeAgainIsToldEveryRevocationSettled(t *testing.T) {
	ctx := context.Background()
	r := newRecoveryRig(t)
	const email = "madeagain@example.com"
	if err := r.people.SignInSubject(ctx, email, "account-a", "refresh-a", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	machine := r.signInMachine(t, email, "account-a", strings.Repeat("m", 43))
	if err := r.people.SignInSubject(ctx, email, "account-b", "refresh-b", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	r.served.revokeOwedBrokerCredentials(ctx)
	if !r.refused(t, email, "account-a") {
		t.Fatalf("after the pass the broker does not refuse account-a at %s", email)
	}
	// The broker's database is made again: every row of its record of accounts is gone, and the
	// credential it held is back. A database made again is empty, so the guards are off for the
	// statements that stand for it here.
	for _, statement := range []string{
		`alter table replaced_person_subjects disable trigger replaced_person_subjects_guarded`,
		`alter table person_replacement_generations disable trigger person_replacement_generations_guarded`,
		`delete from replaced_person_subjects`,
		`delete from person_replacement_generations`,
		`update person_signins set revoked_at = null where id = '` + machine + `'::uuid`,
		`alter table replaced_person_subjects enable trigger replaced_person_subjects_guarded`,
		`alter table person_replacement_generations enable trigger person_replacement_generations_guarded`,
	} {
		if _, err := r.rig.Store.Pool.Exec(ctx, statement); err != nil {
			t.Fatalf("make the broker's database again, %q: %v", statement, err)
		}
	}
	// The settlement is three days older than the moment a restore would be given, so that
	// moment records nothing again: this is the case the page sends to -infinity. The guard
	// leaves done_at alone on a settled row only while it does not move, so it is off for this.
	for _, statement := range []string{
		`alter table broker_revocations disable trigger broker_revocations_binding_columns`,
		`update broker_revocations set done_at = now() - interval '3 days' where email = '` + email + `' and done_at is not null`,
		`alter table broker_revocations enable trigger broker_revocations_binding_columns`,
	} {
		if _, err := r.dispatch.Pool.Exec(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	block := runbookBlockAfter(t, brokerRestoredLead)
	runStatements(t, r.dispatch.Pool, block.statements, pgx.NamedArgs{"backup": time.Now()})
	if owing, err := r.revocations.Owing(ctx, email); err != nil || len(owing) != 0 {
		t.Fatalf("control: with a restore's own moment the block recorded %+v again, %v; want nothing, which is why a database made again is given -infinity", owing, err)
	}
	if paragraph := runbookParagraph(t, madeAgainLead); !strings.Contains(paragraph, "Give the block `-infinity`") {
		t.Fatalf("the runbook's paragraph for a broker database made again reads %q; want it to give -infinity for the backup's moment", paragraph)
	}
	runStatements(t, r.dispatch.Pool, block.statements, pgx.NamedArgs{"backup": "-infinity"})
	r.ageClaims(t)
	r.served.revokeOwedBrokerCredentials(ctx)
	if !r.refused(t, email, "account-a") {
		t.Errorf("after the block with -infinity and a pass, the broker does not refuse account-a at %s; want every settled revocation told again", email)
	}
	if r.machineSignInLive(t, machine) {
		t.Errorf("after the block with -infinity and a pass, account-a's sign-in on a machine at %s is live; want it ended again", email)
	}
}

// TestTheBrokerRestoredBlockHoldsEachAddressesSessionRow: the block reads each address's session
// generation in a subquery and the guard on broker_revocations reads it again, in a VOLATILE
// plpgsql function with a snapshot of its own. A sign-out that commits between the two moves the
// generation, and the guard refuses the whole statement with 23514, so one sign-out anywhere in
// the set would throw away the recovery of every address in it. The block locks each of those
// session rows first (select ... for update), as the single-row blocks do, so such a sign-out
// waits for the commit instead.
//
// Control: drop the `for update` from the runbook's lock statement, and the sign-out no longer
// waits.
func TestTheBrokerRestoredBlockHoldsEachAddressesSessionRow(t *testing.T) {
	ctx := context.Background()
	r := newRecoveryRig(t)
	const email = "lockedrows@example.com"
	if err := r.people.SignInSubject(ctx, email, "account-a", "refresh-a", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	if err := r.people.SignInSubject(ctx, email, "account-b", "refresh-b", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	r.served.revokeOwedBrokerCredentials(ctx)
	if owing, err := r.revocations.Owing(ctx, email); err != nil || len(owing) != 0 {
		t.Fatalf("after the pass, %s owes %+v, %v; want the revocation settled", email, owing, err)
	}
	block := runbookBlockAfter(t, brokerRestoredLead)
	if len(block.statements) != 4 || block.statements[0] != "begin" || block.statements[3] != "commit" ||
		!strings.HasPrefix(block.statements[1], "select u.generation from user_sessions u ") {
		t.Fatalf("the runbook's block for a broker restored alone is %q; want begin, a read of the addresses' session rows, the record-again and commit", block.statements)
	}
	tx, err := r.dispatch.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	args := pgx.NamedArgs{"backup": time.Now().Add(-time.Hour)}
	if _, err := tx.Exec(ctx, block.statements[1], args); err != nil {
		t.Fatalf("the block's lock of the addresses' session rows: %v", err)
	}
	// A sign-out at one of those addresses, from another connection: it must wait for the commit.
	signedOut := make(chan error, 1)
	go func() {
		signedOut <- store.NewPgSessionStore(r.dispatch.Pool).RevokeSessions(context.Background(), email)
	}()
	select {
	case err := <-signedOut:
		t.Fatalf("the sign-out at %s finished (%v) while the block held its session row; want it waiting for the commit", email, err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := tx.Exec(ctx, block.statements[2], args); err != nil {
		t.Fatalf("the block's record-again: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("the block's commit: %v", err)
	}
	select {
	case err := <-signedOut:
		if err != nil {
			t.Errorf("the sign-out at %s after the commit: %v; want it to go through", email, err)
		}
	case <-time.After(10 * time.Second):
		t.Errorf("the sign-out at %s did not finish within 10 s of the commit", email)
	}
	if owing, err := r.revocations.Owing(ctx, email); err != nil || len(owing) != 1 {
		t.Errorf("after the block, %s owes %+v, %v; want the revocation recorded again", email, owing, err)
	}
}
