// check_states_holder_test.go holds the settler to whom an application of a pull request's history
// owes its publication: the issue that holds the pull request when the application commits, not
// the issue a writer saw when it recorded the change. The rig is check_states_test.go's.
package api

import (
	"net/http"
	"testing"
)

// TestAppliedHistoryOwesTheIssueThatHoldsThePullRequestWhenItIsApplied: #70 arrived under its
// repository's old name, credited to no issue, and left H1 for H2 there, and arrived under the
// canonical name at H3, credited to the first issue. The re-key's delete of the old row runs and
// is held open. Meanwhile #70 moves to the other issue, which settles everything it is owed: H2,
// the old row's head, is no former head yet. Then the delete commits and is applied: H2 is a former
// head of #70, which the other issue holds now, so that issue is owed a settle and publishes
// states at H2, though its own owed work settled before the delete committed.
func TestAppliedHistoryOwesTheIssueThatHoldsThePullRequestWhenItIsApplied(t *testing.T) {
	const oldRepo = "oldorg/example-repo"
	rig := newCheckStatesRig(t, checkStatesTwoChecks)
	if response := dispatchRequest(t, rig.handler, http.MethodPut, "/api/v1/settings/repo-projects/"+oldRepo,
		map[string]any{"project": ciJobProject, "renamed_to": ciJobRepository}, "alice"); response.Code != http.StatusOK {
		t.Fatalf("map %s, renamed to %s: status=%d body=%s", oldRepo, ciJobRepository, response.Code, response.Body.String())
	}
	other := rig.fileIssue(t, "Where the pull request moves")
	uploadDocument(t, rig.handler, "/api/v1/issues/"+other, "verification.md", checkStatesTwoChecks)
	writeHead(t, rig.database, oldRepo, 70, eachHeadH1, "open", "")
	writeHead(t, rig.database, oldRepo, 70, eachHeadH2, "open", "")
	writeHead(t, rig.database, ciJobRepository, 70, eachHeadH3, "open", rig.issue)
	settleOwed(t, rig)

	reKey, err := rig.database.Pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin the re-key: %v", err)
	}
	defer reKey.Rollback(t.Context())
	if _, err := reKey.Exec(t.Context(), `delete from pull_request_heads where repo = $1 and number = 70`, oldRepo); err != nil {
		t.Fatalf("the re-key's delete of %s#70: %v", oldRepo, err)
	}
	writeHead(t, rig.database, ciJobRepository, 70, eachHeadH3, "open", other)
	settleOwed(t, rig)
	if _, held := publishedRow(t, rig.database, other, ciJobRepository, eachHeadH2, ""); held {
		t.Fatalf("%s published states at H2 before the delete that makes it a former head committed: this test holds nothing", other)
	}
	if err := reKey.Commit(t.Context()); err != nil {
		t.Fatalf("commit the re-key's delete: %v", err)
	}
	settleOwed(t, rig)
	if _, held := publishedRow(t, rig.database, other, ciJobRepository, eachHeadH2, ""); !held {
		t.Fatalf("once the re-key's delete committed and was applied, %s, which holds #70, published no state at H2, the old row's head: the application owed it nothing", other)
	}
}
