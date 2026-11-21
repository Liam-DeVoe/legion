package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/cipher"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// TestAMachineLoginPassRevokesWhatTheRemovalOwesWhileItsClaimMaySend: one owed revocation costs
// the person's list and a revoke for each login they approved for their own machines, and the pass
// asks before each of those calls whether its claim may still send one. Of erin's four logins and a
// service's login she approved, the pass revokes the four, whatever each one's issued_at: no
// approval of hers was relayed since the removal (store.HoldMachineLoginApprovals). A claim that may
// send two calls lists and revokes one, then stops with errRevocationWindowSpent, and the next
// claim, given the broker's list without the one it revoked, revokes the other three.
//
// Control: send with no check, and the stopped pass revokes all four, named.
func TestAMachineLoginPassRevokesWhatTheRemovalOwesWhileItsClaimMaySend(t *testing.T) {
	rig := newFakeBrokerRig(t)
	client := cipher.New(rig.server.URL, accountTokenFile(t, credentialTestAccountToken))
	removal := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	login := func(id, issued, service string) string {
		svc := "null"
		if service != "" {
			svc = `"` + service + `"`
		}
		return `{"credential_id":"` + id + `","host":"h","service":` + svc + `,"subject":null,"approved_by":"erin@other.example","issued_at":"` + issued + `","expires_at":"2026-12-20T00:00:00Z","expired":false}`
	}
	own := []string{
		login("cred-1", "2026-11-20T09:00:00Z", ""),
		login("cred-2", "2026-11-20T10:00:00Z", ""),
		login("cred-3", "2026-11-20T11:59:59Z", ""),
		login("cred-4", "2026-11-20T12:00:01Z", ""),
	}
	listing := func(logins ...string) string { return `{"credentials":[` + strings.Join(logins, ",") + `]}` }
	service := login("cred-service", "2026-11-20T09:00:00Z", "legion-daemon")
	owed := store.OwedMachineLoginRevocation{Email: "erin@other.example", Generation: 1, RecordedAt: removal}
	ctx := t.Context()

	rig.bodyByRoute["GET /v1/launcher-credentials"] = listing(append(slices.Clone(own), service)...)
	calls := 0
	twoCalls := func() (bool, error) { calls++; return calls <= 2, nil }
	revoked, err := revokeMachineLoginsOf(ctx, client, owed, twoCalls)
	if !errors.Is(err, errRevocationWindowSpent) || revoked != 1 || !slices.Equal(rig.revokes(), []string{"POST /v1/launcher-credentials/cred-1/revoke-by-approver"}) {
		t.Fatalf("a claim that may send two calls revoked %d (%v), sending %v; want cred-1 alone and errRevocationWindowSpent", revoked, err, rig.revokes())
	}

	rig.routes = nil
	rig.bodyByRoute["GET /v1/launcher-credentials"] = listing(own[1], own[2], own[3], service)
	always := func() (bool, error) { return true, nil }
	revoked, err = revokeMachineLoginsOf(ctx, client, owed, always)
	want := []string{"POST /v1/launcher-credentials/cred-2/revoke-by-approver", "POST /v1/launcher-credentials/cred-3/revoke-by-approver", "POST /v1/launcher-credentials/cred-4/revoke-by-approver"}
	if err != nil || revoked != 3 || !slices.Equal(rig.revokes(), want) {
		t.Errorf("the next claim revoked %d (%v), sending %v; want cred-2, cred-3 and cred-4, and not the service's", revoked, err, rig.revokes())
	}
}

// TestARemovalRevokesALoginTheBrokersClockStampsAfterIt: the broker stamps a login's issued_at on
// its own clock, and a removal's recorded_at is Dispatch's database's. carol takes erin, a
// restricted member ACC's list still names, off OTH's list, and the broker, whose clock runs an hour
// ahead of Dispatch's, lists erin's login approved before the removal as issued an hour after it.
// The pass revokes it, comparing no clock, and the revocation is settled.
//
// Control: revoke only the logins issued before the removal's recorded_at, as the head before this
// row did, and the pass revokes nothing, named.
func TestARemovalRevokesALoginTheBrokersClockStampsAfterIt(t *testing.T) {
	rig := newFakeBrokerRig(t)
	served, database, registered := newRegisteredAccessServer(t, testServerOptions{
		signInOutside:   true,
		agentSecretsURL: rig.server.URL, agentSecretsTokenFile: accountTokenFile(t, credentialTestAccountToken),
	})
	ctx := t.Context()
	if created := dispatchRequest(t, served, http.MethodPost, "/api/v1/projects", map[string]string{"key": "OTH", "name": "Another project"}, accessCarol); created.Code != http.StatusCreated {
		t.Fatalf("carol creates OTH: %d %s", created.Code, created.Body.String())
	}
	for _, write := range []struct{ method, key, owner, does string }{
		{http.MethodPut, "ACC", "alice", "names erin on"},
		{http.MethodPut, "OTH", accessCarol, "names erin on"},
		{http.MethodDelete, "OTH", accessCarol, "takes erin off"},
	} {
		if response := dispatchRequest(t, served, write.method, "/api/v1/projects/"+write.key+"/access/people/"+accessErin, nil, write.owner); response.Code != http.StatusOK {
			t.Fatalf("%s %s %s's list: status=%d body=%s", write.owner, write.does, write.key, response.Code, response.Body.String())
		}
	}
	ahead := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	rig.bodyByRoute["GET /v1/launcher-credentials"] = `{"credentials":[{"credential_id":"cred-erin","host":"erin-laptop","service":null,"subject":null,"approved_by":"erin@other.example","issued_at":"` + ahead + `","expires_at":"2026-12-20T00:00:00Z","expired":false}]}`

	registered.revokeBrokerDebts(ctx)
	if !slices.Equal(rig.revokes(), []string{"POST /v1/launcher-credentials/cred-erin/revoke-by-approver"}) {
		t.Errorf("the pass sent %v for erin, her login stamped an hour after the removal on the broker's clock; want it revoked", rig.revokes())
	}
	if owed, _, err := store.NewMachineLoginRevocations(database.Pool).Owing(ctx, accessErin); err != nil || owed {
		t.Errorf("the revocation of erin's machine logins is owed %t (%v) after the pass, want it settled", owed, err)
	}
}

// TestALoginApprovedInFlightAsTheRemovalCommitsIsRevoked: erin's browser approve of her own machine
// login reaches the broker, which has not answered it when alice takes her off ACC's list. The
// approval holds erin's approval lock until the broker answers (relayMachineLoginApproval), so the
// removal waits for it, and records the revocation only after the login exists; the pass then lists
// it and revokes it, and the revocation is settled.
//
// Control: record the revocation without taking the person's approval lock, and the removal commits
// while the approval is in flight; the pass that follows lists no login and settles, the login the
// approval then mints is never revoked, named.
func TestALoginApprovedInFlightAsTheRemovalCommitsIsRevoked(t *testing.T) {
	rig := newFakeBrokerRig(t)
	served, database, registered := newRegisteredAccessServer(t, testServerOptions{
		signInOutside:   true,
		agentSecretsURL: rig.server.URL, agentSecretsTokenFile: accountTokenFile(t, credentialTestAccountToken),
	})
	ctx := t.Context()
	if listed := dispatchRequest(t, served, http.MethodPut, "/api/v1/projects/ACC/access/people/"+accessErin, nil, "alice"); listed.Code != http.StatusOK {
		t.Fatalf("alice names erin on ACC's list: status=%d body=%s", listed.Code, listed.Body.String())
	}
	const (
		list    = "GET /v1/launcher-credentials"
		approve = "POST /v1/credential-requests/rec-erin/approve"
	)
	rig.bodyByRoute["POST /v1/machine-logins/lookup"] = erinsOwnLogin
	rig.bodyByRoute[list] = `{"credentials":[]}`
	reached, release := make(chan struct{}), make(chan struct{})
	rig.beforeAnswer[approve] = func() {
		close(reached)
		<-release
	}
	approved := make(chan int, 1)
	go func() {
		approved <- dispatchRequest(t, served, http.MethodPost, "/api/v1/credential-requests/rec-erin/approve", map[string]any{"code": "ABCD-1234"}, accessErin).Code
	}()
	<-reached
	removed := make(chan int, 1)
	go func() {
		removed <- dispatchRequest(t, served, http.MethodDelete, "/api/v1/projects/ACC/access/people/"+accessErin, nil, "alice").Code
	}()
	waitingOnALock := func() bool {
		var n int
		if err := database.Pool.QueryRow(ctx, `select count(*) from pg_stat_activity
			where datname = current_database() and pid <> pg_backend_pid() and wait_event_type = 'Lock' and wait_event = 'advisory'`).Scan(&n); err != nil {
			t.Fatalf("read whether the removal waits on a lock: %v", err)
		}
		return n > 0
	}
	removedEarly := 0
	for giveUp := time.Now().Add(30 * time.Second); removedEarly == 0 && !waitingOnALock(); time.Sleep(20 * time.Millisecond) {
		select {
		case removedEarly = <-removed:
			// The removal did not wait: a pass now finds no login of erin's and settles.
			registered.revokeBrokerDebts(ctx)
		default:
			if time.Now().After(giveUp) {
				t.Fatalf("alice's removal of erin neither finished nor waited on a lock within 30 s")
			}
		}
	}
	rig.bodyByRoute[list] = `{"credentials":[{"credential_id":"cred-in-flight","host":"erin-desktop","service":null,"subject":null,"approved_by":"erin@other.example","issued_at":"` + time.Now().UTC().Format(time.RFC3339) + `","expires_at":"2026-12-20T00:00:00Z","expired":false}]}`
	close(release)
	if code := <-approved; code != http.StatusOK {
		t.Fatalf("erin's approve in flight answered %d, want 200: it was admitted before the removal", code)
	}
	if removedEarly == 0 {
		removedEarly = <-removed
	}
	if removedEarly != http.StatusOK {
		t.Fatalf("alice's removal of erin answered %d, want 200", removedEarly)
	}

	registered.revokeBrokerDebts(ctx)
	if !slices.Contains(rig.revokes(), "POST /v1/launcher-credentials/cred-in-flight/revoke-by-approver") {
		t.Errorf("the broker was sent %v; want the login erin's approval in flight minted revoked", rig.revokes())
	}
	if owed, _, err := store.NewMachineLoginRevocations(database.Pool).Owing(ctx, accessErin); err != nil || owed {
		t.Errorf("the revocation of erin's machine logins is owed %t (%v) after the pass, want it settled", owed, err)
	}
}

// TestAClaimedMachineLoginRevocationPastItsSendWindowSendsNoRevoke: the loop's pass asks the store
// before each broker call whether its claim may still send one (store.MachineLoginRevocations'
// Sendable). alice takes erin, a restricted member, off ACC's list, and the pass claims the
// revocation that owes and lists erin's machine logins. While the list is in flight the claim ages
// past the retry window less the broker client's bound on a call (its tried_at set back that far and
// a second more, as a slow list would leave it), so the pass sends no revoke of erin's login, which
// another process may claim again before such a call is over, and the revocation stays owed for the
// next claim. The list reaching the broker is the row's own control: the pass ran, and its claim
// was within its window then.
//
// Control: revokeOwedMachineLogins' mayCall answering true whatever the claim's age, and the pass
// revokes erin's login, named.
func TestAClaimedMachineLoginRevocationPastItsSendWindowSendsNoRevoke(t *testing.T) {
	rig := newFakeBrokerRig(t)
	served, database, registered := newRegisteredAccessServer(t, testServerOptions{
		signInOutside:   true,
		agentSecretsURL: rig.server.URL, agentSecretsTokenFile: accountTokenFile(t, credentialTestAccountToken),
	})
	ctx := t.Context()
	for _, write := range []struct{ method, does string }{{http.MethodPut, "names erin on"}, {http.MethodDelete, "takes erin off"}} {
		if response := dispatchRequest(t, served, write.method, "/api/v1/projects/ACC/access/people/"+accessErin, nil, "alice"); response.Code != http.StatusOK {
			t.Fatalf("alice %s ACC's list: status=%d body=%s", write.does, response.Code, response.Body.String())
		}
	}
	const list = "GET /v1/launcher-credentials"
	rig.bodyByRoute[list] = `{"credentials":[{"credential_id":"cred-erin","host":"erin-laptop","service":null,"subject":null,"approved_by":"erin@other.example","issued_at":"2026-11-20T00:00:00Z","expires_at":"2026-12-20T00:00:00Z","expired":false}]}`
	aged := brokerRevocationRetry - registered.deps.AgentSecrets.HTTP.Timeout + time.Second
	rig.beforeAnswer[list] = func() {
		if _, err := database.Pool.Exec(ctx, `update machine_login_revocations set tried_at = tried_at - make_interval(secs => $1)`, aged.Seconds()); err != nil {
			t.Errorf("age the claim while the list is in flight: %v", err)
		}
	}

	registered.revokeBrokerDebts(ctx)
	if !slices.Contains(rig.routes, list) {
		t.Fatalf("the pass sent %v, want erin's list first: the claim was within its window then", rig.routes)
	}
	if revokes := rig.revokes(); len(revokes) != 0 {
		t.Errorf("the pass sent %v after its claim aged past its window during the list, want no revoke", revokes)
	}
	if owed, _, err := store.NewMachineLoginRevocations(database.Pool).Owing(ctx, accessErin); err != nil || !owed {
		t.Errorf("the revocation of erin's machine logins is owed %t (%v) after the pass, want it still owed", owed, err)
	}
}
