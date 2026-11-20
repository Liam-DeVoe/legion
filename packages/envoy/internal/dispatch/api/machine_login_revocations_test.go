package api

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/agentsecrets"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// TestAMachineLoginPassRevokesWhatTheRemovalOwesWhileItsClaimMaySend: one owed revocation costs
// the person's list and a revoke for each login they approved for their own machines before the
// removal, and the pass asks before each of those calls whether its claim may still send one. Of
// erin's three logins issued before the removal, her one issued after it, and a service's login she
// approved, the pass revokes the three. A claim that may send two calls lists and revokes one, then
// stops with errRevocationWindowSpent, and the next claim, given the broker's list without the one
// it revoked, revokes the other two.
//
// Control: revoke every own login whatever its issued_at, and the row fails naming cred-after; send
// with no check, and the stopped pass revokes all three, named.
func TestAMachineLoginPassRevokesWhatTheRemovalOwesWhileItsClaimMaySend(t *testing.T) {
	rig := newFakeBrokerRig(t)
	client := agentsecrets.New(rig.server.URL, accountTokenFile(t, credentialTestAccountToken))
	removal := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	login := func(id, issued, service string) string {
		svc := "null"
		if service != "" {
			svc = `"` + service + `"`
		}
		return `{"credential_id":"` + id + `","host":"h","service":` + svc + `,"subject":null,"approved_by":"erin@other.example","issued_at":"` + issued + `","expires_at":"2026-12-20T00:00:00Z","expired":false}`
	}
	before := []string{
		login("cred-1", "2026-11-20T09:00:00Z", ""),
		login("cred-2", "2026-11-20T10:00:00Z", ""),
		login("cred-3", "2026-11-20T11:59:59Z", ""),
	}
	listing := func(logins ...string) string { return `{"credentials":[` + strings.Join(logins, ",") + `]}` }
	after := login("cred-after", "2026-11-20T12:00:01Z", "")
	service := login("cred-service", "2026-11-20T09:00:00Z", "legion-daemon")
	revokes := func() []string {
		return slices.DeleteFunc(slices.Clone(rig.routes), func(route string) bool { return !strings.HasSuffix(route, "/revoke-by-approver") })
	}
	owed := store.OwedMachineLoginRevocation{Email: "erin@other.example", Generation: 1, RecordedAt: removal}
	ctx := t.Context()

	rig.bodyByRoute["GET /v1/launcher-credentials"] = listing(append(slices.Clone(before), after, service)...)
	calls := 0
	twoCalls := func() (bool, error) { calls++; return calls <= 2, nil }
	revoked, err := revokeMachineLoginsOf(ctx, client, owed, twoCalls)
	if !errors.Is(err, errRevocationWindowSpent) || revoked != 1 || !slices.Equal(revokes(), []string{"POST /v1/launcher-credentials/cred-1/revoke-by-approver"}) {
		t.Fatalf("a claim that may send two calls revoked %d (%v), sending %v; want cred-1 alone and errRevocationWindowSpent", revoked, err, revokes())
	}

	rig.routes = nil
	rig.bodyByRoute["GET /v1/launcher-credentials"] = listing(before[1], before[2], after, service)
	always := func() (bool, error) { return true, nil }
	revoked, err = revokeMachineLoginsOf(ctx, client, owed, always)
	want := []string{"POST /v1/launcher-credentials/cred-2/revoke-by-approver", "POST /v1/launcher-credentials/cred-3/revoke-by-approver"}
	if err != nil || revoked != 2 || !slices.Equal(revokes(), want) {
		t.Errorf("the next claim revoked %d (%v), sending %v; want cred-2 and cred-3, and neither cred-after, issued after the removal, nor the service's", revoked, err, revokes())
	}
}
