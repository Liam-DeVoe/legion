package textcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/lockread/plants"
)

// lockReadRoots are the Go modules whose tests run against a Postgres server other tests share,
// from this directory: the envoy module, with packages/envoy/infra beneath it, and the Legion
// daemon's.
var lockReadRoots = []string{"../..", "../../../daemon"}

// lockReadRepository is the repository's root, from this directory: an offence's path and an
// exemption's path are relative to it.
const lockReadRepository = "../../../.."

// lockReadHelper is the one directory of test code that may name lock or session state: lockread,
// the one reader of it, its proof and its plants. lockReadRuleFile is this file, which names what
// it refuses.
const (
	lockReadHelper   = "packages/envoy/lockread/"
	lockReadRuleFile = "packages/envoy/internal/textcheck/lock_reads_test.go"
)

// lockStateName is a name of a view or function of Postgres that reads the state of locks or of
// sessions, which list every database's on the server, in any case and qualified or not:
//
//   - locks: pg_locks and pg_lock_status, the function behind it; pg_blocking_pids and
//     pg_safe_snapshot_blocking_pids, which name the backends that block one; pg_prepared_xacts,
//     every database's prepared transactions, which hold locks;
//   - sessions: pg_stat_activity and pg_stat_get_activity, the function behind it; the
//     per-backend statistics functions pg_stat_get_backend_* (pg_stat_get_backend_idset names
//     every backend); pg_stat_ssl and pg_stat_gssapi, every backend's connection; and
//     pg_stat_progress_* with pg_stat_get_progress_info, every backend's progress.
//
// Taking a lock is none of them: pg_advisory_lock and its kind act in the database of the session
// that calls them, and Postgres keys each advisory lock by its database, so no such call can read
// or wait on another database's. pg_stat_database is none either: it holds per-database counters,
// no lock and no backend.
var lockStateName = regexp.MustCompile(`(?i)\bpg_(locks|lock_status|blocking_pids|safe_snapshot_blocking_pids|prepared_xacts|stat_activity|stat_get_activity|stat_get_backend_\w+|stat_ssl|stat_gssapi|stat_progress_\w+|stat_get_progress_info)\b`)

// lockReadExemption is one string literal of a test file outside lockread that may name lock or
// session state: the file, the function that holds it and the literal itself, each exactly, and
// why. A literal the entry does not name exactly, in that function or another, is refused; so is
// an entry whose literal is not in its function, and one whose function sends a statement that
// names lock or session state to the server (lockReadQueryCalls).
type lockReadExemption struct {
	path, function, literal, why string
}

// lockReadExemptions are the literals of test code that name lock or session state and read
// nothing: each is a test's statement of what production text says, read against no server.
var lockReadExemptions = []lockReadExemption{
	{"packages/envoy/internal/broker/store/migrate_guard_test.go", "TestMigrateBoundsTheWaitForALockAnotherTransactionHolds", "pg_stat_activity",
		"the runner's lock-timeout error names the query an operator runs to find the holder, and the test requires it to"},
	{"packages/envoy/internal/dispatch/store/migrate_guard_test.go", "TestMigrateBoundsTheWaitForALockAnotherTransactionHolds", "pg_stat_activity",
		"the runner's lock-timeout error names the query an operator runs to find the holder, and the test requires it to"},
	{"packages/envoy/loadtest/pgstats_test.go", "fakePGStats.relationColumns", "pg_locks",
		"the fake database answers the load test's read of a relation's columns by the relation's name; no server runs it"},
	{"packages/envoy/loadtest/pgstats_test.go", "TestPGStatementsWritesTheStepsStatementsAndItsLockWaits", "pg_locks.waitstart",
		"the load test's report names the column its lock waits' length comes from, and the test requires that name"},
	{"packages/envoy/loadtest/pgstats_test.go", "TestPGLockWaitsQueryFollowsTheCatalogue", "pg_blocking_pids",
		"the test requires the load test's lock read not to call that function, by its name in the read's text"},
	{"packages/envoy/loadtest/tables_test.go", "TestTablesRenderTheSamplersAndTheirFailedReads", "pg_locks",
		"the load test's report names the view each failed read read, and the test requires that name"},
	{"packages/envoy/loadtest/tables_test.go", "TestTablesRenderTheSamplersAndTheirFailedReads", "pg_stat_activity",
		"the load test's report names the view each failed read read, and the test requires that name"},
}

// lockReadTestPrograms are the programs only a test suite runs, by directory from the
// repository's root: each counts as test code, so what it imports does too, as a test file's
// imports do. The request seeder is one: Dispatch's browser suite runs it against its broker
// (packages/dispatch/e2e/real-broker.ts), and it imports brokertest and, through it, the broker's
// storetest, which a program that ships would make code that ships. A directory listed here that
// holds no Go file is an offence, so the list cannot outlive its programs.
var lockReadTestPrograms = []string{"packages/envoy/internal/broker/brokertest/requestseed"}

// lockReadQueryCalls are the methods by which test code sends a statement to the server.
var lockReadQueryCalls = map[string]bool{"Query": true, "QueryRow": true, "Exec": true, "Queue": true, "QueryContext": true, "QueryRowContext": true, "ExecContext": true}

// lockReadCounts is what lockStateNamesOutsideLockread read: Go files, those of them that are test
// code, the string literals of that test code, the literals that name lock or session state in
// lockread's own files, and the literals an exemption allowed.
type lockReadCounts struct {
	files, testFiles, literals, helperNames, exempt int
}

// The tests of these modules share one Postgres server: storetest.Open clones a database per test
// from one template, the packages of a run test beside each other, and runs beside each other on
// one server. pg_locks and pg_stat_activity list every database's locks and backends, and every
// clone holds the same tables under the same oids and takes the same advisory keys, so a read of
// either that keeps to no database counts another test's waiter, or names another test's holder,
// as its own: the API's upload lock probe named another test's spec settlement as its issue's
// holder and failed. A check that reads a statement's text to judge whether it keeps to its own
// database can always be passed by a spelling it has not seen. So every read of lock or session
// state in test code goes through lockread, whose sources each keep to the database they run in,
// and whose own test runs each against a second database holding planted waiters; and no Go file
// of test code outside lockread names any of those views or functions (lockStateName) in a string
// literal, but the literals lockReadExemptions names.
//
// Test code is every _test.go file, every program lockReadTestPrograms names, and every file of a
// package only test code imports (testCode). The rule reads every string literal of each such file:
// a comment or a Go name reads nothing. What it does not see: a name assembled from parts at run
// time ("pg_" + "locks"), which no literal of the file holds whole, and a statement read from a file
// that is not Go. Each such read is found by reading it.
//
// Production code is not this rule's: it names these views unqualified and reads the real catalog,
// and nothing in a test database changes that.
func TestNoTestFileOutsideLockreadNamesLockOrSessionState(t *testing.T) {
	offences, counts := lockStateNamesOutsideLockread(t, lockReadRepository, lockReadRoots, lockReadTestPrograms, lockReadHelper, lockReadRuleFile, lockReadExemptions)
	if counts.testFiles == 0 || counts.literals == 0 || counts.helperNames == 0 {
		t.Fatalf("UNKNOWN: the rule read %d Go files, %d of them test code, %d string literals of test code, and %d names of lock or session state in lockread's files: it cannot see what it guards", counts.files, counts.testFiles, counts.literals, counts.helperNames)
	}
	t.Logf("read %d Go files, %d of them test code, and %d string literals of test code: %d literals of lockread's own files name lock or session state, %d are exempt", counts.files, counts.testFiles, counts.literals, counts.helperNames, counts.exempt)
	if len(offences) > 0 {
		t.Fatalf("%d string literals of test code outside %s name lock or session state:\n\t%s\n"+
			"read it through lockread (Locks, Activity, BlockingPIDs, BackendWAL, Slots), which keeps to the "+
			"database the statement runs in; a literal that reads nothing goes in lockReadExemptions with why",
			len(offences), lockReadHelper, strings.Join(offences, "\n\t"))
	}
}

// lockStateNamesOutsideLockread walks the Go modules under roots and names, as "<path>:<line>:
// <function>: <literal>", every string literal of test code (testCode) outside helper and other
// than rule that names lock or session state (lockStateName), but those exempt names exactly; each
// exemption whose literal its function does not hold, or whose function sends a statement naming
// lock or session state (lockReadQueryCalls); and each of programs that holds no Go file. Paths
// are relative to repository.
func lockStateNamesOutsideLockread(t *testing.T, repository string, roots, programs []string, helper, rule string, exempt []lockReadExemption) ([]string, lockReadCounts) {
	t.Helper()
	files, missing, read := testCode(t, repository, roots, programs)
	counts := lockReadCounts{files: read, testFiles: len(files)}
	var offences []string
	for _, program := range missing {
		offences = append(offences, program+": listed as a program only tests run, but it holds no Go file")
	}
	found := make([]bool, len(exempt))
	fileSet := token.NewFileSet()
	for _, source := range files {
		text, err := os.ReadFile(source.path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fileSet, source.path, text, 0)
		if err != nil {
			t.Fatal(err)
		}
		ownRule := source.rel == rule
		inHelper := strings.HasPrefix(source.rel, helper)
		for _, decl := range file.Decls {
			holder := declarationName(decl)
			ast.Inspect(decl, func(node ast.Node) bool {
				literal, ok := node.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatalf("%s: unquote %s: %v", source.rel, literal.Value, err)
				}
				counts.literals++
				if !lockStateName.MatchString(value) || ownRule {
					return true
				}
				if inHelper {
					counts.helperNames++
					return true
				}
				for i, entry := range exempt {
					if entry.path == source.rel && entry.function == holder && entry.literal == value {
						found[i] = true
						counts.exempt++
						return true
					}
				}
				offences = append(offences, fmt.Sprintf("%s:%d: %s: %q", source.rel, fileSet.Position(literal.Pos()).Line, holder, value))
				return true
			})
			for _, entry := range exempt {
				if entry.path != source.rel || entry.function != holder {
					continue
				}
				if name := queryNamingLockState(decl); name != "" {
					offences = append(offences, fmt.Sprintf("%s %s: exempt for %q, but it sends a statement naming %s to the server", entry.path, entry.function, entry.literal, name))
				}
			}
		}
	}
	for i, entry := range exempt {
		if !found[i] {
			offences = append(offences, fmt.Sprintf("%s %s: exempt for %q, but that function holds no such literal", entry.path, entry.function, entry.literal))
		}
	}
	slices.Sort(offences)
	return offences, counts
}

// declarationName is the name of what decl declares, as an offence names the holder of a
// literal: a function's name, a method's receiver type and name, or the names a declaration of
// constants, variables or types declares, joined by commas.
func declarationName(decl ast.Decl) string {
	switch decl := decl.(type) {
	case *ast.FuncDecl:
		name := decl.Name.Name
		if decl.Recv != nil && len(decl.Recv.List) == 1 {
			receiver := decl.Recv.List[0].Type
			if star, ok := receiver.(*ast.StarExpr); ok {
				receiver = star.X
			}
			if index, ok := receiver.(*ast.IndexExpr); ok {
				receiver = index.X
			}
			if named, ok := receiver.(*ast.Ident); ok {
				name = named.Name + "." + name
			}
		}
		return name
	case *ast.GenDecl:
		var names []string
		for _, spec := range decl.Specs {
			switch spec := spec.(type) {
			case *ast.ValueSpec:
				for _, name := range spec.Names {
					names = append(names, name.Name)
				}
			case *ast.TypeSpec:
				names = append(names, spec.Name.Name)
			}
		}
		return strings.Join(names, ",")
	}
	return ""
}

// queryNamingLockState is the first name of lock or session state that a string literal among the
// arguments of a call of decl to a method of lockReadQueryCalls holds, or "" for none.
func queryNamingLockState(decl ast.Decl) string {
	name := ""
	ast.Inspect(decl, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || name != "" {
			return name == ""
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !lockReadQueryCalls[selector.Sel.Name] {
			return true
		}
		for _, argument := range call.Args {
			ast.Inspect(argument, func(node ast.Node) bool {
				literal, ok := node.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING || name != "" {
					return name == ""
				}
				if value, err := strconv.Unquote(literal.Value); err == nil {
					name = lockStateName.FindString(value)
				}
				return name == ""
			})
		}
		return name == ""
	})
	return name
}

// The rule refuses, by its file, line, function and literal: each read the reviews of this change
// planted against the earlier text check (package plants), and each other spelling below, in a test
// file, in a fixture only tests import and in a package only a listed test program imports, in any
// case and qualified or not. It passes the same literal in lockread's directory, in this rule's own
// file and in a package production code imports; a name in a comment; a call that takes an advisory
// lock; and a name assembled at run time from parts, which it states as its limit. Of exemptions, it
// passes an exact one; refuses a second, real read in an exempted function; and refuses an entry
// whose literal is not in its function, one whose function sends the exempt literal to the server,
// and a listed program that is not there.
func TestTheLockReadRuleSeesWhatItGuards(t *testing.T) {
	repository := t.TempDir()
	write := func(path, source string) {
		t.Helper()
		full := filepath.Join(repository, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source := []string{"package a", "", `import (`, `	"fmt"`, `	"strings"`, "", `	_ "example.com/m/fixturetest"`, `	_ "example.com/m/lockread"`, `)`, "",
		"var _ = fmt.Sprint", "var _ = strings.Join", "", "type querier interface{ Exec(string) }", ""}
	var want []string
	refuse := func(comment, function, body, literal string) {
		source = append(source, "// "+comment, "func "+function+"(q querier) { "+body+" }")
		want = append(want, fmt.Sprintf("m/a/a_test.go:%d: %s: %q", len(source), function, literal))
	}
	pass := func(comment, function, body string) {
		source = append(source, "// "+comment, "func "+function+"(q querier) { "+body+" }")
	}
	for i, plant := range plants.All {
		refuse(plant.Name+": "+plant.What, fmt.Sprintf("plant%d", i), "_ = "+strconv.Quote(plant.SQL), plant.SQL)
	}
	refuse("any case", "upperCase", "_ = `select count(*) from PG_LOCKS where not granted`", "select count(*) from PG_LOCKS where not granted")
	refuse("qualified", "qualified", "_ = `select count(*) from pg_catalog.pg_stat_activity`", "select count(*) from pg_catalog.pg_stat_activity")
	refuse("quoted", "quoted", "_ = `select count(*) from \"pg_locks\"`", `select count(*) from "pg_locks"`)
	refuse("the view's name alone, in a variable", "viewAlone", `view := "pg_locks"; _ = "select count(*) from " + view`, "pg_locks")
	refuse("a part of strings.Join", "joined", `_ = strings.Join([]string{"select count(*) from", "pg_stat_activity"}, " ")`, "pg_stat_activity")
	refuse("fmt.Sprintf's format", "sprintf", "_ = fmt.Sprintf(`select count(*) from pg_locks where database = %d`, 1)", "select count(*) from pg_locks where database = %d")
	refuse("the blockers' function", "blockers", "_ = `select pg_blocking_pids($1)`", "select pg_blocking_pids($1)")
	refuse("the function behind pg_locks", "lockStatus", "_ = `select * from pg_lock_status()`", "select * from pg_lock_status()")
	refuse("every backend's id", "backendIDs", "_ = `select pg_stat_get_backend_idset()`", "select pg_stat_get_backend_idset()")
	refuse("every backend's progress", "progress", "_ = `select * from pg_stat_progress_create_index`", "select * from pg_stat_progress_create_index")
	refuse("a failure's message", "message", `_ = "read pg_locks: %v"`, "read pg_locks: %v")
	pass("a comment that names pg_locks reads nothing", "comment", "_ = 1")
	pass("taking an advisory lock reads no other database's", "advisory", "_ = `select pg_advisory_xact_lock(1)`")
	pass("the limit: a name assembled at run time from parts", "assembledAtRunTime", `_ = "select count(*) from " + "pg_" + "locks"`)
	pass("per-database counters, no lock and no backend", "statDatabase", "_ = `select sum(numbackends) from pg_stat_database`")
	pass("an exact exemption", "exemptExactly", `_ = "pg_stat_activity"`)
	source = append(source, "// an exemption's function with a real read beside its literal",
		"func exemptWithARealRead(q querier) { _ = \"pg_stat_activity\"; q.Exec(`select count(*) from pg_locks`) }")
	want = append(want, fmt.Sprintf("m/a/a_test.go:%d: exemptWithARealRead: %q", len(source), "select count(*) from pg_locks"),
		`m/a/a_test.go exemptWithARealRead: exempt for "pg_stat_activity", but it sends a statement naming pg_locks to the server`)
	source = append(source, "// an exemption's function that sends its literal to the server",
		`func exemptSentToTheServer(q querier) { q.Exec("pg_stat_activity") }`)
	want = append(want, `m/a/a_test.go exemptSentToTheServer: exempt for "pg_stat_activity", but it sends a statement naming pg_stat_activity to the server`)

	write("m/go.mod", "module example.com/m\n")
	write("m/a/a_test.go", strings.Join(append(source, ""), "\n"))
	write("m/fixturetest/fixture.go", "package fixturetest\n\nfunc Waiters() { _ = `select count(*) from pg_locks where not granted` }\n")
	want = append(want, `m/fixturetest/fixture.go:3: Waiters: "select count(*) from pg_locks where not granted"`)
	write("m/prod/prod.go", "package prod\n\nfunc Count() { _ = `select count(*) from pg_stat_activity` }\n")
	write("m/main.go", "package main\n\nimport _ \"example.com/m/prod\"\n\nfunc main() {}\n")
	write("m/seed/main.go", "package main\n\nimport _ \"example.com/m/seedlib\"\n\nfunc main() {}\n")
	write("m/seedlib/seedlib.go", "package seedlib\n\nfunc Reads() { _ = `select pid from pg_stat_activity where state = 'active'` }\n")
	want = append(want, `m/seedlib/seedlib.go:3: Reads: "select pid from pg_stat_activity where state = 'active'"`)
	write("m/lockread/lockread.go", "package lockread\n\nconst Locks = `(select * from pg_catalog.pg_locks)`\n")
	write("m/lockread/lockread_test.go", "package lockread\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { _ = `select count(*) from pg_stat_activity` }\n")
	write("m/textcheck/rule_test.go", "package textcheck\n\nvar pattern = `pg_locks|pg_stat_activity`\n")
	want = append(want, "m/gone: listed as a program only tests run, but it holds no Go file",
		`m/a/a_test.go vanished: exempt for "pg_locks", but that function holds no such literal`)

	offences, counts := lockStateNamesOutsideLockread(t, repository, []string{filepath.Join(repository, "m")}, []string{"m/seed", "m/gone"},
		"m/lockread/", "m/textcheck/rule_test.go", []lockReadExemption{
			{"m/a/a_test.go", "exemptExactly", "pg_stat_activity", "planted"},
			{"m/a/a_test.go", "exemptWithARealRead", "pg_stat_activity", "planted, beside a real read"},
			{"m/a/a_test.go", "exemptSentToTheServer", "pg_stat_activity", "planted, and sent to the server"},
			{"m/a/a_test.go", "vanished", "pg_locks", "planted, and names no literal"},
		})
	slices.Sort(want)
	if !slices.Equal(offences, want) {
		t.Errorf("the rule found\n\t%s\nwant\n\t%s", strings.Join(offences, "\n\t"), strings.Join(want, "\n\t"))
	}
	if counts.testFiles != 7 || counts.helperNames != 2 || counts.exempt != 3 {
		t.Errorf("the rule counted %+v, want 7 test files (the test, the fixture and lockread only it imports, lockread's test, the listed program and the package only it imports, the rule's own), the 2 names of lockread's files and 3 exempt literals", counts)
	}
}
