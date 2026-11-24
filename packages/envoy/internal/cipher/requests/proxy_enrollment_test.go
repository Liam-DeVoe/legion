package requests

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/policy"
	"github.com/sjawhar/envoy/internal/cipher/policy/policytest"
	"github.com/sjawhar/envoy/internal/cipher/proxyrules"
)

// TestProxyGrantsOfEnrollmentAnswersEveryLiveGrantOfTheEnrollment: the proxy reads the live grants
// of a connection's enrollment that hold a name bound to the host, so a host any of them binds is
// read and a placeholder of any of them is swapped on one connection. Each grant is answered as a
// connection opened with one of its own placeholders would answer it (AirlockConnection), in grant
// id order. The controls: a grant of another enrollment, a grant that has been revoked, a grant
// that holds none of the names asked about, and a grant whose every name the session's operator
// withheld are not answered, and an enrollment with no grant, or a read naming no name, answers
// none.
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

	both := []string{"AUTO_TOKEN", "SECOND_TOKEN"}
	live, err := m.ProxyGrantsOfEnrollment(t.Context(), enr, "", both)
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
	if got, err := m.ProxyGrantsOfEnrollment(t.Context(), enr, "", []string{"SECOND_TOKEN"}); err != nil || !reflect.DeepEqual(grantIDs(got), []string{second}) {
		t.Fatalf("the enrollment's live grants holding SECOND_TOKEN = %v, %v; want %s alone: %s holds no name asked about", grantIDs(got), err, second, first)
	}
	if none, err := m.ProxyGrantsOfEnrollment(t.Context(), enr, "", nil); err != nil || len(none) != 0 {
		t.Fatalf("a read naming no name answered %v, %v; want none", grantIDs(none), err)
	}

	if err := m.RevokeGrant(t.Context(), second, enr); err != nil {
		t.Fatalf("RevokeGrant: %v", err)
	}
	live, err = m.ProxyGrantsOfEnrollment(t.Context(), enr, "", both)
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
	live, err = m.ProxyGrantsOfEnrollment(t.Context(), enr, "", both)
	if err != nil {
		t.Fatalf("ProxyGrantsOfEnrollment with AUTO_TOKEN withheld: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("the enrollment's live grants with every name withheld = %v; want none", grantIDs(live))
	}
	if still, err := m.ProxyGrantsOfEnrollment(t.Context(), other, "", both); err != nil || !reflect.DeepEqual(grantIDs(still), []string{foreign}) {
		t.Fatalf("the other enrollment's live grants = %v, %v; want %s, which no withhold of this enrollment reaches", grantIDs(still), err, foreign)
	}
	if others, err := m.ProxyGrantsOfEnrollment(t.Context(), other, foreign, both); err != nil || len(others) != 0 {
		t.Fatalf("the other enrollment's live grants but %s = %v, %v; want none: the grant it names is left out", foreign, grantIDs(others), err)
	}

	empty, _ := newEnrollment(t, m.Store, "session", "box-c-"+t.Name(), new(fixtureOperator), nil)
	if none, err := m.ProxyGrantsOfEnrollment(t.Context(), empty, "", both); err != nil || len(none) != 0 {
		t.Fatalf("an enrollment with no grant answered %v, %v; want none", grantIDs(none), err)
	}
}

// TestAGrantHoldingNoBoundNameIsNotCheckedWhileDispatchIsDown: the proxy reads the session's other
// grants for one host, and a grant that holds no name bound to it is left out before its project
// check, which asks Dispatch. So while Dispatch cannot answer, a read for AUTO_TOKEN's host answers
// the AUTO_TOKEN grant beside a live TEAM_TOKEN grant of the same session, where reading every
// grant would fail the whole read for TEAM's sake - a CONNECT to a host the TEAM grant has no part
// in answered 503. The control: a read whose names include TEAM_TOKEN still fails closed,
// ErrProjectAccessUnavailable, and once Dispatch answers it answers both grants.
func TestAGrantHoldingNoBoundNameIsNotCheckedWhileDispatchIsDown(t *testing.T) {
	m, enr, key, _ := newProjectFixture(t, proxyrules.Rules{Secrets: []proxyrules.SecretRule{rule("AUTO_TOKEN", ""), rule("TEAM_TOKEN", "")}}, publicTeam)
	_, untagged := grantOf(t, m, enr, key, "read it", "AUTO_TOKEN")
	_, tagged := grantOf(t, m, enr, key, "read it", "TEAM_TOKEN")
	for _, grantID := range []string{untagged, tagged} {
		if _, err := m.Values(t.Context(), grantID, enr, ReleaseCall{}); err != nil {
			t.Fatalf("Values(%s): %v", grantID, err)
		}
	}
	dispatch := answerFrom(t, m, dispatchRows(publicTeam))
	dispatch.SetProjectsDown(true)

	if got, err := m.ProxyGrantsOfEnrollment(t.Context(), enr, "", []string{"AUTO_TOKEN"}); err != nil || !reflect.DeepEqual(grantIDs(got), []string{untagged}) {
		t.Fatalf("the grants holding AUTO_TOKEN while Dispatch is down = %v, %v; want %s alone: the TEAM grant holds no name asked about, so its project is not asked", grantIDs(got), err, untagged)
	}
	if got, err := m.ProxyGrantsOfEnrollment(t.Context(), enr, "", []string{"AUTO_TOKEN", "TEAM_TOKEN"}); !errors.Is(err, ErrProjectAccessUnavailable) {
		t.Fatalf("the grants holding AUTO_TOKEN or TEAM_TOKEN while Dispatch is down = %v, %v; want ErrProjectAccessUnavailable: the TEAM grant could take the call", grantIDs(got), err)
	}
	dispatch.SetProjectsDown(false)
	want := slices.Sorted(slices.Values([]string{untagged, tagged}))
	if got, err := m.ProxyGrantsOfEnrollment(t.Context(), enr, "", []string{"AUTO_TOKEN", "TEAM_TOKEN"}); err != nil || !reflect.DeepEqual(grantIDs(got), want) {
		t.Fatalf("the grants holding AUTO_TOKEN or TEAM_TOKEN once Dispatch answers = %v, %v; want %v", grantIDs(got), err, want)
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
