package textcheck

import (
	"fmt"
	"go/scanner"
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

// lockWaitRoots are the Go modules whose tests read a lock wait, from this directory: Envoy's,
// Envoy's infra and the Legion daemon's. Each is walked alone and each directory holding a go.mod
// of its own is passed over, so each root must yield Go files of its own module.
var lockWaitRoots = []string{"../..", "../../infra", "../../../daemon"}

// lockWaitHelper is the one Go file that spells the lock-wait predicate, from this directory.
const lockWaitHelper = "../../lockwaittest/lockwaittest.go"

// lockWaitTerm is a read of pg_stat_activity's wait_event_type or wait_event, as a column, a
// qualified column (a.wait_event_type), a coalesce of either, or any of those cast (::text).
const lockWaitTerm = `(?:coalesce\s*\(\s*(?:\w+\.)?wait_event(?:_type)?\s*,[^)]*\)|(?:\w+\.)?wait_event(?:_type)?)(?:\s*::\s*\w+)?`

// lockWaitValue is Lock, the wait_event_type of a lock wait, or one of the wait_event values a lock
// wait reports, which are pg_locks' locktype values, as a SQL string.
const lockWaitValue = `'(?:lock|relation|extend|frozenid|page|tuple|transactionid|virtualxid|spectoken|object|userlock|advisory|applytransaction)'`

// lockWaitOperator is how SQL compares two values for this check: =, <> or !=, or like, ilike and
// their negations.
const lockWaitOperator = `(?:\s*(?:=|<>|!=)\s*|\s+(?:not\s+)?i?like\s+)`

var (
	// lockWaitEvent is a comparison of a lockWaitTerm with a lockWaitValue in either order, or a
	// lockWaitTerm tested against a list (in, not in) that holds a lockWaitValue, in either case
	// and across lines.
	lockWaitEvent = regexp.MustCompile(`(?i)` + lockWaitTerm + lockWaitOperator + lockWaitValue +
		`|` + lockWaitValue + lockWaitOperator + lockWaitTerm +
		`|` + lockWaitTerm + `\s+(?:not\s+)?in\s*\([^)]*` + lockWaitValue)
	// lockWaitPredicate is lockwaittest's predicate written out: the backends pg_locks holds an
	// ungranted request of, with or without a lock type.
	lockWaitPredicate = regexp.MustCompile(`(?i)select\s+pid\s+from\s+pg_locks\s+where\s+not\s+granted`)
)

// No Go file of the three modules reads whether a backend waits on a lock from pg_stat_activity's
// wait event, or writes out lockwaittest's predicate: each reads it through lockwaittest.Waiting
// or lockwaittest.WaitingOn. lockwaittest's package comment says why the wait event is the wrong
// read, and docs/testing.md, "A test reads a lock wait from pg_locks", gives what was measured. A
// root that holds no go.mod, or in which the walk reads no Go file, fails the check, which would
// otherwise pass having read nothing, or only part of a module.
func TestEveryTestReadsALockWaitThroughLockwaittest(t *testing.T) {
	offences, err := lockWaitReads(lockWaitRoots, lockWaitHelper)
	if err != nil {
		t.Fatal(err)
	}
	if len(offences) > 0 {
		t.Fatalf("%d Go string literals read a lock wait other than through lockwaittest:\n\t%s\n"+
			"read it with lockwaittest.Waiting or lockwaittest.WaitingOn (packages/envoy/lockwaittest)",
			len(offences), strings.Join(offences, "\n\t"))
	}
}

// The check names a wait event compared with a lock as SQL writes it plainly, reversed, in a list,
// qualified, in a coalesce and cast, and by like, and the predicate written out, each at the line
// it is on, in a raw literal across lines too. It leaves alone a wait event read for a diagnosis
// or compared with another wait, a comment, the helper file, and a module below the root. A root
// that holds no go.mod, and one in which it reads no Go file, is an error. The planted source
// splits each form across two literals, so this file holds none of them whole.
func TestTheLockWaitCheckSeesWhatItGuards(t *testing.T) {
	root := t.TempDir()
	plant := func(path string, lines ...string) string {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return filepath.ToSlash(full)
	}
	const event, typed = "wait_event", "wait_event" + "_type"
	planted := plant("planted.go",
		"package planted",
		"",
		"// A comment that reads "+typed+" = 'Lock' is prose.",
		"var plain = \""+typed+" = 'Lock'\"",
		"var reversed = \"'Lock' = "+typed+"\"",
		"var listed = \""+typed+" in ('LWLock', 'Lock')\"",
		"var qualified = \"a."+event+" <> 'advisory'\"",
		"var cast = \"coalesce("+typed+", '')::text = 'Lock'\"",
		"var liked = \""+typed+" not like 'Lock'\"",
		"var spelled = `select count(*) from pg_stat_activity",
		"\twhere "+"pid in (select pid from pg_"+"locks where not granted)`",
		"var typedSpelled = \"select pid from pg_"+"locks where not granted and locktype = 'advisory'\"",
		"var diagnosis = \"select "+typed+", "+event+" from pg_stat_activity\"",
		"var other = \""+typed+" = 'IO' and "+event+" = 'WalSync'\"",
		"",
	)
	helper := plant("lockwaittest/lockwaittest.go", "package lockwaittest", "",
		"const Waiting = \"pid in (select pid from pg_"+"locks where not granted)\"")
	plant("go.mod", "module planted")
	plant("nested/go.mod", "module nested")
	plant("nested/nested.go", "package nested", "", "var plain = \""+typed+" = 'Lock'\"")

	got, err := lockWaitReads([]string{root}, helper)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		planted + ":11: the predicate lockwaittest.Waiting holds, written out",
		planted + ":12: the predicate lockwaittest.Waiting holds, written out",
		planted + ":4: a wait event compared with a lock",
		planted + ":5: a wait event compared with a lock",
		planted + ":6: a wait event compared with a lock",
		planted + ":7: a wait event compared with a lock",
		planted + ":8: a wait event compared with a lock",
		planted + ":9: a wait event compared with a lock",
	}
	if !slices.Equal(got, want) {
		t.Errorf("the check found\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}

	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "go.mod"), []byte("module empty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lockWaitReads([]string{root, empty}, helper); err == nil || !strings.Contains(err.Error(), "read no Go file under "+empty) {
		t.Errorf("a module root holding no Go file answered %v, want an error naming it", err)
	}
	inside := filepath.Join(root, "lockwaittest")
	if _, err := lockWaitReads([]string{inside}, helper); err == nil || !strings.Contains(err.Error(), "read no module at "+inside) {
		t.Errorf("a root that holds Go files and no go.mod answered %v, want an error naming it", err)
	}
}

// lockWaitReads names, as path:line: what, sorted, each Go string literal under roots that compares
// a wait event with a lock (lockWaitEvent) or writes out lockwaittest's predicate
// (lockWaitPredicate), the file helper passed over for the second. Each root must hold a go.mod,
// as a module's root does, and is walked alone, its node_modules, vendor, testdata and dot
// directories and each directory below it holding a go.mod passed over, and a root that yields
// no Go file is an error. Comments are not literals, so prose about a read is not read.
func lockWaitReads(roots []string, helper string) ([]string, error) {
	skip, err := filepath.Abs(helper)
	if err != nil {
		return nil, err
	}
	var offences []string
	for _, root := range roots {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
			return nil, fmt.Errorf("read no module at %s: it holds no go.mod, so it is not the root of a module whose Go files the check reads: %w", root, err)
		}
		read := 0
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if path == root {
					return nil
				}
				if name := entry.Name(); name == "node_modules" || name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			source, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			read++
			absolute, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			offences = append(offences, lockWaitReadsIn(path, source, absolute == skip)...)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read the Go files under %s: %w", root, err)
		}
		if read == 0 {
			return nil, fmt.Errorf("read no Go file under %s: the check would pass having seen nothing of that module", root)
		}
	}
	slices.Sort(offences)
	return offences, nil
}

// lockWaitReadsIn is lockWaitReads for the string literals of one file's source. A match in a raw
// literal is named at its own line; one in an interpreted literal, which holds no line break, at
// the literal's.
func lockWaitReadsIn(path string, source []byte, helper bool) []string {
	files := token.NewFileSet()
	file := files.AddFile(path, files.Base(), len(source))
	var lex scanner.Scanner
	lex.Init(file, source, nil, 0)
	var offences []string
	for {
		position, kind, literal := lex.Scan()
		if kind == token.EOF {
			return offences
		}
		if kind != token.STRING {
			continue
		}
		text, err := strconv.Unquote(literal)
		if err != nil {
			text = literal
		}
		line := files.Position(position).Line
		name := func(at int, what string) {
			if strings.HasPrefix(literal, "`") {
				at = line + strings.Count(text[:at], "\n")
			} else {
				at = line
			}
			offences = append(offences, filepath.ToSlash(path)+":"+strconv.Itoa(at)+": "+what)
		}
		for _, match := range lockWaitEvent.FindAllStringIndex(text, -1) {
			name(match[0], "a wait event compared with a lock")
		}
		if !helper {
			for _, match := range lockWaitPredicate.FindAllStringIndex(text, -1) {
				name(match[0], "the predicate lockwaittest.Waiting holds, written out")
			}
		}
	}
}
