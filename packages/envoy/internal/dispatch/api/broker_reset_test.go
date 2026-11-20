package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/broker/brokertest"
	"github.com/sjawhar/envoy/internal/brokerclient"
	"github.com/sjawhar/envoy/internal/dispatch/agentsecrets"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The leads of the runbook's reset after a Dispatch database restored or made again alone
// ("After a Dispatch database restored or made again alone").
const (
	resetSessionsLead    = "**End every browser session and revoke every personal token.**"
	resetCredentialsLead = "**End every person's credential at the broker.**"
	resetDirectoryLead   = "**Clear a refusal only for the directory's holder.**"
)

// brokerList is a shell function of the runbook's credentials block: a read of the broker's
// database that lists, by id and person or by login, one kind of person credential still live.
var brokerList = regexp.MustCompile(`^(signins|logins|pages)\(\) \{ psql "\$BROKER_PSQL_URL" -v ON_ERROR_STOP=1 -At (?:-F ' ' )?-c "([^"]+)"; \}$`)

// resetSessions runs the reset's first block from the runbook: its read of the broker's greatest
// recorded generation on the broker's database, then its one transaction on Dispatch's database.
// It answers the number every session was raised to.
func (r *recoveryRig) resetSessions(t *testing.T) int64 {
	t.Helper()
	ctx := context.Background()
	block := runbookBlockAfter(t, resetSessionsLead)
	if len(block.statements) < 4 || !strings.HasPrefix(block.statements[0], "select coalesce(max(session_generation), 0) from person_replacement_generations") {
		t.Fatalf("the runbook's reset of sessions is %q; want the broker's read, then one transaction on Dispatch's database", block.statements)
	}
	var greatest int64
	if err := r.rig.Store.Pool.QueryRow(ctx, block.statements[0]).Scan(&greatest); err != nil {
		t.Fatalf("the reset's read of the broker's greatest generation: %v", err)
	}
	runStatements(t, r.dispatch.Pool, block.statements[1:], pgx.NamedArgs{"broker_greatest": greatest})
	var raised int64
	if err := r.dispatch.Pool.QueryRow(ctx, `select generation from session_generation_floors where login = '*'`).Scan(&raised); err != nil {
		t.Fatalf("the deployment's floor after the reset: %v", err)
	}
	return raised
}

// credentialsBlock is the reset's second block of the runbook: its lines, and the three reads of
// the broker's database it defines (signins, logins and pages), each a list of one kind of person
// credential still live, by id and person or by login.
type credentialsBlock struct {
	text  string
	lists map[string]string
}

// readCredentialsBlock reads that block from the runbook.
func readCredentialsBlock(t *testing.T) credentialsBlock {
	t.Helper()
	block := runbookBlockAfter(t, resetCredentialsLead)
	lists := map[string]string{}
	for _, command := range block.commands {
		if match := brokerList.FindStringSubmatch(command); match != nil {
			lists[match[1]] = match[2]
		}
	}
	if len(lists) != 3 {
		t.Fatalf("the runbook's credentials block defines the lists %v; want signins, logins and pages", slices.Sorted(maps.Keys(lists)))
	}
	text := strings.Join(block.lines, "\n")
	if !strings.Contains(text, `if left=$( { signins && logins && pages; } ); then`) {
		t.Fatalf("the runbook's credentials block ends with no read of all three lists; it reads:\n%s", text)
	}
	return credentialsBlock{text: text, lists: lists}
}

// rows runs the block's read name on the broker's database and answers each row's columns as
// psql -At prints them.
func (c credentialsBlock) rows(t *testing.T, r *recoveryRig, name string) [][]string {
	t.Helper()
	found, err := r.rig.Store.Pool.Query(context.Background(), c.lists[name])
	if err != nil {
		t.Fatalf("the credentials block's %s(): %v", name, err)
	}
	values, err := pgx.CollectRows(found, func(row pgx.CollectableRow) ([]string, error) {
		columns, err := row.Values()
		if err != nil {
			return nil, err
		}
		var texts []string
		for _, column := range columns {
			texts = append(texts, toText(column))
		}
		return texts, nil
	})
	if err != nil {
		t.Fatalf("the credentials block's %s(): %v", name, err)
	}
	return values
}

// live is what the block's last read lists: every person credential still live at the broker,
// which is empty when the block printed `no person credential is live at the broker`.
func (c credentialsBlock) live(t *testing.T, r *recoveryRig) []string {
	t.Helper()
	var left []string
	for _, name := range []string{"signins", "logins", "pages"} {
		for _, row := range c.rows(t, r, name) {
			left = append(left, name+": "+strings.Join(row, " "))
		}
	}
	return left
}

// endBrokerCredentials runs the reset's second block from the runbook: for each loop the block
// holds, the call it makes for every row of that loop's read - the broker's own routes, as
// Dispatch's ServiceAccount calls them, and, for a login with a page session, the store call
// `envoy-broker end-sessions` makes (store.DeleteBrowserSessionsForLogin). It answers what the
// block's last read lists still live (STILL LIVE), empty when every person credential at the
// broker is ended.
func (r *recoveryRig) endBrokerCredentials(t *testing.T) []string {
	t.Helper()
	ctx := context.Background()
	block := readCredentialsBlock(t)
	if strings.Contains(block.text, `"$broker_url/v1/person-signins/$id/revoke"`) {
		for _, row := range block.rows(t, r, "signins") {
			if status, body := r.rig.UI(t, "POST", "/v1/person-signins/"+row[0]+"/revoke", map[string]any{"person": row[1]}); status != 200 {
				t.Fatalf("NOT REVOKED %s: %d %s", row[0], status, body)
			}
		}
	}
	if strings.Contains(block.text, `"$broker_url/v1/launcher-credentials/$id/revoke-by-approver"`) {
		for _, row := range block.rows(t, r, "logins") {
			if status, body := r.rig.UI(t, "POST", "/v1/launcher-credentials/"+row[0]+"/revoke-by-approver", map[string]any{"approver": row[1]}); status != 200 {
				t.Fatalf("NOT REVOKED %s: %d %s", row[0], status, body)
			}
		}
	}
	if strings.Contains(block.text, `envoy-broker end-sessions "$login"`) {
		for _, row := range block.rows(t, r, "pages") {
			if _, err := r.rig.Store.DeleteBrowserSessionsForLogin(ctx, row[0]); err != nil {
				t.Fatalf("NOT ENDED %s: %v", row[0], err)
			}
		}
	}
	return block.live(t, r)
}

// toText is a column of a list's row as psql -At prints it: a uuid in its hyphened form, any other
// value as fmt prints it.
func toText(value any) string {
	if id, ok := value.([16]byte); ok {
		return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
	}
	return fmt.Sprint(value)
}

// directoryUser is what the Workspace directory answers users.get with, of the fields the
// runbook's block reads.
type directoryUser struct {
	ID           string `json:"id"`
	PrimaryEmail string `json:"primaryEmail"`
}

// directoryAnswer is one response of the directory stand-in. A zero Status is 200, and a missing
// address is 404 as users.get answers an account the organization does not hold.
type directoryAnswer struct {
	User   directoryUser
	Status int
}

// clearHoldersRefusals runs the reset's third block itself, through bash, psql and jq, against the
// broker's database: the runbook's step that makes the libpq URL, then the block. A stand-in for
// curl answers each address from directory. It returns the block's output and its status so tests
// hold both a 404 that leaves a refusal and a non-404 failure that makes the block fail.
func (r *recoveryRig) clearHoldersRefusals(t *testing.T, directory map[string]directoryAnswer) (string, error) {
	t.Helper()
	for _, tool := range []string{"bash", "psql", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed, so the reset's directory block cannot be run here: %v", tool, err)
		}
	}
	dir := t.TempDir()
	answers := filepath.Join(dir, "directory")
	statuses := filepath.Join(dir, "status")
	if err := os.Mkdir(answers, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statuses, 0o700); err != nil {
		t.Fatal(err)
	}
	for address, answer := range directory {
		raw, err := json.Marshal(answer.User)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(answers, address), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		status := answer.Status
		if status == 0 {
			status = 200
		}
		if err := os.WriteFile(filepath.Join(statuses, address), []byte(strconv.Itoa(status)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stubs := filepath.Join(dir, "bin")
	if err := os.Mkdir(stubs, 0o700); err != nil {
		t.Fatal(err)
	}
	// The stand-in for users.get writes its JSON body and, when curl asked for it, the status
	// trailer curl's -w '%{http_code}' prints. A missing address is a 404 and curl exits 22 for
	// every non-200, as --fail-with-body does.
	curl := `#!/bin/sh
write=
for arg; do
  case "$arg" in *'%{http_code}'*) write=1;; esac
  url=$arg
done
address=${url#*/users/}
address=${address%%\?*}
status=404
body='{"error":{"code":404,"message":"Resource Not Found: userKey"}}'
if [ -f "$DIRECTORY_ANSWERS/$address" ]; then
  body=$(cat "$DIRECTORY_ANSWERS/$address")
  status=$(cat "$DIRECTORY_STATUSES/$address")
fi
printf '%s' "$body"
[ -z "$write" ] || printf '\n%s' "$status"
[ "$status" = 200 ] && exit 0
exit 22
`
	if err := os.WriteFile(filepath.Join(stubs, "curl"), []byte(curl), 0o700); err != nil {
		t.Fatal(err)
	}
	block := runbookBlockAfter(t, resetDirectoryLead)
	urlStep := runbookBlockAfter(t, brokerURLLead)
	script := filepath.Join(dir, "directory.sh")
	text := strings.Join(urlStep.lines, "\n") + "\n" + strings.Join(block.lines, "\n") + "\n"
	if err := os.WriteFile(script, []byte(text), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", script)
	command.Env = append(os.Environ(),
		"PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"),
		"BROKER_DATABASE_URL="+psqlURL(t, r.rig.Store.Pool),
		"DIRECTORY_TOKEN=not-a-secret",
		"DIRECTORY_ANSWERS="+answers,
		"DIRECTORY_STATUSES="+statuses)
	output, err := command.CombinedOutput()
	return string(output), err
}

// approveAs asks the broker to approve a new machine login of email, naming account as the
// approving Google account and generation as the session generation Dispatch's check read, as
// Dispatch's approve relay does, and answers the broker's error, nil when it approved.
func (r *recoveryRig) approveAs(t *testing.T, email, account string, generation int64) error {
	t.Helper()
	ctx := context.Background()
	client := brokertest.NewClient(t, r.rig.URL, strings.TrimPrefix(r.rig.URL, "http://"))
	client.UIToken, client.DPoP = r.rig.UIToken, brokerclient.NewDPoPTransport
	authorization := client.DeviceLogin(t, brokertest.NewSigningKey(t), email, "reset-box-"+account)
	lookedUp, err := r.broker.MachineLookup(ctx, json.RawMessage(`{"code":`+strconv.Quote(authorization.UserCode)+`}`))
	if err != nil {
		t.Fatalf("look up %s's machine login by its code: %v", email, err)
	}
	var record struct {
		RecordID string `json:"record_id"`
	}
	if err := json.Unmarshal(lookedUp, &record); err != nil || record.RecordID == "" {
		t.Fatalf("the lookup answered %s (%v), want a record", lookedUp, err)
	}
	code := authorization.UserCode
	_, err = r.broker.Approve(ctx, record.RecordID, agentsecrets.Decision{
		Approver: email, Code: &code, ApproverIssuer: recoveryIssuer, ApproverSubject: account, ApproverSessionGeneration: &generation,
	})
	return err
}

// pageSession gives account a live session on the broker's approval page under email, as the
// page's callback writes one.
func (r *recoveryRig) pageSession(t *testing.T, email, account string) {
	t.Helper()
	hash := sha256.Sum256([]byte(email + "/" + account))
	if _, err := r.rig.Store.Pool.Exec(context.Background(), `insert into browser_sessions (secret_hash, login, expires_at, issuer, subject)
		values ($1, $2, now() + interval '1 hour', $3, $4)`, hash[:], email, recoveryIssuer, account); err != nil {
		t.Fatalf("the page session of %s at %s: %v", account, email, err)
	}
}

// personalToken stores a live personal token of email recording account, as the mint does, and
// answers its id.
func (r *recoveryRig) personalToken(t *testing.T, email, account string) string {
	t.Helper()
	hash := sha256.Sum256([]byte(email + "/" + account + "/token"))
	var id string
	if err := r.dispatch.Pool.QueryRow(context.Background(), `insert into agent_tokens (owner, name, token_hash, prefix, scopes, subject)
		values ($1, 'restored', $2, $3, $4, $5) returning id::text`,
		email, hash[:], strings.Repeat("r", agentTokenPrefixLength), []string{model.ScopePerson}, account).Scan(&id); err != nil {
		t.Fatalf("the personal token of %s at %s: %v", account, email, err)
	}
	return id
}

// TestTheResetEndsEveryCredentialAndClearsOnlyTheHoldersRefusal runs the runbook's reset after a
// Dispatch database restored or made again alone, every step from the runbook, against Dispatch's
// database and a real broker's (brokertest.NewRig), over the three histories round 21's review
// found the per-address recovery missed, and two the directory does not answer for:
//
//   - unapplied@ (F1): A held the address at the backup; after it, B took it, and the revocation of
//     A was still owed when the database was lost, so the broker never refused A. Restored,
//     Dispatch records A, A's personal token is live, and A holds a sign-in on a machine, a machine
//     login and an approval-page session at the broker. The directory names B.
//   - noaccount@ (F2): the database was made again, so Dispatch has no row; the broker refuses X.
//     The directory names H.
//   - takeback@ (F3): the take-back the lost database still owed leaves the broker refusing H,
//     Google's holder, and N, an account that held the address once; Dispatch, restored, records
//     the stale S. The directory names H.
//   - owedrow@: the restored database still owes X's revoke though the broker already applied it;
//     X took the address back after the backup, and the directory names X. The reset settles its
//     restored debt before clearing X, so the first pass after the reset cannot refuse X again.
//   - alias@: the directory answers users.get of the address with another user, whose
//     primaryEmail is not the address (users.get answers an alias), and that user's id is an
//     account the broker refuses here.
//   - outside@: the directory answers 404, as it does for someone from outside the domain and a
//     deleted user; the broker refuses O.
//
// After the reset's first block every session is raised, every personal token revoked and every
// restored debt settled (the store test holds the cookies), after its second no person credential is
// live at the broker and its read says so, and after its third the broker no longer refuses the
// directory's holder at takeback@ or owedrow@ and refuses every other account it refused. Then each
// holder signs in to Dispatch: the sign-in replaces the restored account at the raised generation,
// the loop pays that revocation, and the broker refuses A and S. The directory's owedrow@ holder
// can make a broker sign-in and approve before Dispatch starts and stays live after its first pass.
// Each holder's approve is approved; an approve by A, S, X, N and the alias's account is refused
// APPROVER_REPLACED.
//
// Controls, each a break of the runbook restored by checksum: the credentials block without its
// call that revokes a sign-in (STILL LIVE names it); the directory block without its primaryEmail
// comparison (the alias's account is cleared and approves); and the first block without its
// settlement (the owedrow@ holder is refused again and its sign-in ends).
func TestTheResetEndsEveryCredentialAndClearsOnlyTheHoldersRefusal(t *testing.T) {
	ctx := context.Background()
	r := newRecoveryRig(t)
	const unapplied, noAccount, takeback, owed, kept, alias, outside = "unapplied@example.com", "noaccount@example.com", "takeback@example.com", "owedrow@example.com", "kept@example.com", "alias@example.com", "outside@example.com"

	// unapplied@: Dispatch, restored, records A; A's credentials at the broker are live.
	if err := r.people.SignInSubject(ctx, unapplied, "a-unapplied", "refresh-a", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	token := r.personalToken(t, unapplied, "a-unapplied")
	signIn := r.signInMachine(t, unapplied, "a-unapplied", strings.Repeat("u", 43))
	r.pageSession(t, unapplied, "a-unapplied")
	loginClient := brokertest.NewClient(t, r.rig.URL, strings.TrimPrefix(r.rig.URL, "http://"))
	loginClient.UIToken, loginClient.DPoP = r.rig.UIToken, brokerclient.NewDPoPTransport
	machine := loginClient.DeviceLogin(t, brokertest.NewSigningKey(t), unapplied, "unapplied-box")
	machineLogin := loginClient.ApproveMachineLogin(t, unapplied, machine.UserCode)

	// noaccount@: no Dispatch row; the broker refuses X.
	r.revoke(t, noAccount, "x-noaccount", "h-noaccount", 3)
	// takeback@: Dispatch records the stale S; the broker refuses H and N.
	if err := r.people.SignInSubject(ctx, takeback, "s-takeback", "refresh-s", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	r.revoke(t, takeback, "h-takeback", "s-takeback", 4)
	r.revoke(t, takeback, "n-takeback", "s-takeback", 5)
	// owedrow@: the broker applied X's revoke before the backup, but restored Dispatch still owes
	// it. X then took the address back without that history reaching the broker, so the directory
	// clears X. The first block must settle the restored row before Dispatch starts again.
	if err := r.people.SignInSubject(ctx, owed, "x-owed", "refresh-x", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	if err := r.people.SignInSubject(ctx, owed, "y-owed", "refresh-y", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	owedRows, err := r.revocations.Owing(ctx, owed)
	if err != nil || len(owedRows) != 1 || owedRows[0].SessionGeneration == nil {
		t.Fatalf("the restored database owes %+v at %s, %v; want X's one revocation carrying a generation", owedRows, owed, err)
	}
	r.revoke(t, owed, owedRows[0].Subject, owedRows[0].HolderSubject, *owedRows[0].SessionGeneration)
	// kept@: broker T is refused already, while restored Dispatch still owes S's revoke naming T.
	// The directory names H, so it keeps T's refusal. A stale first pass would apply S's revoke
	// and delete T's refusal; the reset's settlement makes that impossible.
	if err := r.people.SignInSubject(ctx, kept, "s-kept", "refresh-s", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	if err := r.people.SignInSubject(ctx, kept, "t-kept", "refresh-t", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	keptRows, err := r.revocations.Owing(ctx, kept)
	if err != nil || len(keptRows) != 1 || keptRows[0].SessionGeneration == nil {
		t.Fatalf("the restored database owes %+v at %s, %v; want S's one revocation carrying a generation", keptRows, kept, err)
	}
	r.revoke(t, kept, "t-kept", "u-kept", 1)
	// alias@ and outside@: refusals the directory gives no holder for.
	r.revoke(t, alias, "n-alias", "h-alias", 2)
	r.revoke(t, outside, "o-outside", "h-outside", 2)
	directory := map[string]directoryAnswer{
		unapplied: {User: directoryUser{ID: "b-unapplied", PrimaryEmail: unapplied}},
		noAccount: {User: directoryUser{ID: "h-noaccount", PrimaryEmail: noAccount}},
		takeback:  {User: directoryUser{ID: "h-takeback", PrimaryEmail: strings.ToUpper(takeback)}},
		owed:      {User: directoryUser{ID: "x-owed", PrimaryEmail: owed}},
		kept:      {User: directoryUser{ID: "h-kept", PrimaryEmail: kept}},
		alias:     {User: directoryUser{ID: "n-alias", PrimaryEmail: "someone-else@example.com"}},
	}
	if live := readCredentialsBlock(t).live(t, r); len(live) == 0 {
		t.Fatal("control: before the reset no person credential is live at the broker; the test would show nothing")
	}

	raised := r.resetSessions(t)
	if reset := strings.Join(runbookBlockAfter(t, resetSessionsLead).lines, "\n"); !strings.Contains(reset, "lock table session_generation_floors, user_sessions, agent_tokens, broker_revocations in exclusive mode") {
		t.Errorf("the reset's transaction does not lock broker_revocations beside its other rows:\n%s", reset)
	}
	var revoked bool
	if err := r.dispatch.Pool.QueryRow(ctx, `select revoked_at is not null from agent_tokens where id = $1::uuid`, token).Scan(&revoked); err != nil || !revoked {
		t.Errorf("after the reset A's personal token at %s is revoked %t, %v; want revoked", unapplied, revoked, err)
	}
	if owing, err := r.revocations.Owing(ctx, owed); err != nil || len(owing) != 0 {
		t.Errorf("after the reset's first block %s owes %+v, %v; want its restored revocation settled before the directory clears its holder", owed, owing, err)
	}
	if owing, err := r.revocations.Owing(ctx, kept); err != nil || len(owing) != 0 {
		t.Errorf("after the reset's first block %s owes %+v, %v; want its restored revocation settled before the directory keeps T's refusal", kept, owing, err)
	}
	if left := r.endBrokerCredentials(t); len(left) != 0 {
		t.Errorf("STILL LIVE after the reset's credentials block:\n%s", strings.Join(left, "\n"))
	}
	if r.machineSignInLive(t, signIn) {
		t.Errorf("after the reset A's sign-in on a machine at %s is live", unapplied)
	}
	var loginLive bool
	if err := r.rig.Store.Pool.QueryRow(ctx, `select revoked_at is null from launcher_credentials where id = $1::uuid`, machineLogin).Scan(&loginLive); err != nil || loginLive {
		t.Errorf("after the reset A's machine login at %s is live %t, %v; want it revoked", unapplied, loginLive, err)
	}
	var pageSessionsLive int
	if err := r.rig.Store.Pool.QueryRow(ctx, `select count(*) from browser_sessions where login = $1 and expires_at > now()`, unapplied).Scan(&pageSessionsLive); err != nil || pageSessionsLive != 0 {
		t.Errorf("after the reset A's approval-page sessions at %s are live %d (%v); want none", unapplied, pageSessionsLive, err)
	}
	output, err := r.clearHoldersRefusals(t, directory)
	if err != nil {
		t.Fatalf("the reset's directory block: %v\n%s", err, output)
	}
	for _, want := range []string{"HOLDER " + takeback + " h-takeback", "HOLDER " + owed + " x-owed", "HOLDER " + kept + " h-kept", "NO ACCOUNT " + alias, "NO ACCOUNT " + outside} {
		if !strings.Contains(output, want) {
			t.Errorf("the directory block printed:\n%s\nwant a line %q", output, want)
		}
	}
	for _, refusal := range []struct {
		email, account string
		want           bool
	}{
		{noAccount, "x-noaccount", true},
		{takeback, "h-takeback", false},
		{takeback, "n-takeback", true},
		{owed, "x-owed", false},
		{kept, "t-kept", true},
		{alias, "n-alias", true},
		{outside, "o-outside", true},
	} {
		if got := r.refused(t, refusal.email, refusal.account); got != refusal.want {
			t.Errorf("after the directory block the broker refuses %s at %s %t, want %t", refusal.account, refusal.email, got, refusal.want)
		}
	}
	// X can sign in and approve at the broker after the directory clears X and before Dispatch
	// starts. A restored row the first block did not settle would re-refuse X and end this sign-in.
	owedSignIn := r.signInMachine(t, owed, "x-owed", strings.Repeat("o", 43))
	owedGeneration, found, err := store.NewPgSessionStore(r.dispatch.Pool).CurrentSessionGeneration(ctx, owed)
	if err != nil || !found || owedGeneration < raised {
		t.Fatalf("%s's session generation after the reset is %d (found %t, %v); want at least the reset's %d", owed, owedGeneration, found, err, raised)
	}
	if err := r.approveAs(t, owed, "x-owed", owedGeneration); err != nil {
		t.Errorf("the directory's owedrow@ holder X approves after the reset: %v; want it approved", err)
	}
	// This is Dispatch's first pass, before anyone signs in again. A restored owed S→T row that
	// step 1 left owed would apply here and lift T's directory-kept refusal.
	r.ageClaims(t)
	r.served.revokeOwedBrokerCredentials(ctx)
	if !r.refused(t, kept, "t-kept") {
		t.Errorf("after the first pass, the broker does not refuse T at %s; want the directory-kept refusal unchanged", kept)
	}

	// Dispatch is started again, and each holder signs in only after that first pass.
	for _, holder := range []struct{ email, account string }{
		{unapplied, "b-unapplied"}, {noAccount, "h-noaccount"}, {takeback, "h-takeback"},
	} {
		if err := r.people.SignInSubject(ctx, holder.email, holder.account, "refresh-"+holder.account, model.MemberFull); err != nil {
			t.Fatalf("%s's sign-in at %s after the reset: %v", holder.account, holder.email, err)
		}
	}
	r.ageClaims(t)
	r.served.revokeOwedBrokerCredentials(ctx)
	for _, replaced := range []struct{ email, account string }{{unapplied, "a-unapplied"}, {takeback, "s-takeback"}} {
		if !r.refused(t, replaced.email, replaced.account) {
			t.Errorf("after the holder's sign-in and a pass, the broker does not refuse %s at %s; want it refused", replaced.account, replaced.email)
		}
	}
	if r.refused(t, owed, "x-owed") || !r.machineSignInLive(t, owedSignIn) {
		t.Errorf("after Dispatch's first pass, the directory's owedrow@ holder X is refused %t and its sign-in live %t; want not refused and live", r.refused(t, owed, "x-owed"), r.machineSignInLive(t, owedSignIn))
	}
	for _, holder := range []struct{ email, account string }{
		{unapplied, "b-unapplied"}, {noAccount, "h-noaccount"}, {takeback, "h-takeback"},
	} {
		generation, found, err := store.NewPgSessionStore(r.dispatch.Pool).CurrentSessionGeneration(ctx, holder.email)
		if err != nil || !found || generation < raised {
			t.Fatalf("%s's session generation after the sign-in is %d (found %t, %v); want at least the reset's %d", holder.email, generation, found, err, raised)
		}
		if err := r.approveAs(t, holder.email, holder.account, generation); err != nil {
			t.Errorf("the directory's holder %s approves at %s: %v; want it approved", holder.account, holder.email, err)
		}
	}
	for _, other := range []struct{ email, account string }{
		{unapplied, "a-unapplied"}, {noAccount, "x-noaccount"}, {takeback, "s-takeback"}, {takeback, "n-takeback"}, {alias, "n-alias"},
	} {
		err := r.approveAs(t, other.email, other.account, raised+1000)
		var refusal *agentsecrets.Error
		if !errors.As(err, &refusal) || refusal.Status != 403 || refusal.Code != "APPROVER_REPLACED" {
			t.Errorf("an approve by %s at %s answered %v (%+v); want it refused 403 APPROVER_REPLACED", other.account, other.email, err, refusal)
		}
	}
}

// TestTheResetDirectoryBlockReportsEveryUnusableStatus gives each non-200, non-404 directory
// answer a refusal to leave alone. The block must say that the read is unknown, name its status
// and fail, rather than call an unavailable directory account no account.
func TestTheResetDirectoryBlockReportsEveryUnusableStatus(t *testing.T) {
	r := newRecoveryRig(t)
	cases := []struct {
		email  string
		status int
	}{
		{"directory-401@example.com", 401},
		{"directory-403@example.com", 403},
		{"directory-429@example.com", 429},
		{"directory-503@example.com", 503},
	}
	for _, test := range cases {
		r.revoke(t, test.email, "refused-"+strconv.Itoa(test.status), "holder-"+strconv.Itoa(test.status), int64(test.status))
	}
	r.revoke(t, "directory-404@example.com", "refused-404", "holder-404", 404)

	directory := map[string]directoryAnswer{}
	for _, test := range cases {
		directory[test.email] = directoryAnswer{Status: test.status}
	}
	output, err := r.clearHoldersRefusals(t, directory)
	if err == nil {
		t.Fatalf("the directory block answered success:\n%s\nwant failure after its unavailable directory reads", output)
	}
	for _, test := range cases {
		want := "UNKNOWN " + test.email + ": the directory read failed (" + strconv.Itoa(test.status) + ")"
		if !strings.Contains(output, want) {
			t.Errorf("the directory block printed:\n%s\nwant %q", output, want)
		}
		if !r.refused(t, test.email, "refused-"+strconv.Itoa(test.status)) {
			t.Errorf("the directory block cleared the refusal after its %d read at %s; want it left", test.status, test.email)
		}
	}
	if !strings.Contains(output, "NO ACCOUNT directory-404@example.com") {
		t.Errorf("the directory block printed:\n%s\nwant its 404 as NO ACCOUNT", output)
	}
	if !r.refused(t, "directory-404@example.com", "refused-404") {
		t.Error("the directory block cleared the refusal after its 404; want it left")
	}
}

// TestTheResetDirectoryBlockFailsWhenTheBrokerRefusesAClear: the directory names the refused
// account as the address's holder, and the broker's database refuses the clear. The block must
// name the address NOT CLEARED and fail, since that holder stays refused, rather than end in
// success.
//
// Control in the runbook: the clear's psql without its failure arm lets the block exit 0.
func TestTheResetDirectoryBlockFailsWhenTheBrokerRefusesAClear(t *testing.T) {
	r := newRecoveryRig(t)
	const email = "clear-refused@example.com"
	r.revoke(t, email, "refused-holder", "other-account", 1)
	for _, statement := range []string{
		`create function refuse_this_tests_clear() returns trigger language plpgsql as $$ begin raise exception 'this test refuses the clear'; end $$`,
		`create trigger refuse_this_tests_clear before delete on replaced_person_subjects for each row execute function refuse_this_tests_clear()`,
	} {
		if _, err := r.rig.Store.Pool.Exec(context.Background(), statement); err != nil {
			t.Fatalf("make the broker's database refuse the clear: %v", err)
		}
	}
	output, err := r.clearHoldersRefusals(t, map[string]directoryAnswer{
		email: {User: directoryUser{ID: "refused-holder", PrimaryEmail: email}},
	})
	if err == nil {
		t.Fatalf("the directory block answered success:\n%s\nwant failure after the broker refused its clear", output)
	}
	if want := "NOT CLEARED " + email; !strings.Contains(output, want) {
		t.Errorf("the directory block printed:\n%s\nwant %q", output, want)
	}
	if !r.refused(t, email, "refused-holder") {
		t.Error("the broker does not refuse the holder after its refused clear; want the refusal left")
	}
}

// TestTheResetCountsItsHourFromTheStopOfEveryDispatchProcess: step 1 settles every owed broker
// revocation, the ones restored Dispatch recorded after the restore included, so the replaced
// account's pre-move Google ID token is what step 2 must outwait. Every move Dispatch recorded comes
// before the last process stopped, so the hour counts from that stop, not from the loss.
//
// Control: the earlier rule, counted from the loss.
func TestTheResetCountsItsHourFromTheStopOfEveryDispatchProcess(t *testing.T) {
	runbook := strings.Join(runbookLines(t), "\n")
	if want := "Start step 2 no sooner than one hour after the last Dispatch process\nstopped."; !strings.Contains(runbook, want) {
		t.Errorf("the reset does not count its hour from the stop: the runbook is missing %q", want)
	}
	if stale := "one hour after the database was lost"; strings.Contains(runbook, stale) {
		t.Errorf("the reset still counts its hour from the loss: the runbook holds %q", stale)
	}
}

// TestTheResetCredentialsPageLoopGivesBrokerRunNoStdin runs the page-session loop itself. Its
// broker_run stand-in reads stdin, as kubectl run -i, docker run -i or ssh can. With stdin from
// /dev/null it ends every listed login; without the redirect its first run consumes the remaining
// logins from the loop.
func TestTheResetCredentialsPageLoopGivesBrokerRunNoStdin(t *testing.T) {
	for _, tool := range []string{"bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed, so the page-session loop cannot be run here: %v", tool, err)
		}
	}
	block := readCredentialsBlock(t)
	const runner = `broker_run() { '<the command that runs its arguments where your deployment runs a broker subcommand>' "$@"; }`
	const standIn = `broker_run() { cat >/dev/null; printf 'ENDED %s\n' "$3"; }`
	text := strings.Replace(block.text, runner, standIn, 1)
	if text == block.text {
		t.Fatalf("the runbook's page-session loop has no broker_run declaration %q:\n%s", runner, block.text)
	}
	dir := t.TempDir()
	stubs := filepath.Join(dir, "bin")
	if err := os.Mkdir(stubs, 0o700); err != nil {
		t.Fatal(err)
	}
	kubectl := "#!/bin/sh\nprintf token\n"
	if err := os.WriteFile(filepath.Join(stubs, "kubectl"), []byte(kubectl), 0o700); err != nil {
		t.Fatal(err)
	}
	psql := `#!/bin/sh
case "$*" in
  *"browser_sessions"*)
    if [ ! -e "$PAGES_SEEN" ]; then
      printf 'a@example.com\nb@example.com\nc@example.com\n'
      : >"$PAGES_SEEN"
    fi
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(stubs, "psql"), []byte(psql), 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "credentials.sh")
	if err := os.WriteFile(script, []byte(text+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", script)
	command.Env = append(os.Environ(),
		"PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PAGES_SEEN="+filepath.Join(dir, "pages-seen"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("the credentials block: %v\n%s", err, output)
	}
	for _, login := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		if !strings.Contains(string(output), "ENDED "+login) {
			t.Errorf("a broker_run stand-in that reads stdin ended no %s:\n%s", login, output)
		}
	}
	if !strings.Contains(block.text, `broker_run envoy-broker end-sessions "$login" </dev/null`) {
		t.Errorf("the runbook's page-session loop is missing </dev/null:\n%s", block.text)
	}
}
