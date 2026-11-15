package main

import (
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/modelpace"
)

// The reviewer is built whenever its model group is set, whatever the kinds say, so the draft
// route answers; with the group unset it is nil, and a kind other than off then refuses the boot.
func TestTheReviewerIsBuiltWheneverItsModelGroupIsSet(t *testing.T) {
	pacer := modelpace.New(modelpace.Config{})
	unset, err := newReviewer(resolveWith(t, map[string]string{}).Writing, pacer)
	if err != nil || unset != nil {
		t.Fatalf("no group: reviewer %v (%v), want none", unset, err)
	}
	for _, settings := range []map[string]string{
		standInGroup(reviewerModel, "anthropic", nil),
		standInGroup(reviewerModel, "azure", map[string]string{"DISPATCH_REVIEWER_COMMENTS": "hold", "DISPATCH_REVIEWER_ASKS": "after"}),
		anthropicGroup(reviewerModel, nil),
	} {
		built, err := newReviewer(resolveWith(t, settings).Writing, pacer)
		if err != nil || built == nil {
			t.Fatalf("%v: reviewer %v (%v), want one", settings, built, err)
		}
	}
	refusedWith(t, map[string]string{"DISPATCH_REVIEWER_COMMENTS": "hold"}, "needs the reviewer's model group")
}
