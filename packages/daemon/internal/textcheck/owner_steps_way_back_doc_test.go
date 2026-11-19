package textcheck

import "testing"

// The way back of the release whose owner's plan picks the steps stands in two files: the header
// of store migration 20261117034837_issue_plans, which ships the block with the migration, and a
// fenced block of docs/kubernetes.md, under "Rolling back the release whose owner's plan picks the
// steps", which is where an operator reads it at a rollback. internal/store's
// TestTheOwnerStepsWayBackDeletesOnlyItsProjectsRowsTheReleaseBeforeCannotRead runs the header's
// block over queued rows of two projects and reads what it leaves; this test holds the guide's
// block to that one, as the plan ratings' way back is held (plan_ratings_way_back_doc_test.go).
const (
	// ownerStepsMigration is the migration whose header carries the block.
	ownerStepsMigration = "20261117034837_issue_plans.up.sql"
	// ownerStepsWayBackSection is the heading of the guide's section that gives the way back.
	ownerStepsWayBackSection = "Rolling back the release whose owner's plan picks the steps"
	// ownerStepsWayBackMark is the line that deletes the migration's version row, which no other
	// rollback's block carries.
	ownerStepsWayBackMark = "delete from schema_version where version = 20261117034837;"
)

// The guide's way back of the release whose owner's plan picks the steps is the block the
// migration's header carries, line for line. An edit to either side alone fails here: a line
// changed in the guide and not in the header would otherwise never reach the test that runs the
// header's block.
func TestTheOwnerStepsWayBackIsOneTextInTheHeaderAndTheGuide(t *testing.T) {
	header := indentedWayBackOfTheHeader(t, ownerStepsMigration)
	guide := wayBackBlockOfTheGuide(t, ownerStepsWayBackSection, ownerStepsWayBackMark, "the header of "+ownerStepsMigration)
	if header == guide {
		return
	}
	t.Errorf("the header of %s and %s's way back disagree:\n%s"+
		"internal/store's test runs the header's block and an operator the guide's; change them in one commit",
		ownerStepsMigration, wayBackGuide, wayBackBlockDifference("the header", header, guide))
}
