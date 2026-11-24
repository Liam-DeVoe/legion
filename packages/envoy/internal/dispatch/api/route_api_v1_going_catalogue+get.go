package api

import (
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodGet,
	Pattern:     goingCataloguePath,
	Auth:        authAny,
	Description: "The catalogue of operations an agent may propose on Dispatch's graph: twenty of the twenty-one dispatch tools that write, of the thirty-seven tool specs agents operate Dispatch with, each carried whole - its own name, its own description, the arguments it takes with an example call, the rows of this table it writes, and the graph relations its verbs write. The one it leaves out is dispatch_review, on purpose: a review records an approval or a changes request in a person's name, and the loop that proposes operations must never propose one. Twenty actions is a list read whole, so the route takes no query and serves no page. An action the catalogue does not hold is no action at all: nothing serves it.",
	Handler:     (*server).getGoingCatalogue,
	Shape:       routeShape{Status: http.StatusOK, Response: goingCatalogueBody{}},
})
