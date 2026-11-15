package workflow

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// mergeHoldsChanged tells each role that a line of the project's merge holds document can hold
// that the document changed (intake.MergeHoldsChanged). The architect that owns each issue in
// accepting, whose sign_off a line can hold, gets a merge-holds-changed notice. The merger of each
// issue in merging, whose merge a line can hold, gets a row of its own (record.MergeHoldsChanged),
// since a notice reaches only an architect. Both carry one text (record.MergeHoldsChangedReason).
//
// A role that a hold stopped ends its turn with a WAITING line and subscribes to nothing: under
// `runtime: kubernetes` a Dispatch document event does not wake a released pod, and an outbox
// delivery does (the daemon's wakeForDelivery). So these rows are what wake a held role when the
// controller lifts the hold, and what tell a role that a new hold applies to it.
//
// The daemon does not read the document, so it does not know which issue a line holds. It tells
// every issue in the two phases, held or not, and each role reads the document itself. An issue
// that has left its phase (leftPhase: out of the workflow, or its tree lingers) is told nothing. A
// fact of a project other than the engine's writes nothing, and the decoder gives none
// (intake's decodeDispatchDocumentFact). A second delivery of the same event writes nothing, since
// ApplyFact records the event and skips it. The fact moves no phase, writes no slot and wakes no
// claim itself: each row wakes its own role when it runs.
func (e *Engine) mergeHoldsChanged(ctx context.Context, tx pgx.Tx, fact intake.MergeHoldsChanged) (intake.Result, error) {
	if fact.Project != e.cfg.Project {
		return intake.Result{}, nil
	}
	issues, err := e.store.LiveIssues(ctx, tx, e.cfg.Project)
	if err != nil {
		return intake.Result{}, err
	}
	reason := record.MergeHoldsChangedReason(fact.Project)
	lingering := map[string]bool{}
	var architects, mergers []string
	for _, issue := range issues {
		if issue.Phase != phase.Accepting && issue.Phase != phase.Merging {
			continue
		}
		lingers, read := lingering[issue.Tree]
		if !read {
			if lingers, err = record.TreeLingers(ctx, e.store, tx, issue.Tree); err != nil {
				return intake.Result{}, err
			}
			lingering[issue.Tree] = lingers
		}
		if leftPhase(issue, issue.Phase, lingers) != "" {
			continue
		}
		if issue.Phase == phase.Accepting {
			if err := e.notice(ctx, tx, issue.Key, record.Notice{Kind: "merge-holds-changed", Role: claim.RoleArchitect,
				Phase: phase.Accepting, Reason: reason}); err != nil {
				return intake.Result{}, err
			}
			architects = append(architects, issue.Key)
			continue
		}
		if err := e.enqueue(ctx, tx, issue.Key, record.MergeHoldsChanged{Project: fact.Project}); err != nil {
			return intake.Result{}, err
		}
		mergers = append(mergers, issue.Key)
	}
	e.logOnCommit(ctx, "workflow: the project's merge holds changed; the architects in accepting and the mergers in merging are told",
		"event", fact.Event, "accepting", architects, "merging", mergers)
	return intake.Result{}, nil
}

// MergingNow reads in tx what the merging phase is judged by when no pull request is in question:
// the issue and whether its tree lingers (record.TreeLingers). It returns the issue, and why the
// workflow no longer works the issue's merging phase (leftPhase), "" while it does. An issue the
// record does not hold is a nil issue and no reason. The fact that writes the merger's row of a
// merge holds change (mergeHoldsChanged) and the daemon's executor of that row judge the issue by
// this one rule, so the two do not disagree about an issue that left merging. It is exported for
// that executor (the daemon's mergeHoldsChanged).
func MergingNow(ctx context.Context, store record.Store, tx pgx.Tx, key string) (*record.Issue, string, error) {
	issue, err := store.Issue(ctx, tx, key)
	if err != nil || issue == nil {
		return nil, "", err
	}
	lingers, err := record.TreeLingers(ctx, store, tx, issue.Tree)
	if err != nil {
		return nil, "", err
	}
	return issue, leftPhase(*issue, phase.Merging, lingers), nil
}
