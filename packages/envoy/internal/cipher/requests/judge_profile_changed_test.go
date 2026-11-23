package requests

import (
	"errors"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/policy"
	"github.com/sjawhar/envoy/internal/cipher/policy/policytest"
	"github.com/sjawhar/envoy/internal/cipher/record"
	"github.com/sjawhar/envoy/internal/cipher/secrets"
)

// TestAProfileChangedRefusalNamesTheSourcesJudgeTag: a profile with a proxy block carries its
// source key's judge seconds in its fingerprint, so a judged grant of it that its new tag still
// admits (a 300-second grant once the tag goes from 900 to 600) ends as a profile changed. The
// person changed a tag, not the file, so where the tag can be what moved - the source carries one
// now, or the judge decided the grant, so it carried one then - the refusal names the source key's
// judge tag beside the entry. A person's grant of a profile whose source carries no judge tag, and
// a secret served by its own name, read as before.
func TestAProfileChangedRefusalNamesTheSourcesJudgeTag(t *testing.T) {
	judgedSource := policytest.Judged(policytest.Secret("RELEASE_PAT", fixtureOperator, policy.TierHuman, "v"), 600, "")
	plainSource := policytest.Secret("PLAIN_PAT", fixtureOperator, policy.TierHuman, "v")
	stored := policytest.Secret("PLAIN_KEY", fixtureOperator, policy.TierHuman, "v")
	set := policytest.ProfiledCurrent(t, secrets.NewLocal(judgedSource, plainSource, stored), policytest.ProfilesFile(
		`{"name": "RELEASE_READ", "source": "RELEASE_PAT", "proxy": {"hosts": ["api.github.com"]}}`,
		`{"name": "PLAIN_READ", "source": "PLAIN_PAT", "proxy": {"hosts": ["api.github.com"]}}`,
	)).Get()
	const tagged = "'s profile changed since this grant was decided: its entry in the profiles file, or the judge tag of its source key "
	const plain = "'s profile changed since this grant was decided"
	judge := record.JudgeActor("the-model")
	for _, c := range []struct {
		row, name, decidedBy, want string
	}{
		{"a source tagged judge now, a person's grant", "RELEASE_READ", fixtureOperator, "RELEASE_READ" + tagged + "RELEASE_PAT"},
		{"a source with no judge tag now, the judge's grant", "PLAIN_READ", judge, "PLAIN_READ" + tagged + "PLAIN_PAT"},
		{"a source with no judge tag now, a person's grant", "PLAIN_READ", fixtureOperator, "PLAIN_READ" + plain},
		{"a secret served by its own name, the judge's grant", "PLAIN_KEY", judge, "PLAIN_KEY" + plain},
	} {
		if _, served := set.Secrets[c.name]; !served {
			t.Fatalf("%s was not served: %+v", c.name, set.Refused)
		}
		err := profileChanged(set, c.name, c.decidedBy)
		if !errors.Is(err, ErrGrantNotLive) || !strings.HasSuffix(err.Error(), c.want) {
			t.Errorf("%s: profileChanged(%s) = %v; want an ErrGrantNotLive ending %q", c.row, c.name, err, c.want)
		}
	}
}
