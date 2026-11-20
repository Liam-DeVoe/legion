package delivery

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// resultsHeadsRead is one entry of resultsHeadsReads: one read of pull_request_heads on the
// results path, which plan D30 makes one statement for both of its callers. A writer's results
// read runs it over the stored rows alone, and a read-only caller's over the stored rows together
// with the answers the read-only memory keeps (CommitPullAnswers).
type resultsHeadsRead struct {
	// read is the read as plan D30 names it: the name of its statement where the statement has
	// one (headsAttributedQuery), and else the name of the writer's function (HeadStoredAt).
	read string
	// writer is the function a writer's results read calls for the read.
	writer string
	// statement is the name of the package-level constant or variable that holds the read's one
	// statement text: the text after its WITH list, which writer and every entry point of
	// readOnly run. Each of them must reach it through the package's own functions and values.
	// It is "" only while readOnly is empty.
	statement string
	// readOnly is the read's read-only entry points: each function or method of the package that
	// takes the memory's answers (memoryAnswerTypes) and runs statement over them for a read-only
	// caller. A function is named by its name, and a method as Type.Method.
	readOnly []string
}

// resultsHeadsReads is the registry of the reads of pull_request_heads on the results path: the
// five reads of plan D30. An entry is one read, with the writer's function, the read's one
// statement and its read-only entry points. A function that takes the memory's answers to read
// these rows for a read-only caller is named here, among the read-only entry points of the read
// whose statement it runs, and nowhere else. TestEachHeadsReadOnTheResultsPathIsOneStatement
// fails on every function of the package that takes the memory's answers, or whose name ends in
// With, and that neither this registry nor keptBranchReads names: such a function is a second
// copy of a writer's read, which no test holds to the writer's statement.
var resultsHeadsReads = []resultsHeadsRead{
	{read: "headsAttributedQuery", writer: "HeadsAttributedTo"},
	{read: "HeadStoredAt", writer: "HeadStoredAt"},
	{read: "BaseBranchMergedFor", writer: "BaseBranchMergedFor"},
	{read: "baseBranchMergedInQuery", writer: "BaseBranchMergedIn"},
	{read: "NewestPullRequestHead", writer: "NewestPullRequestHead"},
}

// keptBranchReads is the functions and methods that may take the memory's answers and read no
// pull_request_heads row: the branch reads plan D5 keeps, which read a merge the memory learned as
// the yes a writer's store of it would have written (Branches.ReadCommitOnBranch). Each is named
// as resultsHeadsRead.readOnly names an entry point.
var keptBranchReads = []string{
	"Branches.ReadCommitOnBranch",
	"Branches.readCommitOnBranch",
	"mergeLearnedOnBranch",
	"anyLearnedHead",
}

// memoryAnswerTypes is each type a parameter carries the memory's answers in, written as Go
// writes the type: the answers CommitPullAnswers.Learned gives, and each type a read-only entry
// point takes them in instead. A pointer is read as the type it points to, and a variadic
// parameter as the slice it is (answerType).
var memoryAnswerTypes = []string{"[]CommitPulls"}

// TestEachHeadsReadOnTheResultsPathIsOneStatement is plan D30's inventory of the reads of
// pull_request_heads on the results path. It parses the package's non-test files, as go build
// selects them, and fails on three things, naming each:
//
//   - a function or method that takes the memory's answers (memoryAnswerTypes), or whose name
//     ends in With, and that the registry does not name (resultsHeadsReads, keptBranchReads);
//   - a name in the registry that the package does not declare, so the registry keeps no place
//     for a copy that has gone;
//   - a read with a read-only entry point whose statement is not a constant or variable of the
//     package, or is not reached from the read's writer and from each of its entry points.
func TestEachHeadsReadOnTheResultsPathIsOneStatement(t *testing.T) {
	source := parsePackageSource(t)

	registered := map[string]bool{}
	for _, name := range keptBranchReads {
		registered[name] = true
	}
	for _, read := range resultsHeadsReads {
		for _, name := range read.readOnly {
			registered[name] = true
		}
	}
	for _, function := range source.functions {
		if registered[function.key] {
			continue
		}
		var why []string
		if takes := function.memoryAnswers(); takes != "" {
			why = append(why, "takes the memory's answers as "+takes)
		}
		if strings.HasSuffix(function.decl.Name.Name, "With") {
			why = append(why, "has a name that ends in With")
		}
		if len(why) == 0 {
			continue
		}
		t.Errorf("%s: %s %s, and the registry names it nowhere (resultsHeadsReads, keptBranchReads): "+
			"it is a second copy of a writer's read, which no test holds to the writer's statement. "+
			"Run the read's one statement instead, and name the function among that read's read-only entry points",
			source.positions.Position(function.decl.Pos()), function.key, strings.Join(why, " and "))
	}

	for _, name := range keptBranchReads {
		if !source.declaresFunction(name) {
			t.Errorf("keptBranchReads names %s, which is no function or method of the package", name)
		}
	}
	for _, read := range resultsHeadsReads {
		if !source.declaresFunction(read.read) && !source.values[read.read] {
			t.Errorf("resultsHeadsReads names the read %s, which the package does not declare", read.read)
		}
		if !source.declaresFunction(read.writer) {
			t.Errorf("the read %s names its writer %s, which is no function of the package", read.read, read.writer)
		}
		for _, name := range read.readOnly {
			if !source.declaresFunction(name) {
				t.Errorf("the read %s names the read-only entry point %s, which is no function or method of the package", read.read, name)
			}
		}
		if len(read.readOnly) == 0 {
			continue
		}
		if !source.values[read.statement] {
			t.Errorf("the read %s has read-only entry points and names its statement %q, which is no constant or variable of the package",
				read.read, read.statement)
			continue
		}
		for _, caller := range append([]string{read.writer}, read.readOnly...) {
			if source.declaresFunction(caller) && !source.reaches(caller, read.statement) {
				t.Errorf("%s does not run %s, the one statement of the read %s", caller, read.statement, read.read)
			}
		}
	}
}

// packageSource is the package's non-test files as TestEachHeadsReadOnTheResultsPathIsOneStatement
// reads them: every function and method in source order, every package-level constant and
// variable, and the names each of them refers to.
type packageSource struct {
	positions *token.FileSet
	functions []packageFunction
	// values is every package-level constant and variable, by name.
	values map[string]bool
	// refers is the names each function, method, constant and variable refers to, by its key.
	refers map[string]map[string]bool
	// declared is the keys a name may refer to: a function, a constant or a variable of that
	// name, and every method of that name.
	declared map[string][]string
}

// packageFunction is one function or method of the package, with its key: its name, or
// Type.Method for a method.
type packageFunction struct {
	key  string
	decl *ast.FuncDecl
}

// parsePackageSource parses the non-test files go build selects for the package in the test's
// own directory.
func parsePackageSource(t *testing.T) *packageSource {
	t.Helper()
	listed, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("list the package's files: %v", err)
	}
	source := &packageSource{
		positions: token.NewFileSet(),
		values:    map[string]bool{},
		refers:    map[string]map[string]bool{},
		declared:  map[string][]string{},
	}
	for _, name := range listed.GoFiles {
		file, err := parser.ParseFile(source.positions, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				key := decl.Name.Name
				if decl.Recv != nil && len(decl.Recv.List) > 0 {
					key = receiverName(decl.Recv.List[0].Type) + "." + decl.Name.Name
				}
				source.functions = append(source.functions, packageFunction{key: key, decl: decl})
				source.declare(decl.Name.Name, key, decl.Body)
			case *ast.GenDecl:
				if decl.Tok != token.CONST && decl.Tok != token.VAR {
					continue
				}
				for _, spec := range decl.Specs {
					value := spec.(*ast.ValueSpec)
					for _, name := range value.Names {
						if name.Name == "_" {
							continue
						}
						source.values[name.Name] = true
						source.declare(name.Name, name.Name, value)
					}
				}
			}
		}
	}
	return source
}

// declare records key, which name refers to, and the names node refers to as key's.
func (s *packageSource) declare(name, key string, node ast.Node) {
	s.declared[name] = append(s.declared[name], key)
	refers := map[string]bool{}
	if node != nil {
		ast.Inspect(node, func(node ast.Node) bool {
			if ident, isIdent := node.(*ast.Ident); isIdent {
				refers[ident.Name] = true
			}
			return true
		})
	}
	s.refers[key] = refers
}

// declaresFunction is whether key is a function or method of the package.
func (s *packageSource) declaresFunction(key string) bool {
	for _, function := range s.functions {
		if function.key == key {
			return true
		}
	}
	return false
}

// reaches is whether from refers to target, directly or through the package's functions,
// methods, constants and variables. A name is read as every declaration of the package it could
// refer to, so a method call reaches every method of that name: the test asks whether the
// statement is run at all, not by which path.
func (s *packageSource) reaches(from, target string) bool {
	seen := map[string]bool{from: true}
	next := []string{from}
	for len(next) > 0 {
		key := next[0]
		next = next[1:]
		for name := range s.refers[key] {
			for _, referred := range s.declared[name] {
				if referred == target {
					return true
				}
				if !seen[referred] {
					seen[referred] = true
					next = append(next, referred)
				}
			}
		}
	}
	return false
}

// memoryAnswers is the type in which a parameter of the function carries the memory's answers
// (memoryAnswerTypes), and "" where no parameter does.
func (f packageFunction) memoryAnswers() string {
	for _, field := range f.decl.Type.Params.List {
		written := answerType(field.Type)
		for _, answers := range memoryAnswerTypes {
			if written == answers {
				return written
			}
		}
	}
	return ""
}

// answerType is a parameter's type as memoryAnswerTypes writes one: as Go writes it, with every
// pointer read as the type it points to, and a variadic parameter's as the slice it is.
func answerType(expr ast.Expr) string {
	if variadic, isVariadic := expr.(*ast.Ellipsis); isVariadic {
		return "[]" + answerType(variadic.Elt)
	}
	return strings.ReplaceAll(types.ExprString(expr), "*", "")
}

// receiverName is the name of a method's receiver type, without its pointer or its type
// parameters.
func receiverName(expr ast.Expr) string {
	switch expr := expr.(type) {
	case *ast.StarExpr:
		return receiverName(expr.X)
	case *ast.IndexExpr:
		return receiverName(expr.X)
	case *ast.IndexListExpr:
		return receiverName(expr.X)
	case *ast.Ident:
		return expr.Name
	}
	return types.ExprString(expr)
}
