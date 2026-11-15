package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The reviewer's writes that are never held (INFRA-3231's R5 and Decisions 8 and 26): a person's
// own typed command, posted at once with no review; a document version, saved at once and
// reviewed after the save; and an agent's edit of a posted ask, comment or issue title, saved at
// once and reviewed after the save, with nothing more happening when the writer does not act.

// asWriterTyped writes as the rig's writer session with Dispatch-Person-Typed: 1, as the dispatch
// command does for a line a person typed in that session.
func (rig *reviewRig) asWriterTyped(method, target string, body map[string]any) *httptest.ResponseRecorder {
	rig.t.Helper()
	withActor := map[string]any{"actor": map[string]any{"kind": "session", "id": heldWriter}}
	for key, value := range body {
		withActor[key] = value
	}
	data, err := json.Marshal(withActor)
	if err != nil {
		rig.t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer agent-token")
	request.Header.Set(contracts.PersonTypedHeader, "1")
	response := httptest.NewRecorder()
	rig.handler.ServeHTTP(response, request)
	return response
}

// reviewCalls counts the stand-in's review calls whose text holds marker.
func (rig *reviewRig) reviewCalls(marker string) int {
	calls := 0
	for _, request := range rig.stand.Requests() {
		if strings.Contains(request.Text, reviewMarker) && strings.Contains(reviewedText(request), marker) {
			calls++
		}
	}
	return calls
}

// waitFrame waits for a findings frame naming id and answers its body.
func (rig *reviewRig) waitFrame(id string) string {
	rig.t.Helper()
	var body string
	pollUntil(rig.t, 20*time.Second, "a findings frame naming "+id, func() bool {
		for _, frame := range rig.listener.frames() {
			if frame["id"] == id {
				body, _ = frame["body"].(string)
				return true
			}
		}
		return false
	})
	return body
}

// outcomeCount counts the writing_check_events rows of outcome.
func (rig *reviewRig) outcomeCount(outcome string) int64 {
	return countRows(rig.t, rig.database, `select count(*) from writing_check_events where outcome = $1`, outcome)
}

// A comment the writer session's person typed (`!dispatch comment …` through pi-envoy's typed
// shell, which the dispatch command marks Dispatch-Person-Typed: 1) posts at once with the
// reviewer set to hold every kind: no hold, no review call, a row exempt_person_typed with its
// session, and its comment.created marked person_typed. The same comment with no header is held
// (the negative control).
func TestPersonTypedCommentPostsAtOnceUnderHold(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Asks: ReviewHold, Issues: ReviewHold, Comments: ReviewHold, Messages: ReviewHold}, nil)
	typed := rig.asWriterTyped(http.MethodPost, "/api/v1/issues/"+rig.issue+"/comments", map[string]any{
		"body": "Status\nWe spin up the node. " + vacuousPhrase,
	})
	if typed.Code != http.StatusCreated {
		t.Fatalf("the typed comment: %d %s", typed.Code, typed.Body.String())
	}
	answer := decodeBody[heldWriteAnswer](t, typed)
	if answer.Held != nil || answer.ID == "" {
		t.Fatalf("the typed comment answered %s, want it posted with no held object", typed.Body.String())
	}
	if !slices.Contains(rig.commentIDs(), answer.ID) {
		t.Fatal("alice does not read the typed comment at once")
	}
	var actorID string
	if err := rig.database.Pool.QueryRow(context.Background(), `
		select actor_id from writing_check_events where outcome = 'exempt_person_typed'`).Scan(&actorID); err != nil {
		t.Fatalf("read the exempt_person_typed row: %v", err)
	}
	if actorID != heldWriter {
		t.Fatalf("the exempt_person_typed row names %q, want the session %s", actorID, heldWriter)
	}
	if _, reader, project := rig.eventOf(writingCheckPersonTyped); reader != "alice" || project != "RVW" {
		t.Fatalf("the exempt_person_typed row: reader %q project %q", reader, project)
	}
	if marked := countRows(t, rig.database, `
		select count(*) from events where type = 'comment.created' and payload->>'id' = $1 and (payload->>'person_typed')::boolean`,
		answer.ID); marked != 1 {
		t.Fatalf("%d comment.created events of the typed comment carry person_typed, want 1", marked)
	}
	time.Sleep(300 * time.Millisecond)
	if calls := rig.reviewCalls("We spin up the node."); calls != 0 {
		t.Fatalf("the reviewer read the typed comment %d times, want none", calls)
	}
	if held := rig.outcomeCount(reviewHeld); held != 0 {
		t.Fatalf("%d held rows for a typed comment", held)
	}
	// The negative control: the agent's own comment, with no header, is held.
	rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/comments", map[string]any{
		"body": "Status\nWe spin up the node. " + vacuousPhrase,
	}), "the agent's own comment")
}

// An agent's document version saves at once with DISPATCH_REVIEWER_DOCUMENTS=after, and its
// findings reach the writer as a frame naming the document, recorded reviewed_after; with the
// setting off no review runs.
func TestTheReviewerReadsADocumentVersionAfterItsSave(t *testing.T) {
	content := "# Rollout notes\n\nStatus of the rollout. " + vacuousPhrase + "\n"
	t.Run("after", func(t *testing.T) {
		rig := newReviewRig(t, ReviewerConfig{Documents: ReviewAfter}, nil)
		uploaded := rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/artifacts", map[string]any{
			"name": "rollout.md", "content": content,
		})
		if uploaded.Code != http.StatusCreated {
			t.Fatalf("the upload: %d %s", uploaded.Code, uploaded.Body.String())
		}
		var answer artifactUploadResponse
		if err := json.Unmarshal(uploaded.Body.Bytes(), &answer); err != nil || answer.Artifact.ID == "" {
			t.Fatalf("the upload answered %s (%v)", uploaded.Body.String(), err)
		}
		body := rig.waitFrame(answer.Artifact.ID)
		if !strings.Contains(body, "Dispatch posted your document") || !strings.Contains(body, vacuousPhrase) {
			t.Fatalf("the document's frame reads %q", body)
		}
		pollUntil(t, 10*time.Second, "the document's reviewed_after row", func() bool { return rig.outcomeCount(reviewReviewedAfter) == 1 })
		if held := countRows(t, rig.database, `select count(*) from held_texts`); held != 0 {
			t.Fatalf("%d held texts for a document version", held)
		}
	})
	t.Run("off", func(t *testing.T) {
		rig := newReviewRig(t, ReviewerConfig{Comments: ReviewHold}, nil)
		uploaded := rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/artifacts", map[string]any{
			"name": "rollout.md", "content": content,
		})
		if uploaded.Code != http.StatusCreated {
			t.Fatalf("the upload: %d %s", uploaded.Code, uploaded.Body.String())
		}
		// A held comment's review is the marker that the reviewer ran since the upload.
		comment := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/comments", map[string]any{
			"body": "Status\nThe sweep runs. " + vacuousPhrase,
		}), "the marker comment")
		rig.waitHeld(comment.ID, heldAwaitingWriter)
		if calls := rig.reviewCalls("Status of the rollout."); calls != 0 {
			t.Fatalf("the reviewer read the document %d times with the setting off", calls)
		}
	})
}

// editRig is a reviewer rig whose writer is a personal token of bob acting for the writer session,
// the credential an agent edits a comment with (the comment route acts as a person).
type editRig struct {
	*reviewRig
	token string
}

func newEditRig(t *testing.T, cfg ReviewerConfig) editRig {
	rig := newReviewRig(t, cfg, nil)
	token := mintPersonalToken(t, rig.handler, "bob", "writer", []string{model.ScopePerson})
	return editRig{reviewRig: rig, token: token.Token}
}

// write writes as the token's person acting for the writer session, naming that session as the
// actor, as the ask and issue routes take a bearer caller's write.
func (rig editRig) write(method, target string, body map[string]any) *httptest.ResponseRecorder {
	rig.t.Helper()
	withActor := map[string]any{"actor": map[string]any{"kind": "session", "id": heldWriter}}
	for key, value := range body {
		withActor[key] = value
	}
	return rig.writeAsPerson(method, target, withActor)
}

// writeAsPerson writes as the token's person acting for the writer session, naming no actor.
func (rig editRig) writeAsPerson(method, target string, body map[string]any) *httptest.ResponseRecorder {
	rig.t.Helper()
	separator := "?"
	if strings.Contains(target, "?") {
		separator = "&"
	}
	return agentRequest(rig.t, rig.handler, method, target+separator+"session="+heldWriter, body, rig.token)
}

// posted is the id a write answers, once its text has posted: at once where it was not held, and
// once its review passed where it was.
func (rig editRig) posted(response *httptest.ResponseRecorder, what string) string {
	rig.t.Helper()
	if response.Code != http.StatusCreated {
		rig.t.Fatalf("%s: %d %s", what, response.Code, response.Body.String())
	}
	answer := decodeBody[heldWriteAnswer](rig.t, response)
	if answer.ID == "" {
		answer.ID = answer.Key
	}
	if answer.Held != nil {
		rig.waitHeld(answer.ID, heldPosted)
	}
	return answer.ID
}

// An agent's edit through each of PATCH /api/v1/asks/{id}, PATCH /api/v1/comments/{id} and PATCH
// /api/v1/issues/{key} saves at once whatever the kind's setting, and with the setting after or
// hold its findings reach the writer as a frame naming the text; a writer that does not act leaves
// the edit as it saved. With the setting off no review runs (Decision 8 of INFRA-3231's plan).
func TestTheReviewerReadsAnAgentsEditAfterTheSave(t *testing.T) {
	for _, setting := range []string{ReviewAfter, ReviewHold, ReviewOff} {
		t.Run(setting, func(t *testing.T) {
			cfg := ReviewerConfig{Asks: setting, Issues: setting, Comments: setting, WriterWindow: time.Minute}
			if setting == ReviewOff {
				cfg.Messages = ReviewHold
			}
			rig := newEditRig(t, cfg)
			askID := rig.posted(rig.write(http.MethodPost, "/api/v1/issues/"+rig.issue+"/asks", map[string]any{
				"question": "Ship the reviewer today?",
				"options":  []map[string]string{{"label": "Ship it", "description": "Release it today."}, {"label": "Do not file", "description": "Wait."}},
			}), "the ask")
			// The comment route edits as the token's person, beside the session it acts for, so
			// the posted comment is bob's acting for the writer session, as the store holds it.
			var commentID string
			if err := rig.database.Pool.QueryRow(context.Background(), `
				insert into comments (issue_key, author, body)
				values ($1, jsonb_build_object('kind', 'user', 'id', 'bob', 'act', jsonb_build_object('session', $2::text)),
					'Status'||chr(10)||'The sweep runs every ten seconds.')
				returning id::text`, rig.issue, heldWriter).Scan(&commentID); err != nil {
				t.Fatalf("seed the posted comment: %v", err)
			}
			issueKey := rig.posted(rig.write(http.MethodPost, "/api/v1/issues", map[string]any{
				"project": "RVW", "title": "Retry the sweep", "spec": "The sweep retries a failed post.",
			}), "the issue")

			edits := []struct {
				what, target, id, marker string
				body                     map[string]any
				stored                   string
			}{
				{"ask", "/api/v1/asks/" + askID, askID, "Ship the reviewer today? ",
					map[string]any{"question": "Ship the reviewer today? " + vacuousPhrase},
					`select question from asks where id = $1::uuid`},
				{"comment", "/api/v1/comments/" + commentID, commentID, "Status\nThe sweep runs. ",
					map[string]any{"body": "Status\nThe sweep runs. " + vacuousPhrase},
					`select body from comments where id = $1::uuid`},
				{"issue", "/api/v1/issues/" + issueKey, issueKey, "Retry the sweep. ",
					map[string]any{"title": "Retry the sweep. " + vacuousPhrase},
					`select title from issues where key = $1`},
			}
			for _, edit := range edits {
				write := rig.write
				if edit.what == "comment" {
					write = rig.writeAsPerson
				}
				response := write(http.MethodPatch, edit.target, edit.body)
				if response.Code != http.StatusOK || strings.Contains(response.Body.String(), `"held"`) {
					t.Fatalf("the %s edit: %d %s, want it saved at once", edit.what, response.Code, response.Body.String())
				}
			}
			if setting == ReviewOff {
				marker := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/messages", map[string]any{
					"body": "Status\nThe marker message. " + vacuousPhrase,
				}), "the marker message")
				rig.waitFrame(marker.ID)
			}
			for _, edit := range edits {
				if setting == ReviewOff {
					if calls := rig.reviewCalls(edit.marker); calls != 0 {
						t.Fatalf("the reviewer read the %s edit %d times with the setting off", edit.what, calls)
					}
					continue
				}
				body := rig.waitFrame(edit.id)
				if !strings.Contains(body, "Dispatch posted your") || !strings.Contains(body, vacuousPhrase) {
					t.Fatalf("the %s edit's frame reads %q", edit.what, body)
				}
			}
			// The writer does not act: each edit stays as it saved.
			time.Sleep(300 * time.Millisecond)
			for _, edit := range edits {
				var stored string
				if err := rig.database.Pool.QueryRow(context.Background(), edit.stored, edit.id).Scan(&stored); err != nil {
					t.Fatalf("read the edited %s: %v", edit.what, err)
				}
				if !strings.Contains(stored, vacuousPhrase) {
					t.Fatalf("the edited %s reads %q, want the edit as it saved", edit.what, stored)
				}
			}
		})
	}
}
