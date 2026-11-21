package api

import (
	"context"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// answerIssueChecks computes the body GET /api/v1/issues/{key}/results answers for issueKey: the
// checks of the issue's Verification document as the block index holds them, each with its
// results, what they are compared with and whether a pass of it stands, at at (noCommitNamed for
// the heads and branches each check compares with). held is false where the issue has no
// Verification document, and the body then holds no check. trees is the request's display tree
// reader, nil for a caller that asked for no tree.
//
// It is the whole computation of the read, kept apart from the route so the check states
// settler (check_states.go) publishes the very states the read answers. The route keeps what is
// the route's alone: the query, the 404 for an issue nobody has, and the answer without the
// computed state under an end-to-end test's hide-state file (listCheckResults).
func (s *server) answerIssueChecks(ctx context.Context, issueKey, at string, trees *displayTreeReader) (issueCheckResults, bool, error) {
	artifact, held, err := s.verificationDocument(ctx, s.deps.Store.Pool, issueKey, "")
	if err != nil {
		return issueCheckResults{}, false, err
	}
	body := issueCheckResults{Checks: []model.CheckBlock{}, Issue: issueKey, VerificationArtifactID: artifact.ID}
	if !held {
		return body, false, nil
	}
	checks, err := s.checkBlocksOf(ctx, s.deps.Store.Pool, artifact, trees)
	if err != nil {
		return issueCheckResults{}, false, err
	}
	recorded, err := loadCheckResults(ctx, s.deps.Store.Pool, artifact.ID)
	if err != nil {
		return issueCheckResults{}, false, err
	}
	retracted, err := loadCheckResultRetractions(ctx, s.deps.Store.Pool, artifact.ID)
	if err != nil {
		return issueCheckResults{}, false, err
	}
	for index := range checks {
		results := recorded[checks[index].BlockID]
		if results == nil {
			results = []model.CheckResult{}
		}
		for result := range results {
			if retraction, held := retracted[results[result].ID]; held {
				results[result].Retracted = &retraction
			}
		}
		checks[index].Results = results
	}
	headCI, err := s.deriveCIResults(ctx, issueKey, checks)
	if err != nil {
		return issueCheckResults{}, false, err
	}
	// Every head GitHub has to be asked for is learned before the first check is answered, so
	// two `before_merge` checks of one issue name one list of bases and not two
	// (comparisonReader.learn).
	reader := newComparisonReader(s, at)
	reader.headCI = headCI
	reader.learn(ctx, checks)
	for index := range checks {
		reader.answer(ctx, &checks[index])
	}
	answerCompatibilityCurrency(checks, at)
	keepFieldsOlderCommandsRead(checks)
	// Each result's observed text is registered for its tree once no check's results are copied
	// any more, since what is registered is a pointer into the row: deriveCIResults replaces a
	// check's results, and keepFieldsOlderCommandsRead moves them into new lists, results and the
	// recorded_results of a check CI proves, whose results take their trees as well.
	for index := range checks {
		for result := range checks[index].Results {
			trees.text(&checks[index].Results[result].ObservedTree, checks[index].Results[result].Observed)
		}
		for result := range checks[index].RecordedResults {
			trees.text(&checks[index].RecordedResults[result].ObservedTree, checks[index].RecordedResults[result].Observed)
		}
	}
	body.Checks = checks
	trees.resolve()
	return body, true, nil
}
