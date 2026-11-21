package daemon

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sjawhar/legion/daemon/internal/githubrest"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// githubReview is one review as GitHub lists a pull request's reviews (GET /pulls/{number}/reviews):
// its id, its author, its body, its state (APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED or
// PENDING), the commit it was written on, and when it was submitted, which a pending review has not
// been.
type githubReview struct {
	ID   int64 `json:"id"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	CommitID    string    `json:"commit_id"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// readReviews is the per-issue read's read of pr's reviews, whatever phase its issue is in. The
// daemon otherwise learns a decision only from its pull_request_review webhook, and a delivery
// that never reaches the notification stream leaves the decision on GitHub unread; a restart loses
// nothing, since the daemon's durable consumer replays what the stream holds, so the read is for
// the delivery the stream never got. It reads every page of the pull request's reviews with
// github, the implement App's client the read read the pull request with, and applies the review
// the workflow would hold as the newest had every webhook arrived (reviewToApply), as the
// intake.PullRequestReview fact the webhook would have carried, which the workflow decides as it
// decides the webhook's. One read costs one request per hundred of the pull request's reviews, on
// the installation the rest of the read uses.
//
// It reads in every phase, not only while a review round is open, because the workflow records a
// deciding review as the pull request's newest (record.PullRequest.ReviewSeen) whatever phase the
// issue is in and lets it decide only an open round: a review submitted while the work is in
// implementing or testing, or in a round CI's red ended, that the read finds before the next round
// opens is recorded and decides nothing, so that round does not take it as its own decision. One the
// daemon first hears of after the next round opened - its webhook lost and the round opened before
// the next read of the issue - can still decide that round, as a webhook delivered late can; the
// read opens no window the webhook does not already have. A read made only while a round is open
// would apply every such review to the next round.
//
// Nothing beyond the workflow's own order keeps the read and the webhook from deciding twice. Both
// carry GitHub's review id and submission time, so whichever copy is applied first records the
// review as the pull request's newest deciding one, and the other is not after it and records
// nothing; facts apply one at a time (intake.ApplyFact), so the two copies never both pass that
// order.
//
// ctx bounds the apply, and read the reads before it. It says whether the read must stop: ctx
// ended, or GitHub answered the reviews read with its rate limit, which every later read on the
// same installation meets. Any other failure is logged and applies nothing, and the next read of
// the issue asks again.
func (w *workflowRuntime) readReviews(ctx, read context.Context, github githubrest.Client, pr record.PullRequest) bool {
	reviews, err := githubrest.GetPages[githubReview](read, github, fmt.Sprintf("/pulls/%d/reviews", pr.Number))
	if err != nil {
		if ctx.Err() != nil {
			return true
		}
		return w.logReadFailure(err,
			"read a pull request's reviews: GitHub's rate limit; this read asks GitHub no more",
			"read a pull request's reviews; a decision its webhook never delivered stays unread until a later read of the issue reads it",
			"repo", pr.Repo, "pullRequest", pr.Number)
	}
	review, ok, err := w.reviewToApply(read, github, pr, reviews)
	if err != nil {
		if ctx.Err() != nil {
			return true
		}
		return w.logReadFailure(err,
			"read what a pull request's listed reviews decide: GitHub's rate limit; this read asks GitHub no more",
			"apply none of a pull request's reviews this read; a later read of the issue reads them again",
			"repo", pr.Repo, "pullRequest", pr.Number)
	}
	if !ok {
		return false
	}
	// A deciding review the pull request's record had not seen when this read looked it up: its
	// webhook was lost, or is still on its way, and whichever copy comes second records nothing. A
	// review an earlier read applied is not applied again.
	eventID := readEventID("review-read", w.dispatchProject, pr.Repo, pr.Number, strconv.FormatInt(review.ID, 10), clock(review.SubmittedAt))
	if !w.unapplied(ctx, eventID) {
		return false
	}
	w.log.Warn("workflow: GitHub holds a deciding review the daemon has not recorded; applying it as its webhook would",
		"repo", pr.Repo, "pullRequest", pr.Number, "issue", pr.Issue, "review", review.ID, "author", review.Author, "state", review.State, "submitted_at", review.SubmittedAt)
	if _, err := intake.ApplyFact(ctx, w.pool, "github", eventID, review, w.handlers...); err != nil {
		w.log.Warn("apply a review read from GitHub", "repo", pr.Repo, "pullRequest", pr.Number, "review", review.ID, "error", err)
	}
	return false
}

// reviewToApply is the review of reviews, GitHub's list of pr's, that the workflow would hold as
// the pull request's newest deciding review had every webhook arrived, judged by the workflow's
// own rule rather than by a second one beside it: of the reviews that decide
// (intake.PullRequestReview.Decides: requests for changes) and are newer
// (intake.PullRequestReview.Order) than the newest the pull request's record holds
// (record.PullRequest.ReviewSeen), the newest the workflow refuses nothing in
// (workflow.Engine.RefusesRound), its author's write access resolved as a webhook's review's is
// (intake.ResolveAuthorCanWrite with reviewerCanWrite), which reads none for a listed review.
//
// GitHub lists reviews in the order they were created, and a draft keeps its place when it is
// submitted later, so the list's order decides nothing. It says false when no review qualifies. A
// deciding review GitHub lists without an id or a submission time, which the workflow could not
// order, is an error; it applies nothing.
//
// A candidate the judgement refuses is passed over and logged, and the read goes on to the one
// below it, so a newer review the rule refuses hides no older one it admits. The fact carries
// intake.PullRequestReview.ReadFromList, which has the rule refuse every listed review: the
// list's commit_id is no evidence of the head a review read, since GitHub moves it to the pull
// request's current head (dispatch://INFRA-1557), and a review written with the one App's token
// decides nothing however it arrives. So no listed review is applied today; a person's request
// for changes refuses the head only by its webhook, on the commit_id the event carried.
//
// No rule of that judgement turns on the head, so a refused review is refused at every head the
// record can come to hold, and is judged once per daemon process, as a webhook's review is
// judged once when it is delivered: it is kept as refused (refusedReviews) and not judged again
// by this process. So an account given write access after the read judged its review cannot turn
// that review into a later round's decision, which its webhook could not either, and a listed
// review costs one refusal logged, not one per read of the issue, and no read of its author's
// write access at all: the rule refuses it for the list before it would read that answer. The
// set is held in memory, so after a restart the first read judges a review not yet recorded
// again. A read that failed judged nothing, and the next read of the issue asks again.
func (w *workflowRuntime) reviewToApply(ctx context.Context, github githubrest.Client, pr record.PullRequest, reviews []githubReview) (intake.PullRequestReview, bool, error) {
	var candidates []intake.PullRequestReview
	for _, listed := range reviews {
		review := intake.PullRequestReview{Repo: pr.Repo, Number: pr.Number, ID: listed.ID, SubmittedAt: listed.SubmittedAt,
			State: strings.ToLower(listed.State), CommitID: listed.CommitID, Author: listed.User.Login, Body: listed.Body, ReadFromList: true}
		if !review.Decides() {
			continue
		}
		if review.ID == 0 || review.SubmittedAt.IsZero() {
			return intake.PullRequestReview{}, false, fmt.Errorf("GitHub listed %s's %s review of pull request %s#%d with id %d and submission time %s, which no review can be ordered by",
				review.Author, review.State, pr.Repo, pr.Number, review.ID, listed.SubmittedAt)
		}
		if review.Order().After(pr.ReviewSeen) {
			candidates = append(candidates, review)
		}
	}
	slices.SortFunc(candidates, func(a, b intake.PullRequestReview) int {
		switch {
		case a.Order().After(b.Order()):
			return -1
		case b.Order().After(a.Order()):
			return 1
		}
		return 0
	})
	for _, review := range candidates {
		if w.reviewRefused(review.ID) {
			continue
		}
		resolved, err := intake.ResolveAuthorCanWrite(ctx, review, w.reviewerCanWrite)
		if err != nil {
			return intake.PullRequestReview{}, false, fmt.Errorf("read whether %s may decide review %d: %w", review.Author, review.ID, err)
		}
		refused := w.engine.RefusesRound(resolved)
		if refused == "" {
			return resolved, true, nil
		}
		w.log.Warn("workflow: a review GitHub lists decides nothing; this read passes over it and reads the review below it",
			"issue", pr.Issue, "repo", pr.Repo, "pullRequest", pr.Number, "review", resolved.ID, "author", resolved.Author,
			"state", resolved.State, "refused", refused, "commit_id", resolved.CommitID, "head", pr.HeadSHA)
		w.refuseReview(review.ID)
	}
	return intake.PullRequestReview{}, false, nil
}

// reviewRefused is whether this process has already judged the review GitHub lists under id and
// found the workflow refuses it, which it does at every head (reviewToApply), and refuseReview
// records one it has. The per-issue read (readIssuePullRequest) judges reviews on a goroutine of
// its own for each issue, so the set is kept under readMemoMu rather than by whichever read
// touched it last.
func (w *workflowRuntime) reviewRefused(id int64) bool {
	w.readMemoMu.Lock()
	defer w.readMemoMu.Unlock()
	return w.refusedReviews[id]
}

func (w *workflowRuntime) refuseReview(id int64) {
	w.readMemoMu.Lock()
	defer w.readMemoMu.Unlock()
	if w.refusedReviews == nil {
		w.refusedReviews = map[int64]bool{}
	}
	w.refusedReviews[id] = true
}
