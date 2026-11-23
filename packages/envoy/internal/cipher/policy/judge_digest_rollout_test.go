package policy_test

import (
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/policy"
	"github.com/sjawhar/envoy/internal/cipher/policy/policytest"
	"github.com/sjawhar/envoy/internal/cipher/secrets"
)

// TestAJudgedProfileKeepsItsDigestWhenItsJudgeMovesToTheSourcesTag: the merge base 7c880c1556f6
// fingerprinted a proxy block that held "judge": 900 with that field in it, as a deployment's
// judge probe profile does. The first broker of this release reads the same profile with the
// field moved to its source key's judge tag of 900 (the rollout: tag the key, take the field out
// of the file, roll).
// Each request records the digest of the profile it was decided under
// (request_secrets.airlock_digest), and checkRelease ends a grant whose digest moved
// (machine_read.go, profileChanged), so a moved digest ends every live judged grant of the profile
// at the deploy, and its next call gets 407 GRANT_NOT_LIVE "profile changed".
//
// The two digests below are what the merge base's Profiles.Digest answered for
// {"name": "AIRLOCK_JUDGE_PROBE", "proxy": {"hosts": ["api.github.com"], "judge": 900}} (with or
// without a guidance field) and for the same block with no judge. The control row, a source with
// no judge tag, holds today; the judged row fails at 18d4a3a4, which answers the judge-less digest
// for both.
func TestAJudgedProfileKeepsItsDigestWhenItsJudgeMovesToTheSourcesTag(t *testing.T) {
	const (
		judged900AtBase = "373da3f5cd97349b105dfca21363d99fd226fca95c28c4dcfac77306eeae7965"
		noJudgeAtBase   = "a85e3fdf65048e27829d1fc7bc74873f8722eda9b5ef06ce41107415c2657761"
	)
	for _, row := range []struct {
		name  string
		judge string
		want  string
	}{
		{"control: a source with no judge tag keeps the judge-less digest", "", noJudgeAtBase},
		{"a source tagged judge 900 keeps the digest its block had with judge 900", "900", judged900AtBase},
	} {
		t.Run(row.name, func(t *testing.T) {
			source := policytest.Secret("AIRLOCK_JUDGE_PROBE", policy.OwnerShared, policy.TierHuman, "v")
			if row.judge != "" {
				source.Tags[policy.TagJudge] = row.judge
				source.Tags[policy.TagGuidance] = "grant a reason that holds k14-grant"
			}
			set := policytest.ProfiledCurrent(t, secrets.NewLocal(source), policytest.ProfilesFile(
				`{"name": "AIRLOCK_JUDGE_PROBE", "proxy": {"hosts": ["api.github.com"]}}`,
			)).Get()
			served, ok := set.Secrets["AIRLOCK_JUDGE_PROBE"]
			if !ok {
				t.Fatalf("AIRLOCK_JUDGE_PROBE was not served: %+v", set.Refused)
			}
			if served.ProfileDigest != row.want {
				t.Errorf("AIRLOCK_JUDGE_PROBE is served with profile digest %s, want %s, the digest the merge base gave the same profile; a live grant recorded under the base's digest ends at the first release on this broker", served.ProfileDigest, row.want)
			}
		})
	}
}
