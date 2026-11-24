package api

import (
	"fmt"
	"net/http"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodPost,
	Pattern:     "/api/v1/priorities",
	Auth:        authAny,
	Description: fmt.Sprintf("Write one priority {id?, title, description?, owner?}: title is required, and a title with nothing in it or over %d characters (UTF-16 units, counted as an issue title is) is 400 PRIORITY_INPUT; id is the caller's and defaults to the title written as a slug, and an id that is no slug of lowercase letters, digits and single hyphens, one over %d characters, the ids none and any (the issue listing reads advances=none as the work under no priority and advances=any as the work under any), and a title no slug can be written from are each 400 PRIORITY_INPUT, an id derived from the title as much as one given; an id the store already holds is 409 PRIORITY_EXISTS. The new priority takes the last place of the order. A credential scoped to one project is refused, since a priority belongs to none.", contracts.IssueTitleMax, priorityIDMax),
	Handler:     (*server).createPriority,
	WriteClass:  scopeDeployment,
	Shape: routeShape{
		Status: http.StatusCreated, Response: model.Priority{}, Request: createPriorityInput{},
	},
})
