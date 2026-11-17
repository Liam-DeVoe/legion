package textcheck

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The way back of store migration 20261111225552 stands in the migration's header, which ships
// it with the migration, and in a fenced block of docs/kubernetes.md, which is where an operator
// reads it at a rollback; internal/daemon's
// TestTheUpgradesWayBackBlockIsOneTextInTheHeaderAndTheGuide holds the two to one text. That
// comparison is in two halves, because a test that reads the guide puts the guide in its job's
// filter — a declared read applies to every compiled source file of a run — and a
// documentation-only edit would then start the whole daemon Go lane. The product half compares
// the migration's header with internal/daemon/upgrade_way_back_extract.txt, a copy of the guide's
// block committed beside it; this half holds that copy to the block the guide carries, byte for
// byte. Neither half alone is the guarantee: together they are the one the single test used to
// give.
//
// The way back of contract 146 stands the same way, in the headers of its four migrations and in
// a fenced block of the guide of its own, and is held the same way: internal/daemon's
// TestTheArchitectEntryUpgradesWayBackBlockIsOneTextInItsHeadersAndTheGuide compares the four
// headers with internal/daemon/upgrade_architect_entry_way_back_extract.txt, and this file holds
// that copy to the guide's block.
//
// Each block is found under the heading of its section by the one line only that rollback's block
// holds, rather than by its place in the document, which every edit above it moves: the guide
// carries a psql heredoc for several contracts' rollbacks, and the block of any other would pass
// for the one meant if the first fence were taken (wayBackBlockIn).
const (
	// The committed copies, each read from the package it is about, and the guide, read under
	// ../../../.. by shippedText.
	wayBackExtract               = "../daemon/upgrade_way_back_extract.txt"
	architectEntryWayBackExtract = "../daemon/upgrade_architect_entry_way_back_extract.txt"
	wayBackGuide                 = "docs/kubernetes.md"
	// wayBackMark is the line that deletes the migration's version row, which no other rollback's
	// block carries. The version is internal/daemon's phaseEntryMigration, written out because
	// this file reaches no product package: importing internal/daemon would put the daemon, and
	// every file that package's own tests read, back in this job's filter, which is what
	// splitting the check took out. A version that moved cannot pass silently either way: the
	// header, the guide and the extract all carry it, so a guide whose block names another
	// version holds no fence this mark finds, which fails loudly.
	wayBackMark = "delete from schema_version where version = 20261111225552;"
	// architectEntryWayBackMark is the line that deletes the version row of
	// 20261114061900_phase_entry_verbatim, the newest of contract 146's four migrations and the
	// first that block reverses, which no other rollback's block carries. It is written out for
	// wayBackMark's reason.
	architectEntryWayBackMark = "delete from schema_version where version = 20261114061900;"
	// phaseEntryWayBackSection is the heading of the section the phase entry's block and contract
	// 146's both stand in.
	phaseEntryWayBackSection = "Pinning a release that drops what a running pod still uses"
)

// markdownHeading is a line that opens a section of the guide: its first group is the run of `#`,
// whose length is the section's level, and its second the heading's words.
var markdownHeading = regexp.MustCompile(`^(#{1,6})(?:[ \t]+(.*?))?[ \t]*$`)

// The committed copy of the guide's way back is the guide's own block, line for line. An edit to
// either side alone fails here: a line changed in the guide and not in the copy would otherwise
// never reach the migration's own check, which reads the copy and not the guide.
func TestTheWayBackExtractIsTheGuidesWayBackBlock(t *testing.T) {
	assertTheExtractIsTheGuidesBlock(t, wayBackExtract, phaseEntryWayBackSection, wayBackMark)
}

// The committed copy of contract 146's way back is the guide's own block, line for line, for the
// same reason: the four migrations' own check reads the copy and not the guide.
func TestTheArchitectEntryWayBackExtractIsTheGuidesWayBackBlock(t *testing.T) {
	assertTheExtractIsTheGuidesBlock(t, architectEntryWayBackExtract, phaseEntryWayBackSection,
		architectEntryWayBackMark)
}

// assertTheExtractIsTheGuidesBlock holds the body of extract, a committed copy, to the fenced
// block of the guide holding mark under heading, and names each line the two disagree on.
func assertTheExtractIsTheGuidesBlock(t *testing.T, extract, heading, mark string) {
	t.Helper()
	copied := quotedBody(t, extract)
	if copied == "" {
		t.Fatalf("%s carries nothing under its `#` header: internal/daemon's check of the migration "+
			"headers has no copy of the guide's block to hold them to", extract)
	}
	guide := wayBackBlockOfTheGuide(t, heading, mark, extract)
	if copied == guide {
		return
	}
	t.Errorf("%s and %s's way back disagree:\n%s"+
		"the extract is the copy internal/daemon checks the migration headers against, so the guide "+
		"answers for the migrations only while the two are equal; change them in one commit",
		extract, wayBackGuide, wayBackBlockDifference(copied, guide))
}

// wayBackBlockOfTheGuide is a way back as docs/kubernetes.md carries it (wayBackBlockIn): the
// content of the one fenced block holding mark, in the section heading opens.
// extract is the committed copy of that block, which a failure names. A guide with no such block
// stops the test rather than skipping it: an absence here would read exactly like a pass, and
// what this check guards is the copy an operator runs at a rollback.
func wayBackBlockOfTheGuide(t *testing.T, heading, mark, extract string) string {
	t.Helper()
	block, err := wayBackBlockIn(shippedText(t, wayBackGuide), heading, mark)
	if err != nil {
		t.Fatalf("%s %v; that block is the way back an operator runs, and %s is its copy", wayBackGuide, err,
			extract)
	}
	return block
}

// wayBackBlockIn answers the content of text's fenced block holding mark, without its fence lines.
// That block must be the only one of the text that holds mark, and stand in the section heading
// opens: from that heading's line to the next heading of its level or above, where a line inside
// a fenced block opens no section. Each other text is refused, naming why: one with no such
// heading or with two, one where no block holds mark, one where a second block holds it, which an
// operator could take for the way back, and one whose block stands outside the section, which is
// not the block the section tells them to run. A block an HTML comment hides from the rendered
// guide is not this reader's to refuse: scripts/semantic-line-breaks.guides.test.ts refuses every
// raw HTML block of docs/kubernetes.md, a comment among them.
func wayBackBlockIn(text, heading, mark string) (string, error) {
	lines := strings.Split(text, "\n")
	// section is the line of the heading and end the line of the next heading at its level or above,
	// one past the text where there is none; holding is the opening line of each block holding mark.
	section, level, end := 0, 0, len(lines)+1
	var (
		block   []string
		opened  int
		inside  bool
		holding []int
		found   string
	)
	for i, line := range lines {
		switch number := i + 1; {
		case strings.HasPrefix(line, "```") && !inside:
			inside, opened, block = true, number, nil
		case strings.HasPrefix(line, "```"):
			inside = false
			if slices.Contains(block, mark) {
				holding, found = append(holding, opened), strings.Join(block, "\n")
			}
		case inside:
			block = append(block, line)
		default:
			opens := markdownHeading.FindStringSubmatch(line)
			switch {
			case opens == nil:
			case opens[2] == heading && section > 0:
				return "", fmt.Errorf("carries the heading %q twice, at lines %d and %d: a link to it reaches "+
					"the first alone", heading, section, number)
			case opens[2] == heading:
				section, level = number, len(opens[1])
			case section > 0 && end > len(lines) && len(opens[1]) <= level:
				end = number
			}
		}
	}
	switch {
	case section == 0:
		return "", fmt.Errorf("carries no heading %q, whose section shows the way back", heading)
	case len(holding) == 0:
		return "", fmt.Errorf("carries no fenced block holding %q", mark)
	case len(holding) > 1:
		return "", fmt.Errorf("carries %d fenced blocks holding %q, opening at lines %v: an operator cannot "+
			"tell which is the way back, so keep the one under %q alone", len(holding), mark, holding, heading)
	case holding[0] < section || holding[0] > end:
		return "", fmt.Errorf("carries its fenced block holding %q at line %d, outside the section %q opens "+
			"at line %d: the operator runs the block that section shows", mark, holding[0], heading, section)
	}
	return found, nil
}

// The reader takes the way back an operator reads under its heading, and nothing else in its
// place. The block in its section is taken, past a shell comment of another block there, which
// opens no section. The same block deleted, moved to the next section, or given a copy before
// its section or beside it is refused, as is a guide without that heading or with it twice. Each
// case is a guide as docs/kubernetes.md writes one: the section, the one before it and the one
// after it.
func TestTheWayBackReaderTakesTheOneBlockOfItsSection(t *testing.T) {
	const (
		heading = "Rolling back the release that rates each plan"
		mark    = "delete from outbox where kind = 'plan_record';"
		block   = "-- The writes of the runs' plans onto Dispatch.\n" + mark
		fenced  = "```sql\n" + block + "\n```\n"
		before  = "#### Rolling back the release that records each step\n\nRun its block first.\n\n"
		opening = "#### " + heading + "\n\nRun this with the daemon stopped:\n\n"
		after   = "\nThe header carries this same block.\n\n" +
			"#### Rolling back the release whose update round ends at its bound\n\nRun its block too.\n"
		shell = "```sh\n# Stop the daemon first.\nkubectl -n legion scale deployment/legion --replicas=0\n```\n\n"
	)
	for _, guide := range []struct{ shape, text, refusal string }{
		{"the block in its section", before + opening + fenced + after, ""},
		{"the block past a shell comment of another block", before + opening + shell + fenced + after, ""},
		{"the block deleted", before + opening + after, "no fenced block"},
		{"the block moved to the next section", before + opening + after + "\n" + fenced, "outside the section"},
		{"a copy of the block before its section", fenced + "\n" + before + opening + fenced + after,
			"2 fenced blocks"},
		{"a copy of the block beside it", before + opening + fenced + "\n" + fenced + after, "2 fenced blocks"},
		{"no heading of that section", before + fenced + after, "no heading"},
		{"the heading twice", before + opening + fenced + after + "\n" + opening + "Nothing here.\n", "twice"},
	} {
		got, err := wayBackBlockIn(guide.text, heading, mark)
		switch {
		case guide.refusal == "" && (err != nil || got != block):
			t.Errorf("%s: the reader answers %q and %v, want %q", guide.shape, got, err, block)
		case guide.refusal != "" && (err == nil || !strings.Contains(err.Error(), guide.refusal)):
			t.Errorf("%s: the reader answers %q and %v, want a refusal saying %q", guide.shape, got, err,
				guide.refusal)
		}
	}
}

// wayBackBlockDifference renders the lines the two copies disagree on, each with its number
// inside the block, so a failing run names the edit to make rather than two blocks to compare by
// eye. internal/daemon keeps a rendering of the same shape for the header and the copy; this file
// may not import that package (see wayBackMark above).
func wayBackBlockDifference(extract, guide string) string {
	extractLines, guideLines := strings.Split(extract, "\n"), strings.Split(guide, "\n")
	var difference strings.Builder
	for i := range max(len(extractLines), len(guideLines)) {
		copied, held := "", ""
		if i < len(extractLines) {
			copied = extractLines[i]
		}
		if i < len(guideLines) {
			held = guideLines[i]
		}
		if copied == held {
			continue
		}
		fmt.Fprintf(&difference, "line %d:\n\tthe extract: %q\n\tthe guide:   %q\n", i+1, copied, held)
	}
	return difference.String()
}
