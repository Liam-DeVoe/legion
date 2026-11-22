// Package verdict decides what the required check legion-verdict concludes on one head, from what
// the daemon recorded of that head: R4's table and its refusals. It reads nothing and writes
// nothing, so the writer (the daemon's writeVerdict), the merge-group write and the sweep all
// decide a head by this one rule.
//
// On a head a Legion issue owns, the verdict follows the steps of the task's plan (Steps): the head
// concludes success only when every testing step reads yes at that head for each check, every
// review step's agent approved that head, and at least one review step that approved it was run by
// an agent whose claim wrote none of the change (Step.Wrote). Each of those is read at the head
// alone, so a step's pass at an earlier commit counts at no later head; the daemon reads no diff.
// Once every step has passed with no independent review, the head concludes failure, for a reason
// Decide names, so the merger can tell the owner. A person's approval of such a head is no verdict,
// and neither is the CI reviewer's.
// On any other head, the verdict is the last review at the head of the entry's ci_reviewer, the
// side reviewer's verdict where the entry takes side reviews, or a person's approval. A person is
// an account of type User with write access to the repository.
//
// A refusal at a head beats every approval there: a testing step's check reading no, a review
// step's changes, the CI reviewer's request for changes, or a person's last review at the head
// asking for changes. A person's refusal stands on every kind of head until that person approves
// the same head, and a comment lifts nothing. A head that holds no verdict and no refusal
// concludes nothing, and nothing is written on it, so it cannot merge.
package verdict

import (
	"strings"
	"time"

	"github.com/sjawhar/legion/daemon/internal/plan"
)

// CheckName is the name of the required check the daemon writes, as a check run and, over a commit
// status of the same name another writer of the one App left, as a commit status.
const CheckName = "legion-verdict"

// Conclusion is what legion-verdict concludes on a head: success, failure, or None, which writes no
// check.
type Conclusion string

const (
	None    Conclusion = ""
	Success Conclusion = "success"
	Failure Conclusion = "failure"
)

// The review states that decide, as intake spells a GitHub review's state; every other state (a
// comment, a dismissal) decides nothing.
const (
	StateApproved         = "approved"
	StateChangesRequested = "changes_requested"
)

// The states of a check at a head, as Dispatch publishes them (check.state_changed): a testing
// step passes on yes alone, fails on no, and waits on any other.
const (
	StateYes     = "yes"
	StateNo      = "no"
	StateUnknown = "unknown"
)

// Word is a review step's verdict on a head, its agent's, or the side reviewer's a controller
// named. The store records it as it is (store.VerdictWord).
type Word string

const (
	ReviewerApprove Word = "approve"
	ReviewerChanges Word = "changes"
)

// Reason is why Decide concludes failure on a Legion head where no step failed: every testing and
// review step passed at the head, and no review step that approved it counts as independent. ""
// for every other conclusion. The writer names it in the run's title and summary (the daemon's
// verdictOutput), which the merger reads to tell the owner.
type Reason string

const (
	// NoIndependentReview is a head whose approving review steps' agents each wrote part of the
	// change, or a plan with no review step.
	NoIndependentReview Reason = "no independent review"
	// ChangeUnread is a head where the daemon could not read the change's commits, or the sessions
	// of an approving review step's claim that wrote nothing it could see, so it counts that review
	// as no independent one.
	ChangeUnread Reason = "change unread"
)

// Step is one step of the task's plan on a Legion head, as the writer reads it at Inputs.Head: its
// type (#4611's plan.Type) and, for a step of the type review, its agent's verdict at the head and
// what the daemon could tell of that agent's claim. Every other field is read of a review step
// alone.
type Step struct {
	Type plan.Type
	// Verdict is the review step's agent's verdict at the head, "" for none.
	Verdict Word
	// Wrote is whether the review step's agent's claim wrote part of the change: it ran an
	// implementation step of the task, or a commit of the change carries one of its sessions in its
	// Omp-Session trailer.
	Wrote bool
	// Unread is whether the daemon could read none of the sessions it records for that claim, so it
	// cannot tell whether a commit carries one.
	Unread bool
}

// independent is whether step is a review step whose agent approved the head and whose claim, as
// far as the daemon could read it, wrote none of the change.
func (step Step) independent() bool {
	return step.Type == plan.TypeReview && step.Verdict == ReviewerApprove && !step.Wrote && !step.Unread
}

// Check is the state of one check of the head's set at the head, "" where the daemon holds none.
type Check struct {
	Name, State string
}

// Review is one review of the pull request, delivered by webhook: the head it read, its author,
// whether its author is a person (an account of type User with write access), its state, and
// GitHub's id of it and when GitHub says it was submitted, by which it is ordered among the
// author's reviews (record.ReviewOrder).
type Review struct {
	Head        string
	Author      string
	Person      bool
	State       string
	GitHubID    int64
	SubmittedAt time.Time
}

// Inputs is what the daemon recorded of one head of a verdict repository's pull request.
type Inputs struct {
	// Head is the commit decided.
	Head string
	// Legion is whether a Legion issue of this daemon's project owns the head (a legion/<KEY> head,
	// or a task branch legion/<KEY>.<slug>); Steps, Checks and Unread are that issue's, and read on
	// no other head.
	Legion bool
	// Steps are the steps of the task's plan, in its order.
	Steps []Step
	// Checks is each check of the head's set with its state at the head, nil where the daemon holds
	// no set there. A testing step passes at a head when each check of its task reads yes there, so
	// every testing step of the task reads these same states: they are the issue's at the head, not
	// one step's.
	Checks []Check
	// Unread is whether the daemon could not read the change's commits, so no review step counts as
	// independent.
	Unread bool
	// CIReviewer is the bot login the repository's entry names as its ci_reviewer, "" for none.
	CIReviewer string
	// SideReviews is whether the entry takes side reviews, and Side the side reviewer's verdict at
	// the head ("" for none).
	SideReviews bool
	Side        Word
	// Reviews is every review of the pull request the daemon recorded, in the order it recorded
	// them, which can differ from GitHub's when a delivery comes again later; only those at Head
	// are read, and each author's last is taken by GitHub's order (later).
	Reviews []Review
}

// Decide is legion-verdict's conclusion on in.Head, and on a Legion head the reason of a failure no
// step refused (Reason).
func Decide(in Inputs) (Conclusion, Reason) {
	refused, approved := personsAt(in)
	// The CI reviewer's request for changes refuses every kind of head; its approval is a verdict on
	// a head no Legion issue owns alone.
	ci := ciReviewerAt(in)
	refused = refused || ci == StateChangesRequested
	var reason Reason
	if in.Legion {
		// A person's approval is no verdict on a Legion head: only the steps of its plan are.
		var failed bool
		failed, approved, reason = stepsAt(in)
		refused = refused || failed
	} else {
		approved = approved || ci == StateApproved
		if in.SideReviews {
			refused = refused || in.Side == ReviewerChanges
			approved = approved || in.Side == ReviewerApprove
		}
	}
	switch {
	case refused:
		return Failure, reason
	case approved:
		return Success, ""
	}
	return None, ""
}

// stepsAt is what the steps of a Legion head's plan say at the head: failed where a testing step's
// check reads no there or a review step's agent asked for changes there, or where every step passed
// with no independent review, which reason then names; and passed where every step passed and at
// least one approving review step is independent.
func stepsAt(in Inputs) (failed, passed bool, reason Reason) {
	for _, step := range in.Steps {
		switch step.Type {
		case plan.TypeTesting:
			failed = failed || in.checkReads(StateNo)
		case plan.TypeReview:
			failed = failed || step.Verdict == ReviewerChanges
		}
	}
	if failed || !StepsPassed(in) {
		return failed, false, ""
	}
	unread := in.Unread
	for _, step := range in.Steps {
		if step.independent() && !in.Unread {
			return false, true, ""
		}
		unread = unread || step.Type == plan.TypeReview && step.Verdict == ReviewerApprove && !step.Wrote && step.Unread
	}
	if unread {
		return true, false, ChangeUnread
	}
	return true, false, NoIndependentReview
}

// StepsPassed is whether every testing and review step of a Legion head's plan passed at the head:
// each check of the head's set reads yes there, where the plan has a testing step, and each review
// step's agent approved the head. Only then does the independence of a review decide anything, so
// the writer reads the change's commits for the Omp-Session test only then. A head no Legion issue
// owns has no such steps, and answers false.
func StepsPassed(in Inputs) bool {
	if !in.Legion {
		return false
	}
	for _, step := range in.Steps {
		switch {
		case step.Type == plan.TypeTesting && (in.Checks == nil || !in.everyCheckYes()):
			return false
		case step.Type == plan.TypeReview && step.Verdict != ReviewerApprove:
			return false
		}
	}
	return true
}

// everyCheckYes is whether each check of the head's set reads yes at the head: an empty set does.
func (in Inputs) everyCheckYes() bool {
	for _, check := range in.Checks {
		if check.State != StateYes {
			return false
		}
	}
	return true
}

// checkReads is whether a check of the head's set reads state at the head.
func (in Inputs) checkReads(state string) bool {
	for _, check := range in.Checks {
		if check.State == state {
			return true
		}
	}
	return false
}

// personsAt is whether a person's last deciding review at the head refuses it, and whether one
// approves it. Each author's last approval or request for changes at the head is that author's
// word, so a person's refusal is lifted by the same login's later approval alone; the CI
// reviewer's reviews are read apart (ciReviewerAt).
func personsAt(in Inputs) (refused, approved bool) {
	last := map[string]Review{}
	for _, review := range in.Reviews {
		if review.Head != in.Head || !deciding(review.State) || isCIReviewer(in, review.Author) {
			continue
		}
		if kept, ok := last[review.Author]; !ok || later(review, kept) {
			last[review.Author] = review
		}
	}
	for _, review := range last {
		refused = refused || review.Person && review.State == StateChangesRequested
		approved = approved || review.Person && review.State == StateApproved
	}
	return refused, approved
}

// ciReviewerAt is the state of the entry's ci_reviewer's last deciding review at the head, "" for
// none.
func ciReviewerAt(in Inputs) string {
	var kept Review
	found := false
	for _, review := range in.Reviews {
		if review.Head == in.Head && deciding(review.State) && isCIReviewer(in, review.Author) && (!found || later(review, kept)) {
			kept, found = review, true
		}
	}
	return kept.State
}

// later is whether review replaces kept as its author's last: GitHub submitted it after kept, or
// neither is ordered before the other and the daemon recorded it after.
func later(review, kept Review) bool {
	return !after(kept, review)
}

// after is whether GitHub submitted a after b, by record.ReviewOrder.After's rule: the submission
// times decide where both reviews carry one and they differ, and GitHub's ids otherwise. The rule
// is written out here because this package imports no record: the store reads its words and
// conclusions, and record's own tests read the store. TestReviewsAreOrderedAsTheRecordOrdersThem
// holds the two rules alike.
func after(a, b Review) bool {
	if !a.SubmittedAt.IsZero() && !b.SubmittedAt.IsZero() && !a.SubmittedAt.Equal(b.SubmittedAt) {
		return a.SubmittedAt.After(b.SubmittedAt)
	}
	return a.GitHubID > b.GitHubID
}

// isCIReviewer is whether author is the entry's ci_reviewer, compared without case as GitHub
// compares logins: the configuration keeps the login as an operator spelled it. Its reviews are
// read apart from a person's on every kind of head (ciReviewerAt): on a Legion head its request
// for changes refuses the head and its approval decides nothing (Decide).
func isCIReviewer(in Inputs, author string) bool {
	return in.CIReviewer != "" && strings.EqualFold(author, in.CIReviewer)
}

// deciding is whether a review in state decides anything.
func deciding(state string) bool {
	return state == StateApproved || state == StateChangesRequested
}
