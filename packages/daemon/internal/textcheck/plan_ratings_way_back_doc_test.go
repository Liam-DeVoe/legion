package textcheck

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

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
// and the guide's block is the one under that heading that holds the one line only that
// rollback's block holds (wayBackBlockOfTheGuide). The header's block is read here by a parse of
// its own, which shares no code with internal/store's: two readers that agree with each other
// while both drift are what a copy of a document is most at risk of.
const (
	// planRatingsMigration is the migration whose header carries the block.
	planRatingsMigration = "20261117023932_outbox_kinds_with_the_plan_record.up.sql"
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
	header := planRatingsWayBackOfTheHeader(t)
	guide := wayBackBlockOfTheGuide(t, planRatingsWayBackSection, planRatingsWayBackMark,
		"the header of "+planRatingsMigration)
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

// The guide's list of the migrations recorded above the phase entry sends an operator from the
// migration of the release that rates each plan to that release's own way back: the migration's
// entry links the section that gives the block. The list's own checks hold its names alone
// (phase_entry_migrations_doc_test.go), so an entry that kept the name and lost the link, or
// linked another rollback's section, would pass them and leave an operator who rolls back below
// this release with the phase entry's block alone.
func TestThePlanRecordMigrationsEntryLinksItsWayBack(t *testing.T) {
	migration := strings.TrimSuffix(planRatingsMigration, ".up.sql")
	if err := entryLinkIn(shippedText(t, wayBackGuide), migration, planRatingsWayBackSection); err != nil {
		t.Errorf("%s %v: give it the link [%s](#%s), since a rollback to a release before %s takes that "+
			"section's steps with the phase entry's block", wayBackGuide, err, planRatingsWayBackSection,
			sectionAnchor(planRatingsWayBackSection), migration)
	}
}

// The entry check takes the entry a reader sees and asks it for the link alone: an entry that
// links its section passes, and one that lost the link, links another rollback's section, shows
// its link only inside an HTML comment, stands inside one itself or is written twice is refused.
// Each case is a list as the guide writes one, the entry among others.
func TestTheEntryCheckReadsTheLinkOfTheEntryAReaderSees(t *testing.T) {
	const (
		migration = "20990101000001_rated"
		other     = "20990101000002_other touches none of the six, so the block holds over it.\n"
		entry     = migration + " touches none of the six, so the block holds over it, and a rollback to a " +
			"release before it takes the steps of its own section, "
		link   = "[Rolling back the release that rates each plan](#rolling-back-the-release-that-rates-each-plan).\n"
		linked = entry + link
		// redirected is the entry linking the step records' section, which another list entry links.
		redirected = entry + "[Rolling back the release that records each step]" +
			"(#rolling-back-the-release-that-records-each-step).\n"
	)
	for _, list := range []struct{ shape, text, refusal string }{
		{"the entry linking its section", other + linked + other, ""},
		{"the entry without its link", other + migration + " touches none of the six.\n", "links no section"},
		{"the entry linking another rollback's section", other + redirected, "links no section"},
		{"the link inside an HTML comment", other + entry + "<!-- " + strings.TrimSuffix(link, "\n") + " -->\n",
			"links no section"},
		{"the entry inside an HTML comment", other + "<!--\n" + linked + "-->\n", "no entry"},
		{"the entry written twice", linked + other + linked, "2 entries"},
	} {
		err := entryLinkIn(list.text, migration, planRatingsWayBackSection)
		switch {
		case list.refusal == "" && err != nil:
			t.Errorf("%s: the check refuses the entry: %v", list.shape, err)
		case list.refusal != "" && (err == nil || !strings.Contains(err.Error(), list.refusal)):
			t.Errorf("%s: the check answers %v, want a refusal saying %q: %q", list.shape, err, list.refusal,
				list.text)
		}
	}
}

// entryLinkIn reads text where a reader sees it, every HTML comment taken out (htmlComment), and
// answers nil when the one line opening on migration's name, its entry in the guide's list of
// migrations, links the section heading opens by that heading's anchor (sectionAnchor). A text
// with no such line or with two, and one whose line carries no such link, is refused with why.
func entryLinkIn(text, migration, heading string) error {
	opens := regexp.MustCompile(`^` + regexp.QuoteMeta(migration) + `\b`)
	var entries []string
	for _, line := range strings.Split(htmlComment.ReplaceAllString(text, ""), "\n") {
		if opens.MatchString(line) {
			entries = append(entries, line)
		}
	}
	switch link := "](#" + sectionAnchor(heading) + ")"; {
	case len(entries) == 0:
		return fmt.Errorf("carries no entry a reader sees opening on %s", migration)
	case len(entries) > 1:
		return fmt.Errorf("carries %d entries opening on %s, where the list gives a migration one", len(entries),
			migration)
	case !strings.Contains(entries[0], link):
		return fmt.Errorf("carries an entry of %s that links no section %q, whose anchor is #%s", migration,
			heading, sectionAnchor(heading))
	}
	return nil
}

// sectionAnchor is the anchor a link of the guide names a section by: the heading in lower case,
// each space a hyphen, and every character but a letter, a digit, a hyphen and an underscore
// dropped.
func sectionAnchor(heading string) string {
	var anchor strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			anchor.WriteByte('-')
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			anchor.WriteRune(r)
		}
	}
	return anchor.String()
}
