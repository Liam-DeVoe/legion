package policy_test

import (
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/policy"
	"github.com/sjawhar/envoy/internal/cipher/policy/policytest"
	"github.com/sjawhar/envoy/internal/cipher/secrets"
)

// mintedBlock is a proxy block of kind dpop: the capability the broker mints and holds behind it
// (requests.MintedKind), bound to one host, carried in Authorization alone, with an allow list.
const mintedBlock = `"proxy": {"hosts": ["dispatch.example.com"], "headers": ["Authorization"], "kind": "dpop",
  "allow": [{"method": "POST", "pathPrefix": "/api/v1/"}]}`

// TestADPoPProfileIsANameAndAProxyBlockAlone: a profile whose proxy block is of kind dpop names a
// capability the broker mints from no stored secret, so a field that scopes or limits a stored
// secret's grant - source, tier, requesters, maxGrant, a github or a google block - is refused,
// naming that field, since it would read as a limit that holds nothing; a name and the dpop block
// alone parse, and so does a block of no kind beside such fields.
func TestADPoPProfileIsANameAndAProxyBlockAlone(t *testing.T) {
	for field, profile := range map[string]string{
		"source":     `{"name": "TEST_MINTED_TOKEN", "source": "LEGION_IMPLEMENT_APP", ` + mintedBlock + `}`,
		"tier":       `{"name": "TEST_MINTED_TOKEN", "tier": "agent", ` + mintedBlock + `}`,
		"requesters": `{"name": "TEST_MINTED_TOKEN", "requesters": ["ada@example.com"], ` + mintedBlock + `}`,
		"maxGrant":   `{"name": "TEST_MINTED_TOKEN", "maxGrant": "15m", ` + mintedBlock + `}`,
		"github":     `{"name": "TEST_MINTED_TOKEN", ` + acmeRead + `, ` + mintedBlock + `}`,
		"google":     `{"name": "TEST_MINTED_TOKEN", "google": {"scopes": ["cloud-platform"]}, ` + mintedBlock + `}`,
	} {
		t.Run(field, func(t *testing.T) {
			_, err := policy.ParseProfiles([]byte(policytest.ProfilesFile(profile)))
			if err == nil || !strings.Contains(err.Error(), `"TEST_MINTED_TOKEN": `+field+" ") || !strings.Contains(err.Error(), "dpop") {
				t.Fatalf("ParseProfiles of a dpop profile with %s = %v; want a refusal naming the profile, the field and the kind", field, err)
			}
		})
	}
	if _, err := policy.ParseProfiles([]byte(policytest.ProfilesFile(`{"name": "TEST_MINTED_TOKEN", ` + mintedBlock + `}`))); err != nil {
		t.Fatalf("ParseProfiles of a name and a dpop block = %v; want it parsed", err)
	}
	if _, err := policy.ParseProfiles([]byte(policytest.ProfilesFile(readProfile))); err != nil {
		t.Fatalf("ParseProfiles of a profile of no kind with a source and a github block = %v; want it parsed", err)
	}
}

// TestADPoPProfileIsNeverServedAsASecret: a dpop profile is proxy configuration for a capability
// the broker mints and holds, so the policy serves it as no secret whatever the store holds: with
// no stored secret of its name, and with one, it is refused profile-minted, so it can be neither
// requested nor approved. The file's other profile is still served.
func TestADPoPProfileIsNeverServedAsASecret(t *testing.T) {
	for name, stored := range map[string][]secrets.LocalSecret{
		"no stored secret of its name": everySource(),
		"a stored secret of its name":  append(everySource(), policytest.Secret("TEST_MINTED_TOKEN", appOwner, policy.TierAgent, "stored-v1")),
	} {
		t.Run(name, func(t *testing.T) {
			cur, _ := profiled(t, stored, readProfile, `{"name": "TEST_MINTED_TOKEN", `+mintedBlock+`}`)
			set := cur.Get()
			refusal := refusedProfile(t, set, "TEST_MINTED_TOKEN", policy.ReasonProfileMinted)
			if !strings.Contains(refusal.Detail, "dpop") {
				t.Errorf("TEST_MINTED_TOKEN's refusal detail = %q; want it to name the kind dpop", refusal.Detail)
			}
			servedProfile(t, set, "GITHUB_READ")
		})
	}
}
