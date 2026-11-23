package specform

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/specform/charge"
)

// The tests of this package make the charge of the readings of a sentence loud (loudReads): a
// reading for the findings that stand in a sentence that would pass ownReads stops the test that
// makes it, as a walk that breaks the bound of a sentence's findings.
func init() { loudReads = true }

// The charge of a judgment's texts (newTexts, charge.Texts) names two sentences of the same words
// by one unit, and a sentence of a title and an address of the same words each by another, whose
// readings are their own: past the charge of the sentence, each still gives the text; it lets
// ownReads readings of one sentence for the findings that stand elsewhere, each giving the text, and
// refuses the next with nothing given; it lets ownReads readings for the findings that stand in it,
// apart from the others, and stops the program at the next where loudReads is set, and refuses it
// where it is not. Control: another sentence's readings are its own.
func TestTheChargeOfASentenceRefusesAReadingPastItAndStopsAnOwnOneLoudly(t *testing.T) {
	const sentence = "Dispatch reads Web.Config at boot."
	texts := newTexts()
	unit, other := texts.Name(sentence, false, false), texts.Name("It logs it.", false, false)
	if again := texts.Name(sentence, false, false); again != unit {
		t.Errorf("two sentences of the same words are the units %v and %v, want one", unit, again)
	}
	title, address := texts.Name(sentence, false, true), texts.Name(sentence, true, false)
	if title == unit || !title.Title() || address == unit || !address.Address() {
		t.Errorf("a sentence of a title and an address of the same words are the units %v and %v, want others than the sentence's %v, a title's and an address's", title, address, unit)
	}
	for count := range ownReads {
		if text, _, ok := texts.Read(unit, false); !ok || text != sentence {
			t.Fatalf("reading %d of %d for the findings that stand elsewhere gives %q, %v, want the sentence", count+1, ownReads, text, ok)
		}
	}
	if text, kept, ok := texts.Read(unit, false); ok || text != "" || kept != nil {
		t.Errorf("reading %d for the findings that stand elsewhere gives %q, %v, %v, want it refused with nothing given", ownReads+1, text, kept, ok)
	}
	for _, each := range []struct {
		kind string
		unit charge.Unit
	}{{"a sentence of a title", title}, {"an address", address}} {
		if text, _, ok := texts.Read(each.unit, false); !ok || text != sentence {
			t.Errorf("past the charge of the sentence, %s of the same words gives %q, %v, want the text from a charge of its own", each.kind, text, ok)
		}
	}
	if _, _, ok := texts.Read(other, false); !ok {
		t.Errorf("the control: the charge refuses the first reading of another sentence")
	}
	for count := range ownReads {
		if _, _, ok := texts.Read(unit, true); !ok {
			t.Fatalf("the charge refuses reading %d of %d for the findings that stand in the sentence", count+1, ownReads)
		}
	}
	stopped := func() (said any) {
		defer func() { said = recover() }()
		_, _, ok := texts.Read(unit, true)
		return ok
	}()
	if message, ok := stopped.(string); !ok || !strings.Contains(message, fmt.Sprintf("past its limit of %d", ownReads)) {
		t.Errorf("reading %d for the findings that stand in the sentence: the charge says %v, want it to stop the program past ownReads", ownReads+1, stopped)
	}
	quiet := charge.New[sentenceAt](ownReads, false)
	quietUnit := quiet.Name(sentence, false, false)
	for range ownReads {
		if _, _, ok := quiet.Read(quietUnit, true); !ok {
			t.Fatalf("with loudReads off, the charge refuses a reading within ownReads")
		}
	}
	if _, _, ok := quiet.Read(quietUnit, true); ok {
		t.Errorf("with loudReads off, the charge lets reading %d for the findings that stand in the sentence, want it refused", ownReads+1)
	}
}

// chargePath is the import path of the package of the judgment's texts.
const chargePath = "github.com/sjawhar/envoy/internal/dispatch/specform/charge"

// droppedAnswers reads the Go files of the package in dir, with the file planted beside them where
// planted is not empty, by their types (go/types: package charge read from its own files, the
// package's other imports as empty packages, whose names are errors that the check passes over),
// and returns each call whose results hold a charge.Answer and that does not keep it: a call that
// is a statement of its own (also after go or defer), one handed to another call, or one whose
// Answer an assignment or a declaration gives the blank name. A call keeps its Answer where a name
// takes it, where the call is the whole of a return statement (its function hands the Answer on as
// its own results), or, for a call of one result, where an expression reads it (as !w.load()). It
// also returns how many calls keep an Answer, and how many of them are calls of charge.Texts.Read.
func droppedAnswers(t *testing.T, dir, planted string) (faults []string, keptCalls, reads int) {
	t.Helper()
	fset := token.NewFileSet()
	parsed := func(dir string, tests bool) []*ast.File {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var files []*ast.File
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") != tests {
				continue
			}
			file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, file)
		}
		return files
	}
	files := parsed(dir, false)
	if planted != "" {
		file, err := parser.ParseFile(fset, "planted.go", planted, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	empty := map[string]*types.Package{}
	var importer importerFunc
	importer = func(path string) (*types.Package, error) {
		if each, ok := empty[path]; ok {
			return each, nil
		}
		if path == chargePath {
			config := types.Config{Error: func(error) {}, Importer: importer}
			each, _ := config.Check(path, fset, parsed(filepath.Join(dir, "charge"), false), nil)
			empty[path] = each
			return each, nil
		}
		each := types.NewPackage(path, path[strings.LastIndex(path, "/")+1:])
		each.MarkComplete()
		empty[path] = each
		return each, nil
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	config := types.Config{Error: func(error) {}, Importer: importer}
	config.Check("specform", fset, files, info)
	chargePkg, err := importer(chargePath)
	if err != nil || chargePkg.Scope().Lookup("Answer") == nil || chargePkg.Scope().Lookup("Texts") == nil {
		t.Fatalf("the check reads no type Answer or Texts in package charge (%v)", err)
	}
	answer := chargePkg.Scope().Lookup("Answer").Type()
	// answers is where the results of a call of type typ hold an Answer, and how many results it has.
	answers := func(typ types.Type) (at []int, count int) {
		if tuple, ok := typ.(*types.Tuple); ok {
			for index := range tuple.Len() {
				if types.Identical(tuple.At(index).Type(), answer) {
					at = append(at, index)
				}
			}
			return at, tuple.Len()
		}
		if typ != nil && types.Identical(typ, answer) {
			return []int{0}, 1
		}
		return nil, 1
	}
	// named reports whether each of at is a name that is not the blank one.
	named := func(names []ast.Expr, at []int) bool {
		for _, index := range at {
			if name, ok := names[index].(*ast.Ident); !ok || name.Name == "_" {
				return false
			}
		}
		return true
	}
	isRead := func(call *ast.CallExpr) bool {
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		selection, ok := info.Selections[selector]
		if !ok {
			return false
		}
		method, ok := selection.Obj().(*types.Func)
		return ok && method.Origin().Pkg() == chargePkg && method.Name() == "Read"
	}
	for _, file := range files {
		var stack []ast.Node
		ast.Inspect(file, func(node ast.Node) bool {
			if node == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			defer func() { stack = append(stack, node) }()
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			at, count := answers(info.Types[call].Type)
			if len(at) == 0 {
				return true
			}
			keeps := false
			switch parent := stack[len(stack)-1].(type) {
			case *ast.AssignStmt:
				if len(parent.Rhs) == 1 && len(parent.Lhs) == count {
					keeps = named(parent.Lhs, at)
				} else if index := slices.Index(parent.Rhs, ast.Expr(call)); index >= 0 && index < len(parent.Lhs) {
					keeps = named(parent.Lhs, []int{index})
				}
			case *ast.ValueSpec:
				names := make([]ast.Expr, len(parent.Names))
				for index, name := range parent.Names {
					names[index] = name
				}
				if len(parent.Values) == 1 && len(names) == count {
					keeps = named(names, at)
				} else if index := slices.Index(parent.Values, ast.Expr(call)); index >= 0 && index < len(names) {
					keeps = named(names, []int{index})
				}
			case *ast.ReturnStmt:
				keeps = len(parent.Results) == 1 || count == 1
			case *ast.ExprStmt, *ast.GoStmt, *ast.DeferStmt:
			case *ast.CallExpr:
				keeps = count == 1 && parent.Fun != ast.Expr(call)
			default:
				keeps = count == 1
			}
			where := fset.Position(call.Pos())
			if !keeps {
				faults = append(faults, filepath.Base(where.Filename)+":"+fmt.Sprint(where.Line)+": drops the charge.Answer of a call")
				return true
			}
			keptCalls++
			if isRead(call) {
				reads++
			}
			return true
		})
	}
	return faults, keptCalls, reads
}

// importerFunc is a types.Importer that a function is.
type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

// Each reading of a text of a judgment keeps the answer of its charge and acts on it: the judge
// reads a sentence only through charge.Texts.Read, which the types hold, and each call whose
// results hold a charge.Answer, Read's and each walk's that hands Read's answer on, binds it to a
// name or hands it on whole, which the check holds by the type alone (droppedAnswers), so no code
// reads on past a charge that refused it as though it said yes. Controls, each a file planted
// beside the package's own: the blank answer of Read, Read as a statement, in a loop and after
// defer, Read's results handed to another call, the blank answer of a walk that hands one on
// (wholeText.other), and a walk of one Answer as a statement and given the blank name, are each
// named; a call that keeps its answer is not; and with no plant, no fault, and the check finds the
// package's calls of Read and of the walks.
func TestEachReadingOfATextKeepsTheAnswerOfItsCharge(t *testing.T) {
	faults, keptCalls, reads := droppedAnswers(t, ".", "")
	if len(faults) > 0 || reads < 6 || keptCalls < 20 {
		t.Errorf("the package's own files: faults %q, %d calls that keep an Answer and %d of them of Read, want none, 20 or more and 6 or more", faults, keptCalls, reads)
	}
	t.Logf("the package's own files: %d calls that keep a charge.Answer, %d of them of charge.Texts.Read", keptCalls, reads)
	for _, plant := range []struct{ name, body string }{
		{"the blank answer of Read", "text, _, _ := j.texts.Read(unit, false)\n\treturn len(text)"},
		{"Read as a statement", "j.texts.Read(unit, false)\n\treturn 0"},
		{"Read in a loop as a statement", "for range 3 {\n\t\tj.texts.Read(unit, false)\n\t}\n\treturn 0"},
		{"Read after defer", "defer j.texts.Read(unit, false)\n\treturn 0"},
		{"Read's results handed to another call", "return len(fmt.Sprint(j.texts.Read(unit, false)))"},
		{"the blank answer of a walk", "at, _ := j.whole.other(0, nil)\n\treturn at.index"},
		{"a walk of one Answer as a statement", "j.whole.load()\n\treturn 0"},
		{"a walk of one Answer given the blank name", "_ = j.whole.load()\n\treturn 0"},
	} {
		planted := "package specform\n\nimport (\n\t\"fmt\"\n\n\t\"" + chargePath + "\"\n)\n\nvar _ = fmt.Sprint\n\nfunc (j *wayJudge) planted(unit charge.Unit) int {\n\t" + plant.body + "\n}\n"
		if faults, _, _ := droppedAnswers(t, ".", planted); !slices.ContainsFunc(faults, func(fault string) bool { return strings.HasPrefix(fault, "planted.go:") }) {
			t.Errorf("the control, %s: the check gives the faults %q, want it named in planted.go", plant.name, faults)
		}
	}
	kept := "package specform\n\nimport \"" + chargePath + "\"\n\nfunc (j *wayJudge) planted(unit charge.Unit) charge.Answer {\n\t_, _, charged := j.texts.Read(unit, false)\n\treturn charged\n}\n"
	if faults, calls, read := droppedAnswers(t, ".", kept); len(faults) > 0 || calls != keptCalls+1 || read != reads+1 {
		t.Errorf("the control, a call that keeps its answer: faults %q, %d calls and %d of Read, want none, %d and %d", faults, calls, read, keptCalls+1, reads+1)
	}
}
