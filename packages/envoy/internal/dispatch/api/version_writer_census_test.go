package api

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// versionRowInsert matches a statement that writes an artifact_versions row, wherever in the
// statement it sits, a data-modifying common table expression's included, in either spelling a
// row reaches the table by: an insert, or a COPY sent as text.
var versionRowInsert = regexp.MustCompile(`\b(?:insert into|copy) (?:public\.)?artifact_versions\b`)

// versionRowWriter is one entry of the census: whether the writer stores the version's insert tree
// (artifact_versions.insert_tree) in the write that stores its row, and why, or why it stores none.
type versionRowWriter struct {
	fills bool
	why   string
}

// versionTreeRule is what a writer the census does not name, or one that stopped filling the
// column, is told.
const versionTreeRule = "Every write of a document version stores, in the same write, the tree its markdown was\n" +
	"rendered from when that tree holds an insert suggestion's mark (docs.InsertTree into\n" +
	"artifact_versions.insert_tree): the markdown carries no anchor mark, so a version stored without it\n" +
	"gives the words of a pending insert whose comment was deleted to every read (docs.MaskedVersionText,\n" +
	"dispatch://INFRA-1523 R3). Name insert_tree in the write, then add the function below with how it\n" +
	"fills the column - or, for a row that holds no tree at all, why it stores none."

// versionRowWriters is the census: every function of this module that writes an artifact_versions
// row, with how it fills insert_tree. A writer the scan finds and this map does not name reds the
// test; so does an entry marked fills whose write names no insert_tree, and an entry no writer
// answers, since a census that keeps a function it no longer covers stops saying anything.
//
// Every document version a server writes goes through versionInsert: queueVersionInsert's one
// insert, behind docs' writeVersionTx - a settlement (docs/settlement.go), a named version
// (Service.NamedVersion: the version route's, an accept's), a comment anchor's snapshot
// (Service.SnapshotVersion) and a rebuild (docs/rebuild.go) - and behind Ledger.QueueVersion, an
// upload's (api/artifacts.go) and an issue's first spec (api/issue_create.go). A live edit, a
// reject and a restore write the document through applyLive and reach a version through those.
var versionRowWriters = map[string]map[string]versionRowWriter{
	"cmd/dispatch-seed/write_docs.go": {
		"writeDocument": {fills: true, why: "the seed's copy of every version of a document: the latest " +
			"version's tree is the one renderVersion rendered, through docs.InsertTree; an earlier version " +
			"is generated markdown no tree was read from, which holds no span a parse reads as a mark"},
	},
	"internal/dispatch/api/artifacts.go": {
		"queueFileVersion": {why: "a file's or an image's version, which stores bytes and no markdown: " +
			"there is no tree, so no insert suggestion's words"},
	},
	"internal/dispatch/docs/mutation.go": {
		"versionInsert": {fills: true, why: "every document version a server writes, its stamp's tree " +
			"(renderStampOf, RenderStamp.InsertTree)"},
	},
	"internal/dispatch/textversions/textversionstest/textversionstest.go": {
		"insertVersion": {why: "a test's version of an issue's primary document for the keyword fill's " +
			"tests: its markdown is a test string no tree was rendered from, so it holds no insert " +
			"suggestion's words"},
	},
}

// TestEveryWriterOfADocumentVersionStoresItsInsertTree holds the write half of the version mask
// (docs.MaskedVersionText): every write of an artifact_versions row is on the census, and each that
// stores a document version names insert_tree in the write that stores its row, so no version
// holding a pending insert's words is stored without the marks the mask finds them by.
//
// Its reach is the statement as sqlStatementsByFunction assembles it (module_source_test.go) - a
// literal, or a chain of literals and package-level constants - and a pgx CopyFrom whose table is
// a pgx.Identifier literal and whose columns are a []string literal (copiedVersionColumns). A copy
// whose columns are no literal is read as naming none, so it reds until its entry says why. What
// the census does not read is whether the value written is the tree: the rows of
// comment_delete_version_insert_test.go hold that, by reading the column a write stored.
func TestEveryWriterOfADocumentVersionStoresItsInsertTree(t *testing.T) {
	sources := moduleSources(t)
	constants, err := stringConstants(sources)
	if err != nil {
		t.Fatal(err)
	}
	var unlisted, unfilled []string
	answered := map[string]map[string]bool{}
	account := func(path, function, write string, namesTree bool) {
		file, writer, listed := versionWriterEntry(path, function)
		if !listed {
			unlisted = append(unlisted, fmt.Sprintf("%s %s: %s", path, function, write))
			return
		}
		if answered[file] == nil {
			answered[file] = map[string]bool{}
		}
		answered[file][function] = true
		if writer.fills && !namesTree {
			unfilled = append(unfilled, fmt.Sprintf("%s %s: %s", path, function, write))
		}
	}
	for _, source := range sources {
		path := filepath.ToSlash(source.path)
		for _, located := range sqlStatementsByFunction(source.file, constants) {
			// A statement outside any function is a declaration, not a writer: the function that
			// runs it resolves the constant and is found here.
			if located.function == "" || !versionRowInsert.MatchString(located.statement) {
				continue
			}
			account(path, located.function, located.statement, strings.Contains(located.statement, "insert_tree"))
		}
		for _, copied := range copiedVersionColumns(source.file) {
			account(path, copied.function, "CopyFrom artifact_versions ("+strings.Join(copied.columns, ", ")+")",
				slices.Contains(copied.columns, "insert_tree"))
		}
	}
	slices.Sort(unlisted)
	if unlisted = slices.Compact(unlisted); len(unlisted) > 0 {
		t.Errorf("a writer of a document version is on no census entry (versionRowWriters):\n\t%s\n%s",
			strings.Join(unlisted, "\n\t"), versionTreeRule)
	}
	slices.Sort(unfilled)
	if unfilled = slices.Compact(unfilled); len(unfilled) > 0 {
		t.Errorf("a writer of a document version stores no insert tree in its write:\n\t%s\n%s",
			strings.Join(unfilled, "\n\t"), versionTreeRule)
	}
	var stale []string
	for file, functions := range versionRowWriters {
		for function := range functions {
			if !answered[file][function] {
				stale = append(stale, file+" "+function)
			}
		}
	}
	slices.Sort(stale)
	if len(stale) > 0 {
		t.Errorf("a census entry answers no writer of a document version, so it says nothing:\n\t%s\n"+
			"Delete it, or name the function that writes the row now.", strings.Join(stale, "\n\t"))
	}
}

// versionWriterEntry is the entry of versionRowWriters that covers function, declared in the file
// at path, with the file key that found it; false when no entry does. A file is matched by its
// suffix within the module, as the item-row census matches one (censusEntry).
func versionWriterEntry(path, function string) (string, versionRowWriter, bool) {
	for file, functions := range versionRowWriters {
		if !strings.HasSuffix(path, "/"+file) {
			continue
		}
		if writer, listed := functions[function]; listed {
			return file, writer, true
		}
	}
	return "", versionRowWriter{}, false
}

// copiedRows is one CopyFrom into artifact_versions: the top-level function it is in and the
// columns it names.
type copiedRows struct {
	function string
	columns  []string
}

// copiedVersionColumns is every CopyFrom call of file whose table, its second argument, is a
// pgx.Identifier literal ending in artifact_versions, with the columns its third argument names
// when that is a []string literal of strings, and none otherwise.
func copiedVersionColumns(file *ast.File) []copiedRows {
	var copies []copiedRows
	for _, declared := range file.Decls {
		fn, ok := declared.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(fn, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) < 3 {
				return true
			}
			if selector, ok := call.Fun.(*ast.SelectorExpr); !ok || selector.Sel.Name != "CopyFrom" {
				return true
			}
			table := stringLiterals(call.Args[1])
			if len(table) == 0 || table[len(table)-1] != "artifact_versions" {
				return true
			}
			copies = append(copies, copiedRows{function: fn.Name.Name, columns: stringLiterals(call.Args[2])})
			return true
		})
	}
	return copies
}

// stringLiterals is the strings of a composite literal every element of which is a string literal
// (pgx.Identifier{"artifact_versions"}, []string{"id", ...}), and nil for any other expression.
func stringLiterals(expression ast.Expr) []string {
	composite, ok := expression.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(composite.Elts))
	for _, element := range composite.Elts {
		literal, ok := element.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return nil
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			return nil
		}
		values = append(values, value)
	}
	return values
}
