package daemon

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/verdict"
)

// mergeGroupHandler is the intake handler of a merge queue's group (intake.MergeGroup), and of
// nothing else: on checks_requested it asks, on the fact's commit, for a write of legion-verdict
// on the group's commit (queueMergeGroupWrite), which the writer makes outside the fact's
// transaction; on destroyed, and on any other action, it asks for nothing (spec R6).
type mergeGroupHandler struct{ w *workflowRuntime }

func (h mergeGroupHandler) Apply(ctx context.Context, _ pgx.Tx, fact intake.Fact) (intake.Result, error) {
	group, ok := fact.(intake.MergeGroup)
	if !ok || !group.ChecksRequested() {
		return intake.Result{}, nil
	}
	repository, err := ghrepo.Parse("the repository of a merge group", group.Repo)
	if err != nil {
		return intake.Result{}, err
	}
	intake.OnCommit(ctx, func() { h.w.queueMergeGroupWrite(repository, group.Number, group.HeadSHA, false) })
	return intake.Result{}, nil
}

// queueMergeGroupWrite queues, for the writer (writeVerdicts), a write of legion-verdict on group,
// the commit of a merge queue's group for pull request number of repository (spec R6): a
// merge_group event's checks_requested asks for it, and so does the writers' sweep for each group
// commit it finds with no run of this daemon's. overForeign is writeDecided's. A repository no
// verdict entry names asks nothing, as it asks nothing of queueVerdictWrite, and neither does a
// runtime with no writer. No merge-state read waits on it: a group is no head an issue's reads are
// of.
func (w *workflowRuntime) queueMergeGroupWrite(repository ghrepo.Repository, number int, group string, overForeign bool) {
	if w.verdicts == nil {
		return
	}
	if _, ok := w.verdictEntry(repository); !ok {
		return
	}
	w.verdicts.add(verdictTarget{repository: repository, number: number, group: group, overForeign: overForeign})
}

// writeMergeGroup writes legion-verdict on target's group commit from the verdict of its pull
// request at the head that pull request stands at now (decideVerdict at currentHead): success
// where the verdict stands there, and failure where it does not (spec R6). A group with no verdict
// is failure, not nothing, so the queue removes the entry at once rather than at its check's
// timeout. The group commit holds the pull requests ahead of it in the queue too, each with a
// group of its own that the queue also waits for, so only the pull request the group's ref names
// is read. Another project's issue branch is that project's daemon's to write, and is logged once
// per pull request, as writeVerdictOver logs it.
func (w *workflowRuntime) writeMergeGroup(ctx context.Context, target verdictTarget) error {
	entry, ok := w.verdictEntry(target.repository)
	if !ok {
		return nil
	}
	decided, err := w.decideVerdict(ctx, entry, target.repository, target.number, currentHead)
	if err != nil {
		return err
	}
	if decided.otherIssue != "" {
		w.noteOtherProject(target.repository.String(), target.number, decided.otherIssue)
		return nil
	}
	if decided.conclusion != verdict.Success {
		decided.conclusion = verdict.Failure
	}
	return w.writeDecided(ctx, verdictTarget{repository: target.repository, number: target.number, head: target.group}, decided, target.overForeign)
}
