package api

import (
	"net/http"
	"testing"
)

// A number a request body carries is the caller's input as much as one in a path or a query, and
// Postgres integer columns end at 2147483647 (maxIntegerColumn). Rounds 1 to 3 of
// dispatch://INFRA-175 bounded the path and query numbers of that class (fitsIntegerColumn). Two
// body numbers still reach an integer column unbounded, and pgx refuses to encode a larger value,
// so each route answers 500, a fault of the server, for what is the caller's input: a result's
// failed, skipped and excluded counts (check_results' integer columns, POST
// /api/v1/issues/{key}/results), and the caller's last read position of an issue
// (user_issue_state.last_read_seq, PUT /api/v1/me/issues/{key}/state). Each must refuse the
// number as a client error, 4xx, and never 500. The control of each is 2147483647, which each
// route already takes.
func TestABodyNumberAboveTheColumnsRangeIsRefusedNotAFault(t *testing.T) {
	handler, _, _ := newTestServer(t, testServerOptions{})
	issue := createInteractionIssue(t, handler, "RESB", "Body bounds", "A spec\n")
	uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "verification.md", twoChecks)

	for _, count := range []string{"failed", "skipped", "excluded"} {
		fields := map[string]any{count: 2147483648}
		if count != "failed" {
			fields["failed"] = 1
		}
		if response := recordResult(t, handler, issue.Key, resultBody("k1", "fail", headCommit, fields)); response.Code < 400 || response.Code >= 500 {
			t.Errorf("a result with %s 2147483648: status=%d body=%s, want a 4xx refusal of the caller's input", count, response.Code, response.Body.String())
		}
		fields[count] = 2147483647
		if response := recordResult(t, handler, issue.Key, resultBody("k1", "fail", headCommit, fields)); response.Code != http.StatusCreated {
			t.Errorf("a result with %s 2147483647 (the control): status=%d body=%s, want 201", count, response.Code, response.Body.String())
		}
	}

	state := "/api/v1/me/issues/" + issue.Key + "/state"
	if response := dispatchRequest(t, handler, http.MethodPut, state, map[string]any{"last_read_seq": 2147483648}, "alice"); response.Code < 400 || response.Code >= 500 {
		t.Errorf("last_read_seq 2147483648: status=%d body=%s, want a 4xx refusal of the caller's input", response.Code, response.Body.String())
	}
	if response := dispatchRequest(t, handler, http.MethodPut, state, map[string]any{"last_read_seq": 2147483647}, "alice"); response.Code != http.StatusOK {
		t.Errorf("last_read_seq 2147483647 (the control): status=%d body=%s, want 200", response.Code, response.Body.String())
	}
}
