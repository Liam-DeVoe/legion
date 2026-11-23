// packages/envoy/internal/cipher/requests/judge_tag_end_test.go
// What a later change of a key's judge tag does to a grant the model judge already made of it.
package requests

import (
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/policy"
	"github.com/sjawhar/envoy/internal/cipher/policy/policytest"
	"github.com/sjawhar/envoy/internal/cipher/secrets"
)

// TestAJudgeTagLoweredBelowAJudgedGrantOrTakenOffEndsItForGood: the broker's docs say that
// `agent-secrets secret retag --judge none` "ends each live grant the judge made of the secret at
// its next release", and a change of the seconds ends the grants it no longer admits. A max-grant
// tag lowered below a live grant does exactly that (TestAMaxGrantLoweredBelowAGrantAndRaisedBackLeavesItEnded):
// the release that finds the tag no longer admits the grant ends it in the store, so putting the
// tag back does not bring the grant back. A judge tag taken off the key, or lowered below the
// grant's lifetime, is held to the same rule here. The control row lowers the tag to exactly the
// grant's lifetime, which still admits it: that grant keeps releasing, before and after.
//
// At 3b501fde both changes only pause the grant: the release is refused as a broken chain
// (record.MayJudge, ahead of judgeStillDecides), nothing ends it, and once the tag is back at 900
// the same grant releases again.
func TestAJudgeTagLoweredBelowAJudgedGrantOrTakenOffEndsItForGood(t *testing.T) {
	deel := func(judge int) secrets.LocalSecret {
		s := policytest.Secret("DEEL_API_KEY", fixtureOperator, policy.TierHuman, "deel-v1")
		if judge == 0 {
			return s
		}
		return policytest.Judged(s, judge, "")
	}
	for name, c := range map[string]struct {
		judge int
		ends  bool
	}{
		"control: the tag lowered to exactly the grant's lifetime": {judge: 600},
		"the tag lowered below the grant's lifetime":               {judge: 599, ends: true},
		"the tag taken off the key":                                {judge: 0, ends: true},
	} {
		t.Run(name, func(t *testing.T) {
			m, session, key := newJudgeFixture(t)
			judged := judgedGrant(t, m, session, key, 600, "DEEL_API_KEY")
			if release, err := m.Values(t.Context(), judged, session, ReleaseCall{}); err != nil || releasedValue(release, "DEEL_API_KEY") == "" {
				t.Fatalf("Values before the retag = %+v, %v; want DEEL_API_KEY released", release, err)
			}
			retag(t, m, deel(c.judge))
			release, err := m.Values(t.Context(), judged, session, ReleaseCall{})
			if !c.ends {
				if err != nil || releasedValue(release, "DEEL_API_KEY") == "" {
					t.Fatalf("Values under judge %d = %+v, %v; want DEEL_API_KEY released: the tag still admits the grant", c.judge, release, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Values under judge %d = %+v; want the grant refused", c.judge, release)
			}
			retag(t, m, deel(900))
			if release, err := m.Values(t.Context(), judged, session, ReleaseCall{}); err == nil || releasedValue(release, "DEEL_API_KEY") != "" {
				t.Fatalf("Values once the tag is back at 900 = %+v, %v; want the grant to stay ended, as the docs say the change ended it", release, err)
			}
			if by, audits := endedForGood(t, m, judged); by != GrantEndedActor || audits != 1 {
				t.Fatalf("grant revoked_by = %q with %d grant.revoked rows by it; want %s and 1: the change of the judge tag ends the grant in the store", by, audits, GrantEndedActor)
			}
		})
	}
}
