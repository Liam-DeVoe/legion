// answer_ended_by_delete_test.go holds R3 of dispatch://INFRA-1523 on the two answers a delete's
// own writes do not reach: the answer a key kept before the delete, which a repeat of the key
// would replay (answerEndedByDelete, request_keys.go), and a conditional read whose validator was
// given before the delete, which would answer 304 over the deleted text (referencesETag,
// commentListETag, eventFeedETag).
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/contracts"
)

// acceptSuggestion posts, to target, a suggestion on the document slug whose body is body,
// replacing quote with replacement, and accepts it, so the document gains a version named after
// body (acceptedVersionName). It answers the comment's id.
func acceptSuggestion(t *testing.T, handler http.Handler, target, slug, body, quote, replacement string) string {
	t.Helper()
	created := dispatchRequest(t, handler, http.MethodPost, target, map[string]any{
		"body": body, "anchor": map[string]any{"artifact": slug, "quote": quote},
		"suggestion": map[string]string{"replace_with": replacement},
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("suggest on %s: status=%d body=%s", slug, created.Code, created.Body.String())
	}
	id := decodeBody[struct {
		ID string `json:"id"`
	}](t, created).ID
	if accepted := dispatchRequest(t, handler, http.MethodPost, "/api/v1/comments/"+id+"/accept", map[string]any{}, "alice"); accepted.Code != http.StatusOK {
		t.Fatalf("accept the suggestion on %s: status=%d body=%s", slug, accepted.Code, accepted.Body.String())
	}
	return id
}

// createProjectIssue files an issue on project with title and spec, as alice.
func createProjectIssue(t *testing.T, handler http.Handler, project, title, spec string) string {
	t.Helper()
	created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", map[string]string{
		"project": project, "title": title, "spec": spec,
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("create issue %q: status=%d body=%s", title, created.Code, created.Body.String())
	}
	return decodeBody[struct {
		Key string `json:"key"`
	}](t, created).Key
}

// The census: every keyed route whose kept answer can carry the text of a comment or of an issue
// message and is not answered by a reread of the post (keyedRereads, whose rows are
// TestAKeyedRepeatAfterADeleteGivesThePostAsItStandsNow), one row each. A row's write is sent
// under a key and answers the deleted text - a post's snippet in a related-item check, in its own
// route or in a write's advice; a document version an accept named after a suggestion's body; a
// created post, or the block text of the post a reply answers - then the post is deleted and the
// same key sent again.
//
// The repeat is a replay, under the first answer's status, and gives no part of the deleted text.
// A route answered from its stored bytes answers ANSWER_NOT_KEPT, as for an answer it did not
// keep. A route that commits a recovery point answers what it made as it stands now and keeps no
// member of the first answer (replayKeptMembers); a row whose first answer carries one fails
// where the replay copies it.
func TestAKeyedRepeatOfAnAnswerADeleteEndedGivesNoPartOfTheDeletedText(t *testing.T) {
	_, handler, database := browserDocumentService(t)
	holder := createInteractionIssue(t, handler, "TEST", "Posts the census deletes", "alpha bravo charlie")
	asker := createProjectIssue(t, handler, "TEST", "Where the census asks", "delta echo foxtrot")
	notes := createProjectDocument(t, handler, "TEST", "Census notes", "# Census notes\n\nGolf hotel india.\n")
	asked := func(match string) string { return "Should the " + match + " calibration change?" }

	// commented posts a comment on the holder whose body finds match and carries phrase, for a
	// related-item check to find, and answers its withdrawal.
	commented := func(t *testing.T, match, phrase string) func(*httptest.ResponseRecorder) {
		comment := createThreadComment(t, handler, holder.Key, map[string]any{"body": match + " calibration drifted, " + phrase + "."}, "alice")
		refreshTermStats(t, database, "TEST")
		return func(*httptest.ResponseRecorder) { deleteThreadComment(t, handler, comment.ID, "alice") }
	}
	// keyed answers a send of body to target under a key, as alice.
	keyed := func(t *testing.T, method, target string, body map[string]any) func(string) *httptest.ResponseRecorder {
		return func(key string) *httptest.ResponseRecorder {
			return keyedRequest(t, handler, method, target, body, "alice", key)
		}
	}
	// withdrawCreated deletes the post the first answer is.
	withdrawCreated := func(t *testing.T, path string) func(*httptest.ResponseRecorder) {
		return func(first *httptest.ResponseRecorder) {
			id := decodeBody[struct {
				ID string `json:"id"`
			}](t, first).ID
			if deleted := dispatchRequest(t, handler, http.MethodDelete, path+id, nil, "alice"); deleted.Code != http.StatusOK {
				t.Fatalf("delete %s%s: status=%d body=%s", path, id, deleted.Code, deleted.Body.String())
			}
		}
	}
	decision := func(match, block string) map[string]any {
		return insertAtEnd(":::ask{#" + block + "}\n" + asked(match) + "\n:::")
	}

	rows := []struct {
		route string
		// recovered is a route that commits a recovery point (keyedReplays), and carries one whose
		// first answer holds a member a replay keeps (replayKeptMembers): an issue's post carries
		// the write's advice, and a project document's comment carries none (createCommentFor).
		recovered, carries bool
		// write makes what the route's write needs and returns that write, sent under a key, and
		// the delete of the post whose text the first answer carries.
		write func(t *testing.T, letter, match, phrase string) (send func(key string) *httptest.ResponseRecorder, withdraw func(first *httptest.ResponseRecorder))
	}{
		{route: "POST /api/v1/projects/{key}/related, a comment it finds", write: func(t *testing.T, _, match, phrase string) (func(string) *httptest.ResponseRecorder, func(*httptest.ResponseRecorder)) {
			withdraw := commented(t, match, phrase)
			return keyed(t, http.MethodPost, "/api/v1/projects/TEST/related", map[string]any{"title": match + " calibration", "keywords_only": true}), withdraw
		}},
		{route: "POST /api/v1/projects/{key}/related, a message it finds", write: func(t *testing.T, _, match, phrase string) (func(string) *httptest.ResponseRecorder, func(*httptest.ResponseRecorder)) {
			message := createIssueMessage(t, handler, holder.Key, map[string]any{"body": match + " calibration drifted, " + phrase + "."}, "alice")
			refreshTermStats(t, database, "TEST")
			return keyed(t, http.MethodPost, "/api/v1/projects/TEST/related", map[string]any{"title": match + " calibration", "keywords_only": true}),
				func(*httptest.ResponseRecorder) { deleteIssueMessage(t, handler, message.ID, "alice") }
		}},
		{route: "POST /api/v1/issues", write: func(t *testing.T, _, match, phrase string) (func(string) *httptest.ResponseRecorder, func(*httptest.ResponseRecorder)) {
			withdraw := commented(t, match, phrase)
			return keyed(t, http.MethodPost, "/api/v1/issues", map[string]any{"project": "TEST", "title": match + " calibration review"}), withdraw
		}},
		{route: "POST /api/v1/issues/{key}/asks", write: func(t *testing.T, _, match, phrase string) (func(string) *httptest.ResponseRecorder, func(*httptest.ResponseRecorder)) {
			withdraw := commented(t, match, phrase)
			return keyed(t, http.MethodPost, "/api/v1/issues/"+asker+"/asks", map[string]any{"question": asked(match)}), withdraw
		}},
		{route: "POST /api/v1/artifacts/{id}/asks", write: func(t *testing.T, _, match, phrase string) (func(string) *httptest.ResponseRecorder, func(*httptest.ResponseRecorder)) {
			withdraw := commented(t, match, phrase)
			return keyed(t, http.MethodPost, "/api/v1/artifacts/"+notes.ID+"/asks", map[string]any{"question": asked(match)}), withdraw
		}},
		{route: "POST /api/v1/artifacts/{id}/edits", write: func(t *testing.T, letter, match, phrase string) (func(string) *httptest.ResponseRecorder, func(*httptest.ResponseRecorder)) {
			withdraw := commented(t, match, phrase)
			spec := decodeBody[struct {
				PrimaryArtifactID string `json:"primary_artifact_id"`
			}](t, dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+asker, nil, "alice")).PrimaryArtifactID
			return keyed(t, http.MethodPost, "/api/v1/artifacts/"+spec+"/edits", decision(match, "census-"+letter)), withdraw
		}},
		{route: "POST /api/v1/issues/{key}/artifacts/{slug}/edits", write: func(t *testing.T, letter, match, phrase string) (func(string) *httptest.ResponseRecorder, func(*httptest.ResponseRecorder)) {
			withdraw := commented(t, match, phrase)
			return keyed(t, http.MethodPost, "/api/v1/issues/"+asker+"/artifacts/spec/edits", decision(match, "census-"+letter)), withdraw
		}},
		{route: "POST /api/v1/projects/{key}/artifacts/{slug}/edits", write: func(t *testing.T, letter, match, phrase string) (func(string) *httptest.ResponseRecorder, func(*httptest.ResponseRecorder)) {
			withdraw := commented(t, match, phrase)
			return keyed(t, http.MethodPost, "/api/v1/projects/TEST/artifacts/"+notes.Slug+"/edits", decision(match, "census-"+letter)), withdraw
		}},
		{route: "PATCH /api/v1/artifacts/{id}", write: func(t *testing.T, letter, _, phrase string) (func(string) *httptest.ResponseRecorder, func(*httptest.ResponseRecorder)) {
			document := uploadIssueDocument(t, handler, holder.Key, "renamed-"+letter+".md", "alpha bravo charlie")
			comment := acceptSuggestion(t, handler, "/api/v1/issues/"+holder.Key+"/comments", document.Slug, "Say this instead, "+phrase+".", "alpha", "omega")
			return keyed(t, http.MethodPatch, "/api/v1/artifacts/"+document.ID, map[string]any{"name": "renamed-" + letter + "-again.md"}),
				func(*httptest.ResponseRecorder) { deleteThreadComment(t, handler, comment, "alice") }
		}},
		{route: "POST /api/v1/issues/{key}/artifacts, to an existing document", write: func(t *testing.T, letter, _, phrase string) (func(string) *httptest.ResponseRecorder, func(*httptest.ResponseRecorder)) {
			name := "uploaded-" + letter + ".md"
			document := uploadIssueDocument(t, handler, holder.Key, name, "alpha bravo charlie")
			comment := acceptSuggestion(t, handler, "/api/v1/issues/"+holder.Key+"/comments", document.Slug, "Say this instead, "+phrase+".", "alpha", "omega")
			return keyed(t, http.MethodPost, "/api/v1/issues/"+holder.Key+"/artifacts", map[string]any{"name": name, "content": "alpha bravo charlie delta"}),
				func(*httptest.ResponseRecorder) { deleteThreadComment(t, handler, comment, "alice") }
		}},
		{route: "POST /api/v1/projects/{key}/artifacts, to an existing document", write: func(t *testing.T, letter, _, phrase string) (func(string) *httptest.ResponseRecorder, func(*httptest.ResponseRecorder)) {
			name := "Ledger " + letter
			document := createProjectDocument(t, handler, "TEST", name, "# Ledger\n\nkilo lima mike\n")
			comment := acceptSuggestion(t, handler, "/api/v1/artifacts/"+document.ID+"/comments", document.Slug, "Say this instead, "+phrase+".", "kilo", "november")
			return keyed(t, http.MethodPost, "/api/v1/projects/TEST/artifacts", map[string]any{"name": name, "content": "# Ledger\n\nkilo lima mike oscar\n"}),
				func(*httptest.ResponseRecorder) { deleteThreadComment(t, handler, comment, "alice") }
		}},
		{route: "POST /api/v1/issues/{key}/comments", recovered: true, carries: true, write: func(t *testing.T, _, _, phrase string) (func(string) *httptest.ResponseRecorder, func(*httptest.ResponseRecorder)) {
			return keyed(t, http.MethodPost, "/api/v1/issues/"+holder.Key+"/comments", map[string]any{"body": "A census comment, " + phrase + "."}),
				withdrawCreated(t, "/api/v1/comments/")
		}},
		{route: "POST /api/v1/issues/{key}/comments, a reply to a block", recovered: true, carries: true, write: func(t *testing.T, _, _, phrase string) (func(string) *httptest.ResponseRecorder, func(*httptest.ResponseRecorder)) {
			parent := createThreadComment(t, handler, holder.Key, map[string]any{"body": "A census parent, " + phrase + "."}, "alice")
			return keyed(t, http.MethodPost, "/api/v1/issues/"+holder.Key+"/comments", map[string]any{"body": "An answer.", "reply_to": parent.ID, "reply_to_block": "b1"}),
				func(*httptest.ResponseRecorder) { deleteThreadComment(t, handler, parent.ID, "alice") }
		}},
		{route: "POST /api/v1/artifacts/{id}/comments", recovered: true, write: func(t *testing.T, _, _, phrase string) (func(string) *httptest.ResponseRecorder, func(*httptest.ResponseRecorder)) {
			return keyed(t, http.MethodPost, "/api/v1/artifacts/"+notes.ID+"/comments", map[string]any{"body": "A census document comment, " + phrase + "."}),
				withdrawCreated(t, "/api/v1/comments/")
		}},
		{route: "POST /api/v1/issues/{key}/messages", recovered: true, carries: true, write: func(t *testing.T, _, _, phrase string) (func(string) *httptest.ResponseRecorder, func(*httptest.ResponseRecorder)) {
			return keyed(t, http.MethodPost, "/api/v1/issues/"+holder.Key+"/messages", map[string]any{"body": "A census message, " + phrase + "."}),
				withdrawCreated(t, "/api/v1/messages/")
		}},
	}
	for index, row := range rows {
		t.Run(row.route, func(t *testing.T) {
			letter := string(rune('a' + index))
			match, phrase := "quokkaledger"+letter, "zarquonended"+letter
			key := "ended-by-delete-" + letter
			send, withdraw := row.write(t, letter, match, phrase)
			first := send(key)
			if first.Code < 200 || first.Code > 299 || !strings.Contains(first.Body.String(), phrase) {
				t.Fatalf("the first write: status=%d body=%s, want a success holding %q, so this row can fail",
					first.Code, first.Body.String(), phrase)
			}

			withdraw(first)

			again := send(key)
			if again.Code != first.Code || again.Header().Get(contracts.IdempotencyReplayedHeader) != "?1" {
				t.Fatalf("the repeat: status=%d %s=%q body=%s, want the first answer's %d, replayed",
					again.Code, contracts.IdempotencyReplayedHeader, again.Header().Get(contracts.IdempotencyReplayedHeader),
					again.Body.String(), first.Code)
			}
			if strings.Contains(again.Body.String(), phrase) {
				t.Fatalf("the repeat after the delete gives the deleted text %q: %s", phrase, again.Body.String())
			}
			var firstMembers, againMembers map[string]json.RawMessage
			if err := json.Unmarshal(first.Body.Bytes(), &firstMembers); err != nil {
				t.Fatalf("decode the first answer: %v", err)
			}
			if err := json.Unmarshal(again.Body.Bytes(), &againMembers); err != nil {
				t.Fatalf("decode the repeat: %v", err)
			}
			if !row.recovered {
				var refusal struct {
					Code string `json:"code"`
				}
				if err := json.Unmarshal(again.Body.Bytes(), &refusal); err != nil || refusal.Code != contracts.IdempotencyAnswerNotKept {
					t.Fatalf("the repeat answers %s, want %s: the key no longer holds an answer it can give", again.Body.String(), contracts.IdempotencyAnswerNotKept)
				}
				return
			}
			if string(againMembers["id"]) != string(firstMembers["id"]) {
				t.Fatalf("the repeat answers %s, want %s, what the write made", againMembers["id"], firstMembers["id"])
			}
			carried := false
			for _, name := range replayKeptMembers {
				_, kept := firstMembers[name]
				carried = carried || kept
				if value, still := againMembers[name]; still {
					t.Fatalf("the repeat keeps the first answer's %s (%s) from an answer the delete ended", name, value)
				}
			}
			if carried != row.carries {
				t.Fatalf("the first answer carries a member of %v: %t, want %t: %s", replayKeptMembers, carried, row.carries, first.Body.String())
			}
		})
	}
}

// Every conditional read whose answer can carry a post's text, one row each: the read is made,
// its answer holds the text and carries an ETag, the post is deleted, and the read is made again
// under that ETag. It answers 200 with a new ETag and no part of the deleted text, never 304. The
// references read carries a document version an accept named after a suggestion's body, of a
// document on another issue than the one whose references they are, which the delete renames;
// the comment lists and the activity feed carry the post itself.
func TestAConditionalReadAfterADeleteAnswersWithoutTheDeletedText(t *testing.T) {
	_, handler, _ := browserDocumentService(t)
	holder := createInteractionIssue(t, handler, "TEST", "Posts the reads quote", "alpha bravo charlie")
	citer := createProjectIssue(t, handler, "TEST", "The issue that cites", "delta echo foxtrot")

	// cited accepts a suggestion carrying phrase on a document of the holder, which the citer's
	// comment cites, and answers the suggestion's withdrawal.
	cited := func(t *testing.T, letter, phrase string) func() {
		document := uploadIssueDocument(t, handler, holder.Key, "cited-"+letter+".md", "alpha bravo charlie")
		comment := acceptSuggestion(t, handler, "/api/v1/issues/"+holder.Key+"/comments", document.Slug, "Say this instead, "+phrase+".", "alpha", "omega")
		createThreadComment(t, handler, citer, map[string]any{"body": "See dispatch://" + holder.Key + "/artifact/" + document.Slug}, "alice")
		return func() { deleteThreadComment(t, handler, comment, "alice") }
	}

	rows := []struct {
		read string
		// write makes what the read carries phrase in and returns the read's path and the
		// delete of the post that holds it.
		write func(t *testing.T, letter, phrase string) (target string, withdraw func())
	}{
		{"GET /api/v1/issues/{key}/references", func(t *testing.T, letter, phrase string) (string, func()) {
			return "/api/v1/issues/" + citer + "/references", cited(t, letter, phrase)
		}},
		{"GET /api/v1/issues/{key}/references?versions=latest", func(t *testing.T, letter, phrase string) (string, func()) {
			return "/api/v1/issues/" + citer + "/references?versions=latest", cited(t, letter, phrase)
		}},
		{"GET /api/v1/issues/{key}/comments", func(t *testing.T, _, phrase string) (string, func()) {
			comment := createThreadComment(t, handler, holder.Key, map[string]any{"body": "A listed comment, " + phrase + "."}, "alice")
			return "/api/v1/issues/" + holder.Key + "/comments", func() { deleteThreadComment(t, handler, comment.ID, "alice") }
		}},
		{"GET /api/v1/artifacts/{id}/comments", func(t *testing.T, letter, phrase string) (string, func()) {
			document := createProjectDocument(t, handler, "TEST", "Listed "+letter, "# Listed\n\nkilo lima mike\n")
			created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+document.ID+"/comments",
				map[string]any{"body": "A listed document comment, " + phrase + "."}, "alice")
			if created.Code != http.StatusCreated {
				t.Fatalf("comment on %s: status=%d body=%s", document.Name, created.Code, created.Body.String())
			}
			id := decodeBody[struct {
				ID string `json:"id"`
			}](t, created).ID
			return "/api/v1/artifacts/" + document.ID + "/comments", func() { deleteThreadComment(t, handler, id, "alice") }
		}},
		{"GET /api/v1/issues/{key}/events", func(t *testing.T, _, phrase string) (string, func()) {
			// An issue of its own, so the comment's events stand on the feed's first page.
			issue := createProjectIssue(t, handler, "TEST", "A feed of its own", "golf hotel india")
			comment := createThreadComment(t, handler, issue, map[string]any{"body": "A logged comment, " + phrase + "."}, "alice")
			return "/api/v1/issues/" + issue + "/events", func() { deleteThreadComment(t, handler, comment.ID, "alice") }
		}},
	}
	read := func(target, etag string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("X-Dispatch-User", "alice")
		if etag != "" {
			request.Header.Set("If-None-Match", etag)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	for index, row := range rows {
		t.Run(row.read, func(t *testing.T) {
			letter := string(rune('a' + index))
			phrase := "zarquonconditional" + letter
			target, withdraw := row.write(t, letter, phrase)
			first := read(target, "")
			etag := first.Header().Get("ETag")
			if first.Code != http.StatusOK || etag == "" || !strings.Contains(first.Body.String(), phrase) {
				t.Fatalf("the first read: status=%d ETag=%q body=%s, want 200 under an ETag holding %q, so this row can fail",
					first.Code, etag, first.Body.String(), phrase)
			}

			withdraw()

			again := read(target, etag)
			if again.Code != http.StatusOK || again.Header().Get("ETag") == etag {
				t.Fatalf("the read under the ETag given before the delete: status=%d ETag=%q (was %q), want 200 under a new ETag",
					again.Code, again.Header().Get("ETag"), etag)
			}
			if strings.Contains(again.Body.String(), phrase) {
				t.Fatalf("the read after the delete gives the deleted text %q: %s", phrase, again.Body.String())
			}
		})
	}
}
