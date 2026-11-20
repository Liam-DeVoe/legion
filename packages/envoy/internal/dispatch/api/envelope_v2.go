package api

// envelope_v2.go is the one list envelope of /api/v2 (R1.4 of dispatch://INFRA-2946/spec): every
// list answers {items, total, limit, offset}, and takes limit and offset (pageQuery), which the
// decoder reads against the row's List bounds. A member that describes the page beside items -
// the route map's "beside" columns, such as the route index's docs - is declared on the row's
// answer type, which embeds the page:
//
//	type routeIndexV2 struct {
//		Docs string `json:"docs"`
//		page[routeIndexV2Entry]
//	}

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/sjawhar/envoy/internal/dispatch/api/apicode"
)

// page is one page of a list (R1.4): its items, how many the whole list holds, and the limit and
// offset the page was read at. Items is never null: an empty page answers [].
type page[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// logPage is one page of a list whose total is null (R5 of dispatch://INFRA-4071/spec):
// the global event log, GET /api/v2/events, which a log read forward by id would count whole on
// every page, about ten times the page for a reader near the oldest event, and the caller's
// dismissed threads, GET /api/v2/me/threads?state=dismissed, every thread they have ever
// dismissed (listMyThreadsV2), whose other states answer their exact total in it. Every other
// list, every other log among them, answers page's exact total.
type logPage[T any] struct {
	Items  []T  `json:"items"`
	Total  *int `json:"total"`
	Limit  int  `json:"limit"`
	Offset int  `json:"offset"`
}

// newPage is the page of a list a query read in the store: items the rows the query's limit and
// offset selected, total the count of the whole list, and query the page the request asked for.
func newPage[T any](items []T, total int, query pageQuery) page[T] {
	if items == nil {
		items = []T{}
	}
	return page[T]{Items: items, Total: total, Limit: query.Limit, Offset: query.Offset}
}

// pageOf is the page query asks for of items, a list held whole in memory, such as the route
// index. A query whose Limit is 0 - a catalogue read whole, whose row's List is Whole, that named
// no limit - is answered every item from its offset, at the limit of the whole list.
func pageOf[T any](items []T, query pageQuery) page[T] {
	total := len(items)
	limit := query.Limit
	if limit == 0 {
		limit = total
	}
	start := min(query.Offset, total)
	end := start + min(limit, total-start)
	return newPage(items[start:end:end], total, pageQuery{Limit: limit, Offset: query.Offset})
}

// includedPageMax is the largest page a list answers with an include that adds each row's whole
// text: include=text on a list of an owner's artifacts, each document's text up to
// pmdoc.MaxDocumentBytes, and include=body on an issue's handoffs, each body up to
// contracts.DispatchHandoffBodyMax. A page of maxListLimit could hold 200 of them at once
// (includedPageWithinBound).
const includedPageMax = 20

// includedPageLimitText is what the description of such a list says of its page, with
// include=<member> and without.
func includedPageLimitText(member string) string {
	return fmt.Sprintf("limit 1 to %d (default %d), and 1 to %d (default %d) with include=%s, and offset;",
		maxListLimit, defaultListLimit, includedPageMax, includedPageMax, member)
}

// includedPageWithinBound holds page, the decoded page of a list whose include is include, to
// includedPageMax where include names member, which answers what for each row: a limit the query
// names past it is refused 400 INVALID_LIMIT naming the bound, as the decoder refuses a limit past
// the row's bounds, and a query that names none reads a page of that many. It writes the refusal
// and answers false, and runs before the handler reads anything.
func includedPageWithinBound(w http.ResponseWriter, r *http.Request, page *pageQuery, include []string, member, what string) bool {
	if !slices.Contains(include, member) || page.Limit <= includedPageMax {
		return true
	}
	if v2QueryNames(r, "limit") {
		writeError(w, r, apicode.InvalidLimit, fmt.Sprintf(
			"limit must be one integer from 1 to %d with include=%s, which answers %s", includedPageMax, member, what))
		return false
	}
	page.Limit = includedPageMax
	return true
}
