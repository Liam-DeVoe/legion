// packages/envoy/internal/broker/approval/profile_page_test.go
// What the approval page says about a record that asks for a profile: the scope the person
// approves, as the profiles file gives it and as the record's signed target narrows it.
package approval_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/policy"
	"github.com/sjawhar/envoy/internal/broker/policy/policytest"
	"github.com/sjawhar/envoy/internal/broker/record"
	"github.com/sjawhar/envoy/internal/broker/secrets"
)

// TestTheApprovalPagePrintsTheProfilesScope: a record for a GitHub profile prints, beside the
// name it asks for, the profile's source, its permissions, its accounts, its repositories, the
// target the record's signed request narrows it to and the hosts its proxy block sends the
// credential to, each read from the live profiles file and the record itself. Each is its own
// row, so a page that leaves one of them out fails that row alone.
func TestTheApprovalPagePrintsTheProfilesScope(t *testing.T) {
	r := newRig(t)
	local := r.machine.Secrets.(secrets.AWS).Client.(*secrets.Local)
	local.Put(policytest.TypeSecret("ADA_APP", ada, policy.TierHuman, policy.TypeGitHubApp, policytest.TypeValue(policy.TypeGitHubApp)))
	r.machine.Policy = policytest.ProfiledCurrent(t, local, policytest.ProfilesFile(
		`{"name": "GITHUB_WRITE", "source": "ADA_APP", "github": {"accounts": ["acme", "ada"], "repositories": ["ada/lean", "ada/proofs"], "permissions": {"pull_requests": "write", "contents": "write"}}, "proxy": {"hosts": ["api.github.com"]}}`,
	))
	enrollmentID, key := r.benSession(t, "ben-box")
	compact, err := record.Sign(key, r.url, []record.AuthorizationDetail{
		{Type: record.KindAgentSecret, Identifier: "GITHUB_WRITE", Actions: []string{"inject"}, Locations: []string{"https://github.com/ada/proofs"}},
	}, "push the proof", "", nil, time.Now())
	if err != nil {
		t.Fatalf("record.Sign: %v", err)
	}
	status, out := r.session(t, key, enrollmentID, http.MethodPost, "/v1/requests", map[string]any{"request": compact})
	created := decode[struct {
		State    string  `json:"state"`
		RecordID *string `json:"record_id"`
	}](t, out)
	if status != http.StatusOK || created.State != "pending" || created.RecordID == nil {
		t.Fatalf("POST /v1/requests for GITHUB_WRITE --target ada/proofs = %d %s, want it pending with a record", status, out)
	}

	b := r.browser(t)
	b.SignIn(t, *created.RecordID, person(ada))
	block := pageText(t, recordBlock(t, b.Page(t, *created.RecordID).Body, *created.RecordID))
	for _, row := range []struct{ field, want string }{
		{"source", "Source ADA_APP"},
		{"permissions", "Permissions contents: write, pull_requests: write"},
		{"accounts", "GitHub accounts acme, ada"},
		{"repositories", "Repositories ada/lean, ada/proofs"},
		{"target", "Target ada/proofs"},
		{"proxy hosts", "Proxy hosts api.github.com"},
	} {
		t.Run(row.field, func(t *testing.T) {
			if !strings.Contains(block, row.want) {
				t.Fatalf("the record's block does not print the profile's %s, %q:\n%s", row.field, row.want, block)
			}
		})
	}
}
