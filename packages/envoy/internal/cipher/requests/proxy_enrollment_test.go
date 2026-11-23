package requests

import (
	"reflect"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/policy"
	"github.com/sjawhar/envoy/internal/cipher/policy/policytest"
	"github.com/sjawhar/envoy/internal/cipher/proxyrules"
)

// TestProxyGrantsOfEnrollmentAnswersEveryLiveGrantOfTheEnrollment: the proxy reads every live
// grant of a connection's enrollment, so a host any of them binds is read and a placeholder of any
// of them is swapped on one connection. Each grant is answered as a connection opened with one of
// its own placeholders would answer it (AirlockConnection), in grant id order. The controls: a
// grant of another enrollment, a grant that has been revoked, and a grant whose every name the
// session's operator withheld are not answered, and an enrollment with no grant answers none.
func TestProxyGrantsOfEnrollmentAnswersEveryLiveGrantOfTheEnrollment(t *testing.T) {
	m, enr, key, _ := newPlaceholderFixture(t, proxyrules.Rules{Secrets: []proxyrules.SecretRule{rule("AUTO_TOKEN", ""), rule("SECOND_TOKEN", "")}})
	retag(t, m, policytest.Secret("SECOND_TOKEN", fixtureOperator, policy.TierAgent, "second-v1"))
	_, first := grantOf(t, m, enr, key, "read the first for ACME-7", "AUTO_TOKEN")
	_, second := grantOf(t, m, enr, key, "read the second", "SECOND_TOKEN")
	other, otherKey := newEnrollment(t, m.Store, "session", "box-b-"+t.Name(), new(fixtureOperator), nil)
	_, foreign := grantOf(t, m, other, otherKey, "read it on another box", "AUTO_TOKEN")
	passwords := map[string]string{}
	for grantID, enrollmentID := range map[string]string{first: enr, second: enr, foreign: other} {
		release, err := m.Values(t.Context(), grantID, enrollmentID, ReleaseCall{})
		if err != nil {
			t.Fatalf("Values(%s): %v", grantID, err)
		}
		passwords[grantID] = release.Airlock.ProxyPassword
	}

	live, err := m.ProxyGrantsOfEnrollment(t.Context(), enr, "")
	if err != nil {
		t.Fatalf("ProxyGrantsOfEnrollment: %v", err)
	}
	want := slices.Sorted(slices.Values([]string{first, second}))
	if got := grantIDs(live); !reflect.DeepEqual(got, want) {
		t.Fatalf("the enrollment's live grants = %v; want %v, in grant id order, and not %s of another enrollment", got, want, foreign)
	}
	for _, grant := range live {
		opened, err := m.AirlockConnection(t.Context(), passwords[grant.GrantID])
		if err != nil {
			t.Fatalf("AirlockConnection(%s's placeholder): %v", grant.GrantID, err)
		}
		if !reflect.DeepEqual(grant, opened) {
			t.Errorf("grant %s = %+v; want what a connection opened with its placeholder answers, %+v", grant.GrantID, grant, opened)
		}
	}

	if err := m.RevokeGrant(t.Context(), second, enr); err != nil {
		t.Fatalf("RevokeGrant: %v", err)
	}
	live, err = m.ProxyGrantsOfEnrollment(t.Context(), enr, "")
	if err != nil {
		t.Fatalf("ProxyGrantsOfEnrollment after a revoke: %v", err)
	}
	if got := grantIDs(live); !reflect.DeepEqual(got, []string{first}) {
		t.Fatalf("the enrollment's live grants after %s was revoked = %v; want %s alone", second, got, first)
	}

	// Written directly, as TestAirlockConnectionDropsAWithheldName writes it: what is read is
	// withheld_secrets, and the grant is one the session got without asking.
	if _, err := m.Store.Pool.Exec(t.Context(), `insert into withheld_secrets (enrollment_id, name) values ($1,$2)`, enr, "AUTO_TOKEN"); err != nil {
		t.Fatal(err)
	}
	live, err = m.ProxyGrantsOfEnrollment(t.Context(), enr, "")
	if err != nil {
		t.Fatalf("ProxyGrantsOfEnrollment with AUTO_TOKEN withheld: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("the enrollment's live grants with every name withheld = %v; want none", grantIDs(live))
	}
	if still, err := m.ProxyGrantsOfEnrollment(t.Context(), other, ""); err != nil || !reflect.DeepEqual(grantIDs(still), []string{foreign}) {
		t.Fatalf("the other enrollment's live grants = %v, %v; want %s, which no withhold of this enrollment reaches", grantIDs(still), err, foreign)
	}
	if others, err := m.ProxyGrantsOfEnrollment(t.Context(), other, foreign); err != nil || len(others) != 0 {
		t.Fatalf("the other enrollment's live grants but %s = %v, %v; want none: the grant it names is left out", foreign, grantIDs(others), err)
	}

	empty, _ := newEnrollment(t, m.Store, "session", "box-c-"+t.Name(), new(fixtureOperator), nil)
	if none, err := m.ProxyGrantsOfEnrollment(t.Context(), empty, ""); err != nil || len(none) != 0 {
		t.Fatalf("an enrollment with no grant answered %v, %v; want none", grantIDs(none), err)
	}
}

// TestProxyPlaceholderGrantNamesTheGrantOfAPlaceholderLiveOrEnded: the proxy names, in the refusal
// of a request that still carries a placeholder after the swap, the grant that placeholder was
// minted for. A live grant's placeholder names its grant, and so does an ended grant's until the
// sweep deletes its rows; a placeholder the broker never minted names none, and so does an ended
// grant's once its rows are gone.
func TestProxyPlaceholderGrantNamesTheGrantOfAPlaceholderLiveOrEnded(t *testing.T) {
	m, enr, key, _ := newPlaceholderFixture(t, proxyrules.Rules{Secrets: []proxyrules.SecretRule{rule("AUTO_TOKEN", "")}})
	_, grantID := grantOf(t, m, enr, key, "read it", "AUTO_TOKEN")
	release, err := m.Values(t.Context(), grantID, enr, ReleaseCall{})
	if err != nil {
		t.Fatalf("Values: %v", err)
	}
	placeholder := release.Airlock.ProxyPassword
	named := func(what, placeholder, want string) {
		t.Helper()
		if got, err := m.ProxyPlaceholderGrant(t.Context(), placeholder); err != nil || got != want {
			t.Fatalf("ProxyPlaceholderGrant(%s) = %q, %v; want %q", what, got, err, want)
		}
	}
	named("a live grant's placeholder", placeholder, grantID)
	named("a placeholder the broker never minted", PlaceholderPrefix+"neverMintedByThisBrokerAtAll0000", "")
	if err := m.RevokeGrant(t.Context(), grantID, enr); err != nil {
		t.Fatalf("RevokeGrant: %v", err)
	}
	named("an ended grant's placeholder before the sweep", placeholder, grantID)
	if _, err := m.deleteSpentPlaceholders(t.Context()); err != nil {
		t.Fatalf("the sweep: %v", err)
	}
	named("an ended grant's placeholder after the sweep", placeholder, "")
}

// grantIDs is each grant's id, in the order given.
func grantIDs(grants []AirlockConnection) []string {
	ids := make([]string, len(grants))
	for i, grant := range grants {
		ids[i] = grant.GrantID
	}
	return ids
}
