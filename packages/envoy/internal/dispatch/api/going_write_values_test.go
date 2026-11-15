package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestGoingUndoComparesOnlyWhatTheWritesOwnTransactionFoundAndLeft (review round 5, finding 1;
// principle 2): the values a write back's undo compares and puts back are the ones the write's
// route found under its lock and left, as that route answered them, never a read on either side
// of it. Another writer who changes the item between the bar's write and its read after it keeps
// their change: the undo compares what the turn left, which is not what is there, and refuses.
// One who changes it between the bar's read before and its write keeps it too: the undo puts back
// what the write replaced, their value, or, where the route cannot answer that value, refuses.
func TestGoingUndoComparesOnlyWhatTheWritesOwnTransactionFoundAndLeft(t *testing.T) {
	rig := newGoingWritesRig(t)
	issue := createInteractionIssue(t, rig.mux, "DEMO", "Moved around the bar", "The spec.\n").Key
	inner := rig.served.inProcess
	t.Cleanup(func() { rig.served.inProcess = inner })
	// writing runs one write of the bar while served is the route table, then serves the
	// table again.
	writing := func(t *testing.T, served http.Handler, action string, arguments map[string]any) string {
		t.Helper()
		rig.served.inProcess = served
		defer func() { rig.served.inProcess = inner }()
		run, _ := rig.runOne(t, action, arguments, nil)
		return run
	}
	renames := func(t *testing.T, key, title string) func() {
		return func() {
			rig.fixture(t, "bob", http.MethodPatch, "/api/v1/issues/"+key, map[string]any{"title": title})
		}
	}

	t.Run("a title another writer changed after the write", func(t *testing.T) {
		key := rig.issue(t, "Original title")
		run := writing(t, goingAfter(inner, http.MethodPatch+" /api/v1/issues/"+key, renames(t, key, "Bob's title")),
			"dispatch_issue_update", map[string]any{"issue": key, "title": "The bar's title"})
		line := oneLine(t, rig.undo(t, "alice", run, nil), false)
		if title := rig.issueRead(t, key).Title; title != "Bob's title" {
			t.Fatalf("the issue is titled %q after the undo answered %+v, want bob's title kept", title, line.Outcome)
		}
	})

	t.Run("a title another writer changed before the write", func(t *testing.T) {
		key := rig.issue(t, "Original title")
		run := writing(t, goingBefore(inner, http.MethodPatch+" /api/v1/issues/"+key, renames(t, key, "Bob's title")),
			"dispatch_issue_update", map[string]any{"issue": key, "title": "The bar's title"})
		line := oneLine(t, rig.undo(t, "alice", run, nil), true)
		if title := rig.issueRead(t, key).Title; title != "Bob's title" {
			t.Fatalf("the issue is titled %q after the undo answered %+v, want bob's title, the one the write replaced, back", title, line.Outcome)
		}
	})

	// between reports whether the issue moved stands between after and before in rank order.
	between := func(t *testing.T, moved, after, before string) bool {
		t.Helper()
		at := rig.issueRead(t, moved).Rank
		return rig.issueRead(t, after).Rank < at && at < rig.issueRead(t, before).Rank
	}
	t.Run("a rank another writer moved after the write", func(t *testing.T) {
		first, second, third := rig.issue(t, "First"), rig.issue(t, "Second"), rig.issue(t, "Third")
		moved := rig.issue(t, "Moved")
		run := writing(t, goingAfter(inner, http.MethodPatch+" /api/v1/issues/"+moved, func() {
			rig.fixture(t, "bob", http.MethodPatch, "/api/v1/issues/"+moved, map[string]any{"rank": map[string]any{"after": second, "before": third}})
		}), "dispatch_issue_update", map[string]any{"issue": moved, "after": first, "before": second})
		line := oneLine(t, rig.undo(t, "alice", run, nil), false)
		if !between(t, moved, second, third) {
			t.Fatalf("the undo answered %+v and moved the issue off bob's place between %s and %s", line.Outcome, second, third)
		}
	})

	t.Run("a rank another writer moved before the write", func(t *testing.T) {
		first, second, third := rig.issue(t, "First"), rig.issue(t, "Second"), rig.issue(t, "Third")
		moved := rig.issue(t, "Moved")
		run := writing(t, goingBefore(inner, http.MethodPatch+" /api/v1/issues/"+moved, func() {
			rig.fixture(t, "bob", http.MethodPatch, "/api/v1/issues/"+moved, map[string]any{"rank": map[string]any{"after": second, "before": third}})
		}), "dispatch_issue_update", map[string]any{"issue": moved, "after": first, "before": second})
		line := oneLine(t, rig.undo(t, "alice", run, nil), true)
		if !between(t, moved, second, third) {
			t.Fatalf("the undo answered %+v and did not put the issue back at bob's place between %s and %s, the one the write moved it from", line.Outcome, second, third)
		}
	})

	t.Run("an advances relation another writer changed after the write", func(t *testing.T) {
		key := rig.issue(t, "Advancing")
		ship := createTestPriority(t, rig.mux, map[string]any{"title": "Ship the daemon"}, "alice").ID
		cut := createTestPriority(t, rig.mux, map[string]any{"title": "Cut the queue"}, "alice").ID
		hold := createTestPriority(t, rig.mux, map[string]any{"title": "Hold the line"}, "alice").ID
		rig.fixture(t, "alice", http.MethodPatch, "/api/v1/issues/"+key, map[string]any{"advances": []string{ship}})
		revision := rig.issueRead(t, key).AdvancesRevision
		run := writing(t, goingAfter(inner, http.MethodPatch+" /api/v1/issues/"+key, func() {
			// Bob writes at the revision the bar's write left, as a writer who read it after does.
			rig.fixture(t, "bob", http.MethodPatch, "/api/v1/issues/"+key,
				map[string]any{"advances": []string{hold}, "advances_revision": rig.issueRead(t, key).AdvancesRevision})
		}), "dispatch_issue_update", map[string]any{"issue": key, "advances": []any{cut}, "advances_revision": revision})
		line := oneLine(t, rig.undo(t, "alice", run, nil), false)
		if read := rig.issueRead(t, key); !slices.Equal(advanceEntries(read.Advances), []string{hold + " own"}) {
			t.Fatalf("the undo answered %+v and left %s advancing %v, want bob's %s kept", line.Outcome, key, advanceEntries(read.Advances), hold)
		}
	})

	question := func(t *testing.T, ask string) string {
		t.Helper()
		return decodeBody[goingAskRead](t, rig.fixture(t, "alice", http.MethodGet, "/api/v1/asks/"+ask, nil)).Ask.Question
	}
	rewords := func(t *testing.T, ask string) func() {
		return func() {
			rig.fixture(t, "alice", http.MethodPatch, "/api/v1/asks/"+ask, map[string]any{"question": "Ship it on Tuesday?"})
		}
	}
	t.Run("an ask's text another writer changed after the edit", func(t *testing.T) {
		ask := rig.ask(t, issue, "Ship it on Friday?")
		run := writing(t, goingAfter(inner, http.MethodPatch+" /api/v1/asks/"+ask, rewords(t, ask)),
			"dispatch_edit_ask", map[string]any{"ask": ask, "question": "Ship it on Monday?"})
		line := oneLine(t, rig.undo(t, "alice", run, nil), false)
		if words := question(t, ask); words != "Ship it on Tuesday?" {
			t.Fatalf("the ask reads %q after the undo answered %+v, want the later words kept", words, line.Outcome)
		}
	})

	t.Run("an ask's text another writer changed before the edit", func(t *testing.T) {
		ask := rig.ask(t, issue, "Ship it on Friday?")
		run := writing(t, goingBefore(inner, http.MethodPatch+" /api/v1/asks/"+ask, rewords(t, ask)),
			"dispatch_edit_ask", map[string]any{"ask": ask, "question": "Ship it on Monday?"})
		line := oneLine(t, rig.undo(t, "alice", run, nil), true)
		if words := question(t, ask); words != "Ship it on Tuesday?" {
			t.Fatalf("the ask reads %q after the undo answered %+v, want the words the edit replaced back", words, line.Outcome)
		}
	})

	thread := func(t *testing.T, comment string) shapedComment {
		t.Helper()
		return decodeBody[commentRead](t, rig.fixture(t, "alice", http.MethodGet, "/api/v1/comments/"+comment, nil)).Comment
	}
	t.Run("a thread another writer resolved again after the resolve", func(t *testing.T) {
		comment := decodeBody[model.Comment](t, rig.fixture(t, "alice", http.MethodPost, "/api/v1/issues/"+issue+"/comments", map[string]any{"body": "A thread bob settles again."})).ID
		run := writing(t, goingAfter(inner, http.MethodPost+" /api/v1/comments/"+comment+"/resolve", func() {
			rig.fixture(t, "bob", http.MethodPost, "/api/v1/comments/"+comment+"/reopen", nil)
			rig.fixture(t, "bob", http.MethodPost, "/api/v1/comments/"+comment+"/resolve", nil)
		}), "dispatch_resolve_comment", map[string]any{"comment": comment})
		line := oneLine(t, rig.undo(t, "alice", run, nil), false)
		if read := thread(t, comment); !read.Resolved || read.ResolvedBy == nil || model.CanonicalLogin(read.ResolvedBy.ID) != "bob" {
			t.Fatalf("the thread reads resolved %v by %+v after the undo answered %+v, want bob's resolution kept", read.Resolved, read.ResolvedBy, line.Outcome)
		}
	})

	t.Run("a thread another writer resolved before the resolve", func(t *testing.T) {
		comment := decodeBody[model.Comment](t, rig.fixture(t, "alice", http.MethodPost, "/api/v1/issues/"+issue+"/comments", map[string]any{"body": "A thread bob settles first."})).ID
		run := writing(t, goingBefore(inner, http.MethodPost+" /api/v1/comments/"+comment+"/resolve", func() {
			rig.fixture(t, "bob", http.MethodPost, "/api/v1/comments/"+comment+"/resolve", nil)
		}), "dispatch_resolve_comment", map[string]any{"comment": comment})
		line := oneLine(t, rig.undo(t, "alice", run, nil), true)
		if read := thread(t, comment); !read.Resolved {
			t.Fatalf("the undo answered %+v and reopened a thread bob resolved before the write, want it resolved", line.Outcome)
		}
	})

	text := func(t *testing.T, id string) string {
		t.Helper()
		return decodeBody[artifactText](t, rig.fixture(t, "alice", http.MethodGet, "/api/v1/artifacts/"+id+"/text", nil)).Markdown
	}
	edits := func(t *testing.T, id string) func() {
		return func() {
			rig.fixture(t, "bob", http.MethodPost, "/api/v1/artifacts/"+id+"/edits", map[string]any{
				"ops": []any{map[string]any{"op": "replace", "find": "second line", "with": "line bob wrote"}},
			})
		}
	}
	documentEdit := func(slug string) map[string]any {
		return map[string]any{"issue": issue, "artifact": slug,
			"ops": []any{map[string]any{"op": "replace", "find": "first line", "with": "opening line"}}}
	}
	t.Run("a document another writer edited after the edit", func(t *testing.T) {
		notes := uploadDocument(t, rig.mux, "/api/v1/issues/"+issue, "edited-after.md", "# Notes\n\nThe first line.\n\nThe second line.\n")
		run := writing(t, goingAfter(inner, http.MethodPost+" /api/v1/issues/"+issue+"/artifacts/"+notes.Artifact.Slug+"/edits", edits(t, notes.Artifact.ID)),
			"dispatch_doc_edit", documentEdit(notes.Artifact.Slug))
		line := oneLine(t, rig.undo(t, "alice", run, nil), false)
		if read := text(t, notes.Artifact.ID); !strings.Contains(read, "line bob wrote") {
			t.Fatalf("the document reads %q after the undo answered %+v, want bob's passage kept", read, line.Outcome)
		}
	})

	t.Run("a document another writer edited before the edit", func(t *testing.T) {
		notes := uploadDocument(t, rig.mux, "/api/v1/issues/"+issue, "edited-before.md", "# Notes\n\nThe first line.\n\nThe second line.\n")
		run := writing(t, goingBefore(inner, http.MethodPost+" /api/v1/issues/"+issue+"/artifacts/"+notes.Artifact.Slug+"/edits", edits(t, notes.Artifact.ID)),
			"dispatch_doc_edit", documentEdit(notes.Artifact.Slug))
		line := oneLine(t, rig.undo(t, "alice", run, nil), false)
		if read := text(t, notes.Artifact.ID); !strings.Contains(read, "line bob wrote") {
			t.Fatalf("the document reads %q after the undo answered %+v, want bob's passage kept", read, line.Outcome)
		}
	})

	t.Run("a follow another writer made before the follow", func(t *testing.T) {
		ask := rig.ask(t, issue, "Who reviews the change?")
		follower := "/api/v1/asks/" + ask + "/followers/" + goingPlanner
		rig.served.inProcess = goingBefore(inner, http.MethodPut+" "+follower, func() {
			rig.fixture(t, "alice", http.MethodPut, follower, nil)
		})
		run, _ := rig.runOne(t, "dispatch_follow", map[string]any{"ask": ask, "action": "follow"}, goingPlannerTitle)
		rig.served.inProcess = inner
		line := oneLine(t, rig.undo(t, "alice", run, nil), true)
		read := decodeBody[askFollowersRead](t, rig.fixture(t, "alice", http.MethodGet, "/api/v1/asks/"+ask+"/followers", nil))
		if !slices.ContainsFunc(read.Followers, func(follower model.AskFollower) bool { return follower.SessionID == goingPlanner }) {
			t.Fatalf("the undo answered %+v and unfollowed a follow another writer made before the write, want it kept", line.Outcome)
		}
	})

	t.Run("an upload's version another writer wrote before it", func(t *testing.T) {
		notes := uploadDocument(t, rig.mux, "/api/v1/issues/"+issue, "uploaded-over.md", "# Notes\n\nThe first version.\n")
		rig.served.inProcess = goingBefore(inner, http.MethodPost+" /api/v1/issues/"+issue+"/artifacts", func() {
			uploadDocument(t, rig.mux, "/api/v1/issues/"+issue, "uploaded-over.md", "# Notes\n\nThe second version, bob's.\n")
		})
		_, line := rig.runOne(t, "dispatch_artifact", map[string]any{
			"issue": issue, "name": "uploaded-over.md", "content": "# Notes\n\nThe bar's version.\n", "force": true,
		}, nil)
		rig.served.inProcess = inner
		var before goingUploadBefore
		if err := json.Unmarshal(line.Before, &before); err != nil || before.ID != notes.Artifact.ID || before.Version != 2 {
			t.Fatalf("the upload's line reads before %s, want version 2 of %s, the one it wrote its next version over", line.Before, notes.Artifact.ID)
		}
	})
}
