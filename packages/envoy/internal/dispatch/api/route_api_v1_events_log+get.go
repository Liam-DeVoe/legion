package api

import (
	"net/http"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodGet,
	Pattern:     "/api/v1/events/log",
	Auth:        authAny,
	Description: "Page the whole event log by id, whatever owns each event (an issue, a document, a project or none): the events with id > after (default 0), oldest first, at most limit (1 to 1000, default 1000). An event's actor carries proven: true when the write's credential proved it (a browser's person, a broker-issued token's session), and no proven when the request only named it.",
	Handler:     (*server).listEventLog,
	Shape:       routeShape{Status: http.StatusOK, Response: []model.Event{}, Params: eventLogQuery{}},
})
