package intake

import "testing"

// A carried approval is the review Dispatch writes in a person's name when their answer to a
// decision block was the whole change since the version they approved; its reason opens with
// CarriedApprovalReasonPrefix, mirrored from Dispatch's carryReason
// (packages/envoy/internal/dispatch/docs/approval_carry.go). It is not a person's new act, so the
// workflow queues no parked root for it. An approval given with no reason, and a review with any
// other reason, are not carries.
func TestCarriedApprovalReadsTheReasonsPrefix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		want   bool
	}{
		{"Dispatch's carried reason", "Carried forward from the approval of version 3: the answer to a decision block was the whole change since it.", true},
		{"the prefix alone", CarriedApprovalReasonPrefix, true},
		{"no reason", "", false},
		{"a reviewer's own reason", "Looks right; the rollback path is named.", false},
		{"the prefix later in the reason", "Not Carried forward from the approval of version 3", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			review := DispatchArtifact{Kind: DispatchArtifactApproved, Reason: tc.reason}
			if got := review.CarriedApproval(); got != tc.want {
				t.Fatalf("CarriedApproval() of %q = %t, want %t", tc.reason, got, tc.want)
			}
		})
	}
}
