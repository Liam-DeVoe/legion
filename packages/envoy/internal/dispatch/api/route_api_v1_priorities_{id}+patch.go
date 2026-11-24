package api

import (
	"fmt"
	"net/http"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodPatch,
	Pattern:     "/api/v1/priorities/{id}",
	Auth:        authAny,
	Description: fmt.Sprintf("Write one priority's title, description or owner, and move its order {title?, description?, owner?, rank?}: each field named is written and each left out is left alone. rank is {before, after}, the priority this one lands above and the one it lands below, as the issue board's own drag names them; a rank naming neither, a neighbour that is this priority, and one no live priority carries (an id the store does not hold, or one whose state is gone) are each 400 RANK_INPUT. A title with nothing in it or over %d characters (UTF-16 units, counted as an issue title is, so a rename is held to the bound a create is) and a body naming nothing to write are 400 PRIORITY_INPUT, and an id no priority carries is 404 PRIORITY_NOT_FOUND.", contracts.IssueTitleMax),
	Handler:     (*server).patchPriority,
	WriteClass:  scopeDeployment,
	ID:          &idRoute{Reason: "a priority id, a slug of its own and no uuid at all"},
	Shape: routeShape{
		Status: http.StatusOK, Response: model.Priority{}, Request: patchPriorityInput{},
	},
})
