package daemon

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// verdictFacts is the intake handler of the facts a verdict reads that the workflow does not
// record: an outside pull request's sighting and its reviews, and the reviews of a Legion head
// delivered by webhook. It runs in each fact's transaction, after the workflow, and records what
// the fact says; an outside review asks for a write on the fact's commit, and a Legion head's
// review is asked for by the workflow, before its merge-state read (workflow's askVerdictWrite).
func (w *workflowRuntime) verdictFacts() intake.Handler { return verdictFacts{w: w} }

type verdictFacts struct{ w *workflowRuntime }

func (h verdictFacts) Apply(ctx context.Context, tx pgx.Tx, fact intake.Fact) (intake.Result, error) {
	switch fact := fact.(type) {
	case intake.OutsidePullRequest:
		return intake.Result{}, h.w.recordOutsidePullRequest(ctx, tx, fact)
	case intake.OutsideReview:
		return intake.Result{}, h.w.recordOutsideReview(ctx, tx, fact)
	case intake.PullRequestReview:
		return intake.Result{}, h.w.recordLegionReview(ctx, tx, fact)
	case intake.PullRequestOpened:
		h.w.noteIssueBranch(ctx, fact.Repo, fact.Number, fact.Branch)
	case intake.PullRequestSynchronized:
		h.w.noteIssueBranch(ctx, fact.Repo, fact.Number, fact.Branch)
	}
	return intake.Result{}, nil
}

// recordOutsidePullRequest records the sighting of an outside pull request of a verdict repository
// at its head, with GitHub's clock of the pull request the event carries, so a sighting delivered
// late takes no head back (store.OutsidePullRequestTx). A new head has no verdict, so nothing is
// written.
func (w *workflowRuntime) recordOutsidePullRequest(ctx context.Context, tx pgx.Tx, fact intake.OutsidePullRequest) error {
	if !w.namesVerdictRepository(fact.Repo) {
		return nil
	}
	repository, err := ghrepo.Parse("the repository of an outside pull request", fact.Repo)
	if err != nil {
		return err
	}
	_, err = store.RecordOutsidePullRequestTx(ctx, tx, store.OutsidePullRequest{Repository: repository, Number: fact.Number,
		Head: fact.HeadSHA, UpdatedAt: fact.UpdatedAt, RecordedAt: w.clockNow().UTC()})
	return err
}

// recordOutsideReview records a review of an outside pull request of a verdict repository that a
// verdict reads (an approval or a request for changes) at the commit it read, and asks, on the
// commit, for a write on that commit. A review of a pull request whose head the writer knows to be
// another project's issue branch records nothing.
func (w *workflowRuntime) recordOutsideReview(ctx context.Context, tx pgx.Tx, fact intake.OutsideReview) error {
	if !w.namesVerdictRepository(fact.Repo) || !fact.Deciding() || w.verdicts.otherIssue(fact.Repo, fact.Number) != "" {
		return nil
	}
	head, err := w.recordReview(ctx, tx, fact.Repo, fact.PullRequestReview)
	if err != nil || head == "" {
		return err
	}
	repo, number := fact.Repo, fact.Number
	intake.OnCommit(ctx, func() { w.queueVerdictWrite("", repo, number, head) })
	return nil
}

// recordLegionReview records a webhook's review of a Legion head of the project's own on a verdict
// repository that a verdict reads, one not written with the one App's token, under the repository
// the pull request's record keeps: a person's request for changes refuses that head until the same
// person approves it, whatever phase the issue has reached (spec R4), which the round's decision,
// cleared when the round ends, cannot hold. A review of another project's Legion head is that
// project's, and logged once.
func (w *workflowRuntime) recordLegionReview(ctx context.Context, tx pgx.Tx, fact intake.PullRequestReview) error {
	if !w.namesVerdictRepository(fact.Repo) || !fact.Deciding() || fact.Author == w.appLogin {
		return nil
	}
	pr, err := ghrepo.Under(w.renames(), fact.Repo, func(name string) (*record.PullRequest, error) {
		return w.records.PullRequestByNumber(ctx, tx, name, fact.Number)
	})
	if err != nil || pr == nil {
		return err
	}
	issue, err := w.records.Issue(ctx, tx, pr.Issue)
	if err != nil {
		return err
	}
	if issue == nil {
		repo, number, other := fact.Repo, fact.Number, pr.Issue
		intake.OnCommit(ctx, func() { w.noteOtherProject(repo, number, other) })
		return nil
	}
	_, err = w.recordReview(ctx, tx, pr.Repo, fact)
	return err
}

// recordReview records review under repo at the commit it read (commit_id), and answers that head:
// the one a review is a verdict at, as the workflow holds a person's refusal at it (workflow's
// review) and asks for the write there (askMergeState). A review that names none records nothing
// and answers "", rather than standing at the pull request's head the event carried, which is not
// the code the review read.
func (w *workflowRuntime) recordReview(ctx context.Context, tx pgx.Tx, repo string, review intake.PullRequestReview) (string, error) {
	head := review.CommitID
	if head == "" {
		return "", nil
	}
	repository, err := ghrepo.Parse("the repository of a review", repo)
	if err != nil {
		return "", err
	}
	_, err = store.RecordVerdictReviewTx(ctx, tx, store.VerdictReview{Repository: repository, Number: review.Number, GitHubID: review.ID,
		Head: head, Author: review.Author, AuthorCanWrite: review.AuthorCanWrite, State: strings.ToLower(review.State),
		SubmittedAt: review.SubmittedAt, RecordedAt: w.clockNow().UTC()})
	return head, err
}

// noteIssueBranch logs, on the commit and once per pull request, a pull request of a verdict
// repository whose head is another project's issue branch (noteOtherProject).
func (w *workflowRuntime) noteIssueBranch(ctx context.Context, repo string, number int, branch string) {
	key, issueBranch := claim.BranchIssue(branch)
	if !w.namesVerdictRepository(repo) || !issueBranch || claim.KeyOfProject(w.dispatchProject, key) {
		return
	}
	intake.OnCommit(ctx, func() { w.noteOtherProject(repo, number, key) })
}
