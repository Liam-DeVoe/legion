//go:build capture

package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"testing"
	"time"
)

// TestCaptureRoleHoldersForbidden writes Dispatch's 403 ROLE_HOLDER_FORBIDDEN of a publication of
// an issue's role holders to the dispatch client's saved answers (internal/dispatch/testdata,
// role_holders_forbidden.http, whose README names it), whole in the form `curl --include` prints,
// from the rig's real scratch Dispatch, which this checkout builds: the publication the daemon
// sends (PUT /api/v1/issues/{key}/role-holders) under the scratch server's agent token, with an
// actor that names a session other than the issue's project's daemon, as a token that does not
// prove that session gets once R26's refusal is on (putIssueRoleHolders in
// packages/envoy/internal/dispatch/api/issue_role_holders.go). The daemon's outbox reads it as it
// reads a status write's LEGION_STATUS_SCOPE_REQUIRED (decision 12 of dispatch://INFRA-4730).
//
// It runs only by hand, and needs the Postgres LEGION_TEST_PG_DSN names:
//
//	go -C packages/daemon test -tags capture -count=1 -run '^TestCaptureRoleHoldersForbidden$' ./internal/daemon/
func TestCaptureRoleHoldersForbidden(t *testing.T) {
	d := startScratchDispatchOn(t, scratchDispatchOptions{})
	key := createScratchDispatchIssue(t, d.base)
	body, err := json.Marshal(map[string]any{
		"holders": []any{},
		"actor":   map[string]string{"kind": "session", "id": "ses-not-the-daemon"},
	})
	if err != nil {
		t.Fatalf("encode the publication: %v", err)
	}
	request, err := http.NewRequest(http.MethodPut, d.base+"/api/v1/issues/"+url.PathEscape(key)+"/role-holders", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build the publication of %s: %v", key, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+d.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: time.Minute}).Do(request)
	if err != nil {
		t.Fatalf("publish the role holders of %s: %v", key, err)
	}
	defer response.Body.Close()
	raw, err := httputil.DumpResponse(response, true)
	if err != nil {
		t.Fatalf("dump the answer: %v", err)
	}
	if response.StatusCode != http.StatusForbidden || !bytes.Contains(raw, []byte(`"ROLE_HOLDER_FORBIDDEN"`)) {
		t.Fatalf("the publication of %s answered:\n%s\nwant 403 ROLE_HOLDER_FORBIDDEN", key, raw)
	}
	saveAnswer(t, "role_holders_forbidden.http", raw)
}
