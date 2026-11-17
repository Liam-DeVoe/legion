package workflow

import (
	"context"
	"fmt"

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
// that has left its phase is told nothing (leftHeldPhase: out of the workflow, its tree lingers,
// or its pull request has merged). A fact of a project other than the engine's writes nothing,
// and the decoder gives none (intake's decodeDispatchDocumentFact). A second delivery of the same
// event writes nothing, since ApplyFact records the event and skips it. The fact moves no phase,
// writes no slot and wakes no claim itself: each row wakes its own role when it runs.
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
		left, err := leftHeldPhase(ctx, e.store, tx, issue, issue.Phase, lingers)
		if err != nil {
			return intake.Result{}, err
		}
		if left != "" {
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

// MergingNow reads in tx what the merging phase of a merge holds change is judged by: the issue,
// whether its tree lingers (record.TreeLingers) and whether its pull request has merged
// (PhaseFinished). It returns the issue, and why the workflow no longer works the issue's merging
// phase for a merge hold (leftHeldPhase), "" while it does. An issue the record does not hold is
// a nil issue and no reason. The fact that writes the merger's row of a merge holds change
// (mergeHoldsChanged) and the daemon's executor of that row judge the issue by this one rule, so
// the two do not disagree about an issue that left merging. It is exported for that executor (the
// daemon's mergeHoldsChanged).
func MergingNow(ctx context.Context, store record.Store, tx pgx.Tx, key string) (*record.Issue, string, error) {
	issue, err := store.Issue(ctx, tx, key)
	if err != nil || issue == nil {
		return nil, "", err
	}
	lingers, err := record.TreeLingers(ctx, store, tx, issue.Tree)
	if err != nil {
		return nil, "", err
	}
	left, err := leftHeldPhase(ctx, store, tx, *issue, phase.Merging, lingers)
	if err != nil {
		return nil, "", err
	}
	return issue, left, nil
}

// leftHeldPhase says why the workflow no longer works issue's phase at for a merge hold, "" while
// it does: the issue still works at (leftPhase), and the phase's own terminating fact has not
// arrived (PhaseFinished). A root whose pull request merged stands in merging until every issue
// under it leaves the workflow (closeMerged), and a hold stops nothing there: its merger merged
// already, or none ran because the pull request had merged when the phase began. A row for such a
// root would either wait, tried again, on a merger no machine holds, keeping the pod from its
// idle release (the store's idleWaitingKinds), or wake a released merger to begin step 1 again
// with nothing to merge. The merge-queue rows pass over the same root, since their rule
// (leftMergingAt) needs an open pull request. Accepting has no such fact, so an issue in accepting
// is judged by leftPhase alone.
func leftHeldPhase(ctx context.Context, store record.Store, tx pgx.Tx, issue record.Issue, at phase.Phase, lingers bool) (string, error) {
	if left := leftPhase(issue, at, lingers); left != "" {
		return left, nil
	}
	finished, err := PhaseFinished(ctx, store, tx, issue)
	if err != nil || !finished {
		return "", err
	}
	return fmt.Sprintf("the pull request is %s", record.PullRequestMerged), nil
}
