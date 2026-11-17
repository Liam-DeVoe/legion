package textcheck

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/store/migrations"
)

// The way back of the release that rates each plan stands in two files: the header of store
// migration 20261117023932_outbox_kinds_with_the_plan_record, which ships the block with the
// migration, and a fenced block of docs/kubernetes.md, under "Rolling back the release that rates
// each plan", which is where an operator reads it at a rollback. internal/store's
// TestThePlanRatingsWayBackLeavesWhatTheReleaseBeforeReads runs the header's block over pending
// rows and reads what it leaves; this test holds the guide's block to that one, so the block an
// operator runs is the block that test proves. The split keeps the guide out of the daemon Go
// lane's filter, as the phase entry's way back does (way_back_doc_test.go).
//
// The header's block is read by the one reader of a migration's way back (migrations.WayBack),
// which internal/store's test runs, so the text held to the guide is the text that test runs, and
// the guide's block is the one under that heading that holds the one line only that rollback's
// block holds (wayBackBlockOfTheGuide).
const (
	// planRatingsVersion is the version of the migration whose header carries the block, which
	// migrations.WayBack reads it by and whose name the embedded set gives (migrationNames), so
	// the header this test reads and the entry it holds the guide to are one migration.
	planRatingsVersion = 20261117023932
	// planRatingsWayBackSection is the heading of the guide's section that gives the way back.
	planRatingsWayBackSection = "Rolling back the release that rates each plan"
	// planRatingsWayBackMark is the line that deletes the plan records, which no other
	// rollback's block carries.
	planRatingsWayBackMark = "delete from outbox where kind = 'plan_record';"
)

// The guide's way back of the release that rates each plan is the block the migration's header
// carries, line for line. An edit to either side alone fails here: a line changed in the guide
// and not in the header would otherwise never reach the test that runs the header's block.
func TestThePlanRatingsWayBackIsOneTextInTheHeaderAndTheGuide(t *testing.T) {
	migration := migrationNames(t, planRatingsVersion)[0]
	header := planRatingsWayBackOfTheHeader(t)
	guide := wayBackBlockOfTheGuide(t, planRatingsWayBackSection, planRatingsWayBackMark,
		"the header of "+migration)
	if header == guide {
		return
	}
	t.Errorf("the header of %s and %s's way back disagree:\n%s"+
		"internal/store's test runs the header's block and an operator the guide's; change them in one commit",
		migration, wayBackGuide, wayBackBlockDifference("the header", header, guide))
}

// planRatingsWayBackOfTheHeader is the way back's block the header of planRatingsVersion carries,
// read by the one reader of it (migrations.WayBack) that internal/store's test runs. A migration
// that is not embedded, and a header with no way back, stop the test by name.
func planRatingsWayBackOfTheHeader(t *testing.T) string {
	t.Helper()
	block, err := migrations.WayBack(planRatingsVersion)
	if err != nil {
		t.Fatalf("read the way back %s gives from the header of migration %d: %v", wayBackGuide, planRatingsVersion, err)
	}
	return block
}

// The guide's list of the migrations recorded above the phase entry sends an operator from the
// migration of the release that rates each plan to that release's own way back: the migration's
// entry links the section that gives the block. The list's own checks hold its names alone
// (phase_entry_migrations_doc_test.go), so an entry that kept the name and lost the link, or
// linked another rollback's section, would pass them and leave an operator who rolls back below
// this release with the phase entry's block alone. The entry is held where a reader follows it,
// by the check the step records' entries take (entryLinkIn).
func TestThePlanRecordMigrationsEntryLinksItsWayBack(t *testing.T) {
	migration := migrationNames(t, planRatingsVersion)[0]
	if err := entryLinkIn(shippedText(t, wayBackGuide), migration, planRatingsWayBackSection); err != nil {
		t.Errorf("%s's list of the migrations recorded above %s: %v: give the entry the link [%s](#%s), since "+
			"a rollback to a release before %s takes that section's steps with the phase entry's block",
			wayBackGuide, phaseEntryMigration, err, planRatingsWayBackSection,
			sectionAnchor(planRatingsWayBackSection), migration)
	}
}
