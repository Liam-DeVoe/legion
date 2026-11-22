package api

import (
	"cmp"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// A held write's answer is the answer its post would give, whole (INFRA-3231's R5): each test
// below makes one kind of write twice, held, and as the copy its writer's person typed, which
// posts at once with no hold (Decision 26) and gets the answer its route gives every write it
// posts. It compares the two answers as each went on the wire, every field and every value, less
// the held object and what each write makes its own (requireHeldAnswerAsPosted), so a field a held
// answer leaves out, or states otherwise than its post's answer, fails its kind's test, whichever
// field it is. A decision block's hold answers no write: the document write that holds it has
// answered before its settlement opens the block (decisionBlockReviewer.AskBlockOpened).

// heldAnswerPostOnly are the fields of a write's answer that only its post makes, which a held
// answer leaves out, each a dotted path into the answer: an anchor's mark id, the ask.opened
// event's sequence, an issue's rank, its document, its event sequence and the revision of its own
// relations, and the deliveries a post sends.
var heldAnswerPostOnly = []string{
	"anchor.mark_id", "opened_event_id", "rank", "primary_artifact_id", "last_seq", "advances_revision", "deliveries",
}

// heldAnswerOwn are the fields each of two writes fills with a value of its own: when it was
// written, and an issue's number.
var heldAnswerOwn = []string{"created_at", "updated_at", "number"}

// requireHeldAnswerAsPosted fails t unless held, a held write's answer, and posted, the answer of
// the same write posted at once, carry the same status and the same fields with the same values,
// but for the held object, the fields only a post makes (heldAnswerPostOnly), the values each
// write fills with its own (heldAnswerOwn), which both must carry, and the ids heldIDs and
// postedIDs map, each read as the name it maps to: the write's own id, the text it answers.
func requireHeldAnswerAsPosted(t *testing.T, what string, held, posted *httptest.ResponseRecorder, heldIDs, postedIDs map[string]string) {
	t.Helper()
	requireHeldAnswerAsPostedLess(t, what, held, posted, heldIDs, postedIDs, nil)
}

// requireHeldAnswerAsPostedLess is requireHeldAnswerAsPosted with postOnly, more dotted paths that
// only this write's post makes, left out of both answers.
func requireHeldAnswerAsPostedLess(t *testing.T, what string, held, posted *httptest.ResponseRecorder, heldIDs, postedIDs map[string]string, postOnly []string) {
	t.Helper()
	if strings.Contains(posted.Body.String(), `"held"`) {
		t.Fatalf("%s, posted at once: %d %s, want no held object", what, posted.Code, posted.Body.String())
	}
	heldAnswer, postedAnswer := comparedAnswer(t, held, heldIDs, postOnly), comparedAnswer(t, posted, postedIDs, postOnly)
	if held.Code != posted.Code || heldAnswer != postedAnswer {
		t.Errorf("%s: the held answer is not the posted one:\nheld   %d %s\nposted %d %s", what, held.Code, heldAnswer, posted.Code, postedAnswer)
	}
}

// comparedAnswer is response's answer as requireHeldAnswerAsPosted compares it, less postOnly.
func comparedAnswer(t *testing.T, response *httptest.ResponseRecorder, ids map[string]string, postOnly []string) string {
	t.Helper()
	var answer map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode the answer %s: %v", response.Body.String(), err)
	}
	delete(answer, "held")
	for _, path := range append(slices.Clone(heldAnswerPostOnly), postOnly...) {
		withoutField(answer, strings.Split(path, "."))
	}
	for _, field := range heldAnswerOwn {
		if _, carried := answer[field]; carried {
			answer[field] = "its own"
		}
	}
	return asJSON(t, renamedIDs(answer, ids))
}

// withoutField deletes the field at path from object, where object holds it.
func withoutField(object map[string]any, path []string) {
	if len(path) == 1 {
		delete(object, path[0])
		return
	}
	if child, isObject := object[path[0]].(map[string]any); isObject {
		withoutField(child, path[1:])
	}
}

// renamedIDs is node with each string in it that ids maps read as the name it maps to.
func renamedIDs(node any, ids map[string]string) any {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			value[key] = renamedIDs(child, ids)
		}
	case []any:
		for index, child := range value {
			value[index] = renamedIDs(child, ids)
		}
	case string:
		if name, named := ids[value]; named {
			return name
		}
	}
	return node
}

// postedID is the id of a write posted at once: its id, an issue's key.
func postedID(t *testing.T, posted *httptest.ResponseRecorder) string {
	t.Helper()
	answer := decodeBody[heldWriteAnswer](t, posted)
	return cmp.Or(answer.ID, answer.Key)
}

// A held ask's whole answer is its post's: the question, the options, the anchor less the mark id,
// the document the anchor quotes, whom the ask waits on, and the advice.
func TestAHeldAsksWholeAnswerIsItsPostsAnswer(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Asks: ReviewHold, WriterWindow: time.Minute}, nil)
	rig.settleTermStats("RVW")
	asks := "/api/v1/issues/" + rig.issue + "/asks"
	ask := map[string]any{
		"question": "Ship the reviewer?\n" + vacuousPhrase,
		"options":  []map[string]string{{"label": "Ship it", "description": "Release it today."}, {"label": "Do not file", "description": "Wait."}},
		"multiple": true, "urgency": "high",
		"anchor": map[string]any{"artifact": specOf(t, rig.database, rig.issue), "quote": "Seed paragraph"},
	}
	held := rig.asWriter(http.MethodPost, asks, ask)
	id := rig.held(held, "the held ask").ID
	rig.waitHeld(id, heldAwaitingWriter)
	posted := rig.asWriterTyped(http.MethodPost, asks, ask)
	requireHeldAnswerAsPosted(t, "the ask", held, posted, map[string]string{id: "the ask"}, map[string]string{postedID(t, posted): "the ask"})
}

// A held comment's whole answer is its post's: a comment on a quote, its anchor less the mark id;
// a suggestion, what it suggests; and a reply on an ask, its turn and whom the ask waits on once
// it posts (ask_waiting_on); each with its advice.
func TestAHeldCommentsWholeAnswerIsItsPostsAnswer(t *testing.T) {
	rig := newReviewRig(t, holdComments(), nil)
	asked := rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/asks", map[string]any{"question": "Which port should the node use?"})
	if asked.Code != http.StatusCreated {
		t.Fatalf("the writer's ask: %d %s", asked.Code, asked.Body.String())
	}
	quote := map[string]any{"artifact": specOf(t, rig.database, rig.issue), "quote": "Seed paragraph"}
	comments := "/api/v1/issues/" + rig.issue + "/comments"
	for _, comment := range []struct {
		name string
		body map[string]any
	}{
		{"on a quote", map[string]any{"body": "Status\nWe spin up the node. " + vacuousPhrase, "anchor": quote}},
		{"a suggestion", map[string]any{"body": "Status\nName the seed. " + vacuousPhrase, "anchor": quote, "suggestion": map[string]any{"replace_with": "Seed text"}}},
		{"on an ask", map[string]any{"body": "Status\nThe node answers on 8080. " + vacuousPhrase, "ask_id": decodeBody[model.Ask](t, asked).ID}},
	} {
		held := rig.asWriter(http.MethodPost, comments, comment.body)
		id := rig.held(held, "the held comment "+comment.name).ID
		rig.waitHeld(id, heldAwaitingWriter)
		posted := rig.asWriterTyped(http.MethodPost, comments, comment.body)
		requireHeldAnswerAsPosted(t, "the comment "+comment.name, held, posted, map[string]string{id: "the comment"}, map[string]string{postedID(t, posted): "the comment"})
		// The next comment would wait for its turn behind this one and count it in its advice, as
		// its post finds it posted (TestAHeldTextWaitingForItsTurnAnswersAsItsPost): posted now, it
		// is counted by the held answer and its typed copy alike.
		rig.postedAsWritten(id)
	}
}

// A held message's whole answer is its post's: the message it replies to, the block it names, and
// its advice.
func TestAHeldMessagesWholeAnswerIsItsPostsAnswer(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Messages: ReviewHold, WriterWindow: time.Minute}, nil)
	messages := "/api/v1/issues/" + rig.issue + "/messages"
	root := dispatchRequest(t, rig.handler, http.MethodPost, messages, map[string]any{"body": "One.\n\nTwo."}, "alice")
	if root.Code != http.StatusCreated {
		t.Fatalf("alice's message: %d %s", root.Code, root.Body.String())
	}
	message := map[string]any{
		"body": "Status\nWe spin up the node. " + vacuousPhrase, "in_reply_to": decodeBody[model.Message](t, root).ID, "in_reply_to_block": "b2",
	}
	held := rig.asWriter(http.MethodPost, messages, message)
	id := rig.held(held, "the held message").ID
	rig.waitHeld(id, heldAwaitingWriter)
	posted := rig.asWriterTyped(http.MethodPost, messages, message)
	requireHeldAnswerAsPosted(t, "the message", held, posted, map[string]string{id: "the message"}, map[string]string{postedID(t, posted): "the message"})
}

// A held issue's whole answer is its post's: its title, parent, labels, priority, assignee,
// blockers, repository and priorities it advances, each as the posted create answers it, in
// triage, with its advice.
func TestAHeldIssuesWholeAnswerIsItsPostsAnswer(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Issues: ReviewHold, WriterWindow: time.Minute}, nil)
	seedPeople(t, rig.database, "bob")
	createTestPriority(t, rig.handler, map[string]any{"id": "ship-the-daemon", "title": "Ship the daemon", "owner": "alice"}, "alice")
	blocker := requireCreatedIssue(t, createIssueRequest(t, rig.handler, map[string]any{
		"project": "RVW", "title": "The exporter's transport of record", "spec": "The exporter sends its batches over a queue.",
	}))
	rig.settleTermStats("RVW")
	create := map[string]any{
		"project": "RVW", "title": "Astrolabe gnomon reseat offsets",
		"spec":   "The astrolabe reads a constant offset after each gnomon reseat.\n\n" + vacuousPhrase,
		"parent": rig.issue, "labels": []string{"reviewer", "astrolabe"}, "priority": 2, "assignee": "bob",
		"blocked_by": []string{blocker.Key}, "repo": "acme/widgets", "advances": []string{"ship-the-daemon"},
	}
	held := rig.asWriter(http.MethodPost, "/api/v1/issues", create)
	key := rig.held(held, "the held issue").ID
	rig.waitHeld(key, heldAwaitingWriter)
	posted := rig.asWriterTyped(http.MethodPost, "/api/v1/issues", create)
	requireHeldAnswerAsPosted(t, "the issue", held, posted, map[string]string{key: "the issue"}, map[string]string{postedID(t, posted): "the issue"})
}

// A held reply to a mention in an ask's thread answers as its post's answer does: the ask it joins,
// the comment it answers, its turn and whom the ask waits on once it posts. Sent again while held,
// it answers as the route answers a reply sent again on an answered attempt: the reply as it posts,
// with no ask_waiting_on, which names whom the ask waits on once a reply is its newest.
func TestAHeldCommentRepliesWholeAnswerIsItsPostsAnswer(t *testing.T) {
	rig := newReviewRig(t, holdComments(), nil)
	asked := rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/asks", map[string]any{"question": "Which port should the node use?"})
	if asked.Code != http.StatusCreated {
		t.Fatalf("the writer's ask: %d %s", asked.Code, asked.Body.String())
	}
	ask := decodeBody[model.Ask](t, asked).ID
	mention := func(body string) string {
		t.Helper()
		comment := decodeMentionedComment(t, postMentionedComment(t, rig.handler, rig.issue, map[string]any{
			"body": body, "ask_id": ask, "mentions": []map[string]any{{"target": "session:" + heldWriter}},
		}))
		settledCommentDeliveries(t, rig.database, comment.ID)
		return comment.ID
	}
	first, second := mention("Is port 8080 free?"), mention("Is port 8081 free?")
	reply := map[string]any{"body": "Status\nThe node answers on 8080. " + vacuousPhrase, "attempt": 1}
	held := rig.asWriter(http.MethodPost, "/api/v1/comments/"+first+"/reply", reply)
	id := rig.held(held, "the held reply").ID
	rig.waitHeld(id, heldAwaitingWriter)
	posted := rig.asWriterTyped(http.MethodPost, "/api/v1/comments/"+second+"/reply", reply)
	heldIDs := map[string]string{id: "the reply", first: "the mention"}
	postedIDs := map[string]string{postedID(t, posted): "the reply", second: "the mention"}
	requireHeldAnswerAsPosted(t, "the reply", held, posted, heldIDs, postedIDs)
	requireHeldAnswerAsPosted(t, "the reply sent again",
		rig.asWriter(http.MethodPost, "/api/v1/comments/"+first+"/reply", reply),
		rig.asWriterTyped(http.MethodPost, "/api/v1/comments/"+second+"/reply", reply), heldIDs, postedIDs)
}

// A held reply to a message answers as its post's answer does: the message it answers and its
// target; sent again while held, as the route answers a reply sent again on an answered attempt,
// marked duplicate; and a held follow-up once the reply posted, the reply it is filed under.
func TestAHeldMessageRepliesWholeAnswerIsItsPostsAnswer(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Messages: ReviewHold, WriterWindow: time.Minute}, nil)
	first, second := rig.messageToWriter("Is the first node up?"), rig.messageToWriter("Is the second node up?")
	reply := map[string]any{"body": "Status\nWe spin up the node. " + vacuousPhrase, "attempt": 1}
	held := rig.asWriter(http.MethodPost, "/api/v1/messages/"+first+"/reply", reply)
	id := rig.held(held, "the held reply").ID
	rig.waitHeld(id, heldAwaitingWriter)
	posted := rig.asWriterTyped(http.MethodPost, "/api/v1/messages/"+second+"/reply", reply)
	heldIDs := map[string]string{id: "the reply", first: "the message"}
	postedIDs := map[string]string{postedID(t, posted): "the reply", second: "the message"}
	requireHeldAnswerAsPosted(t, "the reply", held, posted, heldIDs, postedIDs)
	requireHeldAnswerAsPosted(t, "the reply sent again",
		rig.asWriter(http.MethodPost, "/api/v1/messages/"+first+"/reply", reply),
		rig.asWriterTyped(http.MethodPost, "/api/v1/messages/"+second+"/reply", reply), heldIDs, postedIDs)
	rig.postedAsWritten(id)
	followUp := map[string]any{"body": "Status\nWe spin up the second node. " + vacuousPhrase, "attempt": 1}
	heldFollowUp := rig.asWriter(http.MethodPost, "/api/v1/messages/"+first+"/reply?follow_up=true", followUp)
	heldIDs[rig.held(heldFollowUp, "the held follow-up").ID] = "the follow-up"
	postedFollowUp := rig.asWriterTyped(http.MethodPost, "/api/v1/messages/"+second+"/reply?follow_up=true", followUp)
	postedIDs[postedID(t, postedFollowUp)] = "the follow-up"
	requireHeldAnswerAsPosted(t, "the follow-up", heldFollowUp, postedFollowUp, heldIDs, postedIDs)
}

// A held write beside a held parent runs no dry run before that parent posts, and its answer is
// still its post's answer, whole, as each test below holds it: the parent is held, the write is
// held beside it, the parent posts and the write waits on its writer, and the writer's typed copy
// of the write, which posts at once once the parent is posted, gets the answer the held one must
// equal. Whom it waits on, its thread and its advice are read off the held texts it waits on, as
// its post will find them once they have posted (heldBeside).

// A held ask on a held issue answers as its post's answer does: whom it waits on, the assignee
// the issue's post writes, and the advice of the issue the issue's post files, in triage.
func TestAHeldAskOnAHeldIssueAnswersAsItsPost(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Issues: ReviewHold, Asks: ReviewHold, WriterWindow: time.Minute}, nil)
	rig.settleTermStats("RVW")
	issue := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues", map[string]any{
		"project": "RVW", "title": "Astrolabe gnomon reseat offsets",
		"spec": "The astrolabe reads a constant offset after each gnomon reseat.\n\n" + vacuousPhrase,
	}), "the held issue").ID
	asks := "/api/v1/issues/" + issue + "/asks"
	ask := map[string]any{
		"question": "Reseat the gnomon before the next reading?\n" + vacuousPhrase,
		"options":  []map[string]string{{"label": "Reseat it", "description": "Reseat it today."}, {"label": "Wait", "description": "Read first."}},
		"urgency":  "high",
	}
	held := rig.asWriter(http.MethodPost, asks, ask)
	id := rig.held(held, "the held ask on the held issue").ID
	rig.postedAsWritten(issue)
	rig.waitHeld(id, heldAwaitingWriter)
	posted := rig.asWriterTyped(http.MethodPost, asks, ask)
	requireHeldAnswerAsPosted(t, "the ask on the held issue", held, posted, map[string]string{id: "the ask"}, map[string]string{postedID(t, posted): "the ask"})
}

// A held comment that answers a held ask answers as its post's answer does: the ask it joins, its
// turn, whom the ask waits on once it posts (ask_waiting_on), and the advice of its issue once the
// ask has posted, the ask among the session's open asks and counted among its writes.
func TestAHeldCommentOnAHeldAskAnswersAsItsPost(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Asks: ReviewHold, Comments: ReviewHold, WriterWindow: time.Minute}, nil)
	ask := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues/"+rig.issue+"/asks", map[string]any{
		"question": "Which port should the node use?\n" + vacuousPhrase,
	}), "the held ask").ID
	comments := "/api/v1/issues/" + rig.issue + "/comments"
	comment := map[string]any{"body": "Status\nThe node answers on 8080. " + vacuousPhrase, "ask_id": ask}
	held := rig.asWriter(http.MethodPost, comments, comment)
	id := rig.held(held, "the held comment on the held ask").ID
	rig.postedAsWritten(ask)
	rig.waitHeld(id, heldAwaitingWriter)
	posted := rig.asWriterTyped(http.MethodPost, comments, comment)
	requireHeldAnswerAsPosted(t, "the comment on the held ask", held, posted, map[string]string{id: "the comment"}, map[string]string{postedID(t, posted): "the comment"})
}

// A held reply to a held comment answers as its post's answer does: the comment it answers, the
// head of that comment's thread as its reply_to, and the advice of its issue once that comment
// has posted, counted among the session's writes.
func TestAHeldReplyToAHeldCommentAnswersAsItsPost(t *testing.T) {
	rig := newReviewRig(t, holdComments(), nil)
	comments := "/api/v1/issues/" + rig.issue + "/comments"
	root := rig.held(rig.asWriter(http.MethodPost, comments, map[string]any{"body": "Status\nWe spin up the node. " + vacuousPhrase}), "the held comment").ID
	reply := map[string]any{"body": "Status\nThe node answers on 8080. " + vacuousPhrase, "reply_to": root}
	held := rig.asWriter(http.MethodPost, comments, reply)
	id := rig.held(held, "the held reply to the held comment").ID
	rig.postedAsWritten(root)
	rig.waitHeld(id, heldAwaitingWriter)
	posted := rig.asWriterTyped(http.MethodPost, comments, reply)
	requireHeldAnswerAsPosted(t, "the reply to the held comment", held, posted, map[string]string{id: "the reply"}, map[string]string{postedID(t, posted): "the reply"})
}

// A held message on a held issue answers as its post's answer does, with the advice of the issue
// the issue's post files, in triage.
func TestAHeldMessageOnAHeldIssueAnswersAsItsPost(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Issues: ReviewHold, Messages: ReviewHold, WriterWindow: time.Minute}, nil)
	issue := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues", map[string]any{
		"project": "RVW", "title": "Astrolabe gnomon reseat offsets",
		"spec": "The astrolabe reads a constant offset after each gnomon reseat.\n\n" + vacuousPhrase,
	}), "the held issue").ID
	messages := "/api/v1/issues/" + issue + "/messages"
	message := map[string]any{"body": "Status\nWe reseat the gnomon. " + vacuousPhrase}
	held := rig.asWriter(http.MethodPost, messages, message)
	id := rig.held(held, "the held message on the held issue").ID
	rig.postedAsWritten(issue)
	rig.waitHeld(id, heldAwaitingWriter)
	posted := rig.asWriterTyped(http.MethodPost, messages, message)
	requireHeldAnswerAsPosted(t, "the message on the held issue", held, posted, map[string]string{id: "the message"}, map[string]string{postedID(t, posted): "the message"})
}

// A held issue whose parent is a held issue answers the advice its post answers, a new issue's in
// triage: no writes and no open asks of the session on it yet, the blocks of its spec as its post
// reads them (decision_blocks), and Legion owning its status where the held parent carries the
// legion label, which the label walk reads off the parent's record while no issues row holds it.
func TestAHeldIssueUnderAHeldIssueAnswersItsPostsAdvice(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Issues: ReviewHold, WriterWindow: time.Minute}, nil)
	parent := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues", map[string]any{
		"project": "RVW", "title": "Astrolabe gnomon reseat offsets", "labels": []string{"legion"},
		"spec": "The astrolabe reads a constant offset after each gnomon reseat.\n\n" + vacuousPhrase,
	}), "the held parent").ID
	child := rig.asWriter(http.MethodPost, "/api/v1/issues", map[string]any{
		"project": "RVW", "title": "Bellows valve chatter at idle", "parent": parent,
		"spec": "The bellows valve chatters while the pump idles.\n\n" + vacuousPhrase,
	})
	rig.held(child, "the held child of the held issue")
	var answer struct {
		Parent *string `json:"parent"`
		Advice struct {
			IssueStatus             string      `json:"issue_status"`
			SessionWritesSinceHuman int         `json:"session_writes_since_human"`
			YourOpenAsks            []adviceAsk `json:"your_open_asks"`
			LegionManagedStatus     *bool       `json:"legion_managed_status"`
			DecisionBlocks          *int        `json:"decision_blocks"`
		} `json:"advice"`
	}
	if err := json.Unmarshal(child.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode the held child's answer %s: %v", child.Body.String(), err)
	}
	advice := answer.Advice
	if answer.Parent == nil || *answer.Parent != parent || advice.IssueStatus != model.IssueStatusTriage || advice.SessionWritesSinceHuman != 0 ||
		advice.YourOpenAsks == nil || len(advice.YourOpenAsks) != 0 || advice.LegionManagedStatus == nil || !*advice.LegionManagedStatus ||
		advice.DecisionBlocks == nil || *advice.DecisionBlocks != 0 {
		t.Errorf("the held child of a held legion issue answers %s, want its parent, and the advice of a new issue in triage that Legion owns, with its spec's blocks", child.Body.String())
	}
}

// A held text that waits only for its turn behind its writer's earlier held text at its place
// answers as its post's answer does: its post finds that text posted, counted among the session's
// writes in its advice.
func TestAHeldTextWaitingForItsTurnAnswersAsItsPost(t *testing.T) {
	rig := newReviewRig(t, holdComments(), nil)
	comments := "/api/v1/issues/" + rig.issue + "/comments"
	first := rig.held(rig.asWriter(http.MethodPost, comments, map[string]any{"body": "Status\nWe spin up the node. " + vacuousPhrase}), "the first held comment").ID
	comment := map[string]any{"body": "Status\nThe node answers on 8080. " + vacuousPhrase}
	held := rig.asWriter(http.MethodPost, comments, comment)
	answer := rig.held(held, "the comment held behind it")
	if answer.Held.WaitsOn != first {
		t.Fatalf("the second comment waits on %q, want its turn behind %s", answer.Held.WaitsOn, first)
	}
	rig.waitHeld(answer.ID, heldAwaitingWriter)
	rig.postedAsWritten(first)
	posted := rig.asWriterTyped(http.MethodPost, comments, comment)
	requireHeldAnswerAsPosted(t, "the comment behind the held one", held, posted, map[string]string{answer.ID: "the comment"}, map[string]string{postedID(t, posted): "the comment"})
}

// A held ask on a held issue whose anchor quotes that issue's spec answers as its post's answer
// does: the quote and the document it quotes (anchor_artifact), less what that issue's post makes,
// the document's id, the block and the version.
func TestAHeldAnchoredAskOnAHeldIssueAnswersAsItsPost(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Issues: ReviewHold, Asks: ReviewHold, WriterWindow: time.Minute}, nil)
	rig.settleTermStats("RVW")
	issue := rig.held(rig.asWriter(http.MethodPost, "/api/v1/issues", map[string]any{
		"project": "RVW", "title": "Astrolabe gnomon reseat offsets",
		"spec": "The astrolabe reads a constant offset after each gnomon reseat.\n\n" + vacuousPhrase,
	}), "the held issue").ID
	asks := "/api/v1/issues/" + issue + "/asks"
	ask := map[string]any{
		"question": "Reseat the gnomon before the next reading?\n" + vacuousPhrase,
		"anchor":   map[string]any{"artifact": "spec", "quote": "constant offset"},
	}
	held := rig.asWriter(http.MethodPost, asks, ask)
	id := rig.held(held, "the held anchored ask on the held issue").ID
	rig.postedAsWritten(issue)
	rig.waitHeld(id, heldAwaitingWriter)
	posted := rig.asWriterTyped(http.MethodPost, asks, ask)
	requireHeldAnswerAsPostedLess(t, "the anchored ask on the held issue", held, posted, map[string]string{id: "the ask"}, map[string]string{postedID(t, posted): "the ask"},
		[]string{"anchor.artifact_id", "anchor.block_id", "anchor.version"})
}

// A held follow-up that waits on its writer's held reply to the same delivery answers as its
// post's answer does: filed under that reply, on the delivery's issue and to its target.
func TestAHeldFollowUpBesideAHeldReplyAnswersAsItsPost(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Messages: ReviewHold, WriterWindow: time.Minute}, nil)
	first, second := rig.messageToWriter("Is the first node up?"), rig.messageToWriter("Is the second node up?")
	reply := map[string]any{"body": "Status\nWe spin up the node. " + vacuousPhrase, "attempt": 1}
	followUp := map[string]any{"body": "Status\nWe spin up the second node. " + vacuousPhrase, "attempt": 1}
	heldReply := rig.held(rig.asWriter(http.MethodPost, "/api/v1/messages/"+first+"/reply", reply), "the held reply").ID
	held := rig.asWriter(http.MethodPost, "/api/v1/messages/"+first+"/reply?follow_up=true", followUp)
	answer := rig.held(held, "the held follow-up beside the held reply")
	if answer.Held.WaitsOn != heldReply {
		t.Fatalf("the held follow-up waits on %q, want the held reply %s", answer.Held.WaitsOn, heldReply)
	}
	postedReply := rig.asWriterTyped(http.MethodPost, "/api/v1/messages/"+second+"/reply", reply)
	posted := rig.asWriterTyped(http.MethodPost, "/api/v1/messages/"+second+"/reply?follow_up=true", followUp)
	requireHeldAnswerAsPosted(t, "the follow-up beside the held reply", held, posted,
		map[string]string{answer.ID: "the follow-up", heldReply: "the reply", first: "the message"},
		map[string]string{postedID(t, posted): "the follow-up", postedID(t, postedReply): "the reply", second: "the message"})
}
