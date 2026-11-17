package specgraph

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Parse reads each of the four blocks of the final file format, with its attributes, its body and
// the line its opening line is on, and finds no problem in the example of that format.
func TestParseReadsEachBlockKind(t *testing.T) {
	t.Parallel()
	tree, problems := Parse(readFiles(t, "testdata/blocks"))
	if len(problems) != 0 {
		t.Fatalf("Parse: problems %+v, want none", problems)
	}
	want := Tree{
		Statements: []Statement{{
			ID:     "a-stored-message-survives-a-restart",
			Text:   "Envoy delivers each stored message to its session after the listener starts again.",
			Kind:   KindBehavior,
			Parent: "envoy-delivers-each-message",
			About:  []string{"envoy-delivery"},
			Pos:    Position{File: "all-kinds.md", Line: 1},
		}},
		Checks: []Check{{
			ID:             "a-restart-loses-no-message",
			Serves:         "a-stored-message-survives-a-restart",
			Claim:          "A message that the listener stores before a restart reaches its session after the restart.",
			ProcedureText:  "The program stores one message, starts the listener again and reads the messages of the session.\nIt must find the message one time.",
			FailingExample: "A listener that deletes its stored messages when it starts.",
			Subject:        SubjectCode,
			Procedure:      ProcedureProgram,
			Test:           &Test{Runner: "go", Selector: "github.com/sjawhar/envoy/cmd/listener TestARestartLosesNoMessage", Job: "go-checks"},
			Pos:            Position{File: "all-kinds.md", Line: 5},
		}},
		Components: []Component{
			{
				ID:      "envoy-delivery",
				Name:    "Envoy delivery",
				Purpose: "It delivers each stored message to the session that it is for.",
				Parent:  "envoy",
				Pos:     Position{File: "all-kinds.md", Line: 12},
			},
			{
				ID:        "legion-to-dispatch",
				Name:      "Legion to Dispatch",
				Purpose:   "Legion reads issues and writes results through it.",
				Interface: true,
				Joins:     []string{"dispatch", "legion"},
				Pos:       Position{File: "all-kinds.md", Line: 16},
			},
		},
		Terms: []Term{{
			ID:         "work-unit",
			Name:       "work unit",
			Definition: "A work unit is work that makes one or more statements true.",
			Pos:        Position{File: "all-kinds.md", Line: 20},
		}},
	}
	if !reflect.DeepEqual(tree, want) {
		t.Errorf("Parse:\n got %+v\nwant %+v", tree, want)
	}
}

// Parse refuses each line the grammar does not hold with exactly one problem, naming the line
// and the rule, and goes on reading the file after it.
func TestParseRefuses(t *testing.T) {
	t.Parallel()
	const term = ":::term{#work-unit name=\"work unit\"}\nA work unit is work that makes one or more statements true.\n:::\n"
	for _, row := range []struct {
		name, text string
		want       Problem
	}{
		{
			name: "a block that is not a spec block",
			text: ":::plan_task{#x}\nA task.\n:::\n",
			want: Problem{Pos: Position{"x.md", 1}, Node: "x", Rule: "unknown-block", Detail: `unknown-block: "plan_task" is not requirement, check, component or term`},
		},
		{
			name: "a setting that makes the failing example optional",
			text: ":::check{#c serves=\"s\" subject=\"code\" procedure=\"agent\" failing_example_optional=\"true\"}\nClaim: The page shows each statement.\nProcedure: An agent reads the page.\n:::\n",
			want: Problem{Pos: Position{"x.md", 1}, Node: "c", Rule: "unknown-attribute", Detail: `unknown-attribute: check takes no attribute "failing_example_optional"`},
		},
		{
			name: "a paragraph line outside any block",
			text: term + "\nA paragraph line.\nIts second line.\n",
			want: Problem{Pos: Position{"x.md", 5}, Node: "-", Rule: "text-outside-block", Detail: "text-outside-block: a spec file holds only blocks, headings and blank lines"},
		},
		{
			name: "a block with no closing line",
			text: ":::term{#work-unit name=\"work unit\"}\nA work unit is work.\n",
			want: Problem{Pos: Position{"x.md", 1}, Node: "work-unit", Rule: "unclosed-block", Detail: "unclosed-block: the block has no closing ::: line"},
		},
		{
			name: "a block inside a block",
			text: ":::requirement{#a kind=\"behavior\"}\nA text.\n:::requirement{#b}\n:::\n",
			want: Problem{Pos: Position{"x.md", 3}, Node: "a", Rule: "nested-block", Detail: "nested-block: a block cannot hold a block"},
		},
		{
			name: "an id the shortcut ends at a dot",
			text: ":::requirement{#a.b kind=\"behavior\"}\nA text.\n:::\n",
			want: Problem{Pos: Position{"x.md", 1}, Node: "-", Rule: "bad-block-line", Detail: "bad-block-line: the line opens no block"},
		},
		{
			name: "a class, which no spec block takes",
			text: ":::requirement{#a .b kind=\"behavior\"}\nA text.\n:::\n",
			want: Problem{Pos: Position{"x.md", 1}, Node: "a", Rule: "unknown-attribute", Detail: `unknown-attribute: requirement takes no attribute "class"`},
		},
		{
			name: "an attribute written twice",
			text: ":::check{#c serves=\"s\" subject=\"code\" subject=\"code\" procedure=\"agent\"}\nClaim: The page shows each statement.\n:::\n",
			want: Problem{Pos: Position{"x.md", 1}, Node: "-", Rule: "bad-block-line", Detail: "bad-block-line: the line opens no block"},
		},
		{
			name: "a closing line outside any block",
			text: term + ":::\n",
			want: Problem{Pos: Position{"x.md", 4}, Node: "-", Rule: "bad-block-line", Detail: "bad-block-line: the line opens no block"},
		},
		{
			name: "an opening line of four colons",
			text: "::::term{#work-unit name=\"work unit\"}\nA work unit is work.\n:::\n",
			want: Problem{Pos: Position{"x.md", 1}, Node: "-", Rule: "bad-block-line", Detail: "bad-block-line: the line opens no block"},
		},
		{
			name: "a body line that opens no block",
			text: ":::term{#work-unit name=\"work unit\"}\n::: no\n:::\n",
			want: Problem{Pos: Position{"x.md", 2}, Node: "work-unit", Rule: "bad-block-line", Detail: "bad-block-line: the line opens no block"},
		},
		{
			name: "a check that holds its claim twice",
			text: ":::check{#c serves=\"s\" subject=\"code\" procedure=\"agent\"}\nClaim: The page shows each statement.\nClaim: The page shows each check.\nProcedure: An agent reads the page.\n:::\n",
			want: Problem{Pos: Position{"x.md", 3}, Node: "c", Rule: "bad-field", Detail: "bad-field: a check holds one Claim, one Procedure and one Failing example line, in that order"},
		},
		{
			name: "a check that holds its fields out of order",
			text: ":::check{#c serves=\"s\" subject=\"code\" procedure=\"agent\"}\nProcedure: An agent reads the page.\nClaim: The page shows each statement.\n:::\n",
			want: Problem{Pos: Position{"x.md", 3}, Node: "c", Rule: "bad-field", Detail: "bad-field: a check holds one Claim, one Procedure and one Failing example line, in that order"},
		},
		{
			name: "a check with text before its first field",
			text: ":::check{#c serves=\"s\" subject=\"code\" procedure=\"agent\"}\nThe page.\nClaim: The page shows each statement.\n:::\n",
			want: Problem{Pos: Position{"x.md", 2}, Node: "c", Rule: "bad-field", Detail: "bad-field: a check holds one Claim, one Procedure and one Failing example line, in that order"},
		},
		{
			name: "a claim of two lines",
			text: ":::check{#c serves=\"s\" subject=\"code\" procedure=\"agent\"}\nClaim: The page shows each statement.\nIt shows each check.\nProcedure: An agent reads the page.\n:::\n",
			want: Problem{Pos: Position{"x.md", 3}, Node: "c", Rule: "bad-field", Detail: "bad-field: a claim is one line"},
		},
		{
			name: "a component kind that is not interface",
			text: ":::component{#envoy name=\"Envoy\" kind=\"service\"}\nIt delivers messages.\n:::\n",
			want: Problem{Pos: Position{"x.md", 1}, Node: "envoy", Rule: "bad-component", Detail: `bad-component: kind is "interface" or absent, not "service"`},
		},
		{
			name: "a time limit that is not a Go duration",
			text: ":::check{#c serves=\"s\" subject=\"code\" procedure=\"agent\" time_limit=\"a day\"}\nClaim: The page shows each statement.\n:::\n",
			want: Problem{Pos: Position{"x.md", 1}, Node: "c", Rule: "bad-time-limit", Detail: `bad-time-limit: "a day" is not a positive Go duration`},
		},
		{
			name: "a definition that is not UTF-8",
			text: ":::term{#cafe-term name=\"cafe\"}\nA caf\xe9 is a place where a session waits.\n:::\n",
			want: Problem{Pos: Position{"x.md", 2}, Node: "-", Rule: "bad-encoding", Detail: "bad-encoding: a spec file is UTF-8 text with no NUL byte"},
		},
		{
			name: "a name that is not UTF-8",
			text: ":::term{#cafe-term name=\"caf\xe9\"}\nA cafe is a place where a session waits.\n:::\n",
			want: Problem{Pos: Position{"x.md", 1}, Node: "-", Rule: "bad-encoding", Detail: "bad-encoding: a spec file is UTF-8 text with no NUL byte"},
		},
		{
			name: "a NUL byte",
			text: ":::term{#nul-term name=\"nul term\"}\nA nul term is a word.\n\nIt holds a nul\x00byte.\n:::\n",
			want: Problem{Pos: Position{"x.md", 4}, Node: "-", Rule: "bad-encoding", Detail: "bad-encoding: a spec file is UTF-8 text with no NUL byte"},
		},
		{
			name: "two lines that are not text, named once at the first",
			text: ":::term{#cafe-term name=\"cafe\"}\nA caf\xe9 is a place.\nA caf\xe9 is a room.\n:::\n",
			want: Problem{Pos: Position{"x.md", 2}, Node: "-", Rule: "bad-encoding", Detail: "bad-encoding: a spec file is UTF-8 text with no NUL byte"},
		},
		{
			name: "a claim of two sentences on one line",
			text: ":::check{#c serves=\"s\" subject=\"code\" procedure=\"agent\"}\nClaim: The daemon reads the claim. It deletes no claim.\nProcedure: An agent reads the claims.\n:::\n",
			want: Problem{Pos: Position{"x.md", 2}, Node: "c", Rule: "bad-field", Detail: "bad-field: a claim is one sentence"},
		},
		{
			name: "a heading that names code",
			text: "# The internal/dispatch/store package\n\n" + term,
			want: Problem{Pos: Position{"x.md", 1}, Node: "-", Rule: "code-in-text", Detail: "code-in-text: heading names code: internal/dispatch/store"},
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			_, problems := Parse(map[string][]byte{"x.md": []byte(row.text)})
			if !reflect.DeepEqual(problems, []Problem{row.want}) {
				t.Errorf("Parse(%q):\n got %+v\nwant %+v", row.text, problems, []Problem{row.want})
			}
		})
	}
}

// A file in UTF-8 reads with no problem, whatever letters it holds: the control of the bad-encoding
// rows of TestParseRefuses, which write the same term in Latin-1.
func TestParseReadsUTF8Text(t *testing.T) {
	t.Parallel()
	tree, problems := Parse(map[string][]byte{"x.md": []byte(":::term{#cafe-term name=\"caf\u00e9\"}\nA caf\u00e9 is a place where a session waits.\n:::\n")})
	if len(problems) != 0 || len(tree.Terms) != 1 || tree.Terms[0].Name != "caf\u00e9" {
		t.Errorf("Parse: tree %+v, problems %+v; want the term café and no problem", tree, problems)
	}
}

// A claim of one sentence and a heading of plain words read with no problem, also with a dot inside
// a word: the controls of the claim and heading rows of TestParseRefuses.
func TestParseReadsAClaimOfOneSentenceAndAPlainHeading(t *testing.T) {
	t.Parallel()
	for _, claim := range []string{
		"Version 2.1 of the daemon reads the claim of an issue.",
		"The daemon reads the claim of each Node.js client.",
		"The daemon reads the claim of an issue after it starts again",
	} {
		text := "# Claims of Node.js clients\n\n:::check{#c serves=\"s\" subject=\"code\" procedure=\"agent\"}\nClaim: " + claim + "\n:::\n"
		if tree, problems := Parse(map[string][]byte{"x.md": []byte(text)}); len(problems) != 0 || len(tree.Checks) != 1 || tree.Checks[0].Claim != claim {
			t.Errorf("claim %q: tree %+v, problems %+v; want the claim and no problem", claim, tree, problems)
		}
	}
}

// A file written with CRLF line ends, or with a leading byte order mark, reads as the same tree,
// and each node keeps its digest.
func TestParseNormalizesLineEnds(t *testing.T) {
	t.Parallel()
	lf := readFiles(t, "testdata/blocks")["all-kinds.md"]
	crlf := []byte(strings.ReplaceAll(string(lf), "\n", "\r\n"))
	bom := append([]byte("\ufeff"), lf...)
	base, problems := Parse(map[string][]byte{"all-kinds.md": lf})
	if len(problems) != 0 {
		t.Fatalf("Parse(LF): %+v", problems)
	}
	for name, content := range map[string][]byte{"CRLF": crlf, "a byte order mark": bom, "both": append([]byte("\ufeff"), crlf...)} {
		tree, problems := Parse(map[string][]byte{"all-kinds.md": content})
		if len(problems) != 0 {
			t.Errorf("Parse(%s): %+v", name, problems)
			continue
		}
		if !reflect.DeepEqual(tree, base) {
			t.Errorf("Parse(%s):\n got %+v\nwant %+v", name, tree, base)
		}
		if got, want := digests(tree), digests(base); !reflect.DeepEqual(got, want) {
			t.Errorf("digests(%s) %v, want %v", name, got, want)
		}
	}
}

// A time limit reads as its Go duration.
func TestParseReadsATimeLimit(t *testing.T) {
	t.Parallel()
	tree, problems := Parse(map[string][]byte{"x.md": []byte(":::check{#c serves=\"s\" subject=\"production\" procedure=\"agent\" time_limit=\"36h\"}\nClaim: The page shows each statement.\n:::\n")})
	if len(problems) != 0 || len(tree.Checks) != 1 {
		t.Fatalf("Parse: %+v, %+v", tree, problems)
	}
	if got := tree.Checks[0].TimeLimit; got != 36*time.Hour {
		t.Errorf("TimeLimit %v, want 36h", got)
	}
}

// parseRule is one kind of problem of Parse, by the text that parse.go writes for its Detail (a
// constant, or the format of fmt.Sprintf), and a file that gives it.
type parseRule struct{ detail, file string }

// Each kind of problem of Parse is in one of two columns: it can lose a node of the files or change
// one, so a comparison of the tree misses that text, or it leaves each node as the files write it.
// LosesNode gives each the answer of its column, for a file that gives it. The test reads parse.go
// for the Detail of each problem that it writes, so a new kind of problem fails the test until it
// is in a column, and a problem that parse.go writes without the reader's problem method fails it
// too.
func TestEachProblemOfParseSaysWhetherItLosesANode(t *testing.T) {
	t.Parallel()
	check := func(attributes, body string) string {
		return ":::check{#a-restart-keeps-the-claim serves=\"a-claim-survives-a-restart\" subject=\"code\" procedure=\"program\"" + attributes + "}\n" + body + ":::\n"
	}
	losesOrChanges := []parseRule{
		{detailTextOutsideBlock, "A line outside a block.\n"},
		{detailUnclosedBlock, ":::term{#order name=\"order\"}\nAn order is a request.\n"},
		{detailNestedBlock, ":::term{#order name=\"order\"}\nAn order is a request.\n:::term{#bill name=\"bill\"}\nA bill is a request for money.\n:::\n:::\n"},
		{detailBadBlockLine, ":::\n"},
		{`unknown-block: %q is not requirement, check, component or term`, ":::note{#a-note}\nA note.\n:::\n"},
		{`unknown-attribute: %s takes no attribute %q`, ":::term{#order name=\"order\" colour=\"red\"}\nAn order is a request.\n:::\n"},
		{`bad-component: kind is "interface" or absent, not %q`, ":::component{#orders name=\"Orders\" kind=\"service\"}\nIt keeps the orders.\n:::\n"},
		{`bad-time-limit: %q is not a positive Go duration`, check(` time_limit="soon"`, "Claim: The daemon reads the claim after it starts again.\n")},
		{detailBadField, check("", "Procedure: The program starts the daemon again.\nClaim: The daemon reads the claim after it starts again.\n")},
	}
	keepsNodes := []parseRule{
		{detailBadEncoding, ":::term{#cafe name=\"cafe\"}\nA caf\xe9 is a place where a session waits.\n:::\n"},
		{detailHeadingNamesCode, "# Orders and I/O\n"},
		{detailClaimOneLine, check("", "Claim: The daemon reads the claim\nafter it starts again.\n")},
		{detailClaimOneSentence, check("", "Claim: The daemon starts again. It reads the claim.\n")},
	}

	written := problemDetails(t)
	column := map[string]bool{}
	for losesNode, rules := range map[bool][]parseRule{true: losesOrChanges, false: keepsNodes} {
		for _, rule := range rules {
			if _, twice := column[rule.detail]; twice {
				t.Errorf("%q is in both columns", rule.detail)
			}
			column[rule.detail] = losesNode
			if !slices.Contains(written, rule.detail) {
				t.Errorf("%q is in a column, and parse.go writes no such problem", rule.detail)
			}
			start, _, _ := strings.Cut(rule.detail, "%")
			_, problems := Parse(map[string][]byte{"spec.md": []byte(rule.file)})
			given := 0
			for _, p := range problems {
				if strings.HasPrefix(p.Detail, start) {
					given++
					if LosesNode(p) != losesNode {
						t.Errorf("LosesNode(%+v) = %t, want %t", p, !losesNode, losesNode)
					}
				}
			}
			if given == 0 {
				t.Errorf("Parse(%q) gives no problem %q, only %+v", rule.file, rule.detail, problems)
			}
		}
	}
	for _, detail := range written {
		if _, ok := column[detail]; !ok {
			t.Errorf("parse.go writes the problem %q, which is in neither column: say whether it can lose a node or change one", detail)
		}
	}
}

// problemDetails is the Detail of each problem that parse.go writes with the reader's problem
// method: the value of a constant, a string, the string before a +, or the format of fmt.Sprintf.
// It fails the test for a Detail that it cannot read so, and for a Problem that parse.go makes
// outside that method.
func problemDetails(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "parse.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	constants := map[string]string{}
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				for i, name := range value.Names {
					if lit, ok := value.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						constants[name.Name], _ = strconv.Unquote(lit.Value)
					}
				}
			}
		}
	}
	var text func(e ast.Expr) (string, bool)
	text = func(e ast.Expr) (string, bool) {
		switch e := e.(type) {
		case *ast.BasicLit:
			s, err := strconv.Unquote(e.Value)
			return s, e.Kind == token.STRING && err == nil
		case *ast.Ident:
			s, ok := constants[e.Name]
			return s, ok
		case *ast.BinaryExpr:
			return text(e.X)
		case *ast.CallExpr:
			if fun, ok := e.Fun.(*ast.SelectorExpr); ok && fun.Sel.Name == "Sprintf" && len(e.Args) > 0 {
				return text(e.Args[0])
			}
		}
		return "", false
	}
	var details []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "problem" && len(n.Args) == 3 {
					detail, ok := text(n.Args[2])
					if !ok {
						t.Errorf("%s: cannot read the Detail of this problem", fset.Position(n.Pos()))
					}
					details = append(details, detail)
				}
			case *ast.CompositeLit:
				if name, ok := n.Type.(*ast.Ident); ok && name.Name == "Problem" && fn.Name.Name != "problem" {
					t.Errorf("%s: %s makes a Problem outside the reader's problem method", fset.Position(n.Pos()), fn.Name.Name)
				}
			}
			return true
		})
	}
	if len(details) == 0 {
		t.Fatal("UNKNOWN: parse.go holds no call of the reader's problem method, so no kind of problem was read")
	}
	slices.Sort(details)
	return slices.Compact(details)
}

// readFiles reads every file directly in dir, by name, as the parser takes them.
func readFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name()] = content
	}
	return files
}

// digests is the digest of every node of tree, by its kind and id.
func digests(tree Tree) map[string]string {
	all := map[string]string{}
	for _, s := range tree.Statements {
		all["requirement "+s.ID] = s.Digest()
	}
	for _, c := range tree.Checks {
		all["check "+c.ID] = c.Digest()
	}
	for _, c := range tree.Components {
		all["component "+c.ID] = c.Digest()
	}
	for _, term := range tree.Terms {
		all["term "+term.ID] = term.Digest()
	}
	return all
}
