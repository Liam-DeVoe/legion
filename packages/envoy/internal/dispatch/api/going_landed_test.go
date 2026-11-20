package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// goingReasonsOn is how many messages of issue say body.
func (rig goingWritesRig) goingReasonsOn(t *testing.T, issue string, body any) int {
	t.Helper()
	var count int
	if err := rig.database.Pool.QueryRow(context.Background(),
		`select count(*) from messages where issue_key = $1 and body = $2`, issue, body).Scan(&count); err != nil {
		t.Fatalf("count %s's messages saying %v: %v", issue, body, err)
	}
	return count
}

// goingCommittedThenRefused is a route table that serves the first request of `refused` whole,
// so its transaction commits, and then answers 503 in its place, as a route does whose change
// committed and whose read after it failed (createStoredMessage, messages.go); every request is
// counted by method and path.
func goingCommittedThenRefused(inner http.Handler, refused string, sent map[string]int) http.Handler {
	answered := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := r.Method + " " + r.URL.Path
		sent[request]++
		if request == refused && !answered {
			answered = true
			inner.ServeHTTP(httptest.NewRecorder(), r)
			writeError(w, "SERVICE_UNAVAILABLE", http.StatusServiceUnavailable, "the change committed and its answer failed")
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// TestGoingUndoWhoseRequestCommittedAndAnswered5xxIsNeverSentAgain (review round 4, finding 1, in
// the undo): the undo of an issue the turn created posts its reason and closes it; a reason whose
// message committed and was then answered 503 landed, so the undo goes on to the close, and no
// later undo posts the reason a second time.
func TestGoingUndoWhoseRequestCommittedAndAnswered5xxIsNeverSentAgain(t *testing.T) {
	rig := newGoingWritesRig(t)
	createInteractionIssue(t, rig.mux, "DEMO", "An older issue", "The spec.\n")
	inner := rig.served.inProcess
	t.Cleanup(func() { rig.served.inProcess = inner })
	run, line := rig.runOne(t, "dispatch_issue", map[string]any{"project": "DEMO", "title": "Made, then undone", "force": true}, nil)
	const reason = "Committed, then answered 503."
	messages := http.MethodPost + " /api/v1/issues/" + line.Item + "/messages"
	sent := map[string]int{}
	rig.served.inProcess = goingCommittedThenRefused(inner, messages, sent)
	rig.browser(t, "alice", http.MethodPost, "/api/v1/going/runs/"+run+"/undo", map[string]any{"reason": reason})
	rig.browser(t, "alice", http.MethodPost, "/api/v1/going/runs/"+run+"/undo", map[string]any{"reason": reason})
	rig.served.inProcess = inner
	var posted int
	if err := rig.database.Pool.QueryRow(context.Background(),
		`select count(*) from messages where issue_key = $1 and body = $2`, line.Item, reason).Scan(&posted); err != nil || posted != 1 || sent[messages] != 1 {
		t.Fatalf("the created issue holds %d messages saying %q (%v), with %s sent %d times, want the reason posted once", posted, reason, err, messages, sent[messages])
	}
	if status := rig.issueRead(t, line.Item).Status; status != "done" {
		t.Fatalf("the created issue is %s after the undos, want done", status)
	}
}
