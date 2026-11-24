package api

import (
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodGet,
	Pattern:     "/api/v1/artifacts/_test/rooms",
	Auth:        authAny,
	Description: "Test hook: list the documents this process holds a room of.",
	Handler:     (*server).listResidentRooms,
	TestHook:    true,
	Shape:       routeShape{Status: http.StatusOK, Response: residentRooms{}},
})
