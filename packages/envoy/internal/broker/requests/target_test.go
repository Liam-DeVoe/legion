// packages/envoy/internal/broker/requests/target_test.go
// A request's target: the GitHub account, or repositories of one account, that its signed
// locations narrow its one GitHub profile to (record.AuthorizationDetail.Target). The record a
// person approves holds it, request_secrets records it, a live grant or a pending request is used
// again only for the same profile and the same target, and a release mints the target's scope.
package requests

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/policy"
	"github.com/sjawhar/envoy/internal/broker/policy/policytest"
	"github.com/sjawhar/envoy/internal/broker/record"
	"github.com/sjawhar/envoy/internal/broker/secrets"
)

// signTargetRequest signs a request object for the one name, whose locations are locations (none
// for a request that names no target), ready for Machine.Create.
func signTargetRequest(t *testing.T, m *Machine, key *ecdsa.PrivateKey, name string, locations ...string) string {
	t.Helper()
	compact, err := record.Sign(key, m.Audience, []record.AuthorizationDetail{
		{Type: record.KindAgentSecret, Identifier: name, Actions: []string{"inject"}, Locations: locations},
	}, "read the repository", "", nil, time.Now())
	if err != nil {
		t.Fatalf("record.Sign: %v", err)
	}
	return compact
}

// targetFixture is newAppFixture serving APP_KEY, a github-app secret of tier, through the
// profiles of file, or, for "", through policytest.AppProfile's profile of its own name.
func targetFixture(t *testing.T, tier, file string) (*Machine, string, *ecdsa.PrivateKey) {
	t.Helper()
	m, enrollmentID, key, _ := newAppFixture(t, "APP_KEY")
	fixtureStore(m).Put(requestTypeSecret("APP_KEY", policy.TypeGitHubApp, tier, ""))
	if file == "" {
		file = policytest.ProfilesFile(policytest.AppProfile("APP_KEY"))
	}
	m.Policy = policytest.ProfiledCurrent(t, fixtureStore(m), file, fixtureService)
	return m, enrollmentID, key
}

// recordedTarget is the target request_secrets holds for the name of requestID.
func recordedTarget(t *testing.T, m *Machine, requestID, name string) (*string, []string) {
	t.Helper()
	var account *string
	var repositories []string
	if err := m.Store.Pool.QueryRow(t.Context(), `select target_account, target_repositories from request_secrets where request_id=$1 and name=$2`, requestID, name).Scan(&account, &repositories); err != nil {
		t.Fatalf("read the recorded target: %v", err)
	}
	return account, repositories
}

// TestTargetIsALocationOfTheSignedRequest: the target travels as the locations of the signed
// request's one authorization detail, so the record a person approves holds it: the body Create
// stores embeds the request object, and that object, verified again, names the same locations.
// request_secrets records the target the record holds, the account and the repositories' names,
// which the release and the reuse of a grant read; a request with no target records none.
func TestTargetIsALocationOfTheSignedRequest(t *testing.T) {
	m, enrollmentID, key := targetFixture(t, policy.TierHuman, "")
	locations := []string{"https://github.com/ada/proofs", "https://github.com/ada/lean"}
	request, err := m.Create(t.Context(), enrollmentID, signTargetRequest(t, m, key, "APP_KEY", locations...), "")
	if err != nil || request.State != "pending" || request.RecordID == nil {
		t.Fatalf("Create(APP_KEY --target ada/proofs --target ada/lean) = %+v, %v; want it pending with a record", request, err)
	}
	var canonical string
	var createdAt time.Time
	if err := m.Store.Pool.QueryRow(t.Context(), `select body, created_at from credential_requests where id=$1`, *request.RecordID).Scan(&canonical, &createdAt); err != nil {
		t.Fatalf("read the record: %v", err)
	}
	body, err := record.ParseBody(canonical)
	if err != nil {
		t.Fatalf("ParseBody: %v", err)
	}
	obj, err := record.VerifyRequestObject(body.Request, m.Audience, m.Skew, createdAt)
	if err != nil {
		t.Fatalf("the record's request object does not verify: %v", err)
	}
	if len(obj.Details) != 1 || !slices.Equal(obj.Details[0].Locations, locations) {
		t.Fatalf("the record's authorization details = %+v, want one naming the locations %q", obj.Details, locations)
	}
	account, repositories := recordedTarget(t, m, request.ID, "APP_KEY")
	if account == nil || *account != "ada" || !slices.Equal(repositories, []string{"lean", "proofs"}) {
		t.Fatalf("request_secrets records the target %v %q, want ada and [lean proofs]", account, repositories)
	}

	plain, err := m.Create(t.Context(), enrollmentID, signRequest(t, m, key, "read", "DEEL_API_KEY"), "")
	if err != nil {
		t.Fatalf("Create(DEEL_API_KEY): %v", err)
	}
	if account, repositories := recordedTarget(t, m, plain.ID, "DEEL_API_KEY"); account != nil || repositories != nil {
		t.Fatalf("a request with no target records %v %q, want none", account, repositories)
	}
}

// TestALiveGrantIsReusedOnlyForTheSameProfileAndTarget: a session that asks again for the
// profile it holds a live grant of gets that grant back only when its new request names the same
// target: the same account, the same repositories, or no target as before. Another target of the
// same profile, and the same target of another profile of the same source, are new requests with
// grants of their own, since each grant releases its own scope.
func TestALiveGrantIsReusedOnlyForTheSameProfileAndTarget(t *testing.T) {
	m, enrollmentID, key := targetFixture(t, policy.TierAgent, policytest.ProfilesFile(
		policytest.AppProfile("APP_KEY"),
		`{"name": "APP_KEY_ADA", "source": "APP_KEY", "github": {"accounts": ["ada"], "permissions": {"metadata": "read"}}}`,
	))
	asks := []struct {
		name      string
		profile   string
		locations []string
	}{
		{"an account", "APP_KEY", []string{"https://github.com/ada"}},
		{"another account", "APP_KEY", []string{"https://github.com/acme"}},
		{"a repository of the account", "APP_KEY", []string{"https://github.com/ada/proofs"}},
		{"two repositories of the account", "APP_KEY", []string{"https://github.com/ada/proofs", "https://github.com/ada/lean"}},
		{"no target", "APP_KEY", nil},
		{"the same account through another profile", "APP_KEY_ADA", []string{"https://github.com/ada"}},
	}
	grants := map[string]string{}
	for _, ask := range asks {
		request, err := m.Create(t.Context(), enrollmentID, signTargetRequest(t, m, key, ask.profile, ask.locations...), "")
		if err != nil || request.GrantID == nil {
			t.Fatalf("Create(%s, %s) = %+v, %v; want a grant", ask.profile, ask.name, request, err)
		}
		for other, grant := range grants {
			if grant == *request.GrantID {
				t.Fatalf("Create(%s, %s) got the grant of %s back; want a grant of its own", ask.profile, ask.name, other)
			}
		}
		grants[ask.name] = *request.GrantID
	}
	for _, ask := range asks {
		// The same repositories in another order are the same target.
		locations := slices.Clone(ask.locations)
		slices.Reverse(locations)
		again, err := m.Create(t.Context(), enrollmentID, signTargetRequest(t, m, key, ask.profile, locations...), "")
		if err != nil || again.GrantID == nil || *again.GrantID != grants[ask.name] {
			t.Fatalf("Create(%s, %s) again = %+v, %v; want its live grant %s back", ask.profile, ask.name, again, err, grants[ask.name])
		}
	}
	var requests int
	if err := m.Store.Pool.QueryRow(t.Context(), `select count(*) from requests`).Scan(&requests); err != nil || requests != len(asks) {
		t.Fatalf("the broker holds %d request rows, %v; want %d, one for each target", requests, err, len(asks))
	}
}

// TestAPendingRequestIsJoinedOnlyForTheSameTarget: a second ask for a profile while the first
// waits for its approver joins that request only when it names the same target, since the grant
// the approval makes releases the first request's target. One of another target is refused as a
// second pending request for the same name (ErrRequestPending), as any other overlap is.
func TestAPendingRequestIsJoinedOnlyForTheSameTarget(t *testing.T) {
	m, enrollmentID, key := targetFixture(t, policy.TierHuman, "")
	first, err := m.Create(t.Context(), enrollmentID, signTargetRequest(t, m, key, "APP_KEY", "https://github.com/ada"), "")
	if err != nil || first.State != "pending" {
		t.Fatalf("Create(APP_KEY --target ada) = %+v, %v; want it pending", first, err)
	}
	joined, err := m.Create(t.Context(), enrollmentID, signTargetRequest(t, m, key, "APP_KEY", "https://github.com/ada"), "")
	if err != nil || joined.ID != first.ID || !joined.Coalesced {
		t.Fatalf("Create(APP_KEY --target ada) again = %+v, %v; want it joined to %s", joined, err, first.ID)
	}
	for name, locations := range map[string][]string{
		"another account": {"https://github.com/acme"},
		"a repository":    {"https://github.com/ada/proofs"},
		"no target":       nil,
	} {
		other, err := m.Create(t.Context(), enrollmentID, signTargetRequest(t, m, key, "APP_KEY", locations...), "")
		if !errors.Is(err, ErrRequestPending) || other.ID != "" {
			t.Fatalf("Create(APP_KEY, %s) = %+v, %v; want ErrRequestPending, not the request of another target", name, other, err)
		}
	}
}

// TestAReleaseMintsTheTargetsScope: a release of a grant whose request names a target asks the
// minter for the target's account alone, with the target's repositories, or, for an account
// target, the profile's repositories of that account: a target narrows the profile and never
// widens it. A request with no target asks for the profile's own scope, its repositories of
// both accounts among them, each as account/name, so the minter can send each account its own.
func TestAReleaseMintsTheTargetsScope(t *testing.T) {
	m, enrollmentID, key := targetFixture(t, policy.TierAgent, policytest.ProfilesFile(
		`{"name": "APP_KEY", "github": {"accounts": ["acme", "ada"], "repositories": ["ada/lean", "ada/proofs", "acme/site"], "permissions": {"contents": "read"}}}`,
	))
	var asked []secrets.GitHubScope
	m.Minters = map[string]Minter{
		policy.TypeGitHubApp: func(_ context.Context, req secrets.MintRequest) (secrets.Minted, error) {
			asked = append(asked, *req.GitHub)
			return secrets.Minted{Value: "token"}, nil
		},
	}
	for _, row := range []struct {
		name      string
		locations []string
		want      secrets.GitHubScope
	}{
		{"no target", nil, secrets.GitHubScope{Accounts: []string{"acme", "ada"}, Repositories: []string{"acme/site", "ada/lean", "ada/proofs"}}},
		{"an account", []string{"https://github.com/ada"}, secrets.GitHubScope{Accounts: []string{"ada"}, Repositories: []string{"ada/lean", "ada/proofs"}}},
		{"the other account", []string{"https://github.com/acme"}, secrets.GitHubScope{Accounts: []string{"acme"}, Repositories: []string{"acme/site"}}},
		{"a repository", []string{"https://github.com/ada/proofs"}, secrets.GitHubScope{Accounts: []string{"ada"}, Repositories: []string{"ada/proofs"}}},
	} {
		t.Run(row.name, func(t *testing.T) {
			asked = nil
			request, err := m.Create(t.Context(), enrollmentID, signTargetRequest(t, m, key, "APP_KEY", row.locations...), "")
			if err != nil || request.GrantID == nil {
				t.Fatalf("Create = %+v, %v; want a grant", request, err)
			}
			if _, err := m.Values(t.Context(), *request.GrantID, enrollmentID, ReleaseCall{}); err != nil {
				t.Fatalf("Values: %v", err)
			}
			row.want.Permissions = map[string]string{"contents": "read"}
			row.want.Value = policy.ValueInstallations
			if want := []secrets.GitHubScope{row.want}; !reflect.DeepEqual(asked, want) {
				t.Fatalf("the minter was asked for %+v, want the target's scope %+v", asked, want)
			}
		})
	}
}
