package specform

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The tests of this package make the charge of the readings of a sentence loud (loudReads): a
// reading for the findings that stand in a sentence that would pass ownReads stops the test that
// makes it, as a walk that breaks the bound of a sentence's findings.
func init() { loudReads = true }

// The charge of a sentence (sentenceReads.charge) lets ownReads readings of one sentence for the
// findings that stand elsewhere and refuses the next, and lets ownReads readings for the findings
// that stand in it, apart from the others, and stops the program at the next where loudReads is
// set. Control: another sentence's readings are its own.
func TestTheChargeOfASentenceRefusesAReadingPastItAndStopsAnOwnOneLoudly(t *testing.T) {
	var reads sentenceReads
	unit, other := unitKey{text: "Dispatch reads Web.Config at boot."}, unitKey{text: "It logs it."}
	for count := range ownReads {
		if !reads.charge(unit, false) {
			t.Fatalf("the charge refuses reading %d of %d for the findings that stand elsewhere", count+1, ownReads)
		}
	}
	if reads.charge(unit, false) {
		t.Errorf("the charge lets reading %d for the findings that stand elsewhere, want it refused", ownReads+1)
	}
	if !reads.charge(other, false) {
		t.Errorf("the control: the charge refuses the first reading of another sentence")
	}
	for count := range ownReads {
		if !reads.charge(unit, true) {
			t.Fatalf("the charge refuses reading %d of %d for the findings that stand in the sentence", count+1, ownReads)
		}
	}
	stopped := func() (said any) {
		defer func() { said = recover() }()
		reads.charge(unit, true)
		return nil
	}()
	if message, ok := stopped.(string); !ok || !strings.Contains(message, "past ownReads") {
		t.Errorf("reading %d for the findings that stand in the sentence: the charge says %v, want it to stop the program past ownReads", ownReads+1, stopped)
	}
	loudReads = false
	defer func() { loudReads = true }()
	if reads.charge(unit, true) {
		t.Errorf("with loudReads off, the charge lets reading %d for the findings that stand in the sentence, want it refused", ownReads+1)
	}
}

// readBytes is each field of the package that holds the bytes of a sentence or what one reading of
// a sentence keeps of it, by its type: a function that selects one reads a sentence.
var readBytes = map[string][]string{
	"unitKey":         {"text"},
	"wholeText":       {"line", "spans", "spreads", "bare"},
	"writtenSentence": {"text"},
	"sentencePlaces":  {"words", "shown", "sentences"},
}

// readWithin is each function of the package that reads a sentence and asks no charge, with the
// functions that may call it: it reads only what its callers charged in the same reading, the
// sentences of a way out and the runs between them.
var readWithin = map[string][]string{
	"wholeText.spread":   {"wholeText.changedRuns", "wholeText.pieces", "wholeText.writesNoNewFinding"},
	"wholeText.pieces":   {"wholeText.load", "wholeText.writesNoNewFinding"},
	"wholeText.dangling": {"wholeText.writesNoNewFinding"},
}

// readingFaults reads the Go files of the package in dir, with the file planted beside them where
// planted is not empty, and returns each function that reads a sentence (readBytes) and neither
// asks its charge (sentenceReads.charge) nor stands in readWithin, and each call of a function of
// readWithin from a function that readWithin does not name for it; and what it read: how many
// selections of each field of readBytes, and each function that asks the charge.
func readingFaults(t *testing.T, dir, planted string) (faults []string, selected map[string]int, charging []string) {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, dir+"/"+name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	if planted != "" {
		file, err := parser.ParseFile(fset, "planted.go", planted, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	// The package's imports are read as empty packages: each name of them is an error that the
	// check passes over, while the package's own types, fields and functions are read in full.
	imported := map[string]*types.Package{}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	config := types.Config{Error: func(error) {}, Importer: importerFunc(func(path string) (*types.Package, error) {
		if each, ok := imported[path]; ok {
			return each, nil
		}
		each := types.NewPackage(path, path[strings.LastIndex(path, "/")+1:])
		each.MarkComplete()
		imported[path] = each
		return each, nil
	})}
	pkg, _ := config.Check("specform", fset, files, info)
	nameOf := func(fn *types.Func) string {
		signature, ok := fn.Type().(*types.Signature)
		if !ok || signature.Recv() == nil {
			return fn.Name()
		}
		receiver := signature.Recv().Type()
		if pointer, ok := receiver.(*types.Pointer); ok {
			receiver = pointer.Elem()
		}
		if named, ok := receiver.(*types.Named); ok {
			return named.Obj().Name() + "." + fn.Name()
		}
		return fn.Name()
	}
	reads := map[string]bool{}
	charges := map[string]bool{}
	callers := map[string][]string{}
	selected = map[string]int{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			fn, ok := info.Defs[fd.Name].(*types.Func)
			if !ok {
				continue
			}
			name := nameOf(fn)
			ast.Inspect(fd, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.SelectorExpr:
					selection, ok := info.Selections[node]
					if !ok || selection.Kind() != types.FieldVal {
						return true
					}
					receiver := selection.Recv()
					if pointer, ok := receiver.(*types.Pointer); ok {
						receiver = pointer.Elem()
					}
					if named, ok := receiver.(*types.Named); ok && named.Obj().Pkg() == pkg && slices.Contains(readBytes[named.Obj().Name()], node.Sel.Name) {
						reads[name] = true
						selected[named.Obj().Name()+"."+node.Sel.Name]++
					}
				case *ast.Ident:
					used, ok := info.Uses[node].(*types.Func)
					if !ok || used.Pkg() != pkg {
						return true
					}
					switch callee := nameOf(used); {
					case strings.HasPrefix(callee, "sentenceReads."):
						charges[name] = true
					case readWithin[callee] != nil:
						callers[callee] = append(callers[callee], name)
					}
				}
				return true
			})
		}
	}
	for name := range reads {
		if !charges[name] && readWithin[name] == nil && !strings.HasPrefix(name, "sentenceReads.") {
			faults = append(faults, fmt.Sprintf("%s reads a sentence and asks no charge", name))
		}
	}
	for callee, allowed := range readWithin {
		for _, caller := range callers[callee] {
			if !slices.Contains(allowed, caller) {
				faults = append(faults, fmt.Sprintf("%s uses %s, which reads only what %v charged", caller, callee, allowed))
			}
		}
		for _, caller := range allowed {
			if !charges[caller] && readWithin[caller] == nil {
				faults = append(faults, fmt.Sprintf("%s may use %s, and neither asks a charge nor stands in readWithin", caller, callee))
			}
		}
	}
	for name := range charges {
		charging = append(charging, name)
	}
	sort.Strings(faults)
	sort.Strings(charging)
	return faults, selected, charging
}

// importerFunc is a types.Importer that a function is.
type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

// Each reading of a sentence in a judgment asks the sentence's charge first (sentenceReads), so a
// walk that reads a sentence and does not fails here, not in a review: each function of the package
// that selects a field that holds a sentence (readBytes) asks the charge, or reads only what the
// functions that call it charged, which readWithin names. The check reads the package's own types
// (go/types), so it sees each selection of each field of readBytes. Controls, each with the
// package's files: a planted walk that reads the words of the sentences where a finding stands
// alone and asks no charge is found, and so is a planted call of wholeText.pieces from a function
// that readWithin does not name; and with no plant, no fault.
func TestEveryReadingOfASentenceAsksItsChargeFirst(t *testing.T) {
	faults, selected, charging := readingFaults(t, ".", "")
	for _, fault := range faults {
		t.Errorf("%s", fault)
	}
	for owner, fields := range readBytes {
		for _, field := range fields {
			if selected[owner+"."+field] == 0 {
				t.Errorf("the check sees no selection of %s.%s, so it reads too little", owner, field)
			}
		}
	}
	for _, want := range []string{"wayJudge.unit", "wayJudge.before", "wayJudge.passes", "wholeText.holding", "wholeText.passes",
		"wholeText.changedRuns", "wholeText.writesNoNewFinding", "sentencePlaces.of", "newWholeText"} {
		if !slices.Contains(charging, want) {
			t.Errorf("the check finds no charge asked in %s; it finds %v", want, charging)
		}
	}
	total := 0
	for _, count := range selected {
		total += count
	}
	t.Logf("%d selections of %d fields that hold a sentence; charges asked in %v", total, len(selected), charging)
	const plantedWalk = `package specform

func (j *wayJudge) plantedEcho(places []unitKey) int {
	words := 0
	for _, at := range places {
		words += len(sentencesOf(at.text))
	}
	return words
}
`
	if faults, _, _ := readingFaults(t, ".", plantedWalk); !slices.Contains(faults, "wayJudge.plantedEcho reads a sentence and asks no charge") {
		t.Errorf("the control: a planted walk that reads each place and asks no charge gives the faults %q, want it named", faults)
	}
	const plantedCall = `package specform

func (w *wholeText) plantedPieces() int {
	words := 0
	w.pieces(0, 0, nil, false, func(each int) { words += each })
	return words
}
`
	if faults, _, _ := readingFaults(t, ".", plantedCall); !slices.Contains(faults,
		"wholeText.plantedPieces uses wholeText.pieces, which reads only what [wholeText.load wholeText.writesNoNewFinding] charged") {
		t.Errorf("the control: a planted call of wholeText.pieces gives the faults %q, want it named", faults)
	}
}
