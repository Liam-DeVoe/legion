package api

import (
	"math"
	"net/http"
	"net/url"
)

// list_page.go is the page each list INFRA-3231 adds answers: GET /api/v1/me/definitions, GET
// /api/v1/readers/{login}/words and GET /api/v1/writing-check/counts each answer
// {items, total, limit, offset}, the shape every list of /api/v2 answers. items is the page,
// total how many the list holds, and limit and offset the page the request named.

// The bounds of those lists' limit, which had none before they paged: the caller's own answers
// list's (answers.go), 50 when a request names none and 200 at most.
const (
	listDefaultLimit = myAnswersDefaultLimit
	listMaxLimit     = myAnswersMaxLimit
)

// listPage is the page a list route was asked for.
type listPage struct {
	limit, offset int
}

// parseListPage reads a list's limit and offset as parseMyAnswersPage reads its own: a limit
// outside 1 to listMaxLimit, or an offset that is not a non-negative integer, is 400
// INVALID_QUERY naming the bound.
func parseListPage(query url.Values) (listPage, error) {
	page := listPage{limit: listDefaultLimit}
	if value, present, valid := parseQueryInt(query, "limit", 1, listMaxLimit); present {
		if !valid {
			return listPage{}, errorf(http.StatusBadRequest, "INVALID_QUERY", "limit must be one integer from 1 to %d", listMaxLimit)
		}
		page.limit = value
	}
	if value, present, valid := parseQueryInt(query, "offset", 0, math.MaxInt); present {
		if !valid {
			return listPage{}, errorf(http.StatusBadRequest, "INVALID_QUERY", "offset must be one non-negative integer")
		}
		page.offset = value
	}
	return page, nil
}
