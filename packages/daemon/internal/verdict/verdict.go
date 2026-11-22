// Package verdict decides what the required check legion-verdict concludes on one head, from what
// the daemon recorded of that head: R4's table and its refusals. It reads nothing and writes
// nothing, so the writer (the daemon's writeVerdict), the merge-group write and the sweep all
// decide a head by this one rule.
//
// On a head a Legion issue owns, the verdict is the tester's pass and the reviewer's approve at
// that head. A pass the tester recorded at another commit counts at the head only where the
// reviewer's verdict at the head names that commit as its testedCommit, by which the reviewer says
// that no commit since it changes code that runs; the daemon reads no diff. A person's approval of
// such a head is no verdict. On any other head, the verdict is the last review at the head of the
// entry's ci_reviewer, the side reviewer's verdict where the entry takes side reviews, or a
// person's approval. A person is an account of type User with write access to the repository.
//
// A refusal at a head beats every approval there: the tester's fail, a reviewer's changes, the CI
// reviewer's request for changes, or a person's last review at the head asking for changes. A
// person's refusal stands on every kind of head until that person approves the same head, and a
// comment lifts nothing. A head that holds no verdict and no refusal concludes nothing, and nothing
// is written on it, so it cannot merge.
package verdict

import (
	"strings"
	"time"
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

// The tester's words, as its completion carries them.
const (
	TesterPass = "pass"
	TesterFail = "fail"
)

// Word is a reviewer's verdict on a head: a Legion issue's reviewer's, or the side reviewer's a
// controller named. The store records it as it is (store.VerdictWord).
type Word string

const (
	ReviewerApprove Word = "approve"
	ReviewerChanges Word = "changes"
)

// Tester is the tester's recorded completion of a Legion issue: its verdict and the commit it
// tested.
type Tester struct {
	Verdict string
	Commit  string
}

// NoTestingPass is the tester's result that every reader of a Legion head takes until legion-verdict
// decides a testing step by Dispatch's check states at the head: no pass. The record keeps no
// tester's verdict since store migration 20261121074614_phase_verdict_gone dropped
// phases.verdict (a tester's completion carries none, and the states decide the testing phase), so
// no testing step reads as passed at any head, no Legion head concludes success, and no reviewer's
// approve sends its issue back to testing. The writer (the daemon's legionInputs) and the
// workflow's carry (uncarried) read it in place of the tester's row.
var NoTestingPass = Tester{}

// Reviewer is a Legion issue's reviewer's verdict: its word, the commit it reviewed, and the commit
// of the tester's pass it carries to that commit ("" for none).
type Reviewer struct {
	Verdict      Word
	Commit       string
	TestedCommit string
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
	// Legion is whether a Legion issue of this daemon's project owns the head (a legion/<KEY>
	// head); Tester and Reviewer are that issue's, and read on no other head.
	Legion   bool
	Tester   Tester
	Reviewer Reviewer
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

// Decide is legion-verdict's conclusion on in.Head.
func Decide(in Inputs) Conclusion {
	refused, approved := personsAt(in)
	if in.Legion {
		refused = refused || in.Tester.Commit == in.Head && in.Tester.Verdict == TesterFail ||
			in.Reviewer.Commit == in.Head && in.Reviewer.Verdict == ReviewerChanges
		// A person's approval is no verdict on a Legion head: only the reviewer's approve, with
		// the tester's pass at the head or carried to it, is.
		approved = in.Reviewer.Commit == in.Head && in.Reviewer.Verdict == ReviewerApprove && PassCounts(in.Tester, in.Head, in.Reviewer.TestedCommit)
	} else {
		if ci := ciReviewerAt(in); ci != "" {
			refused = refused || ci == StateChangesRequested
			approved = approved || ci == StateApproved
		}
		if in.SideReviews {
			refused = refused || in.Side == ReviewerChanges
			approved = approved || in.Side == ReviewerApprove
		}
	}
	switch {
	case refused:
		return Failure
	case approved:
		return Success
	}
	return None
}

// PassCounts is whether the tester's pass counts at head, a head a Legion issue owns: recorded at
// head, or recorded at the commit the reviewer's verdict at head names as its testedCommit, by
// which the reviewer states that no commit since changes code that runs. It is the one rule of the
// carry: Decide reads it, and the workflow sends an issue back to testing where a reviewer's
// approve fails it (spec R4). It compares two recorded commits and reads no diff.
func PassCounts(tester Tester, head, testedCommit string) bool {
	if tester.Verdict != TesterPass || tester.Commit == "" {
		return false
	}
	return tester.Commit == head || testedCommit == tester.Commit
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
// compares logins: the configuration keeps the login as an operator spelled it. A Legion head has
// none: the CI reviewer's review of it is a bot's, which decides nothing.
func isCIReviewer(in Inputs, author string) bool {
	return !in.Legion && in.CIReviewer != "" && strings.EqualFold(author, in.CIReviewer)
}

// deciding is whether a review in state decides anything.
func deciding(state string) bool {
	return state == StateApproved || state == StateChangesRequested
}
