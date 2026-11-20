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

// The life of a held text past its hold (INFRA-3231's R5, Decision 4): a reply to a held message
// once it posts, a post its document refuses, two held replies of one writer, a text that waits
// on a held parent, a kept write's one run, the issue a held issue's answer names, a scoped
// token's write beside a held text, and a reply sent again on an answered delivery.

// asKeptWriter writes as the rig's writer session through a client that reads the answer of a
// write Dispatch keeps beside a held text (contracts.AcceptsKeptWriteHeader), as the agents'
// DispatchClient does on every write.
func (rig *reviewRig) asKeptWriter(method, target string, body map[string]any) *httptest.ResponseRecorder {
	rig.t.Helper()
	return rig.asWriterMarked(contracts.AcceptsKeptWriteHeader, method, target, body)
}

// postHeldAsWritten is the writer's post of the held text id as written.
func (rig *reviewRig) postHeldAsWritten(id string) {
	rig.t.Helper()
	if posted := rig.asWriter(http.MethodPut, "/api/v1/held/"+id+"/state", map[string]any{"state": "posted", "reason": "As written."}); posted.Code != http.StatusOK {
		rig.t.Fatalf("the writer's post of %s: %d %s", id, posted.Code, posted.Body.String())
	}
}

// A held message to a session posts under the id its hold reserved, and the recipient's reply to
// that delivery is a reply like any other: the held row under the same id, posted, is no frame
// for the writer to answer any more. Before, the reply was taken as an answer to the held text's
// findings frame and refused 403 REPLY_FORBIDDEN, since the recipient is not the writer.
func TestARecipientRepliesToAHeldMessageOnceItPosts(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Messages: ReviewHold, WriterWindow: time.Minute}, nil)
	const recipient = "s-recipient"
	rig.listener.start(recipient)
	held := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/messages", map[string]any{
		"body": "Status\nWe spin up the node. " + vacuousPhrase, "target": "session:" + recipient, "delivery": "btw",
	}), "the agent's message to a session")
	rig.waitHeld(held.ID, heldAwaitingWriter)
	rig.postHeldAsWritten(held.ID)
	rig.waitHeld(held.ID, heldPosted)
	pollUntil(t, 10*time.Second, "the posted message's delivery to "+recipient, func() bool {
		return countRows(t, rig.database, `select count(*) from message_deliveries where message_id::text = $1 and session_id = $2`, held.ID, recipient) == 1
	})
	reply := rig.asSession(recipient, http.MethodPost, "/api/v1/messages/"+held.ID+"/reply", map[string]any{
		"attempt": 1, "body": "The node answers on its port now.",
	})
	if reply.Code != http.StatusCreated {
		t.Fatalf("the recipient's reply to the posted message: %d %s, want 201", reply.Code, reply.Body.String())
	}
	if id := decodeBody[heldWriteAnswer](t, reply).ID; id == "" || id == held.ID {
		t.Fatalf("the recipient's reply answered id %q, want one of its own", id)
	}
	if answer := rig.heldRow(held.ID).answer; answer != "" {
		t.Fatalf("the recipient's reply was kept as the writer's answer %q", answer)
	}
}

// A person signed in at a browser learns nothing of a held text: their write on a held issue's or
// a held comment's path, with the kept-write header or without, is the route's own, answered as
// the same write on an id nobody holds is answered, naming no held text, and nothing is kept.
func TestAPersonsWriteOnAHeldTextIsAnsweredAsIfNoTextWereThere(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Issues: ReviewHold, Comments: ReviewHold, WriterWindow: time.Hour}, nil)
	heldComment, nobodysComment := "0f6a4c1e-6d2b-4b8e-9c55-2a1d7e3f9b10", "5b9e2d7c-3a41-4f6e-8d02-7c1b9e4a6f23"
	for id, kind := range map[string]string{"RVW-900": "issue", heldComment: "comment"} {
		if _, err := rig.database.Pool.Exec(context.Background(), `
			insert into held_texts (id, kind, route, request, original, actor, writer, reader, project, state, deadline)
			values ($1, $2, 'POST /fixture', '{}', '{}', '{"kind":"session","id":"s-writer"}', 's-writer', 'alice', 'RVW',
				'awaiting_writer', now() + interval '1 hour')`, id, kind); err != nil {
			t.Fatalf("hold the fixture %s: %v", id, err)
		}
	}
	send := func(method, target string, body map[string]any, readsKept bool) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(method, target, bytes.NewReader(data))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Dispatch-User", "alice")
		if readsKept {
			request.Header.Set(contracts.AcceptsKeptWriteHeader, "1")
		}
		response := httptest.NewRecorder()
		rig.handler.ServeHTTP(response, request)
		return response
	}
	for _, write := range []struct {
		method, held, nobodys string
		body                  map[string]any
	}{
		{http.MethodPatch, "/api/v1/issues/RVW-900", "/api/v1/issues/RVW-901", map[string]any{"title": "Retitled by alice"}},
		{http.MethodPost, "/api/v1/comments/" + heldComment + "/resolve", "/api/v1/comments/" + nobodysComment + "/resolve", map[string]any{}},
	} {
		want := send(write.method, write.nobodys, write.body, false)
		for _, readsKept := range []bool{false, true} {
			got := send(write.method, write.held, write.body, readsKept)
			if got.Code != want.Code || strings.Contains(got.Body.String(), "HELD_TEXT") || strings.Contains(got.Body.String(), `"held"`) {
				t.Fatalf("alice's %s %s (kept-write header %v): %d %s, want %d as for an id nobody holds",
					write.method, write.held, readsKept, got.Code, got.Body.String(), want.Code)
			}
		}
	}
	if kept := countRows(t, rig.database, `select count(*) from held_texts where kind = 'request'`); kept != 0 {
		t.Fatalf("%d of alice's writes kept beside a held text, want none", kept)
	}
}

// A held comment anchored by a quote its issue's spec loses while the comment is held is refused
// at its post, as the comment itself would be refused now: the held text fails at once, saying
// why, and its writer is told. Before, the document's refusal reached the post raw, read as no
// refusal, and the sweep took the post again every ten seconds for ever, with every text held
// beside it waiting.
func TestAHeldPostItsDocumentRefusesFailsAtOnce(t *testing.T) {
	rig := newReviewRig(t, holdComments(), nil)
	held := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/comments", map[string]any{
		"body":   "Status\nWe spin up the node. " + vacuousPhrase,
		"anchor": map[string]any{"artifact": "spec", "quote": "Seed paragraph"},
	}), "the anchored comment")
	rig.waitHeld(held.ID, heldAwaitingWriter)
	edited := dispatchRequest(t, rig.handler, http.MethodPost, "/api/v1/issues/"+rig.issue+"/artifacts/spec/edits", map[string]any{
		"ops":     []map[string]string{{"op": "replace", "find": "Seed paragraph here.", "with": "Another line entirely."}},
		"summary": "Reword the spec",
	}, "alice")
	if edited.Code != http.StatusOK {
		t.Fatalf("alice's edit of the spec: %d %s", edited.Code, edited.Body.String())
	}
	rig.postHeldAsWritten(held.ID)
	if failed := rig.waitHeld(held.ID, heldFailed); !strings.Contains(failed.detail, "the post was refused: ") ||
		!strings.HasSuffix(failed.detail, "; its writer was told") {
		t.Fatalf("the failed comment's detail reads %q", failed.detail)
	}
	if body := rig.waitOneFrameAbout(held.ID, "did not post"); !strings.Contains(body, "your held comment "+held.ID) {
		t.Fatalf("the failed comment's frame reads %q", body)
	}
	if slices.Contains(rig.commentIDs(), held.ID) {
		t.Fatal("alice reads the comment its document refused")
	}
}

// Two replies of one writer to one delivery, each held, post in the order the writer wrote them:
// the second waits on the first and is reviewed once the first posted, so the first posts as the
// delivery's reply under its own id, and the second as the follow-up it asked to be, threaded
// under the first. Before, the second, reviewed clean while the first waited on its writer, posted
// first as the delivery's reply, and the first then answered that stored reply and was lost,
// recorded posted.
func TestTwoHeldRepliesOfOneWriterPostInTheOrderWritten(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Messages: ReviewHold, WriterWindow: time.Minute}, nil)
	message := rig.messageToWriter("Is it done?")
	first := rig.held(rig.asWriter(http.MethodPost, "/api/v1/messages/"+message+"/reply", map[string]any{
		"attempt": 1, "body": "Status\nWe spin up the node. " + vacuousPhrase,
	}), "the first reply")
	rig.waitHeld(first.ID, heldAwaitingWriter)
	second := rig.held(rig.asWriter(http.MethodPost, "/api/v1/messages/"+message+"/reply?follow_up=true", map[string]any{
		"attempt": 1, "body": "The node answers on its port now.",
	}), "the second reply")
	if second.Held.WaitsOn != first.ID {
		t.Fatalf("the second reply waits on %q, want the first, %s", second.Held.WaitsOn, first.ID)
	}
	time.Sleep(300 * time.Millisecond)
	if row := rig.heldRow(second.ID); row.state != heldReviewing {
		t.Fatalf("the second reply is %s (%s) before the first posted, want it still in review", row.state, row.detail)
	}
	rig.postHeldAsWritten(first.ID)
	if posted := rig.waitHeld(second.ID, heldPosted); posted.outcome != reviewPassed {
		t.Fatalf("the second reply posted %s (%s), want passed", posted.outcome, posted.detail)
	}
	var answeredBy string
	if err := rig.database.Pool.QueryRow(context.Background(), `
		select coalesce(reply_id::text, '') from message_deliveries where message_id::text = $1 and attempt = 1`, message).Scan(&answeredBy); err != nil {
		t.Fatal(err)
	}
	if answeredBy != first.ID {
		t.Fatalf("the delivery is answered by %q, want the first reply, %s", answeredBy, first.ID)
	}
	var threadedUnder string
	if err := rig.database.Pool.QueryRow(context.Background(), `
		select coalesce(in_reply_to::text, '') from messages where id::text = $1`, second.ID).Scan(&threadedUnder); err != nil {
		t.Fatalf("read the second reply's message: %v", err)
	}
	if threadedUnder != first.ID {
		t.Fatalf("the second reply answers %q, want the first reply, %s", threadedUnder, first.ID)
	}
}

// A child issue held beside its held parent is not reviewed while the parent is held, since its
// record would miss the parent, and is reviewed once the parent posts, posting passed. Before, its
// review ran at once, could not read the parent, and posted it unreviewed.
func TestAHeldTextWaitingOnAHeldParentIsReviewedOnceTheParentPosts(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Issues: ReviewHold, WriterWindow: time.Minute}, nil)
	const childTitle = "Held child of a held parent"
	parent := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues", map[string]any{
		"project": "RVW", "title": "Held parent", "spec": vacuousPhrase,
	}), "the held parent")
	rig.waitHeld(parent.ID, heldAwaitingWriter)
	child := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues", map[string]any{
		"project": "RVW", "title": childTitle, "parent": parent.ID,
	}), "the child")
	time.Sleep(500 * time.Millisecond)
	if row := rig.heldRow(child.ID); row.state != heldReviewing || row.outcome != "" {
		t.Fatalf("the child is %s %q (%s) while its parent is held, want it in review, undecided", row.state, row.outcome, row.detail)
	}
	if calls := rig.reviewCalls(childTitle); calls != 0 {
		t.Fatalf("the reviewer read the child %d times while its parent was held, want none", calls)
	}
	rig.postHeldAsWritten(parent.ID)
	if posted := rig.waitHeld(child.ID, heldPosted); posted.outcome != reviewPassed {
		t.Fatalf("the child posted %s (%s), want passed", posted.outcome, posted.detail)
	}
	if calls := rig.reviewCalls(childTitle); calls != 1 {
		t.Fatalf("the reviewer read the child %d times, want once", calls)
	}
}

// A kept write marks its held row posted in the transaction of the write itself. A post of the
// write whose row another post has marked already - one that took it past staleClaim while the
// first ran - writes nothing: its route's commit is refused and the write rolls back. Before, the
// write committed first and the row was marked after it in a transaction of its own, which found
// it posted and changed nothing, so the write ran twice. A post of the row still waiting runs the
// write and leaves the row posted.
func TestAKeptWriteWhoseHeldRowPostedRunsNoSecondTime(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Issues: ReviewHold, WriterWindow: time.Hour}, nil)
	ctx := context.Background()
	// The rig's issue stands in for a held issue, so a write naming it is kept, and its replay
	// writes a row that exists.
	if _, err := rig.database.Pool.Exec(ctx, `
		insert into held_texts (id, kind, route, request, original, actor, writer, reader, project, state, deadline)
		values ($1, 'issue', 'POST /fixture', '{}', '{}', '{"kind":"session","id":"s-writer"}', 's-writer', 'alice', 'RVW',
			'awaiting_writer', now() + interval '1 hour')`, rig.issue); err != nil {
		t.Fatalf("hold the fixture %s: %v", rig.issue, err)
	}
	kept := rig.held(rig.asKeptWriter(http.MethodPatch, "/api/v1/issues/"+rig.issue, map[string]any{"labels": []string{"kept"}}), "the kept update")
	labels := func() []string {
		var labels []string
		if err := rig.database.Pool.QueryRow(ctx, `select labels from issues where key = $1`, rig.issue).Scan(&labels); err != nil {
			t.Fatal(err)
		}
		return labels
	}
	if _, err := rig.database.Pool.Exec(ctx, `
		update held_texts set state = 'posted', posted_at = now() where id = $1`, kept.ID); err != nil {
		t.Fatal(err)
	}
	if err := rig.served.runHeldPost(rig.served.postContext(), rig.heldRow(kept.ID)); err == nil {
		t.Fatal("the post of a kept write another post posted answered no error")
	}
	if slices.Contains(labels(), "kept") {
		t.Fatalf("the kept write ran again, though its row was posted: labels %v", labels())
	}
	if _, err := rig.database.Pool.Exec(ctx, `
		update held_texts set state = 'posting', posted_at = null where id = $1`, kept.ID); err != nil {
		t.Fatal(err)
	}
	if err := rig.served.runHeldPost(rig.served.postContext(), rig.heldRow(kept.ID)); err != nil {
		t.Fatalf("the post of the kept write: %v", err)
	}
	if !slices.Contains(labels(), "kept") {
		t.Fatalf("the kept write did not run: labels %v", labels())
	}
	if row := rig.heldRow(kept.ID); row.state != heldPosted {
		t.Fatalf("the kept write's row is %s after its post, want posted", row.state)
	}
}

// A held issue's answer reads as the issue its post files: in triage, as every create files it,
// and under the assignee its post writes, for an issue and for a child of a held issue, whose
// assignee is its parent's. Before, it read open, with no assignee.
func TestAHeldIssuesAnswerReadsTriageAndTheAssigneeItPostsUnder(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Issues: ReviewHold, WriterWindow: time.Minute}, nil)
	hold := func(body map[string]any) (heldWriteAnswer, model.Issue) {
		t.Helper()
		response := rig.asWriter(http.MethodPost, "/api/v1/issues", body)
		held := rig.held(response, "the held issue "+body["title"].(string))
		answer := decodeBody[model.Issue](t, response)
		if answer.Status != model.IssueStatusTriage || answer.Assignee == nil || *answer.Assignee == "" {
			t.Fatalf("the held issue answered status %q assignee %v, want triage and an assignee", answer.Status, answer.Assignee)
		}
		return held, answer
	}
	parent, parentAnswer := hold(map[string]any{"project": "RVW", "title": "Held issue for its assignee", "spec": vacuousPhrase})
	rig.waitHeld(parent.ID, heldAwaitingWriter)
	child, childAnswer := hold(map[string]any{"project": "RVW", "title": "Held child for its assignee", "parent": parent.ID})
	rig.postHeldAsWritten(parent.ID)
	rig.waitHeld(child.ID, heldPosted)
	for key, answer := range map[string]model.Issue{parent.ID: parentAnswer, child.ID: childAnswer} {
		posted := decodeBody[model.Issue](t, dispatchRequest(t, rig.handler, http.MethodGet, "/api/v1/issues/"+key, nil, "alice"))
		if posted.Status != answer.Status || posted.Assignee == nil || *posted.Assignee != *answer.Assignee {
			t.Fatalf("%s posted with status %q assignee %v; its held answer read %q %q", key, posted.Status, posted.Assignee, answer.Status, *answer.Assignee)
		}
	}
}

// A project-scoped token's write on a held issue of its own project reaches the hold, though no
// issues row holds the key yet: it is kept beside the held issue for a client that reads that
// answer, and refused 409 HELD_TEXT for one that does not. Its write on a held issue of another
// project is refused PROJECT_SCOPE, naming that project, as a write on any issue of another
// project is. Before, the scope check found no row for the held key and refused the token's own
// write 403 PROJECT_SCOPE.
func TestAProjectScopedTokenWritesBesideAHeldTextOfItsOwnProject(t *testing.T) {
	rig := newBrokerTokenRigOn(t, testServerOptions{reviewer: NewReviewer(ReviewerConfig{Issues: ReviewHold})})
	caller := newBrokerCaller(t)
	const token = "a-token-writing-beside-a-held-issue"
	rig.broker.Says(token, liveNamedToken(caller.jkt))
	own, other := brokerTestProject+"-900", "OTH-900"
	for id, project := range map[string]string{own: brokerTestProject, other: "OTH"} {
		if _, err := rig.database.Pool.Exec(context.Background(), `
			insert into held_texts (id, kind, route, request, original, actor, writer, reader, project, state, deadline)
			values ($1, 'issue', 'POST /fixture', '{}', '{}', '{"kind":"session","id":"s-writer"}', 's-writer', 'alice', $2,
				'awaiting_writer', now() + interval '1 hour')`, id, project); err != nil {
			t.Fatalf("hold the fixture %s: %v", id, err)
		}
	}
	write := func(key string, readsKept bool) *httptest.ResponseRecorder {
		t.Helper()
		request := caller.presenting(t, http.MethodPatch, "/api/v1/issues/"+key, token, map[string]any{"title": "Retitled beside the hold"})
		if readsKept {
			request.Header.Set(contracts.AcceptsKeptWriteHeader, "1")
		}
		return rig.serve(request)
	}
	if kept := write(own, true); kept.Code != http.StatusAccepted || decodeBody[heldDeferred](t, kept).Held.WaitsOn != own {
		t.Fatalf("the scoped token's write on its own project's held issue: %d %s, want 202 waiting on %s", kept.Code, kept.Body.String(), own)
	}
	if refused := write(own, false); refused.Code != http.StatusConflict || decodeBody[heldTextError](t, refused).HeldID != own {
		t.Fatalf("the same write from a client that does not read a kept write: %d %s, want 409 HELD_TEXT", refused.Code, refused.Body.String())
	}
	refused := write(other, true)
	if refused.Code != http.StatusForbidden {
		t.Fatalf("the scoped token's write on another project's held issue: %d %s, want 403", refused.Code, refused.Body.String())
	}
	if refusal := decodeBody[refusalAnswer](t, refused); refusal.Code != codeProjectScope || !strings.Contains(refusal.Error, "OTH") {
		t.Fatalf("the refusal = %+v, want %s naming OTH", refusal, codeProjectScope)
	}
}

// A reply sent again on a delivery its first reply already answered writes nothing and answers
// the stored reply, 200, as it does with the reviewer off, on the reply route of a delivered
// message and of a delivered mention: its dry run passes that answer back, and no text is held.
// Before, the dry run ended as a pass on that route, and the repeat was held under an id of its
// own, to answer the stored reply at its post.
func TestAHeldReplySentAgainOnAnAnsweredDeliveryAnswersTheStoredReply(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Comments: ReviewHold, Messages: ReviewHold, WriterWindow: time.Minute}, nil)
	for _, route := range []struct {
		pattern   string
		delivered func() string
	}{
		{"/api/v1/messages/{id}/reply", func() string { return "/api/v1/messages/" + rig.messageToWriter("Is it done?") + "/reply" }},
		{"/api/v1/comments/{id}/reply", func() string { return "/api/v1/comments/" + rig.mentionOfWriter("Is it done?") + "/reply" }},
	} {
		target := route.delivered()
		body := map[string]any{"attempt": 1, "body": "The node answers on its port now."}
		first := rig.held(rig.asWriter(http.MethodPost, target, body), "the first reply on "+route.pattern)
		rig.waitHeld(first.ID, heldPosted)
		before := countRows(t, rig.database, `select count(*) from held_texts`)
		again := rig.asWriter(http.MethodPost, target, body)
		if again.Code != http.StatusOK || strings.Contains(again.Body.String(), `"held"`) {
			t.Fatalf("the reply sent again on %s: %d %s, want 200 and the stored reply", route.pattern, again.Code, again.Body.String())
		}
		if id := decodeBody[heldWriteAnswer](t, again).ID; id != first.ID {
			t.Fatalf("the reply sent again on %s answered %q, want the stored reply %s", route.pattern, id, first.ID)
		}
		if after := countRows(t, rig.database, `select count(*) from held_texts`); after != before {
			t.Fatalf("the reply sent again on %s held %d texts, want none", route.pattern, after-before)
		}
	}
}
