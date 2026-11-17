package textcheck

import (
	"slices"
	"strings"
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
// The migration is read through migrations.All, as phase_entry_migrations_doc_test.go reads them,
// and the guide's block is found by the one line only that rollback's block holds
// (wayBackBlockOfTheGuide). The header's block is read here by a parse of its own, which shares no
// code with internal/store's: two readers that agree with each other while both drift are what a
// copy of a document is most at risk of.
const (
	// planRatingsMigration is the migration whose header carries the block.
	planRatingsMigration = "20261117023932_outbox_kinds_with_the_plan_record.up.sql"
	// planRatingsWayBackMark is the line that deletes the plan records, which no other
	// rollback's block carries.
	planRatingsWayBackMark = "delete from outbox where kind = 'plan_record';"
)

// The guide's way back of the release that rates each plan is the block the migration's header
// carries, line for line. An edit to either side alone fails here: a line changed in the guide
// and not in the header would otherwise never reach the test that runs the header's block.
func TestThePlanRatingsWayBackIsOneTextInTheHeaderAndTheGuide(t *testing.T) {
	header := planRatingsWayBackOfTheHeader(t)
	guide := wayBackBlockOfTheGuide(t, planRatingsWayBackMark, "the header of "+planRatingsMigration)
	if header != guide {
		t.Errorf("the way back of the release that rates each plan differs between %s and the header of %s:\n"+
			"the guide:\n%s\nthe header:\n%s\nthe header's block is the one internal/store runs, so the guide "+
			"answers for it only while the two are equal; change them in one commit", wayBackGuide, planRatingsMigration, guide, header)
	}
}

// planRatingsWayBackOfTheHeader is the way back's block the header of planRatingsMigration
// carries: its header lines, up to the first line that is no comment, that stand indented two
// spaces past the prose, each with the comment marker and that indent taken off. A migration that
// is not embedded, and a header with no such lines, stop the test by name.
func planRatingsWayBackOfTheHeader(t *testing.T) string {
	t.Helper()
	embedded, err := migrations.All()
	if err != nil {
		t.Fatalf("read the embedded migrations: %v", err)
	}
	at := slices.IndexFunc(embedded, func(m migrations.Migration) bool { return m.Name == planRatingsMigration })
	if at < 0 {
		t.Fatalf("the daemon embeds no %s, whose header carries the way back %s gives", planRatingsMigration, wayBackGuide)
	}
	var block []string
	for _, line := range strings.Split(embedded[at].SQL, "\n") {
		if !strings.HasPrefix(line, "--") {
			break
		}
		if held, ok := strings.CutPrefix(line, "--   "); ok {
			block = append(block, held)
		}
	}
	if len(block) == 0 {
		t.Fatalf("the header of %s carries no indented block: that block is the way back %s gives", planRatingsMigration, wayBackGuide)
	}
	return strings.Join(block, "\n")
}
