package store

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// movedTables are the four tables the move of dispatch://INFRA-1098's R32 takes into the graph
// store: the priority store's nodes and tuples and the workflow store's.
var movedTables = []string{"priority_nodes", "priority_tuples", "workflow_nodes", "workflow_tuples"}

// movedTable matches a name of movedTables as a word, as grep -w reads one: a name inside a longer
// identifier, such as a constraint's name that begins with a table's, is none.
var movedTable = regexp.MustCompile(`\b(?:` + strings.Join(movedTables, "|") + `)\b`)

// movedTablesInventoryRoots are the directories of the checkout the inventory reads.
var movedTablesInventoryRoots = []string{"packages", "scripts", ".dispatch"}

// movedTablesNamer is one file the inventory lets name a moved table, by its path from the
// checkout's root, with the reason it may.
type movedTablesNamer struct {
	path, reason string
}

// historyKeeps is the reason of a migration that created or edited a moved table before this
// change: the runner keeps the text of every migration it applied, so the file stays as it was.
const historyKeeps = "a migration that created or edited the table before dispatch://INFRA-1098's R32; the runner keeps an applied migration's text as it was"

// theMove is the reason of the move at boot's own code: it reads the four tables, copies them,
// drops them and creates the views of their names, for one release.
const theMove = "the move at boot (storesmove), which reads, copies and drops the four tables and creates the views of their names"

// beforeTheMove is the reason of a test or a test helper that writes the four tables as the
// release before wrote them, before the move it holds to what it must do.
const beforeTheMove = "a test of the move, or its helper, which writes the old tables in the release before's shapes before the move"

// movedTablesNamers is every file of the checkout that may name a moved table, each with why. A
// file joins it in the commit that writes what it names a table for, and leaves it in the commit
// that takes that out, so it holds only files that stand and name a table.
var movedTablesNamers = []movedTablesNamer{
	{"packages/envoy/internal/dispatch/store/migrations/20261112024320_priority_nodes.up.sql", historyKeeps},
	{"packages/envoy/internal/dispatch/store/migrations/20261112024320_priority_nodes.census.sql", historyKeeps + " (its census)"},
	{"packages/envoy/internal/dispatch/store/migrations/20261116235202_workflow_nodes.up.sql", historyKeeps},
	{"packages/envoy/internal/dispatch/store/migrations/20261116235202_workflow_nodes.census.sql", historyKeeps + " (its census)"},
	{"packages/envoy/internal/dispatch/store/moved_tables_inventory_test.go", "this test, which names the four tables it looks for (movedTables)"},
	{"packages/envoy/internal/dispatch/storesmove/storesmove.go", theMove},
	{"packages/envoy/internal/dispatch/storesmove/copy.go", theMove + ": its copy"},
	{"packages/envoy/internal/dispatch/storesmove/swap.go", theMove + ": its swap and the views"},
	{"packages/envoy/internal/dispatch/storesmove/census.go", theMove + ": its census before the drop"},
	{"packages/envoy/internal/dispatch/storesmove/rehearse.go", theMove + ": the pre-deploy census's rehearsal of it"},
	{"packages/envoy/cmd/dispatch/census.go", "the pre-deploy census, which rehearses the move while priority_nodes is a table"},
	{"packages/envoy/internal/dispatch/graphstore/stores_moved.go", "the two triggers that complete what the release before writes through the views workflow_nodes and workflow_tuples, kept for one release"},
	{"packages/envoy/internal/dispatch/storesmove/storesmovetest/storesmovetest.go", beforeTheMove},
	{"packages/envoy/internal/dispatch/storesmove/storesmove_test.go", beforeTheMove},
	{"packages/envoy/internal/dispatch/api/stores_moved_revision_test.go", beforeTheMove + " (TestARevisionReadBeforeTheMoveIsRefusedAfterIt)"},
	{"packages/envoy/cmd/dispatch/stores_rehearsal_test.go", beforeTheMove + " (TestThePreDeployCensusRehearsesTheMoveAndChangesNothing)"},
	{"packages/envoy/cmd/dispatch/main_test.go", "a test that brings a database back to migration 52, before the migrations that create the four tables, so it drops them as that version held none (TestCensusSubcommandExitsOneWhenAPendingMigrationsCensusCountsRows)"},
}

// TestNoFileAtTheHeadNamesTheMovedTablesOutsideItsList is the inventory of the move of the priority
// store and the workflow store into the graph store (dispatch://INFRA-1098's R32). It lists every
// file of the checkout under packages/, scripts/ and .dispatch/ that git does not ignore, tracked
// or not, and that names one of the four tables as a word (movedTable), and compares that list
// with movedTablesNamers in both directions: it fails naming each file it finds that the list
// does not hold, with the tables it names and the first line that names one, and each listed file
// that the checkout does not hold or that names no table any more.
//
// Every reader and writer of the four tables reads and writes the graph store once the move
// stands, so each file it names outside the list is one still to move: a reader or a writer of
// prioritystore, workflowstore and their callers, a test that reads the tables, a list that
// excepts them from the census, or a document that describes them.
func TestNoFileAtTheHeadNamesTheMovedTablesOutsideItsList(t *testing.T) {
	root := checkoutRoot(t)
	files := checkoutFilesUnder(t, root, movedTablesInventoryRoots)
	if len(files) == 0 {
		t.Fatalf("git ls-files lists no file under %v of %s, so the inventory would read nothing", movedTablesInventoryRoots, root)
	}
	found := map[string]movedTablesHit{}
	for _, name := range files {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if bytes.IndexByte(content[:min(len(content), 8192)], 0) >= 0 {
			continue
		}
		if hit, names := movedTablesIn(content); names {
			found[name] = hit
		}
	}
	t.Logf("read %d files under %v; %d name a moved table, and the list holds %d", len(files), movedTablesInventoryRoots, len(found), len(movedTablesNamers))

	listed := map[string]bool{}
	for _, namer := range movedTablesNamers {
		listed[namer.path] = true
		hit, names := found[namer.path]
		switch {
		case !slices.Contains(files, namer.path):
			t.Errorf("the list holds %s (%s), which the checkout does not hold: take it out of movedTablesNamers", namer.path, namer.reason)
		case !names:
			t.Errorf("the list holds %s (%s), which names no moved table any more: take it out of movedTablesNamers", namer.path, namer.reason)
		default:
			t.Logf("listed: %s names %s: %s", namer.path, hit, namer.reason)
		}
	}
	var unlisted []string
	for name := range found {
		if !listed[name] {
			unlisted = append(unlisted, name)
		}
	}
	slices.Sort(unlisted)
	for _, name := range unlisted {
		t.Errorf("%s names %s, and movedTablesNamers does not hold it: move what it reads or writes to the graph store, or list it with the reason it may name the table", name, found[name])
	}
	if len(unlisted) > 0 {
		t.Logf("%d files name a moved table outside the list:\n%s", len(unlisted), strings.Join(unlisted, "\n"))
	}
}

// movedTablesHit is what one file names of the moved tables: each table, once, in the order it
// first names them, and the first line that names one.
type movedTablesHit struct {
	tables    []string
	firstLine int
}

func (h movedTablesHit) String() string {
	return fmt.Sprintf("%s (first at line %d)", strings.Join(h.tables, ", "), h.firstLine)
}

// movedTablesIn is what content names of the moved tables, and whether it names one.
func movedTablesIn(content []byte) (movedTablesHit, bool) {
	var hit movedTablesHit
	for index, line := range strings.Split(string(content), "\n") {
		for _, table := range movedTable.FindAllString(line, -1) {
			if hit.firstLine == 0 {
				hit.firstLine = index + 1
			}
			if !slices.Contains(hit.tables, table) {
				hit.tables = append(hit.tables, table)
			}
		}
	}
	return hit, hit.firstLine != 0
}

// checkoutRoot is the checkout this package stands in, five directories above it.
func checkoutRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "packages", "envoy", "go.mod")); err != nil {
		t.Fatalf("%s holds no packages/envoy/go.mod, so it is no checkout of this repository: %v", root, err)
	}
	return root
}

// checkoutFilesUnder is every regular file of the checkout under dirs that git does not ignore,
// tracked or not, by its path from root, sorted: what a reviewer of the tree reads. A tracked file
// the working copy deleted is left out, since no copy of it stands.
func checkoutFilesUnder(t *testing.T, root string, dirs []string) []string {
	t.Helper()
	args := append([]string{"-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--"}, dirs...)
	listed, err := exec.Command("git", args...).Output()
	if err != nil {
		t.Fatalf("list the checkout's files under %v with git ls-files: %v", dirs, err)
	}
	var files []string
	for _, name := range strings.Split(string(listed), "\x00") {
		if name == "" {
			continue
		}
		if info, err := os.Lstat(filepath.Join(root, name)); err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, name)
	}
	slices.Sort(files)
	return slices.Compact(files)
}
