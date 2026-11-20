// packages/envoy/internal/broker/api/target_test.go
// POST /v1/requests's refusal of a target it cannot serve, 400 TARGET_REFUSED (C-5): a target
// outside its profile, a target on a name with no github block, and a value-token profile asked
// for with no target while it names more than one account. Each is refused at Create, before a
// request row, a record or a check mint, so nobody is asked and GitHub is asked nothing.
package api_test

import (
	"crypto/ecdsa"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/policy"
	"github.com/sjawhar/envoy/internal/broker/policy/policytest"
	"github.com/sjawhar/envoy/internal/broker/record"
)

// targetProfiles is the profiles file of every test here. APP_KEY, agent tier, is served by its
// own name on two accounts and every repository, and as PROOFS_READ on two repositories of one
// of them. HUMAN_APP_KEY, human tier, whose github-owners tag names three accounts, is served by
// its own name, as GITHUB_READ, a bare token of all three, and as ADA_TOKEN, a bare token of
// one. DEEL_API_KEY, a stored value, is served behind the proxy as DEEL_PROXIED, with no github
// block.
var targetProfiles = policytest.ProfilesFile(
	policytest.AppProfile("APP_KEY"),
	`{"name": "PROOFS_READ", "source": "APP_KEY", "github": {"accounts": ["acme", "ada"], "repositories": ["ada/lean", "ada/proofs"], "permissions": {"contents": "read"}}}`,
	`{"name": "HUMAN_APP_KEY", "github": {"accounts": ["acme", "ada", "example-org"], "permissions": {"metadata": "read"}}}`,
	`{"name": "GITHUB_READ", "source": "HUMAN_APP_KEY", "github": {"accounts": ["acme", "ada", "example-org"], "permissions": {"contents": "read"}, "value": "token"}}`,
	`{"name": "ADA_TOKEN", "source": "HUMAN_APP_KEY", "github": {"accounts": ["ada"], "permissions": {"contents": "read"}, "value": "token"}}`,
	`{"name": "DEEL_PROXIED", "source": "DEEL_API_KEY", "proxy": {"hosts": ["api.deel.example"]}}`,
)

// newTargetServer is a profileServer serving targetProfiles, whose minter mints every check.
func newTargetServer(t *testing.T) *profileServer {
	t.Helper()
	sources := appSources()
	sources[1].Tags[policy.TagGitHubOwners] = "acme ada example-org"
	return newProfileServerOf(t, targetProfiles, minting, sources, "APP_KEY", "PROOFS_READ", "HUMAN_APP_KEY", "GITHUB_READ", "ADA_TOKEN", "DEEL_PROXIED")
}

// signTargetRequest signs a request object for the one name narrowed to the target locations
// name (none for no target), addressed to audience.
func signTargetRequest(t *testing.T, key *ecdsa.PrivateKey, audience, name string, locations ...string) string {
	t.Helper()
	compact, err := record.Sign(key, audience, []record.AuthorizationDetail{
		{Type: record.KindAgentSecret, Identifier: name, Actions: []string{"inject"}, Locations: locations},
	}, "read the repository", "", nil, time.Now())
	if err != nil {
		t.Fatalf("record.Sign: %v", err)
	}
	return compact
}

// askTargeted posts a request for name narrowed to locations and answers the status and the
// decoded answer, the refusal's code and text among it.
func (s *profileServer) askTargeted(t *testing.T, name string, locations ...string) (int, wireError, []byte) {
	t.Helper()
	status, body := s.request(t, name, map[string]any{"request": signTargetRequest(t, s.key, s.URL, name, locations...)})
	return status, decode[wireError](t, body), body
}

// requireTargetRefused fails t unless the answer is 400 TARGET_REFUSED whose text holds each of
// words, with no request row or record written and the minter asked nothing.
func (s *profileServer) requireTargetRefused(t *testing.T, status int, reply wireError, body []byte, words ...string) {
	t.Helper()
	if status != http.StatusBadRequest || reply.Code != "TARGET_REFUSED" {
		t.Fatalf("POST /v1/requests = %d %s, want 400 TARGET_REFUSED", status, body)
	}
	for _, word := range words {
		if !strings.Contains(reply.Error, word) {
			t.Errorf("the refusal says %q; want it to name %q", reply.Error, word)
		}
	}
	if rows, records := s.pendingRows(t); rows != 0 || records != 0 {
		t.Fatalf("the refused request left %d request rows and %d records; want none", rows, records)
	}
	if minted := s.take(); len(minted) != 0 {
		t.Fatalf("the minter was asked %d times for a refused target; want never", len(minted))
	}
}

// TestTargetOutsideTheProfileIsRefused: a target narrows its profile and never widens it. An
// account the profile does not name, a repository it does not name, and an account it names no
// repository of are each refused 400 TARGET_REFUSED, naming the profile's accounts and
// repositories, before anything is written or minted. A target inside the profile, the account
// or a repository it names, is granted; on a profile that names no repository, any repository of
// its accounts is inside it.
func TestTargetOutsideTheProfileIsRefused(t *testing.T) {
	s := newTargetServer(t)
	for _, row := range []struct {
		name, profile string
		locations     []string
		words         []string
	}{
		{"an account the profile does not name", "PROOFS_READ", []string{"https://github.com/example-org"}, []string{"PROOFS_READ", "example-org", "acme, ada", "ada/lean, ada/proofs"}},
		{"a repository the profile does not name", "PROOFS_READ", []string{"https://github.com/ada/proofs", "https://github.com/ada/site"}, []string{"PROOFS_READ", "ada/proofs, ada/site", "acme, ada", "ada/lean, ada/proofs"}},
		{"an account the profile names no repository of", "PROOFS_READ", []string{"https://github.com/acme"}, []string{"PROOFS_READ", "acme, ada", "ada/lean, ada/proofs"}},
		{"a repository of an account the profile names no repository of", "PROOFS_READ", []string{"https://github.com/acme/lean"}, []string{"PROOFS_READ", "acme/lean", "acme, ada", "ada/lean, ada/proofs"}},
		{"an account a profile of every repository does not name", "APP_KEY", []string{"https://github.com/example-org/site"}, []string{"APP_KEY", "example-org/site", "acme, ada", "every repository"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			status, reply, body := s.askTargeted(t, row.profile, row.locations...)
			s.requireTargetRefused(t, status, reply, body, row.words...)
		})
	}
	for _, row := range []struct {
		name, profile string
		locations     []string
	}{
		{"an account the profile names", "PROOFS_READ", []string{"https://github.com/ada"}},
		{"a repository the profile names", "PROOFS_READ", []string{"https://github.com/ada/lean"}},
		{"any repository of a profile of every repository", "APP_KEY", []string{"https://github.com/ada/site"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			status, body := s.request(t, row.profile, map[string]any{"request": signTargetRequest(t, s.key, s.URL, row.profile, row.locations...)})
			if created := decode[wireCreateRequestResponse](t, body); status != http.StatusOK || created.GrantID == nil {
				t.Fatalf("POST /v1/requests for %s %v = %d %s, want an automatic grant", row.profile, row.locations, status, body)
			}
		})
	}
}

// TestTargetOnAProfileWithNoGitHubBlockIsRefused: a target narrows a GitHub profile alone. On a
// stored secret served by its own name, and on a profile with no github block, it is refused 400
// TARGET_REFUSED naming the name, which has no account or repository to narrow, before anything
// is written. The same names asked for with no target are served as before.
func TestTargetOnAProfileWithNoGitHubBlockIsRefused(t *testing.T) {
	s := newTargetServer(t)
	for _, name := range []string{"WORKER_TOKEN", "DEEL_PROXIED"} {
		t.Run(name, func(t *testing.T) {
			status, reply, body := s.askTargeted(t, name, "https://github.com/ada")
			s.requireTargetRefused(t, status, reply, body, name, "no github block")
		})
	}
	status, body := s.request(t, "WORKER_TOKEN", map[string]any{})
	if created := decode[wireCreateRequestResponse](t, body); status != http.StatusOK || created.GrantID == nil {
		t.Fatalf("POST /v1/requests for WORKER_TOKEN with no target = %d %s, want its automatic grant", status, body)
	}
}

// TestAValueTokenProfileWithNoTargetIsRefusedBeforeAnyoneIsAsked: a value-token profile releases
// one account's bare token, so asked for with no target while it names three accounts it is
// refused at Create, 400 TARGET_REFUSED naming the accounts and --target, with no request row, no
// record for its approver to see and no check mint. With a target naming one of them, and for a
// value-token profile of one account with no target, the request goes to its approver as any
// human-tier request does, with the check mint of the one account in effect.
func TestAValueTokenProfileWithNoTargetIsRefusedBeforeAnyoneIsAsked(t *testing.T) {
	s := newTargetServer(t)
	status, reply, body := s.askTargeted(t, "GITHUB_READ")
	s.requireTargetRefused(t, status, reply, body, "GITHUB_READ", "acme, ada, example-org", "--target")

	for _, row := range []struct {
		name, profile string
		locations     []string
		account       string
	}{
		{"a target naming one account", "GITHUB_READ", []string{"https://github.com/example-org"}, "example-org"},
		{"a profile of one account", "ADA_TOKEN", nil, "ada"},
	} {
		t.Run(row.name, func(t *testing.T) {
			status, body := s.request(t, row.profile, map[string]any{"request": signTargetRequest(t, s.key, s.URL, row.profile, row.locations...)})
			if created := decode[wireCreateRequestResponse](t, body); status != http.StatusOK || created.State != "pending" {
				t.Fatalf("POST /v1/requests for %s = %d %s, want it pending on its approver", row.profile, status, body)
			}
			minted := s.take()
			if len(minted) != 1 || !minted[0].Check || minted[0].GitHub == nil || strings.Join(minted[0].GitHub.Accounts, " ") != row.account {
				t.Fatalf("the minter was asked %+v; want the one check mint of %s for %s", minted, row.profile, row.account)
			}
		})
	}
}
