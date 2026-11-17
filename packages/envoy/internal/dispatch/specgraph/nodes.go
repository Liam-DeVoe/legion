package specgraph

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// ErrUnprintable is Print's refusal of a node that no document can hold as the same node: its
// document would read back as another node, or would not be in the form of the files. The error
// that wraps it names the node and the field.
var ErrUnprintable = errors.New("unprintable")

// Print writes the statement as its YAML document, from its "---" line to its final line end,
// which Parse reads back as the statement.
func (s Statement) Print() (string, error) { return printNode(s) }

// Print writes the check as its YAML document, which Parse reads back as the check.
func (c Check) Print() (string, error) { return printNode(c) }

// Print writes the component or the interface as its YAML document, which Parse reads back as it.
func (c Component) Print() (string, error) { return printNode(c) }

// Print writes the term as its YAML document, which Parse reads back as the term.
func (t Term) Print() (string, error) { return printNode(t) }

// The file structs are the data of one YAML document of a spec file, one struct for each type of
// node, with the keys of the schema (schema/spec-node.schema.json) in the order the document holds
// them: type and id first, the texts last. A text is a list of its lines. An absent value is an
// absent key, so no document holds an empty string, an empty list or a null.
type (
	requirementFile struct {
		Type   string   `json:"type" yaml:"type"`
		ID     string   `json:"id" yaml:"id"`
		Kind   string   `json:"kind,omitempty" yaml:"kind,omitempty"`
		Parent string   `json:"parent,omitempty" yaml:"parent,omitempty"`
		Serves []string `json:"serves,omitempty" yaml:"serves,omitempty"`
		About  []string `json:"about,omitempty" yaml:"about,omitempty"`
		Text   []string `json:"text,omitempty" yaml:"text,omitempty"`
	}
	testFile struct {
		Runner   string `json:"runner,omitempty" yaml:"runner,omitempty"`
		Selector string `json:"selector,omitempty" yaml:"selector,omitempty"`
		Job      string `json:"job,omitempty" yaml:"job,omitempty"`
	}
	checkFile struct {
		Type           string    `json:"type" yaml:"type"`
		ID             string    `json:"id" yaml:"id"`
		Serves         string    `json:"serves,omitempty" yaml:"serves,omitempty"`
		Subject        string    `json:"subject,omitempty" yaml:"subject,omitempty"`
		Procedure      string    `json:"procedure,omitempty" yaml:"procedure,omitempty"`
		Test           *testFile `json:"test,omitempty" yaml:"test,omitempty"`
		TimeLimit      string    `json:"time_limit,omitempty" yaml:"time_limit,omitempty"`
		Claim          string    `json:"claim,omitempty" yaml:"claim,omitempty"`
		ProcedureText  []string  `json:"procedure_text,omitempty" yaml:"procedure_text,omitempty"`
		FailingExample []string  `json:"failing_example,omitempty" yaml:"failing_example,omitempty"`
	}
	componentFile struct {
		Type    string   `json:"type" yaml:"type"`
		ID      string   `json:"id" yaml:"id"`
		Name    string   `json:"name,omitempty" yaml:"name,omitempty"`
		Kind    string   `json:"kind,omitempty" yaml:"kind,omitempty"` // "component" or "interface"
		Parent  string   `json:"parent,omitempty" yaml:"parent,omitempty"`
		Joins   []string `json:"joins,omitempty" yaml:"joins,omitempty"`
		Purpose []string `json:"purpose,omitempty" yaml:"purpose,omitempty"`
	}
	termFile struct {
		Type       string   `json:"type" yaml:"type"`
		ID         string   `json:"id" yaml:"id"`
		Name       string   `json:"name,omitempty" yaml:"name,omitempty"`
		Definition []string `json:"definition,omitempty" yaml:"definition,omitempty"`
	}
)

// printNode is the document of n, a Statement, Check, Component or Term: "---\n", then the file
// struct of n written by a yaml.v3 Encoder with SetIndent(2). Lists are sorted. A text is split at
// "\n" into its lines; "" is no key. A scalar that holds U+2028 or U+2029 is written double-quoted,
// as \L or \P: yaml.v3 reads those two as line ends (YAML 1.1) and writes them as a line break in
// a single-quoted scalar, which a YAML 1.2 reader, such as the TypeScript yaml library, reads as
// the character and the indentation after it. The error wraps ErrUnprintable and names the node
// and the field: an empty id, a string that is not UTF-8 or that holds NUL or CR, a line of a
// one-line field that holds LF, a text with an empty line, a test part that names nothing, or a
// negative time limit.
func printNode(n any) (string, error) {
	file, p := fileOf(n)
	if p == nil {
		return "", fmt.Errorf("%w: %T is no node", ErrUnprintable, n)
	}
	return p.document(file)
}

// fileOf is the file struct of n, the data of its document, and the printer that checked its
// values; nil for a value that is no node.
func fileOf(n any) (any, *nodePrinter) {
	switch n := n.(type) {
	case Statement:
		p := newNodePrinter("requirement", n.ID)
		return requirementFile{
			Type: "requirement", ID: n.ID, Kind: p.line("kind", string(n.Kind)), Parent: p.line("parent", n.Parent),
			Serves: p.ids("serves", n.Serves), About: p.ids("about", n.About), Text: p.lines("text", n.Text),
		}, p
	case Check:
		p := newNodePrinter("check", n.ID)
		f := checkFile{
			Type: "check", ID: n.ID, Serves: p.line("serves", n.Serves), Subject: p.line("subject", string(n.Subject)),
			Procedure: p.line("procedure", string(n.Procedure)), Claim: p.line("claim", n.Claim),
			ProcedureText: p.lines("procedure_text", n.ProcedureText), FailingExample: p.lines("failing_example", n.FailingExample),
		}
		if n.Test != nil {
			if *n.Test == (Test{}) {
				p.refuse("test", "a test part names a runner, a selector or a job")
			}
			f.Test = &testFile{Runner: p.line("runner", n.Test.Runner), Selector: p.line("selector", n.Test.Selector), Job: p.line("job", n.Test.Job)}
		}
		switch {
		case n.TimeLimit > 0:
			f.TimeLimit = n.TimeLimit.String()
		case n.TimeLimit < 0:
			p.refuse("time_limit", "a time limit is a positive duration")
		}
		return f, p
	case Component:
		p := newNodePrinter("component", n.ID)
		kind := "component"
		if n.Interface {
			kind = "interface"
		}
		return componentFile{
			Type: "component", ID: n.ID, Name: p.line("name", n.Name), Kind: kind, Parent: p.line("parent", n.Parent),
			Joins: p.ids("joins", n.Joins), Purpose: p.lines("purpose", n.Purpose),
		}, p
	case Term:
		p := newNodePrinter("term", n.ID)
		return termFile{Type: "term", ID: n.ID, Name: p.line("name", n.Name), Definition: p.lines("definition", n.Definition)}, p
	}
	return nil, nil
}

// nodePrinter checks the values of one node as printNode writes them, and keeps the first refusal.
type nodePrinter struct {
	kind, id string
	err      error
}

func newNodePrinter(kind, id string) *nodePrinter {
	p := &nodePrinter{kind: kind, id: id}
	if id == "" {
		p.refuse("id", "a node has an id")
	}
	p.line("id", id)
	return p
}

// refuse records that field cannot be printed, for the reason given, when no earlier field was.
func (p *nodePrinter) refuse(field, reason string) {
	if p.err == nil {
		p.err = fmt.Errorf("%w: %s %q, %s: %s", ErrUnprintable, p.kind, p.id, field, reason)
	}
}

// line is value as one scalar of the document, refusing a value that is not UTF-8 or that holds
// NUL, CR or LF.
func (p *nodePrinter) line(field, value string) string {
	switch {
	case !utf8.ValidString(value):
		p.refuse(field, "a text is UTF-8")
	case strings.ContainsRune(value, 0):
		p.refuse(field, "a text holds no NUL byte")
	case strings.ContainsRune(value, '\r'):
		p.refuse(field, "a text holds no carriage return")
	case strings.ContainsRune(value, '\n'):
		p.refuse(field, "it is one line")
	}
	return value
}

// lines is the lines of a text, nil for "", refusing a text with an empty line.
func (p *nodePrinter) lines(field, text string) []string {
	lines := splitLines(text)
	for _, line := range lines {
		if line == "" {
			p.refuse(field, "a text holds no empty line")
		}
		p.line(field, line)
	}
	return lines
}

// ids is a list of ids, sorted, refusing an empty id.
func (p *nodePrinter) ids(field string, ids []string) []string {
	sorted := slices.Sorted(slices.Values(ids))
	for _, id := range sorted {
		if id == "" {
			p.refuse(field, "a list holds no empty id")
		}
		p.line(field, id)
	}
	return sorted
}

// document writes file as one YAML document, or answers the first refusal. Only a document that
// holds U+2028 or U+2029 takes the way through a yaml.Node, where its scalars get their style.
func (p *nodePrinter) document(file any) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	text, err := encodeYAML(file)
	if err == nil && strings.ContainsAny(text, "\u2028\u2029") {
		var node yaml.Node
		if err = node.Encode(file); err == nil {
			quoteLineSeparators(&node)
			text, err = encodeYAML(&node)
		}
	}
	if err != nil {
		return "", fmt.Errorf("%w: %s %q: %v", ErrUnprintable, p.kind, p.id, err)
	}
	return "---\n" + text, nil
}

// encodeYAML is v written by a yaml.v3 Encoder with SetIndent(2).
func encodeYAML(v any) (string, error) {
	var b strings.Builder
	encoder := yaml.NewEncoder(&b)
	encoder.SetIndent(2)
	if err := encoder.Encode(v); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}

// quoteLineSeparators makes each scalar under node that holds U+2028 or U+2029 double-quoted.
func quoteLineSeparators(node *yaml.Node) {
	if node.Kind == yaml.ScalarNode && strings.ContainsAny(node.Value, "\u2028\u2029") {
		node.Style = yaml.DoubleQuotedStyle
	}
	for _, child := range node.Content {
		quoteLineSeparators(child)
	}
}

// splitLines is the lines of text, split at "\n"; nil for "".
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// joinLines is the text of lines, joined with "\n".
func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}
