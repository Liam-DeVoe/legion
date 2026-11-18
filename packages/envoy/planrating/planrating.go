// Package planrating holds the scale of a retro's rating of the plan its issue ran, and the most
// characters of the rating's reason, for a caller outside this module.
//
// It exists because the Legion daemon (packages/daemon, module github.com/sjawhar/legion/daemon)
// is a Go module of its own, and Go's internal rule bars it from
// github.com/sjawhar/envoy/internal/contracts, where these numbers are generated from
// packages/contracts (LEGION_PLAN_RATING_LEAST, LEGION_PLAN_RATING_MOST and
// LEGION_PLAN_REASON_MAX). This package is the one public door onto them, so the daemon's
// completion route holds a rating to the scale Dispatch's record of the retro takes and the
// plugins' tools state, with no copy of its own.
//
// No package of the envoy module imports this one, for the reason sessiontiming gives: the
// listener image's build context holds only go.mod, go.sum, cmd, internal, listenfd and the other
// directories packages/envoy/docker/Dockerfile copies. An envoy package that needs one of these
// numbers reads internal/contracts directly.
package planrating

import "github.com/sjawhar/envoy/internal/contracts"

// Least and Most are the scale of a rating, Least the plan cost the work and Most the plan fit the
// work, and ReasonMax the most characters, counted as Unicode code points, a rating's reason holds.
// Each is the generated contracts constant itself.
const (
	Least     = contracts.PlanRatingLeast
	Most      = contracts.PlanRatingMost
	ReasonMax = contracts.PlanReasonMax
)
