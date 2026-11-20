// parent_status_rule_test.go holds the inventories of R24, the rule between the status of a
// parent and the statuses of its children: the group of each status, and each writer of a
// status or a parent.
package api

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestEveryStatusFallsInOneGroup: each status of model.IssueStatuses has its group in the table
// below, each row of the table is a status, and statusGroup gives each status the group of its
// row. A status added to the lifecycle fails this test until it has a row.
func TestEveryStatusFallsInOneGroup(t *testing.T) {
	groups := map[string]group{
		"triage":       groupNotStarted,
		"icebox":       groupNotStarted,
		"backlog":      groupNotStarted,
		"todo":         groupInWork,
		"in_progress":  groupInWork,
		"testing":      groupInWork,
		"needs_review": groupInWork,
		"retro":        groupInWork,
		"done":         groupDone,
	}
	for _, status := range slices.Sorted(maps.Keys(groups)) {
		if !model.IsIssueStatus(status) {
			t.Errorf("the table has a row for %q, which is not a status of model.IssueStatuses", status)
			continue
		}
		if got, want := statusGroup(status), groups[status]; got != want {
			t.Errorf("statusGroup(%q) = %q, want %q", status, got, want)
		}
	}
	for _, status := range model.IssueStatuses {
		if _, listed := groups[status]; !listed {
			t.Errorf("the status %q of model.IssueStatuses has no row in the table: give it its group", status)
		}
	}
	if got := statusGroup("closed"); got != "" {
		t.Errorf(`statusGroup("closed") = %q, want no group for a string that is not a status`, got)
	}
}

// TestEveryWriteOfAStatusOrAParentKeepsTheRule: with DISPATCH_PARENT_STATUS_RULE on, each writer
// of an issue's status or parent refuses a write that breaks R24 with 409
// PARENT_STATUS_CONFLICT, and the refusal names the issue at the other end of the pair. This is
// true whoever sends the write: a person signed in, a personal token or a broker token. The
// writers are the PATCH with a status, the PATCH with a parent, the create with a parent and
// the status-change route. A grep of each non-test `update issues`, `insert into issues`,
// `set("parent_key"` and `"status = "` in packages/envoy found no other writer.
//
// Each case seeds issues of its own, a parent before its child, so each seed keeps the rule
// and only the case's own write breaks it.
func TestEveryWriteOfAStatusOrAParentKeepsTheRule(t *testing.T) {
	rig := newBrokerTokenRigOn(t, testServerOptions{parentStatusRule: true})
	agent := newBrokerCaller(t)
	const brokerToken = "a-token-that-writes-statuses"
	rig.broker.Says(brokerToken, liveNamedToken(agent.jkt))
	if created := rig.asPerson(t, http.MethodPost, "/api/v1/projects", map[string]any{
		"key": brokerTestProject, "name": "Broker",
	}); created.Code != http.StatusCreated {
		t.Fatalf("create the project as a person: status=%d body=%s", created.Code, created.Body.String())
	}
	minted := rig.asPerson(t, http.MethodPost, "/api/v1/me/agent-tokens", map[string]any{"name": "laptop"})
	if minted.Code != http.StatusCreated {
		t.Fatalf("mint a personal token: status=%d body=%s", minted.Code, minted.Body.String())
	}
	personal := decodeBody[agentTokenResponse](t, minted).Token

	// seed files an issue as the person, under parent when parent is not empty, and moves it
	// to status.
	seed := func(t *testing.T, title, status, parent string) string {
		t.Helper()
		body := map[string]any{"project": brokerTestProject, "title": title, "force": true}
		if parent != "" {
			body["parent"] = parent
		}
		created := rig.asPerson(t, http.MethodPost, "/api/v1/issues", body)
		if created.Code != http.StatusCreated {
			t.Fatalf("seed %q: status=%d body=%s", title, created.Code, created.Body.String())
		}
		key := decodeBody[struct {
			Key string `json:"key"`
		}](t, created).Key
		if status != model.IssueStatusTriage {
			moved := rig.asPerson(t, http.MethodPatch, "/api/v1/issues/"+key, map[string]any{"status": status})
			if moved.Code != http.StatusOK {
				t.Fatalf("move the seed %s to %s: status=%d body=%s", key, status, moved.Code, moved.Body.String())
			}
		}
		return key
	}

	// breakingWrite is one write that breaks R24, and holds is the key of the issue that its
	// refusal must name.
	type breakingWrite struct {
		method, path string
		body         map[string]any
		holds        string
	}
	writers := []struct {
		name string
		// write seeds the case's issues, titled after label, and gives the write that breaks
		// the rule over them.
		write func(t *testing.T, label string) breakingWrite
	}{
		{"PATCH with a status: a parent to done over a child in todo", func(t *testing.T, label string) breakingWrite {
			parent := seed(t, label+" parent", "in_progress", "")
			child := seed(t, label+" child", "todo", parent)
			return breakingWrite{http.MethodPatch, "/api/v1/issues/" + parent, map[string]any{"status": "done"}, child}
		}},
		{"PATCH with a parent: a child in todo under a done parent", func(t *testing.T, label string) breakingWrite {
			parent := seed(t, label+" done parent", "done", "")
			child := seed(t, label+" child", "todo", "")
			return breakingWrite{http.MethodPatch, "/api/v1/issues/" + child, map[string]any{"parent": parent}, parent}
		}},
		{"create with a parent: an issue filed under a done parent", func(t *testing.T, label string) breakingWrite {
			parent := seed(t, label+" done parent", "done", "")
			return breakingWrite{http.MethodPost, "/api/v1/issues", map[string]any{
				"project": brokerTestProject, "title": label + " new child", "parent": parent, "force": true,
			}, parent}
		}},
		{"the status-change route: a parent to done over a child in todo with no option", func(t *testing.T, label string) breakingWrite {
			parent := seed(t, label+" parent", "in_progress", "")
			child := seed(t, label+" child", "todo", parent)
			return breakingWrite{http.MethodPost, "/api/v1/issues/" + parent + "/status-change", map[string]any{"status": "done"}, child}
		}},
	}
	callers := []struct {
		name string
		send func(t *testing.T, method, path string, body map[string]any) *httptest.ResponseRecorder
	}{
		{"a person signed in", func(t *testing.T, method, path string, body map[string]any) *httptest.ResponseRecorder {
			return rig.asPerson(t, method, path, body)
		}},
		{"a personal token", func(t *testing.T, method, path string, body map[string]any) *httptest.ResponseRecorder {
			body["actor"] = map[string]any{"kind": "session", "id": "ses_personal_token"}
			return rig.serve(brokerRequest(t, method, path, body, "Bearer "+personal, ""))
		}},
		{"a broker token", func(t *testing.T, method, path string, body map[string]any) *httptest.ResponseRecorder {
			return rig.serve(agent.presenting(t, method, path, brokerToken, body))
		}},
	}
	for w, writer := range writers {
		t.Run(writer.name, func(t *testing.T) {
			for c, caller := range callers {
				t.Run(caller.name, func(t *testing.T) {
					write := writer.write(t, fmt.Sprintf("Rule case %d.%d", w+1, c+1))
					requireParentStatusConflict(t, write.method+" "+write.path, caller.send(t, write.method, write.path, write.body), write.holds)
				})
			}
		})
	}
}

// requireParentStatusConflict fails t unless response is 409 PARENT_STATUS_CONFLICT and its
// issues name holds and no other issue.
func requireParentStatusConflict(t *testing.T, request string, response *httptest.ResponseRecorder, holds string) {
	t.Helper()
	var refusal struct {
		Code   string `json:"code"`
		Issues []struct {
			Key string `json:"key"`
		} `json:"issues"`
	}
	// A body that is not the refusal's JSON leaves the code empty, and the check below names it.
	_ = json.Unmarshal(response.Body.Bytes(), &refusal)
	if response.Code != http.StatusConflict || refusal.Code != "PARENT_STATUS_CONFLICT" {
		t.Errorf("%s: status=%d body=%.300s, want 409 PARENT_STATUS_CONFLICT naming %s", request, response.Code, response.Body.String(), holds)
		return
	}
	named := make([]string, 0, len(refusal.Issues))
	for _, issue := range refusal.Issues {
		named = append(named, issue.Key)
	}
	slices.Sort(named)
	if named = slices.Compact(named); !slices.Equal(named, []string{holds}) {
		t.Errorf("%s: the refusal names %q, want %s alone", request, named, holds)
	}
}
