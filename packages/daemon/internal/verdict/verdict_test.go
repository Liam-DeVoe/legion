package verdict

import (
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The heads of these cases: H the head decided, H0 any other commit.
const (
	h  = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
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

// The steps of a task's plan at H, as the writer reads them: a review step whose agent approved H,
// changes at H, or gave no verdict there; and the agent that approved H whose claim wrote part of
// the change, or whose sessions the daemon could not read.
var (
	approves       = Step{Type: plan.TypeReview, Verdict: ReviewerApprove}
	approvesWrote  = Step{Type: plan.TypeReview, Verdict: ReviewerApprove, Wrote: true}
	approvesUnread = Step{Type: plan.TypeReview, Verdict: ReviewerApprove, Unread: true}
	changes        = Step{Type: plan.TypeReview, Verdict: ReviewerChanges}
	noVerdict      = Step{Type: plan.TypeReview}
)

// allYes is a head's set of two checks, each reading yes at H.
var allYes = []Check{{Name: "k1", State: StateYes}, {Name: "k2", State: StateYes}}

// checks is a head's set of two checks: k1 reading yes at H and k2 reading state ("" for no state
// held).
func checks(state string) []Check {
	return []Check{{Name: "k1", State: StateYes}, {Name: "k2", State: state}}
}

// defaultSteps is a Legion head H of a task with the default steps, planner implementer tester
// reviewer acceptance:owner: the head's checks, and the reviewer's step and the owner's acceptance
// as given.
func defaultSteps(held []Check, reviewer, acceptance Step) Inputs {
	return Inputs{Head: h, Legion: true, Checks: held, Steps: []Step{{Type: plan.TypePlanning}, {Type: plan.TypeImplementation},
		{Type: plan.TypeTesting}, reviewer, acceptance}}
}

// stepsOf is a Legion head H of a task with steps of these types: an implementation step, and
// each of more as given.
func stepsOf(held []Check, more ...Step) Inputs {
	return Inputs{Head: h, Legion: true, Checks: held, Steps: append([]Step{{Type: plan.TypeImplementation}}, more...)}
}

// testingStep is a testing step.
var testingStep = Step{Type: plan.TypeTesting}

// withReviews is in with reviews delivered by webhook.
func withReviews(in Inputs, reviews ...Review) Inputs {
	in.Reviews = reviews
	return in
}

// withUnread is in where the daemon could not read the change's commits.
func withUnread(in Inputs) Inputs {
	in.Unread = true
	return in
}

// Decide is R4's table and refusals, one case per row and per refusal. On a head a Legion issue
// owns, success only when every testing step reads yes at H for each check, every review step's
// agent approved H, and at least one approving review step's agent is independent: its claim wrote
// none of the change, and the daemon read its sessions and the change's commits. Failure on a check
// no at H, on a review step's changes at H, and once every step has passed with no independent
// review, which Decide names as its reason. Nothing before. A person's approval is never a
// verdict there, and a person's last request for changes at H refuses it. On any other head, the
// ci_reviewer's last review, the side reviewer's verdict where the entry takes side reviews, or a
// person's approval; and on every head a refusal - a failed step, the CI reviewer's request for
// changes, or a person's last review at the head asking for changes - beats every approval. A head
// that holds none of them gets no conclusion, so nothing is written on it. On a Legion head the
// daemon reads an author's write access for a request for changes alone (intake's
// resolveReviewPermission), so an approval there is given here as it arrives: with Person false.
func TestDecideIsR4sTable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     Inputs
		want   Conclusion
		reason Reason
	}{
		// A head a Legion issue owns, with the default steps.
		{"a Legion head: every check yes, and both review steps approve, the reviewer independent", defaultSteps(allYes, approves, approves), Success, ""},
		{"a Legion head: a check no at H, before any review", defaultSteps(checks(StateNo), noVerdict, noVerdict), Failure, ""},
		{"a Legion head: a check no at H beats both approvals", defaultSteps(checks(StateNo), approves, approves), Failure, ""},
		{"a Legion head: the reviewer's changes", defaultSteps(allYes, changes, noVerdict), Failure, ""},
		{"a Legion head: the owner's changes after the reviewer's approve", defaultSteps(allYes, approves, changes), Failure, ""},
		{"a Legion head: a check with no state at H", defaultSteps(checks(""), approves, approves), None, ""},
		{"a Legion head: a check unknown at H", defaultSteps(checks(StateUnknown), approves, approves), None, ""},
		{"a Legion head: no set of checks held at H", defaultSteps(nil, approves, approves), None, ""},
		{"a Legion head: an empty set of checks at H", defaultSteps([]Check{}, approves, approves), Success, ""},
		{"a Legion head: the reviewer approves and the owner has not", defaultSteps(allYes, approves, noVerdict), None, ""},
		{"a Legion head: neither review step has a verdict", defaultSteps(allYes, noVerdict, noVerdict), None, ""},
		{"a Legion head: both approve and both agents wrote part of the change", defaultSteps(allYes, approvesWrote, approvesWrote), Failure, NoIndependentReview},
		{"a Legion head: the reviewer wrote part of the change, the owner none", defaultSteps(allYes, approvesWrote, approves), Success, ""},
		{"a Legion head: the change's commits could not be read", withUnread(defaultSteps(allYes, approves, approves)), Failure, ChangeUnread},
		{"a Legion head: the commits unread and every agent wrote part of the change", withUnread(defaultSteps(allYes, approvesWrote, approvesWrote)), Failure, ChangeUnread},
		{"a Legion head: the reviewer's sessions could not be read and the owner wrote", defaultSteps(allYes, approvesUnread, approvesWrote), Failure, ChangeUnread},
		{"a Legion head: the reviewer's sessions could not be read and the owner is independent", defaultSteps(allYes, approvesUnread, approves), Success, ""},
		{"a Legion head: no independent review, and a check not yet yes", defaultSteps(checks(""), approvesWrote, approvesWrote), None, ""},

		// A head a Legion issue owns, with a plan of other steps.
		{"a plan implementer reviewer: the reviewer's approve, independent", stepsOf(nil, approves), Success, ""},
		{"a plan implementer reviewer: a check's state is no testing step's", stepsOf(checks(StateNo), approves), Success, ""},
		{"a plan implementer reviewer: the reviewer wrote part of the change", stepsOf(nil, approvesWrote), Failure, NoIndependentReview},
		{"a plan implementer tester: every check yes and no review step", stepsOf(allYes, testingStep), Failure, NoIndependentReview},
		{"a plan implementer tester: a check with no state", stepsOf(checks(""), testingStep), None, ""},
		{"a plan implementer tester: a check no", stepsOf(checks(StateNo), testingStep), Failure, ""},

		// A person's review of a Legion head.
		{"a Legion head: a person's approval is no verdict", withReviews(defaultSteps(allYes, approves, noVerdict), approvedBy("a-writer", false, h)), None, ""},
		{"a Legion head: a person's request for changes refuses every step's pass", withReviews(defaultSteps(allYes, approves, approves),
			refusedBy("a-writer", true, h)), Failure, ""},
		{"a Legion head: the same person's later approval lifts the refusal", withReviews(defaultSteps(allYes, approves, approves),
			refusedBy("a-writer", true, h), approvedBy("a-writer", false, h)), Success, ""},
		{"a Legion head: another person's approval lifts no refusal", withReviews(defaultSteps(allYes, approves, approves),
			refusedBy("a-writer", true, h), approvedBy("another-writer", false, h)), Failure, ""},
		{"a Legion head: a comment after a request for changes lifts nothing", withReviews(defaultSteps(allYes, approves, approves),
			refusedBy("a-writer", true, h), commentedBy("a-writer", false, h)), Failure, ""},
		{"a Legion head: an approval recorded after the same person's later refusal lifts nothing", withReviews(defaultSteps(allYes, approves, approves),
			submitted(refusedBy("a-writer", true, h), 12, 5), submitted(approvedBy("a-writer", false, h), 11, 1)), Failure, ""},
		{"a Legion head: a request for changes at another head refuses nothing here", withReviews(defaultSteps(allYes, approves, approves),
			refusedBy("a-writer", true, h0)), Success, ""},
		{"a Legion head: an account with no write access refuses nothing", withReviews(defaultSteps(allYes, approves, approves),
			refusedBy("a-reader", false, h)), Success, ""},
		{"a Legion head: a person's request for changes before every step passed", withReviews(defaultSteps(checks(""), noVerdict, noVerdict),
			refusedBy("a-writer", true, h)), Failure, ""},
		{"a Legion head: the CI reviewer's approval is no verdict", func() Inputs {
			in := withReviews(defaultSteps(allYes, approves, noVerdict), approvedBy(ciReviewer, false, h))
			in.CIReviewer = ciReviewer
			return in
		}(), None, ""},
		{"a Legion head: the CI reviewer's request for changes refuses every step's pass", func() Inputs {
			in := withReviews(defaultSteps(allYes, approves, approves), refusedBy(ciReviewer, false, h))
			in.CIReviewer = ciReviewer
			return in
		}(), Failure, ""},
		{"a Legion head: the CI reviewer's later approval lifts its own request for changes", func() Inputs {
			in := withReviews(defaultSteps(allYes, approves, approves), submitted(refusedBy(ciReviewer, false, h), 41, 1),
				submitted(approvedBy(ciReviewer, false, h), 42, 5))
			in.CIReviewer = ciReviewer
			return in
		}(), Success, ""},
		{"a Legion head: the CI reviewer's request for changes at another head refuses nothing here", func() Inputs {
			in := withReviews(defaultSteps(allYes, approves, approves), refusedBy(ciReviewer, false, h0))
			in.CIReviewer = ciReviewer
			return in
		}(), Success, ""},

		// A head no Legion issue owns, on a repository whose entry names a ci_reviewer.
		{"an outside head: the CI reviewer named in another case than GitHub spells it", Inputs{Head: h, CIReviewer: "Example-CI-Review[bot]",
			Reviews: []Review{approvedBy(ciReviewer, false, h)}}, Success, ""},
		{"an outside head: the CI reviewer approves", Inputs{Head: h, CIReviewer: ciReviewer, Reviews: []Review{approvedBy(ciReviewer, false, h)}}, Success, ""},
		{"an outside head: the CI reviewer asks for changes", Inputs{Head: h, CIReviewer: ciReviewer, Reviews: []Review{refusedBy(ciReviewer, false, h)}}, Failure, ""},
		{"an outside head: the CI reviewer's last review stands", Inputs{Head: h, CIReviewer: ciReviewer,
			Reviews: []Review{refusedBy(ciReviewer, false, h), approvedBy(ciReviewer, false, h)}}, Success, ""},
		{"an outside head: a person's request for changes after the CI reviewer's approval", Inputs{Head: h, CIReviewer: ciReviewer,
			Reviews: []Review{approvedBy(ciReviewer, false, h), refusedBy("a-writer", true, h)}}, Failure, ""},
		{"an outside head: the CI reviewer's approval recorded after its later request for changes", Inputs{Head: h, CIReviewer: ciReviewer,
			Reviews: []Review{submitted(refusedBy(ciReviewer, false, h), 22, 5), submitted(approvedBy(ciReviewer, false, h), 21, 1)}}, Failure, ""},
		{"an outside head: a person's approval recorded after their later request for changes", Inputs{Head: h,
			Reviews: []Review{submitted(refusedBy("a-writer", true, h), 32, 5), submitted(approvedBy("a-writer", true, h), 31, 1)}}, Failure, ""},
		{"an outside head: the CI reviewer's approval of another head", Inputs{Head: h, CIReviewer: ciReviewer, Reviews: []Review{approvedBy(ciReviewer, false, h0)}}, None, ""},
		{"an outside head: a bot that is not the CI reviewer", Inputs{Head: h, CIReviewer: ciReviewer,
			Reviews: []Review{approvedBy("another-bot[bot]", false, h), refusedBy("another-bot[bot]", false, h)}}, None, ""},
		{"an outside head: a person's approval", Inputs{Head: h, CIReviewer: ciReviewer, Reviews: []Review{approvedBy("a-writer", true, h)}}, Success, ""},
		{"an outside head: an approval by an account with no write access", Inputs{Head: h, CIReviewer: ciReviewer, Reviews: []Review{approvedBy("a-reader", false, h)}}, None, ""},
		{"an outside head: no plan step is read", Inputs{Head: h, Steps: []Step{approvesWrote}, Checks: checks(StateNo),
			Reviews: []Review{approvedBy("a-writer", true, h)}}, Success, ""},

		// A head no Legion issue owns, on a repository whose entry takes side reviews.
		{"an outside head: the side reviewer approves", Inputs{Head: h, SideReviews: true, Side: ReviewerApprove}, Success, ""},
		{"an outside head: the side reviewer asks for changes", Inputs{Head: h, SideReviews: true, Side: ReviewerChanges}, Failure, ""},
		{"an outside head: a person's refusal beats the side reviewer's approve", Inputs{Head: h, SideReviews: true, Side: ReviewerApprove,
			Reviews: []Review{refusedBy("a-writer", true, h)}}, Failure, ""},
		{"an outside head: a side verdict where the entry takes none", Inputs{Head: h, Side: ReviewerApprove}, None, ""},

		// A head no Legion issue owns, on a repository whose entry names neither.
		{"an outside head with neither: a person's approval", Inputs{Head: h, Reviews: []Review{approvedBy("a-writer", true, h)}}, Success, ""},
		{"an outside head with neither: nothing recorded", Inputs{Head: h}, None, ""},
		{"an outside head with neither: a bot's approval", Inputs{Head: h, Reviews: []Review{approvedBy(ciReviewer, false, h)}}, None, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, reason := Decide(tc.in); got != tc.want || reason != tc.reason {
				t.Errorf("Decide(%+v) = %q, %q; want %q, %q", tc.in, got, reason, tc.want, tc.reason)
			}
		})
	}
}

// StepsPassed is whether every testing and review step of a Legion head's plan passed at the head,
// which alone makes the independence of a review decide anything: the writer reads the change's
// commits for the Omp-Session test only then.
func TestStepsPassedAtTheHead(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Inputs
		want bool
	}{
		{"every check yes and both review steps approve", defaultSteps(allYes, approvesWrote, approves), true},
		{"a check with no state", defaultSteps(checks(""), approves, approves), false},
		{"a review step with no verdict", defaultSteps(allYes, approves, noVerdict), false},
		{"a review step's changes", defaultSteps(allYes, approves, changes), false},
		{"a plan with no testing step and no set held", stepsOf(nil, approves), true},
		{"a plan with no review step", stepsOf(allYes, testingStep), true},
		{"an outside head", Inputs{Head: h, Steps: []Step{approves}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := StepsPassed(tc.in); got != tc.want {
				t.Errorf("StepsPassed(%+v) = %t, want %t", tc.in, got, tc.want)
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
