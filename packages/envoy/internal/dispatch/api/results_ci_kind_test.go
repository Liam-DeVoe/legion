// results_ci_kind_test.go holds the results routes to the rule that the kind of a check comes from
// its check block alone, at each read: a check whose ci_check names a run and that keeps the three
// rules of such a block is CI's, whatever rows stand against it. A role's row on such a check (one
// the build before this rule wrote while it served beside this one) counts for nothing and says why.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// olderBuildsRow writes a tester's pass of check blockID at commit as the build before this rule
// wrote one: role tester, no branch, against the check's words now.
func olderBuildsRow(t *testing.T, rig *ciJobRig, blockID, commit string) {
	t.Helper()
	check := checkOf(t, readResults(t, rig.handler, rig.issue), blockID)
	if _, err := rig.database.Pool.Exec(context.Background(), `
		insert into check_results (issue_key, check_issue_key, check_artifact_id, check_block_id, check_version, check_sha256, role, commit_sha, verdict, actor)
		values ($1, $1, $2::uuid, $3, $4, $5, 'tester', $6, 'pass', '{"kind":"session","id":"older-build"}')
	`, rig.issue, rig.verification, blockID, check.Version, check.SHA256, commit); err != nil {
		t.Fatalf("write the older build's row of %s: %v", blockID, err)
	}
}

// notCountedOf is, by check, the not_counted member of each result with role role, as the wire
// carries it in results and in recorded_results.
func notCountedOf(t *testing.T, handler http.Handler, issue, role string) map[string][]string {
	t.Helper()
	response := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+issue+"/results", nil, "alice")
	type wireResult struct {
		Role       string `json:"role"`
		NotCounted string `json:"not_counted"`
	}
	var read struct {
		Checks []struct {
			BlockID         string       `json:"block_id"`
			Results         []wireResult `json:"results"`
			RecordedResults []wireResult `json:"recorded_results"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &read); err != nil {
		t.Fatalf("decode the read of %s: %v (%s)", issue, err, response.Body.String())
	}
	held := map[string][]string{}
	for _, check := range read.Checks {
		for _, result := range append(check.Results, check.RecordedResults...) {
			if result.Role == role {
				held[check.BlockID] = append(held[check.BlockID], result.NotCounted)
			}
		}
	}
	return held
}

// TestARolesRowOnACICheckCountsForNothing: the older build's tester pass of c1 stands at the open
// pull request's head. c1's block names ci_check go-checks and keeps the three rules, so c1 is still
// a check CI proves; the tester's row counts for nothing, says so in not_counted, and makes no pass
// stand; and a further role's result of c1 is still 409 CHECK_PROVED_BY_CI. The control is k1, an
// ordinary check holding the same row, on which the tester's pass stands and counts.
func TestARolesRowOnACICheckCountsForNothing(t *testing.T) {
	rig := newCIJobRig(t)
	writeHead(t, rig.database, ciJobRepository, 70, ciJobCommit, "open", rig.issue)
	olderBuildsRow(t, rig, "c1", ciJobCommit)
	olderBuildsRow(t, rig, "k1", ciJobCommit)

	read := readResults(t, rig.handler, rig.issue)
	if k1 := checkOf(t, read, "k1"); k1.PassStands.Answer != model.PassStandsYes {
		t.Fatalf("the control: the tester's pass on the ordinary check k1 reads %+v, want it standing", k1.PassStands)
	}
	c1 := checkOf(t, read, "c1")
	if c1.CI == nil {
		t.Fatalf("c1, whose block names ci_check %q, reads as no check CI proves once a role's row stands against it: ci_check %q, results %+v",
			ciJobName, c1.CICheck, c1.Results)
	}
	if c1.PassStands.Answer == model.PassStandsYes {
		t.Fatalf("the role's row on c1 makes a pass stand: %+v", c1.PassStands)
	}
	notCounted := notCountedOf(t, rig.handler, rig.issue, "tester")
	if len(notCounted["c1"]) != 1 || !strings.Contains(notCounted["c1"][0], "counts for nothing") {
		t.Fatalf("the tester's row on c1 carries not_counted %q, want the reason it counts for nothing", notCounted["c1"])
	}
	if len(notCounted["k1"]) != 1 || notCounted["k1"][0] != "" {
		t.Fatalf("the tester's row on k1 carries not_counted %q, want none", notCounted["k1"])
	}

	refused := rig.post(t, rig.issue, rig.sessionToken(), resultBody("c1", "pass", ciJobCommit, map[string]any{"role": "tester"}))
	if code, _ := refusalOf(t, refused); refused.Code != http.StatusConflict || code != "CHECK_PROVED_BY_CI" {
		t.Fatalf("a further tester's result of c1: status=%d body=%s, want 409 CHECK_PROVED_BY_CI", refused.Code, refused.Body.String())
	}
}
