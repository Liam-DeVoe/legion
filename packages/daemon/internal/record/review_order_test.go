package record

import (
	"testing"
	"time"
)

// A review's place among a pull request's deciding reviews is its submission time, then GitHub's
// review id: a draft keeps the id it was created with and is submitted later, so a time that
// disagrees with the ids wins; two reviews of one moment, or a review with no time, are ordered by
// id. The workflow orders a person's requests for changes by it (workflow's review), so a request
// delivered again, or late, records nothing over a newer one.
func TestAReviewsOrderIsItsSubmissionTimeThenItsID(t *testing.T) {
	early := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	late := early.Add(time.Minute)
	for _, tc := range []struct {
		name         string
		o, other     ReviewOrder
		after, under bool
	}{
		{name: "a later time with a smaller id, a draft submitted late", o: ReviewOrder{SubmittedAt: late, ID: 1}, other: ReviewOrder{SubmittedAt: early, ID: 2}, after: true},
		{name: "one moment, ordered by id", o: ReviewOrder{SubmittedAt: early, ID: 2}, other: ReviewOrder{SubmittedAt: early, ID: 1}, after: true},
		{name: "no time on one side, ordered by id", o: ReviewOrder{ID: 2}, other: ReviewOrder{SubmittedAt: late, ID: 1}, after: true},
		{name: "the same review", o: ReviewOrder{SubmittedAt: early, ID: 1}, other: ReviewOrder{SubmittedAt: early, ID: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.o.After(tc.other); got != tc.after {
				t.Fatalf("%+v.After(%+v) = %v, want %v", tc.o, tc.other, got, tc.after)
			}
			if got := tc.other.After(tc.o); got != tc.under {
				t.Fatalf("%+v.After(%+v) = %v, want %v", tc.other, tc.o, got, tc.under)
			}
		})
	}
}
