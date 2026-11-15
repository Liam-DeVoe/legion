// going_takeover_test.go holds a write and an undo whose process ended to the one rule both obey:
// nothing of either acts before it reads what landed (check c21). A press that takes over a write,
// and an undo that takes over another past its bound, each begin with the run's record of which of
// its requests landed: a write whose every request landed is recorded landed, from what its route
// recorded beside the landing in its own transaction, and never translated, read or sent again; an
// undo resumes after what it landed, and only one that reached a request and recorded no landing of
// it is answered unsettled.
package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// goingDiesOn is a route table that serves the first request of method whose path ends with
// suffix whole, so its transaction commits, and then ends the request making it, as a process
// ends between a change and its answer (goingDiesAfter, for a path the test cannot spell before
// the write runs).
func goingDiesOn(inner http.Handler, method, suffix string) http.Handler {
	died := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == method && strings.HasSuffix(r.URL.Path, suffix) && !died {
			died = true
			inner.ServeHTTP(&recordedAnswer{header: http.Header{}}, r)
			panic(http.ErrAbortHandler)
		}
		inner.ServeHTTP(w, r)
	})
}

// TestGoingWriteWhoseProcessEndedAfterItsLastRequestLandedIsRecordedLanded (review round 5,
// thread 1; check c21): for every row whose write sends more than one request, derived from the
// catalogue, the process making the write ends after its last request - a reasoned close's close -
// committed, before the answer and the write's record. Past the claim's bound the next press reads
// what landed before anything else: it sends nothing, is answered the write landed and records it
// so, though its translation would now refuse the issue as closed; and the turn's undo reopens the
// issue, from the values the close's route recorded beside its landing.
func TestGoingWriteWhoseProcessEndedAfterItsLastRequestLandedIsRecordedLanded(t *testing.T) {
	rig := newGoingWritesRig(t)
	rows := goingSeveralRequestRows(t, rig)
	inner := rig.served.inProcess
	t.Cleanup(func() { rig.served.inProcess = inner })
	for _, row := range rows {
		t.Run(row.action, func(t *testing.T) {
			issue := rig.issue(t, "Closed as the process ended")
			before := rig.issueRead(t, issue)
			arguments, first, own := row.on(t, rig, issue)
			run := rig.proposed(t, "alice", false, row.action, arguments)
			press := map[string]any{"offer": row.action, "invocation": 0, "approved": goingApprovedPerson}
			rig.served.inProcess = goingDiesAfter(inner, own)
			rig.dieWriting(t, run, press)
			sent := map[string]int{}
			rig.served.inProcess = goingCommittedThenRefused(inner, "", sent)
			rig.ageGoingWrites(t, run)
			answered := rig.write(t, "alice", run, press)
			rig.served.inProcess = inner
			if answered.Code != http.StatusOK || !goingLineOf(t, answered).Outcome.OK || sent[first] != 0 || sent[own] != 0 {
				t.Fatalf("the press after the process ended answered %d %s with %s sent %d times and %s %d, want the write landed and nothing sent",
					answered.Code, answered.Body.String(), first, sent[first], own, sent[own])
			}
			writes := rig.record(t, "alice", run).Writes
			if len(writes) != 1 || writes[0].Pending || !writes[0].Outcome.OK || writes[0].Undo != goingUndoWriteBack {
				t.Fatalf("the run keeps %+v, want the one write recorded landed, its undo a write back", writes)
			}
			if status := rig.issueRead(t, issue).Status; status != "done" {
				t.Fatalf("%s is %s after the press, want done", issue, status)
			}
			undone := oneLine(t, rig.undo(t, "alice", run, nil), true)
			if reopened := rig.issueRead(t, issue); reopened.Status != before.Status || reopened.ClosedAt != nil {
				t.Fatalf("the turn's undo answered %+v and left %s %s, want it reopened %s", undone, issue, reopened.Status, before.Status)
			}
		})
	}
}

// TestGoingUploadWithoutForceWhoseProcessEndedAfterItLandedIsAnsweredLanded (review round 5,
// thread 1; check c21): an upload without force whose process ended after the upload committed is
// answered landed by the next press past the claim's bound, not refused by its own read before,
// which now finds the document the upload made.
func TestGoingUploadWithoutForceWhoseProcessEndedAfterItLandedIsAnsweredLanded(t *testing.T) {
	rig := newGoingWritesRig(t)
	issue := createInteractionIssue(t, rig.mux, "DEMO", "Uploaded as the process ended", "The spec.\n").Key
	inner := rig.served.inProcess
	t.Cleanup(func() { rig.served.inProcess = inner })
	run := rig.proposed(t, "alice", false, "dispatch_artifact", map[string]any{
		"issue": issue, "name": "ended.md", "content": "# Ended\n\nAlice's words.\n",
	})
	press := map[string]any{"offer": "dispatch_artifact", "invocation": 0, "approved": goingApprovedPerson}
	rig.served.inProcess = goingDiesAfter(inner, http.MethodPost+" /api/v1/issues/"+issue+"/artifacts")
	rig.dieWriting(t, run, press)
	rig.served.inProcess = inner
	rig.ageGoingWrites(t, run)
	for range 2 {
		answered := rig.write(t, "alice", run, press)
		if answered.Code != http.StatusOK || !goingLineOf(t, answered).Outcome.OK {
			t.Fatalf("a press after the upload's process ended answered %d %s, want the upload landed", answered.Code, answered.Body.String())
		}
	}
	if versions := rig.goingDocumentVersions(t, issue, "ended-md"); versions != 1 {
		t.Fatalf("ended.md holds %d versions, want the one upload", versions)
	}
}

// goingTakenOverCase is one row whose route records, beside its request's landing, what it found
// and left: what the run proposes, the request the process ends after, and what the item holds
// once the turn is undone.
type goingTakenOverCase struct {
	name   string
	action string
	fill   any
	dies   [2]string
	setup  func(t *testing.T) map[string]any
	undone func(t *testing.T, line goingWriteLine) string
}

// TestGoingWriteTakenOverIsUndoneFromWhatItsRouteRecorded (review round 5, thread 1, the
// architect's sink): for every row whose undo puts something back, the process making the write
// ends after its request committed. Past the claim's bound the next press is answered the write
// landed, its values before and after taken from what its route recorded in the transaction of
// the change - never from a read made now - and the turn's undo undoes it.
func TestGoingWriteTakenOverIsUndoneFromWhatItsRouteRecorded(t *testing.T) {
	rig := newGoingWritesRig(t)
	issue := createInteractionIssue(t, rig.mux, "DEMO", "Taken over", "The spec.\n").Key
	inner := rig.served.inProcess
	t.Cleanup(func() { rig.served.inProcess = inner })
	askState := func(t *testing.T, id string) string {
		t.Helper()
		return decodeBody[goingAskRead](t, rig.fixture(t, "alice", http.MethodGet, "/api/v1/asks/"+id, nil)).Ask.State
	}
	madeIssue := func(t *testing.T, title string) string {
		t.Helper()
		var key string
		if err := rig.database.Pool.QueryRow(context.Background(), `select key from issues where title = $1`, title).Scan(&key); err != nil {
			t.Fatalf("find the issue titled %q: %v", title, err)
		}
		return key
	}
	notes := uploadDocument(t, rig.mux, "/api/v1/issues/"+issue, "taken.md", "# Taken\n\nThe first line.\n")
	original := decodeBody[artifactText](t, rig.fixture(t, "alice", http.MethodGet, "/api/v1/artifacts/"+notes.Artifact.ID+"/text", nil)).Markdown
	for _, tc := range []goingTakenOverCase{
		{
			name: "an issue's title", action: "dispatch_issue_update",
			setup: func(t *testing.T) map[string]any {
				return map[string]any{"issue": rig.issue(t, "Titled before"), "title": "Titled by the bar"}
			},
			undone: func(t *testing.T, line goingWriteLine) string { return rig.issueRead(t, line.Item).Title },
		},
		{
			name: "a claim", action: "dispatch_claim", dies: [2]string{http.MethodPost, "/claim"},
			setup: func(t *testing.T) map[string]any { return map[string]any{"issue": rig.issue(t, "Claimed by the bar")} },
			undone: func(t *testing.T, line goingWriteLine) string {
				if claim := rig.issueRead(t, line.Item).Claim; claim != nil {
					return "claimed by " + claim.Actor.ID
				}
				return "unclaimed"
			},
		},
		{
			name: "a release", action: "dispatch_claim", dies: [2]string{http.MethodDelete, "/claim"},
			setup: func(t *testing.T) map[string]any {
				held := rig.issue(t, "Released by the bar")
				rig.fixture(t, "alice", http.MethodPost, "/api/v1/issues/"+held+"/claim", map[string]any{})
				return map[string]any{"issue": held, "release": true}
			},
			undone: func(t *testing.T, line goingWriteLine) string {
				if claim := rig.issueRead(t, line.Item).Claim; claim != nil {
					return "claimed by " + model.CanonicalLogin(claim.Actor.ID)
				}
				return "unclaimed"
			},
		},
		{
			name: "an ask's question", action: "dispatch_edit_ask", dies: [2]string{http.MethodPatch, ""},
			setup: func(t *testing.T) map[string]any {
				return map[string]any{"ask": rig.ask(t, issue, "Ship it on Friday?"), "question": "Ship it on Monday?"}
			},
			undone: func(t *testing.T, line goingWriteLine) string {
				return decodeBody[goingAskRead](t, rig.fixture(t, "alice", http.MethodGet, "/api/v1/asks/"+line.Item, nil)).Ask.Question
			},
		},
		{
			name: "a thread's resolve", action: "dispatch_resolve_comment", dies: [2]string{http.MethodPost, "/resolve"},
			setup: func(t *testing.T) map[string]any {
				comment := decodeBody[model.Comment](t, rig.fixture(t, "alice", http.MethodPost, "/api/v1/issues/"+issue+"/comments", map[string]any{"body": "A thread."})).ID
				return map[string]any{"comment": comment}
			},
			undone: func(t *testing.T, line goingWriteLine) string {
				if decodeBody[commentRead](t, rig.fixture(t, "alice", http.MethodGet, "/api/v1/comments/"+line.Item, nil)).Comment.Resolved {
					return "resolved"
				}
				return "open"
			},
		},
		{
			name: "a follow", action: "dispatch_follow", fill: goingPlannerTitle, dies: [2]string{http.MethodPut, "/followers/" + goingPlanner},
			setup: func(t *testing.T) map[string]any {
				return map[string]any{"ask": rig.ask(t, issue, "Which region first?"), "action": "follow"}
			},
			undone: func(t *testing.T, line goingWriteLine) string {
				for _, follower := range decodeBody[askFollowersRead](t, rig.fixture(t, "alice", http.MethodGet, "/api/v1/asks/"+line.Item+"/followers", nil)).Followers {
					if follower.SessionID == goingPlanner {
						return "followed"
					}
				}
				return "not followed"
			},
		},
		{
			name: "a document's edit", action: "dispatch_doc_edit", dies: [2]string{http.MethodPost, "/edits"},
			setup: func(t *testing.T) map[string]any {
				return map[string]any{"issue": issue, "artifact": notes.Artifact.Slug,
					"ops": []any{map[string]any{"op": "replace", "find": "first line", "with": "opening line"}}}
			},
			undone: func(t *testing.T, line goingWriteLine) string {
				return decodeBody[artifactText](t, rig.fixture(t, "alice", http.MethodGet, "/api/v1/artifacts/"+notes.Artifact.ID+"/text", nil)).Markdown
			},
		},
		{
			name: "a created issue", action: "dispatch_issue", dies: [2]string{http.MethodPost, "/api/v1/issues"},
			setup: func(t *testing.T) map[string]any {
				return map[string]any{"project": "DEMO", "title": "Made as the process ended", "force": true}
			},
			undone: func(t *testing.T, line goingWriteLine) string {
				return rig.issueRead(t, madeIssue(t, "Made as the process ended")).Status
			},
		},
		{
			name: "an opened ask", action: "dispatch_ask", dies: [2]string{http.MethodPost, "/asks"},
			setup: func(t *testing.T) map[string]any {
				return map[string]any{"issue": issue, "question": "Opened as the process ended?"}
			},
			undone: func(t *testing.T, line goingWriteLine) string { return askState(t, line.Item) },
		},
		{
			name: "an approval request", action: "dispatch_request_approval", dies: [2]string{http.MethodPost, "/approval-requests"},
			setup: func(t *testing.T) map[string]any {
				return map[string]any{"issue": rig.issue(t, "Approval asked as the process ended"), "artifact": "spec", "summary": "Ready."}
			},
			undone: func(t *testing.T, line goingWriteLine) string { return askState(t, line.Item) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arguments := tc.setup(t)
			run := rig.proposed(t, "alice", false, tc.action, arguments)
			press := map[string]any{"offer": tc.action, "invocation": 0, "approved": goingApprovedPerson}
			if tc.fill != nil {
				press["fill"] = tc.fill
			}
			dies := tc.dies
			if dies[0] == "" {
				dies = [2]string{http.MethodPatch, ""}
			}
			rig.served.inProcess = goingDiesOn(inner, dies[0], dies[1])
			rig.dieWriting(t, run, press)
			rig.served.inProcess = inner
			rig.ageGoingWrites(t, run)
			line := goingLineOf(t, rig.write(t, "alice", run, press))
			if !line.Outcome.OK {
				t.Fatalf("the press after the process ended answered %+v, want the write landed", line)
			}
			if writes := rig.record(t, "alice", run).Writes; len(writes) != 1 || writes[0].Pending || writes[0].AfterUnread {
				t.Fatalf("the run keeps %+v, want the write landed with the value its route recorded it left", writes)
			}
			left := tc.undone(t, line)
			undone := oneLine(t, rig.undo(t, "alice", run, nil), true)
			if now := tc.undone(t, line); now == left {
				t.Fatalf("the turn's undo answered %+v and left %q, as the write left it, want it undone", undone, now)
			}
			switch tc.action {
			case "dispatch_doc_edit":
				if now := tc.undone(t, line); now != original {
					t.Fatalf("the document reads %q after the undo, want %q", now, original)
				}
			case "dispatch_issue":
				if now := tc.undone(t, line); now != "done" {
					t.Fatalf("the created issue is %s after the undo, want done", now)
				}
			case "dispatch_ask", "dispatch_request_approval":
				if now := tc.undone(t, line); now != "resolved" {
					t.Fatalf("the ask the turn opened is %s after the undo, want resolved", now)
				}
			}
		})
	}
}

// TestGoingWriteTakenOverWhoseDocumentChangedSinceIsUndoneByNothingSayingWhy (review round 5,
// thread 1, the architect's sink): a document edit whose process ended after it committed, and
// which another writer edited again before the next press, is answered landed with what it left
// unread, since the document read now is not the one the edit left, as its route's recorded token
// says. Its undo puts nothing back, and its line says why in plain words and what the person can
// do instead.
func TestGoingWriteTakenOverWhoseDocumentChangedSinceIsUndoneByNothingSayingWhy(t *testing.T) {
	rig := newGoingWritesRig(t)
	issue := createInteractionIssue(t, rig.mux, "DEMO", "Edited as the process ended", "The spec.\n").Key
	inner := rig.served.inProcess
	t.Cleanup(func() { rig.served.inProcess = inner })
	notes := uploadDocument(t, rig.mux, "/api/v1/issues/"+issue, "notes.md", "# Notes\n\nThe first line.\n")
	run := rig.proposed(t, "alice", false, "dispatch_doc_edit", map[string]any{"issue": issue, "artifact": notes.Artifact.Slug,
		"ops": []any{map[string]any{"op": "replace", "find": "first line", "with": "opening line"}}})
	press := map[string]any{"offer": "dispatch_doc_edit", "invocation": 0, "approved": goingApprovedPerson}
	rig.served.inProcess = goingDiesOn(inner, http.MethodPost, "/edits")
	rig.dieWriting(t, run, press)
	rig.served.inProcess = inner
	rig.fixture(t, "bob", http.MethodPost, "/api/v1/artifacts/"+notes.Artifact.ID+"/edits",
		map[string]any{"ops": []any{map[string]any{"op": "replace", "find": "opening line", "with": "line bob wrote"}}})
	bobs := decodeBody[artifactText](t, rig.fixture(t, "alice", http.MethodGet, "/api/v1/artifacts/"+notes.Artifact.ID+"/text", nil)).Markdown
	rig.ageGoingWrites(t, run)
	if line := goingLineOf(t, rig.write(t, "alice", run, press)); !line.Outcome.OK {
		t.Fatalf("the press after the process ended answered %+v, want the edit landed", line)
	}
	undone := oneLine(t, rig.undo(t, "alice", run, nil), false)
	if undone.Outcome.Code != codeGoingUndoAfterUnread || !strings.Contains(undone.Outcome.Message, "while the process making it was down") ||
		!strings.Contains(undone.Outcome.Message, "change it by hand") {
		t.Fatalf("the undo answered %+v, want %s saying the write landed while its process was down and to change the item by hand", undone.Outcome, codeGoingUndoAfterUnread)
	}
	if now := decodeBody[artifactText](t, rig.fixture(t, "alice", http.MethodGet, "/api/v1/artifacts/"+notes.Artifact.ID+"/text", nil)).Markdown; now != bobs {
		t.Fatalf("the document reads %q after the undo, want bob's %q left as it is", now, bobs)
	}
}

// dieBeforeSending sends one press of the run whose process ends on the read before its write,
// GET path, before anything of the write is planned or sent, and fails unless the run keeps that
// write pending with nothing planned (sends 0) and no request recorded.
func (rig goingWritesRig) dieBeforeSending(t *testing.T, run, path string, press map[string]any) {
	t.Helper()
	inner := rig.served.inProcess
	rig.served.inProcess = goingDiesBefore(inner, http.MethodGet+" "+path)
	rig.dieWriting(t, run, press)
	rig.served.inProcess = inner
	if writes := rig.record(t, "alice", run).Writes; len(writes) != 1 || !writes[0].Pending || writes[0].Sends != 0 || len(writes[0].Requests) != 0 {
		t.Fatalf("the run keeps %+v, want the one write pending, nothing of it planned or recorded", writes)
	}
}

// TestGoingWriteWhoseProcessEndedBeforeItSentAnythingIsRunByTheNextPress (deep review of
// ffad0d79; check c21): a title change whose process ended on its read before, after its claim and
// before its call was planned (sends 0) or any request of it sent, recorded no landing because
// nothing of it was sent, as its record proves. Past the claim's bound the next press takes the
// claim over and runs the write, as the next undo makes an undo never reached: the title changes,
// once, and a further press answers the write's line and sends nothing.
func TestGoingWriteWhoseProcessEndedBeforeItSentAnythingIsRunByTheNextPress(t *testing.T) {
	rig := newGoingWritesRig(t)
	createInteractionIssue(t, rig.mux, "DEMO", "The project's first", "The spec.\n")
	issue := rig.issue(t, "Titled before the press")
	inner := rig.served.inProcess
	t.Cleanup(func() { rig.served.inProcess = inner })
	run := rig.proposed(t, "alice", false, "dispatch_issue_update", map[string]any{"issue": issue, "title": "Titled by the press"})
	press := map[string]any{"offer": "dispatch_issue_update", "invocation": 0, "approved": goingApprovedPerson}
	rig.dieBeforeSending(t, run, "/api/v1/issues/"+issue, press)
	sent := map[string]int{}
	rig.served.inProcess = goingCommittedThenRefused(inner, "", sent)
	rig.ageGoingWrites(t, run)
	answered := rig.write(t, "alice", run, press)
	if answered.Code != http.StatusOK || !goingLineOf(t, answered).Outcome.OK {
		t.Fatalf("the press after the process ended answered %d %s, want the write run and landed", answered.Code, answered.Body.String())
	}
	if again := goingLineOf(t, rig.write(t, "alice", run, press)); !again.Outcome.OK {
		t.Fatalf("a further press answered %+v, want the write's line", again)
	}
	patch := http.MethodPatch + " /api/v1/issues/" + issue
	if title := rig.issueRead(t, issue).Title; title != "Titled by the press" || sent[patch] != 1 {
		t.Fatalf("%s is titled %q with %s sent %d times, want it retitled by one request", issue, title, patch, sent[patch])
	}
	if writes := rig.record(t, "alice", run).Writes; len(writes) != 1 || writes[0].Pending || !writes[0].Outcome.OK {
		t.Fatalf("the run keeps %+v, want the one write, recorded landed", writes)
	}
}

// TestGoingWriteNeverReachedIsRunByOneOfTwoPressesPastTheBound (deep review of ffad0d79): two
// presses read a write whose process ended before it sent anything, both past the claim's bound,
// and both reach the statement that takes the claim over at once. One takes it and runs the write;
// the other finds the claim under way again and is answered 429 GOING_WRITE_IN_PROGRESS, so the
// write is sent once.
func TestGoingWriteNeverReachedIsRunByOneOfTwoPressesPastTheBound(t *testing.T) {
	rig := newGoingWritesRig(t)
	createInteractionIssue(t, rig.mux, "DEMO", "The project's first", "The spec.\n")
	issue := rig.issue(t, "Titled before two presses")
	inner := rig.served.inProcess
	t.Cleanup(func() { rig.served.inProcess = inner })
	run := rig.proposed(t, "alice", false, "dispatch_issue_update", map[string]any{"issue": issue, "title": "Titled by one press"})
	press := map[string]any{"offer": "dispatch_issue_update", "invocation": 0, "approved": goingApprovedPerson}
	rig.dieBeforeSending(t, run, "/api/v1/issues/"+issue, press)
	rig.ageGoingWrites(t, run)

	// The press that takes the claim over is held at its write until the other has answered.
	patch := http.MethodPatch + " /api/v1/issues/" + issue
	release := make(chan struct{})
	var releaseOnce sync.Once
	let := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(let)
	var patches atomic.Int32
	rig.served.inProcess = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method+" "+r.URL.Path == patch {
			patches.Add(1)
			<-release
		}
		inner.ServeHTTP(w, r)
	})

	ctx := context.Background()
	lock, err := rig.database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, `select 1 from going_runs where id = $1::uuid for update`, run); err != nil {
		t.Fatalf("hold the run: %v", err)
	}
	cookie := rig.cookie(t, "alice")
	answers := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			answers <- goingRequestTo(t, rig.network, http.MethodPost, "/api/v1/going/runs/"+run+"/writes", press, pageOrigin, cookie)
		}()
	}
	// Both presses read the run and wait at the statement that takes its claim over.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := rig.database.Pool.QueryRow(ctx, `
			select count(*) from pg_stat_activity
			where datname = current_database() and wait_event_type = 'Lock'
			  and query like '%update going_runs%' and pid <> pg_backend_pid()`).Scan(&waiting); err != nil {
			t.Fatalf("read who waits on the run: %v", err)
		}
		if waiting == 2 {
			break
		}
		if len(answers) == 2 || time.Now().After(deadline) {
			let()
			first, second := <-answers, <-answers
			t.Fatalf("the presses never both waited to take the claim over; they answered %d %s and %d %s",
				first.Code, first.Body.String(), second.Code, second.Body.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := lock.Commit(ctx); err != nil {
		t.Fatalf("let the run go: %v", err)
	}
	refused := <-answers
	let()
	ran := <-answers
	rig.served.inProcess = inner
	if refused.Code != http.StatusTooManyRequests || !strings.Contains(refused.Body.String(), codeGoingWriteInProgress) {
		t.Fatalf("the press that did not take the claim over answered %d %s, want 429 %s", refused.Code, refused.Body.String(), codeGoingWriteInProgress)
	}
	if ran.Code != http.StatusOK || !goingLineOf(t, ran).Outcome.OK {
		t.Fatalf("the press that took the claim over answered %d %s, want the write landed", ran.Code, ran.Body.String())
	}
	if title := rig.issueRead(t, issue).Title; title != "Titled by one press" || patches.Load() != 1 {
		t.Fatalf("%s is titled %q with %s sent %d times, want it retitled by one request", issue, title, patch, patches.Load())
	}
}

// keepUndoClaims makes the record refuse every update that takes an undo off the run, until the
// func it answers lets it again: a process that ended gives up none of the claims its undo made,
// which a handler returning gives up as it returns.
func (rig goingWritesRig) keepUndoClaims(t *testing.T) (allow func()) {
	t.Helper()
	ctx := context.Background()
	if _, err := rig.database.Pool.Exec(ctx, `
		create function going_test_keep_undo_claims() returns trigger language plpgsql as $$
		begin
		  if jsonb_array_length(new.undos) < jsonb_array_length(old.undos) then
		    raise exception 'the process ended, so no claim of its undo is given up';
		  end if;
		  return new;
		end $$;
		create trigger going_test_keep_undo_claims before update on going_runs for each row execute function going_test_keep_undo_claims();
	`); err != nil {
		t.Fatalf("keep the undo's claims: %v", err)
	}
	return func() {
		t.Helper()
		if _, err := rig.database.Pool.Exec(ctx, `drop trigger going_test_keep_undo_claims on going_runs; drop function going_test_keep_undo_claims()`); err != nil {
			t.Fatalf("let the record give up an undo's claims again: %v", err)
		}
	}
}

// dieUndoing sends one undo of the run that the route table ends, keeping every claim it made.
func (rig goingWritesRig) dieUndoing(t *testing.T, run string, body any) {
	t.Helper()
	allow := rig.keepUndoClaims(t)
	defer allow()
	func() {
		defer func() {
			if ended := recover(); ended != nil && !errors.Is(asError(ended), http.ErrAbortHandler) {
				panic(ended)
			}
		}()
		rig.browser(t, "alice", http.MethodPost, "/api/v1/going/runs/"+run+"/undo", body)
	}()
}

// ageGoingUndos puts every undo of the run past goingClaimBound, as a claim whose request ended
// long ago.
func (rig goingWritesRig) ageGoingUndos(t *testing.T, run string) {
	t.Helper()
	aged := time.Now().UTC().Add(-(rig.served.goingClaimBound() + time.Minute)).Format(time.RFC3339Nano)
	if _, err := rig.database.Pool.Exec(context.Background(), `
		update going_runs set undos = (
		  select jsonb_agg(jsonb_set(u, '{at}', to_jsonb($2::text)) order by n)
		  from jsonb_array_elements(undos) with ordinality e(u, n))
		where id = $1::uuid`, run, aged); err != nil {
		t.Fatalf("age the run's undos: %v", err)
	}
}

// TestGoingUndoOfACreatedIssueWhoseProcessEndedAfterItsReasonResumesAtTheClose (review round 5,
// thread 2; check c21): the undo of an issue the turn created posts its reason, and its process
// ends after the reason committed, before the close. Past the claim's bound the next undo takes
// it over and resumes at the close: the issue is closed, its reason posted once.
func TestGoingUndoOfACreatedIssueWhoseProcessEndedAfterItsReasonResumesAtTheClose(t *testing.T) {
	rig := newGoingWritesRig(t)
	createInteractionIssue(t, rig.mux, "DEMO", "An older issue", "The spec.\n")
	inner := rig.served.inProcess
	t.Cleanup(func() { rig.served.inProcess = inner })
	run, made := rig.runOne(t, "dispatch_issue", map[string]any{"project": "DEMO", "title": "Made, its undo ended", "force": true}, nil)
	const reason = "Undone as the process ended."
	rig.served.inProcess = goingDiesAfter(inner, http.MethodPost+" /api/v1/issues/"+made.Item+"/messages")
	rig.dieUndoing(t, run, map[string]any{"reason": reason})
	rig.served.inProcess = inner
	rig.ageGoingUndos(t, run)
	// The run lists as undoable: the next undo takes the dead one over (goingRunUndoable).
	listed := decodeBody[goingRunsPage](t, rig.browser(t, "alice", http.MethodGet, "/api/v1/me/going-runs?undoable=true", nil))
	if len(listed.Rows) != 1 || listed.Rows[0].ID != run {
		t.Fatalf("alice's undoable runs are %d, want the run whose undo ended, which the next undo takes over", len(listed.Rows))
	}
	line := oneLine(t, rig.undo(t, "alice", run, map[string]any{"reason": reason}), true)
	if status := rig.issueRead(t, made.Item).Status; status != "done" || rig.goingReasonsOn(t, made.Item, reason) != 1 {
		t.Fatalf("the undo after the process ended answered %+v and left %s %s with %d reasons, want it closed with its reason posted once",
			line, made.Item, status, rig.goingReasonsOn(t, made.Item, reason))
	}
}

// TestGoingUndoWhoseProcessEndedAfterItsOneRequestLandedIsRecordedDone (review round 5, thread 2;
// check c21): an undo of one request whose process ended after that request committed, before
// its record, is recorded done by the next undo past the claim's bound, which sends nothing again;
// the write is then settled, so a further undo answers no line.
func TestGoingUndoWhoseProcessEndedAfterItsOneRequestLandedIsRecordedDone(t *testing.T) {
	rig := newGoingWritesRig(t)
	createInteractionIssue(t, rig.mux, "DEMO", "The project's first", "The spec.\n")
	issue := rig.issue(t, "Titled before the turn")
	inner := rig.served.inProcess
	t.Cleanup(func() { rig.served.inProcess = inner })
	run, _ := rig.runOne(t, "dispatch_issue_update", map[string]any{"issue": issue, "title": "Titled by the turn"}, nil)
	rig.served.inProcess = goingDiesAfter(inner, http.MethodPatch+" /api/v1/issues/"+issue)
	rig.dieUndoing(t, run, nil)
	sent := map[string]int{}
	rig.served.inProcess = goingCommittedThenRefused(inner, "", sent)
	rig.ageGoingUndos(t, run)
	line := oneLine(t, rig.undo(t, "alice", run, nil), true)
	rig.served.inProcess = inner
	if title := rig.issueRead(t, issue).Title; title != "Titled before the turn" || sent[http.MethodPatch+" /api/v1/issues/"+issue] != 0 {
		t.Fatalf("the undo after the process ended answered %+v, left %q and sent the write back %d times, want it recorded done and nothing sent",
			line, title, sent[http.MethodPatch+" /api/v1/issues/"+issue])
	}
	if again := rig.undo(t, "alice", run, nil); len(again.Lines) != 0 {
		t.Fatalf("a further undo answered %+v, want no line: the write's undo is settled", again.Lines)
	}
}

// TestGoingUndoWhoseProcessEndedResumesTheWritesItDidNotReach (review round 5, thread 2; check
// c21): an undo of a turn of two writes claims both, undoes the newer, and its process ends after
// that write back committed, before it reached the older. Past the claim's bound the next undo
// records the newer done and undoes the older, which the ended undo claimed and never reached.
func TestGoingUndoWhoseProcessEndedResumesTheWritesItDidNotReach(t *testing.T) {
	rig := newGoingWritesRig(t)
	createInteractionIssue(t, rig.mux, "DEMO", "The project's first", "The spec.\n")
	older, newer := rig.issue(t, "The older before the turn"), rig.issue(t, "The newer before the turn")
	inner := rig.served.inProcess
	t.Cleanup(func() { rig.served.inProcess = inner })
	run := rig.proposed(t, "alice", false, "dispatch_issue_update",
		map[string]any{"issue": older, "title": "The older, retitled"}, map[string]any{"issue": newer, "title": "The newer, retitled"})
	for invocation := range 2 {
		rig.wrote(t, "alice", run, map[string]any{"offer": "dispatch_issue_update", "invocation": invocation, "approved": goingApprovedPerson})
	}
	rig.served.inProcess = goingDiesAfter(inner, http.MethodPatch+" /api/v1/issues/"+newer)
	rig.dieUndoing(t, run, nil)
	rig.served.inProcess = inner
	if title := rig.issueRead(t, older).Title; title != "The older, retitled" {
		t.Fatalf("the older issue is titled %q after the undo that ended, want it as the turn left it: the undo never reached it", title)
	}
	rig.ageGoingUndos(t, run)
	answer := rig.undo(t, "alice", run, nil)
	if len(answer.Lines) != 2 || !answer.Lines[0].Outcome.OK || !answer.Lines[1].Outcome.OK {
		t.Fatalf("the undo after the process ended answered %+v, want both writes undone", answer.Lines)
	}
	if o, n := rig.issueRead(t, older).Title, rig.issueRead(t, newer).Title; o != "The older before the turn" || n != "The newer before the turn" {
		t.Fatalf("the issues read %q and %q after the undo, want both titles back", o, n)
	}
}

// TestGoingUndoWhoseProcessEndedBeforeItsRequestLandedIsUnsettled (check c21): an undo whose
// process ended once its request was on its way, before its route committed anything, records no
// landing of it: past the claim's bound it is answered unsettled and never made again.
func TestGoingUndoWhoseProcessEndedBeforeItsRequestLandedIsUnsettled(t *testing.T) {
	rig := newGoingWritesRig(t)
	createInteractionIssue(t, rig.mux, "DEMO", "The project's first", "The spec.\n")
	issue := rig.issue(t, "Titled before, its undo cut short")
	inner := rig.served.inProcess
	t.Cleanup(func() { rig.served.inProcess = inner })
	run, _ := rig.runOne(t, "dispatch_issue_update", map[string]any{"issue": issue, "title": "Titled by the turn, its undo cut short"}, nil)
	rig.served.inProcess = goingDiesBefore(inner, http.MethodPatch+" /api/v1/issues/"+issue)
	rig.dieUndoing(t, run, nil)
	rig.served.inProcess = inner
	rig.ageGoingUndos(t, run)
	line := oneLine(t, rig.undo(t, "alice", run, nil), false)
	if line.Outcome.Code != codeGoingUndoUnsettled || rig.issueRead(t, issue).Title != "Titled by the turn, its undo cut short" {
		t.Fatalf("the undo after the process ended answered %+v, want it unsettled and the title as the turn left it", line.Outcome)
	}
}
