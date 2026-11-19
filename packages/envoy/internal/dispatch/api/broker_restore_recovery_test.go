package api

import (
	"context"
	"crypto/sha256"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/broker/brokertest"
	brokerstore "github.com/sjawhar/envoy/internal/broker/store"
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

// clear settles what owed names without the broker, as `envoy-dispatch clear-broker-revocation`
// does, naming each revocation as the operator's read printed it.
func (r *recoveryRig) clear(t *testing.T, email string, owed ...store.OwedBrokerRevocation) {
	t.Helper()
	var seen []store.BrokerRevocationSeen
	for _, revocation := range owed {
		seen = append(seen, store.BrokerRevocationSeen{Subject: revocation.Subject, Generation: revocation.Generation})
	}
	if cleared, err := r.revocations.Clear(context.Background(), email, seen); err != nil || len(cleared) != len(seen) {
		t.Fatalf("clear-broker-revocation %s %+v: %+v, %v", email, seen, cleared, err)
	}
}

// runRecovery runs the runbook's recovery, block by block, over every address the sweep lists.
// directory stands for the Workspace directory the runbook reads where the broker refuses the
// account the address records: the Google account that holds the address now, which no test can
// ask Google for.
func (r *recoveryRig) runRecovery(t *testing.T, directory map[string]string) []listedAddress {
	t.Helper()
	ctx := context.Background()
	_, unknown, listed := runbookSweep(t).run(t, r.rig.Store, r.dispatch)
	if unknown {
		t.Fatal("the sweep reported UNKNOWN: the broker holds no recorded generation, so the recovery has nothing to run over")
	}
	generationRead := runbookBlockAfter(t, brokerGenerationLead)
	refusedRead := runbookBlockAfter(t, brokerRefusedLead)
	liveRead := runbookBlockAfter(t, brokerLiveLead)
	holderRead := runbookBlockAfter(t, dispatchHolderLead)
	raise := runbookBlockAfter(t, raiseLead)
	writeBack := runbookBlockAfter(t, writeBackLead)
	staleBranch := runbookBlockAfter(t, staleRowLead)
	// The two branches are paragraphs of instructions; reading them here holds the test to what
	// the page says rather than to what it remembers.
	holdsIt := runbookParagraph(t, holderHoldsItLead)
	stale := runbookParagraph(t, staleRowLead)
	if !strings.Contains(holdsIt, "record a revocation of every other account") || !strings.Contains(stale, "Record no revocation of the address yet") {
		t.Fatalf("the runbook's two branches read %q and %q; want the first to record a revocation of every other account and the second to record none", holdsIt, stale)
	}
	if directoryStep := runbookParagraph(t, directoryLead); !strings.Contains(directoryStep, "Workspace directory") {
		t.Fatalf("the runbook's step after the raise reads %q; want it to read which account holds the address in the Workspace directory", directoryStep)
	}
	listsOwed := slices.ContainsFunc(holderRead.commands, func(command string) bool {
		return strings.HasPrefix(command, "envoy-dispatch clear-broker-revocation ")
	})
	staleClears := slices.ContainsFunc(staleBranch.commands, func(command string) bool {
		return strings.HasPrefix(command, "envoy-dispatch clear-broker-revocation ")
	})
	if !listsOwed {
		t.Fatal("the runbook's read of the address's account does not list what it owes")
	}
	for _, address := range listed {
		email := address.person
		args := pgx.NamedArgs{"person": email, "email": email, "issuer": recoveryIssuer}
		generations := queryRunbook(t, r.rig.Store.Pool, generationRead, args)
		if len(generations) != 1 {
			t.Fatalf("the runbook's read of the broker's generation at %s answered %q", email, generations)
		}
		// psql passes -v broker_generation='<n>' as a literal the statement casts to bigint; pgx
		// sends the number itself.
		generation, err := strconv.ParseInt(generations[0], 10, 64)
		if err != nil {
			t.Fatalf("the broker's generation at %s, %q: %v", email, generations[0], err)
		}
		args["broker_generation"] = generation
		refusedAccounts := queryRunbook(t, r.rig.Store.Pool, refusedRead, args)
		liveAccounts := queryRunbook(t, r.rig.Store.Pool, liveRead, args)
		holder := queryRunbook(t, r.dispatch.Pool, holderRead, args)
		owing, err := r.revocations.Owing(ctx, email)
		if err != nil {
			t.Fatal(err)
		}
		runStatements(t, r.dispatch.Pool, raise.statements, args)

		recorded := holder != nil && len(holder) == 1
		if recorded && slices.Contains(refusedAccounts, holder[0]) && directory[email] != holder[0] {
			// Another account holds the address: Dispatch's row is stale. Revoke the address's
			// tokens and settle what the page says to settle, reading the list the command prints
			// (store.OwedBrokerRevocation.Listed) rather than the field behind it, which an
			// operator cannot see: every revocation, or, where the page says to leave the one
			// printed "session generation none" owed, every other one.
			runStatements(t, r.dispatch.Pool, staleBranch.statements, args)
			if staleClears {
				leaveNullOwed := strings.Contains(strings.Join(strings.Fields(stale), " "),
					"Leave the one the list prints `session generation none` owed")
				var settle []store.OwedBrokerRevocation
				for _, revocation := range owing {
					if leaveNullOwed && strings.Contains(revocation.Listed(), "session generation none") {
						continue
					}
					settle = append(settle, revocation)
				}
				if len(settle) > 0 {
					r.clear(t, email, settle...)
				}
			}
			continue
		}
		// The account the address records holds it: settle what the broker already applied, and
		// record a revocation of every other account, each the live read listed among them.
		told := map[string]bool{}
		for _, revocation := range owing {
			if slices.Contains(refusedAccounts, revocation.Subject) {
				r.clear(t, email, revocation)
				continue
			}
			args["subject"] = revocation.Subject
			runStatements(t, r.dispatch.Pool, writeBack.statements, args)
			told[revocation.Subject] = true
		}
		for _, account := range liveAccounts {
			if recorded && account == holder[0] || told[account] {
				continue
			}
			args["subject"] = account
			runStatements(t, r.dispatch.Pool, writeBack.statements, args)
			told[account] = true
		}
		if len(told) == 0 && recorded && slices.Contains(refusedAccounts, holder[0]) {
			t.Fatalf("at %s the recovery recorded nothing and the broker still refuses %s; the runbook's broker clear is the step here, and no address of this test reaches it", email, holder[0])
		}
	}
	return listed
}

// TestARestoredStaleHolderStaysRefusedAtTheBroker runs the runbook's recovery after a Dispatch
// database restored or made again alone, every block read from the runbook, against Dispatch's
// database and a real broker's (brokertest.NewRig) and Dispatch's own revocation pass, at three
// addresses whose histories need the recovery's two branches and the directory read that chooses
// between them:
//
//   - stale@: H0 takes the address from A, so revocation X (A, holder H0) is owed. After the backup
//     the broker applied X and H1 replaced H0 at X's generation plus one, so the broker refuses A
//     and H0; Dispatch, restored, records H0 again, owes X, and holds a personal token the restore
//     brought back. The broker refuses the account Dispatch records and the directory names H1, so
//     the row is stale: the runbook's stale branch revokes the token and settles X, recording
//     nothing again, and the broker still refuses H0.
//   - recreated@: the broker recorded a replacement at generation 5 under a Dispatch database since
//     made again, and the new one owes Y (A, holder H0) at its own, lower, generation. The broker
//     refuses neither A nor H0 at the address, so the other branch records Y again, and the pass
//     has the broker apply it: A is refused.
//   - takeback@: the backup holds H0. After it, H1 replaced H0 (the broker applied that revocation,
//     so it refuses H0) and then H0 took the address back, whose revocation of H1 the lost database
//     still owed. Dispatch, restored, records H0 and knows nothing of H1, and the broker holds a
//     live sign-in of H1 on a machine. The broker refuses the account Dispatch records, but the
//     directory names that same account, so its refusal is the stale side: the recovery records a
//     revocation of every other account the broker still serves — H1 — and the pass has the broker
//     end H1's credentials and stop refusing H0, which can sign in and be approved again. H0's
//     personal tokens are left alone.
//
// Each pass before the recovery is answered superseded, as the runbook's ERROR describes, and the
// sweep lists all three addresses.
//
// stale@ also owes a revocation an earlier head of this branch recorded with no session
// generation, which the broker refuses on every call: the stale branch leaves it owed, since
// settling it would tell the broker nothing of an account nothing applied.
//
// Controls: put the old recovery (the raise, then recording the revocation again) in the runbook's
// stale branch, and H0 is no longer refused at stale@; have the live read list nothing, and
// takeback@ keeps H1 live and H0 refused; drop the stale branch's sentence that leaves a row with
// no generation owed, and that row is settled.
func TestARestoredStaleHolderStaysRefusedAtTheBroker(t *testing.T) {
	ctx := context.Background()
	r := newRecoveryRig(t)
	const stale, recreated, takeback = "stale@example.com", "recreated@example.com", "takeback@example.com"

	signIn := func(t *testing.T, email string, subjects ...string) int64 {
		t.Helper()
		for _, subject := range subjects {
			if err := r.people.SignInSubject(ctx, email, subject, "refresh-"+subject, model.MemberFull); err != nil {
				t.Fatalf("the sign-in of %s at %s: %v", subject, email, err)
			}
		}
		owing, err := r.revocations.Owing(ctx, email)
		if err != nil || len(owing) != 1 || owing[0].SessionGeneration == nil {
			t.Fatalf("after the sign-ins at %s Dispatch owes %+v, %v; want one revocation with a generation", email, owing, err)
		}
		return *owing[0].SessionGeneration
	}

	staleAt := signIn(t, stale, "a-stale", "h0-stale")
	r.revoke(t, stale, "a-stale", "h0-stale", staleAt)
	r.revoke(t, stale, "h0-stale", "h1-stale", staleAt+1)
	hash := sha256.Sum256([]byte(stale + "/restored"))
	var staleToken string
	if err := r.dispatch.Pool.QueryRow(ctx, `insert into agent_tokens (owner, name, token_hash, prefix, scopes, subject)
		values ($1, 'restored', $2, $3, $4, 'h0-stale') returning id::text`,
		stale, hash[:], strings.Repeat("r", agentTokenPrefixLength), []string{model.ScopePerson}).Scan(&staleToken); err != nil {
		t.Fatalf("the personal token the restore brought back: %v", err)
	}
	// A revocation an earlier head of this branch recorded holds no session generation (written
	// with the guard off, as such a head wrote it): the broker refuses it on every call, so nothing
	// applied it, and the stale branch must leave it owed rather than settle it unannounced.
	for _, statement := range []string{
		`alter table broker_revocations disable trigger broker_revocations_binding_columns`,
		`insert into broker_revocations (email, issuer, subject, holder_subject) values ('` + stale + `', '` + recoveryIssuer + `', 'c-stale', 'h0-stale')`,
		`alter table broker_revocations enable trigger broker_revocations_binding_columns`,
	} {
		if _, err := r.dispatch.Pool.Exec(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}

	r.revoke(t, recreated, "old-recreated", "a-recreated", 5)
	if recreatedAt := signIn(t, recreated, "a-recreated", "h0-recreated"); recreatedAt >= 5 {
		t.Fatalf("recreated@'s generation at Dispatch is %d, want below the broker's 5", recreatedAt)
	}

	// takeback@: H0 holds the address at Dispatch, which is where the restore leaves it; the
	// broker went on to apply H1's replacement of H0 and holds H1's sign-in on a machine.
	if err := r.people.SignInSubject(ctx, takeback, "h0-takeback", "refresh-h0", model.MemberFull); err != nil {
		t.Fatalf("the first sign-in at %s: %v", takeback, err)
	}
	takebackAt, found, err := store.NewPgSessionStore(r.dispatch.Pool).CurrentSessionGeneration(ctx, takeback)
	if err != nil || !found {
		t.Fatalf("read %s's generation: %d, %t, %v", takeback, takebackAt, found, err)
	}
	r.revoke(t, takeback, "h0-takeback", "h1-takeback", takebackAt+1)
	h1SignIn := r.signInMachine(t, takeback, "h1-takeback", strings.Repeat("h", 43))
	h0Token := sha256.Sum256([]byte(takeback + "/h0"))
	var takebackToken string
	if err := r.dispatch.Pool.QueryRow(ctx, `insert into agent_tokens (owner, name, token_hash, prefix, scopes, subject)
		values ($1, 'h0 of the holder', $2, $3, $4, 'h0-takeback') returning id::text`,
		takeback, h0Token[:], strings.Repeat("k", agentTokenPrefixLength), []string{model.ScopePerson}).Scan(&takebackToken); err != nil {
		t.Fatalf("the holder's personal token: %v", err)
	}

	r.served.revokeOwedBrokerCredentials(ctx)
	for email, want := range map[string]int{stale: 2, recreated: 1} {
		if owing, err := r.revocations.Owing(ctx, email); err != nil || len(owing) != want {
			t.Fatalf("after a pass the broker answered superseded, %s owes %+v, %v; want its %d revocations still owed", email, owing, err, want)
		}
	}
	if r.refused(t, recreated, "a-recreated") {
		t.Fatal("control: before the recovery the broker already refuses A at recreated@; the test cannot see the record-again")
	}
	if !r.refused(t, takeback, "h0-takeback") || !r.machineSignInLive(t, h1SignIn) {
		t.Fatalf("control: before the recovery the broker refuses H0 at takeback@ %t and H1's sign-in is live %t; want both, or the test shows nothing",
			r.refused(t, takeback, "h0-takeback"), r.machineSignInLive(t, h1SignIn))
	}

	listed := r.runRecovery(t, map[string]string{
		// The Workspace directory's answer: who holds each address now.
		stale:    "h1-stale",
		takeback: "h0-takeback",
	})
	if len(listed) != 3 {
		t.Fatalf("the sweep listed %+v, want recreated@, stale@ and takeback@", listed)
	}
	r.ageClaims(t)
	r.served.revokeOwedBrokerCredentials(ctx)

	if !r.refused(t, stale, "h0-stale") || !r.refused(t, stale, "a-stale") {
		t.Errorf("after the runbook's recovery of the stale row at %s, the broker refuses H0 %t and A %t; want both", stale, r.refused(t, stale, "h0-stale"), r.refused(t, stale, "a-stale"))
	}
	var revoked bool
	if err := r.dispatch.Pool.QueryRow(ctx, `select revoked_at is not null from agent_tokens where id = $1::uuid`, staleToken).Scan(&revoked); err != nil || !revoked {
		t.Errorf("after the stale branch, the personal token the restore brought back is revoked %t, %v; want revoked", revoked, err)
	}
	if !r.refused(t, recreated, "a-recreated") {
		t.Errorf("after the runbook's raise and record-again at %s, the broker does not refuse A; want it refused", recreated)
	}
	if r.refused(t, takeback, "h0-takeback") || !r.refused(t, takeback, "h1-takeback") {
		t.Errorf("after the recovery at %s, the broker refuses the holder H0 %t and H1 %t; want H0 admitted and H1 refused",
			takeback, r.refused(t, takeback, "h0-takeback"), r.refused(t, takeback, "h1-takeback"))
	}
	if r.machineSignInLive(t, h1SignIn) {
		t.Errorf("after the recovery at %s, H1's sign-in on a machine is still live; want it ended", takeback)
	}
	if err := r.dispatch.Pool.QueryRow(ctx, `select revoked_at is not null from agent_tokens where id = $1::uuid`, takebackToken).Scan(&revoked); err != nil || revoked {
		t.Errorf("after the recovery at %s, the holder's own personal token is revoked %t, %v; want it left alone", takeback, revoked, err)
	}
	// The holder signs in again, which records no replacement, since the address records its
	// account already; nothing of the recovery undoes that.
	if err := r.people.SignInSubject(ctx, takeback, "h0-takeback", "refresh-h0-again", model.MemberFull); err != nil {
		t.Errorf("the holder's next sign-in at %s: %v", takeback, err)
	}
	if owing, err := r.revocations.Owing(ctx, stale); err != nil || len(owing) != 1 || owing[0].Subject != "c-stale" || owing[0].SessionGeneration != nil {
		t.Errorf("after the stale branch and a pass, %s owes %+v, %v; want the revocation with no session generation alone, left owed, since nothing applied it", stale, owing, err)
	}
	for _, email := range []string{recreated, takeback} {
		if owing, err := r.revocations.Owing(ctx, email); err != nil || len(owing) != 0 {
			t.Errorf("after the recovery and a pass, %s owes %+v, %v; want nothing", email, owing, err)
		}
	}
}

// TestAnAddressWithNoSessionRowStartsAtTheBrokersGeneration: a Dispatch database made again holds
// no session row for an address that has not signed in since, so the sweep lists it at generation
// 0 and the raise matches no row — the row cannot be made by hand, since user_sessions_login_fkey
// needs a people row and only a sign-in writes one. The raise's second statement writes the
// address's floor instead (session_generation_floors, migration 20261119060414), where the first
// session row of the address starts: the sweep stops listing the address, the first sign-in makes
// the row above the broker's generation, and the replacement after it records a revocation the
// broker applies rather than supersedes.
//
// Control, in the test: before the raise, the sweep lists the address; and the broker's answer to
// the pass is read, so a superseded answer (what a first sign-in at generation 1 would get) fails
// the test.
func TestAnAddressWithNoSessionRowStartsAtTheBrokersGeneration(t *testing.T) {
	ctx := context.Background()
	r := newRecoveryRig(t)
	const email = "norow@example.com"
	const brokerGeneration = 6
	tx, err := r.rig.Store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brokerstore.RecordReplacedSubjectTx(ctx, tx, email, recoveryIssuer, "old-norow", "new-norow", brokerGeneration); err != nil {
		t.Fatalf("the replacement the broker applied before the database was made again: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	swept := runbookSweep(t)
	_, unknown, listed := swept.run(t, r.rig.Store, r.dispatch)
	if unknown || len(listed) != 1 || listed[0].person != email || listed[0].dispatchAt != 0 {
		t.Fatalf("control: the sweep listed %+v (UNKNOWN %t), want %s alone at Dispatch generation 0", listed, unknown, email)
	}

	raise := runbookBlockAfter(t, raiseLead)
	runStatements(t, r.dispatch.Pool, raise.statements, pgx.NamedArgs{"email": email, "broker_generation": int64(brokerGeneration)})
	if _, _, listed := swept.run(t, r.rig.Store, r.dispatch); len(listed) != 0 {
		t.Errorf("after the raise the sweep still lists %+v, want nothing: the address's floor is at the broker's generation", listed)
	}

	if err := r.people.SignInSubject(ctx, email, "first-norow", "refresh-first", model.MemberFull); err != nil {
		t.Fatalf("the first sign-in at %s: %v", email, err)
	}
	generation, found, err := store.NewPgSessionStore(r.dispatch.Pool).CurrentSessionGeneration(ctx, email)
	if err != nil || !found || generation < brokerGeneration {
		t.Fatalf("after the first sign-in, %s's session generation is %d (found %t, %v); want at least the broker's %d", email, generation, found, err, brokerGeneration)
	}
	if err := r.people.SignInSubject(ctx, email, "second-norow", "refresh-second", model.MemberFull); err != nil {
		t.Fatalf("the replacing sign-in at %s: %v", email, err)
	}
	r.served.revokeOwedBrokerCredentials(ctx)
	if owing, err := r.revocations.Owing(ctx, email); err != nil || len(owing) != 0 {
		t.Errorf("after a pass, %s owes %+v, %v; want nothing: the broker applied the revocation rather than answering it superseded", email, owing, err)
	}
	if !r.refused(t, email, "first-norow") {
		t.Errorf("after a pass, the broker does not refuse the account the replacement replaced at %s; want it refused", email)
	}
}

// TestABrokerRestoredAloneIsRecoveredByTellingItAgainWhatWasSettledSince: the broker's database,
// restored alone to a moment before a revocation Dispatch paid, holds that replaced account's
// credentials live again and no record refusing it. Nothing at Dispatch sees it: the revocation is
// settled, so the loop claims none, the owed gauges read zero and the sweep lists no address,
// since Dispatch's generation is above the restored broker's. The runbook's other block records
// again every revocation settled at or after the broker's backup less an hour, and the loop then
// tells the broker each: it refuses the replaced account again and ends what it holds.
//
// The moment the test gives the block is two minutes after the settlement, as an operator's is
// when the backup's record rounds it to the minute, names the backup's end, or comes from a clock
// ahead of Dispatch's. The hour's margin is what reaches back over that; the row would otherwise
// be left out and the account would keep its credentials with nothing showing it.
//
// Control, in the test: before the block, a pass changes nothing, the owed count is zero and the
// sweep lists nothing — the state the recovery exists for.
func TestABrokerRestoredAloneIsRecoveredByTellingItAgainWhatWasSettledSince(t *testing.T) {
	ctx := context.Background()
	r := newRecoveryRig(t)
	const email = "lostbroker@example.com"
	if err := r.people.SignInSubject(ctx, email, "account-a", "refresh-a", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	machine := r.signInMachine(t, email, "account-a", strings.Repeat("a", 43))
	if err := r.people.SignInSubject(ctx, email, "account-b", "refresh-b", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	r.served.revokeOwedBrokerCredentials(ctx)
	if owing, err := r.revocations.Owing(ctx, email); err != nil || len(owing) != 0 {
		t.Fatalf("after the pass, %s owes %+v, %v; want the revocation settled", email, owing, err)
	}
	if !r.refused(t, email, "account-a") || r.machineSignInLive(t, machine) {
		t.Fatalf("after the pass the broker refuses account-a %t and its sign-in is live %t; want it refused and ended", r.refused(t, email, "account-a"), r.machineSignInLive(t, machine))
	}
	// The moment the operator gives the block, as the backup's own record writes it: after the
	// settlement rather than before it, by rounding or by the other server's clock. Only the
	// hour's margin in the block reaches back over it.
	backup := time.Now().Add(2 * time.Minute)

	// The broker's database is restored to the backup: its record of the replacement is gone and
	// account-a's sign-in is live again. A restore writes rows the guards refuse, so they are off
	// for these statements.
	for _, statement := range []string{
		`alter table replaced_person_subjects disable trigger replaced_person_subjects_guarded`,
		`alter table person_replacement_generations disable trigger person_replacement_generations_guarded`,
		`delete from replaced_person_subjects where person = '` + email + `'`,
		`delete from person_replacement_generations where person = '` + email + `'`,
		`update person_signins set revoked_at = null where id = '` + machine + `'::uuid`,
		`alter table replaced_person_subjects enable trigger replaced_person_subjects_guarded`,
		`alter table person_replacement_generations enable trigger person_replacement_generations_guarded`,
	} {
		if _, err := r.rig.Store.Pool.Exec(ctx, statement); err != nil {
			t.Fatalf("restore the broker to the backup, %q: %v", statement, err)
		}
	}
	// Another address the restore kept, whose generation Dispatch is level with: it keeps the
	// broker's table from being empty, so the sweep compares rather than reporting UNKNOWN.
	const bystander = "bystander@example.com"
	if err := r.people.SignInSubject(ctx, bystander, "account-c", "refresh-c", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	level, found, err := store.NewPgSessionStore(r.dispatch.Pool).CurrentSessionGeneration(ctx, bystander)
	if err != nil || !found {
		t.Fatalf("read %s's generation: %d, %t, %v", bystander, level, found, err)
	}
	if _, err := r.rig.Store.Pool.Exec(ctx, `insert into person_replacement_generations (person, session_generation) values ($1, $2)`, bystander, level); err != nil {
		t.Fatal(err)
	}

	count, oldest, err := r.revocations.Owed(ctx)
	if err != nil || count != 0 || oldest != 0 {
		t.Fatalf("control: after the broker's restore Dispatch owes %d (oldest %s), %v; want nothing owed, which is why the sweep and the gauges cannot see it", count, oldest, err)
	}
	if _, unknown, listed := runbookSweep(t).run(t, r.rig.Store, r.dispatch); unknown || len(listed) != 0 {
		t.Fatalf("control: after the broker's restore the sweep listed %+v (UNKNOWN %t), want nothing", listed, unknown)
	}
	r.served.revokeOwedBrokerCredentials(ctx)
	if r.refused(t, email, "account-a") || !r.machineSignInLive(t, machine) {
		t.Fatalf("control: a pass alone recovered the broker (refuses account-a %t, its sign-in live %t); want it to change nothing",
			r.refused(t, email, "account-a"), r.machineSignInLive(t, machine))
	}

	block := runbookBlockAfter(t, brokerRestoredLead)
	runStatements(t, r.dispatch.Pool, block.statements, pgx.NamedArgs{"backup": backup})
	// The pass that paid the revocation claimed it within the retry window; the loop's next pass
	// comes after that window, which the test does not wait out.
	r.ageClaims(t)
	r.served.revokeOwedBrokerCredentials(ctx)
	if !r.refused(t, email, "account-a") {
		t.Errorf("after the runbook's recovery of a broker restored alone, the broker does not refuse account-a at %s; want it refused again", email)
	}
	if r.machineSignInLive(t, machine) {
		t.Errorf("after the runbook's recovery of a broker restored alone, account-a's sign-in on a machine at %s is live; want it ended again", email)
	}
	if owing, err := r.revocations.Owing(ctx, email); err != nil || len(owing) != 0 {
		t.Errorf("after the recovery and a pass, %s owes %+v, %v; want the revocation settled again", email, owing, err)
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
