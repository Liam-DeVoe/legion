package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// GET /api/v2/issues/{key}/references pages each of the read's three lists in the envelope, as
// the architect ruled (dispatch://INFRA-4071/comment/7ce01db2-7ea2-4cc4-8dd7-d4b6e9e9fa05): the
// issue's comment links to three documents, so links and members each hold three rows, and three
// comments of another issue link to the issue, so linked_from holds three. A page of two answers
// two rows and the list's whole total; the next page answers the third. A row of members or of
// links whose node is a document carries the document's row, and a row of linked_from carries
// none. With no relation the read is of members. Each page answers an ETag of its own, which
// another page or another list never shares, and the page's own ETag answers 304.
func TestEachV2ReferencesListPagesPastItsLimit(t *testing.T) {
	handler, _ := blockIndexHandler(t)
	createReferenceAPIProject(t, handler, "CORE")
	createReferenceAPIProject(t, handler, "OPS")
	issue := createReferenceAPIIssue(t, handler, "CORE")
	other := createReferenceAPIIssue(t, handler, "OPS")
	body := "It links"
	for _, name := range []string{"first.md", "second.md", "third.md"} {
		document := uploadDocument(t, handler, "/api/v1/issues/"+other.Key, name, "A document the issue links to.\n").Artifact
		body += " dispatch://" + other.Key + "/artifact/" + document.Slug
	}
	createReferenceAPIComment(t, handler, issue.Key, body+" here.")
	for range 3 {
		createReferenceAPIComment(t, handler, other.Key, "It cites dispatch://"+issue.Key+" here.")
	}

	target := "/api/v2/issues/" + issue.Key + "/references"
	read := func(t *testing.T, query string) (page[referenceRowV2], string) {
		t.Helper()
		response := dispatchRequest(t, handler, http.MethodGet, target+query, nil, "alice")
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s%s: status=%d body=%s", target, query, response.Code, response.Body.String())
		}
		return decodeBody[page[referenceRowV2]](t, response), response.Header().Get("ETag")
	}
	etags := map[string]string{}
	for _, relation := range []string{"members", "links", "linked_from"} {
		t.Run(relation, func(t *testing.T) {
			first, firstTag := read(t, "?relation="+relation+"&limit=2")
			next, nextTag := read(t, "?relation="+relation+"&limit=2&offset=2")
			if first.Total != 3 || len(first.Items) != 2 || first.Limit != 2 || next.Total != 3 || len(next.Items) != 1 || next.Offset != 2 {
				t.Fatalf("%s pages: first total=%d items=%d limit=%d, next total=%d items=%d offset=%d; want 3 rows paged 2 and 1",
					relation, first.Total, len(first.Items), first.Limit, next.Total, len(next.Items), next.Offset)
			}
			seen := map[string]bool{}
			for _, row := range append(first.Items, next.Items...) {
				if seen[row.Node.ID] {
					t.Fatalf("%s answers node %s on two pages", relation, row.Node.ID)
				}
				seen[row.Node.ID] = true
				document := relation != "linked_from"
				if document && (row.Node.Kind != "artifact" || row.Artifact == nil || row.Artifact.ID != row.Node.ID) {
					t.Fatalf("%s row %s %s carries artifact %+v; want the document's own row", relation, row.Node.Kind, row.Node.ID, row.Artifact)
				}
				if !document && row.Artifact != nil {
					t.Fatalf("linked_from row %s %s carries artifact %s; want none", row.Node.Kind, row.Node.ID, row.Artifact.ID)
				}
			}
			for _, tag := range []string{firstTag, nextTag} {
				if tag == "" {
					t.Fatalf("%s answered a page with no ETag", relation)
				}
				if owner, taken := etags[tag]; taken {
					t.Fatalf("%s answered the ETag %s that %s answered too", relation, tag, owner)
				}
				etags[tag] = relation
			}
			request := newDispatchRequest(t, http.MethodGet, target+"?relation="+relation+"&limit=2", nil, "alice")
			request.Header.Set("If-None-Match", firstTag)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNotModified {
				t.Fatalf("%s's first page sent its own ETag answered %d, want 304", relation, response.Code)
			}
		})
	}
	if plain, plainTag := read(t, "?limit=2"); plain.Total != 3 || etags[plainTag] != "members" {
		t.Fatalf("a read naming no relation answered total=%d under the ETag of %q; want members' first page", plain.Total, etags[plainTag])
	}
}
