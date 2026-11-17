package textcheck

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/store/migrations"
)

// The way back of the release that records each step stands in the header of its roll forward,
// 20261117064152_step_starts_roll_forward, which ships it with the migration it re-arms, and in a
// fenced block of docs/kubernetes.md, which is where an operator reads it at a rollback.
// internal/store's TestTheRollForwardMarksUncountedEachIssueTheEarlierReleaseMoved runs the
// header's block; this holds the guide's block to it, byte for byte. The header is read from the
// embedded migrations (internal/store/migrations, as the guide's migration list is), so neither
// half reads a file of another package.
const (
	// stepRecordsRollForward is the version of the roll forward whose header carries the block.
	stepRecordsRollForward = 20261117064152
	// stepRecordsWayBackFirst opens the block in the header and in the guide.
	stepRecordsWayBackFirst = "insert into step_starts_rollback (issue, tree, witness)"
)

// stepRecordsWayBackMark is the line that deletes the roll forward's version row, which closes the
// block and which no other rollback's block carries.
var stepRecordsWayBackMark = fmt.Sprintf("delete from schema_version where version = %d;", stepRecordsRollForward)

// The guide's way back of the release that records each step is the block its roll forward's
// header carries, line for line: an edit to either alone fails here, since the store's test runs
// the header's and an operator runs the guide's.
func TestTheStepRecordsWayBackIsOneTextInTheRollForwardAndTheGuide(t *testing.T) {
	header := stepRecordsWayBackOfTheHeader(t)
	guide := wayBackBlockOfTheGuide(t, stepRecordsWayBackMark, "the header of the step records' roll forward")
	if header == guide {
		return
	}
	t.Errorf("the header of migration %d and %s's way back disagree:\n%s"+
		"the store's test runs the header's block and an operator the guide's; change them in one commit",
		stepRecordsRollForward, wayBackGuide, wayBackBlockDifference(header, guide))
}

// stepRecordsWayBackOfTheHeader is the block the roll forward's header carries, from its first
// statement to the mark, without the comment markers. A header with no such block stops the test:
// an empty block would compare equal to nothing at all.
func stepRecordsWayBackOfTheHeader(t *testing.T) string {
	t.Helper()
	all, err := migrations.All()
	if err != nil {
		t.Fatalf("read the embedded migrations: %v", err)
	}
	for _, migration := range all {
		if migration.Version != stepRecordsRollForward {
			continue
		}
		var block []string
		for _, line := range strings.Split(migration.SQL, "\n") {
			statement, comment := strings.CutPrefix(line, "-- ")
			if !comment || block == nil && statement != stepRecordsWayBackFirst {
				continue
			}
			block = append(block, statement)
			if statement == stepRecordsWayBackMark {
				return strings.Join(block, "\n")
			}
		}
		t.Fatalf("%s carries no way back from %q to %q", migration.Name, stepRecordsWayBackFirst, stepRecordsWayBackMark)
	}
	t.Fatalf("no embedded migration has version %d, the step records' roll forward", stepRecordsRollForward)
	return ""
}
