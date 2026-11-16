package api

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// coverage.go answers what each requirement of an issue's spec lacks. The requirements are the
// `requirement` blocks of the issue's spec, in document order. A requirement has a check when a
// `check` block of any document of that issue or of any descendant issue, at any status, names
// it in `serves`, and a plan task when a `plan_task` block of that set does; the descendants are
// read with refs.IssueDescendants, the server's one recursive `child_of` subtree walk. The
// missing-plan-task gap is named only once the issue has a plan document, since the plan comes
// after the spec's approval. A requirement whose block's node state is proposed is a question
// still open (dispatch://INFRA-1950/spec#b-r-proposed), so it raises neither gap here and the
// approval request's answer names none for it; one at settled or live is counted as every
// requirement is, and dispatch://INFRA-1253 gives overtaken its meaning. A check or a plan task
// whose block is proposed is a question still open too, so it serves nothing yet and its
// requirement is counted without it (servingBlockCounts). Nothing is ever refused
// for a gap: three readers show it — the route below for the Spec tab, `dispatch read --issue`,
// which prints each gap after `Coverage: `, and an approval request's answer, which names the
// missing checks alone.

// noRequirementsGap is the one gap line a spec that marks no requirement answers, in place of a
// line per requirement. noCheckReason and noPlanTaskReason are what one requirement lacks, as
// RequirementCoverage.Gaps carries it; a document-level gap line is the requirement's block id
// and one of these, which is what noCheckGap builds.
const (
	noRequirementsGap = "the spec marks no requirements"
	noCheckReason     = "has no check"
	noPlanTaskReason  = "has no plan task"
)

// GET /api/v1/issues/{key}/coverage  (any authenticated caller)
//
// What each requirement of the issue's spec lacks. An issue with no spec, or a spec that marks
// no requirements, answers no requirements and the single gap line.
func (s *server) getIssueCoverage(w http.ResponseWriter, r *http.Request) {
	who, ok := s.actingReader(w, r)
	if !ok {
		return
	}
	issueKey := r.PathValue("key")
	if err := s.requireIssueExists(r.Context(), issueKey); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	coverage, err := s.issueCoverage(r.Context(), s.deps.Store.Pool, who, issueKey)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, coverage)
}

// issueCoverage reads the issue's requirements and what serves each, for who: an issue below it
// who may not see serves nothing here (refs.IssueDescendants). The documents' blocks are read
// from the block index (document_blocks and block_refs), which every accepted write leaves in
// step with the document it wrote; the requirements' order and words come from the live spec,
// and each one's node state from the store (requirementStates).
func (s *server) issueCoverage(ctx context.Context, q queryer, who model.Actor, issueKey string) (model.IssueCoverage, error) {
	coverage := model.IssueCoverage{Issue: issueKey, Requirements: []model.RequirementCoverage{}, Gaps: []string{}}
	var specRefKey string
	err := q.QueryRow(ctx, `
		select coalesce((select a.id::text from artifacts a where a.issue_key = $1 and a.is_primary), ''),
		       coalesce((select a.ref_key from artifacts a where a.issue_key = $1 and a.is_primary), ''),
		       exists (select 1 from artifacts a where a.issue_key = $1 and `+aliasedPlanRole+`)
	`, issueKey).Scan(&coverage.SpecArtifactID, &specRefKey, &coverage.HasPlan)
	if err != nil {
		return model.IssueCoverage{}, fmt.Errorf("read the documents of %s: %w", issueKey, err)
	}
	if coverage.SpecArtifactID == "" {
		coverage.Gaps = []string{noRequirementsGap}
		return coverage, nil
	}
	requirements, err := s.requirementBlocksIn(ctx, coverage.SpecArtifactID)
	if err != nil {
		return model.IssueCoverage{}, err
	}
	if len(requirements) == 0 {
		coverage.Gaps = []string{noRequirementsGap}
		return coverage, nil
	}
	states, err := requirementStates(ctx, q, who, specRefKey, requirements)
	if err != nil {
		return model.IssueCoverage{}, err
	}
	descendants, err := refs.IssueDescendants(ctx, q, who, issueKey)
	if err != nil {
		return model.IssueCoverage{}, err
	}
	served, err := servingBlockCounts(ctx, q, who, coverage.SpecArtifactID, append([]string{issueKey}, descendants...))
	if err != nil {
		return model.IssueCoverage{}, err
	}
	coverage.Requirements = make([]model.RequirementCoverage, len(requirements))
	for index, requirement := range requirements {
		requirement.Ref, _ = text.FormatBlockRef(text.Ref{
			Kind: "block", IssueKey: issueKey, ID: "spec", Block: requirement.BlockID,
		})
		counts := served[requirement.BlockID]
		requirement.Checks, requirement.PlanTasks = counts.checks, counts.planTasks
		requirement.State = cmp.Or(states[requirement.BlockID], graphstore.StateLive)
		requirement.Gaps = []string{}
		// A proposed requirement is a question still open, never work left undone.
		if requirement.State != graphstore.StateProposed {
			if requirement.Checks == 0 {
				requirement.Gaps = append(requirement.Gaps, noCheckReason)
			}
			if coverage.HasPlan && requirement.PlanTasks == 0 {
				requirement.Gaps = append(requirement.Gaps, noPlanTaskReason)
			}
		}
		coverage.Requirements[index] = requirement
		for _, reason := range requirement.Gaps {
			coverage.Gaps = append(coverage.Gaps, requirement.BlockID+" "+reason)
		}
	}
	return coverage, nil
}

// requirementStatesQuery reads the node state of each requirement block whose node id is in $1
// (`<ref_key>#<block id>`, kind block) for a reader ($2), in one statement: each block through the
// block kind's predicate (refs.VisibleKind), with the state and the visibility the store holds of
// it (refs.StoredNodeOf), null where the store holds no row of it.
var requirementStatesQuery = `
	select requirement.id, ` + refs.StoredNodeOf(`'`+refs.BlockNodeKind+`'`, `requirement.id`) + `
	from unnest($1::text[]) as requirement(id)
	where ` + refs.VisibleKind(refs.BlockNodeKind, `requirement.id`, `$2`)

// requirementStates is the node state of each requirement of the spec whose ref_key is refKey,
// by block id, for who, read in one statement (requirementStatesQuery). A requirement the store
// holds no row of reads live, as every read of a node answers one (refs.StoredStateAndVisibility);
// one the predicate hides from who answers no state, and the caller reads it live.
func requirementStates(ctx context.Context, q queryer, who model.Actor, refKey string, requirements []model.RequirementCoverage) (map[string]string, error) {
	ids := make([]string, len(requirements))
	for index, requirement := range requirements {
		ids[index] = refKey + "#" + requirement.BlockID
	}
	rows, err := q.Query(ctx, requirementStatesQuery, ids, refs.VisibilityArg(who))
	if err != nil {
		return nil, fmt.Errorf("read the node state of the requirements of %s: %w", refKey, err)
	}
	defer rows.Close()
	states := make(map[string]string, len(requirements))
	for rows.Next() {
		var id string
		var stored []string
		if err := rows.Scan(&id, &stored); err != nil {
			return nil, fmt.Errorf("read the node state of the requirements of %s: %w", refKey, err)
		}
		state, _, ok := refs.StoredStateAndVisibility(stored)
		if !ok {
			return nil, fmt.Errorf("the requirement %s holds a stored state and visibility of %d values", id, len(stored))
		}
		states[strings.TrimPrefix(id, refKey+"#")] = state
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the node state of the requirements of %s: %w", refKey, err)
	}
	return states, nil
}

// noCheckGap is the gap line a requirement with no check earns, written in one place: the
// route's gap list, which builds it from the requirement's own reason, and the approval
// request's answer (missingCheckGaps) carry the same words.
func noCheckGap(blockID string) string {
	return blockID + " " + noCheckReason
}

// missingCheckGaps is the gap lines an approval request's answer carries: one per requirement
// with no check, and never a missing plan task, since the plan comes after the approval. A
// requirement at proposed is a question still open and carries none, as the route's gaps carry
// none for it. A spec that marks no requirements carries the one line that says so, as the route
// does, so the approver is told that too; a spec every requirement of which has a check carries
// none.
func missingCheckGaps(coverage model.IssueCoverage) []string {
	if len(coverage.Requirements) == 0 {
		return []string{noRequirementsGap}
	}
	gaps := []string{}
	for _, requirement := range coverage.Requirements {
		if requirement.Checks == 0 && requirement.State != graphstore.StateProposed {
			gaps = append(gaps, noCheckGap(requirement.BlockID))
		}
	}
	return gaps
}

// requirementBlocksIn is every `requirement` block of one document, in document order, with its
// words, read live out of the document's markdown (typedBlocksIn).
func (s *server) requirementBlocksIn(ctx context.Context, artifactID string) ([]model.RequirementCoverage, error) {
	var requirements []model.RequirementCoverage
	err := s.typedBlocksIn(ctx, artifactID, "requirement", func(node *pmdoc.Node, id string) {
		requirements = append(requirements, model.RequirementCoverage{BlockID: id, Text: blockWords(node)})
	})
	if err != nil {
		return nil, err
	}
	return requirements, nil
}

// servingCounts is how many `check` and `plan_task` blocks serve one requirement.
type servingCounts struct {
	checks    int
	planTasks int
}

// servingBlockCounts counts, for each block of the spec, the `check` and `plan_task` blocks of
// the documents of issues that name it in `serves`. Both ends are current blocks of their
// documents: deleting the check deletes its row, so its requirement is uncovered again. A check or
// a plan task whose block's node state is proposed is a question still open and serves nothing
// yet (dispatch://INFRA-1950/spec#b-r-proposed), so it is not counted; one at any other state is,
// and one the store holds no row of reads live, as every read of a node answers one. Its state is
// read for who in the same statement, as requirementStatesQuery reads a requirement's: from the
// store's row of the block (kind block, keyed by the row's node_id), through the block kind's
// predicate over that row (refs.VisibleKindRow), with the kind, the state and the reader bound. So
// a block the predicate hides from who answers who no state and is counted as live, and the count
// tells who nothing of the state of a node who may not see.
func servingBlockCounts(ctx context.Context, q queryer, who model.Actor, specArtifactID string, issues []string) (map[string]servingCounts, error) {
	rows, err := q.Query(ctx, `
		select t.block_id, s.type, count(*)
		from block_refs r
		join document_blocks t on t.node_id = r.to_id and t.artifact_id = $1::uuid and t.type = 'requirement'
		join document_blocks s on s.artifact_id = r.artifact_id and s.block_id = r.block_id
		join artifacts a on a.id = r.artifact_id
		where r.kind = 'serves' and r.to_kind = 'block'
		  and a.issue_key = any($2) and s.type in ('check', 'plan_task')
		  and not exists (
		      select 1 from graph_nodes serving
		      where serving.kind = $3 and serving.id = s.node_id and serving.state = $4
		        and `+refs.VisibleKindRow(refs.BlockNodeKind, "serving", "$5")+`)
		group by t.block_id, s.type
	`, specArtifactID, issues, refs.BlockNodeKind, graphstore.StateProposed, refs.VisibilityArg(who))
	if err != nil {
		return nil, fmt.Errorf("read what serves the requirements of %s: %w", specArtifactID, err)
	}
	defer rows.Close()
	served := map[string]servingCounts{}
	for rows.Next() {
		var blockID, blockType string
		var count int
		if err := rows.Scan(&blockID, &blockType, &count); err != nil {
			return nil, fmt.Errorf("read what serves the requirements of %s: %w", specArtifactID, err)
		}
		counts := served[blockID]
		if blockType == "check" {
			counts.checks += count
		} else {
			counts.planTasks += count
		}
		served[blockID] = counts
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read what serves the requirements of %s: %w", specArtifactID, err)
	}
	return served, nil
}

// approvalCoverage is the coverage an approval request's answer carries for the document id
// names, read for who, the requester, and whether that coverage could not be read. The gaps are
// the issue's, when that document is the issue's spec (isSpecDocument), and none for any other
// document.
//
// It is read before the request opens its transaction. Reading it folds the issue's live spec,
// which for a document no room holds is a cold read bounded by a minute (docs/read_cache.go),
// and no owner row lock is held across a read that long: four writers parked on one issue's row
// lock already cost a read of an unrelated issue the whole seventeen seconds the lock was held
// (store/pool.go). Nothing the lock protects is settled here - the coverage is a report the
// answer carries, and a gap refuses nothing.
//
// A coverage read that fails does not fail the request: it is logged here, the request is
// recorded as it was before the answer carried any coverage at all, and unread says the answer
// could not count the gaps rather than that it found none. Reading the document itself is the
// request's error, as it is inside the transaction.
func (s *server) approvalCoverage(ctx context.Context, q queryer, who model.Actor, id string) (gaps []string, unread bool, err error) {
	artifact, err := s.loadArtifact(ctx, q, id)
	if err != nil {
		return nil, false, err
	}
	if !isSpecDocument(artifact) {
		return []string{}, false, nil
	}
	coverage, err := s.issueCoverage(ctx, q, who, *artifact.IssueKey)
	if err != nil {
		slog.Warn("dispatch: read the coverage an approval request reports", "artifact", artifact.ID, "error", err)
		return []string{}, true, nil
	}
	return missingCheckGaps(coverage), false, nil
}
