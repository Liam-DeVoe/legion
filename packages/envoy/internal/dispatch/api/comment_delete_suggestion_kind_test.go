package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The architect's ruling (b) of 2026-11-20 08:08Z on dispatch://INFRA-1523: a suggestion records
// the kind of its mark when it is made, in the row's suggestion column (storedSuggestion), and the
// delete of a suggestion whose mark a browser edit took out of the document reads that kind to
// tell an insert's quote, which is the comment's own words, from document text (deleteComment).
// A row made before the create stored the kind takes the rule the delete had before: its quote is
// emptied when it is its replace_with, byte for byte.

// suggestedWordsStem starts every word the insert suggestions below put in the document, as typed
// and as a browser retyped them, so a read that gives any of those words gives the stem.
const suggestedWordsStem = "zarquon"

// storedSuggestionKind is the kind the row of comment id holds in its suggestion column, and
// whether it holds one.
func storedSuggestionKind(t *testing.T, database *store.Store, id string) (string, bool) {
	t.Helper()
	var kind *string
	if err := database.Pool.QueryRow(context.Background(), `
		select suggestion->>'kind' from comments where id = $1
	`, id).Scan(&kind); err != nil {
		t.Fatalf("read the stored suggestion kind of %s: %v", id, err)
	}
	if kind == nil {
		return "", false
	}
	return *kind, true
}

// retypeSuggestedWords stands in for a browser editor that retypes part of the words a suggestion
// mark wraps: from becomes to in the text under markID, which keeps the mark.
func retypeSuggestedWords(t *testing.T, documentService *docs.Service, artifactID, markID, from, to string) {
	t.Helper()
	peer := connectBrowserPeer(t, documentService, artifactID)
	t.Cleanup(peer.close)
	peer.edit(t, func(tree *pmdoc.Node) error {
		retyped := false
		var walk func(node *pmdoc.Node)
		walk = func(node *pmdoc.Node) {
			for _, mark := range node.Marks {
				if !retyped && mark.Type == string(docs.MarkSuggestion) && mark.Attrs["id"] == markID && strings.Contains(node.Text, from) {
					node.Text = strings.Replace(node.Text, from, to, 1)
					retyped = true
				}
			}
			for _, child := range node.Children {
				walk(child)
			}
		}
		walk(tree)
		if !retyped {
			return fmt.Errorf("no text under the suggestion mark %q holds %q", markID, from)
		}
		return nil
	})
	peer.barrier(t)
}

// orphanedInsert is an insert suggestion whose mark a browser edit took out of the document, as
// orphanedInsertSuggestion leaves it: quote is its anchor's stored quote.
type orphanedInsert struct {
	handler  http.Handler
	database *store.Store
	issueKey string
	comment  model.Comment
	quote    string
}

// orphanedInsertSuggestion makes an insert suggestion on a browser's insert mark around
// "zarquonsuggested ", and then a browser edit that takes the words and the mark out of the
// document. retyped has another browser edit change "suggested" to "revised" under the mark
// first, and a server edit elsewhere refresh the document's anchors, so the anchor's stored quote
// is "zarquonrevised " and no longer the suggestion's replace_with. legacy takes the stored kind
// off the row, as a row made before the create stored one holds none.
func orphanedInsertSuggestion(t *testing.T, retyped, legacy bool) orphanedInsert {
	t.Helper()
	const typed = suggestedWordsStem + "suggested "
	documentService, handler, database := browserDocumentService(t)
	issue := createInteractionIssue(t, handler, "TEST", "Delete an orphaned insert", "The "+typed+"brown fox")
	writeBrowserMark(t, database, documentService, issue.PrimaryArtifactID, docs.MarkSpec{
		Kind:  docs.MarkSuggestion,
		ID:    "m-insert",
		Attrs: pmdoc.Attrs{"id": "m-insert", "by": "user:alice", "kind": "insert"},
	}, typed)
	comment := createThreadComment(t, handler, issue.Key, map[string]any{
		"body":       "Add this word.",
		"anchor":     map[string]any{"artifact": "spec", "mark_id": "m-insert"},
		"suggestion": map[string]string{"replace_with": typed},
	}, "alice")
	quote := typed
	if retyped {
		retypeSuggestedWords(t, documentService, issue.PrimaryArtifactID, "m-insert", "suggested", "revised")
		if response := editDocument(t, handler, issue.PrimaryArtifactID, map[string]any{
			"ops": []map[string]any{{"op": "replace", "find": "fox", "with": "dog"}},
		}); response.Code != http.StatusOK {
			t.Fatalf("the server edit that refreshes the anchors: status=%d body=%s", response.Code, response.Body.String())
		}
		quote = suggestedWordsStem + "revised "
	}
	// The positive control: the anchor quotes the words on the standing mark, and a retyped
	// quote is no longer the suggestion's replace_with.
	if read := readThreadComment(t, handler, comment.ID); read.Anchor == nil || read.Anchor.Quote != quote || read.Anchor.Orphaned ||
		read.Suggestion == nil || (read.Suggestion.ReplaceWith == read.Anchor.Quote) == retyped {
		t.Fatalf("the comment before the orphaning = anchor %#v, suggestion %#v; want the anchor quoting %q on the standing mark",
			read.Anchor, read.Suggestion, quote)
	}
	if legacy {
		if _, err := database.Pool.Exec(context.Background(), `
			update comments set suggestion = suggestion - 'kind' where id = $1
		`, comment.ID); err != nil {
			t.Fatalf("take the stored kind off the row: %v", err)
		}
		if kind, stored := storedSuggestionKind(t, database, comment.ID); stored {
			t.Fatalf("the row still stores the kind %q", kind)
		}
	}
	writeBrowserDocument(t, documentService, issue.PrimaryArtifactID, "The brown dog\n")
	return orphanedInsert{handler: handler, database: database, issueKey: issue.Key, comment: comment, quote: quote}
}

// deleteAndFindTheWords deletes the suggestion, answers the comment's read after the delete, and
// names each read that gives any of the suggestion's words: the delete's answer, the comment's
// read, each event the delete stored, and the event log's read of each event of the comment.
func (f orphanedInsert) deleteAndFindTheWords(t *testing.T) (model.Comment, []string) {
	t.Helper()
	var giving []string
	gives := func(what, text string) {
		if strings.Contains(text, suggestedWordsStem) {
			giving = append(giving, what+": "+text)
		}
	}
	beforeDelete := lastEventID(t, f.database)
	answer := dispatchRequest(t, f.handler, http.MethodDelete, "/api/v1/comments/"+f.comment.ID, nil, "alice")
	if answer.Code != http.StatusOK {
		t.Fatalf("delete the suggestion: status=%d body=%s", answer.Code, answer.Body.String())
	}
	gives("the delete's answer", answer.Body.String())
	read := dispatchRequest(t, f.handler, http.MethodGet, "/api/v1/comments/"+f.comment.ID, nil, "alice")
	if read.Code != http.StatusOK {
		t.Fatalf("read the comment: status=%d body=%s", read.Code, read.Body.String())
	}
	gives("the read after the delete", read.Body.String())
	deletes := 0
	for _, event := range storedCommentEvents(t, f.database, f.comment.ID, beforeDelete) {
		gives("the stored "+event.eventType+" the delete wrote", event.raw)
		if event.eventType == "comment.deleted" {
			deletes++
		}
	}
	if deletes != 1 {
		t.Fatalf("the delete stored %d comment.deleted events, want one", deletes)
	}
	for _, event := range decodedIssueEvents(t, f.handler, f.issueKey) {
		if event.Payload["id"] != f.comment.ID {
			continue
		}
		encoded, err := json.Marshal(event.Payload)
		if err != nil {
			t.Fatalf("encode the event log's %s: %v", event.Type, err)
		}
		gives("the event log's "+event.Type, string(encoded))
	}
	return decodeBody[model.Comment](t, read), giving
}

// Row 1 of the ruling. A browser edit changed some of an insert's words under its mark, an anchor
// refresh moved the anchor's quote to the words as retyped, and a later browser edit took the
// words and the mark out of the document. The quote is no longer the suggestion's replace_with, so
// no fact of the row's text tells it from document text; the kind the create stored says it is an
// insert's, and the delete empties it.
func TestDeletingAnOrphanedInsertSuggestionABrowserRetypedLeavesNoneOfItsWords(t *testing.T) {
	fixture := orphanedInsertSuggestion(t, true, false)

	read, giving := fixture.deleteAndFindTheWords(t)

	if read.Anchor == nil || read.Anchor.Quote != "" || read.Anchor.MarkID != "m-insert" || !read.Anchor.Orphaned {
		t.Errorf("the read after the delete gives the anchor %#v, want mark m-insert, orphaned, with an empty quote", read.Anchor)
	}
	for _, what := range giving {
		t.Errorf("%s gives the deleted insert suggestion's words", what)
	}
}

// Row 2 of the ruling. A replace suggestion's quote is document text, which stays in the document
// whatever the delete does, so the delete of one whose mark a browser edit took out keeps it. Its
// replace_with is the very text it quotes: that is the one replace whose quote the rule for a row
// with no stored kind empties, so only the stored kind keeps it.
func TestDeletingAnOrphanedReplaceSuggestionKeepsItsQuote(t *testing.T) {
	documentService, handler, database := browserDocumentService(t)
	issue := createInteractionIssue(t, handler, "TEST", "Delete an orphaned replace", "The quick brown fox")
	comment := createThreadComment(t, handler, issue.Key, map[string]any{
		"body":       "Keep this word.",
		"anchor":     map[string]any{"artifact": "spec", "quote": "quick"},
		"suggestion": map[string]string{"replace_with": "quick"},
	}, "alice")
	// A browser edit takes the mark out of the document and leaves its text.
	writeBrowserDocument(t, documentService, issue.PrimaryArtifactID, "The quick brown dog\n")
	if _, err := documentService.VerifyMark(context.Background(), issue.PrimaryArtifactID, docs.MarkSuggestion, comment.Anchor.MarkID); !errors.Is(err, docs.ErrAnchorMissing) {
		t.Fatalf("the suggestion's mark after the browser edit = %v, want it gone", err)
	}

	beforeDelete := lastEventID(t, database)
	answer := deleteThreadComment(t, handler, comment.ID, "alice")

	if answer.Anchor == nil || answer.Anchor.Quote != "quick" {
		t.Errorf("the delete's answer gives the anchor %#v, want the quote %q", answer.Anchor, "quick")
	}
	if read := readThreadComment(t, handler, comment.ID); read.Anchor == nil || read.Anchor.Quote != "quick" || !read.Anchor.Orphaned {
		t.Errorf("the read after the delete gives the anchor %#v, want the quote %q, orphaned", read.Anchor, "quick")
	}
	for _, event := range storedCommentEvents(t, database, comment.ID, beforeDelete) {
		if event.eventType != "comment.deleted" {
			continue
		}
		if quote, ok := payloadAnchorQuote(event.payload); !ok || quote != "quick" {
			t.Errorf("the stored comment.deleted anchor quote = %q (carried %t), want %q", quote, ok, "quick")
		}
	}
}

// Row 3 of the ruling. A row made before the create stored the kind holds none, and the delete of
// its orphaned suggestion takes the rule it had before: the quote goes when it is the suggestion's
// replace_with, byte for byte, and stays otherwise, as the words of an insert a browser retyped
// stay, which no fact of such a row tells from document text.
func TestDeletingAnOrphanedSuggestionOfARowWithNoStoredKindComparesItsQuote(t *testing.T) {
	for _, test := range []struct {
		name    string
		retyped bool
	}{
		{name: "a quote that is the replace_with"},
		{name: "a quote a browser retyped", retyped: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := orphanedInsertSuggestion(t, test.retyped, true)

			read, giving := fixture.deleteAndFindTheWords(t)

			want := ""
			if test.retyped {
				want = fixture.quote
			}
			if read.Anchor == nil || read.Anchor.Quote != want {
				t.Fatalf("the read after the delete gives the anchor %#v, want the quote %q", read.Anchor, want)
			}
			if !test.retyped {
				for _, what := range giving {
					t.Errorf("%s gives the deleted insert suggestion's words", what)
				}
			}
		})
	}
}

// The create stores the kind of the suggestion's mark - a browser mark's own, and replace for the
// mark a quote anchor writes - and no answer carries it: every read decodes the column into
// model.Suggestion, which has no kind, and so does the comment.created event the create wrote.
func TestASuggestionStoresTheKindOfItsMarkAndNoAnswerCarriesIt(t *testing.T) {
	for _, test := range []struct {
		name, kind string
		mark       bool
	}{
		{name: "an insert mark", kind: "insert", mark: true},
		{name: "a delete mark", kind: "delete", mark: true},
		{name: "a quote", kind: "replace"},
	} {
		t.Run(test.name, func(t *testing.T) {
			documentService, handler, database := browserDocumentService(t)
			issue := createInteractionIssue(t, handler, "TEST", "Store "+test.name, "The quick brown fox")
			anchor := map[string]any{"artifact": "spec", "quote": "quick"}
			if test.mark {
				writeBrowserMark(t, database, documentService, issue.PrimaryArtifactID, docs.MarkSpec{
					Kind:  docs.MarkSuggestion,
					ID:    "m-browser",
					Attrs: pmdoc.Attrs{"id": "m-browser", "by": "user:alice", "kind": test.kind},
				}, "quick")
				anchor = map[string]any{"artifact": "spec", "mark_id": "m-browser"}
			}
			comment := createThreadComment(t, handler, issue.Key, map[string]any{
				"body": "Say this instead.", "anchor": anchor, "suggestion": map[string]string{"replace_with": "slow"},
			}, "alice")

			if kind, stored := storedSuggestionKind(t, database, comment.ID); !stored || kind != test.kind {
				t.Fatalf("the row stores the kind %q (stored %t), want %q", kind, stored, test.kind)
			}
			response := dispatchRequest(t, handler, http.MethodGet, "/api/v1/comments/"+comment.ID, nil, "alice")
			if response.Code != http.StatusOK {
				t.Fatalf("read comment: status=%d body=%s", response.Code, response.Body.String())
			}
			read := decodeBody[map[string]any](t, response)
			suggestion, _ := read["suggestion"].(map[string]any)
			if suggestion == nil || suggestion["replace_with"] != "slow" {
				t.Fatalf("the read gives the suggestion %#v, want replace_with slow", read["suggestion"])
			}
			if kind, carried := suggestion["kind"]; carried {
				t.Errorf("the read's suggestion carries the kind %v: %#v", kind, suggestion)
			}
			list := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+issue.Key+"/comments", nil, "alice")
			if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), comment.ID) {
				t.Fatalf("list comments: status=%d body=%s", list.Code, list.Body.String())
			}
			if stored := `"kind":"` + test.kind + `"`; strings.Contains(list.Body.String(), stored) {
				t.Errorf("the comment list carries %s: %s", stored, list.Body.String())
			}
			created := 0
			for _, event := range storedCommentEvents(t, database, comment.ID, 0) {
				if event.eventType != "comment.created" {
					continue
				}
				created++
				suggestion, _ := event.payload["suggestion"].(map[string]any)
				if suggestion == nil {
					t.Fatalf("the stored comment.created carries no suggestion: %s", event.raw)
				}
				if kind, carried := suggestion["kind"]; carried {
					t.Errorf("the stored comment.created suggestion carries the kind %v: %s", kind, event.raw)
				}
			}
			if created != 1 {
				t.Fatalf("the create stored %d comment.created events, want one", created)
			}
		})
	}
}
