package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/grapherase"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// listed reports whether a line of the run's listing holds every one of parts, which is how
// these tests read one line of it without pinning the whole listing.
func listed(erasure *grapherase.Erasure, parts ...string) bool {
	for _, line := range erasure.Listing {
		found := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				found = false
				break
			}
		}
		if found {
			return true
		}
	}
	return false
}

// blockWhoseTextIs is the node address of the block of document whose stored text is exactly
// text, as ref_key, '#' and the block id (migration 0121), after reading that the store holds its
// node. It is read by the whole text and not by a search inside it, so a sibling block that
// repeats the words is never the block it answers.
func blockWhoseTextIs(t *testing.T, database *store.Store, document, text string) string {
	t.Helper()
	var address string
	if err := database.Pool.QueryRow(context.Background(), `
		select a.ref_key || '#' || t.block_id from document_block_texts t join artifacts a on a.id = t.artifact_id
		where t.artifact_id = $1::uuid and t.text = $2
		  and exists (select 1 from graph_nodes n where n.kind = 'block' and n.id = a.ref_key || '#' || t.block_id)
		order by t.version desc limit 1`, document, text).Scan(&address); err != nil {
		t.Fatalf("read the block node of %s whose text is %q: %v", document, text, err)
	}
	return address
}

// documentEvents is the payload of every event about document, by event id, as the log holds them
// now: a read before the erase and a read after it say what the erase moved and what it left.
func documentEvents(t *testing.T, database *store.Store, document string) map[int64]string {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `
		select id, payload::text from events where payload->>'artifact_id' = $1 order by id`, document)
	if err != nil {
		t.Fatalf("read the events about %s: %v", document, err)
	}
	defer rows.Close()
	payloads := map[int64]string{}
	for rows.Next() {
		var id int64
		var payload string
		if err := rows.Scan(&id, &payload); err != nil {
			t.Fatalf("scan an event about %s: %v", document, err)
		}
		payloads[id] = payload
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the events about %s: %v", document, err)
	}
	return payloads
}

// versionsOf is the number of every version of document the store holds.
func versionsOf(t *testing.T, database *store.Store, document string) []int32 {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(),
		`select number from artifact_versions where artifact_id = $1::uuid order by number`, document)
	if err != nil {
		t.Fatalf("read the versions of %s: %v", document, err)
	}
	defer rows.Close()
	var numbers []int32
	for rows.Next() {
		var number int32
		if err := rows.Scan(&number); err != nil {
			t.Fatalf("scan a version of %s: %v", document, err)
		}
		numbers = append(numbers, number)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the versions of %s: %v", document, err)
	}
	return numbers
}

// R5: a block erase takes its own block's text and leaves every other block of the same document
// as it is, though that block repeats the erased words. The document holds a sibling paragraph
// opening with the erased block's whole text and running on - the words inside a longer line are
// the sibling's, which is a node of its own - written at version 1, and the erased block is added
// at version 2.
//
// After the erase: the sibling's paragraph stands byte for byte in the live text, version 1,
// which holds the sibling and never the erased block, stands with it, and the event of each
// version keeps the sibling's line, the one about version 2 reading the marker where the erased
// block's own line was.
func TestTheBlockEraseLeavesASiblingThatRepeatsItsWords(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	databaseURL := database.Pool.Config().ConnString()
	server := startDispatchProcess(t, databaseURL)
	server.waitHealthy(t)
	server.call(t, http.MethodPost, "/api/v1/projects", `{"key":"ERB","name":"Erase"}`, http.StatusCreated)

	secret := eraseTestSecret()
	sibling := secret + "s ripen in winter, said the sibling."
	issue, _ := createEraseIssue(t, server, "ERB", "A sibling that repeats the words", "# Spec\n\nNothing here.\n")
	document := uploadEraseDocument(t, server, issue, "sib.md", "# Notes\n\n"+sibling+"\n")
	uploadEraseDocument(t, server, issue, "sib.md", "# Notes\n\n"+sibling+"\n\n"+secret+"\n")
	block := blockWhoseTextIs(t, database, document, secret)
	if text := eraseDocumentText(t, server, document); !strings.Contains(text, sibling) || !strings.Contains(text, "\n"+secret+"\n") {
		t.Fatalf("the live text before the erase reads %q, want the sibling's line and the erased block's own", text)
	}
	if held := versionsOf(t, database, document); len(held) != 2 {
		t.Fatalf("the document holds the versions %v before the erase, want the sibling's and the erased block's", held)
	}
	events := documentEvents(t, database, document)
	if len(events) == 0 {
		t.Fatalf("no event about %s holds its versions, so the event check proves nothing", document)
	}

	erasure, err := confirmErase(ctx, databaseURL, "block", block, time.Millisecond)
	if err != nil || !erasure.Applied {
		t.Fatalf("erase %s: applied %t, %v", block, erasure != nil && erasure.Applied, err)
	}
	text := eraseDocumentText(t, server, document)
	if !strings.Contains(text, sibling) {
		t.Errorf("the sibling's line is not in the live text after the erase, which reads %q", text)
	}
	if strings.Contains(text, "\n"+secret+"\n") || !strings.Contains(text, "erased by envoy-dispatch erase block:"+block) {
		t.Errorf("the erased block still reads %q in the live text after the erase; want the marker in its place", text)
	}
	if held := versionsOf(t, database, document); len(held) != 1 || held[0] != 1 {
		t.Errorf("the document holds the versions %v after the erase, want version 1 alone: it holds the sibling's words and no block the erase took", held)
	}
	if !listed(erasure, "version 2", "the erased block holds the text at that version") {
		t.Errorf("the listing does not say why version 2 goes:\n%s", strings.Join(erasure.Listing, "\n"))
	}
	for id, before := range documentEvents(t, database, document) {
		after := events[id]
		if after == "" {
			t.Errorf("event %d about the document is one the erase wrote: %s", id, before)
			continue
		}
		if !strings.Contains(before, sibling) && !strings.Contains(after, sibling) {
			continue
		}
		if !strings.Contains(before, sibling) {
			t.Errorf("event %d lost the sibling's line, which no erase of another node may take:\nbefore %s\nafter  %s", id, after, before)
		}
	}
	var siblingText int
	if err := database.Pool.QueryRow(ctx,
		`select count(*) from document_block_texts where artifact_id = $1::uuid and text = $2`, document, sibling).Scan(&siblingText); err != nil {
		t.Fatalf("read the sibling's stored text: %v", err)
	}
	if siblingText != 1 {
		t.Errorf("the sibling's stored text reads %d row(s) holding it as written after the erase, want its one", siblingText)
	}
}

// writeAnchoredComment writes body as a comment of issue anchored to the quote in document, as
// the test's person, through the route a person writes one through, and answers its id. The write
// projects the comment into the document's margin (api's commentMarkRecord, docs.ProjectMark), so
// the document's stored history holds the body.
func writeAnchoredComment(t *testing.T, server *dispatchProcess, issue, document, quote, body string) string {
	t.Helper()
	input, err := json.Marshal(map[string]any{
		"body":   body,
		"anchor": map[string]any{"artifact": document, "quote": quote},
	})
	if err != nil {
		t.Fatalf("encode the anchored comment: %v", err)
	}
	created := server.call(t, http.MethodPost, "/api/v1/issues/"+issue+"/comments", string(input), http.StatusCreated)
	var comment struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created, &comment); err != nil || comment.ID == "" {
		t.Fatalf("read the anchored comment back: %v: %s", err, created)
	}
	return comment.ID
}

// writeCommentReply writes body as a reply to comment in issue and answers its id. The write
// projects the thread's root again, its replies with it, so the root's margin record holds the
// reply's body.
func writeCommentReply(t *testing.T, server *dispatchProcess, issue, comment, body string) string {
	t.Helper()
	input, err := json.Marshal(map[string]string{"body": body, "reply_to": comment})
	if err != nil {
		t.Fatalf("encode the reply: %v", err)
	}
	created := server.call(t, http.MethodPost, "/api/v1/issues/"+issue+"/comments", string(input), http.StatusCreated)
	var reply struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created, &reply); err != nil || reply.ID == "" {
		t.Fatalf("read the reply back: %v: %s", err, created)
	}
	return reply.ID
}

// R5: a comment's body stands in the margin record of the document it is anchored to, which is
// stored in that document's own history (doc_updates), and a reply's stands in its thread root's
// record there. The erase of either takes the body out of that history, which is the document's
// editing state, and lists the document under what it changes. The document's own text is no
// part of what goes: it is left as it stands, record and anchor marks aside.
//
// Each erase is held against a control that finds the bytes in the history before it runs. The
// two comments are erased from documents of their own, so neither erase is the one that took the
// other's bytes: the margin of a document goes with its history, which one erase replaces whole.
func TestTheCommentEraseTakesItsBodyOutOfTheMarginOfItsDocument(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	databaseURL := database.Pool.Config().ConnString()
	server := startDispatchProcess(t, databaseURL)
	server.waitHealthy(t)
	server.call(t, http.MethodPost, "/api/v1/projects", `{"key":"ERM","name":"Erase"}`, http.StatusCreated)

	const anchored = "A line a person had something to say about."
	issue, _ := createEraseIssue(t, server, "ERM", "A comment in the margin", "# Spec\n\nNothing here.\n")
	alone := uploadEraseDocument(t, server, issue, "alone.md", "# Alone\n\n"+anchored+"\n")
	thread := uploadEraseDocument(t, server, issue, "thread.md", "# Thread\n\n"+anchored+"\n")
	remark, answer := eraseTestSecret(), eraseTestSecret()+"answered"
	comment := writeAnchoredComment(t, server, issue, alone, anchored, remark)
	root := writeAnchoredComment(t, server, issue, thread, anchored, "A remark with nothing secret in it.")
	reply := writeCommentReply(t, server, issue, root, answer)
	aloneText, threadText := eraseDocumentText(t, server, alone), eraseDocumentText(t, server, thread)

	// The controls: each document's stored history holds the body of the comment of it, read by
	// the very statement that must read none of it afterwards.
	if held := updatesHolding(t, database, alone, remark); held == 0 {
		t.Fatalf("no stored update of the anchored document holds the comment's body, so the erase proves nothing")
	}
	if held := updatesHolding(t, database, thread, answer); held == 0 {
		t.Fatalf("no stored update of the thread's document holds the reply's body, so the erase proves nothing")
	}

	erased, err := confirmErase(ctx, databaseURL, "comment", comment, time.Millisecond)
	if err != nil || !erased.Applied {
		t.Fatalf("erase the anchored comment: applied %t, %v", erased != nil && erased.Applied, err)
	}
	if held := updatesHolding(t, database, alone, remark); held != 0 {
		t.Errorf("after the erase %d stored update(s) of the anchored document still hold the comment's body", held)
	}
	if len(erased.Documents) != 1 || !strings.HasSuffix(erased.Documents[0].Document, "/alone-md") || erased.Documents[0].Held {
		t.Errorf("the erase of the anchored comment lists the documents %+v, want the document it is anchored to, its text holding none of the body",
			erased.Documents)
	}
	if places, searched := columnsHolding(t, database, remark); len(places) > 0 {
		t.Errorf("after the erase the comment's body is still in %s (%d columns searched)", strings.Join(places, ", "), searched)
	}
	if text := eraseDocumentText(t, server, alone); text != aloneText {
		t.Errorf("the erase moved the text of the document it is anchored to:\nbefore %q\nafter  %q", aloneText, text)
	}

	answered, err := confirmErase(ctx, databaseURL, "comment", reply, time.Millisecond)
	if err != nil || !answered.Applied {
		t.Fatalf("erase the reply: applied %t, %v", answered != nil && answered.Applied, err)
	}
	if held := updatesHolding(t, database, thread, answer); held != 0 {
		t.Errorf("after the erase %d stored update(s) of the thread's document still hold the reply's body", held)
	}
	if len(answered.Documents) != 1 || !strings.HasSuffix(answered.Documents[0].Document, "/thread-md") {
		t.Errorf("the erase of the reply lists the documents %+v, want the document its thread's root is anchored to", answered.Documents)
	}
	if places, searched := columnsHolding(t, database, answer); len(places) > 0 {
		t.Errorf("after the erase the reply's body is still in %s (%d columns searched)", strings.Join(places, ", "), searched)
	}
	if text := eraseDocumentText(t, server, thread); text != threadText {
		t.Errorf("the erase moved the text of the thread's document:\nbefore %q\nafter  %q", threadText, text)
	}
}
