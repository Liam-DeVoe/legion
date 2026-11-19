package store

import (
	"context"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/auth"
)

// operatorDoc is a file that gives one of the operator's blocks, in a psql heredoc after the line
// starting with lead.
type operatorDoc struct{ path, lead string }

// lockAddressSessionsSQL is the statement each operator block runs first in its transaction: it
// locks the address's user_sessions row, so a sign-out at the same moment waits for the block,
// rather than moving the generation between a statement's read of it and the guard's own check.
const lockAddressSessionsSQL = "select generation from user_sessions where login = :'email' for update"

// operatorBlock is the block both docs give (sqlBlockAfter), which must say the same in both: begin,
// the lock of the address's user_sessions row (lockAddressSessionsSQL), the block's statements, the
// last starting with last, and commit. It answers the statements between begin and commit, the lock
// first, each with its psql variables (:'email', :'issuer', :'subject') turned into the named
// placeholders @email, @issuer and @subject.
func operatorBlock(t *testing.T, docs []operatorDoc, last string) []string {
	t.Helper()
	var block []string
	for _, doc := range docs {
		found := sqlBlockAfter(t, doc.path, doc.lead)
		if len(found) < 4 || found[0] != "begin" || found[1] != lockAddressSessionsSQL || found[len(found)-1] != "commit" ||
			!strings.HasPrefix(found[len(found)-2], last) {
			t.Fatalf("%s gives %q after %q, want begin, %q, the block's statements ending in one starting %q, and commit",
				doc.path, found, doc.lead, lockAddressSessionsSQL, last)
		}
		if block != nil && !slices.Equal(found, block) {
			t.Fatalf("%s gives %q, and %s gives %q: the two must say the same", doc.path, found, docs[0].path, block)
		}
		block = found
	}
	replacer := strings.NewReplacer(":'email'", "@email", ":'issuer'", "@issuer", ":'subject'", "@subject")
	sent := make([]string, 0, len(block)-2)
	for _, statement := range block[1 : len(block)-1] {
		sent = append(sent, replacer.Replace(statement))
	}
	return sent
}

// runOperatorBlock runs statements in one transaction, as psql runs the block between its begin and
// its commit, binding @email, @issuer (the canonical issuer) and @subject wherever each names them.
// Before it commits, it checks that the transaction holds the lock of email's user_sessions row: a
// sign-out's write of that row from another transaction, given 100 ms, is refused with SQLSTATE
// 55P03 (lock_not_available), and rolled back.
func runOperatorBlock(t *testing.T, pool *Pool, statements []string, email, subject string) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	args := pgx.NamedArgs{"email": email, "issuer": canonicalIssuer, "subject": subject}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement, args); err != nil {
			t.Fatalf("the operator's %q: %v", statement, err)
		}
	}
	signOut, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signOut.Exec(ctx, `set local lock_timeout = '100ms'`); err != nil {
		t.Fatal(err)
	}
	_, err = signOut.Exec(ctx, `update user_sessions set generation = generation + 1 where login = $1`, email)
	if rollback := signOut.Rollback(ctx); rollback != nil {
		t.Fatal(rollback)
	}
	if !isSQLState(err, "55P03") {
		t.Fatalf("a sign-out of %s while the operator's block is open answered %v, want it held off with SQLSTATE 55P03: the block must lock the address's user_sessions row first", email, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("the operator's commit: %v", err)
	}
}

// recordAgainDocs are the two files that give the operator's recording-again of a revocation
// settled by itself: this package's README and the runbook.
var recordAgainDocs = []operatorDoc{
	{"README.md", "- **A revocation settled by itself**"},
	{dispatchRunbook, "A revocation settled without the broker"},
}

// raiseGenerationLead starts the runbook's paragraph whose block raises an address's session
// generation past every generation the lost history can have issued, the one the broker recorded
// plus a million, and writes that number as the address's floor, after the broker answered a
// revocation superseded.
const raiseGenerationLead = "Raise the address's session generation on Dispatch's database past every generation the lost"

// TestADispatchGenerationBelowTheBrokersIsRaisedToIt: the broker answered a revocation superseded,
// since the address's session generation at Dispatch is below the one the broker recorded, and
// Dispatch's loop left it owed at its own, lower, generation, which the broker would answer the same
// again. The runbook's raise, run here from the runbook with the broker's generation, raises the
// address's generation to it, which the guard on user_sessions lets through as it only rises, and a
// raise to a value below the one Dispatch holds changes nothing; its second statement writes the
// same number as the address's floor, where the generation of a session row made later starts, and
// a later raise to a lower number leaves the floor where it is. The record-again block then makes
// the revocation owed at the raised generation, the one the broker applies.
//
// Control, in the test: before the recovery the revocation is owed at the lower generation.
func TestADispatchGenerationBelowTheBrokersIsRaisedToIt(t *testing.T) {
	ctx := context.Background()
	database := migratedEmptyStore(t)
	revocations := NewBrokerRevocations(database.Pool)
	const email = "behind@example.com"
	signInAll(t, brokerPeople(database), email, "member-1", "member-2")
	held := sessionGenerationOf(t, database.Pool, email)
	if owing, err := revocations.Owing(ctx, email); err != nil || len(owing) != 1 || owing[0].SessionGeneration == nil || *owing[0].SessionGeneration != held {
		t.Fatalf("control: before the recovery, %s owes %s, %v; want member-1's alone, at its own generation %d", email, owedText(owing...), err, held)
	}
	block := sqlBlockAfter(t, dispatchRunbook, raiseGenerationLead)
	if len(block) != 2 || !strings.HasPrefix(block[0], "update user_sessions set generation = ") ||
		!strings.HasPrefix(block[1], "insert into session_generation_floors ") {
		t.Fatalf("the runbook gives %q after %q, want the raise of user_sessions.generation and the write of the address's floor", block, raiseGenerationLead)
	}
	names := strings.NewReplacer(":'email'", "@email", ":'broker_generation'", "@broker_generation")
	broker := held + 5
	raised := broker + restoreRaiseMargin
	for _, generation := range []int64{broker, held} {
		for _, statement := range block {
			if _, err := database.Pool.Exec(ctx, names.Replace(statement), pgx.NamedArgs{"email": email, "broker_generation": generation}); err != nil {
				t.Fatalf("the runbook's raise to %d, %q: %v", generation, statement, err)
			}
		}
		if got := sessionGenerationOf(t, database.Pool, email); got != raised {
			t.Fatalf("after the runbook's raise to %d, %s's session generation is %d, want %d: the broker's plus the margin, never lowered", generation, email, got, raised)
		}
		var floor int64
		if err := database.Pool.QueryRow(ctx, `select generation from session_generation_floors where login = $1`, email).Scan(&floor); err != nil || floor != raised {
			t.Fatalf("after the runbook's raise to %d, %s's floor is %d, %v; want %d: the broker's plus the margin, never lowered", generation, email, floor, err, raised)
		}
	}
	runOperatorBlock(t, database.Pool, operatorBlock(t, recordAgainDocs, "update broker_revocations set generation = generation + 1"), email, "member-1")
	owing, err := revocations.Owing(ctx, email)
	if err != nil || len(owing) != 1 || owing[0].Subject != "member-1" || owing[0].SessionGeneration == nil || *owing[0].SessionGeneration != raised {
		t.Fatalf("after the recording-again, %s owes %s, %v; want member-1's alone, at session generation %d", email, owedText(owing...), err, raised)
	}
}

// restoreRaiseMargin is what the runbook's raise adds to the generation the broker recorded, so
// that no cookie the lost history issued matches again
// (TestTheRestoreRaiseEndsACookieIssuedAfterTheBackup).
const restoreRaiseMargin = 1000000

// TestTheRestoreRaiseEndsACookieIssuedAfterTheBackup: the generation the broker recorded for an
// address is the one the replacement it last applied left, which is the generation every cookie
// that replacement's sign-in issued carries. A Dispatch database restored to before it holds a
// lower one, and a cookie is admitted while its generation is the address's
// (identity.CookieIdentity.Login reads equality alone), so a raise to exactly the broker's number
// would make each of those cookies match again, one its holder's sign-out had already ended
// included. The runbook's raise adds a million, past every generation the lost history can have
// issued, which the broker takes as readily as its own number.
//
// Control, in the test: before the raise, the restored row matches no such cookie, so the raise is
// what could revive it; a raise to exactly the broker's generation does.
func TestTheRestoreRaiseEndsACookieIssuedAfterTheBackup(t *testing.T) {
	ctx := context.Background()
	database := migratedEmptyStore(t)
	sessions := NewPgSessionStore(database.Pool)
	const email = "restored-cookie@example.com"
	signInAll(t, brokerPeople(database), email, "account-a")
	backup := sessionGenerationOf(t, database.Pool, email)
	// After the backup: account-b replaces account-a, which the broker applies at the generation
	// that leaves; account-b signs in with a cookie of that generation and then signs out.
	signInAll(t, brokerPeople(database), email, "account-b")
	brokerGeneration, err := sessions.EnsureSession(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	cookie := auth.IssueSessionCookie(email, brokerGeneration, testSigningKey, true)
	if err := sessions.RevokeSessions(ctx, email); err != nil {
		t.Fatal(err)
	}
	matches := func(t *testing.T) (int64, bool) {
		t.Helper()
		request := httptest.NewRequest("GET", "https://dispatch.example/", nil)
		request.Header.Set("Cookie", strings.SplitN(cookie, ";", 2)[0])
		session, ok := auth.SessionFromRequest(request, testSigningKey)
		if !ok {
			t.Fatal("the cookie account-b was issued does not verify")
		}
		current, found, err := sessions.CurrentSessionGeneration(ctx, email)
		if err != nil || !found {
			t.Fatalf("read %s's generation: %v, %t", email, err, found)
		}
		return current, current == session.Generation
	}
	// restore puts the address's generation back to the backup's, as restoring the database does.
	// The guard refuses a lowering, so it runs with that trigger off.
	restore := func(t *testing.T) {
		t.Helper()
		tx, err := database.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		for _, step := range []struct {
			statement string
			args      []any
		}{
			{`alter table user_sessions disable trigger user_sessions_binding_columns`, nil},
			{`update user_sessions set generation = $2 where login = $1`, []any{email, backup}},
			{`alter table user_sessions enable trigger user_sessions_binding_columns`, nil},
		} {
			if _, err := tx.Exec(ctx, step.statement, step.args...); err != nil {
				t.Fatalf("%s: %v", step.statement, err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	raise := sqlBlockAfter(t, dispatchRunbook, raiseGenerationLead)
	names := strings.NewReplacer(":'email'", "@email", ":'broker_generation'", "@broker_generation")

	restore(t)
	if current, ok := matches(t); ok {
		t.Fatalf("control: the restored row already reads %d, which account-b's signed-out cookie matches; the test would show nothing", current)
	}
	for _, statement := range raise {
		if _, err := database.Pool.Exec(ctx, names.Replace(statement), pgx.NamedArgs{"email": email, "broker_generation": brokerGeneration}); err != nil {
			t.Fatalf("the runbook's raise, %q: %v", statement, err)
		}
	}
	if current, ok := matches(t); ok {
		t.Errorf("after the runbook's raise the address reads generation %d, which account-b's signed-out cookie matches; want no cookie of the lost history matching", current)
	}
}

// TestARevocationSettledByItselfIsPaidOnceRecordedAgain: a statement that settles an owed
// revocation by itself, which the guard lets through since it is the operator's
// clear-broker-revocation, leaves nothing for the server's loop to pay, so the broker never ends the
// replaced account's credentials. The operator's recording-again, run here from the README and the
// runbook, makes it owed under its next generation, naming the account the address records now as
// its holder: the loop claims it again, also after the address passed to a third account, and
// another address's revocation is left as it was. The block holds the address's user_sessions row
// locked until it commits (runOperatorBlock).
//
// Control, in the test: after the settlement and before the recording-again, nothing is owed and a
// claim takes nothing.
func TestARevocationSettledByItselfIsPaidOnceRecordedAgain(t *testing.T) {
	ctx := context.Background()
	database := migratedEmptyStore(t)
	people := brokerPeople(database)
	revocations := NewBrokerRevocations(database.Pool)
	const email = "settled@example.com"
	signInAll(t, people, email, "member-1", "member-2")
	signInAll(t, people, "other@example.com", "member-1", "member-2")
	if _, err := database.Pool.Exec(ctx, `update broker_revocations set done_at = now() where email = $1`, email); err != nil {
		t.Fatalf("a statement settling the revocation by itself: %v", err)
	}
	if count, _, err := revocations.Owed(ctx); err != nil || count != 1 {
		t.Fatalf("control: after the settlement, Owed = %d, %v; want 1, the other address's alone", count, err)
	}
	if owed, err := revocations.Claim(ctx, time.Minute, 10); err != nil || len(owed) != 1 || owed[0].Email != "other@example.com" {
		t.Fatalf("control: after the settlement, a claim takes %+v, %v; want the other address's alone", owed, err)
	}

	// The address passes to a third account meanwhile: the recording-again names it as the holder.
	signInAll(t, people, email, "member-3")
	ageClaims(t, database, email)
	runOperatorBlock(t, database.Pool, operatorBlock(t, recordAgainDocs, "update broker_revocations set generation = generation + 1"), email, "member-1")
	rows := brokerRevocationsOf(t, database, email)
	if !slices.Contains(rows, owedRow("member-1", "member-3", 2)) {
		t.Fatalf("after the recording-again, the revocations of %s are %+v; want member-1's owed at generation 2, held by member-3", email, rows)
	}
	if got, want := sessionGenerationsOf(t, database, email)["member-1"], sessionGenerationOf(t, database.Pool, email); got != want {
		t.Fatalf("after the recording-again, member-1's revocation records session generation %d, want the address's %d, which member-3's sign-in left", got, want)
	}
	if owing, err := revocations.Owing(ctx, email); err != nil || len(owing) != 2 {
		t.Fatalf("after the recording-again, %s owes %+v, %v; want two: member-1's again and member-2's, which member-3's sign-in recorded", email, owing, err)
	}
}

// skippedRevocationDocs are the two files that give the operator's recording of the revocation an
// account written into people.sub by itself skipped: this package's README and the runbook.
var skippedRevocationDocs = []operatorDoc{
	{"README.md", "- **An account written into `people.sub` by itself**"},
	{dispatchRunbook, "The broker refuses an account it was told no longer holds an address until a later revocation"},
}

// TestAnAccountWrittenBackByItselfIsRecoveredByRecordingTheSkippedRevocation: member-a holds the
// address and member-b takes it, whose sign-in records member-a's revocation, still owed and unpaid.
// Then a statement writes member-a back into people.sub by itself: it records no revocation of
// member-b, whose broker credentials would stay live, and settles nothing of member-a, whose owed
// revocation the loop would pay first and so end what member-a holds. The operator's recovery, run
// here from the README and the runbook, settles member-a's revocation, as a sign-in that takes an
// address back does, and makes member-b's owed, naming member-a, the account people records now, as
// its holder, at the address's session generation; a claim takes it, so the server's loop pays it.
// The block holds the address's user_sessions row locked until it commits (runOperatorBlock).
//
// Controls, in the test: after the stray write and before the recovery, nothing of member-b is
// owed; and, at another address, the block without its settle leaves member-a's revocation owed
// beside member-b's.
func TestAnAccountWrittenBackByItselfIsRecoveredByRecordingTheSkippedRevocation(t *testing.T) {
	ctx := context.Background()
	database := migratedEmptyStore(t)
	people := brokerPeople(database)
	revocations := NewBrokerRevocations(database.Pool)
	statements := operatorBlock(t, skippedRevocationDocs, "insert into broker_revocations ")
	// writtenBack signs member-a and then member-b in at email, leaving member-a's revocation owed,
	// and then writes member-a back into people.sub by itself.
	writtenBack := func(email string) {
		t.Helper()
		signInAll(t, people, email, "member-a", "member-b")
		owing, err := revocations.Owing(ctx, email)
		if err != nil || len(owing) != 1 || owing[0].Subject != "member-a" || owing[0].HolderSubject != "member-b" {
			t.Fatalf("before the stray write, %s owes %s, %v; want member-a's alone, which member-b took", email, owedText(owing...), err)
		}
		if _, err := database.Pool.Exec(ctx, `update people set sub = 'member-a' where email = $1`, email); err != nil {
			t.Fatalf("the statement writing member-a back by itself: %v", err)
		}
	}
	// owedSubjects is the replaced accounts email owes a revocation of, in the order a pass pays them.
	owedSubjects := func(email string) []string {
		t.Helper()
		owing, err := revocations.Owing(ctx, email)
		if err != nil {
			t.Fatalf("Owing(%s): %v", email, err)
		}
		var subjects []string
		for _, revocation := range owing {
			subjects = append(subjects, revocation.Subject)
		}
		return subjects
	}

	const email = "written-back@example.com"
	writtenBack(email)
	if got := owedSubjects(email); !slices.Equal(got, []string{"member-a"}) {
		t.Fatalf("control: after member-a was written back by itself, %s owes %v; want member-a's alone, member-b's skipped", email, got)
	}
	runOperatorBlock(t, database.Pool, statements, email, "member-b")
	want := OwedBrokerRevocation{Email: email, Issuer: canonicalIssuer, Subject: "member-b", HolderSubject: "member-a", Generation: 1,
		SessionGeneration: new(sessionGenerationOf(t, database.Pool, email))}
	if owing, err := revocations.Owing(ctx, email); err != nil || len(owing) != 1 || !reflect.DeepEqual(owing[0], want) {
		t.Fatalf("after the recovery, %s owes %s, %v; want %s alone, member-a's settled", email, owedText(owing...), err, owedText(want))
	}
	var settled bool
	if err := database.Pool.QueryRow(ctx, `select done_at is not null from broker_revocations where email = $1 and subject = 'member-a'`, email).Scan(&settled); err != nil || !settled {
		t.Fatalf("after the recovery, member-a's revocation is settled %t, %v; want settled", settled, err)
	}
	ageClaims(t, database, email)
	if owed, err := revocations.Claim(ctx, time.Minute, 10); err != nil || len(owed) != 1 || !reflect.DeepEqual(owed[0], want) {
		t.Fatalf("the claim after the recovery: %s, %v; want %s", owedText(owed...), err, owedText(want))
	}

	const control = "written-back-control@example.com"
	writtenBack(control)
	unsettled := slices.DeleteFunc(slices.Clone(statements), func(statement string) bool {
		return strings.HasPrefix(statement, "update broker_revocations set done_at = now()")
	})
	if len(unsettled) != len(statements)-1 {
		t.Fatalf("the block %q holds no settle to leave out", statements)
	}
	runOperatorBlock(t, database.Pool, unsettled, control, "member-b")
	if got := owedSubjects(control); !slices.Equal(got, []string{"member-a", "member-b"}) {
		t.Errorf("control: the block without its settle leaves %s owing %v; want member-a's still owed before member-b's, so the settle is what ends it", control, got)
	}
}
