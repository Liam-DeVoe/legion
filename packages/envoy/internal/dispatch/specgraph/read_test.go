package specgraph

import (
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Each document of the hand-written files of testdata/nodes reads, with yaml.v3 and as JSON, as
// the data that a person wrote for it in the .decoded.json file beside it. The TypeScript test of
// the schema holds yaml's reading of the same files to the same data.
func TestNodesDecodeToTheirData(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"all-kinds", "quoted"} {
		documents, problems := readDocuments(name+".yaml", []byte(fileText(t, "testdata/nodes/"+name+".yaml")))
		if len(problems) != 0 {
			t.Fatalf("%s.yaml: problems %+v", name, problems)
		}
		var want []any
		if err := json.Unmarshal([]byte(fileText(t, "testdata/nodes/"+name+".decoded.json")), &want); err != nil {
			t.Fatal(err)
		}
		if len(documents) != len(want) {
			t.Fatalf("%s.yaml holds %d documents, and %s.decoded.json %d objects", name, len(documents), name, len(want))
		}
		for i, document := range documents {
			var got any
			if err := json.Unmarshal(document.data, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want[i]) {
				t.Errorf("%s.yaml, the document at line %d:\n got %s\nwant the object %d of %s.decoded.json, %v", name, document.line, document.data, i+1, name, want[i])
			}
		}
	}
}

// The reader reads each kind of node from the hand-written file, with each node at the line of its
// document's "---", and finds no problem in it.
func TestReadReadsEachNodeKind(t *testing.T) {
	t.Parallel()
	tree, problems := parseFiles(map[string][]byte{"all-kinds.yaml": []byte(fileText(t, "testdata/nodes/all-kinds.yaml"))})
	if len(problems) != 0 {
		t.Fatalf("parseFiles: problems %+v, want none", problems)
	}
	nodes := allKinds()
	at := func(line int) Position { return Position{File: "all-kinds.yaml", Line: line} }
	statement, check, component, iface, term := nodes[0].(Statement), nodes[1].(Check), nodes[2].(Component), nodes[3].(Component), nodes[4].(Term)
	statement.Pos, check.Pos, component.Pos, iface.Pos, term.Pos = at(1), at(10), at(26), at(34), at(44)
	iface.Joins = []string{"dispatch", "legion"}
	want := Tree{
		Statements: []Statement{statement},
		Checks:     []Check{check},
		Components: []Component{component, iface},
		Terms:      []Term{term},
	}
	if !reflect.DeepEqual(tree, want) {
		t.Errorf("parseFiles:\n got %+v\nwant %+v", tree, want)
	}

	// The control: the same documents in the reverse order read with no problem and keep each
	// digest.
	documents := splitDocuments(fileText(t, "testdata/nodes/all-kinds.yaml"))
	slices.Reverse(documents)
	other, problems := parseFiles(map[string][]byte{"reversed.yaml": []byte(strings.Join(documents, ""))})
	if len(problems) != 0 {
		t.Fatalf("parseFiles(reversed): problems %+v, want none", problems)
	}
	if got, want := digests(other), digests(tree); !reflect.DeepEqual(got, want) {
		t.Errorf("digests of the reversed file %v, want %v", got, want)
	}
	if other.Statements[0].Pos == tree.Statements[0].Pos {
		t.Fatalf("the statement is at %v in both trees; the control moves nothing", tree.Statements[0].Pos)
	}
}

// The reader refuses each fault of a file with exactly one problem: its line, its node and its
// rule. A fault of the YAML or of its JSON form gives no node of that document; a fault that only
// the schema finds gives the node when its keys have the schema's types; and each other spelling
// of a node is not-printed.
func TestReadRefuses(t *testing.T) {
	t.Parallel()
	const term = "---\ntype: term\nid: work-unit\nname: work unit\ndefinition:\n  - A work unit is work.\n"
	const program = "---\ntype: check\nid: a-restart-keeps-the-claim\nserves: a-claim-survives-a-restart\nsubject: code\nprocedure: program\n" +
		"test:\n  runner: go\n  selector: example.com/acme/daemon TestARestartKeepsTheClaim\n  job: go-checks\n" +
		"time_limit: %s\nclaim: The daemon reads the claim after it starts again.\n" +
		"procedure_text:\n  - The program claims one issue and starts the daemon again.\nfailing_example:\n  - A daemon with no store.\n"
	const query = "---\ntype: check\nid: each-claim-is-in-the-graph\nserves: a-claim-survives-a-restart\nsubject: graph\nprocedure: query\n" +
		"test:\n  runner: go\n  selector: example.com/acme/daemon TestARestartKeepsTheClaim\n  job: go-checks\n" +
		"claim: The graph holds each claim.\nprocedure_text:\n  - A query reads the claims of the graph.\nfailing_example:\n  - A graph with no claims.\n"
	const structure = "---\ntype: requirement\nid: legion-reads-issues-only-through-dispatch\nkind: structure\nabout:\n  - legion\n  - dispatch\n" +
		"text:\n  - Legion reads each issue through Dispatch.\n"
	notPrinted := func(line int, node string) Problem {
		return Problem{Position{"x.yaml", line}, node, "not-printed", "not-printed: line " + strconv.Itoa(line) + " is not as Print writes the node"}
	}
	for _, row := range []struct {
		name  string
		text  string
		want  Problem
		nodes int // how many nodes the tree holds
	}{
		{name: "an open flow list", text: "---\ntype: [\n",
			want: Problem{Position{"x.yaml", 2}, "-", "bad-yaml", "bad-yaml: line 2: did not find expected node content"}},
		{name: "a byte that is not UTF-8", text: strings.Replace(term, "work unit\n", "caf\xe9 unit\n", 1),
			want: Problem{Position{"x.yaml", 1}, "-", "bad-yaml", "bad-yaml: invalid trailing UTF-8 octet"}},
		{name: "a NUL byte", text: strings.Replace(term, "work unit\n", "work\x00unit\n", 1),
			want: Problem{Position{"x.yaml", 1}, "-", "bad-yaml", "bad-yaml: control characters are not allowed"}},
		{name: "a key written two times", text: strings.Replace(term, "type: term\n", "type: term\ntype: term\n", 1),
			want: Problem{Position{"x.yaml", 3}, "-", "bad-yaml", `bad-yaml: line 3: mapping key "type" already defined at line 2`}},
		{name: "a key that is a number", text: "---\n1: x\n",
			want: Problem{Position{"x.yaml", 1}, "-", "bad-yaml", "bad-yaml: the document holds a value that JSON cannot hold"}},
		{name: "an empty file", text: "",
			want: Problem{Position{"x.yaml", 1}, "-", "no-node", "no-node: a spec file holds one or more nodes"}},
		{name: "a key that the node does not take", text: term + "surprise: true\n", nodes: 1,
			want: Problem{Position{"x.yaml", 1}, "work-unit", "schema", "schema: node: additional properties 'surprise' not allowed"}},
		{name: "a type that is no node kind", text: strings.Replace(term, "type: term", "type: plan_task", 1),
			want: Problem{Position{"x.yaml", 1}, "work-unit", "schema", "schema: /type: value must be one of 'requirement', 'check', 'component', 'term'"}},
		{name: "a text that is a string", text: "---\ntype: requirement\nid: a-claim-survives-a-restart\nkind: behavior\ntext: Legion keeps each claim.\n",
			want: Problem{Position{"x.yaml", 1}, "a-claim-survives-a-restart", "schema", "schema: /text: got string, want array"}},
		{name: "a document that is only its --- line", text: "---\n",
			want: Problem{Position{"x.yaml", 1}, "-", "schema", "schema: node: got null, want object"}},
		{name: "a name that is a number in the second document", text: term + strings.Replace(strings.Replace(term, "work-unit", "work-item", 1), "work unit", "12", 1), nodes: 1,
			want: Problem{Position{"x.yaml", 7}, "work-item", "schema", "schema: /name: got number, want string"}},
		{name: "a query check with a test part", text: query, nodes: 1,
			want: Problem{Position{"x.yaml", 1}, "each-claim-is-in-the-graph", "schema", "schema: /test: the node takes no such key"}},
		{name: "a key out of its order", text: strings.Replace(term, "id: work-unit\nname: work unit\n", "name: work unit\nid: work-unit\n", 1), nodes: 1,
			want: notPrinted(3, "work-unit")},
		{name: "a comment", text: strings.Replace(term, "---\n", "---\n# The term of work.\n", 1), nodes: 1, want: notPrinted(2, "work-unit")},
		{name: "a tag", text: strings.Replace(term, "name: work unit", "name: !!str work unit", 1), nodes: 1, want: notPrinted(4, "work-unit")},
		{name: "a space at the end of a line", text: strings.Replace(term, "name: work unit", "name: work unit ", 1), nodes: 1, want: notPrinted(4, "work-unit")},
		{name: "quotes around a plain value", text: strings.Replace(term, "name: work unit", `name: "work unit"`, 1), nodes: 1, want: notPrinted(4, "work-unit")},
		{name: "a flow list", text: strings.Replace(term, "definition:\n  - A work unit is work.", "definition: [A work unit is work.]", 1), nodes: 1, want: notPrinted(5, "work-unit")},
		{name: "a block scalar", text: strings.Replace(term, "  - A work unit is work.", "  - |-\n    A work unit is work.", 1), nodes: 1, want: notPrinted(6, "work-unit")},
		{name: "a list out of its order", text: structure, nodes: 1, want: notPrinted(6, "legion-reads-issues-only-through-dispatch")},
		{name: "an indent of four spaces", text: strings.Replace(term, "  - A work", "    - A work", 1), nodes: 1, want: notPrinted(6, "work-unit")},
		{name: "a date that is not quoted", text: strings.Replace(term, "name: work unit", "name: 2026-11-16", 1), nodes: 1, want: notPrinted(4, "work-unit")},
		{name: "an anchor and an alias", text: strings.Replace(term, "name: work unit\ndefinition:\n  - A work unit is work.", "name: &n work unit\ndefinition:\n  - *n", 1), nodes: 1,
			want: notPrinted(4, "work-unit")},
		{name: "a first document with no --- line", text: strings.TrimPrefix(term, "---\n"), nodes: 1, want: notPrinted(1, "work-unit")},
		{name: "CRLF line ends", text: strings.ReplaceAll(term, "\n", "\r\n"), nodes: 1, want: notPrinted(1, "work-unit")},
		{name: "a byte order mark", text: "\ufeff" + term, nodes: 1, want: notPrinted(1, "work-unit")},
		{name: "a time limit that is not a Go duration", text: strings.Replace(program, "%s", "a day", 1), nodes: 1,
			want: Problem{Position{"x.yaml", 1}, "a-restart-keeps-the-claim", "bad-time-limit", `bad-time-limit: "a day" is not a positive Go duration`}},
		{name: "a time limit of zero", text: strings.Replace(program, "%s", "0s", 1), nodes: 1,
			want: Problem{Position{"x.yaml", 1}, "a-restart-keeps-the-claim", "bad-time-limit", `bad-time-limit: "0s" is not a positive Go duration`}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			tree, problems := parseFiles(map[string][]byte{"x.yaml": []byte(row.text)})
			if !reflect.DeepEqual(problems, []Problem{row.want}) {
				t.Errorf("parseFiles(%q):\n got %+v\nwant %+v", row.text, problems, []Problem{row.want})
			}
			if got := len(nodesOf(tree)); got != row.nodes {
				t.Errorf("parseFiles(%q): the tree holds %d nodes, want %d: %+v", row.text, got, row.nodes, tree)
			}
		})
	}
}

// splitDocuments is each document of text, a file that starts with its first "---" line: the text
// from each line that is exactly "---" to the next.
func splitDocuments(text string) []string {
	var documents []string
	for line := range strings.Lines(text) {
		if line == "---\n" || len(documents) == 0 {
			documents = append(documents, "")
		}
		documents[len(documents)-1] += line
	}
	return documents
}
