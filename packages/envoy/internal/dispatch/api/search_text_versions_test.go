package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/sessionanswers"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/dispatch/textversions"
	"github.com/sjawhar/envoy/internal/dispatch/textversions/textversionstest"
	"github.com/sjawhar/envoy/internal/testwait"
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
	wantStatus(t, "retitle the issue", dispatchRequest(t, handler, http.MethodPatch, "/api/v1/issues/"+issue.Key,
		map[string]any{"title": "Rotate the signing keys before the audit"}, "alice"), http.StatusOK)
	history.keep("the issue's title changes")
	wantStatus(t, "move the issue's status", dispatchRequest(t, handler, http.MethodPatch, "/api/v1/issues/"+issue.Key,
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
	callbackReply := decodeBody[model.Comment](t, wantStatus(t, "reply through the delivery callback",
		bearerRequest(t, handler, http.MethodPost, "/api/v1/comments/"+mentioned.ID+"/reply", map[string]any{
			"actor": map[string]any{"kind": "session", "id": "s1"}, "attempt": 1, "body": "It ships at nine, the canary first.",
		}), http.StatusCreated))
	history.keep("the session's reply through the delivery callback")
	requireEdit(t, "edit the comment", editCommentAs(t, handler, comment.ID, "The verifiers read the old key until Wednesday.", nil, nil))
	history.keep("the comment's edit")
	wantStatus(t, "resolve the comment", dispatchRequest(t, handler, http.MethodPost, "/api/v1/comments/"+comment.ID+"/resolve",
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
	wantStatus(t, "write an ask block into the primary document", sessionRequest(t, handler, http.MethodPost,
		"/api/v1/artifacts/"+issue.PrimaryArtifactID+"/edits", map[string]any{
			"actor": sessionActor(), "ops": []map[string]string{{"op": "insert", "after": "end", "markdown": transportAsk}},
		}), http.StatusOK)
	awaitIndexedAskBlock(t, handler, issue.PrimaryArtifactID, "decision", "Which transport?")
	askID := blockAskID(t, handler, issue.Key, "decision")
	history.keep("an ask")
	wantStatus(t, "edit the ask", sessionRequest(t, handler, http.MethodPatch, "/api/v1/asks/"+askID, map[string]any{
		"question": "Which transport ships first?", "actor": sessionActor(),
	}), http.StatusOK)
	history.keep("the ask's edit")
	wantStatus(t, "answer the ask", dispatchRequest(t, handler, http.MethodPost, "/api/v1/asks/"+askID+"/answer", map[string]any{
		"selected": []string{"REST"}, "text": "REST first; gRPC after the audit.", "expected_edited_at": readBlockAsk(t, handler, askID).EditedAt,
	}, "alice"), http.StatusOK)
	history.keep("the ask's answer")
	wantStatus(t, "delete the answered ask's block", dispatchRequest(t, handler, http.MethodPost,
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
	wantStatus(t, "write the answered ask's block back", dispatchRequest(t, handler, http.MethodPost,
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
	documentComment := decodeBody[model.Comment](t, wantStatus(t, "comment on the project document",
		dispatchRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+document.ID+"/comments",
			map[string]any{"body": "Name the pager rotation here."}, "alice"), http.StatusCreated))
	history.keep("a comment on a project document")
	documentAsk := decodeBody[model.Ask](t, wantStatus(t, "ask on the project document",
		sessionRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+document.ID+"/asks",
			map[string]any{"question": "Who owns the pager rotation?", "actor": sessionActor()}), http.StatusCreated))
	history.keep("an ask on a project document")
	if documentComment.IssueKey != nil || documentAsk.IssueKey != nil {
		t.Fatalf("the project document's comment and ask name issues %v and %v, want none", documentComment.IssueKey, documentAsk.IssueKey)
	}

	// A message with no issue, a session's direct message, which the table leaves out.
	direct := decodeBody[model.Message](t, wantStatus(t, "message the session with no issue",
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
	a, aStarted := textversionstest.BeginTransaction(t, database)
	defer func() { _ = a.Rollback(context.Background()) }()
	var aBackend int32
	if err := a.QueryRow(ctx, `select pg_backend_pid()`).Scan(&aBackend); err != nil {
		t.Fatalf("read transaction A's backend: %v", err)
	}
	edited := decodeBody[model.Comment](t, wantStatus(t, "B edits the comment",
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
	if rows := textversionstest.RowsOf(t, database, unchanging...); rows != 0 {
		t.Errorf("the status-only issue.updated and the comment.resolved (events %v) have %d rows, want none", unchanging, rows)
	}
}

// TestTheKeywordFillWritesWhileTheServerAnswers runs the fill as a serving process runs it
// (textversions.Run, its process serving) beside the handler of a server whose log holds 2,000
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
	last := backlog[len(backlog)-1].ID

	fill := startKeywordFill(t, filled)
	fill.AwaitLine(t, keywordBatchLine)
	if cursor := textversionstest.Cursor(t, filled.database); cursor >= last {
		t.Fatalf("the fill's first batch took the cursor to %d, the backlog's last event %d: nothing is left to catch up beside the requests", cursor, last)
	}
	before := len(fill.Lines(t, keywordBatchLine))
	// The rounds run until the fill has caught up the backlog. A round usually outlasts a batch and
	// its rest, so the cursor moves while it runs and the next round begins at once; after a round
	// in which the cursor stood, the row waits for it to move before the next. Each round thus
	// begins with the cursor past where the one before began, and a fill that writes no more of
	// the backlog fails naming where its cursor stands, at the shared wait's give-up.
	var rounds [][]keywordAnswer
	for cursor := textversionstest.Cursor(t, filled.database); cursor < last; {
		rounds = append(rounds, keywordRound(t, filled, len(rounds)+1, fill.Wake))
		stood := cursor
		cursor = testwait.Until(t, fmt.Sprintf("the keyword fill's cursor to move past event %d, where it stood as round %d of requests began, toward the backlog's last event %d", stood, len(rounds), last), func() (int64, bool) {
			cursor := textversionstest.Cursor(t, filled.database)
			if cursor > stood {
				return cursor, true
			}
			fill.Wake()
			return cursor, false
		})
	}
	if during := len(fill.Lines(t, keywordBatchLine)) - before; during < 2 {
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
	newest := lastEventID(t, filled.database)
	keywordAwaitCursor(t, filled.database, newest, fill.Wake)
	fill.Stop(t)
	textversionstest.RequireRowsOf(t, filled.database, backlog)
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
// A read counts as slow only 800 ms after it began, and a fast database writes the whole backlog
// sooner, so the row holds the fill's first batch, which asked its pause while the reads were
// fast, at a gate of the test's own before the batch's transaction begins, through the read's
// turn to slow, and then opens the gate (keywordArmSlowRead, which takes the arming again where
// the fill loses its hold or a pass of it fails): that batch writes, and the fill's next line is
// its pause, with most of the backlog still to write. For the 20 s the lock on
// message_deliveries then holds, while events are written, the cursor does not move. Once the
// lock is released, reads go on, and for the 10 s after the slow read ended the cursor still
// stands; then the fill says it continues and the cursor moves, and every event, those written
// under the lock included, gets its rows.
func TestTheKeywordFillPausesWhileTheAnswersReadIsSlow(t *testing.T) {
	server := newKeywordServer(t)
	backlog := seedKeywordBacklog(t, server.database, server.deps.Events, 4000)
	last := backlog[len(backlog)-1].ID
	issue := createInteractionIssue(t, server.handler, "TEST", "Rotate the signing keys", "# Rotation\n")
	keywordRecordCursorWrites(t, server.database)
	const answers = "/api/v1/agents/" + answersSession + "/answers"
	fill, holder, held, wrote := keywordArmSlowRead(t, server, answers)
	defer func() { _ = holder.Rollback(context.Background()) }()
	pausedAt := wrote + 1
	batchesBeforePause := keywordCountLines(fill.Lines(t, "")[:pausedAt], keywordBatchLine)
	// The fill writes nothing while paused, so every write of the cursor recorded so far is
	// committed, the in-flight batch's last.
	var pauseMark int64
	if before := keywordCursorWritesAfter(t, server.database, 0); len(before) > 0 {
		pauseMark = before[len(before)-1].id
	}
	paused := textversionstest.Cursor(t, server.database)
	if paused >= last {
		t.Fatalf("the fill paused with its cursor at %d, the backlog's last event %d: it was not catching up", paused, last)
	}

	// The lock holds 20 s while events are written, and the cursor stands: ten rounds, each a
	// write, a wake and a 2 s span, after which the cursor is read. A cursor that moved stays moved,
	// so the read at each span's end sees any move inside it. What is checked is an absence, so the
	// spans are held for keywordPausedWhy.
	writes := 0
	for range 10 {
		writes++
		keywordWriteUnderLock(t, server, issue.Key, writes)
		fill.Wake()
		testwait.Span(t, 2*time.Second, keywordPausedWhy)
		if cursor := textversionstest.Cursor(t, server.database); cursor != paused {
			t.Fatalf("the cursor moved from %d to %d while the answers read was held by the lock", paused, cursor)
		}
	}
	if batches := len(fill.Lines(t, keywordBatchLine)); batches != batchesBeforePause {
		t.Fatalf("the fill logged %d batches while paused, want none", batches-batchesBeforePause)
	}
	// The held read ends after the lock is released, so the fill may write nothing until 10 s after
	// the release. The release's moment is read on the database's clock, through the holder before
	// its rollback, for the check of the transaction starts it records (keywordRecordCursorWrites).
	var releasingAtDatabase time.Time
	if err := holder.QueryRow(t.Context(), `select clock_timestamp()`).Scan(&releasingAtDatabase); err != nil {
		t.Fatalf("read the database's clock before the release: %v", err)
	}
	if err := holder.Rollback(t.Context()); err != nil {
		t.Fatalf("release message_deliveries: %v", err)
	}
	if response := testwait.Receive(t, "the held answers read to answer once the lock is released", held); response.Code != http.StatusOK {
		t.Fatalf("the held answers read answered %d: %s", response.Code, response.Body.String())
	}

	// For the 10 s after the held read ended the reads go on, one a second, as agents' reads do.
	// Each answers within 800 ms on an idle machine; one that takes longer, as on a loaded one, only
	// lengthens the pause, so its time is logged, not failed. Whether the fill held its cursor for
	// the 10 s is decided below from when each write of the cursor began, which Postgres records,
	// against the database's clock read before the release, so no reading of this machine's clock
	// decides it.
	for range int((sessionanswers.SlowWithin + time.Second) / time.Second) {
		began := time.Now()
		if response := dispatchRequest(t, server.handler, http.MethodGet, answers, nil, "alice"); response.Code != http.StatusOK {
			t.Fatalf("an answers read after the release answered %d: %s", response.Code, response.Body.String())
		}
		if took := time.Since(began); took > sessionanswers.SlowRead {
			t.Logf("an answers read after the release took %v, over %v: the fill's pause runs to %v after it ended", took, sessionanswers.SlowRead, sessionanswers.SlowWithin)
		}
		testwait.Span(t, time.Second, keywordPausedWhy)
	}

	// Then the fill continues: the cursor moves, and every event gets its rows. Every write of the
	// cursor after the pause began 10 s or more after the release, which is when the held read can
	// have ended at the earliest: the transaction start Postgres recorded for each
	// (keywordRecordCursorWrites), against the database's clock read before the release.
	keywordAwaitLineFrom(t, fill, pausedAt, keywordContinuesLine)
	testwait.Until(t, fmt.Sprintf("the cursor to move from %d once the fill says it continues", paused), func() (int64, bool) {
		cursor := textversionstest.Cursor(t, server.database)
		return cursor, cursor != paused
	})

	newest := lastEventID(t, server.database)
	testwait.Until(t, fmt.Sprintf("the fill's cursor to reach event %d", newest), func() (int64, bool) {
		cursor := textversionstest.Cursor(t, server.database)
		fill.Wake()
		return cursor, cursor >= newest
	})

	after := keywordCursorWritesAfter(t, server.database, pauseMark)
	if len(after) == 0 {
		t.Fatalf("no write of the keyword fill's cursor was recorded after its pause, though the cursor moved from %d to %d", paused, newest)
	}
	for _, write := range after {
		if earliest := releasingAtDatabase.Add(sessionanswers.SlowWithin); write.began.Before(earliest) {
			t.Fatalf("the fill wrote its cursor to event %d in a transaction begun at %s, before %s, 10 s after the slow read could have ended, both on the database's clock:\n%s",
				write.event, write.began.Format(time.RFC3339Nano), earliest.Format(time.RFC3339Nano), fill.Log())
		}
	}
	textversionstest.RequireRowsOf(t, server.database, backlog)
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

// startKeywordFill runs the fill as a serving process runs it (textversions.Run, through
// textversionstest.StartFill) beside server, until the test stops it or ends: its lease pool
// reserved of server's pool as the server reserves it (reserveKeywordFillLeases), its process
// serving and its answers reads the server's.
func startKeywordFill(t *testing.T, server keywordServer) *textversionstest.Fill {
	t.Helper()
	return runKeywordFill(t, server, reserveKeywordFillLeases(t, server.database.Pool))
}

// reserveKeywordFillLeases reserves the keyword fill's connections of pool's lease pool, as the
// server reserves them of its own (textversions.ReserveLeases), and answers the lease pool: the
// server's pool, or a second pool on its database whose statements a test traces
// (keywordArmSlowRead).
func reserveKeywordFillLeases(t *testing.T, pool *store.Pool) *pgxpool.Pool {
	t.Helper()
	callers, err := pool.Leases()
	if err != nil {
		t.Fatalf("open the lease pool: %v", err)
	}
	leases, err := textversions.ReserveLeases(callers)
	if err != nil {
		t.Fatalf("reserve the keyword fill's leases: %v", err)
	}
	return leases
}

// runKeywordFill runs the fill beside server as startKeywordFill does, on leases, the lease pool
// reserveKeywordFillLeases reserved.
func runKeywordFill(t *testing.T, server keywordServer, leases *pgxpool.Pool) *textversionstest.Fill {
	t.Helper()
	serving := make(chan struct{})
	close(serving)
	config := textversions.Config{
		Leases:  leases,
		Horizon: events.NewHorizon(server.database.Pool),
		Latency: server.latency,
		Serving: serving,
	}
	return textversionstest.StartFill(t, func(ctx context.Context, wake <-chan struct{}, logger *slog.Logger) error {
		config.Events, config.Logger = wake, logger
		return textversions.Run(ctx, config)
	})
}

// The lines of the fill the tests read, by the start of each one's message: a batch's
// (filler.record), the pause's two (textversions' pausedMessage and continuesMessage), the hold's
// take and its loss (tookHoldMessage and lostHoldMessage), and a pass's failure
// (runner.fillUnder), which carries the pass's error, such as a batch's statement past its
// statement_timeout.
const (
	keywordBatchLine      = "dispatch: the keyword fill wrote a batch"
	keywordPausedLine     = "dispatch: the keyword fill paused:"
	keywordContinuesLine  = "dispatch: the keyword fill continues:"
	keywordTookHoldLine   = "dispatch: the keyword fill took its hold"
	keywordLostHoldLine   = "dispatch: the keyword fill's hold is lost"
	keywordPassFailedLine = "dispatch: the keyword fill's pass failed"
)

// keywordArmSlowRead runs the fill beside server on its backlog with the first batch of that fill,
// one that asked its pause while the reads were fast, held at a gate of the test's own before its
// transaction begins (keywordBatchGate), makes an answers read through server's handler slow, and
// opens the gate. It answers once that batch has written and the fill has paused before its next
// batch, with the fill, the transaction holding message_deliveries, on which the read is held, the
// channel the held read answers on, and the index of the batch's line among the fill's lines: the
// first line the fill logged after the read turned slow, the paused line the next.
//
// The read turns slow 800 ms after it began, on the wall clock, and the batch waits through that
// turn at the gate alone. It has begun no transaction there, so none of its bounds runs on it
// (textversions' batchBoundsQuery sets its lock_timeout, statement_timeout and
// idle_in_transaction_session_timeout inside the transaction), and nothing lets it go on but the
// test's opening of the gate, or the end of the fill's context, which ends the batch. So however
// long the read takes to turn slow, or the test's own thread to see it, the batch in flight once
// the read is slow is the one that asked its pause while the reads were fast.
//
// A round (keywordArmRound) in which the fill loses its hold or a pass of it fails, as a loaded
// machine can bring about on correct code (a renewal of the hold that does not reach the database
// within its margin, a batch's statement past its statement_timeout), is taken again, as main's
// row took it again: the read is let go, the round's fill stopped, the keyword table and its
// cursor emptied, and once the reads are fast again the next round's fill runs, its first batch
// held at a gate of its own (keywordEndArmRound, keywordGates). No count bounds the rounds: each
// wait of a round is the shared wait and names what the rounds before it ended in
// (keywordArmRounds), so a fill that ends every round so gives up naming the last line it ended
// one in, whichever wait the give-up comes in. Any other wrong outcome fails the row by its own
// name: before the gate opens, a batch's or a pause's line, or a last line other than the hold's
// take; once it opens, a line other than that batch's (a pause that stops the batch in flight);
// after that batch's, a line other than its pause (another batch, or another line first).
func keywordArmSlowRead(t *testing.T, server keywordServer, answers string) (*textversionstest.Fill, pgx.Tx, chan *httptest.ResponseRecorder, int) {
	t.Helper()
	// The rounds' locks and the transactions they sit in run on the shared wait's context, so a
	// lock that is never granted ends its statement at the give-up (keywordLock).
	bound := testwait.Context(t)
	gates := &keywordGates{}
	gate := gates.arm()
	leases := reserveKeywordFillLeases(t, storetest.Traced(t, server.database.Pool, gates, nil))
	rounds := &keywordArmRounds{}
	for {
		rounds.number++
		fill, holder, held, wrote, ended := keywordArmRound(t, bound, server, answers, leases, gate, rounds)
		if ended == "" {
			return fill, holder, held, wrote
		}
		rounds.again++
		rounds.last = ended
		t.Logf("round %d of arming the slow read ended on %s; it is taken again", rounds.number, ended)
		keywordEndArmRound(t, bound, server, fill, holder, held, rounds)
		gate = gates.arm()
	}
}

// keywordArmRounds is what the rounds of keywordArmSlowRead have read: the number of the round
// under way, how many rounds were taken again, and the line the last of those ended in, as the
// fill wrote it, a lost hold's or a failed pass's with its error. Each wait of a round names it
// (what).
type keywordArmRounds struct {
	number, again int
	last          string
}

// what is fact, a fact a wait of the round under way waits for, named with what the rounds have
// read once one was taken again, so a give-up in any of the waits names the line that ended the
// last round taken again.
func (r *keywordArmRounds) what(fact string) string {
	if r.again == 0 {
		return fact
	}
	return fmt.Sprintf("%s (round %d of arming the slow read; %d rounds so far ended in a lost hold or a failed pass and were taken again, the last on %s)", fact, r.number, r.again, r.last)
}

// keywordArmRound is one round of keywordArmSlowRead: it runs a fill of its own on leases, whose
// first batch gate holds, makes an answers read through server's handler slow and opens the gate.
// It answers the line the round ended in, as the fill wrote it, when the fill lost its hold or a
// pass of it failed, so the round is taken again (keywordEndArmRound), and "" with the index of
// the batch's line once the batch held at the gate has written and the fill's next line is its
// pause. Any other outcome fails the test by its own name. The transaction that locks
// message_deliveries begins and takes its lock on bound, the shared wait's context.
func keywordArmRound(t *testing.T, bound context.Context, server keywordServer, answers string, leases *pgxpool.Pool, gate *keywordBatchGate, rounds *keywordArmRounds) (fill *textversionstest.Fill, holder pgx.Tx, held chan *httptest.ResponseRecorder, wrote int, ended string) {
	t.Helper()
	fill = runKeywordFill(t, server, leases)
	testwait.Until(t, rounds.what("the keyword fill's first batch to reach the gate before its transaction"), func() (string, bool) {
		reached := gate.reached()
		return fmt.Sprintf("at the gate: %t; the fill's log:\n%s", reached, fill.Log()), reached
	})
	holder = keywordBegin(t, bound, server.database, rounds.what("a transaction to lock message_deliveries in"))
	handed := false
	defer func() {
		if !handed {
			_ = holder.Rollback(context.Background())
		}
	}()
	keywordLock(t, bound, server.database, holder, "message_deliveries", "access exclusive",
		rounds.what("the access exclusive lock on message_deliveries, on which the answers read is to wait"))
	held = make(chan *httptest.ResponseRecorder, 1)
	go func() { held <- dispatchRequest(t, server.handler, http.MethodGet, answers, nil, "alice") }()
	testwait.Until(t, rounds.what("the answers read to wait on message_deliveries"), func() (int, bool) {
		queued := storetest.LockWaiters(t, holder)
		return queued, queued >= 1
	})
	testwait.Until(t, rounds.what("the answers read held by the lock to turn slow"), func() (string, bool) {
		now := time.Now()
		slow := server.latency.Slow(now)
		return fmt.Sprintf("the latency's Slow at %s: %t; the held read answered: %t", now.Format(time.RFC3339Nano), slow, len(held) > 0), slow
	})
	// The fill logs its hold's take before its first batch. A try for the hold that failed before
	// the take is a standby's, after which the fill tries again 10 s later (textversions'
	// runner.standBy): its bound of 5 s covers the first dial of a fresh lease pool, which a loaded
	// machine can take longer than, and the batch held at the gate is still the first. A lost hold
	// or a failed pass, before the gate opens or after, ends the round.
	logged := fill.Lines(t, "")
	for index, line := range logged {
		if keywordEndsRound(line) {
			handed = true
			return fill, holder, held, 0, keywordRawLine(fill, index)
		}
	}
	for _, line := range logged {
		if strings.HasPrefix(line, keywordBatchLine) || strings.HasPrefix(line, keywordPausedLine) {
			t.Fatalf("as the answers read turned slow, with the keyword fill's first batch held at the gate before its transaction, the fill had logged %q: a batch or a pause before that batch:\n%s", logged, fill.Log())
		}
	}
	if len(logged) == 0 || !strings.HasPrefix(logged[len(logged)-1], keywordTookHoldLine) {
		t.Fatalf("as the answers read turned slow, with the keyword fill's first batch held at the gate before its transaction, the fill had logged %q, whose last line is not its hold's take:\n%s", logged, fill.Log())
	}
	wrote = len(logged)
	gate.open()
	// The batch's outcome is the fill's next line: its batch line once it commits, or the line of
	// a hold that was lost, of a pass that failed or of a pause. A batch rests as long as it ran,
	// its wait at the gate included, before the next asks the pause.
	switch first := keywordLineAt(t, fill, wrote, rounds); {
	case keywordEndsRound(first):
		handed = true
		return fill, holder, held, 0, keywordRawLine(fill, wrote)
	case !strings.HasPrefix(first, keywordBatchLine):
		t.Fatalf("once the gate let the batch held through the answers read's turn to slow go on, the fill logged %q, not that batch's line: the batch in flight did not write:\n%s", first, fill.Log())
	}
	switch next := keywordLineAt(t, fill, wrote+1, rounds); {
	case strings.HasPrefix(next, keywordBatchLine):
		t.Fatalf("after the answers read turned slow, the fill wrote the batch in flight and then another without pausing:\n%s", fill.Log())
	case keywordEndsRound(next):
		handed = true
		return fill, holder, held, 0, keywordRawLine(fill, wrote+1)
	case !strings.HasPrefix(next, keywordPausedLine):
		t.Fatalf("after the answers read turned slow, the fill wrote the batch in flight and then logged %q, not its pause:\n%s", next, fill.Log())
	}
	handed = true
	return fill, holder, held, wrote, ""
}

// keywordEndsRound is whether line, a message of the fill's, ends a round of keywordArmSlowRead
// for it to be taken again: the hold's loss, or a pass that failed.
func keywordEndsRound(line string) bool {
	return strings.HasPrefix(line, keywordLostHoldLine) || strings.HasPrefix(line, keywordPassFailedLine)
}

// keywordRawLine is line index of fill's log as the fill wrote it, with its attributes, the error
// of a pass that failed among them: the lines fill.Lines reads are these, in this order.
func keywordRawLine(fill *textversionstest.Fill, index int) string {
	at := 0
	for line := range strings.Lines(fill.Log()) {
		if at == index {
			return strings.TrimSuffix(line, "\n")
		}
		at++
	}
	return ""
}

// keywordEndArmRound ends a round of keywordArmSlowRead that is taken again, as main's row ended
// one: it releases message_deliveries and waits for the held read's answer, stops the round's
// fill and waits for it to return, empties the keyword table and its cursor, so the next round
// has the whole backlog to write, and waits for the answers reads to be fast again, so the next
// round's first batch asks its pause while they are. Each wait names what the rounds have read
// (keywordArmRounds.what), and the transaction that empties the tables runs on bound, the shared
// wait's context.
func keywordEndArmRound(t *testing.T, bound context.Context, server keywordServer, fill *textversionstest.Fill, holder pgx.Tx, held chan *httptest.ResponseRecorder, rounds *keywordArmRounds) {
	t.Helper()
	if err := holder.Rollback(t.Context()); err != nil {
		t.Fatalf("release message_deliveries: %v", err)
	}
	if response := testwait.Receive(t, rounds.what("the held answers read to answer once the lock is released"), held); response.Code != http.StatusOK {
		t.Fatalf("the held answers read answered %d: %s", response.Code, response.Body.String())
	}
	fill.Cancel()
	testwait.Until(t, rounds.what("the round's keyword fill to return after its stop"), func() (string, bool) {
		return fill.Log(), fill.Stopped()
	})
	// A batch locks the cursor's row and holds that lock to its end, so the exclusive lock waits
	// out any batch of the stopped fill still running on the server, whose Go side has already let
	// go, and the deletes after it see what that batch committed.
	emptying := keywordBegin(t, bound, server.database, rounds.what("a transaction to empty the keyword table and its cursor in"))
	defer func() { _ = emptying.Rollback(context.Background()) }()
	keywordLock(t, bound, server.database, emptying, "search_text_versions_fill", "exclusive",
		rounds.what("the exclusive lock on the keyword fill's cursor table, to empty it and the keyword table for the next round"))
	for _, statement := range []string{`delete from search_text_versions`, `delete from search_text_versions_fill`} {
		if _, err := emptying.Exec(bound, statement); err != nil {
			keywordBoundFailed(t, bound, rounds.what(fmt.Sprintf("the keyword table and its cursor to be emptied (%s)", statement)), err)
		}
	}
	if err := emptying.Commit(bound); err != nil {
		keywordBoundFailed(t, bound, rounds.what("the emptied keyword table and its cursor to commit"), err)
	}
	testwait.Until(t, rounds.what("the answers reads to be fast again"), func() (string, bool) {
		now := time.Now()
		slow := server.latency.Slow(now)
		return fmt.Sprintf("the latency's Slow at %s: %t", now.Format(time.RFC3339Nano), slow), !slow
	})
}

// keywordBegin begins a transaction on database for a round of keywordArmSlowRead on bound, the
// shared wait's context, and fails t naming what when it cannot (keywordBoundFailed).
func keywordBegin(t *testing.T, bound context.Context, database *store.Store, what string) pgx.Tx {
	t.Helper()
	tx, err := database.Pool.Begin(bound)
	if err != nil {
		keywordBoundFailed(t, bound, what, err)
	}
	return tx
}

// keywordLock takes the lock of mode on table in tx on bound, the shared wait's context
// (testwait.Context), for a round of keywordArmSlowRead. A lock that is never granted ends the
// statement at the give-up, and the failure names what, with the sessions that hold a lock on
// table then (keywordLockHolders).
func keywordLock(t *testing.T, bound context.Context, database *store.Store, tx pgx.Tx, table, mode, what string) {
	t.Helper()
	if _, err := tx.Exec(bound, fmt.Sprintf("lock table %s in %s mode", table, mode)); err != nil {
		keywordBoundFailed(t, bound, what, fmt.Errorf("%w; the sessions holding a lock on %s: %s", err, table, keywordLockHolders(t, database, table)))
	}
}

// keywordBoundFailed fails t on err, which ended a statement of a round of keywordArmSlowRead that
// ran on bound, the shared wait's context: once bound has ended, as the give-up of a wait for
// what, which is how a lock never granted ends; before, as what failing.
func keywordBoundFailed(t *testing.T, bound context.Context, what string, err error) {
	t.Helper()
	if bound.Err() != nil {
		t.Fatalf("gave up waiting for %s, at the shared wait's give-up (testwait.Context): %v", what, err)
	}
	t.Fatalf("%s failed: %v", what, err)
}

// keywordLockHolders is every session that holds a granted lock on table in database's database,
// each with its pid, the lock's mode, its state and the start of its last statement, for the
// failure of a lock to name; or why they could not be read.
func keywordLockHolders(t *testing.T, database *store.Store, table string) string {
	t.Helper()
	rows, err := database.Pool.Query(t.Context(), `
		select format('pid %s holds %s (%s): %s', l.pid, l.mode, a.state, left(a.query, 200))
		  from pg_locks l join pg_stat_activity a on a.pid = l.pid
		 where l.locktype = 'relation' and l.granted
		   and l.database = (select oid from pg_database where datname = current_database())
		   and l.relation = to_regclass($1)
		 order by l.pid`, table)
	if err != nil {
		return fmt.Sprintf("(not read: %v)", err)
	}
	holders, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Sprintf("(not read: %v)", err)
	}
	return fmt.Sprintf("%q", holders)
}

// keywordGates is the tracer (storetest.Traced) of the pool the rounds of keywordArmSlowRead run
// their fills on, one round at a time: it hands each statement to the gate of the round under way,
// which the round arms before its fill starts, once the fill of the round before has returned.
type keywordGates struct {
	current atomic.Pointer[keywordBatchGate]
}

// arm makes a new shut gate, which no batch has reached, the gate of the round under way, and
// answers it.
func (g *keywordGates) arm() *keywordBatchGate {
	gate := newKeywordBatchGate()
	g.current.Store(gate)
	return gate
}

func (g *keywordGates) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if gate := g.current.Load(); gate != nil {
		return gate.TraceQueryStart(ctx, conn, data)
	}
	return ctx
}

func (g *keywordGates) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// keywordBatchGate is the gate of one round of keywordArmSlowRead, to which the tracer of the pool
// the keyword fill alone runs on (keywordGates) hands the statements of that round's fill: it holds
// the first transaction begun from starting until the test opens the gate. The fill begins a
// transaction for each of its batches and for nothing else (textversions' filler.batch; its hold's
// statements and its read of where it stands run outside one), so the first is its first batch's,
// which asked its pause the moment before (filler.batchesTo). The gate holds the BEGIN before it
// is sent, so the server holds no transaction of the batch's yet. A context that ends lets the
// batch go too, so a fill stopped at the round's end or the test's does not wait there.
type keywordBatchGate struct {
	first sync.Once
	// at is closed once the first BEGIN is at the gate, opened once the test opens the gate.
	at, opened chan struct{}
}

// newKeywordBatchGate is a shut gate no batch has reached.
func newKeywordBatchGate() *keywordBatchGate {
	return &keywordBatchGate{at: make(chan struct{}), opened: make(chan struct{})}
}

func (g *keywordBatchGate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !strings.HasPrefix(data.SQL, "begin") {
		return ctx
	}
	held := false
	g.first.Do(func() {
		held = true
		close(g.at)
	})
	if held {
		select {
		case <-g.opened:
		case <-ctx.Done():
		}
	}
	return ctx
}

func (g *keywordBatchGate) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// reached is whether the fill's first batch has reached the gate.
func (g *keywordBatchGate) reached() bool {
	select {
	case <-g.at:
		return true
	default:
		return false
	}
}

// open lets the batch held at the gate, and every later one, go on.
func (g *keywordBatchGate) open() {
	close(g.opened)
}

// keywordLineAt waits for fill to have logged its line index, and answers that line's message.
// Its one bound is the test binary's deadline (testwait.Until): a batch rests as long as it ran,
// which nothing in the fill bounds, before the next asks the pause. The wait names what the rounds
// of keywordArmSlowRead have read (keywordArmRounds.what).
func keywordLineAt(t *testing.T, fill *textversionstest.Fill, index int, rounds *keywordArmRounds) string {
	t.Helper()
	return testwait.Until(t, rounds.what(fmt.Sprintf("the keyword fill to log its line %d", index+1)), func() (string, bool) {
		lines := fill.Lines(t, "")
		if len(lines) > index {
			return lines[index], true
		}
		return fmt.Sprintf("%d lines:\n%s", len(lines), fill.Log()), false
	})
}

// keywordAwaitLineFrom waits for fill to log, at its line from or later, a line whose message
// starts with prefix, and answers that line's index among all its lines. Its one bound is the
// test binary's deadline (testwait.Until).
func keywordAwaitLineFrom(t *testing.T, fill *textversionstest.Fill, from int, prefix string) int {
	t.Helper()
	return testwait.Until(t, fmt.Sprintf("the keyword fill to log a %q line", prefix), func() (int, bool) {
		lines := fill.Lines(t, "")
		for index := from; index < len(lines); index++ {
			if strings.HasPrefix(lines[index], prefix) {
				return index, true
			}
		}
		return len(lines), false
	})
}

// keywordRecordCursorWrites gives database, the row's own, a table and a trigger of this test's
// alone, which no product code reads: each insert or update of the keyword fill's cursor row adds
// a row to keyword_pause_cursor_writes with the event the cursor names and now(), the start of the
// transaction that wrote it. A batch begins that transaction after it asked the pause
// (textversions' batchesTo and filler.batch), so a write begun before a time was asked for before
// it, whatever the fill logged and whenever the test looked.
func keywordRecordCursorWrites(t *testing.T, database *store.Store) {
	t.Helper()
	if _, err := database.Pool.Exec(t.Context(), `
		create table keyword_pause_cursor_writes (
			id bigint generated always as identity primary key,
			event_id bigint not null,
			began timestamptz not null
		);
		create function keyword_pause_cursor_write() returns trigger language plpgsql as $$
		begin
			insert into keyword_pause_cursor_writes (event_id, began) values (new.event_id, now());
			return new;
		end
		$$;
		create trigger keyword_pause_cursor_write after insert or update on search_text_versions_fill
			for each row execute function keyword_pause_cursor_write();
	`); err != nil {
		t.Fatalf("record the writes of the keyword fill's cursor: %v", err)
	}
}

// keywordCursorWrite is one write of the keyword fill's cursor keywordRecordCursorWrites recorded:
// its id, the event the cursor named after it, and the start of the transaction that wrote it.
type keywordCursorWrite struct {
	id    int64
	event int64
	began time.Time
}

// keywordCursorWritesAfter is every committed write of the keyword fill's cursor recorded with an
// id above after, in id order.
func keywordCursorWritesAfter(t *testing.T, database *store.Store, after int64) []keywordCursorWrite {
	t.Helper()
	rows, err := database.Pool.Query(t.Context(),
		`select id, event_id, began from keyword_pause_cursor_writes where id > $1 order by id`, after)
	if err != nil {
		t.Fatalf("read the writes of the keyword fill's cursor: %v", err)
	}
	writes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (keywordCursorWrite, error) {
		var write keywordCursorWrite
		err := row.Scan(&write.id, &write.event, &write.began)
		return write, err
	})
	if err != nil {
		t.Fatalf("read the writes of the keyword fill's cursor: %v", err)
	}
	return writes
}

// keywordCountLines is how many of lines start with prefix.
func keywordCountLines(lines []string, prefix string) int {
	count := 0
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

// keywordAwaitCursor waits, through the shared wait, until the fill's cursor reaches id, waking the
// fill at each ask.
func keywordAwaitCursor(t *testing.T, database *store.Store, id int64, wake func()) {
	t.Helper()
	testwait.Until(t, fmt.Sprintf("the fill's cursor to reach event %d", id), func() (int64, bool) {
		cursor := textversionstest.Cursor(t, database)
		if cursor >= id {
			return cursor, true
		}
		wake()
		return cursor, false
	})
}

// keywordPausedWhy is why the pause row's spans are spans: a paused fill is the absence of its
// writes, which no state of the fill shows until it continues; the fill and the server run on the
// wall clock with real Postgres I/O, out of synctest's reach; and the one later action that ends
// the pause, the release, is what the row times the pause against.
const keywordPausedWhy = "a paused fill is the absence of its writes, which no state shows until it continues; it runs on the wall clock with real Postgres I/O, out of synctest's reach; and the one later action, the release, is what the pause is timed against"

// seedKeywordBacklog writes project ACME (textversionstest.SeedProject) and its issues ACME-1
// onwards (textversionstest.WriteIssue) until the log holds n events or more, and answers each
// event with the versions it starts.
func seedKeywordBacklog(t *testing.T, database *store.Store, broker *events.Broker, n int) []textversionstest.Written {
	t.Helper()
	textversionstest.SeedProject(t, database)
	var seeded []textversionstest.Written
	for number := 1; len(seeded) < n; number++ {
		seeded = append(seeded, textversionstest.WriteIssue(t, database, broker, number)...)
	}
	return seeded
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
	created := wantStatus(t, "create the round's issue", answer("POST /api/v1/issues", dispatchRequest(t, h, http.MethodPost,
		"/api/v1/issues", map[string]string{
			"project": project, "title": fmt.Sprintf("Rotate the signing keys of round %d", round),
			"spec": fmt.Sprintf("# Round %d\n\nThe verifiers read the old key until the audit.\n", round),
		}, "alice"), true), http.StatusCreated)
	issue := decodeBody[struct {
		Key string `json:"key"`
	}](t, created).Key
	answer("GET /api/v1/issues/{key}", dispatchRequest(t, h, http.MethodGet, "/api/v1/issues/"+issue, nil, "alice"), false)

	comment := decodeBody[model.Comment](t, wantStatus(t, "comment on the round's issue", answer("POST /api/v1/issues/{key}/comments",
		dispatchRequest(t, h, http.MethodPost, "/api/v1/issues/"+issue+"/comments",
			map[string]any{"body": fmt.Sprintf("Round %d: the verifiers read the old key until Monday.", round)}, "alice"), true), http.StatusCreated))
	answer("PATCH /api/v1/comments/{id}", dispatchRequest(t, h, http.MethodPatch, "/api/v1/comments/"+comment.ID,
		map[string]any{"body": fmt.Sprintf("Round %d: the verifiers read the old key until Tuesday.", round)}, "alice"), true)
	answer("GET /api/v1/comments/{id}", dispatchRequest(t, h, http.MethodGet, "/api/v1/comments/"+comment.ID, nil, "alice"), false)

	ask := decodeBody[model.Ask](t, wantStatus(t, "ask on the round's issue", answer("POST /api/v1/issues/{key}/asks",
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
// locked, which a comment's write does not read, and fails when it does not answer 201. Its wait
// is bounded by the test binary's deadline alone (testwait.Receive): a write that read
// message_deliveries would wait for the lock the row holds until then.
func keywordWriteUnderLock(t *testing.T, server keywordServer, issue string, n int) {
	t.Helper()
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		answered <- dispatchRequest(t, server.handler, http.MethodPost, "/api/v1/issues/"+issue+"/comments",
			map[string]any{"body": fmt.Sprintf("Note %d, written while the answers read waits.", n)}, "alice")
	}()
	if response := testwait.Receive(t, fmt.Sprintf("comment %d, written under the lock on message_deliveries, to answer", n), answered); response.Code != http.StatusCreated {
		t.Fatalf("write comment %d under the lock: status=%d body=%s", n, response.Code, response.Body.String())
	}
}
