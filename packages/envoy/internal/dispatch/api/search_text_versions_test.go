package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/sessionanswers"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/dispatch/textversions"
)

// TestTheKeywordTableHoldsEachVersionOfEachItem writes, through the API, every kind of item the
// keyword table holds and every change that starts a version of one: an issue with a primary
// document, a second version of the document and a title change; a comment, a reply through the
// delivery callback and the comment's edit; an issue message and a reply to it; an ask, its edit,
// its answer and its answer restored after its block left the document; a comment and an ask on a
// project document; a comment longer than an event carries whole, then edited, and a message that
// long. Three changes start none: an issue.updated that changes only the status, the comment's
// resolve and a message with no issue, which the table leaves out. Between each two changes the
// test keeps a database time at which nothing else is written, and every item's search column and
// text as they stand then (keywordHistory.keep).
//
// One more comment carries the reversed pair: transaction A begins, B edits the comment through
// the API and commits, then A writes the comment's body and an event of its own and commits, so
// A's event has the later seq and the earlier created_at. One kept time falls inside A's window,
// after B's commit, and one after A's commit.
//
// Then the fill writes the table (textversions.FillToHorizon), and at every kept time the relation
// store.TextVersionsAt hands out gives each item that existed one version: its vector is the kept
// column, its body the kept text and none of the text written after it, an issue's names the title
// and the primary document version of that time, and its issue, project, event and seq are its
// event's. Inside A's window the reversed pair's comment has A's version, and after A's commit too.
// No version is the message with no issue's, the long comment's first version and the long
// message's hold their whole bodies, read from the comment's first blocks and from the message's
// row, and at the end every item's version is its last, equal to its present column.
func TestTheKeywordTableHoldsEachVersionOfEachItem(t *testing.T) {
	live := true
	sent := []map[string]any{}
	listener := sessionListener(t, &live, &sent)
	defer listener.Close()
	handler, database, deps := newTestServer(t, testServerOptions{envoyURL: listener.URL})
	ctx := t.Context()
	history := &keywordHistory{t: t, database: database}

	// An issue with its primary document, then the changes of the issue's text: a second version
	// of the document and a title, and an issue.updated that changes only the status.
	issue := createInteractionIssue(t, handler, "TEST", "Rotate the signing keys",
		"# Rotation\n\nThe verifiers read the old key until Monday.\n")
	history.keep("the issue is created with its primary document")
	agentEdit(t, handler, issue.PrimaryArtifactID, 2, replaceOp("until Monday.", "until Tuesday, when the auditors sign."))
	history.keep("a second version of the issue's primary document")
	requireKeywordAnswer(t, "retitle the issue", dispatchRequest(t, handler, http.MethodPatch, "/api/v1/issues/"+issue.Key,
		map[string]any{"title": "Rotate the signing keys before the audit"}, "alice"), http.StatusOK)
	history.keep("the issue's title changes")
	requireKeywordAnswer(t, "move the issue's status", dispatchRequest(t, handler, http.MethodPatch, "/api/v1/issues/"+issue.Key,
		map[string]any{"status": "in_progress"}, "alice"), http.StatusOK)
	unchanging := []int64{keywordLatestEvent(t, database, "issue.updated", issue.Key)}
	history.keep("an issue.updated changes only the issue's status")

	// A comment; a comment that mentions a session, and the session's reply through the delivery
	// callback (comment.answered); the first comment's edit; and its resolve, which changes no text,
	// between two kept times.
	comment := createThreadComment(t, handler, issue.Key, map[string]any{"body": "The verifiers read the old key until Monday."}, "alice")
	history.keep("a comment")
	mentioned := decodeMentionedComment(t, postMentionedComment(t, handler, issue.Key, map[string]any{
		"body": "Can the rotation ship tonight?", "mentions": []map[string]any{{"target": "session:s1"}},
	}))
	settledCommentDeliveries(t, database, mentioned.ID)
	history.keep("a comment that mentions a session")
	callbackReply := decodeBody[model.Comment](t, requireKeywordAnswer(t, "reply through the delivery callback",
		bearerRequest(t, handler, http.MethodPost, "/api/v1/comments/"+mentioned.ID+"/reply", map[string]any{
			"actor": map[string]any{"kind": "session", "id": "s1"}, "attempt": 1, "body": "It ships at nine, the canary first.",
		}), http.StatusCreated))
	history.keep("the session's reply through the delivery callback")
	requireEdit(t, "edit the comment", editCommentAs(t, handler, comment.ID, "The verifiers read the old key until Wednesday.", nil, nil))
	history.keep("the comment's edit")
	requireKeywordAnswer(t, "resolve the comment", dispatchRequest(t, handler, http.MethodPost, "/api/v1/comments/"+comment.ID+"/resolve",
		map[string]any{}, "alice"), http.StatusOK)
	unchanging = append(unchanging, keywordLatestEvent(t, database, "comment.resolved", issue.Key))
	history.keep("the comment is resolved")

	// An issue message and a reply to it (message.answered).
	message := createIssueMessage(t, handler, issue.Key, map[string]any{"body": "Can the rotation wait for the freeze to end?"}, "alice")
	history.keep("an issue message")
	messageReply := createIssueMessage(t, handler, issue.Key, map[string]any{
		"body": "It can; the freeze ends Friday.", "in_reply_to": message.ID,
	}, "bob")
	history.keep("a reply to the issue message")

	// An ask, a decision block of the primary document; its edit; its answer; and its answer
	// restored: the answered block leaves the document, the ask's row holds settlement's retraction
	// as a build before the present one wrote it (TestListMyAnswersCollapsesRestoredAnswerEvents),
	// and the block comes back, which writes the answer back with an ask.answered marked restored.
	requireKeywordAnswer(t, "write an ask block into the primary document", sessionRequest(t, handler, http.MethodPost,
		"/api/v1/artifacts/"+issue.PrimaryArtifactID+"/edits", map[string]any{
			"actor": sessionActor(), "ops": []map[string]string{{"op": "insert", "after": "end", "markdown": transportAsk}},
		}), http.StatusOK)
	awaitIndexedAskBlock(t, handler, issue.PrimaryArtifactID, "decision", "Which transport?")
	askID := blockAskID(t, handler, issue.Key, "decision")
	history.keep("an ask")
	requireKeywordAnswer(t, "edit the ask", sessionRequest(t, handler, http.MethodPatch, "/api/v1/asks/"+askID, map[string]any{
		"question": "Which transport ships first?", "actor": sessionActor(),
	}), http.StatusOK)
	history.keep("the ask's edit")
	requireKeywordAnswer(t, "answer the ask", dispatchRequest(t, handler, http.MethodPost, "/api/v1/asks/"+askID+"/answer", map[string]any{
		"selected": []string{"REST"}, "text": "REST first; gRPC after the audit.", "expected_edited_at": readBlockAsk(t, handler, askID).EditedAt,
	}, "alice"), http.StatusOK)
	history.keep("the ask's answer")
	requireKeywordAnswer(t, "delete the answered ask's block", dispatchRequest(t, handler, http.MethodPost,
		"/api/v1/artifacts/"+issue.PrimaryArtifactID+"/edits", map[string]any{
			"ops": []map[string]string{{"op": "delete", "block": "decision"}},
		}, "bob"), http.StatusOK)
	settleDocument(t, handler, issue.PrimaryArtifactID, issue.Key, "after-delete")
	history.keep("the answered ask's block leaves the document")
	// The retraction and the restore are one step: the retraction an older build wrote changed
	// the row's answer with an event this test does not write, so no time is kept between them.
	if _, err := database.Pool.Exec(ctx, `
		update asks
		   set state = 'resolved', answer = null,
		       resolution = jsonb_build_object(
		         'kind', 'retracted',
		         'reason', 'removed from the document in version 9',
		         'actor', jsonb_build_object('kind', 'system', 'id', 'document-settlement'),
		         'at', now())
		 where id = $1`, askID); err != nil {
		t.Fatalf("write the retraction an older build's settlement wrote: %v", err)
	}
	requireKeywordAnswer(t, "write the answered ask's block back", dispatchRequest(t, handler, http.MethodPost,
		"/api/v1/artifacts/"+issue.PrimaryArtifactID+"/edits", map[string]any{
			"ops": []map[string]string{{"op": "insert", "after": "end", "markdown": strings.Replace(transportAsk,
				"Which transport?", "Which transport ships first?", 1)}},
		}, "bob"), http.StatusOK)
	settleDocument(t, handler, issue.PrimaryArtifactID, issue.Key, "after-restore")
	var restored int
	if err := database.Pool.QueryRow(ctx, `
		select count(*) from events
		 where type = 'ask.answered' and payload->>'id' = $1 and payload->>'restored' = 'true'`, askID).Scan(&restored); err != nil {
		t.Fatalf("count the ask's restored answers: %v", err)
	}
	if restored != 1 {
		t.Fatalf("the ask's block came back with %d restored ask.answered events, want one", restored)
	}
	history.keep("the ask's answer is restored")

	// A comment and an ask on a project document, which belong to no issue.
	document := createProjectDocument(t, handler, "TEST", "Runbook", "# Runbook\n\nPage the on-call engineer first.\n")
	documentComment := decodeBody[model.Comment](t, requireKeywordAnswer(t, "comment on the project document",
		dispatchRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+document.ID+"/comments",
			map[string]any{"body": "Name the pager rotation here."}, "alice"), http.StatusCreated))
	history.keep("a comment on a project document")
	documentAsk := decodeBody[model.Ask](t, requireKeywordAnswer(t, "ask on the project document",
		sessionRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+document.ID+"/asks",
			map[string]any{"question": "Who owns the pager rotation?", "actor": sessionActor()}), http.StatusCreated))
	history.keep("an ask on a project document")
	if documentComment.IssueKey != nil || documentAsk.IssueKey != nil {
		t.Fatalf("the project document's comment and ask name issues %v and %v, want none", documentComment.IssueKey, documentAsk.IssueKey)
	}

	// A message with no issue, a session's direct message, which the table leaves out.
	direct := decodeBody[model.Message](t, requireKeywordAnswer(t, "message the session with no issue",
		dispatchRequest(t, handler, http.MethodPost, "/api/v1/agents/s1/messages",
			map[string]any{"body": "The pager rotation moves to Thursday.", "delivery": "btw"}, "alice"), http.StatusCreated))
	if direct.IssueKey != nil {
		t.Fatalf("the direct message names issue %s, want none", *direct.IssueKey)
	}
	history.keep("a message with no issue")

	// A comment longer than an event carries whole, then edited, and an issue message that long.
	longBody := keywordLongBody("verifiers")
	longComment := createThreadComment(t, handler, issue.Key, map[string]any{"body": longBody}, "alice")
	history.keep("a comment longer than an event carries whole")
	requireEdit(t, "edit the long comment", editCommentAs(t, handler, longComment.ID, longBody+" The kestrel signs it off.", nil, nil))
	history.keep("the long comment's edit")
	longMessage := createIssueMessage(t, handler, issue.Key, map[string]any{"body": keywordLongBody("auditors")}, "alice")
	history.keep("an issue message longer than an event carries whole")

	// The reversed pair.
	pair := createThreadComment(t, handler, issue.Key, map[string]any{"body": "The canary takes the first region."}, "alice")
	history.keep("the comment the reversed pair edits")
	a, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction A: %v", err)
	}
	defer func() { _ = a.Rollback(context.Background()) }()
	var aStarted time.Time
	var aBackend int32
	if err := a.QueryRow(ctx, `select now(), pg_backend_pid()`).Scan(&aStarted, &aBackend); err != nil {
		t.Fatalf("begin transaction A: %v", err)
	}
	edited := decodeBody[model.Comment](t, requireKeywordAnswer(t, "B edits the comment",
		editCommentAs(t, handler, pair.ID, "The canary takes the first two regions.", nil, nil), http.StatusOK))
	history.keepInside("inside A's window, after B's commit", aBackend)
	const aBody = "The canary takes every region at once."
	if _, err := a.Exec(ctx, `update comments set body = $2, edited_at = now() where id = $1::uuid`, pair.ID, aBody); err != nil {
		t.Fatalf("A writes the comment's body: %v", err)
	}
	payload := model.CommentEventPayload{Comment: edited}
	payload.Body = aBody
	editedAt := aStarted.UTC().Format(time.RFC3339Nano)
	payload.EditedAt = &editedAt
	issueKey := issue.Key
	aEvent, err := deps.Events.Append(ctx, a, model.Event{
		IssueKey: &issueKey, Type: "comment.edited", Actor: model.Actor{Kind: "user", ID: "alice"}, Payload: payload,
	})
	if err != nil {
		t.Fatalf("A appends its comment.edited: %v", err)
	}
	if err := a.Commit(ctx); err != nil {
		t.Fatalf("commit transaction A: %v", err)
	}
	history.keep("after A's commit")
	requireReversedPair(t, database, pair.ID, aEvent.ID)

	if err := textversions.FillToHorizon(ctx, database, events.NewHorizon(database.Pool)); err != nil {
		t.Fatalf("fill the keyword table: %v", err)
	}
	history.keep("the end, after the fill")
	history.reversed = keywordItem{"comment", pair.ID}
	history.reversedEvent = aEvent.ID
	history.verify()
	if t.Failed() {
		return
	}

	// The replies have versions of their own, started by their own events.
	history.requireVersion("the session's reply through the delivery callback", keywordItem{"comment", callbackReply.ID},
		func(v keywordVersion) bool { return v.eventType == "comment.answered" }, "started by its comment.answered")
	history.requireVersion("a reply to the issue message", keywordItem{"message", messageReply.ID},
		func(v keywordVersion) bool { return v.eventType == "message.answered" }, "started by its message.answered")
	// The long comment's first version and the long message's hold their whole bodies, which
	// their events cut: the comment's read from the blocks of its first revision, since its row
	// has been edited since, and the message's from its row. The edit's version reads the row.
	history.requireVersion("a comment longer than an event carries whole", keywordItem{"comment", longComment.ID},
		func(v keywordVersion) bool { return v.truncated && v.bodyFrom == "blocks" }, "whole from its first blocks, its event's body cut")
	history.requireVersion("the long comment's edit", keywordItem{"comment", longComment.ID},
		func(v keywordVersion) bool { return v.truncated && v.bodyFrom == "row" }, "whole from its row, its event's body cut")
	history.requireVersion("an issue message longer than an event carries whole", keywordItem{"message", longMessage.ID},
		func(v keywordVersion) bool { return v.truncated && v.bodyFrom == "row" }, "whole from its row, its event's body cut")
	// The changes that change no text start no version.
	if rows := keywordRowsOfEvents(t, database, unchanging); rows != 0 {
		t.Errorf("the status-only issue.updated and the comment.resolved (events %v) have %d rows, want none", unchanging, rows)
	}
}

// TestTheKeywordFillWritesWhileTheServerAnswers runs the fill as a serving process runs it
// (textversions.Run, its server listening) beside the handler of a server whose log holds 2,000
// earlier events, and once the fill has begun catching them up, runs rounds of requests that write
// issues, comments, asks and messages and read them, until the fill has caught up. A second server,
// with the same log and no fill, answers the same rounds: each request answers alike, status and
// body, but for the ids and times each server draws. At the end every event has its rows: each
// earlier event the rows it starts, and the table is the one a fill run on the quiet database from
// no row writes.
func TestTheKeywordFillWritesWhileTheServerAnswers(t *testing.T) {
	filled, quiet := newKeywordServer(t), newKeywordServer(t)
	backlog := seedKeywordBacklog(t, filled.database, filled.deps.Events, 2000)
	seedKeywordBacklog(t, quiet.database, quiet.deps.Events, 2000)
	last := backlog[len(backlog)-1].id

	fill := startKeywordFill(t, filled)
	fill.awaitBatches(t, 1, time.Minute)
	if cursor := keywordCursor(t, filled.database); cursor >= last {
		t.Fatalf("the fill's first batch took the cursor to %d, the backlog's last event %d: nothing is left to catch up beside the requests", cursor, last)
	}
	before := len(fill.lines(t, keywordBatchLine))
	var rounds [][]keywordAnswer
	for round := 1; keywordCursor(t, filled.database) < last; round++ {
		if round > keywordMaxRounds {
			t.Fatalf("the fill had not caught up the backlog after %d rounds of requests: its cursor is %d of %d",
				keywordMaxRounds, keywordCursor(t, filled.database), last)
		}
		rounds = append(rounds, keywordRound(t, filled, round, fill.wake))
	}
	if during := len(fill.lines(t, keywordBatchLine)) - before; during < 2 {
		t.Fatalf("the fill wrote %d batches while %d rounds of requests ran, want at least two", during, len(rounds))
	}

	for index, filledAnswers := range rounds {
		quietAnswers := keywordRound(t, quiet, index+1, func() {})
		for n := range max(len(filledAnswers), len(quietAnswers)) {
			if n >= len(filledAnswers) || n >= len(quietAnswers) {
				t.Errorf("round %d: the server with the fill answered %d requests, the one without %d", index+1, len(filledAnswers), len(quietAnswers))
				break
			}
			got, want := filledAnswers[n], quietAnswers[n]
			if got != want {
				at := keywordFirstDifference(got.body, want.body)
				t.Errorf("round %d, %s: with the fill it answered %d, from byte %d of its body %.300s\nwith no fill %s answered %d, from byte %d %.300s",
					index+1, got.request, got.status, at, got.body[at:], want.request, want.status, at, want.body[at:])
			}
		}
	}

	// Every event has its rows.
	newest := keywordNewestEvent(t, filled.database)
	fill.wake()
	keywordAwaitCursor(t, filled.database, newest, time.Minute, fill.wake)
	fill.stop(t)
	requireKeywordSeededRows(t, filled.database, backlog)
	written := keywordTableRows(t, filled.database, newest)
	if _, err := filled.database.Pool.Exec(t.Context(), `delete from search_text_versions`); err != nil {
		t.Fatalf("empty the keyword table: %v", err)
	}
	if _, err := filled.database.Pool.Exec(t.Context(), `delete from search_text_versions_fill`); err != nil {
		t.Fatalf("empty the keyword fill's cursor: %v", err)
	}
	if err := textversions.FillToHorizon(t.Context(), filled.database, events.NewHorizon(filled.database.Pool)); err != nil {
		t.Fatalf("fill the keyword table again on the quiet database: %v", err)
	}
	again := keywordTableRows(t, filled.database, newest)
	if len(written) != len(again) {
		t.Errorf("the fill beside the requests wrote %d rows for events up to %d; a fill from no row writes %d", len(written), newest, len(again))
	}
	for index := range min(len(written), len(again)) {
		if written[index] != again[index] {
			t.Fatalf("row %d: the fill beside the requests wrote\n%.600s\na fill from no row writes\n%.600s", index, written[index], again[index])
		}
	}
}

// TestTheKeywordFillPausesWhileTheAnswersReadIsSlow runs the fill as a serving process runs it,
// on the server's own sessionanswers.Latency, while it catches up a backlog of 4,000 events. A
// transaction locks message_deliveries, which the answers read reads, so an answers read through
// the handler runs past 800 ms: within one batch the cursor stops, and the fill says it paused.
// For the 20 s the lock then holds, while events are written, the cursor does not move. Once the
// lock is released, reads answer within 800 ms, and for the 10 s after the slow read ended the
// cursor still stands; then the fill says it continues and the cursor moves, and every event,
// those written under the lock included, gets its rows.
func TestTheKeywordFillPausesWhileTheAnswersReadIsSlow(t *testing.T) {
	server := newKeywordServer(t)
	backlog := seedKeywordBacklog(t, server.database, server.deps.Events, 4000)
	last := backlog[len(backlog)-1].id
	issue := createInteractionIssue(t, server.handler, "TEST", "Rotate the signing keys", "# Rotation\n")
	fill := startKeywordFill(t, server)
	fill.awaitBatches(t, 1, time.Minute)
	if cursor := keywordCursor(t, server.database); cursor >= last {
		t.Fatalf("the fill's first batch took the cursor to %d, the backlog's last event %d: it is not catching up as the lock starts", cursor, last)
	}

	holder, err := server.database.Pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin the transaction that holds the answers read: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(t.Context(), `lock table message_deliveries in access exclusive mode`); err != nil {
		t.Fatalf("lock message_deliveries: %v", err)
	}
	const answers = "/api/v1/agents/" + answersSession + "/answers"
	held := make(chan *httptest.ResponseRecorder, 1)
	go func() { held <- dispatchRequest(t, server.handler, http.MethodGet, answers, nil, "alice") }()
	waitForDatabaseLocks(t, holder, 1)
	slowDeadline := time.Now().Add(5 * time.Second)
	for !server.latency.Slow(time.Now()) {
		if time.Now().After(slowDeadline) {
			t.Fatal("the answers read held by the lock was no slow read 5 s after it began")
		}
		time.Sleep(10 * time.Millisecond)
	}
	batchesWhenSlow := len(fill.lines(t, keywordBatchLine))
	fill.awaitLine(t, keywordPausedLine, 30*time.Second)
	var batchesBeforePause int
	for _, line := range fill.lines(t, "") {
		if strings.HasPrefix(line.Msg, keywordPausedLine) {
			break
		}
		if strings.HasPrefix(line.Msg, keywordBatchLine) {
			batchesBeforePause++
		}
	}
	if after := batchesBeforePause - batchesWhenSlow; after > 1 {
		t.Fatalf("the fill wrote %d batches after the answers read turned slow and before it paused, want at most the one in flight", after)
	}
	paused := keywordCursor(t, server.database)
	if paused >= last {
		t.Fatalf("the fill paused with its cursor at %d, the backlog's last event %d: it was not catching up", paused, last)
	}

	// The lock holds 20 s while events are written, and the cursor stands.
	writes := 0
	releaseAt := time.Now().Add(20 * time.Second)
	for time.Now().Before(releaseAt) {
		writes++
		keywordWriteUnderLock(t, server, issue.Key, writes)
		fill.wake()
		for sampled := time.Now(); time.Since(sampled) < 2*time.Second && time.Now().Before(releaseAt); {
			if cursor := keywordCursor(t, server.database); cursor != paused {
				t.Fatalf("the cursor moved from %d to %d while the answers read was held by the lock", paused, cursor)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	if batches := len(fill.lines(t, keywordBatchLine)); batches != batchesBeforePause {
		t.Fatalf("the fill logged %d batches while paused, want none", batches-batchesBeforePause)
	}
	if err := holder.Rollback(t.Context()); err != nil {
		t.Fatalf("release message_deliveries: %v", err)
	}
	released := time.Now()
	select {
	case response := <-held:
		if response.Code != http.StatusOK {
			t.Fatalf("the held answers read answered %d: %s", response.Code, response.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the held answers read did not answer within 10 s of the lock's release")
	}

	// For the 10 s after the slow read ended, reads answer within 800 ms, and the cursor stands:
	// the slow read ended after the release, so nothing may move before released + 10 s.
	for time.Since(released) < sessionanswers.SlowWithin+time.Second {
		began := time.Now()
		if response := dispatchRequest(t, server.handler, http.MethodGet, answers, nil, "alice"); response.Code != http.StatusOK {
			t.Fatalf("an answers read after the release answered %d: %s", response.Code, response.Body.String())
		}
		if took := time.Since(began); took > sessionanswers.SlowRead {
			t.Fatalf("an answers read after the release took %v, over %v", took, sessionanswers.SlowRead)
		}
		if sampled := time.Now(); sampled.Before(released.Add(sessionanswers.SlowWithin)) {
			if cursor := keywordCursor(t, server.database); cursor != paused {
				t.Fatalf("the cursor moved from %d to %d %v after the release, inside the %v after the slow read ended",
					paused, cursor, sampled.Sub(released), sessionanswers.SlowWithin)
			}
		}
		time.Sleep(time.Until(began.Add(time.Second)))
	}

	// Then the fill continues: the cursor moves, and every event gets its rows.
	fill.awaitLine(t, keywordContinuesLine, 20*time.Second)
	moveDeadline := time.Now().Add(20 * time.Second)
	for keywordCursor(t, server.database) == paused {
		if time.Now().After(moveDeadline) {
			t.Fatalf("the cursor stood at %d 20 s after the fill said it continues", paused)
		}
		time.Sleep(100 * time.Millisecond)
	}
	keywordAwaitCursor(t, server.database, keywordNewestEvent(t, server.database), 2*time.Minute, fill.wake)
	requireKeywordSeededRows(t, server.database, backlog)
	var underLock int
	if err := server.database.Pool.QueryRow(t.Context(), `
		select count(distinct e.id) from events e join search_text_versions v on v.event_id = e.id
		 where e.type = 'comment.created' and e.issue_key = $1`, issue.Key).Scan(&underLock); err != nil {
		t.Fatalf("count the rows of the comments written under the lock: %v", err)
	}
	if underLock != writes {
		t.Fatalf("%d of the %d comments written under the lock have rows", underLock, writes)
	}
}

// keywordItem is one item of the keyword table: its kind and its id, an issue's key or a post's or
// an ask's uuid.
type keywordItem struct{ kind, id string }

// keptItem is an item as a kept time read it: the text its search column is made from (an issue's
// title, a post's body, or an ask's question, a space and ask_search_suffix of its options and
// answer), the column as text, its issue, and for an issue its primary document and that
// document's latest version.
type keptItem struct {
	text, search string
	issueKey     *string
	spec         *string
	specVersion  *int32
}

// keptTime is a database time at which nothing else was written, and every item as it stood then.
type keptTime struct {
	step  string
	at    time.Time
	items map[keywordItem]keptItem
	// inside is the kept time inside the reversed pair's transaction A, which was open then.
	inside bool
}

// keywordVersion is one row the relation answered at a kept time, with the columns of the event
// that started it.
type keywordVersion struct {
	issueKey     *string
	projectKey   string
	eventID      int64
	eventSeq     int32
	startedAt    time.Time
	body         string
	bodyFrom     string
	specArtifact *string
	specVersion  *int32
	search       string
	// The event's: its type, seq, created_at, issue and project as the log names them, the item it
	// is of (an issue event's issue, else its payload's id), and whether it cut the post's body.
	// eventType is "" for an event the log does not hold.
	eventType    string
	eventSeqOf   int32
	eventAt      time.Time
	eventIssue   *string
	eventProject string
	eventItem    string
	truncated    bool
}

// keywordHistory is the kept times of a test that writes items through the API, and what the
// relation answered at each once the fill ran.
type keywordHistory struct {
	t        *testing.T
	database *store.Store
	kept     []keptTime
	versions []map[keywordItem][]keywordVersion
	// reversed is the reversed pair's comment, and reversedEvent transaction A's event of it.
	reversed      keywordItem
	reversedEvent int64
}

// keep keeps a database time after step at which no other transaction of the database is open,
// and every item's search column and text as they stand then (keywordHistory.read).
func (h *keywordHistory) keep(step string) {
	h.t.Helper()
	h.keepAt(step, false, nil)
}

// keepInside keeps a time as keep does while the transaction of backend is open, inside its
// window, which no other kept time falls in.
func (h *keywordHistory) keepInside(step string, backend int32) {
	h.t.Helper()
	h.keepAt(step, true, []int32{backend})
}

func (h *keywordHistory) keepAt(step string, inside bool, open []int32) {
	h.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		kept, quiet := h.read(open)
		if quiet {
			kept.step, kept.inside = step, inside
			h.kept = append(h.kept, kept)
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("after %s, no moment in 30 s had no other transaction of the database open", step)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// keywordBusyQuery answers how many events are committed, and whether another client backend of
// this database is in a transaction, other than those of $1.
const keywordBusyQuery = `
	select (select count(*) from events),
	       exists (select from pg_stat_activity a
	                where a.datname = current_database() and a.pid <> pg_backend_pid()
	                  and a.backend_type = 'client backend' and a.xact_start is not null
	                  and a.pid <> all($1::int[]))`

// keywordItemsQuery reads every item and the text its search column is made from, the column
// itself, its issue, and an issue's primary document and that document's latest version.
const keywordItemsQuery = `
	select 'issue', i.key, i.title, i.search::text, i.key, a.id::text,
	       (select max(v.number) from artifact_versions v where v.artifact_id = a.id)
	  from issues i left join artifacts a on a.issue_key = i.key and a.is_primary
	union all
	select 'comment', c.id::text, c.body, c.search::text, c.issue_key, null, null from comments c
	union all
	select 'message', m.id::text, m.body, m.search::text, m.issue_key, null, null from messages m
	union all
	select 'ask', k.id::text, k.question || ' ' || ask_search_suffix(k.options, k.answer), k.search::text,
	       k.issue_key, null, null
	  from asks k`

// read reads a kept time and the items at it, and answers whether the moment was quiet. The time
// is read with the count of the events committed then; then no other transaction may be open (but
// those of open), so each transaction that began before the time has committed; then the items are
// read in one snapshot; then again no other transaction may be open, and the count of events must
// not have changed since the time was read, so no write that began after the time is among what
// the snapshot read.
func (h *keywordHistory) read(open []int32) (keptTime, bool) {
	h.t.Helper()
	ctx := h.t.Context()
	if open == nil {
		open = []int32{}
	}
	busy := func() (int64, bool) {
		var count int64
		var busy bool
		if err := h.database.Pool.QueryRow(ctx, keywordBusyQuery, open).Scan(&count, &busy); err != nil {
			h.t.Fatalf("read the database's open transactions: %v", err)
		}
		return count, busy
	}
	var kept keptTime
	var at int64
	if err := h.database.Pool.QueryRow(ctx, `select clock_timestamp(), (select count(*) from events)`).Scan(&kept.at, &at); err != nil {
		h.t.Fatalf("read the database's time: %v", err)
	}
	before, open1 := busy()
	if open1 || before != at {
		return keptTime{}, false
	}
	tx, err := h.database.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		h.t.Fatalf("begin the read of the items: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var during int64
	if err := tx.QueryRow(ctx, `select count(*) from events`).Scan(&during); err != nil {
		h.t.Fatalf("count the events: %v", err)
	}
	rows, err := tx.Query(ctx, keywordItemsQuery)
	if err != nil {
		h.t.Fatalf("read the items: %v", err)
	}
	kept.items = map[keywordItem]keptItem{}
	for rows.Next() {
		var key keywordItem
		var item keptItem
		if err := rows.Scan(&key.kind, &key.id, &item.text, &item.search, &item.issueKey, &item.spec, &item.specVersion); err != nil {
			h.t.Fatalf("read the items: %v", err)
		}
		kept.items[key] = item
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("read the items: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		h.t.Fatalf("end the read of the items: %v", err)
	}
	after, open2 := busy()
	return kept, !open2 && before == during && during == after
}

// keywordVersionsQuery reads the relation at a time, whose SQL stands in for {relation}, joined
// with the table's own row of each version, for its body_from, and with the event that started it.
const keywordVersionsQuery = `
	select v.kind, v.id, v.issue_key, v.project_key, v.event_id, v.event_seq, v.started_at, v.body, t.body_from,
	       v.spec_artifact_id::text, v.spec_version, v.search::text,
	       coalesce(e.type, ''), coalesce(e.seq, -1), coalesce(e.created_at, 'epoch'::timestamptz), e.issue_key,
	       coalesce(e.project_key, ei.project_key, ea.project_key, ''),
	       coalesce(case when e.type like 'issue.%' or e.type = 'artifact.version' then e.issue_key
	                     else e.payload->>'id' end, ''),
	       coalesce(e.payload->>'body_truncated' = 'true', false)
	  from {relation} v
	  join search_text_versions t on t.kind = v.kind and t.id = v.id and t.event_seq = v.event_seq
	  left join events e on e.id = v.event_id
	  left join issues ei on ei.key = e.issue_key
	  left join artifacts ea on ea.id = e.artifact_id`

// verify reads the relation at each kept time through store.TextVersionsAt, once the fill has
// reached the last of them, and holds each item that existed then to it (keywordHistory.check).
// At the last kept time each item's version is also its last row.
func (h *keywordHistory) verify() {
	h.t.Helper()
	ctx := h.t.Context()
	keywordAwaitReach(h.t, h.database, h.kept[len(h.kept)-1].at)
	h.versions = make([]map[keywordItem][]keywordVersion, len(h.kept))
	for index, kept := range h.kept {
		relation, err := h.database.TextVersionsAt(ctx, kept.at)
		if err != nil {
			h.t.Fatalf("TextVersionsAt %s (kept after %s): %v", kept.at.Format(time.RFC3339Nano), kept.step, err)
		}
		rows, err := h.database.Pool.Query(ctx, strings.Replace(keywordVersionsQuery, "{relation}", relation.SQL(), 1))
		if err != nil {
			h.t.Fatalf("read the relation at the time kept after %s: %v", kept.step, err)
		}
		got := map[keywordItem][]keywordVersion{}
		for rows.Next() {
			var key keywordItem
			var v keywordVersion
			if err := rows.Scan(&key.kind, &key.id, &v.issueKey, &v.projectKey, &v.eventID, &v.eventSeq, &v.startedAt, &v.body,
				&v.bodyFrom, &v.specArtifact, &v.specVersion, &v.search, &v.eventType, &v.eventSeqOf, &v.eventAt, &v.eventIssue,
				&v.eventProject, &v.eventItem, &v.truncated); err != nil {
				h.t.Fatalf("read the relation at the time kept after %s: %v", kept.step, err)
			}
			got[key] = append(got[key], v)
		}
		if err := rows.Err(); err != nil {
			h.t.Fatalf("read the relation at the time kept after %s: %v", kept.step, err)
		}
		h.versions[index] = got
	}
	failures := 0
	fail := func(format string, args ...any) {
		h.t.Helper()
		if failures++; failures <= 25 {
			h.t.Errorf(format, args...)
		}
	}
	later := h.laterWords()
	for index, kept := range h.kept {
		for key := range h.versions[index] {
			if _, existed := kept.items[key]; !existed {
				fail("after %s: the relation holds a version of %s %s, which did not exist then", kept.step, key.kind, key.id)
			}
		}
		for key, item := range kept.items {
			h.check(fail, index, key, item, later[key][index])
		}
	}
	h.checkLast(fail)
	var directRows int
	if err := h.database.Pool.QueryRow(ctx, `
		select count(*) from search_text_versions v join messages m on m.id::text = v.id
		 where v.kind = 'message' and m.issue_key is null`).Scan(&directRows); err != nil {
		h.t.Fatalf("count the rows of the messages with no issue: %v", err)
	}
	if directRows != 0 {
		fail("the keyword table holds %d rows of messages with no issue, want none", directRows)
	}
	if failures > 25 {
		h.t.Errorf("... and %d more failures", failures-25)
	}
}

// check holds the versions of key at kept time index to the item as it stood then: one version;
// its vector the kept column; its body the kept text (word for word for a body read from a
// comment's blocks, which keep the words of the body and not the white space between its blocks)
// and none of the words written into the item only after the time, later; an issue's naming the
// primary document and the version of it the issue held; and its issue, project, seq and start
// its event's, an event of the item. A message with no issue has no version. Inside the reversed
// pair's window its comment's version is transaction A's, whose text was written after the time,
// and the controls on the vector and the text do not apply; after A's commit its version is A's.
func (h *keywordHistory) check(fail func(string, ...any), index int, key keywordItem, item keptItem, later []string) {
	kept := h.kept[index]
	got := h.versions[index][key]
	if key.kind == "message" && item.issueKey == nil {
		if len(got) != 0 {
			fail("after %s: the message with no issue %s has %d versions, want none", kept.step, key.id, len(got))
		}
		return
	}
	if len(got) != 1 {
		fail("after %s: %s %s has %d versions at %s, want one", kept.step, key.kind, key.id, len(got), kept.at.Format(time.RFC3339Nano))
		return
	}
	v := got[0]
	if key == h.reversed && (kept.inside || index > slices.IndexFunc(h.kept, func(k keptTime) bool { return k.inside })) {
		if v.eventID != h.reversedEvent {
			fail("after %s: the reversed pair's comment has the version of event %d (seq %d), want transaction A's event %d",
				kept.step, v.eventID, v.eventSeq, h.reversedEvent)
		}
	}
	if !(kept.inside && key == h.reversed) {
		if v.search != item.search {
			fail("after %s: %s %s's version (event %d) has the vector %.200s; its search column was %.200s",
				kept.step, key.kind, key.id, v.eventID, v.search, item.search)
		}
		if !keywordSameBody(v, item.text) {
			fail("after %s: %s %s's version (event %d, body from %s) holds %q; its text was %q",
				kept.step, key.kind, key.id, v.eventID, v.bodyFrom, keywordExcerpt(v.body), keywordExcerpt(item.text))
		}
		words := keywordWords(v.body)
		for _, word := range later {
			if words[word] {
				fail("after %s: %s %s's version (event %d) holds %q, written after that time", kept.step, key.kind, key.id, v.eventID, word)
				break
			}
		}
	}
	if key.kind == "issue" && (!keywordSameText(v.specArtifact, item.spec) || !keywordSameNumber(v.specVersion, item.specVersion)) {
		fail("after %s: issue %s's version (event %d) names document %v version %v; the issue held %v version %v",
			kept.step, key.id, v.eventID, keywordText(v.specArtifact), keywordNumber(v.specVersion), keywordText(item.spec), keywordNumber(item.specVersion))
	}
	switch {
	case v.eventType == "":
		fail("after %s: %s %s's version names event %d, which the log does not hold", kept.step, key.kind, key.id, v.eventID)
	case v.eventItem != key.id:
		fail("after %s: %s %s's version names event %d (%s), an event of %s", kept.step, key.kind, key.id, v.eventID, v.eventType, v.eventItem)
	case v.eventSeqOf != v.eventSeq || !v.eventAt.Equal(v.startedAt) || !keywordSameText(v.eventIssue, v.issueKey) || v.eventProject != v.projectKey:
		fail("after %s: %s %s's version holds seq %d, start %s, issue %v and project %q; its event %d (%s) has seq %d, created_at %s, issue %v and project %q",
			kept.step, key.kind, key.id, v.eventSeq, v.startedAt.Format(time.RFC3339Nano), keywordText(v.issueKey), v.projectKey,
			v.eventID, v.eventType, v.eventSeqOf, v.eventAt.Format(time.RFC3339Nano), keywordText(v.eventIssue), v.eventProject)
	}
}

// checkLast holds each item's version at the last kept time, after every change, to its last row:
// an item unchanged since its last version holds that version, whose vector and body check held to
// its present column and text.
func (h *keywordHistory) checkLast(fail func(string, ...any)) {
	rows, err := h.database.Pool.Query(h.t.Context(), `select kind, id, max(event_seq) from search_text_versions group by kind, id`)
	if err != nil {
		h.t.Fatalf("read each item's last row: %v", err)
	}
	lastSeq, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (struct {
		key keywordItem
		seq int32
	}, error) {
		var last struct {
			key keywordItem
			seq int32
		}
		err := row.Scan(&last.key.kind, &last.key.id, &last.seq)
		return last, err
	})
	if err != nil {
		h.t.Fatalf("read each item's last row: %v", err)
	}
	end := len(h.kept) - 1
	for _, last := range lastSeq {
		if got := h.versions[end][last.key]; len(got) == 1 && got[0].eventSeq != last.seq {
			fail("at the end: %s %s holds its version of seq %d, not its last row's seq %d", last.key.kind, last.key.id, got[0].eventSeq, last.seq)
		}
	}
}

// laterWords answers, for each item and kept time, the words its texts at later kept times hold
// that none of its texts up to that time held: the text written after the time.
func (h *keywordHistory) laterWords() map[keywordItem]map[int][]string {
	out := map[keywordItem]map[int][]string{}
	items := map[keywordItem]bool{}
	for _, kept := range h.kept {
		for key := range kept.items {
			items[key] = true
		}
	}
	for key := range items {
		out[key] = map[int][]string{}
		for index := range h.kept {
			seen := map[string]bool{}
			for _, kept := range h.kept[:index+1] {
				if item, ok := kept.items[key]; ok {
					for word := range keywordWords(item.text) {
						seen[word] = true
					}
				}
			}
			added := map[string]bool{}
			for _, kept := range h.kept[index+1:] {
				if item, ok := kept.items[key]; ok {
					for word := range keywordWords(item.text) {
						if !seen[word] {
							added[word] = true
						}
					}
				}
			}
			for word := range added {
				out[key][index] = append(out[key][index], word)
			}
			slices.Sort(out[key][index])
		}
	}
	return out
}

// requireVersion fails unless the version of key at the time kept after step holds want.
func (h *keywordHistory) requireVersion(step string, key keywordItem, want func(keywordVersion) bool, wanted string) {
	h.t.Helper()
	index := slices.IndexFunc(h.kept, func(k keptTime) bool { return k.step == step })
	if index < 0 {
		h.t.Fatalf("no time was kept after %s", step)
	}
	got := h.versions[index][key]
	if len(got) != 1 || !want(got[0]) {
		h.t.Errorf("after %s: %s %s's versions are %+v, want one %s", step, key.kind, key.id, got, wanted)
	}
}

// requireReversedPair fails unless the comment's two edits are the reversed pair: transaction A's
// event, aEvent, has the later seq and the earlier created_at than B's.
func requireReversedPair(t *testing.T, database *store.Store, comment string, aEvent int64) {
	t.Helper()
	var bEvent int64
	var aSeq, bSeq int32
	var aAt, bAt time.Time
	if err := database.Pool.QueryRow(t.Context(), `
		select b.id, b.seq, b.created_at, a.seq, a.created_at
		  from events a, events b
		 where a.id = $2 and b.type = 'comment.edited' and b.payload->>'id' = $1 and b.id <> a.id`,
		comment, aEvent).Scan(&bEvent, &bSeq, &bAt, &aSeq, &aAt); err != nil {
		t.Fatalf("read the reversed pair's two edits: %v", err)
	}
	if aSeq <= bSeq || !aAt.Before(bAt) {
		t.Fatalf("A's event %d has seq %d and created_at %s, B's event %d seq %d and created_at %s: not a reversed pair",
			aEvent, aSeq, aAt.Format(time.RFC3339Nano), bEvent, bSeq, bAt.Format(time.RFC3339Nano))
	}
}

// keywordAwaitReach waits up to 30 s for the keyword table's reach to come to at, the latest time
// the test reads the relation at.
func keywordAwaitReach(t *testing.T, database *store.Store, at time.Time) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		reach, reached, err := database.TextVersionsReach(t.Context())
		if err != nil {
			t.Fatalf("read the keyword table's reach: %v", err)
		}
		if reached && !reach.Before(at.Truncate(time.Microsecond)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the keyword table's reach is %s (reached %v) 30 s on; the last kept time is %s",
				reach.Format(time.RFC3339Nano), reached, at.Format(time.RFC3339Nano))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// keywordSameBody is whether v's body is text: the same words for a body read from a comment's
// first blocks, which drop the white space that ends each block, and the same string otherwise.
func keywordSameBody(v keywordVersion, text string) bool {
	if v.bodyFrom == "blocks" {
		return strings.Join(strings.Fields(v.body), " ") == strings.Join(strings.Fields(text), " ")
	}
	return v.body == text
}

// keywordWords is the set of words of text, lower case.
func keywordWords(text string) map[string]bool {
	words := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		words[word] = true
	}
	return words
}

// keywordExcerpt is text cut for a failure message.
func keywordExcerpt(text string) string {
	if len(text) > 160 {
		return text[:160] + fmt.Sprintf("… (%d bytes)", len(text))
	}
	return text
}

func keywordSameText(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func keywordSameNumber(a, b *int32) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func keywordText(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func keywordNumber(value *int32) any {
	if value == nil {
		return nil
	}
	return *value
}

// keywordLongBody is a body of three paragraphs of about 11,000 UTF-16 units each, each naming
// word: longer than the 32,000 units an event carries whole (model.EventBody), each paragraph one
// block inside the block bound.
func keywordLongBody(word string) string {
	paragraphs := make([]string, 3)
	for index := range paragraphs {
		sentence := fmt.Sprintf("The %s read paragraph %d of the rotation plan before the audit. ", word, index+1)
		paragraphs[index] = strings.TrimSpace(strings.Repeat(sentence, 11000/len(sentence)+1))
	}
	return strings.Join(paragraphs, "\n\n")
}

// requireKeywordAnswer fails unless response answered status, and answers it.
func requireKeywordAnswer(t *testing.T, step string, response *httptest.ResponseRecorder, status int) *httptest.ResponseRecorder {
	t.Helper()
	if response.Code != status {
		t.Fatalf("%s: status=%d body=%s, want %d", step, response.Code, response.Body.String(), status)
	}
	return response
}

// keywordLatestEvent is the id of issue's latest event of eventType.
func keywordLatestEvent(t *testing.T, database *store.Store, eventType, issue string) int64 {
	t.Helper()
	var id int64
	if err := database.Pool.QueryRow(t.Context(),
		`select id from events where type = $1 and issue_key = $2 order by id desc limit 1`, eventType, issue).Scan(&id); err != nil {
		t.Fatalf("read %s's latest %s: %v", issue, eventType, err)
	}
	return id
}

// keywordRowsOfEvents counts the keyword table's rows of the events ids.
func keywordRowsOfEvents(t *testing.T, database *store.Store, ids []int64) int {
	t.Helper()
	var count int
	if err := database.Pool.QueryRow(t.Context(), `select count(*) from search_text_versions where event_id = any($1)`, ids).Scan(&count); err != nil {
		t.Fatalf("count the rows of events %v: %v", ids, err)
	}
	return count
}

// keywordServer is the package's test server with its answers reads timed into latency
// (Deps.AnswersLatency), as a serving process's are, for a fill that pauses on them.
type keywordServer struct {
	handler  http.Handler
	database *store.Store
	deps     Deps
	served   *server
	latency  *sessionanswers.Latency
}

// newKeywordServer stands up a keywordServer on a database of its own.
func newKeywordServer(t *testing.T) keywordServer {
	t.Helper()
	_, database, deps := newTestServer(t, testServerOptions{})
	latency := sessionanswers.NewLatency(time.Now)
	deps.AnswersLatency = latency
	mux := http.NewServeMux()
	served := register(mux, t.Context(), deps)
	t.Cleanup(func() { waitBackgroundOf(t, served) })
	return keywordServer{handler: mux, database: database, deps: deps, served: served, latency: latency}
}

// keywordFill is the fill a serving process runs (textversions.Run) beside a keywordServer: its
// lease pool reserved as the server reserves it, its server listening, its answers reads the
// server's, its events wake the test's, and its lines logged at debug level.
type keywordFill struct {
	logs    *storetest.LockedLog
	events  chan struct{}
	cancel  context.CancelFunc
	stopped chan struct{}
}

// startKeywordFill runs the fill beside server until the test stops it or ends.
func startKeywordFill(t *testing.T, server keywordServer) *keywordFill {
	t.Helper()
	callers, err := server.database.Pool.Leases()
	if err != nil {
		t.Fatalf("open the lease pool: %v", err)
	}
	leases, err := textversions.ReserveLeases(callers)
	if err != nil {
		t.Fatalf("reserve the keyword fill's leases: %v", err)
	}
	listening := make(chan struct{})
	close(listening)
	fill := &keywordFill{logs: &storetest.LockedLog{}, events: make(chan struct{}, 1), stopped: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	fill.cancel = cancel
	config := textversions.Config{
		Leases:    leases,
		Horizon:   events.NewHorizon(server.database.Pool),
		Latency:   server.latency,
		Listening: listening,
		Events:    fill.events,
		Logger:    slog.New(slog.NewJSONHandler(fill.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	go func() {
		defer close(fill.stopped)
		if err := textversions.Run(ctx, config); err != nil {
			t.Errorf("run the keyword fill: %v", err)
		}
	}()
	t.Cleanup(func() { fill.stop(t) })
	return fill
}

// wake signals the fill's events wake, as a commit that wrote an event does through the listener
// hub.
func (f *keywordFill) wake() {
	select {
	case f.events <- struct{}{}:
	default:
	}
}

// stop ends the fill's context, as a serving process's stop does, and waits for Run to return.
func (f *keywordFill) stop(t *testing.T) {
	t.Helper()
	f.cancel()
	select {
	case <-f.stopped:
	case <-time.After(30 * time.Second):
		t.Fatalf("the keyword fill did not return within 30 s of its stop:\n%s", f.logs.String())
	}
}

// The lines of the fill the tests read: a batch's (filler.record), and the pause's two
// (textversions' pausedMessage and continuesMessage), by the start each one's message has.
const (
	keywordBatchLine     = "dispatch: the keyword fill wrote a batch"
	keywordPausedLine    = "dispatch: the keyword fill paused:"
	keywordContinuesLine = "dispatch: the keyword fill continues:"
)

// keywordLine is what the tests read of one line the fill logged.
type keywordLine struct {
	Msg    string `json:"msg"`
	Cursor int64  `json:"cursor"`
}

// lines is the lines the fill logged whose message starts with prefix, in order.
func (f *keywordFill) lines(t *testing.T, prefix string) []keywordLine {
	t.Helper()
	var lines []keywordLine
	scanner := bufio.NewScanner(strings.NewReader(f.logs.String()))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var line keywordLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("read the fill's line %q: %v", scanner.Text(), err)
		}
		if strings.HasPrefix(line.Msg, prefix) {
			lines = append(lines, line)
		}
	}
	return lines
}

// awaitBatches waits until the fill has logged n batches.
func (f *keywordFill) awaitBatches(t *testing.T, n int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for len(f.lines(t, keywordBatchLine)) < n {
		if time.Now().After(deadline) {
			t.Fatalf("the fill logged no %d batches within %v:\n%s", n, within, f.logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// awaitLine waits until the fill logs a line whose message starts with prefix.
func (f *keywordFill) awaitLine(t *testing.T, prefix string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for len(f.lines(t, prefix)) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the fill logged no %q line within %v:\n%s", prefix, within, f.logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// keywordCursor is the fill's cursor, 0 before it wrote its row.
func keywordCursor(t *testing.T, database *store.Store) int64 {
	t.Helper()
	var cursor int64
	if err := database.Pool.QueryRow(t.Context(),
		`select coalesce((select event_id from search_text_versions_fill where id = 1), 0)`).Scan(&cursor); err != nil {
		t.Fatalf("read the keyword fill's cursor: %v", err)
	}
	return cursor
}

// keywordNewestEvent is the newest event's id.
func keywordNewestEvent(t *testing.T, database *store.Store) int64 {
	t.Helper()
	var newest int64
	if err := database.Pool.QueryRow(t.Context(), `select coalesce(max(id), 0) from events`).Scan(&newest); err != nil {
		t.Fatalf("read the newest event: %v", err)
	}
	return newest
}

// keywordAwaitCursor waits until the fill's cursor reaches id, waking the fill each second.
func keywordAwaitCursor(t *testing.T, database *store.Store, id int64, within time.Duration, wake func()) {
	t.Helper()
	deadline := time.Now().Add(within)
	for keywordCursor(t, database) < id {
		if time.Now().After(deadline) {
			t.Fatalf("the fill's cursor is %d %v on; want event %d", keywordCursor(t, database), within, id)
		}
		wake()
		time.Sleep(time.Second)
	}
}

// keywordSeeded is an event the backlog holds and how many keyword versions it starts.
type keywordSeeded struct {
	id       int64
	versions int
}

// seedKeywordBacklog writes project ACME and its issues ACME-1 onwards (seedKeywordIssue) until
// the log holds n events or more, and answers each event with the versions it starts.
func seedKeywordBacklog(t *testing.T, database *store.Store, broker *events.Broker, n int) []keywordSeeded {
	t.Helper()
	if _, err := database.Pool.Exec(t.Context(),
		`insert into projects (key, name, default_assignee) values ('ACME', 'Acme', 'alice')`); err != nil {
		t.Fatalf("seed project ACME: %v", err)
	}
	var seeded []keywordSeeded
	for number := 1; len(seeded) < n; number++ {
		seeded = append(seeded, seedKeywordIssue(t, database, broker, number)...)
	}
	return seeded
}

// seedKeywordIssue writes issue ACME-n with its primary document at versions 1 and 2, and
// fourteen events of it in one transaction, appended through broker as the routes append them: one
// of every type a rule reads, each changing the text it reads and so starting one version, an
// issue.updated that keeps the title and an issue.claimed, which start none.
func seedKeywordIssue(t *testing.T, database *store.Store, broker *events.Broker, n int) []keywordSeeded {
	t.Helper()
	ctx := t.Context()
	key := fmt.Sprintf("ACME-%d", n)
	title := fmt.Sprintf("Rotate the signing keys of region %d", n)
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin issue %s: %v", key, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `
		insert into issues (key, project_key, number, title, created_by, rank)
		values ($1, 'ACME', $2, $3, '{"kind":"user","id":"alice"}', 'A')`, key, n, title); err != nil {
		t.Fatalf("insert issue %s: %v", key, err)
	}
	var artifact string
	if err := tx.QueryRow(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
		values ($1, 'ACME', 'spec', 'spec.md', 'doc', true, '{"kind":"user","id":"alice"}')
		returning id::text`, key).Scan(&artifact); err != nil {
		t.Fatalf("insert the primary document of %s: %v", key, err)
	}
	second := fmt.Sprintf("# Rotation %d\n\nMove each signing key of region %d to its successor, then audit the verifiers.", n, n)
	if _, err := tx.Exec(ctx, `
		insert into artifact_versions (artifact_id, number, markdown, authors)
		values ($1::uuid, 1, $2, '[]'), ($1::uuid, 2, $3, '[]')`,
		artifact, fmt.Sprintf("# Rotation %d\n\nMove each signing key to its successor.", n), second); err != nil {
		t.Fatalf("insert the versions of %s's primary document: %v", key, err)
	}
	actor := model.Actor{Kind: "user", ID: "alice"}
	comment, message, ask := uuid.NewString(), uuid.NewString(), uuid.NewString()
	issueEvent := func(eventType, title string) model.Event {
		return model.Event{IssueKey: &key, Type: eventType, Actor: actor,
			Payload: map[string]any{"key": key, "title": title, "primary_artifact_id": artifact}}
	}
	post := func(eventType, id, body string) model.Event {
		return model.Event{IssueKey: &key, Type: eventType, Actor: actor,
			Payload: map[string]any{"id": id, "issue_key": key, "body": body}}
	}
	askEvent := func(eventType, question string, answer *model.AskAnswer) model.Event {
		return model.Event{IssueKey: &key, Type: eventType, Actor: actor, Payload: model.AskEventPayload{Ask: model.Ask{
			ID: ask, IssueKey: &key, Author: actor, Kind: "question", Question: question, State: "open",
			Options: []model.AskOption{{Label: "Rotate now"}, {Label: "Wait a week"}}, Answer: answer,
		}}}
	}
	note := "Rotate them before the audit."
	steps := []struct {
		event    model.Event
		versions int
	}{
		{issueEvent("issue.created", title), 1},
		{model.Event{IssueKey: &key, Type: "artifact.version", Actor: actor,
			Payload: map[string]any{"artifact_id": artifact, "version": map[string]any{"number": 2, "size": len(second)}}}, 1},
		{issueEvent("issue.updated", title+", then audit"), 1},
		{issueEvent("issue.updated", title+", then audit"), 0},
		{model.Event{IssueKey: &key, Type: "issue.claimed", Actor: actor, Payload: map[string]any{"key": key}}, 0},
		{issueEvent("issue.closed", title+", audited"), 1},
		{post("comment.created", comment, "The verifiers read the old key until Monday."), 1},
		{post("comment.answered", uuid.NewString(), "Monday works; the audit starts Tuesday."), 1},
		{post("comment.edited", comment, "The verifiers read the old key until Tuesday."), 1},
		{post("message.created", message, "Can the rotation wait for the freeze to end?"), 1},
		{post("message.answered", uuid.NewString(), "It can; the freeze ends Friday."), 1},
		{askEvent("ask.opened", "Rotate the keys now?", nil), 1},
		{askEvent("ask.edited", "Rotate the signing keys now?", nil), 1},
		{askEvent("ask.answered", "Rotate the signing keys now?",
			&model.AskAnswer{User: "alice", Selected: []string{"Rotate now"}, Text: &note, At: time.Now()}), 1},
	}
	var out []keywordSeeded
	if err := store.RunWrite(ctx, tx, func(batch *store.WriteBatch) error {
		for _, step := range steps {
			versions := step.versions
			if err := broker.QueueAppend(batch, step.event, func(appended model.Event) {
				out = append(out, keywordSeeded{id: appended.ID, versions: versions})
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("append the events of %s: %v", key, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit issue %s: %v", key, err)
	}
	return out
}

// requireKeywordSeededRows holds the backlog's events to the rows each starts.
func requireKeywordSeededRows(t *testing.T, database *store.Store, seeded []keywordSeeded) {
	t.Helper()
	ids := make([]int64, len(seeded))
	for index, event := range seeded {
		ids[index] = event.id
	}
	rows, err := database.Pool.Query(t.Context(),
		`select event_id, count(*) from search_text_versions where event_id = any($1) group by event_id`, ids)
	if err != nil {
		t.Fatalf("count the backlog's rows: %v", err)
	}
	got := map[int64]int{}
	for rows.Next() {
		var id int64
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			t.Fatalf("count the backlog's rows: %v", err)
		}
		got[id] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("count the backlog's rows: %v", err)
	}
	var wrong []string
	for _, event := range seeded {
		if got[event.id] != event.versions {
			wrong = append(wrong, fmt.Sprintf("event %d: %d rows, want %d", event.id, got[event.id], event.versions))
		}
	}
	if len(wrong) > 0 {
		t.Fatalf("%d events of the backlog have the wrong rows, the first: %v", len(wrong), wrong[:min(len(wrong), 10)])
	}
}

// keywordTableRows is every row of the keyword table of an event at or below upTo, each as one
// line, in key order.
func keywordTableRows(t *testing.T, database *store.Store, upTo int64) []string {
	t.Helper()
	rows, err := database.Pool.Query(t.Context(), `
		select concat_ws(' | ', kind, id, issue_key, project_key, event_id, event_seq, started_at, body_from,
		                 spec_artifact_id, spec_version, md5(body), search::text)
		  from search_text_versions
		 where event_id <= $1
		 order by kind, id, event_seq`, upTo)
	if err != nil {
		t.Fatalf("read the keyword table: %v", err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read the keyword table: %v", err)
	}
	return lines
}

// keywordMaxRounds bounds the rounds of requests that run while the fill catches up.
const keywordMaxRounds = 40

// keywordAnswer is one request of a round and how it was answered: its status, and its body with
// the ids and times a server draws masked (keywordMasked).
type keywordAnswer struct {
	request string
	status  int
	body    string
}

// keywordRound is one round of requests a client makes, in a project of its own so the near-copy
// gate compares its issue with nothing: it writes the project, an issue, a comment and its edit, an
// ask and its answer and a message, reads each, reads an issue of the backlog, and searches for the
// word only the comment's edit wrote, whose one result ranks in no tie the servers' ids would break.
// After each write it wakes the fill and waits out the write's background work
// (awaitBackgroundWrites), so the next request reads what any server would.
func keywordRound(t *testing.T, server keywordServer, round int, wake func()) []keywordAnswer {
	t.Helper()
	var answers []keywordAnswer
	project := fmt.Sprintf("LOAD%d", round)
	answer := func(request string, response *httptest.ResponseRecorder, wrote bool) *httptest.ResponseRecorder {
		t.Helper()
		answers = append(answers, keywordAnswer{request: request, status: response.Code, body: keywordMasked(response.Body.String())})
		if wrote {
			wake()
			awaitBackgroundWrites(t, server.served)
		}
		return response
	}
	h := server.handler
	answer("POST /api/v1/projects", dispatchRequest(t, h, http.MethodPost, "/api/v1/projects",
		map[string]string{"key": project, "name": "Load " + project}, "alice"), true)
	created := requireKeywordAnswer(t, "create the round's issue", answer("POST /api/v1/issues", dispatchRequest(t, h, http.MethodPost,
		"/api/v1/issues", map[string]string{
			"project": project, "title": fmt.Sprintf("Rotate the signing keys of round %d", round),
			"spec": fmt.Sprintf("# Round %d\n\nThe verifiers read the old key until the audit.\n", round),
		}, "alice"), true), http.StatusCreated)
	issue := decodeBody[struct {
		Key string `json:"key"`
	}](t, created).Key
	answer("GET /api/v1/issues/{key}", dispatchRequest(t, h, http.MethodGet, "/api/v1/issues/"+issue, nil, "alice"), false)

	comment := decodeBody[model.Comment](t, requireKeywordAnswer(t, "comment on the round's issue", answer("POST /api/v1/issues/{key}/comments",
		dispatchRequest(t, h, http.MethodPost, "/api/v1/issues/"+issue+"/comments",
			map[string]any{"body": fmt.Sprintf("Round %d: the verifiers read the old key until Monday.", round)}, "alice"), true), http.StatusCreated))
	answer("PATCH /api/v1/comments/{id}", dispatchRequest(t, h, http.MethodPatch, "/api/v1/comments/"+comment.ID,
		map[string]any{"body": fmt.Sprintf("Round %d: the verifiers read the old key until Tuesday.", round)}, "alice"), true)
	answer("GET /api/v1/comments/{id}", dispatchRequest(t, h, http.MethodGet, "/api/v1/comments/"+comment.ID, nil, "alice"), false)

	ask := decodeBody[model.Ask](t, requireKeywordAnswer(t, "ask on the round's issue", answer("POST /api/v1/issues/{key}/asks",
		sessionRequest(t, h, http.MethodPost, "/api/v1/issues/"+issue+"/asks", map[string]any{
			"question": fmt.Sprintf("Rotate round %d's keys now?", round),
			"options":  []map[string]string{{"label": "Rotate now"}, {"label": "Wait a week"}},
			"actor":    sessionActor(),
		}), true), http.StatusCreated))
	answer("POST /api/v1/asks/{id}/answer", dispatchRequest(t, h, http.MethodPost, "/api/v1/asks/"+ask.ID+"/answer",
		map[string]any{"selected": []string{"Rotate now"}, "text": "Before the audit."}, "alice"), true)
	answer("GET /api/v1/asks/{id}", dispatchRequest(t, h, http.MethodGet, "/api/v1/asks/"+ask.ID, nil, "alice"), false)

	answer("POST /api/v1/issues/{key}/messages", dispatchRequest(t, h, http.MethodPost, "/api/v1/issues/"+issue+"/messages",
		map[string]any{"body": fmt.Sprintf("Can round %d wait for the freeze to end?", round)}, "alice"), true)
	answer("GET /api/v1/issues/{key}/messages", dispatchRequest(t, h, http.MethodGet, "/api/v1/issues/"+issue+"/messages", nil, "alice"), false)

	answer("GET /api/v1/issues/{backlog key}", dispatchRequest(t, h, http.MethodGet, fmt.Sprintf("/api/v1/issues/ACME-%d", round), nil, "alice"), false)
	answer("GET /api/v1/search", dispatchRequest(t, h, http.MethodGet, "/api/v1/search?q=Tuesday&project="+project, nil, "alice"), false)
	return answers
}

// keywordUUID, keywordTime and keywordTook match the ids and the times a server draws, and the
// milliseconds a request took that it reports (a search's took_ms), which two servers answering the
// same requests draw differently.
var (
	keywordUUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	keywordTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
	keywordTook = regexp.MustCompile(`("[a-z_]+_ms"):\d+`)
)

// keywordMasked is body with its ids, times and timings masked.
func keywordMasked(body string) string {
	masked := keywordTime.ReplaceAllString(keywordUUID.ReplaceAllString(body, "<uuid>"), "<time>")
	return keywordTook.ReplaceAllString(masked, "$1:<ms>")
}

// keywordFirstDifference is where a failure message shows two differing bodies from: 100 bytes
// before the first byte at which they differ, so the message shows the difference in its place.
func keywordFirstDifference(a, b string) int {
	at := 0
	for at < min(len(a), len(b)) && a[at] == b[at] {
		at++
	}
	return max(0, at-100)
}

// keywordWriteUnderLock writes a comment on issue through the API while message_deliveries is
// locked, which a comment's write does not read, and fails when it does not answer within 5 s.
func keywordWriteUnderLock(t *testing.T, server keywordServer, issue string, n int) {
	t.Helper()
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		answered <- dispatchRequest(t, server.handler, http.MethodPost, "/api/v1/issues/"+issue+"/comments",
			map[string]any{"body": fmt.Sprintf("Note %d, written while the answers read waits.", n)}, "alice")
	}()
	select {
	case response := <-answered:
		if response.Code != http.StatusCreated {
			t.Fatalf("write comment %d under the lock: status=%d body=%s", n, response.Code, response.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("comment %d, written under the lock on message_deliveries, did not answer within 5 s", n)
	}
}
