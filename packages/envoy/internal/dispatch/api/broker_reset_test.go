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

// clearHoldersRefusals runs the reset's third block itself, through bash, psql and jq, against the
// broker's database: the runbook's step that makes the libpq URL, then the block. A stand-in for
// curl answers each address from directory, the Workspace directory's users.get, or 404 for an
// address directory leaves out, as the directory answers someone from outside the domain and a
// deleted user. It answers the block's output. It skips the test where bash, psql or jq is not
// installed, since nothing else shows what the shell does with the block.
func (r *recoveryRig) clearHoldersRefusals(t *testing.T, directory map[string]directoryUser) string {
	t.Helper()
	for _, tool := range []string{"bash", "psql", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed, so the reset's directory block cannot be run here: %v", tool, err)
		}
	}
	dir := t.TempDir()
	answers := filepath.Join(dir, "directory")
	if err := os.Mkdir(answers, 0o700); err != nil {
		t.Fatal(err)
	}
	for address, user := range directory {
		raw, err := json.Marshal(user)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(answers, address), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stubs := filepath.Join(dir, "bin")
	if err := os.Mkdir(stubs, 0o700); err != nil {
		t.Fatal(err)
	}
	// The stand-in for the directory's users.get: the address from the URL's path, the answer from
	// the test's directory, 404 and exit 22 (curl's --fail-with-body) for an address it holds none for.
	curl := `#!/bin/sh
for arg; do url=$arg; done
address=${url#*/users/}
address=${address%%\?*}
if [ -f "$DIRECTORY_ANSWERS/$address" ]; then cat "$DIRECTORY_ANSWERS/$address"; exit 0; fi
echo '{"error":{"code":404,"message":"Resource Not Found: userKey"}}'
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
		"DIRECTORY_ANSWERS="+answers)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("the reset's directory block: %v\n%s", err, output)
	}
	return string(output)
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
//   - alias@: the directory answers users.get of the address with another user, whose
//     primaryEmail is not the address (users.get answers an alias), and that user's id is an
//     account the broker refuses here.
//   - outside@: the directory answers 404, as it does for someone from outside the domain and a
//     deleted user; the broker refuses O.
//
// After the reset's first block every session is raised and every personal token revoked (the
// store test holds the cookies), after its second no person credential is live at the broker and
// its read says so, and after its third the broker no longer refuses the directory's holder at
// takeback@ and refuses every other account it refused. Then each holder signs in to Dispatch:
// the sign-in replaces the restored account at the raised generation, the loop pays that
// revocation, and the broker refuses A and S. Each holder's approve is approved; an approve by A,
// S, X, N and the alias's account is refused APPROVER_REPLACED.
//
// Controls, each a break of the runbook restored by checksum: the credentials block without its
// call that revokes a sign-in (STILL LIVE names it); the directory block without its primaryEmail
// comparison (the alias's account is cleared and approves).
func TestTheResetEndsEveryCredentialAndClearsOnlyTheHoldersRefusal(t *testing.T) {
	ctx := context.Background()
	r := newRecoveryRig(t)
	const unapplied, noAccount, takeback, alias, outside = "unapplied@example.com", "noaccount@example.com", "takeback@example.com", "alias@example.com", "outside@example.com"

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
	// alias@ and outside@: refusals the directory gives no holder for.
	r.revoke(t, alias, "n-alias", "h-alias", 2)
	r.revoke(t, outside, "o-outside", "h-outside", 2)
	directory := map[string]directoryUser{
		unapplied: {ID: "b-unapplied", PrimaryEmail: unapplied},
		noAccount: {ID: "h-noaccount", PrimaryEmail: noAccount},
		takeback:  {ID: "h-takeback", PrimaryEmail: strings.ToUpper(takeback)},
		alias:     {ID: "n-alias", PrimaryEmail: "someone-else@example.com"},
	}
	if live := readCredentialsBlock(t).live(t, r); len(live) == 0 {
		t.Fatal("control: before the reset no person credential is live at the broker; the test would show nothing")
	}

	raised := r.resetSessions(t)
	var revoked bool
	if err := r.dispatch.Pool.QueryRow(ctx, `select revoked_at is not null from agent_tokens where id = $1::uuid`, token).Scan(&revoked); err != nil || !revoked {
		t.Errorf("after the reset A's personal token at %s is revoked %t, %v; want revoked", unapplied, revoked, err)
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
	output := r.clearHoldersRefusals(t, directory)
	for _, want := range []string{"HOLDER " + takeback + " h-takeback", "NO ACCOUNT " + alias, "NO ACCOUNT " + outside} {
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
		{alias, "n-alias", true},
		{outside, "o-outside", true},
	} {
		if got := r.refused(t, refusal.email, refusal.account); got != refusal.want {
			t.Errorf("after the directory block the broker refuses %s at %s %t, want %t", refusal.account, refusal.email, got, refusal.want)
		}
	}

	// Dispatch is started again, and each holder signs in.
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
