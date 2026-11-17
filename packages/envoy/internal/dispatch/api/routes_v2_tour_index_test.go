package api

import (
	"net/http"
	"net/url"
	"slices"
	"testing"
)

func init() { addTourSteps("index", indexTourSteps) }

// indexTourSteps are the route tour's steps of the route index, GET /api/v2: read whole, as a
// request naming no limit reads it, and a page of it.
func indexTourSteps() []tourStep {
	return []tourStep{
		{
			Method: http.MethodGet, Pattern: "", Status: http.StatusOK,
			Setup: func(*testing.T, *routeTour) tourRequest { return tourRequest{} },
		},
		{
			Method: http.MethodGet, Pattern: "", Name: "a page of it", Status: http.StatusOK,
			Setup: func(*testing.T, *routeTour) tourRequest {
				return tourRequest{Query: url.Values{"limit": {"2"}, "offset": {"1"}}}
			},
		},
	}
}

// GET /api/v2 answers every route on one page where the request names no limit, limit the whole
// list, and the page a request names - the same routes, in the same order - where it names a limit
// and an offset; a limit of 0 and a negative offset are refused, naming the bound (R1.4).
func TestTheV2RouteIndexReadsItsPageThroughTheDecoder(t *testing.T) {
	handler := newTestHandler(t)
	read := func(target string) routeIndexV2 {
		t.Helper()
		response := dispatchRequest(t, handler, http.MethodGet, target, nil, "")
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", target, response.Code, response.Body.String())
		}
		return decodeBody[routeIndexV2](t, response)
	}
	whole := read("/api/v2")
	if whole.Total < 3 || len(whole.Items) != whole.Total || whole.Limit != whole.Total || whole.Offset != 0 || whole.Docs == "" {
		t.Fatalf("the whole index answered total %d, %d items, limit %d, offset %d: want every route, limit the whole list",
			whole.Total, len(whole.Items), whole.Limit, whole.Offset)
	}
	paged := read("/api/v2?limit=2&offset=1")
	if paged.Total != whole.Total || paged.Limit != 2 || paged.Offset != 1 ||
		!slices.EqualFunc(paged.Items, whole.Items[1:3], func(left, right routeIndexV2Entry) bool {
			return left.Path == right.Path && left.Method == right.Method
		}) {
		t.Fatalf("the page at limit 2, offset 1 answered %+v, want routes 2 and 3 of %d", paged.page, whole.Total)
	}
	past := read("/api/v2?offset=100000")
	if past.Total != whole.Total || past.Items == nil || len(past.Items) != 0 {
		t.Fatalf("a page past the end answered %d items (nil %v), want an empty list", len(past.Items), past.Items == nil)
	}
	for target, code := range map[string]string{"/api/v2?limit=0": "INVALID_LIMIT", "/api/v2?offset=-1": "INVALID_OFFSET"} {
		response := dispatchRequest(t, handler, http.MethodGet, target, nil, "")
		if refused, _ := refusalOf(t, response); response.Code != http.StatusBadRequest || refused != code {
			t.Errorf("GET %s: %d %s, want 400 %s", target, response.Code, response.Body.String(), code)
		}
	}
}
