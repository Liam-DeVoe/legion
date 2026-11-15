package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/agentloop"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/modeltest"
	"github.com/sjawhar/envoy/internal/dispatch/reviewer"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The reviewer of INFRA-3231's R5 on the server: a stand-in model that flags `This names the
// problem.`, and the word `store` for a reader whose records hold a question about it, a fake
// listener that keeps every frame it is sent, and an issue assigned to alice, the person who reads
// what an agent writes on it.

const (
	heldWriter    = "s-writer"
	reviewDaemon  = "legion-daemon:RVW"
	reviewMarker  = "You review a text for its reader."
	vacuousPhrase = "This names the problem."
)

// reviewFinding is the stand-in's finding of the vacuous phrase or of `store`.
func standInReview(t *testing.T, quote string, rewrite map[string]any) string {
	t.Helper()
	reply := map[string]any{
		"findings": []map[string]string{{"rule_set": "phrases", "quote": quote, "why": "It states no fact.", "fix": "Say what fails."}},
		"rewrite":  rewrite,
	}
	encoded, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// reviewedText is the text part of a review request's user message.
func reviewedText(request modeltest.Request) string {
	start := strings.Index(request.Text, "<text kind=")
	end := strings.Index(request.Text, "</text>")
	if start < 0 || end < start {
		return ""
	}
	return request.Text[start:end]
}

// reviewStandIn is the stand-in reviewer: findings for the vacuous phrase, findings for `store`
// where the reader's words hold it, and none for anything else. rewrite is the rewrite each
// finding comes with, by the text's kind.
func reviewStandIn(t *testing.T) *modeltest.Model {
	t.Helper()
	isReview := func(request modeltest.Request) bool { return strings.Contains(request.Text, reviewMarker) }
	return modeltest.New(t,
		modeltest.Rule{
			Match: func(request modeltest.Request) bool {
				return isReview(request) && strings.Contains(reviewedText(request), vacuousPhrase) && strings.Contains(reviewedText(request), `kind="ask"`)
			},
			Reply: modeltest.Reply{Text: standInReview(t, vacuousPhrase, map[string]any{
				"body": "Ship the reviewer?\nThe test of the held ask fails on its first call.",
				"options": []map[string]string{
					{"id": "", "label": "Ship it", "description": "Release it today, with the failing test."},
					{"id": "", "label": "Do not file", "description": "Wait until the test passes."},
				},
				"multiple": false,
			})},
		},
		modeltest.Rule{
			Match: func(request modeltest.Request) bool {
				return isReview(request) && strings.Contains(reviewedText(request), vacuousPhrase) && strings.Contains(reviewedText(request), `kind="issue"`)
			},
			Reply: modeltest.Reply{Text: standInReview(t, vacuousPhrase, map[string]any{"title": "Fix the held issue", "body": "The held issue's test fails."})},
		},
		modeltest.Rule{
			Match: func(request modeltest.Request) bool {
				return isReview(request) && strings.Contains(reviewedText(request), vacuousPhrase)
			},
			Reply: modeltest.Reply{Text: standInReview(t, vacuousPhrase, map[string]any{"body": "Status\nThe test fails on its first call."})},
		},
		modeltest.Rule{
			Match: func(request modeltest.Request) bool {
				return isReview(request) && strings.Contains(reviewedText(request), "store") &&
					strings.Contains(request.Text, "<reader_words>\n- store\n</reader_words>")
			},
			Reply: modeltest.Reply{Text: standInReview(t, "the store", map[string]any{"body": "Rows wait in the table until the sweep."})},
		},
		modeltest.Rule{Match: isReview, Reply: modeltest.Reply{Text: `{"findings":[]}`}},
	)
}

// fakeReviewListener is the Envoy listener the reviewer's tests run against: each session in live
// is running, any other is gone, and every send is kept.
type fakeReviewListener struct {
	mu    sync.Mutex
	live  map[string]bool
	sends []map[string]any
}

func newFakeReviewListener(t *testing.T, live ...string) (string, *fakeReviewListener) {
	t.Helper()
	listener := &fakeReviewListener{live: map[string]bool{}}
	for _, session := range live {
		listener.live[session] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/registry/", func(w http.ResponseWriter, r *http.Request) {
		session := strings.TrimPrefix(r.URL.Path, "/v1/registry/")
		listener.mu.Lock()
		running := listener.live[session]
		listener.mu.Unlock()
		if !running {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"session_id": session, "state": "live", "machine_id": "m1", "host": "m1",
			"capabilities": []string{"btw", "aside", "steer"}, "roles": []string{},
		})
	})
	mux.HandleFunc("/v1/messages/send", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		listener.mu.Lock()
		listener.sends = append(listener.sends, body)
		count := len(listener.sends)
		listener.mu.Unlock()
		WriteJSON(w, http.StatusOK, map[string]any{"event_id": "env-" + string(rune('a'+count)), "recipient": body["target_session"]})
	})
	mux.HandleFunc("/v1/sessions", func(w http.ResponseWriter, _ *http.Request) { WriteJSON(w, http.StatusOK, []any{}) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL, listener
}

func (l *fakeReviewListener) stop(session string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.live, session)
}

// frames are the findings frames the listener was sent: the delivered event of each send whose
// actor is Dispatch's reviewer.
func (l *fakeReviewListener) frames() []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var frames []map[string]any
	for _, send := range l.sends {
		payload, _ := send["payload"].(string)
		var frame struct {
			Event struct {
				Type    string         `json:"type"`
				Actor   model.Actor    `json:"actor"`
				Payload map[string]any `json:"payload"`
			} `json:"event"`
		}
		if json.Unmarshal([]byte(payload), &frame) != nil || frame.Event.Actor != reviewerActor {
			continue
		}
		frames = append(frames, map[string]any{
			"target": send["target_session"], "type": frame.Event.Type, "id": frame.Event.Payload["id"], "body": frame.Event.Payload["body"],
		})
	}
	return frames
}

// reviewRig is a server with the reviewer on, its stand-in model, its listener and an issue alice
// holds, whose reader asked about `store` before.
type reviewRig struct {
	t        *testing.T
	handler  http.Handler
	database *store.Store
	served   *server
	stand    *modeltest.Model
	listener *fakeReviewListener
	issue    string
}

// reviewerModel is a reviewer's model group at the stand-in's url.
func reviewerModel(url string) reviewer.Model {
	return reviewer.Model{
		Model: agentloop.Model{Endpoint: agentloop.Endpoint{
			Kind: agentloop.OpenAIChat, BaseURL: url,
			Token: func(context.Context) (string, error) { return "stand-in", nil },
		}, ID: "reviewer", Effort: "off"},
		Timeout: 20 * time.Second,
	}
}

// newReviewRig builds the rig with cfg, its model pointed at stand (reviewStandIn where nil).
func newReviewRig(t *testing.T, cfg ReviewerConfig, stand *modeltest.Model) *reviewRig {
	t.Helper()
	if stand == nil {
		stand = reviewStandIn(t)
	}
	cfg.Model = reviewerModel(stand.URL)
	listenerURL, listener := newFakeReviewListener(t, heldWriter)
	handler, database, _, served := newRegisteredTestServer(t, testServerOptions{
		reviewer: NewReviewer(cfg), envoyURL: listenerURL, envoyTimeout: 2 * time.Second,
	})
	rig := &reviewRig{t: t, handler: handler, database: database, served: served, stand: stand, listener: listener}
	rig.issue = createInteractionIssue(t, handler, "RVW", "Reviewer issue", "Seed paragraph here.\n").Key
	// alice asked what a store is before: her records hold the word.
	if _, err := database.Pool.Exec(context.Background(), `
		insert into word_questions (source, text_key, event_id, person, written_at, day, verdict, words, state, model)
		values ('event', 'event:9999001', 9999001, 'alice', now() - interval '1 day', current_date - 1,
			'asks_word_meaning', '{store}', 'done', 'stand-in')`); err != nil {
		t.Fatalf("record alice's question about a store: %v", err)
	}
	return rig
}

// asWriter writes as the agent session that writes the rig's texts.
func (rig *reviewRig) asWriter(method, target string, body map[string]any) *httptest.ResponseRecorder {
	rig.t.Helper()
	return rig.asSession(heldWriter, method, target, body)
}

func (rig *reviewRig) asSession(session, method, target string, body map[string]any) *httptest.ResponseRecorder {
	rig.t.Helper()
	withActor := map[string]any{"actor": map[string]any{"kind": "session", "id": session}}
	for key, value := range body {
		withActor[key] = value
	}
	return agentRequest(rig.t, rig.handler, method, target, withActor, "agent-token")
}

// heldAnswer is the part of a held write's answer the tests read.
type heldWriteAnswer struct {
	ID   string     `json:"id"`
	Key  string     `json:"key"`
	Held *heldState `json:"held"`
}

func (rig *reviewRig) held(response *httptest.ResponseRecorder, what string) heldWriteAnswer {
	rig.t.Helper()
	if response.Code != http.StatusCreated && response.Code != http.StatusAccepted {
		rig.t.Fatalf("%s: %d %s, want it held", what, response.Code, response.Body.String())
	}
	answer := decodeBody[heldWriteAnswer](rig.t, response)
	if answer.Held == nil {
		rig.t.Fatalf("%s: %s carries no held object", what, response.Body.String())
	}
	if answer.ID == "" {
		answer.ID = answer.Key
	}
	return answer
}

// heldRow reads the held text id as the reviewer keeps it.
func (rig *reviewRig) heldRow(id string) heldRow {
	rig.t.Helper()
	row, err := rig.served.loadHeld(context.Background(), rig.database.Pool, id, false)
	if err != nil {
		rig.t.Fatalf("read held text %s: %v", id, err)
	}
	return row
}

// waitHeld waits for the held text id to reach state.
func (rig *reviewRig) waitHeld(id, state string) heldRow {
	rig.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		row := rig.heldRow(id)
		if row.state == state {
			return row
		}
		if time.Now().After(deadline) {
			rig.t.Fatalf("held text %s is %s (outcome %q, detail %q), want %s", id, row.state, row.outcome, row.detail, state)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// outcomes are the reviewer's outcomes recorded for the rig's writes, in order.
func (rig *reviewRig) outcomes() []string {
	rig.t.Helper()
	rows, err := rig.database.Pool.Query(context.Background(), `
		select outcome from writing_check_events
		where outcome in ('held', 'passed', 'edited', 'posted_as_written', 'rewrite_posted', 'posted_unreviewed', 'reviewed_after')
		order by id`)
	if err != nil {
		rig.t.Fatal(err)
	}
	defer rows.Close()
	var outcomes []string
	for rows.Next() {
		var outcome string
		if err := rows.Scan(&outcome); err != nil {
			rig.t.Fatal(err)
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}

// eventOf is the reviewer's row of outcome: its detail, reader and project.
func (rig *reviewRig) eventOf(outcome string) (detail, reader, project string) {
	rig.t.Helper()
	if err := rig.database.Pool.QueryRow(context.Background(), `
		select coalesce(detail, ''), coalesce(reader, ''), coalesce(project, '')
		from writing_check_events where outcome = $1 order by id desc limit 1`, outcome).Scan(&detail, &reader, &project); err != nil {
		rig.t.Fatalf("read the %s row: %v", outcome, err)
	}
	return detail, reader, project
}

// commentIDs are the ids of the issue's comments as alice reads them.
func (rig *reviewRig) commentIDs() []string {
	rig.t.Helper()
	response := dispatchRequest(rig.t, rig.handler, http.MethodGet, "/api/v1/issues/"+rig.issue+"/comments", nil, "alice")
	if response.Code != http.StatusOK {
		rig.t.Fatalf("read the comments: %d %s", response.Code, response.Body.String())
	}
	var ids []string
	for _, comment := range decodeBody[[]model.Comment](rig.t, response) {
		ids = append(ids, comment.ID)
	}
	return ids
}

func (rig *reviewRig) commentBody(id string) string {
	rig.t.Helper()
	var body string
	if err := rig.database.Pool.QueryRow(context.Background(), `select body from comments where id = $1`, id).Scan(&body); err != nil {
		rig.t.Fatalf("read comment %s: %v", id, err)
	}
	return body
}

func holdComments() ReviewerConfig {
	return ReviewerConfig{Comments: ReviewHold, WriterWindow: time.Minute}
}

// An agent's comment on an issue a person holds, with the reviewer set to hold comments, answers
// at once with the comment's id and is held: the person's read of the issue's comments does not
// show it, the writer session gets the findings as a frame from Dispatch whose message is the held
// text, and the writer's post with a reason posts it, recorded posted_as_written with the reason.
func TestTheReviewerHoldsAnAgentsCommentUntilItsWriterPostsIt(t *testing.T) {
	rig := newReviewRig(t, holdComments(), nil)
	written := rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/comments", map[string]any{
		"body": "Status\nWe spin up the node. " + vacuousPhrase,
	})
	answer := rig.held(written, "the agent's comment")
	if answer.ID == "" || answer.Held.State != heldReviewing {
		t.Fatalf("the held comment answered %s, want its id and held reviewing", written.Body.String())
	}
	row := rig.waitHeld(answer.ID, heldAwaitingWriter)
	if row.deadline == nil || len(row.findings) != 1 || row.findings[0].Quote != vacuousPhrase {
		t.Fatalf("the held comment's review: deadline %v findings %+v", row.deadline, row.findings)
	}
	if slices.Contains(rig.commentIDs(), answer.ID) {
		t.Fatal("alice reads the held comment before it posts")
	}
	frames := rig.listener.frames()
	if len(frames) != 1 || frames[0]["target"] != heldWriter || frames[0]["type"] != "message.created" || frames[0]["id"] != answer.ID {
		t.Fatalf("the writer's frames = %+v, want one message.created naming the held comment", frames)
	}
	if body, _ := frames[0]["body"].(string); !strings.Contains(body, "dispatch held-post "+answer.ID) || !strings.Contains(body, vacuousPhrase) {
		t.Fatalf("the frame's body = %q, want the findings and the post command", frames[0]["body"])
	}
	if count := countRows(t, rig.database, `select count(*) from messages where id::text = $1`, answer.ID); count != 0 {
		t.Fatalf("the frame stored %d message rows, want none", count)
	}
	// A person reads no held text, and another session posts none.
	if response := dispatchRequest(t, rig.handler, http.MethodGet, "/api/v1/held/"+answer.ID, nil, "alice"); response.Code != http.StatusForbidden {
		t.Fatalf("alice's read of the held text: %d, want 403", response.Code)
	}
	if response := rig.asSession("s-other", http.MethodPost, "/api/v1/held/"+answer.ID+"/post", map[string]any{"reason": "mine"}); response.Code != http.StatusForbidden {
		t.Fatalf("another session's post: %d %s, want 403", response.Code, response.Body.String())
	}
	if response := rig.asWriter(http.MethodPost, "/api/v1/held/"+answer.ID+"/post", map[string]any{"reason": " "}); response.Code != http.StatusBadRequest {
		t.Fatalf("a post with no reason: %d, want 400", response.Code)
	}
	reason := "The reader asked for the status line as written."
	posted := rig.asWriter(http.MethodPost, "/api/v1/held/"+answer.ID+"/post", map[string]any{"reason": reason})
	if posted.Code != http.StatusOK || decodeBody[heldRead](t, posted).State != heldPosted {
		t.Fatalf("the writer's post: %d %s", posted.Code, posted.Body.String())
	}
	if !slices.Contains(rig.commentIDs(), answer.ID) {
		t.Fatal("alice does not read the comment once its writer posted it")
	}
	if body := rig.commentBody(answer.ID); !strings.Contains(body, vacuousPhrase) {
		t.Fatalf("the posted comment reads %q, want it as written", body)
	}
	if detail, reader, project := rig.eventOf(reviewPostedAsWritten); detail != reason || reader != "alice" || project != "RVW" {
		t.Fatalf("the posted_as_written row: detail %q reader %q project %q", detail, reader, project)
	}
	if outcomes := rig.outcomes(); !slices.Equal(outcomes, []string{reviewHeld, reviewPostedAsWritten}) {
		t.Fatalf("outcomes = %v", outcomes)
	}
}

// A second comment that uses `store` for a reader who asked about it is held, and its writer's
// edit through PATCH /api/v1/held/{id} is reviewed again and posts plainly, recorded passed.
func TestTheReviewerPostsAHeldCommentItsWriterEdited(t *testing.T) {
	rig := newReviewRig(t, holdComments(), nil)
	answer := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/comments", map[string]any{
		"body": "Rows wait in the store until the sweep.",
	}), "the comment that uses store")
	row := rig.waitHeld(answer.ID, heldAwaitingWriter)
	if len(row.findings) != 1 || row.findings[0].Quote != "the store" {
		t.Fatalf("findings = %+v, want the store", row.findings)
	}
	edited := rig.asWriter(http.MethodPatch, "/api/v1/held/"+answer.ID, map[string]any{"body": "Rows wait in the table until the sweep."})
	if edited.Code != http.StatusOK {
		t.Fatalf("the writer's edit: %d %s", edited.Code, edited.Body.String())
	}
	if response := rig.asWriter(http.MethodPatch, "/api/v1/held/"+answer.ID, map[string]any{"title": "No"}); response.Code != http.StatusBadRequest && response.Code != http.StatusConflict {
		t.Fatalf("an edit of a field a comment does not hold: %d, want 400 (or 409 once posted)", response.Code)
	}
	posted := rig.waitHeld(answer.ID, heldPosted)
	if posted.outcome != reviewPassed {
		t.Fatalf("the edited comment posted %s, want passed", posted.outcome)
	}
	if body := rig.commentBody(answer.ID); body != "Rows wait in the table until the sweep." {
		t.Fatalf("the posted comment reads %q, want the edit", body)
	}
	if outcomes := rig.outcomes(); !slices.Equal(outcomes, []string{reviewHeld, reviewEdited, reviewPassed}) {
		t.Fatalf("outcomes = %v", outcomes)
	}
}

// A held ask its writer leaves past the window posts the reviewer's rewrite, which keeps the
// option labels, their order and the first line.
func TestTheReviewerPostsTheRewriteOfAHeldAskLeftToTheWindow(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Asks: ReviewHold, WriterWindow: 300 * time.Millisecond, Unedited: UneditedRewrite}, nil)
	answer := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/asks", map[string]any{
		"question": "Ship the reviewer?\n" + vacuousPhrase,
		"options":  []map[string]string{{"label": "Ship it", "description": "Release it today."}, {"label": "Do not file", "description": "Wait."}},
	}), "the agent's ask")
	rig.waitHeld(answer.ID, heldAwaitingWriter)
	time.Sleep(400 * time.Millisecond)
	rig.served.sweepHeld(t.Context())
	posted := rig.waitHeld(answer.ID, heldPosted)
	if posted.outcome != reviewRewritePosted {
		t.Fatalf("the ask posted %s (%s), want rewrite_posted", posted.outcome, posted.detail)
	}
	var question string
	var options []model.AskOption
	if err := rig.database.Pool.QueryRow(context.Background(), `select question, options from asks where id = $1`, answer.ID).Scan(&question, &options); err != nil {
		t.Fatalf("read the posted ask: %v", err)
	}
	if first, _, _ := strings.Cut(question, "\n"); first != "Ship the reviewer?" || strings.Contains(question, vacuousPhrase) {
		t.Fatalf("the posted ask asks %q, want the rewrite with the first line kept", question)
	}
	if len(options) != 2 || options[0].Label != "Ship it" || options[1].Label != "Do not file" {
		t.Fatalf("the posted ask's options = %+v, want Ship it then Do not file", options)
	}
	if posted.original.Body != "Ship the reviewer?\n"+vacuousPhrase {
		t.Fatalf("the held text keeps %q as the original", posted.original.Body)
	}
}

// A writer session that has gone gets no time: the setting's choice posts at once.
func TestTheReviewerPostsAtOnceWhenTheWriterHasGone(t *testing.T) {
	for _, unedited := range []string{UneditedRewrite, UneditedAsWritten} {
		t.Run(unedited, func(t *testing.T) {
			rig := newReviewRig(t, ReviewerConfig{Comments: ReviewHold, WriterWindow: time.Hour, Unedited: unedited}, nil)
			rig.listener.stop(heldWriter)
			answer := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/comments", map[string]any{
				"body": "Status\nWe spin up the node. " + vacuousPhrase,
			}), "the gone writer's comment")
			posted := rig.waitHeld(answer.ID, heldPosted)
			want, body := reviewRewritePosted, "Status\nThe test fails on its first call."
			if unedited == UneditedAsWritten {
				want, body = reviewPostedAsWritten, "Status\nWe spin up the node. "+vacuousPhrase
			}
			if posted.outcome != want || rig.commentBody(answer.ID) != body {
				t.Fatalf("posted %s %q, want %s %q", posted.outcome, rig.commentBody(answer.ID), want, body)
			}
			if frames := rig.listener.frames(); len(frames) != 0 {
				t.Fatalf("a gone writer was sent %d frames", len(frames))
			}
		})
	}
}

// A model that fails posts the text as written, recorded posted_unreviewed.
func TestTheReviewerPostsAsWrittenWhenTheModelFails(t *testing.T) {
	failing := modeltest.New(t, modeltest.Rule{Reply: modeltest.Reply{Status: http.StatusInternalServerError, Text: "down"}})
	rig := newReviewRig(t, holdComments(), failing)
	answer := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/comments", map[string]any{
		"body": "Status\nWe spin up the node. " + vacuousPhrase,
	}), "the comment")
	posted := rig.waitHeld(answer.ID, heldPosted)
	if posted.outcome != reviewPostedUnreviewed || !strings.Contains(rig.commentBody(answer.ID), vacuousPhrase) {
		t.Fatalf("posted %s %q, want posted_unreviewed as written", posted.outcome, rig.commentBody(answer.ID))
	}
	if outcomes := rig.outcomes(); !slices.Equal(outcomes, []string{reviewHeld, reviewPostedUnreviewed}) {
		t.Fatalf("outcomes = %v", outcomes)
	}
}

// A second write naming a held issue as its parent succeeds at once and runs after the parent
// posts, as do a claim, an update and a document of the held issue.
func TestAWriteNamingAHeldIssueRunsAfterItPosts(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Issues: ReviewHold, WriterWindow: time.Minute}, nil)
	parent := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues", map[string]any{
		"project": "RVW", "title": "Held parent", "spec": vacuousPhrase,
	}), "the held issue")
	if !strings.HasPrefix(parent.ID, "RVW-") {
		t.Fatalf("the held issue answered key %q, want one of RVW's", parent.ID)
	}
	rig.waitHeld(parent.ID, heldAwaitingWriter)
	child := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues", map[string]any{
		"project": "RVW", "title": "Held child", "parent": parent.ID,
	}), "the child")
	if child.Held.WaitsOn != parent.ID {
		t.Fatalf("the child waits on %q, want %s", child.Held.WaitsOn, parent.ID)
	}
	claim := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues/"+parent.ID+"/claim", map[string]any{}), "the claim")
	update := rig.held(rig.asWriter(http.MethodPatch, "/api/v1/issues/"+parent.ID, map[string]any{"labels": []string{"held"}}), "the update")
	document := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues/"+parent.ID+"/artifacts", map[string]any{
		"name": "notes", "content": "The notes.\n",
	}), "the document")
	for _, waiting := range []heldWriteAnswer{claim, update, document} {
		if waiting.Held.WaitsOn != parent.ID {
			t.Fatalf("a write of the held issue waits on %q", waiting.Held.WaitsOn)
		}
	}
	if count := countRows(t, rig.database, `select count(*) from issues where key = $1 or parent_key = $1`, parent.ID); count != 0 {
		t.Fatalf("%d issues exist before the parent posts", count)
	}
	if response := rig.asWriter(http.MethodPost, "/api/v1/held/"+parent.ID+"/post", map[string]any{"reason": "As written."}); response.Code != http.StatusOK {
		t.Fatalf("the parent's post: %d %s", response.Code, response.Body.String())
	}
	childPosted := rig.waitHeld(child.ID, heldPosted)
	rig.waitHeld(claim.ID, heldPosted)
	rig.waitHeld(update.ID, heldPosted)
	rig.waitHeld(document.ID, heldPosted)
	parentPosted := rig.heldRow(parent.ID)
	if childPosted.postedAt.Before(*parentPosted.postedAt) {
		t.Fatalf("the child posted at %v, before its parent at %v", childPosted.postedAt, parentPosted.postedAt)
	}
	var childParent string
	if err := rig.database.Pool.QueryRow(context.Background(), `select parent_key from issues where key = $1`, child.ID).Scan(&childParent); err != nil || childParent != parent.ID {
		t.Fatalf("the child's parent is %q (%v), want %s", childParent, err, parent.ID)
	}
	issue := decodeBody[model.Issue](t, dispatchRequest(t, rig.handler, http.MethodGet, "/api/v1/issues/"+parent.ID, nil, "alice"))
	if issue.Claim == nil || issue.Claim.Actor.ID != heldWriter || !slices.Contains(issue.Labels, "held") {
		t.Fatalf("the posted issue: claim %+v labels %v, want claimed by the writer and labelled", issue.Claim, issue.Labels)
	}
	if count := countRows(t, rig.database, `select count(*) from artifacts where issue_key = $1 and slug = 'notes'`, parent.ID); count != 1 {
		t.Fatalf("the held issue's document: %d rows", count)
	}
}

// The draft route answers the reviewer's findings, its rewrite and each call's deployment, and
// writes nothing.
func TestTheReviewerDraftRouteAnswersAndWritesNothing(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{}, nil)
	counts := func() int64 {
		return countRows(t, rig.database, `select (select count(*) from held_texts) + (select count(*) from writing_check_events) + (select count(*) from comments)`)
	}
	before := counts()
	response := dispatchRequest(t, rig.handler, http.MethodPost, "/api/v1/reviewer/draft", map[string]any{
		"kind": "comment", "text": "Status\n" + vacuousPhrase, "issue": rig.issue,
	}, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("the draft route: %d %s", response.Code, response.Body.String())
	}
	answer := decodeBody[reviewerDraftAnswer](t, response)
	if answer.Reader != "alice" || len(answer.Findings) != 1 || answer.Rewrite == nil || len(answer.Calls) != 1 ||
		answer.Calls[0].Deployment != "reviewer" || answer.Calls[0].Served == "" {
		t.Fatalf("the draft's answer = %+v", answer)
	}
	if after := counts(); after != before {
		t.Fatalf("the draft route wrote %d rows", after-before)
	}
	off := newRegisteredTestHandlerWithoutReviewer(t)
	if response := dispatchRequest(t, off, http.MethodPost, "/api/v1/reviewer/draft", map[string]any{"kind": "comment", "text": "x"}, "alice"); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("the draft route with no reviewer's model: %d, want 503", response.Code)
	}
}

func newRegisteredTestHandlerWithoutReviewer(t *testing.T) http.Handler {
	t.Helper()
	handler, _, _ := newTestServer(t, testServerOptions{})
	return handler
}

// With the reviewer off, an agent's comment posts at once: the negative control.
func TestAnAgentsCommentPostsAtOnceWithTheReviewerOff(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Comments: ReviewOff, Asks: ReviewHold}, nil)
	response := rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/comments", map[string]any{"body": "Status\n" + vacuousPhrase})
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), `"held"`) {
		t.Fatalf("the comment with the reviewer off: %d %s, want posted", response.Code, response.Body.String())
	}
	if id := decodeBody[model.Comment](t, response).ID; !slices.Contains(rig.commentIDs(), id) {
		t.Fatal("alice does not read the comment the reviewer did not hold")
	}
	if count := countRows(t, rig.database, `select count(*) from held_texts`); count != 0 {
		t.Fatalf("%d held texts", count)
	}
}

// A person's own comment and the Legion daemon's post at once, with the reviewer holding comments.
func TestThePersonsAndTheDaemonsWritesPostAtOnce(t *testing.T) {
	rig := newReviewRig(t, holdComments(), nil)
	for name, response := range map[string]*httptest.ResponseRecorder{
		"the person's": dispatchRequest(t, rig.handler, http.MethodPost, "/api/v1/issues/"+rig.issue+"/comments", map[string]any{"body": vacuousPhrase}, "alice"),
		"the daemon's": rig.asSession(reviewDaemon, http.MethodPost, "/api/v1/issues/"+rig.issue+"/comments", map[string]any{"body": vacuousPhrase}),
	} {
		if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), `"held"`) {
			t.Errorf("%s comment: %d %s, want it posted at once", name, response.Code, response.Body.String())
			continue
		}
		if id := decodeBody[model.Comment](t, response).ID; !slices.Contains(rig.commentIDs(), id) {
			t.Errorf("alice does not read %s comment", name)
		}
	}
	if count := countRows(t, rig.database, `select count(*) from held_texts`); count != 0 {
		t.Fatalf("%d held texts", count)
	}
}

// A held text's write is checked as the write is checked with the reviewer off: a comment whose
// turn its create path refuses is refused at once, not held.
func TestAHeldWriteIsRefusedAtOnceForWhatItsPathRefuses(t *testing.T) {
	rig := newReviewRig(t, holdComments(), nil)
	response := rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/comments", map[string]any{
		"body": "Status\n" + vacuousPhrase, "turn": "agent",
	})
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "TURN_REQUIRES_ASK") {
		t.Fatalf("a held comment with a turn on no ask: %d %s, want 400 TURN_REQUIRES_ASK", response.Code, response.Body.String())
	}
	if count := countRows(t, rig.database, `select count(*) from held_texts`); count != 0 {
		t.Fatalf("%d held texts", count)
	}
}

// An agent's reply to a person's message that a plugin posts is held, and posts through the reply
// route under the id the hold reserved; one marked as generated posts at once.
func TestTheReviewerHoldsAnAgentsReplyToAPersonsMessage(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Messages: ReviewHold, WriterWindow: time.Minute}, nil)
	send := dispatchRequest(t, rig.handler, http.MethodPost, "/api/v1/agents/"+heldWriter+"/messages", map[string]any{"body": "Is it done?", "delivery": "btw"}, "alice")
	if send.Code != http.StatusCreated {
		t.Fatalf("alice's message: %d %s", send.Code, send.Body.String())
	}
	message := decodeBody[model.Message](t, send).ID
	reply := rig.held(rig.asWriter(http.MethodPost, "/api/v1/messages/"+message+"/reply", map[string]any{
		"attempt": 1, "body": "Status\n" + vacuousPhrase,
	}), "the agent's reply")
	rig.waitHeld(reply.ID, heldAwaitingWriter)
	if count := countRows(t, rig.database, `select count(*) from messages where in_reply_to = $1`, message); count != 0 {
		t.Fatalf("%d replies exist while the reply is held", count)
	}
	if response := rig.asWriter(http.MethodPost, "/api/v1/held/"+reply.ID+"/post", map[string]any{"reason": "As written."}); response.Code != http.StatusOK {
		t.Fatalf("the writer's post: %d %s", response.Code, response.Body.String())
	}
	rig.waitHeld(reply.ID, heldPosted)
	var posted string
	if err := rig.database.Pool.QueryRow(context.Background(), `select id::text from messages where in_reply_to = $1`, message).Scan(&posted); err != nil || posted != reply.ID {
		t.Fatalf("the posted reply is %q (%v), want the reserved id %s", posted, err, reply.ID)
	}
}
