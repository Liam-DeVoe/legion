package api

import (
	"net/http"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
)

// v2Infra979Rows are the rows of the INFRA-979 tree (infra979Rows), each served in both versions
// by one declaration, at /api/v1 where that tree wrote it and at the same path under /api/v2. Each
// keeps its v1 handler, its description and its shape: that tree's own spec decides their change,
// in both versions (R3), and the parent's shape rules bind none of them, so they declare none of
// the R1 columns. The facts the side maps held for them are their columns now.
func (s *server) v2Infra979Rows() []routeRow {
	return []routeRow{
		{
			Method: http.MethodGet, Pattern: "/settings/delivery", Auth: authAny,
			Description: "The delivery timeline's one configuration record (deploy repository, workflow paths, production job names, optional production apply step, population authors, excluded repositories, and the Pipeline page's optional staging end-to-end, staging deploy and PR-checks roll-up job names), or null when it has not been set.",
			Handler:     s.getDeliverySettings,
			Shape: routeShape{
				Status: http.StatusOK, Response: delivery.DeliverySettings{},
				// A deployment that has not set them answers 200 and the JSON literal `null`
				// (settings_delivery.go).
				Nullable: true,
			},
		},
		{
			Method: http.MethodPut, Pattern: "/settings/delivery", Auth: authAny,
			Description: "Set the delivery timeline's configuration after proving the GitHub App can read the deploy repository; 409 DELIVERY_SETTINGS_ACCESS a credential or repository problem.",
			Handler:     s.putDeliverySettings,
			Body:        requestBody{Kind: bodyJSON, Type: deliverySettingsInput{}},
			Shape:       routeShape{Status: http.StatusOK, Response: delivery.DeliverySettings{}},
			// A deployment-wide setting, which a credential scoped to one project may not change.
			Scope: scopeDeployment,
		},
		{
			Method: http.MethodGet, Pattern: "/delivery/timeline", Auth: authAny,
			Description: "The delivery timeline within [from, to) (default the last 7 days), the search q (title or PR id) and the given facets (repeatable repo, parent_agent, session, issue, priority (P0-P3), component (a parent includes its children), author, rework, deployed; __no_issue__, __no_priority__, __no_component__ and __no_session__ select by cause): merges with each PR's issue title, priority and effective components, successful production deploys with every window PR they shipped first, pipeline failures with each run's production job and failing step, and merged work waiting to deploy, each PR's deployed_status (deployed, waiting, not_tracked) computed from stored facts; facet_counts (each facet counted with every other facet applied and its own ignored), color_counts (with none applied), the components and issue titles the window names, data freshness, and measures (what GET /api/v1/delivery/measures answers for the same window, search and facets); 404 DELIVERY_NOT_CONFIGURED without a delivery_settings row.",
			Handler:     s.getDeliveryTimeline,
			Query:       deliveryWindowQuery{},
			Shape:       routeShape{Status: http.StatusOK, Response: deliveryTimelineWithMeasures{}},
		},
		{
			Method: http.MethodGet, Pattern: "/delivery/measures", Auth: authAny,
			Description: "The four delivery measures and Sami's KPI targets for [from, to) (default the last 7 days) and the same facets the timeline takes: deploys a day, lead time (merge, first commit and open to production; open to merge), change failure rate per PR and per deploy (flags_source says whether stored flags fed it), time to restore, rework share, deploy-run success, a per-UTC-day series, the open P0 issues with no owner, and whether each target is met; 404 DELIVERY_NOT_CONFIGURED without a delivery_settings row.",
			Handler:     s.getDeliveryMeasures,
			Query:       deliveryWindowQuery{},
			Shape:       routeShape{Status: http.StatusOK, Response: deliveryMeasuresResponse{}},
		},
		{
			Method: http.MethodGet, Pattern: "/delivery/pipeline", Auth: authAny,
			Description: "The Pipeline page: over the seven UTC days ending today (now, or ?at= for a fixed day), by the day each run started, the PR-checks workflow's run wall time and success, its five slowest jobs (50 timed results to rank) and the jobs that failed in the last 24 hours, the duration and result of the staging end-to-end, staging deploy and production deploy jobs the settings name in push runs on main, and merge-to-production and PR-open-to-merge per merge day against their targets; not filtered by the timeline's window or facets; 400 INVALID_QUERY for a malformed at, 404 DELIVERY_NOT_CONFIGURED without a delivery_settings row.",
			Handler:     s.getDeliveryPipeline,
			Query:       deliveryPipelineQuery{},
			Shape:       routeShape{Status: http.StatusOK, Response: deliveryPipelineResponse{}},
		},
	}
}
