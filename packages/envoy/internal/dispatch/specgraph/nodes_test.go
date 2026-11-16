package specgraph

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// allKinds is the five nodes of testdata/nodes/all-kinds.yaml, in the order of the file, as Go
// values with no position: a statement, a check, a component, an interface and a term.
func allKinds() []any {
	return []any{
		Statement{
			ID:     "a-stored-message-survives-a-restart",
			Text:   "Envoy delivers each stored message to its session after the listener starts again.",
			Kind:   KindBehavior,
			Parent: "envoy-delivers-each-message",
			About:  []string{"envoy-delivery"},
		},
		Check{
			ID:             "a-restart-loses-no-message",
			Serves:         "a-stored-message-survives-a-restart",
			Claim:          "A message that the listener stores before a restart reaches its session after the restart.",
			ProcedureText:  "The program stores one message, starts the listener again and reads the messages of the session.\nIt must find the message one time.",
			FailingExample: "A listener that deletes its stored messages when it starts.",
			Subject:        SubjectCode,
			Procedure:      ProcedureProgram,
			Test:           &Test{Runner: "go", Selector: "github.com/sjawhar/envoy/cmd/listener TestARestartLosesNoMessage", Job: "go-checks"},
		},
		Component{
			ID:      "envoy-delivery",
			Name:    "Envoy delivery",
			Purpose: "It delivers each stored message to the session that it is for.",
			Parent:  "envoy",
		},
		Component{
			ID:        "legion-to-dispatch",
			Name:      "Legion to Dispatch",
			Purpose:   "Legion reads issues and writes results through it.",
			Interface: true,
			Joins:     []string{"legion", "dispatch"},
		},
		Term{ID: "work-unit", Name: "work unit", Definition: "A work unit is work that makes one or more statements true."},
	}
}

// quotedTerm is the term of testdata/nodes/quoted.yaml: a definition whose lines YAML must quote or
// escape, each in the style yaml.v3 picks for it.
var quotedTerm = Term{ID: "quoted", Name: "quoted term", Definition: strings.Join([]string{
	"a: b", "x #y", "no", "2026-11-16", "it's", `say "x"`, "a\tb", "café", "emoji \U0001F600", " lead",
}, "\n")}

// printNode writes each node as the document that a person wrote by hand in testdata/nodes: the
// keys in the order of the schema's table, each list sorted, each text a list of its lines, and a
// component's kind always written.
func TestPrintWritesTheFileForm(t *testing.T) {
	t.Parallel()
	var printed strings.Builder
	for _, node := range allKinds() {
		document, err := printNode(node)
		if err != nil {
			t.Fatalf("printNode(%+v): %v", node, err)
		}
		printed.WriteString(document)
	}
	sameText(t, "all-kinds.yaml", printed.String(), fileText(t, "testdata/nodes/all-kinds.yaml"))

	document, err := printNode(quotedTerm)
	if err != nil {
		t.Fatal(err)
	}
	sameText(t, "quoted.yaml", document, fileText(t, "testdata/nodes/quoted.yaml"))
}

// sameText fails the test at the first line where got and want differ.
func sameText(t *testing.T, name, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	gotLines, wantLines := strings.SplitAfter(got, "\n"), strings.SplitAfter(want, "\n")
	for i := range max(len(gotLines), len(wantLines)) {
		var g, w string
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			t.Fatalf("%s: line %d is %q, want %q", name, i+1, g, w)
		}
	}
}

// fileText is the content of path.
func fileText(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// The texts that YAML must quote or escape: the texts of the round trip of the block format, and
// each line of the plan's review focus 1. Each line is one line of a text, so a text of more lines
// is a list of more items.
var hardTexts = []string{
	`a "quoted" word`, "a & b", "a &amp; b", "a &lt; b", "a } b", "a { b", "a # b", "a `code` b",
	"a\u202fb", "a \U0001F600 b", "#a}", "line one\nline two",
	"a: b", "x #y", "- x", "? x", ": x", "|", ">", "'", `"`, "#", "@at", "`", " lead",
	"yes", "no", "Off", "0x1F", "1_000", ".inf", "2026-11-16", "a\tb", "a\u2028b", "a\u2029b", "a\u0085b",
	"a\ufeffb", "a\u00a0", "a\x01b", "emoji \U0001F600", "line one\nline two\nline three",
}

// Each node prints as a document that the reader reads back as the same node, with the same digest
// and no problem, whatever its texts hold; the lists come back sorted. Each scalar is one line: no
// document holds U+0085, U+2028 or U+2029 as it is, since YAML 1.1 (yaml.v3) reads each as a line
// end and YAML 1.2 (the TypeScript yaml library) does not.
func TestPrintThenReadKeepsEveryText(t *testing.T) {
	t.Parallel()
	for _, text := range hardTexts {
		oneLine := strings.ReplaceAll(text, "\n", " ")
		for _, node := range []any{
			Statement{ID: "a-statement", Text: text, Kind: KindStructure, Parent: "the-parent", Serves: []string{"two", "one"}, About: []string{"envoy"}},
			Check{
				ID: "a-check", Serves: "a-statement", Claim: oneLine, ProcedureText: text, FailingExample: text,
				Subject: SubjectCode, Procedure: ProcedureProgram, TimeLimit: 90 * time.Minute,
				Test: &Test{Runner: "go", Selector: oneLine, Job: oneLine},
			},
			Check{ID: "a-query", Serves: "a-statement", Claim: oneLine, ProcedureText: text, FailingExample: text, Subject: SubjectGraph, Procedure: ProcedureQuery},
			Component{ID: "a-component", Name: oneLine, Purpose: text, Parent: "envoy"},
			Component{ID: "an-interface", Name: oneLine, Purpose: text, Interface: true, Joins: []string{"legion", "dispatch"}},
			Term{ID: "a-term", Name: oneLine, Definition: text},
		} {
			document, err := printNode(node)
			if err != nil {
				t.Errorf("printNode(%+v): %v", node, err)
				continue
			}
			if strings.ContainsAny(document, "\u0085\u2028\u2029") {
				t.Errorf("printNode(%+v) = %q: a scalar holds a line end of YAML 1.1 as it is", node, document)
			}
			tree, problems := parseFiles(map[string][]byte{"x.yaml": []byte(document)})
			if len(problems) != 0 {
				t.Errorf("parseFiles(printNode(%+v)) = parseFiles(%q): problems %+v", node, document, problems)
				continue
			}
			got, want := onlyTreeNode(t, tree), sortedLists(node)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("parseFiles(%q):\n got %+v\nwant %+v", document, got, want)
			}
			if nodeDigest(got) != nodeDigest(node) {
				t.Errorf("parseFiles(%q) has another digest than the node it printed", document)
			}
		}
	}
}

// FuzzPrintThenRead holds the printer to yaml.v3's quoting: a term whose definition is one line of
// any text that a line may hold (UTF-8, not empty, no NUL, CR or LF) prints, and reads back as the
// same term with no problem.
func FuzzPrintThenRead(f *testing.F) {
	for _, text := range hardTexts {
		for line := range strings.SplitSeq(text, "\n") {
			f.Add(line)
		}
	}
	f.Fuzz(func(t *testing.T, line string) {
		if line == "" || !utf8.ValidString(line) || strings.ContainsAny(line, "\x00\r\n") {
			t.Skip("not a line")
		}
		term := Term{ID: "a-term", Name: "a term", Definition: line}
		document, err := printNode(term)
		if err != nil {
			t.Fatalf("printNode(%q): %v", line, err)
		}
		tree, problems := parseFiles(map[string][]byte{"x.yaml": []byte(document)})
		if len(problems) != 0 || len(tree.Terms) != 1 {
			t.Fatalf("parseFiles(%q): %+v, problems %+v", document, tree, problems)
		}
		if got := tree.Terms[0].Definition; got != line {
			t.Fatalf("parseFiles(%q): the definition reads back as %q, want %q", document, got, line)
		}
	})
}

// onlyTreeNode is the one node tree holds, with no position.
func onlyTreeNode(t *testing.T, tree Tree) any {
	t.Helper()
	var nodes []any
	for _, s := range tree.Statements {
		s.Pos = Position{}
		nodes = append(nodes, s)
	}
	for _, c := range tree.Checks {
		c.Pos = Position{}
		nodes = append(nodes, c)
	}
	for _, c := range tree.Components {
		c.Pos = Position{}
		nodes = append(nodes, c)
	}
	for _, term := range tree.Terms {
		term.Pos = Position{}
		nodes = append(nodes, term)
	}
	if len(nodes) != 1 {
		t.Fatalf("tree %+v holds %d nodes, want one", tree, len(nodes))
	}
	return nodes[0]
}

// sortedLists is node with each of its lists sorted, as the reader gives a printed node back.
func sortedLists(node any) any {
	sorted := func(list []string) []string { return slices.Sorted(slices.Values(list)) }
	switch n := node.(type) {
	case Statement:
		n.Serves, n.About = sorted(n.Serves), sorted(n.About)
		return n
	case Component:
		n.Joins = sorted(n.Joins)
		return n
	}
	return node
}

// nodeDigest is the digest of node, a Statement, Check, Component or Term.
func nodeDigest(node any) string {
	return node.(interface{ Digest() string }).Digest()
}
