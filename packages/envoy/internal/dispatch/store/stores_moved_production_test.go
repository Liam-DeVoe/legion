package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/pgmigrate"
)

// storesMoveTables are the six tables the pre-deploy census's rehearsal of the stores' move reads
// and writes (storesmove's rehearsalTables, which this package cannot import: storesmove imports
// it). A pending migration of another tree that touches one of them refuses the rehearsal.
var storesMoveTables = []string{"priority_nodes", "priority_tuples", "workflow_nodes", "workflow_tuples", "graph_nodes", "graph_tuples"}

// migrationsDir is this store's migrations, as the repository holds them at any commit.
const migrationsDir = "packages/envoy/internal/dispatch/store/migrations"

// TestNoMigrationProductionLacksTouchesTheStores is the precondition of the rehearsal of the
// stores' move (dispatch://INFRA-1098's R32, the check c-moved-recorded): production must hold every
// migration of origin/main that touches one of storesMoveTables, or the census of the deploy that
// carries the move refuses the rehearsal and exits 2. MAIN_COMMIT names the commit of origin/main
// the run fetched, and PRODUCTION_COMMIT the commit production's /healthz names; the test reads
// both with git and needs no database.
//
// It lists each migration of MAIN_COMMIT that PRODUCTION_COMMIT does not hold, and judges each
// as the rehearsal judges a pending migration of another tree (pgmigrate.RehearseOther): it
// touches each table pgmigrate.TouchedTables names for it, and each table of a trigger whose
// function it creates or replaces, the trigger's own function and not one that function calls.
// The triggers are those the create trigger statements of MAIN_COMMIT's migrations create, which
// stand in for production's catalog, since no pod reads production's database. It fails naming
// each missing migration that touches one of the six tables, with why.
func TestNoMigrationProductionLacksTouchesTheStores(t *testing.T) {
	mainNamed, productionNamed := strings.TrimSpace(os.Getenv("MAIN_COMMIT")), strings.TrimSpace(os.Getenv("PRODUCTION_COMMIT"))
	if mainNamed == "" && productionNamed == "" {
		t.Skip("MAIN_COMMIT and PRODUCTION_COMMIT name the commits this test compares: the commit of origin/main " +
			"and the commit production's /healthz names (c-moved-recorded)")
	}
	root := checkoutRoot(t)
	mainCommit := commitNamed(t, root, "MAIN_COMMIT", mainNamed)
	productionCommit := commitNamed(t, root, "PRODUCTION_COMMIT", productionNamed)

	mainMigrations := migrationsAt(t, root, mainCommit)
	held := map[string]bool{}
	for _, name := range migrationNamesAt(t, root, productionCommit) {
		held[name] = true
	}
	triggers := triggerFunctions(t, mainMigrations)
	t.Logf("the triggers of the six tables that MAIN_COMMIT %s's migrations create: %v", mainCommit, triggers)

	var lacking, touching []string
	for _, migration := range mainMigrations {
		if held[migration.Name] {
			continue
		}
		step, why := pgmigrate.RehearseOther(migration, storesMoveTables, triggers)
		lacking = append(lacking, migration.Name)
		t.Logf("production lacks %s, which the rehearsal %s: %s", migration.Name, step, why)
		if step == pgmigrate.RehearsalRefuses {
			touching = append(touching, migration.Name+": "+why)
		}
	}
	t.Logf("PRODUCTION_COMMIT %s lacks %d of the %d migrations of MAIN_COMMIT %s; %d of them touch the six tables",
		productionCommit, len(lacking), len(mainMigrations), mainCommit, len(touching))
	if len(touching) > 0 {
		t.Errorf("production runs %s, which lacks %d migrations of %s that touch one of %s, so the deploy's census refuses the rehearsal of the stores' move and exits 2; have %s deployed first:\n  %s",
			productionCommit, len(touching), mainCommit, strings.Join(storesMoveTables, ", "), mainCommit, strings.Join(touching, "\n  "))
	}
}

// commitNamed is the full id of the commit the environment variable variable names, value, which
// the checkout must hold.
func commitNamed(t *testing.T, root, variable, value string) string {
	t.Helper()
	if value == "" {
		t.Fatalf("%s is unset, and the test compares the commit it names with the other one's", variable)
	}
	out, err := gitOutput(root, "rev-parse", "--verify", "--quiet", value+"^{commit}")
	commit := strings.TrimSpace(string(out))
	if err != nil || len(commit) != 40 {
		t.Fatalf("%s=%s names no commit this checkout holds (git rev-parse --verify: %v)", variable, value, err)
	}
	return commit
}

// migrationNamesAt is the name of each forward migration of this store at commit, in name order.
func migrationNamesAt(t *testing.T, root, commit string) []string {
	t.Helper()
	listing, err := gitOutput(root, "ls-tree", "-z", "--name-only", commit+":"+migrationsDir)
	if err != nil {
		t.Fatalf("list %s at %s: %v", migrationsDir, commit, err)
	}
	var names []string
	for name := range strings.SplitSeq(strings.TrimRight(string(listing), "\x00"), "\x00") {
		if strings.HasSuffix(name, ".up.sql") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		t.Fatalf("%s holds no migration at %s", migrationsDir, commit)
	}
	return names
}

// migrationsAt is each forward migration of this store at commit, its name and its text, in name
// order, read in one git cat-file --batch.
func migrationsAt(t *testing.T, root, commit string) []pgmigrate.Migration {
	t.Helper()
	names := migrationNamesAt(t, root, commit)
	var request bytes.Buffer
	for _, name := range names {
		fmt.Fprintf(&request, "%s:%s\n", commit, path.Join(migrationsDir, name))
	}
	cmd := exec.Command("git", "-C", root, "cat-file", "--batch")
	cmd.Stdin = &request
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("read %s at %s with git cat-file --batch: %v: %s", migrationsDir, commit, err, strings.TrimSpace(stderr.String()))
	}
	reader := bufio.NewReader(bytes.NewReader(out))
	migrations := make([]pgmigrate.Migration, 0, len(names))
	for _, name := range names {
		header, err := reader.ReadString('\n')
		fields := strings.Fields(header)
		if err != nil || len(fields) != 3 || fields[1] != "blob" {
			t.Fatalf("read %s at %s: git cat-file answered %q", name, commit, header)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil {
			t.Fatalf("read %s at %s: git cat-file answered the size %q", name, commit, fields[2])
		}
		text := make([]byte, size+1)
		if _, err := io.ReadFull(reader, text); err != nil {
			t.Fatalf("read %s at %s: %v", name, commit, err)
		}
		migrations = append(migrations, pgmigrate.Migration{Name: name, SQL: string(text[:size])})
	}
	return migrations
}

// triggerFunctions is each trigger on one of storesMoveTables that a create trigger statement of
// migrations creates, with the function it runs, as Postgres's own parser reads the statement. A
// migration the parser does not read fails the test, since a trigger it creates would go unread.
func triggerFunctions(t *testing.T, migrations []pgmigrate.Migration) []pgmigrate.TriggerFunction {
	t.Helper()
	var triggers []pgmigrate.TriggerFunction
	for _, migration := range migrations {
		statements, ok := parsedStatements(migration.SQL)
		if !ok {
			t.Fatalf("Postgres's parser does not read %s, so the triggers it creates cannot be read", migration.Name)
		}
		for _, statement := range statements {
			if statement.node != "CreateTrigStmt" {
				continue
			}
			var create struct {
				relationOf
				Funcname []struct {
					String struct {
						Value string `json:"sval"`
					} `json:"String"`
				} `json:"funcname"`
			}
			if json.Unmarshal(statement.body, &create) != nil || len(create.Funcname) == 0 {
				t.Fatalf("%s holds a create trigger the test cannot read: %s", migration.Name, sourceText(statement.text))
			}
			trigger := pgmigrate.TriggerFunction{Table: create.Relation.Name, Function: create.Funcname[len(create.Funcname)-1].String.Value}
			schema := create.Relation.Schema
			if (schema == "" || schema == "public") && slices.Contains(storesMoveTables, trigger.Table) && !slices.Contains(triggers, trigger) {
				triggers = append(triggers, trigger)
			}
		}
	}
	return triggers
}

// gitOutput runs git in root with args and answers its standard output, or an error carrying its
// standard error.
func gitOutput(root string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
