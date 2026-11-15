// legion_metrics.go serves the Delivery page's Legion section: the four measures of
// dispatch://INFRA-1077, computed from Dispatch's own event log, the handoffs, and what the
// sampler recorded of the project's Legion daemon.
//
// The handler is the read-and-compute split api/delivery_pipeline.go uses: metrics.Read makes
// every database and GitHub read the answer needs and metrics.Compute turns that population into
// the body without touching either, so the live hub recomputes the same numbers from a population
// it already holds rather than carrying a second copy of the rules.
package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/legion/metrics"
)

// getLegionMetrics answers GET /api/v1/projects/{key}/legion/metrics: the four measures of the
// project the path names. Its parameters are `from` and `to` (RFC3339; with neither, the last 7
// days, as GET /api/v1/delivery/measures reads its window) and `scope` (all, roots or children).
//
// The project is the path's, and nothing picks one for the caller: R26 of
// dispatch://INFRA-979's spec addresses everything of one project under its own path, and R34
// gives a cross-project read no default project. A project with nothing measured — one that has
// never named a Legion daemon among them — answers an empty report rather than a refusal.
func (s *server) getLegionMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuthenticated(w, r) {
		return
	}
	query := r.URL.Query()
	from, to, err := parseDeliveryWindow(query)
	if err != nil {
		writeError(w, "INVALID_QUERY", http.StatusBadRequest, err.Error())
		return
	}
	scope, err := metrics.ParseScope(strings.TrimSpace(query.Get("scope")))
	if err != nil {
		writeError(w, "INVALID_QUERY", http.StatusBadRequest, err.Error())
		return
	}
	project, ok := s.legionProjectNamed(w, r, strings.TrimSpace(r.PathValue("key")), "INVALID_QUERY")
	if !ok {
		return
	}
	from, to = from.UTC(), to.UTC()
	population, err := metrics.Read(r.Context(), s.deps.Store.Pool, s.deps.GitHub, project, from, to)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, metrics.Compute(population, scope, time.Now().UTC()))
}
