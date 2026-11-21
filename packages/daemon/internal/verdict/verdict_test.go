package verdict

import (
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// The heads of these cases: H the head decided, H1 the commit of a tester's pass that a reviewer's
// verdict at H carries there, H0 any other commit.
const (
	h  = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	h1 = "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"
	h0 = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"
)

// ciReviewer is the bot login a verdict repository's entry names as its ci_reviewer.
const ciReviewer = "example-ci-review[bot]"

func approvedBy(author string, person bool, at string) Review {
	return Review{Head: at, Author: author, Person: person, State: StateApproved}
}

func refusedBy(author string, person bool, at string) Review {
	return Review{Head: at, Author: author, Person: person, State: StateChangesRequested}
}

func commentedBy(author string, person bool, at string) Review {
	return Review{Head: at, Author: author, Person: person, State: "commented"}
}

// submitted is review as GitHub numbered it and submitted it, minute minutes past ten: what orders
// each author's reviews (record.ReviewOrder), whatever order the daemon recorded them in.
func submitted(review Review, id int64, minute int) Review {
	review.GitHubID, review.SubmittedAt = id, time.Date(2026, 11, 20, 10, minute, 0, 0, time.UTC)
	return review
}

// passAtH and approveAtH are a Legion head's two halves of a verdict at H.
var (
	passAtH    = Tester{Verdict: TesterPass, Commit: h}
	approveAtH = Reviewer{Verdict: ReviewerApprove, Commit: h}
)

// Decide is R4's table and refusals, one case per row and per refusal: on a head a Legion issue
// owns, success only from the tester's pass and the reviewer's approve at that head, the pass
// carried from another commit only by the reviewer's testedCommit naming it, and a person's
// approval never a verdict there; on any other head, the ci_reviewer's last review, the side
// reviewer's verdict where the entry takes side reviews, or a person's approval; and on every
// head a refusal - the tester's fail, a reviewer's changes, the CI reviewer's request for changes,
// or a person's last review at the head asking for changes - beating every approval. A head that
// holds none of them gets no conclusion, so nothing is written on it. On a Legion head the daemon
// reads an author's write access for a request for changes alone (intake's resolveReviewPermission),
// so an approval there is given here as it arrives: with Person false.
func TestDecideIsR4sTable(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Inputs
		want Conclusion
	}{
		// A head a Legion issue owns.
		{"a Legion head: the tester's pass and the reviewer's approve at the head", Inputs{Head: h, Legion: true, Tester: passAtH, Reviewer: approveAtH}, Success},
		{"a Legion head: the reviewer's changes", Inputs{Head: h, Legion: true, Tester: passAtH, Reviewer: Reviewer{Verdict: ReviewerChanges, Commit: h}}, Failure},
		{"a Legion head: the tester's fail", Inputs{Head: h, Legion: true, Tester: Tester{Verdict: TesterFail, Commit: h}}, Failure},
		{"a Legion head: the tester's fail beats the reviewer's approve", Inputs{Head: h, Legion: true, Tester: Tester{Verdict: TesterFail, Commit: h}, Reviewer: approveAtH}, Failure},
		{"a Legion head: the tester's pass and no reviewer verdict", Inputs{Head: h, Legion: true, Tester: passAtH}, None},
		{"a Legion head: the reviewer's approve and no tester pass", Inputs{Head: h, Legion: true, Reviewer: approveAtH}, None},
		{"a Legion head: a person's approval is no verdict", Inputs{Head: h, Legion: true, Tester: passAtH,
			Reviews: []Review{approvedBy("a-writer", false, h)}}, None},
		{"a Legion head: a person's request for changes refuses the reviewer's approve", Inputs{Head: h, Legion: true, Tester: passAtH, Reviewer: approveAtH,
			Reviews: []Review{refusedBy("a-writer", true, h)}}, Failure},
		{"a Legion head: the same person's later approval lifts the refusal", Inputs{Head: h, Legion: true, Tester: passAtH, Reviewer: approveAtH,
			Reviews: []Review{refusedBy("a-writer", true, h), approvedBy("a-writer", false, h)}}, Success},
		{"a Legion head: another person's approval lifts no refusal", Inputs{Head: h, Legion: true, Tester: passAtH, Reviewer: approveAtH,
			Reviews: []Review{refusedBy("a-writer", true, h), approvedBy("another-writer", false, h)}}, Failure},
		{"a Legion head: a comment after a request for changes lifts nothing", Inputs{Head: h, Legion: true, Tester: passAtH, Reviewer: approveAtH,
			Reviews: []Review{refusedBy("a-writer", true, h), commentedBy("a-writer", false, h)}}, Failure},
		{"a Legion head: an approval recorded after the same person's later refusal lifts nothing", Inputs{Head: h, Legion: true, Tester: passAtH, Reviewer: approveAtH,
			Reviews: []Review{submitted(refusedBy("a-writer", true, h), 12, 5), submitted(approvedBy("a-writer", false, h), 11, 1)}}, Failure},
		{"a Legion head: a request for changes at another head refuses nothing here", Inputs{Head: h, Legion: true, Tester: passAtH, Reviewer: approveAtH,
			Reviews: []Review{refusedBy("a-writer", true, h0)}}, Success},
		{"a Legion head: an account with no write access refuses nothing", Inputs{Head: h, Legion: true, Tester: passAtH, Reviewer: approveAtH,
			Reviews: []Review{refusedBy("a-reader", false, h)}}, Success},
		{"a Legion head: the CI reviewer decides no Legion head", Inputs{Head: h, Legion: true, Tester: passAtH, CIReviewer: ciReviewer,
			Reviews: []Review{approvedBy(ciReviewer, false, h)}}, None},
		{"a Legion head: the reviewer's approve of another commit approves nothing here", Inputs{Head: h, Legion: true, Tester: passAtH,
			Reviewer: Reviewer{Verdict: ReviewerApprove, Commit: h0}}, None},
		{"a Legion head: the tester's pass at H1 carries to H when the verdict at H names H1", Inputs{Head: h, Legion: true,
			Tester: Tester{Verdict: TesterPass, Commit: h1}, Reviewer: Reviewer{Verdict: ReviewerApprove, Commit: h, TestedCommit: h1}}, Success},
		{"a Legion head: a verdict at H with no testedCommit carries no pass", Inputs{Head: h, Legion: true,
			Tester: Tester{Verdict: TesterPass, Commit: h1}, Reviewer: approveAtH}, None},
		{"a Legion head: a testedCommit that is not the tester's carries no pass", Inputs{Head: h, Legion: true,
			Tester: Tester{Verdict: TesterPass, Commit: h1}, Reviewer: Reviewer{Verdict: ReviewerApprove, Commit: h, TestedCommit: h0}}, None},
		{"a Legion head: a tester's fail at H1 refuses nothing at H", Inputs{Head: h, Legion: true,
			Tester: Tester{Verdict: TesterFail, Commit: h1}, Reviewer: Reviewer{Verdict: ReviewerApprove, Commit: h, TestedCommit: h1}}, None},

		// A head no Legion issue owns, on a repository whose entry names a ci_reviewer.
		{"an outside head: the CI reviewer named in another case than GitHub spells it", Inputs{Head: h, CIReviewer: "Example-CI-Review[bot]",
			Reviews: []Review{approvedBy(ciReviewer, false, h)}}, Success},
		{"an outside head: the CI reviewer approves", Inputs{Head: h, CIReviewer: ciReviewer, Reviews: []Review{approvedBy(ciReviewer, false, h)}}, Success},
		{"an outside head: the CI reviewer asks for changes", Inputs{Head: h, CIReviewer: ciReviewer, Reviews: []Review{refusedBy(ciReviewer, false, h)}}, Failure},
		{"an outside head: the CI reviewer's last review stands", Inputs{Head: h, CIReviewer: ciReviewer,
			Reviews: []Review{refusedBy(ciReviewer, false, h), approvedBy(ciReviewer, false, h)}}, Success},
		{"an outside head: a person's request for changes after the CI reviewer's approval", Inputs{Head: h, CIReviewer: ciReviewer,
			Reviews: []Review{approvedBy(ciReviewer, false, h), refusedBy("a-writer", true, h)}}, Failure},
		{"an outside head: the CI reviewer's approval recorded after its later request for changes", Inputs{Head: h, CIReviewer: ciReviewer,
			Reviews: []Review{submitted(refusedBy(ciReviewer, false, h), 22, 5), submitted(approvedBy(ciReviewer, false, h), 21, 1)}}, Failure},
		{"an outside head: a person's approval recorded after their later request for changes", Inputs{Head: h,
			Reviews: []Review{submitted(refusedBy("a-writer", true, h), 32, 5), submitted(approvedBy("a-writer", true, h), 31, 1)}}, Failure},
		{"an outside head: the CI reviewer's approval of another head", Inputs{Head: h, CIReviewer: ciReviewer, Reviews: []Review{approvedBy(ciReviewer, false, h0)}}, None},
		{"an outside head: a bot that is not the CI reviewer", Inputs{Head: h, CIReviewer: ciReviewer,
			Reviews: []Review{approvedBy("another-bot[bot]", false, h), refusedBy("another-bot[bot]", false, h)}}, None},
		{"an outside head: a person's approval", Inputs{Head: h, CIReviewer: ciReviewer, Reviews: []Review{approvedBy("a-writer", true, h)}}, Success},
		{"an outside head: an approval by an account with no write access", Inputs{Head: h, CIReviewer: ciReviewer, Reviews: []Review{approvedBy("a-reader", false, h)}}, None},

		// A head no Legion issue owns, on a repository whose entry takes side reviews.
		{"an outside head: the side reviewer approves", Inputs{Head: h, SideReviews: true, Side: ReviewerApprove}, Success},
		{"an outside head: the side reviewer asks for changes", Inputs{Head: h, SideReviews: true, Side: ReviewerChanges}, Failure},
		{"an outside head: a person's refusal beats the side reviewer's approve", Inputs{Head: h, SideReviews: true, Side: ReviewerApprove,
			Reviews: []Review{refusedBy("a-writer", true, h)}}, Failure},
		{"an outside head: a side verdict where the entry takes none", Inputs{Head: h, Side: ReviewerApprove}, None},

		// A head no Legion issue owns, on a repository whose entry names neither.
		{"an outside head with neither: a person's approval", Inputs{Head: h, Reviews: []Review{approvedBy("a-writer", true, h)}}, Success},
		{"an outside head with neither: nothing recorded", Inputs{Head: h}, None},
		{"an outside head with neither: a bot's approval", Inputs{Head: h, Reviews: []Review{approvedBy(ciReviewer, false, h)}}, None},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.in); got != tc.want {
				t.Errorf("Decide(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// PassCounts is the one rule of whether a tester's pass counts at a head, which Decide reads and
// the workflow sends an issue back to testing by: a pass recorded at the head, or at the commit the
// reviewer's verdict at the head names as its testedCommit. No other verdict, no recorded commit,
// no testedCommit and a testedCommit that is not the pass's own carry nothing.
func TestPassCountsOnlyAtItsCommitOrWhereTheTestedCommitNamesIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tester Tester
		tested string
		want   bool
	}{
		{"a pass at the head", passAtH, "", true},
		{"a pass at H1, which the verdict at H names", Tester{Verdict: TesterPass, Commit: h1}, h1, true},
		{"a pass at H1, and a verdict at H naming no testedCommit", Tester{Verdict: TesterPass, Commit: h1}, "", false},
		{"a pass at H1, and a verdict at H naming H0", Tester{Verdict: TesterPass, Commit: h1}, h0, false},
		{"a fail at the head", Tester{Verdict: TesterFail, Commit: h}, "", false},
		{"a fail at H1, which the verdict at H names", Tester{Verdict: TesterFail, Commit: h1}, h1, false},
		{"a pass that names no commit", Tester{Verdict: TesterPass}, "", false},
		{"no tester's completion", Tester{}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PassCounts(tc.tester, h, tc.tested); got != tc.want {
				t.Errorf("PassCounts(%+v, H, %q) = %t, want %t", tc.tester, tc.tested, got, tc.want)
			}
		})
	}
}

// Decide takes each author's last review by the order the record orders reviews by
// (record.ReviewOrder.After): this package writes that rule out, since it imports no record, and
// each pair here, in both directions, is ordered by the two alike. The pairs are record's own:
// a draft submitted late, two reviews of one moment, a review with no time, and one review.
func TestReviewsAreOrderedAsTheRecordOrdersThem(t *testing.T) {
	early := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	late := early.Add(time.Minute)
	for _, pair := range [][2]Review{
		{{SubmittedAt: late, GitHubID: 1}, {SubmittedAt: early, GitHubID: 2}},
		{{SubmittedAt: early, GitHubID: 2}, {SubmittedAt: early, GitHubID: 1}},
		{{GitHubID: 2}, {SubmittedAt: late, GitHubID: 1}},
		{{SubmittedAt: early, GitHubID: 1}, {SubmittedAt: early, GitHubID: 1}},
	} {
		for _, ab := range [][2]Review{pair, {pair[1], pair[0]}} {
			a, b := ab[0], ab[1]
			want := record.ReviewOrder{SubmittedAt: a.SubmittedAt, ID: a.GitHubID}.After(record.ReviewOrder{SubmittedAt: b.SubmittedAt, ID: b.GitHubID})
			if got := after(a, b); got != want {
				t.Errorf("after(%+v, %+v) = %v, want %v as record.ReviewOrder.After answers", a, b, got, want)
			}
		}
	}
}
