package api

import (
	"context"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
)

// results_merge.go holds what one results read knows of the merges of the pull requests it
// orders: whether a set of merged pull request rows can be ordered by when each merged
// (fillMergeTimes), over the read's memo (mergeTimeFills).

// mergeTimeFills is one results read's memo of the merge times it learned (fillMergeTimes):
// listCheckResults makes it before deriveCIResults, and the comparisonReader holds the same one
// (comparisonReader.fills), so the read learns no merge time twice. This form of the fill learns
// none, and the memo holds nothing.
type mergeTimeFills struct{}

// fillMergeTimes reports whether rows, the merged pull request rows one read orders by when each
// merged, can be ordered (ordered): a set of fewer than two rows needs no order, and a set holding
// a row with no time of merge cannot be ordered, since the row ordered last may have merged last.
// So no read names an older merge as the newest. Where it learns a row's time, it sets that row's
// MergedAt in rows, so the caller orders by it. This form learns no time: it reads no GitHub and
// changes no row, and a set that holds a row with no time stays one that cannot be ordered.
func (s *server) fillMergeTimes(ctx context.Context, fills *mergeTimeFills, rows []delivery.MergeTime) (ordered bool) {
	if len(rows) < 2 {
		return true
	}
	for _, row := range rows {
		if row.MergedAt == nil {
			return false
		}
	}
	return true
}
