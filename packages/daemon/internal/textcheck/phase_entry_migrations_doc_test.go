package textcheck

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/store/migrations"
)

// The way back of store migration 20261111225552_phase_entry in docs/kubernetes.md drops six
// columns and deletes that migration's own version row, and the lines after its block name each
// migration recorded above it and say what the block does to it: it holds over the migration, or
// the migration is reversed first. An operator rolls a store back by that list. A migration the
// list leaves out is one nobody told them about, and a name it carries that no migration has
// sends them looking for a way back that does not exist. Every change that adds a migration of the
// daemon puts one more above the phase entry, so these tests hold the list to the embedded
// migrations in both directions. The sentence that opens the list carries no count and no
// contract number: every change that adds a migration would have to edit the one, and every
// change of the daemon API contract the other, and the list it opens is held to the migrations
// of the tree it ships in, which is all the sentence says.
//
// The migrations are read through migrations.All, the set the daemon's binary embeds and its
// Migrate applies, rather than through a copy. That package is the migrations themselves and the
// rule of their names, so importing it puts in this job's filter the files whose change these
// tests must see, and nothing else of the daemon.
//
// The list is what a reader sees of it: a name inside an HTML comment counts for nothing, since
// the rendered guide does not show it. A migration the list describes is one an entry says what
// the block does to: it opens the entry, or opens a clause of it after ", and", so one entry may
// speak for several migrations (entrySubjects). A name anywhere else in an entry is a mention in
// passing, which tells the operator nothing of what the block does to that migration, so it
// describes nothing; the list's every name is still held to the embedded set.
const (
	// phaseEntryMigration is the migration whose way back the list follows, by the name the guide
	// (wayBackGuide, way_back_doc_test.go) writes. The list's subject is every embedded migration
	// with a higher version.
	phaseEntryMigration = "20261111225552_phase_entry"
	// phaseEntryListEnd opens the line after the list, the one that names the identifier of the
	// phase entry's block.
	phaseEntryListEnd = "The `20261111225552` is the identifier of "
	// phaseEntryListOpening is the sentence that opens the list, the whole of its line.
	phaseEntryListOpening = "The migrations recorded above this one are these."
)

var (
	// migrationNameInGuide is a migration's name as the guide writes it: the file's name without
	// `.up.sql`.
	migrationNameInGuide = regexp.MustCompile(`\b\d{14}_\w+`)
	// htmlComment is an HTML comment of the guide's Markdown, which the rendered guide does not
	// show. One never closed is read as hiding the rest of the list, since a comment that opens a
	// line runs to the end of the document.
	htmlComment = regexp.MustCompile(`(?s)<!--.*?(?:-->|$)`)
	// entrySubjects is where an entry says what the block does to a migration: the migrations
	// that open the entry, or open a clause of it after ", and", alone or several joined by
	// " and ", each perhaps followed by the ", of <reference>," of the change it came with. Its
	// group holds those names.
	entrySubjects = regexp.MustCompile(`(?:^|, and )(\d{14}_\w+(?:, of \S+,)?(?: and \d{14}_\w+(?:, of \S+,)?)*)`)
)

// Every migration the daemon embeds above the phase entry is described in the list after the
// phase entry's way back. An operator who rolls back by the list is told nothing of a migration
// it leaves out, or names only in passing: neither that the block holds over it nor that it must
// be reversed first.
func TestTheGuideNamesEveryMigrationAboveThePhaseEntry(t *testing.T) {
	above, _ := embeddedMigrationsAboveThePhaseEntry(t)
	described, _ := phaseEntryList(t)
	var missing []string
	for _, name := range above {
		if !slices.Contains(described, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%s's list of the migrations recorded above %s describes no %s: give each an entry "+
			"beside the one it belongs with, opening on its name or with a clause after \", and\" that "+
			"does, saying what it adds or changes and what the phase entry's block does to it; a name "+
			"inside an HTML comment, or in passing elsewhere in an entry, does not count", wayBackGuide,
			phaseEntryMigration, strings.Join(missing, ", "))
	}
}

// The list names no migration but the ones the daemon embeds above the phase entry. A name that
// no migration has, or one at or below the phase entry, tells the operator of a migration the
// block has nothing to do with, or misspells one it has.
func TestTheGuidesListAboveThePhaseEntryNamesOnlyThoseMigrations(t *testing.T) {
	above, all := embeddedMigrationsAboveThePhaseEntry(t)
	_, named := phaseEntryList(t)
	slices.Sort(named)
	for _, name := range slices.Compact(named) {
		switch {
		case slices.Contains(above, name):
		case slices.Contains(all, name):
			t.Errorf("%s's list of the migrations recorded above %s names %s, which is not above it",
				wayBackGuide, phaseEntryMigration, name)
		default:
			t.Errorf("%s's list of the migrations recorded above %s names %s, and the daemon embeds no "+
				"migration of that name", wayBackGuide, phaseEntryMigration, name)
		}
	}
}

// A migration the list names only inside an HTML comment is one the operator never reads: the
// rendered guide shows nothing of it. So the list counts no name a comment holds, whether the
// comment fills a line, follows an entry, spans lines or is never closed, and still counts every
// name of an entry the reader sees. Each case is a list as the guide writes one, between its
// opening sentence and the line that ends it.
func TestTheListCountsNoMigrationNamedOnlyInAnHTMLComment(t *testing.T) {
	const (
		opening = phaseEntryListOpening + "\n"
		end     = phaseEntryListEnd + "`20261111225552_phase_entry.up.sql`.\n"
	)
	for _, list := range []struct {
		shape, entries string
		want           []string
	}{
		{"a comment on a line of its own",
			"20990101000001_seen adds a column.\n<!-- 20990101000002_hidden adds a table. -->\n",
			[]string{"20990101000001_seen"}},
		{"a comment after an entry",
			"20990101000001_seen adds a column. <!-- 20990101000002_hidden adds a table. -->\n",
			[]string{"20990101000001_seen"}},
		{"a comment across lines",
			"20990101000001_seen adds a column.\n<!--\n20990101000002_hidden adds a table.\n-->\n",
			[]string{"20990101000001_seen"}},
		{"a comment never closed",
			"20990101000001_seen adds a column.\n<!-- 20990101000002_hidden adds a table.\n",
			[]string{"20990101000001_seen"}},
		{"one visible entry for two migrations",
			"20990101000001_seen and 20990101000003_seen_with_it touch none of the six.\n",
			[]string{"20990101000001_seen", "20990101000003_seen_with_it"}},
	} {
		described, named := phaseEntryListIn(t, list.shape, opening+list.entries+end)
		if !slices.Equal(described, list.want) || !slices.Equal(named, list.want) {
			t.Errorf("%s: the list describes %v and names %v, want %v for both: %q", list.shape, described,
				named, list.want, list.entries)
		}
	}
}

// A migration counts as described only where an entry says what the block does to it: where it
// opens the entry, or opens a clause of it after ", and", alone or with others joined by "and",
// each perhaps followed by the ", of <reference>," of the change it came with. A name anywhere
// else in an entry is a mention in passing, which tells the operator nothing of what the block
// does to that migration, so it describes nothing, while the list still names it and the other
// test still holds that name to the embedded set. The cases are the shapes the guide's entries
// take, and two mentions in passing.
func TestTheListDescribesOnlyTheMigrationsThatOpenAnEntryOrAClause(t *testing.T) {
	const (
		opening = phaseEntryListOpening + "\n"
		end     = phaseEntryListEnd + "`20261111225552_phase_entry.up.sql`.\n"
	)
	for _, list := range []struct {
		shape, entry        string
		described, mentions []string
	}{
		{"one migration opening its entry",
			"20990101000001_alone adds a column and touches none of the six.",
			[]string{"20990101000001_alone"}, nil},
		{"one migration and the change it came with",
			"20990101000001_alone, of owner/repo#1, adds a column and touches none of the six.",
			[]string{"20990101000001_alone"}, nil},
		{"several migrations joined by and",
			"20990101000001_first and 20990101000002_second, of owner/repo#2, and 20990101000003_third, " +
				"of owner/repo#3, touch none of the six either.",
			[]string{"20990101000001_first", "20990101000002_second", "20990101000003_third"}, nil},
		{"a migration opening a clause after \", and\"",
			"20990101000001_first writes into three of the six, and 20990101000002_second adds the table " +
				"it marks, so the block above reverses both first.",
			[]string{"20990101000001_first", "20990101000002_second"}, nil},
		{"a mention in parentheses",
			"20990101000001_alone adds a column. (The same release also includes 20990101000002_passing.)",
			[]string{"20990101000001_alone"}, []string{"20990101000002_passing"}},
		{"a mention after and inside a clause",
			"20990101000001_alone adds a column and 20990101000002_passing reads it.",
			[]string{"20990101000001_alone"}, []string{"20990101000002_passing"}},
	} {
		described, named := phaseEntryListIn(t, list.shape, opening+list.entry+"\n"+end)
		if want := append(slices.Clone(list.described), list.mentions...); !slices.Equal(described, list.described) ||
			!slices.Equal(named, want) {
			t.Errorf("%s: the list describes %v and names %v, want %v described and %v named: %q", list.shape,
				described, named, list.described, want, list.entry)
		}
	}
}

// embeddedMigrationsAboveThePhaseEntry is every migration the daemon embeds above the phase entry,
// and every migration it embeds, each by the name the guide writes, in version order. A set
// without the phase entry stops the test, since the list it is held to follows that
// migration's way back.
func embeddedMigrationsAboveThePhaseEntry(t *testing.T) (above, all []string) {
	t.Helper()
	embedded, err := migrations.All()
	if err != nil {
		t.Fatalf("read the embedded migrations: %v", err)
	}
	for _, migration := range embedded {
		all = append(all, strings.TrimSuffix(migration.Name, ".up.sql"))
	}
	// migrations.All answers in ascending version order, one migration to a version, so the
	// migrations above the phase entry are the ones after it.
	at := slices.Index(all, phaseEntryMigration)
	if at < 0 {
		t.Fatalf("the daemon embeds no %s, whose way back the list in %s follows", phaseEntryMigration,
			wayBackGuide)
	}
	return all[at+1:], all
}

// phaseEntryList is what the list in the guide describes and names (phaseEntryListIn).
func phaseEntryList(t *testing.T) (described, named []string) {
	t.Helper()
	return phaseEntryListIn(t, wayBackGuide, shippedText(t, wayBackGuide))
}

// phaseEntryListIn reads the list in text, the guide called name, where a reader sees it: the
// lines after its opening sentence, up to the line phaseEntryListEnd opens, with every HTML
// comment taken out. It answers the migrations the list describes and every migration name it
// carries. A text with no such sentence or with two, with no such line after it, or whose list
// names no migration stops the test: each would otherwise read as a list that names nothing,
// which TestTheGuidesListAboveThePhaseEntryNamesOnlyThoseMigrations passes.
func phaseEntryListIn(t *testing.T, name, text string) (described, named []string) {
	t.Helper()
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		if line != phaseEntryListOpening {
			continue
		}
		if start >= 0 {
			t.Fatalf("%s opens the list of the migrations above %s twice, at lines %d and %d",
				name, phaseEntryMigration, start+1, i+1)
		}
		start = i
	}
	if start < 0 {
		t.Fatalf("%s carries no line %q: that sentence, which carries no count and no contract number, "+
			"opens the list of the migrations recorded above %s after that migration's way back", name,
			phaseEntryListOpening, phaseEntryMigration)
	}
	end := slices.IndexFunc(lines[start+1:], func(line string) bool { return strings.HasPrefix(line, phaseEntryListEnd) })
	if end < 0 {
		t.Fatalf("%s carries no line opening %q after line %d, which ends the list of the migrations above %s",
			name, phaseEntryListEnd, start+1, phaseEntryMigration)
	}
	visible := htmlComment.ReplaceAllString(strings.Join(lines[start+1:start+1+end], "\n"), "")
	named = migrationNameInGuide.FindAllString(visible, -1)
	if len(named) == 0 {
		t.Fatalf("%s's list after line %d names no migration", name, start+1)
	}
	for _, entry := range strings.Split(visible, "\n") {
		for _, subjects := range entrySubjects.FindAllStringSubmatch(strings.TrimLeft(entry, " \t"), -1) {
			described = append(described, migrationNameInGuide.FindAllString(subjects[1], -1)...)
		}
	}
	return described, named
}
