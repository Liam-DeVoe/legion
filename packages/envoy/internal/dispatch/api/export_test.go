package api

import (
	"context"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The test that runs a related-item check's record write against an erase of the check's source
// lives in package api_test, because grapherase imports api and a test of package api could not
// import grapherase. The names below are what it reaches for across that boundary, and nothing
// more: they are part of no package's surface but the test binary's.

// RecordDecisionBlockCheck runs the record write of one related-item check of decision block
// blockID of document (recordRelatedChecks), keyed by issue where issue is not empty and by the
// document otherwise, holding terms, on a server of its own over database, and returns once that
// server's detached work - the record's retry, where its first attempt ran out of time - has
// ended as well.
func RecordDecisionBlockCheck(ctx context.Context, database *store.Store, project, issue, document, blockID string, terms []string) {
	srv := directServer(Deps{Store: database})
	source := suggestionSource{
		kind: "decision_block", blockID: blockID, blockDocument: document, actor: model.Actor{Kind: "user", ID: "alice"},
	}
	if issue != "" {
		source.issueKey = issue
	} else {
		source.artifactID = document
	}
	srv.recordRelatedChecks(ctx, relatedCheckRecord{
		source:      source,
		suggestions: &model.Suggestions{Outcome: relatedNone, Search: relatedKeywordsOnly, Related: []model.WriteSuggestion{}},
		record:      relatedRecord{project: project, terms: terms, totalMS: 40},
	})
	srv.detached.wait()
}
