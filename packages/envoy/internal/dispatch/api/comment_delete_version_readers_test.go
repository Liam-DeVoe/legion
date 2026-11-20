package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// The architect's ruling of 2026-11-20 22:18Z on dispatch://INFRA-1523, census class V (b): the
// stored readers of an old version, and the stored answers cut from one, give no word of a pending
// insert suggestion its author deleted. A pinned block reference (V.6) and the candidates of a
// pinned short block id (V.11) read the version's block text masked by its insert tree, as every
// read of the version's text is (docs.MaskedVersionBlockTexts); a kept related-item check whose
// document hit was cut while the insert stood is not replayed (V.22, answerEndedByDelete); and an
// answer run given a document holding the words is withdrawn by the comment's delete (V.13,
// goingItemText, recordGoingPosts).

// pinnedIDPrefix is the prefix the two paragraphs of a pinnedInsert's spec share: their ids are it
// with "ef" and with "ff".
const pinnedIDPrefix = "ab12cd34"

// pinnedInsert is a spec of two paragraphs whose ids share pinnedIDPrefix, the first holding
// insertedWords under alice's pending insert suggestion, and the version named while the insert
// stood pending, whose insert tree holds the suggestion's mark and whose block index recorded the
// first paragraph's text with the words.
type pinnedInsert struct {
	handler http.Handler
	specRef string
	version int
	insert  model.Comment
}

// newPinnedInsert makes a pinnedInsert. A browser gives the spec's paragraphs their ids, as a
// browser keeps the ids its blocks carry, and marks the words as an insert suggestion, which alice
// then suggests; the version named after holds both.
func newPinnedInsert(t *testing.T) pinnedInsert {
	t.Helper()
	documentService, handler, database := browserDocumentService(t)
	issue := createInteractionIssue(t, handler, "TEST", "Pinned reads of an insert's words",
		"The "+insertedWords+" brown fox jumps\n\nAnother paragraph.")
	peer := connectBrowserPeer(t, documentService, issue.PrimaryArtifactID)
	t.Cleanup(peer.close)
	peer.edit(t, func(tree *pmdoc.Node) error {
		if len(tree.Children) != 2 {
			return fmt.Errorf("the spec holds %d blocks, want its two paragraphs", len(tree.Children))
		}
		for index, suffix := range []string{"ef", "ff"} {
			paragraph := tree.Children[index]
			if paragraph.Attrs == nil {
				paragraph.Attrs = pmdoc.Attrs{}
			}
			paragraph.Attrs[pmdoc.BlockIDAttr] = pinnedIDPrefix + suffix
		}
		return nil
	})
	peer.barrier(t)
	writeBrowserMark(t, database, documentService, issue.PrimaryArtifactID, docs.MarkSpec{
		Kind:  docs.MarkSuggestion,
		ID:    "m-insert",
		Attrs: pmdoc.Attrs{"id": "m-insert", "by": "user:alice", "kind": "insert"},
	}, insertedWords+" ")
	insert := createThreadComment(t, handler, issue.Key, map[string]any{
		"body":       "Add this word.",
		"anchor":     map[string]any{"artifact": "spec", "mark_id": "m-insert"},
		"suggestion": map[string]string{"replace_with": insertedWords + " "},
	}, "alice")
	return pinnedInsert{
		handler: handler,
		specRef: "dispatch://" + issue.Key + "/spec",
		version: nameVersion(t, handler, issue.PrimaryArtifactID, "With the insert pending"),
		insert:  insert,
	}
}

// pinned is the reference to block of the spec at the version named while the insert stood.
func (f pinnedInsert) pinned(block string) string {
	return f.specRef + "#b-" + block + "@v" + strconv.Itoa(f.version)
}

// V.6: a block reference pinned to the version named while the insert stood gives the block's text
// at that version, which the block index recorded with the words and never rewrites. Once the
// insert's author deletes it, the pin's text is the block's in that version's tree read without
// the words, as the version's own read gives it (docs.MaskedVersionText).
func TestAPinnedBlockReferenceGivesNoWordOfAPendingInsertDeletedSince(t *testing.T) {
	f := newPinnedInsert(t)
	reference := f.pinned(text.EncodeBlockID(pinnedIDPrefix + "ef"))
	read := func() (string, model.GraphReferences) {
		t.Helper()
		response := graphRequest(t, f.handler, url.Values{"to": {reference}})
		if response.Code != http.StatusOK {
			t.Fatalf("GET the references to %s: status=%d body=%s", reference, response.Code, response.Body.String())
		}
		return response.Body.String(), decodeBody[model.GraphReferences](t, response)
	}
	// The positive control: while the insert stands pending, the pin gives its words.
	if _, before := read(); before.Node.Pin == nil || !strings.Contains(before.Node.Pin.Text, insertedWords) {
		t.Fatalf("the pinned reference before the delete = %#v, want its pin giving %q, so its check after the delete can fail", before.Node, insertedWords)
	}

	deleteThreadComment(t, f.handler, f.insert.ID, "alice")
	body, after := read()
	if strings.Contains(body, insertedWords) {
		t.Errorf("the pinned reference after the insert's delete gives its words %q: %s", insertedWords, body)
	}
	if after.Node.Pin == nil || after.Node.Pin.Text == "" {
		t.Errorf("the pinned reference after the delete = %#v, want a pin with the block's text at version %d", after.Node, f.version)
	}
}

// V.11: a short block id pinned to the version, which both paragraphs' ids start with, is 409
// ID_AMBIGUOUS listing both blocks with the first line of each one's text at that version. Once the
// insert's author deletes it, the first paragraph's line is its text read without the words.
func TestAPinnedShortBlockIDsCandidatesGiveNoWordOfAPendingInsertDeletedSince(t *testing.T) {
	f := newPinnedInsert(t)
	reference := f.pinned(pinnedIDPrefix)
	read := func() (string, string) {
		t.Helper()
		response := graphRequest(t, f.handler, url.Values{"to": {reference}})
		if response.Code != http.StatusConflict {
			t.Fatalf("GET the references to %s: status=%d body=%s, want 409", reference, response.Code, response.Body.String())
		}
		answer := decodeBody[ambiguousIDAnswer](t, response)
		if answer.Code != contracts.IDAmbiguous || len(answer.Candidates) != 2 ||
			answer.Candidates[0].ID != pinnedIDPrefix+"ef" || answer.Candidates[1].ID != pinnedIDPrefix+"ff" {
			t.Fatalf("the refusal = %+v, want %s listing both paragraphs", answer, contracts.IDAmbiguous)
		}
		lines := make([]string, len(answer.Candidates))
		for index, candidate := range answer.Candidates {
			lines[index] = candidate.FirstLine
		}
		return response.Body.String(), strings.Join(lines, "\n")
	}
	// The positive control: while the insert stands pending, the first paragraph's line gives its
	// words.
	if _, lines := read(); !strings.Contains(lines, insertedWords) {
		t.Fatalf("the candidates' first lines before the delete are %q, want one giving %q, so their check after the delete can fail", lines, insertedWords)
	}

	deleteThreadComment(t, f.handler, f.insert.ID, "alice")
	if body, _ := read(); strings.Contains(body, insertedWords) {
		t.Errorf("the candidates after the insert's delete give its words %q: %s", insertedWords, body)
	}
}

// V.22: a related-item check sent under a key while an insert suggestion stood in a project
// document lists that document with a snippet cut from its version holding the words. Once the
// insert's author deletes it, a repeat of that key is told the answer is not kept, as for a write
// that landed with no copy of its answer, and gives none of the words; a check whose key was claimed
// before the insert existed is replayed as it was kept. The time is the key's against the
// comment's (answerEndedByDelete).
func TestAKeptRelatedCheckFromWhileAnInsertStoodIsNotReplayedAfterItsDelete(t *testing.T) {
	documentService, handler, database := browserDocumentService(t)
	if response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/projects", map[string]string{
		"key": "CORE", "name": "Core",
	}, "alice"); response.Code != http.StatusCreated {
		t.Fatalf("create project: status=%d body=%s", response.Code, response.Body.String())
	}
	notes := createProjectDocument(t, handler, "CORE", "Design notes", "The brown fox jumps\n")
	refreshTermStats(t, database, "CORE")
	check := func(key string) *httptest.ResponseRecorder {
		t.Helper()
		return keyedRequest(t, handler, http.MethodPost, "/api/v1/projects/CORE/related",
			map[string]any{"title": "brown fox jumps", "keywords_only": true}, "alice", key)
	}
	listsNotes := func(response *httptest.ResponseRecorder) bool {
		body := response.Body.String()
		return response.Code == http.StatusOK && strings.Contains(body, `"kind":"document"`) && strings.Contains(body, notes.ID)
	}
	before := check("before-the-insert")
	if !listsNotes(before) || strings.Contains(before.Body.String(), insertedWords) {
		t.Fatalf("the check before the insert: status=%d body=%s, want 200 listing the document %s without %q",
			before.Code, before.Body.String(), notes.ID, insertedWords)
	}

	// A browser inserts the words as alice's insert suggestion, which she suggests, and the version
	// named after holds them under the suggestion's mark.
	writeBrowserDocument(t, documentService, notes.ID, "The "+insertedWords+" brown fox jumps\n")
	writeBrowserMark(t, database, documentService, notes.ID, docs.MarkSpec{
		Kind:  docs.MarkSuggestion,
		ID:    "m-insert",
		Attrs: pmdoc.Attrs{"id": "m-insert", "by": "user:alice", "kind": "insert"},
	}, insertedWords+" ")
	created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+notes.ID+"/comments", map[string]any{
		"body":       "Add this word.",
		"anchor":     map[string]any{"artifact": notes.Slug, "mark_id": "m-insert"},
		"suggestion": map[string]string{"replace_with": insertedWords + " "},
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("suggest the insert on the project document: status=%d body=%s", created.Code, created.Body.String())
	}
	insert := decodeBody[model.Comment](t, created)
	nameVersion(t, handler, notes.ID, "With the insert pending")
	// The positive control: the check while the insert stands gives its words in the document's
	// snippet, so its repeat after the delete can fail.
	during := check("while-the-insert-stood")
	if !listsNotes(during) || !strings.Contains(during.Body.String(), insertedWords) {
		t.Fatalf("the check while the insert stood: status=%d body=%s, want 200 listing the document %s with %q",
			during.Code, during.Body.String(), notes.ID, insertedWords)
	}

	deleteThreadComment(t, handler, insert.ID, "alice")
	again := check("while-the-insert-stood")
	if code, _ := refusalOf(t, again); again.Code != during.Code || !replayed(again) || code != contracts.IdempotencyAnswerNotKept {
		t.Errorf("the repeat of the check kept while the insert stood: status=%d replayed=%t body=%s, want %d %s, replayed",
			again.Code, replayed(again), again.Body.String(), during.Code, contracts.IdempotencyAnswerNotKept)
	}
	if strings.Contains(again.Body.String(), insertedWords) {
		t.Errorf("the repeat after the delete gives the insert's words %q: %s", insertedWords, again.Body.String())
	}
	kept := check("before-the-insert")
	if kept.Code != before.Code || !replayed(kept) || kept.Body.String() != before.Body.String() {
		t.Errorf("the repeat of the check kept before the insert: status=%d replayed=%t body=%s, want it replayed as kept: %d %s",
			kept.Code, replayed(kept), kept.Body.String(), before.Code, before.Body.String())
	}
}

// V.13: an answer run given the spec while alice's insert suggestion stood in it reads the words
// from the spec's version, whose insert tree holds the suggestion's mark, and quotes them. The
// run is recorded with the suggestion's comment, whose own words they are (goingItemText,
// recordGoingPosts), so the comment's delete withdraws the run as the delete of any post it read
// does: the stream gives the withdrawal alone, under the id of the run's ending.
func TestGoingStreamOverADocumentHoldingAPendingInsertIsWithdrawnByItsDelete(t *testing.T) {
	answerer := &fakeAnswerer{}
	rig := newGoingRoutesRig(t, answerer)
	documentService, served := rig.server.deps.Docs.(*docs.Service)
	if !served {
		t.Fatalf("the rig's document service is %T, want *docs.Service", rig.server.deps.Docs)
	}
	issue := createInteractionIssue(t, rig.handler, "GWI", "Where the answer reads", "The "+insertedWords+" brown fox jumps")
	writeBrowserMark(t, rig.database, documentService, issue.PrimaryArtifactID, docs.MarkSpec{
		Kind:  docs.MarkSuggestion,
		ID:    "m-insert",
		Attrs: pmdoc.Attrs{"id": "m-insert", "by": "user:alice", "kind": "insert"},
	}, insertedWords+" ")
	insert := createThreadComment(t, rig.handler, issue.Key, map[string]any{
		"body":       "Add this word.",
		"anchor":     map[string]any{"artifact": "spec", "mark_id": "m-insert"},
		"suggestion": map[string]string{"replace_with": insertedWords + " "},
	}, "alice")
	nameVersion(t, rig.handler, issue.PrimaryArtifactID, "With the insert pending")
	run := askQuotingPost(t, rig, answerer, "GWI", "document", issue.PrimaryArtifactID, insertedWords)

	// The positive control: while the insert stands, the run streams its frames, the words among
	// them.
	want := quotingStream("document", issue.PrimaryArtifactID, insertedWords)
	if before := rig.readStream(t, run.ID, ""); before != want {
		t.Fatalf("before the delete the stream wrote\n%q\nwant\n%q", before, want)
	}

	deleteThreadComment(t, rig.handler, insert.ID, "alice")
	after := rig.readStream(t, run.ID, "")
	if after != withdrawnGoingStream(3) {
		t.Errorf("after the insert's delete the stream wrote\n%q\nwant the withdrawal alone\n%q", after, withdrawnGoingStream(3))
	}
	if strings.Contains(after, insertedWords) {
		t.Errorf("after the delete the stream carries the insert's words: %q", after)
	}
}
