package specgraph

import (
	"bytes"
	"cmp"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"github.com/sjawhar/envoy/internal/dispatch/architecture/notes"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"gopkg.in/yaml.v3"
)

// schemaJSON is the schema of one node: the data of one YAML document of a spec file. It is the
// contract of the files: the reader checks each document against it, the TypeScript test of the
// schema reads it, and a person's editor can check a document against it.
//
//go:embed schema/spec-node.schema.json
var schemaJSON []byte

// schemaID is the $id of the schema.
const schemaID = "urn:dispatch:spec-node"

// nodeSchema is the compiled schema. A schema that does not compile is a fault of the build: it
// panics.
var nodeSchema = sync.OnceValue(func() *jsonschema.Schema {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		panic(fmt.Sprintf("specgraph: the node schema is not JSON: %v", err))
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaID, doc); err != nil {
		panic(fmt.Sprintf("specgraph: the node schema: %v", err))
	}
	schema, err := compiler.Compile(schemaID)
	if err != nil {
		panic(fmt.Sprintf("specgraph: the node schema does not compile: %v", err))
	}
	return schema
})

// english writes the messages of the schema validator.
var english = message.NewPrinter(language.English)

// yamlLine is the line that a message of yaml.v3 names at its start.
var yamlLine = regexp.MustCompile(`^line ([0-9]+):`)

// document is one YAML document of a spec file: the line of its "---" (or of its first line, for a
// document that has none), and its data written as JSON, nil when the reader refused the data.
type document struct {
	line int
	data []byte
}

// readDocuments reads the documents of one spec file, the steps of readFile before the schema: it
// decodes each document with yaml.v3 into a yaml.Node, then into a value, and writes the value as
// JSON. A fault of the YAML is bad-yaml: one that ends the stream stops the reading, keeps the
// documents before it and answers stopped; one that only its document holds, such as a key written
// two times or a key that is not a string, keeps the document with no data. A file with no
// document is no-node.
func readDocuments(name string, data []byte) (documents []document, problems []Problem, stopped bool) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc yaml.Node
		err := decoder.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			problems = append(problems, yamlProblem(name, err.Error(), 1))
			stopped = true
			break
		}
		d := document{line: doc.Line}
		var value any
		var typeErr *yaml.TypeError
		switch err := doc.Decode(&value); {
		case errors.As(err, &typeErr):
			for _, entry := range typeErr.Errors {
				problems = append(problems, yamlProblem(name, entry, d.line))
			}
		case err != nil:
			problems = append(problems, yamlProblem(name, err.Error(), d.line))
		default:
			js, err := json.Marshal(value)
			if err != nil || !stringKeys(value) {
				problems = append(problems, idProblem(Position{File: name, Line: d.line}, "", "bad-yaml: the document holds a value that JSON cannot hold"))
				break
			}
			d.data = js
		}
		documents = append(documents, d)
	}
	if len(documents) == 0 && len(problems) == 0 {
		problems = append(problems, idProblem(Position{File: name, Line: 1}, "", "no-node: a spec file holds one or more nodes"))
	}
	return documents, problems, stopped
}

// yamlProblem is the bad-yaml problem of one message of yaml.v3, without its "yaml: " prefix, at
// the line the message names, else at line.
func yamlProblem(name, msg string, line int) Problem {
	msg = strings.TrimPrefix(msg, "yaml: ")
	if m := yamlLine.FindStringSubmatch(msg); m != nil {
		line, _ = strconv.Atoi(m[1])
	}
	return idProblem(Position{File: name, Line: line}, "", "bad-yaml: "+msg)
}

// stringKeys reports whether each mapping of value has strings for keys, as JSON's objects do:
// yaml.v3 gives a mapping with another key as a map[any]any, which encoding/json would write with
// the key as a string.
func stringKeys(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for _, item := range v {
			if !stringKeys(item) {
				return false
			}
		}
	case []any:
		for _, item := range v {
			if !stringKeys(item) {
				return false
			}
		}
	case map[any]any:
		return false
	}
	return true
}

// readFile reads one spec file: each of its documents, in order, with its node (Pos{name, the
// line of its document's "---"}, nil for a document that gives none) and its text, and each
// problem. A document whose data the schema refuses still gives its node when its keys have the
// schema's types, so one fault gives one problem. When the file has no other problem, it must be
// exactly the documents that printNode writes for its nodes, in its order: one node has one
// spelling. The texts of the documents, joined, are data: the text of a document runs from the
// line of its "---" to the next document's (the first from the start of the file), and the part of
// a file after a fault that stopped the reading is a document of its own with no node, from the
// first line after the last document read that starts the next one ("---" or "...").
func readFile(name string, data []byte) ([]Document, []Problem) {
	documents, problems, stopped := readDocuments(name, data)
	var nodes []any
	var lines []int
	for _, d := range documents {
		if d.data == nil {
			continue
		}
		node, found := readNode(name, d)
		problems = append(problems, found...)
		if node != nil {
			nodes = append(nodes, node)
			lines = append(lines, d.line)
		}
	}
	if len(problems) == 0 {
		if line, node := firstUnprinted(data, nodes, lines); line > 0 {
			problems = append(problems, idProblem(Position{File: name, Line: line}, nodeID(node), fmt.Sprintf("not-printed: line %d is not as Print writes the node", line)))
		}
	}
	return splitFile(data, documents, nodes, lines, stopped), problems
}

// splitFile is the documents of data, each with its node: documents is each document read, at
// its line, and nodes the node of each document at lines that gave one.
func splitFile(data []byte, documents []document, nodes []any, lines []int, stopped bool) []Document {
	text := string(data)
	// starts is the byte offset of the start of each line, from line 1.
	starts := []int{0}
	for i := range len(text) {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	offset := func(line int) int { return starts[min(max(line, 1), len(starts))-1] }
	cuts := make([]int, 0, len(documents)+1)
	for i, d := range documents {
		at := offset(d.line)
		if i == 0 {
			at = 0
		}
		cuts = append(cuts, at)
	}
	if stopped {
		rest := len(text)
		if len(documents) == 0 {
			rest = 0
		} else {
			for line := documents[len(documents)-1].line + 1; line <= len(starts); line++ {
				if l := text[offset(line):]; strings.HasPrefix(l, "---") || strings.HasPrefix(l, "...") {
					rest = offset(line)
					break
				}
			}
		}
		cuts = append(cuts, rest)
	}
	var result []Document
	for i, at := range cuts {
		end := len(text)
		if i+1 < len(cuts) {
			end = cuts[i+1]
		}
		if at == end && i == len(documents) {
			continue // a stop at the end of the file leaves no text after the documents
		}
		d := Document{Text: text[at:end]}
		if i < len(documents) {
			if k := slices.Index(lines, documents[i].line); k >= 0 {
				d.Node = nodes[k]
			}
		}
		result = append(result, d)
	}
	if len(result) == 0 && len(text) > 0 {
		result = []Document{{Text: text}}
	}
	return result
}

// schemaRefusals is one schema problem at pos, of the node id, for each place where the data inst
// breaks the schema, each detail once.
func schemaRefusals(pos Position, id string, inst any) []Problem {
	var refused *jsonschema.ValidationError
	if !errors.As(nodeSchema().Validate(inst), &refused) {
		return nil
	}
	var problems []Problem
	for _, leaf := range schemaLeaves(refused) {
		problems = append(problems, idProblem(pos, id, "schema: "+schemaDetail(leaf)))
	}
	slices.SortFunc(problems, func(a, b Problem) int { return strings.Compare(a.Detail, b.Detail) })
	return slices.CompactFunc(problems, func(a, b Problem) bool { return a.Detail == b.Detail })
}

// schemaProblems is each problem that the schema finds in the data of a node of tree, at the
// node's position, as Parse finds it in the node's document. A tree that a caller builds with no
// reader, as the pilot does, meets the same rules of shape as the files: CheckStates judges the
// tree by schemaProblems and Validate.
func schemaProblems(tree Tree) []Problem {
	var problems []Problem
	for _, n := range nodesOf(tree) {
		file, _ := fileOf(n.node)
		js, err := json.Marshal(file)
		if err != nil {
			continue
		}
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(js))
		if err != nil {
			continue
		}
		problems = append(problems, schemaRefusals(n.pos, n.id, inst)...)
	}
	return problems
}

// readNode checks the data of one document against the schema, and gives its node: nil for a
// type that is not one of the four, or for a key whose value is of another type than the schema's.
func readNode(name string, d document) (any, []Problem) {
	pos := Position{File: name, Line: d.line}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(d.data))
	if err != nil {
		return nil, []Problem{idProblem(pos, "", "bad-yaml: the document holds a value that JSON cannot hold")}
	}
	object, _ := inst.(map[string]any)
	id, _ := object["id"].(string)
	problems := schemaRefusals(pos, id, inst)
	typ, _ := object["type"].(string)
	switch typ {
	case "requirement":
		var f requirementFile
		if json.Unmarshal(d.data, &f) != nil {
			return nil, problems
		}
		return Statement{ID: f.ID, Text: joinLines(f.Text), Kind: Kind(f.Kind), Parent: f.Parent, Serves: f.Serves, About: f.About, Pos: pos}, problems
	case "check":
		var f checkFile
		if json.Unmarshal(d.data, &f) != nil {
			return nil, problems
		}
		c := Check{
			ID: f.ID, Serves: f.Serves, Claim: f.Claim, ProcedureText: joinLines(f.ProcedureText), FailingExample: joinLines(f.FailingExample),
			Subject: Subject(f.Subject), Procedure: Procedure(f.Procedure), Pos: pos,
		}
		if f.Test != nil {
			c.Test = &Test{Runner: f.Test.Runner, Selector: f.Test.Selector, Job: f.Test.Job}
		}
		if f.TimeLimit != "" {
			limit, err := time.ParseDuration(f.TimeLimit)
			if err != nil || limit <= 0 {
				problems = append(problems, idProblem(pos, id, fmt.Sprintf("bad-time-limit: %q is not a positive Go duration", f.TimeLimit)))
			} else {
				c.TimeLimit = limit
			}
		}
		return c, problems
	case "component":
		var f componentFile
		if json.Unmarshal(d.data, &f) != nil {
			return nil, problems
		}
		return Component{ID: f.ID, Name: f.Name, Purpose: joinLines(f.Purpose), Parent: f.Parent, Interface: f.Kind == "interface", Joins: f.Joins, Pos: pos}, problems
	case "term":
		var f termFile
		if json.Unmarshal(d.data, &f) != nil {
			return nil, problems
		}
		return Term{ID: f.ID, Name: f.Name, Definition: joinLines(f.Definition), Pos: pos}, problems
	}
	return nil, problems
}

// schemaLeaves is each error of the tree under e that has no cause of its own: the places where
// the data breaks the schema.
func schemaLeaves(e *jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(e.Causes) == 0 {
		return []*jsonschema.ValidationError{e}
	}
	var leaves []*jsonschema.ValidationError
	for _, cause := range e.Causes {
		leaves = append(leaves, schemaLeaves(cause)...)
	}
	return leaves
}

// schemaDetail is "<where>: <message>" for one leaf: where is the JSON pointer of the value it
// refuses, "node" for the node itself, and the message is the validator's, except for a key that
// the schema forbids, which the validator names only "false schema".
func schemaDetail(leaf *jsonschema.ValidationError) string {
	where := jsonPointer(leaf.InstanceLocation)
	if where == "" {
		where = "node"
	}
	msg := "the node takes no such key"
	if _, forbidden := leaf.ErrorKind.(*kind.FalseSchema); !forbidden {
		msg = leaf.ErrorKind.LocalizedString(english)
	}
	return where + ": " + msg
}

// jsonPointer is the JSON pointer of a location within a document: "" for the document itself.
func jsonPointer(tokens []string) string {
	var b strings.Builder
	for _, token := range tokens {
		b.WriteString("/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(token))
	}
	return b.String()
}

// firstUnprinted is the first line of data that is not as printNode writes nodes, which the
// documents at lines hold, and the node whose document holds that line; 0 when data is exactly
// their documents. Past the end of the shorter text, the line is the one after it.
func firstUnprinted(data []byte, nodes []any, lines []int) (int, any) {
	var printed strings.Builder
	for _, node := range nodes {
		document, err := printNode(node)
		if err != nil {
			return firstLineOf(lines), node
		}
		printed.WriteString(document)
	}
	if printed.String() == string(data) {
		return 0, nil
	}
	got, want := strings.SplitAfter(string(data), "\n"), strings.SplitAfter(printed.String(), "\n")
	line := 1
	for line <= len(got) && line <= len(want) && got[line-1] == want[line-1] {
		line++
	}
	holder := 0
	for i, start := range lines {
		if start <= line {
			holder = i
		}
	}
	return line, nodes[holder]
}

// firstLineOf is the first of lines, 1 when there is none.
func firstLineOf(lines []int) int {
	if len(lines) == 0 {
		return 1
	}
	return lines[0]
}

// nodeID is the id of node, a Statement, Check, Component or Term; "" for nil.
func nodeID(node any) string {
	switch n := node.(type) {
	case Statement:
		return n.ID
	case Check:
		return n.ID
	case Component:
		return n.ID
	case Term:
		return n.ID
	}
	return ""
}

// FileSuffix is the ending of the name of a spec file.
const FileSuffix = ".yaml"

// IsFile reports whether one entry of the spec directory is a spec file: a regular file (kind
// notes.KindFile) directly in the directory, whose name ends in FileSuffix. Each reader of the
// spec files takes its entries by this rule: spec-check, through notes.ReadSet, and ReadScenario
// for the files of each commit.
func IsFile(name string, kind notes.Kind) bool {
	return kind == notes.KindFile && !strings.Contains(name, "/") && strings.HasSuffix(name, FileSuffix)
}

// ReadLayout reads the spec files, keyed by their names directly in the spec directory, as Parse
// does, and keeps each document of each file with its node and its text, so Layout.Bytes gives
// each file back byte for byte. Parse is ReadLayout and the tree of its documents, so one reader
// serves both. The problems are sorted by SortProblems.
func ReadLayout(files map[string][]byte) (Layout, []Problem) {
	l := Layout{Files: make(map[string][]Document, len(files))}
	var problems []Problem
	for _, name := range slices.Sorted(maps.Keys(files)) {
		documents, found := readFile(name, files[name])
		l.Files[name] = documents
		problems = append(problems, found...)
	}
	SortProblems(problems)
	return l, problems
}

// Parse reads the spec files, keyed by their names directly in the spec directory, and gives the
// tree they hold, each list sorted by ID then Pos, and every problem, sorted by SortProblems.
//
// A spec file holds one or more nodes, one YAML document for each. The reader decodes each
// document with yaml.v3 (bad-yaml for YAML it does not read, a key written two times, or a value
// that JSON cannot hold), checks its data against the schema of schema/spec-node.schema.json
// (schema, with the JSON pointer of the value it refuses), and reads a positive Go duration from a
// time limit (bad-time-limit). A document that the schema refuses still gives its node when its
// keys have the schema's types, so one fault gives one problem. A file with no document is
// no-node, and a file with no other problem must be exactly the documents that Print writes for
// its nodes, in its order (not-printed): the reader normalizes nothing, so one node has one
// spelling in git. A node is at the line of its document's "---".
func Parse(files map[string][]byte) (Tree, []Problem) {
	l, problems := ReadLayout(files)
	return l.tree(), problems
}

// tree is the node of each document of l, each list sorted by ID, then by Pos.
func (l Layout) tree() Tree {
	var tree Tree
	for _, name := range slices.Sorted(maps.Keys(l.Files)) {
		for _, d := range l.Files[name] {
			switch n := d.Node.(type) {
			case Statement:
				tree.Statements = append(tree.Statements, n)
			case Check:
				tree.Checks = append(tree.Checks, n)
			case Component:
				tree.Components = append(tree.Components, n)
			case Term:
				tree.Terms = append(tree.Terms, n)
			}
		}
	}
	byIDThenPos := func(aID, bID string, a, b Position) int {
		return cmp.Or(cmp.Compare(aID, bID), cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line))
	}
	slices.SortStableFunc(tree.Statements, func(a, b Statement) int { return byIDThenPos(a.ID, b.ID, a.Pos, b.Pos) })
	slices.SortStableFunc(tree.Checks, func(a, b Check) int { return byIDThenPos(a.ID, b.ID, a.Pos, b.Pos) })
	slices.SortStableFunc(tree.Components, func(a, b Component) int { return byIDThenPos(a.ID, b.ID, a.Pos, b.Pos) })
	slices.SortStableFunc(tree.Terms, func(a, b Term) int { return byIDThenPos(a.ID, b.ID, a.Pos, b.Pos) })
	return tree
}
