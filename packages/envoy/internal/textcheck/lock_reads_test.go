package textcheck

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// lockReadRoots are the Go modules whose tests run against a Postgres server other tests share,
// from this directory: the envoy module, with packages/envoy/infra beneath it, and the Legion
// daemon's.
var lockReadRoots = []string{"../..", "../../../daemon"}

// lockReadRepository is the repository's root, from this directory: an offence's path and an
// exemption's key are relative to it.
const lockReadRepository = "../../../.."

// lockViewRead is a statement that reads a view of every database's locks or backends on the
// server: pg_locks or pg_stat_activity, or the function behind either.
var lockViewRead = regexp.MustCompile(`\b(from|join)\s+(pg_catalog\.)?(pg_locks|pg_stat_activity)\b|\b(pg_stat_get_activity|pg_lock_status)\s*\(`)

// lockReadScope is what keeps such a read to its test's own database: the database itself, by
// name or oid, one backend by its process id (the reading one's, a parameter, or a list of them),
// what blocks a backend, or an application name a test gives its own connections.
var lockReadScope = regexp.MustCompile(`current_database\(\)|\bdatname\s*=|\bdatabase\s*=|\bdatid\s*=|\bpid\s*=\s*(\$[0-9]+|any\s*\(|pg_backend_pid\(\))|pg_blocking_pids\s*\(|\bapplication_name\s*=`)

// lockReadExemptions are the reads of test code that name no database or backend of their own and
// still cannot see another test's lock or backend, keyed "<path> <function>", each with why. A
// key that names no such read is itself an offence, so the list cannot outlive its reads.
var lockReadExemptions = map[string]string{
	"packages/envoy/cmd/dispatch/connection_ceiling_test.go connectionRefusalCause": "a refused connection's diagnosis, not a " +
		"lock read: it counts every client backend of the server beside the server's max_connections, the limit Postgres " +
		"refused the connection on, so it counts the whole server's on purpose",
	"packages/envoy/internal/broker/store/migrate_guard_test.go TestMigrateReleasesTheVersionTablesAlterLockBeforeItsMigrations": "the " +
		"test's own migration counts the locks on broker_schema_migrations, a table the test creates in an empty database of " +
		"its own (emptySchemaStore): a table created on the server takes an oid from the server's one counter, which no " +
		"other database's table holds",
	"packages/envoy/internal/dispatch/store/pool_test.go TestHorizonIsServedWhileTheSharedPoolIsFullyAcquired": "a read the " +
		"horizon pool must answer while the shared pool is fully acquired; the test asserts that it answers, and nothing of " +
		"the count",
	"packages/envoy/internal/pgmigrate/set_test.go TestLoadRefusesACensusWithNoMigrationOneThatIsNotASelectAndOneThatCallsAForbiddenFunction": "census " +
		"files the loader must refuse for the function each calls (pg_terminate_backend, pg_cancel_backend); no server runs " +
		"them",
}

// lockReadTestPrograms are the programs only a test suite runs, by directory from the
// repository's root: each counts as test code, so what it imports does too, as a test file's
// imports do. The request seeder is one: Dispatch's browser suite runs it against its broker
// (packages/dispatch/e2e/real-broker.ts), and it imports brokertest and, through it, the broker's
// storetest, which a program that ships would make code that ships. A directory listed here that
// holds no Go file is an offence, so the list cannot outlive its programs.
var lockReadTestPrograms = []string{"packages/envoy/internal/broker/brokertest/requestseed"}

// The tests of these modules share one Postgres server: storetest.Open clones a database per test
// from one template, the packages of a run test beside each other, and runs beside each other on
// one server. pg_locks and pg_stat_activity list every database's locks and backends, and every
// clone holds the same tables under the same oids and takes the same advisory keys, so a read of
// either that keeps to no database or backend counts another test's waiter, or names another
// test's holder, as its own: the API's upload lock probe named another test's spec settlement as
// its issue's holder and failed. So every such read in test code names its database or a backend
// of its own, or reads through storetest (Waiting, Holders and WaitersOn keep to the database they
// run in), or is named in lockReadExemptions with why it cannot see another test's.
//
// Test code is every _test.go file, every program lockReadTestPrograms names, and every file of a
// package only test code imports: the fixtures, storetest among them. The check reads each
// statement as a string literal, or a `+` chain of literals and its package's string constants; a
// part it cannot read is a gap, and a statement whose read it cannot show to keep to its database,
// gaps and all, is refused rather than guessed at. That refuses a read whose condition comes in as
// a parameter even when every caller passes a scoped one: the check reads a statement, not the
// calls that reach it, and a caller added later can pass any condition. Such a read names its
// scope in the statement, as a constant of its package (`... where ` + ownBackends), or reads
// through storetest.
func TestEveryLockReadOfATestKeepsToItsOwnDatabase(t *testing.T) {
	offences, counts := unscopedLockReads(t, lockReadRepository, lockReadRoots, lockReadTestPrograms, lockReadExemptions)
	if counts.testFiles == 0 || counts.reads == 0 {
		t.Fatalf("UNKNOWN: the check read %d Go files, %d of them test code, and found %d lock reads under %v: it cannot see what it guards", counts.files, counts.testFiles, counts.reads, lockReadRoots)
	}
	t.Logf("read %d Go files, %d of them test code; %d statements of test code read pg_locks or pg_stat_activity: %d keep to their database or a backend, %d are exempt", counts.files, counts.testFiles, counts.reads, counts.scoped, counts.exempt)
	if len(offences) > 0 {
		t.Fatalf("%d reads of pg_locks or pg_stat_activity in test code name no database or backend of their own:\n\t%s\n"+
			"read the lock through storetest (Waiting, Holders, WaitersOn), or keep the read to its database "+
			"(datname = current_database(), or pg_locks' database against its oid) or to one backend by its pid; "+
			"a read that cannot see another test's database however it is written goes in lockReadExemptions with why",
			len(offences), strings.Join(offences, "\n\t"))
	}
}

// The check refuses an unscoped read in a test file, in a fixture only tests import, in a package
// only a listed test program imports, one assembled from a part it cannot read, and one whose scope
// comes in as a parameter, though its one caller passes a scoped condition; it passes a read that
// names its database, a backend or what blocks one, a read whose scope comes from a constant, a
// literal that names a view in a sentence, a read in a package that production code imports, and
// an exempt read; and it refuses an exemption that names no read and a listed program that is not
// there. The planted reads are built from words this file holds apart, so it holds no read of its
// own.
func TestTheLockReadCheckSeesWhatItGuards(t *testing.T) {
	repository := t.TempDir()
	views := strings.NewReplacer("LOCKS", "pg_"+"locks", "ACTIVITY", "pg_"+"stat_activity", "GETACT", "pg_"+"stat_get_activity")
	write := func(path, source string) {
		t.Helper()
		full := filepath.Join(repository, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(views.Replace(source)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("m/go.mod", "module example.com/m\n")
	write("m/a/a_test.go", strings.Join([]string{
		"package a",
		"",
		`import _ "example.com/m/fixturetest"`,
		"",
		"const here = `datname = current_database()`",
		"",
		"func unscoped() { _ = `select count(*) from LOCKS where relation = 'x'::regclass and not granted` }",
		"func activity() { _ = `select count(*) from ACTIVITY where wait_event_type = 'Lock'` }",
		"func listing() { _ = `select pid from GETACT(null)` }",
		"func assembled(more string) { _ = `select count(*) from LOCKS l where ` + more }",
		"func scopeFromParameter(where string) { _ = `select count(*) from ACTIVITY where ` + where }",
		"func callsIt() { scopeFromParameter(here) }",
		"func scoped() { _ = `select count(*) from LOCKS l where l.database = (select oid from pg_database where datname = current_database())` }",
		"func byPID() { _ = `select mode from LOCKS where pid = $1` }",
		"func blocked() { _ = `select count(*) from ACTIVITY where cardinality(pg_blocking_pids(pid)) > 0` }",
		"func fromConstant() { _ = `select count(*) from ACTIVITY where ` + here }",
		`func sentence() { _ = "read LOCKS: %v" }`,
		"func exempt() { _ = `select count(*) from ACTIVITY` }",
		"",
	}, "\n"))
	write("m/fixturetest/fixture.go", "package fixturetest\n\nfunc Waiters() { _ = `select count(*) from LOCKS where not granted` }\n")
	write("m/prod/prod.go", "package prod\n\nfunc Count() { _ = `select count(*) from ACTIVITY` }\n")
	write("m/main.go", "package main\n\nimport _ \"example.com/m/prod\"\n\nfunc main() {}\n")
	write("m/seed/main.go", "package main\n\nimport _ \"example.com/m/seedlib\"\n\nfunc main() {}\n")
	write("m/seedlib/seedlib.go", "package seedlib\n\nfunc Reads() { _ = `select pid from ACTIVITY where state = 'active'` }\n")

	offences, counts := unscopedLockReads(t, repository, []string{filepath.Join(repository, "m")}, []string{"m/seed", "m/gone"}, map[string]string{
		"m/a/a_test.go exempt":   "planted",
		"m/a/a_test.go vanished": "planted, and names no read",
	})
	want := []string{
		"m/a/a_test.go vanished: exempt, but no read of test code there names no database or backend",
		"m/a/a_test.go:10: assembled: select count(*) from " + views.Replace("LOCKS") + " l where ‹?›",
		"m/a/a_test.go:11: scopeFromParameter: select count(*) from " + views.Replace("ACTIVITY") + " where ‹?›",
		"m/a/a_test.go:7: unscoped: select count(*) from " + views.Replace("LOCKS") + " where relation = 'x'::regclass and not granted",
		"m/a/a_test.go:8: activity: select count(*) from " + views.Replace("ACTIVITY") + " where wait_event_type = 'lock'",
		"m/a/a_test.go:9: listing: select pid from " + views.Replace("GETACT") + "(null)",
		"m/fixturetest/fixture.go:3: Waiters: select count(*) from " + views.Replace("LOCKS") + " where not granted",
		"m/gone: listed as a program only tests run, but it holds no Go file",
		"m/seedlib/seedlib.go:3: Reads: select pid from " + views.Replace("ACTIVITY") + " where state = 'active'",
	}
	if !slices.Equal(offences, want) {
		t.Errorf("the check found\n\t%s\nwant\n\t%s", strings.Join(offences, "\n\t"), strings.Join(want, "\n\t"))
	}
	if counts.testFiles != 4 || counts.reads != 12 || counts.scoped != 4 || counts.exempt != 1 {
		t.Errorf("the check counted %+v, want 4 test files (the test, the fixture only it imports, the listed program and the package only it imports) and 12 reads, 4 kept to a database or backend and 1 exempt", counts)
	}
}

// lockReadCounts is what unscopedLockReads read: Go files, those of them that are test code, the
// statements of test code that read pg_locks or pg_stat_activity, and of those the ones that keep
// to a database or backend and the ones exempt.
type lockReadCounts struct {
	files, testFiles, reads, scoped, exempt int
}

// goSource is one Go file the walk found: its package's directory and name, whether it is a test
// file, and the import paths it names.
type goSource struct {
	path, dir, pkg string
	test           bool
	imports        []string
}

// unscopedLockReads walks the Go modules under roots and names every statement of test code that
// reads pg_locks or pg_stat_activity and keeps to no database or backend (lockReadScope), as
// "<path>:<line>: <function>: <statement>", the statement lowercased, its whitespace collapsed and
// a part it cannot read written ‹?›, but those exempt names by "<path> <function>"; each key of
// exempt that names no such read; and each of programs, the directories of the programs only
// tests run, that holds no Go file. Paths are relative to repository. node_modules, vendor,
// testdata and hidden directories are passed over, as go passes over the last two.
func unscopedLockReads(t *testing.T, repository string, roots, programs []string, exempt map[string]string) ([]string, lockReadCounts) {
	t.Helper()
	repository, err := filepath.Abs(repository)
	if err != nil {
		t.Fatal(err)
	}
	var counts lockReadCounts
	modules := map[string]string{} // a module's directory -> its path
	var sources []goSource
	files := token.NewFileSet()
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if name := entry.Name(); path != root && (name == "node_modules" || name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			switch {
			case entry.Name() == "go.mod":
				text, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				for line := range strings.Lines(string(text)) {
					if module, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
						modules[filepath.Dir(path)] = strings.Trim(strings.TrimSpace(module), `"`)
					}
				}
			case strings.HasSuffix(path, ".go"):
				file, err := parser.ParseFile(files, path, nil, parser.ImportsOnly)
				if err != nil {
					return err
				}
				source := goSource{path: path, dir: filepath.Dir(path), pkg: file.Name.Name, test: strings.HasSuffix(path, "_test.go")}
				for _, spec := range file.Imports {
					imported, err := strconv.Unquote(spec.Path.Value)
					if err != nil {
						return err
					}
					source.imports = append(source.imports, imported)
				}
				sources = append(sources, source)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("read the Go files under %s: %v", root, err)
		}
	}
	counts.files = len(sources)

	// A file of a program only tests run counts as a test file.
	listed := map[string]bool{}
	for _, program := range programs {
		listed[program] = false
	}
	for i, source := range sources {
		absolute, err := filepath.Abs(source.dir)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(repository, absolute)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := listed[filepath.ToSlash(rel)]; ok {
			listed[filepath.ToSlash(rel)] = true
			sources[i].test = true
		}
	}

	// Each directory's import path, from the module whose directory is the nearest above it.
	importPath := map[string]string{}
	for _, source := range sources {
		if _, done := importPath[source.dir]; done {
			continue
		}
		for dir := source.dir; ; dir = filepath.Dir(dir) {
			if module, ok := modules[dir]; ok {
				rel, err := filepath.Rel(dir, source.dir)
				if err != nil {
					t.Fatal(err)
				}
				importPath[source.dir] = strings.TrimSuffix(module+"/"+filepath.ToSlash(rel), "/.")
				break
			}
			if parent := filepath.Dir(dir); parent == dir {
				importPath[source.dir] = ""
				break
			}
		}
	}
	// A package only test code imports: it has importers, and each is a test file or a file of
	// another such package. Every imported package starts as one, and each loses it once a file
	// of a package that is not one imports it, until no package loses it.
	directory := map[string]string{}
	for dir, path := range importPath {
		directory[path] = dir
	}
	importers := map[string][]goSource{}
	for _, source := range sources {
		for _, imported := range source.imports {
			if dir, ok := directory[imported]; ok && dir != source.dir {
				importers[dir] = append(importers[dir], source)
			}
		}
	}
	testOnly := map[string]bool{}
	for dir := range importers {
		testOnly[dir] = true
	}
	for changed := true; changed; {
		changed = false
		for dir := range testOnly {
			for _, importer := range importers[dir] {
				if !importer.test && !testOnly[importer.dir] {
					delete(testOnly, dir)
					changed = true
					break
				}
			}
		}
	}

	used := map[string]bool{}
	var offences []string
	constants := map[string]map[string]string{}
	for _, source := range sources {
		if !source.test && !testOnly[source.dir] {
			continue
		}
		counts.testFiles++
		text, err := os.ReadFile(source.path)
		if err != nil {
			t.Fatal(err)
		}
		lower := bytes.ToLower(text)
		if !bytes.Contains(lower, []byte("pg_locks")) && !bytes.Contains(lower, []byte("pg_stat_activity")) &&
			!bytes.Contains(lower, []byte("pg_stat_get_activity")) && !bytes.Contains(lower, []byte("pg_lock_status")) {
			continue
		}
		key := source.dir + " " + source.pkg
		if constants[key] == nil {
			constants[key] = packageStringConstants(t, files, sources, source)
		}
		file, err := parser.ParseFile(files, source.path, text, 0)
		if err != nil {
			t.Fatal(err)
		}
		absolute, err := filepath.Abs(source.path)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(repository, absolute)
		if err != nil {
			t.Fatal(err)
		}
		rel = filepath.ToSlash(rel)
		for _, read := range lockReadsOf(files, file, constants[key]) {
			counts.reads++
			switch {
			case lockReadScope.MatchString(read.text):
				counts.scoped++
			case exempt[rel+" "+read.function] != "":
				counts.exempt++
				used[rel+" "+read.function] = true
			default:
				offences = append(offences, fmt.Sprintf("%s:%d: %s: %s", rel, read.line, read.function, read.text))
			}
		}
	}
	for program, found := range listed {
		if !found {
			offences = append(offences, program+": listed as a program only tests run, but it holds no Go file")
		}
	}
	for key := range exempt {
		if !used[key] {
			offences = append(offences, key+": exempt, but no read of test code there names no database or backend")
		}
	}
	slices.Sort(offences)
	return offences, counts
}

// packageStringConstants is every package-level string constant of source's package, by name, as
// a statement is assembled from them: a literal, or a `+` chain of literals and the package's
// other constants, resolved until no pass resolves one more. A name two of the package's files
// declare with different values, or one the chain cannot read whole, is left out.
func packageStringConstants(t *testing.T, files *token.FileSet, sources []goSource, of goSource) map[string]string {
	t.Helper()
	values := map[string]ast.Expr{}
	ambiguous := map[string]bool{}
	for _, source := range sources {
		if source.dir != of.dir || source.pkg != of.pkg {
			continue
		}
		file, err := parser.ParseFile(files, source.path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			general, ok := decl.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, spec := range general.Specs {
				valueSpec := spec.(*ast.ValueSpec)
				if len(valueSpec.Values) != len(valueSpec.Names) {
					continue
				}
				for i, name := range valueSpec.Names {
					if _, seen := values[name.Name]; seen {
						ambiguous[name.Name] = true
					}
					values[name.Name] = valueSpec.Values[i]
				}
			}
		}
	}
	resolved := map[string]string{}
	for changed := true; changed; {
		changed = false
		for name, value := range values {
			if _, done := resolved[name]; done || ambiguous[name] {
				continue
			}
			if text, whole, ok := statementText(value, resolved); ok && whole {
				resolved[name] = text
				changed = true
			}
		}
	}
	return resolved
}

// lockRead is one statement lockReadsOf found: its line, the function or package-level value that
// holds it, and its text.
type lockRead struct {
	line     int
	function string
	text     string
}

// lockReadsOf is every statement of file that reads pg_locks or pg_stat_activity (lockViewRead):
// each string literal, or `+` chain of them and constants, that is not part of a longer chain,
// lowercased, its whitespace collapsed, a part it cannot read written ‹?›.
func lockReadsOf(files *token.FileSet, file *ast.File, constants map[string]string) []lockRead {
	var reads []lockRead
	for _, decl := range file.Decls {
		holder := ""
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			holder = decl.Name.Name
			if decl.Recv != nil && len(decl.Recv.List) == 1 {
				receiver := decl.Recv.List[0].Type
				if star, ok := receiver.(*ast.StarExpr); ok {
					receiver = star.X
				}
				if index, ok := receiver.(*ast.IndexExpr); ok {
					receiver = index.X
				}
				if named, ok := receiver.(*ast.Ident); ok {
					holder = named.Name + "." + holder
				}
			}
		case *ast.GenDecl:
			var names []string
			for _, spec := range decl.Specs {
				if value, ok := spec.(*ast.ValueSpec); ok {
					for _, name := range value.Names {
						names = append(names, name.Name)
					}
				}
			}
			holder = strings.Join(names, ",")
		}
		ast.Inspect(decl, func(node ast.Node) bool {
			expression, ok := node.(ast.Expr)
			if !ok {
				return true
			}
			// A constant on its own is read where it is declared, and inside each chain it is
			// part of; a use of it alone is neither.
			if _, alone := expression.(*ast.Ident); alone {
				return true
			}
			text, _, ok := statementText(expression, constants)
			if !ok {
				return true
			}
			text = strings.ToLower(strings.Join(strings.Fields(text), " "))
			if lockViewRead.MatchString(text) {
				reads = append(reads, lockRead{line: files.Position(expression.Pos()).Line, function: holder, text: text})
			}
			return false
		})
	}
	return reads
}

// statementText renders expression as the text of a statement: a string literal, or a `+` chain
// of literals and constants, a part it cannot read written ‹?›. ok is whether it holds a string
// at all, and whole whether it holds no gap.
func statementText(expression ast.Expr, constants map[string]string) (text string, whole, ok bool) {
	switch expression := expression.(type) {
	case *ast.BasicLit:
		if expression.Kind != token.STRING {
			return "", false, false
		}
		value, err := strconv.Unquote(expression.Value)
		if err != nil {
			return "", false, false
		}
		return value, true, true
	case *ast.ParenExpr:
		return statementText(expression.X, constants)
	case *ast.Ident:
		value, found := constants[expression.Name]
		return value, found, found
	case *ast.BinaryExpr:
		if expression.Op != token.ADD {
			return "", false, false
		}
		left, leftWhole, leftOK := statementText(expression.X, constants)
		right, rightWhole, rightOK := statementText(expression.Y, constants)
		if !leftOK && !rightOK {
			return "", false, false
		}
		if !leftOK {
			left = "‹?›"
		}
		if !rightOK {
			right = "‹?›"
		}
		return left + right, leftWhole && rightWhole, true
	}
	return "", false, false
}
