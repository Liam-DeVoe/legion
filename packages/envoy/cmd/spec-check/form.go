package main

import (
	"slices"
	"strconv"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/specform"
	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
)

// formContext is what tree knows of the system when it judges the form: the emails of the people
// who signed in (--people), the project keys (--projects) and the address of the Dispatch server
// (--server-url). No field that specform.Check reads is nil: with no --people the list is empty,
// with no --projects no key is a project, and with no --server-url no full address is a link.
func formContext(people, projects []string, serverURL string) specform.Context {
	if people == nil {
		people = []string{}
	}
	return specform.Context{
		PersonNames:  people,
		IsProjectKey: func(key string) bool { return slices.Contains(projects, key) },
		ServerURL:    serverURL,
	}
}

// formInput is the texts of t as specform.Check judges them: the text of each statement, the
// claim, the procedure and the failing example of each check, and the name and the purpose of each
// component, each with the id of its block as its node; and each term as a proposed definition.
// Where is "<file>:<line>" of the block. A Body is the text as the file holds it, links kept, its
// lines joined with "\n" as the server's import passes them, and Check reads it as one line. An
// empty text is left out: Validate already refuses each one (no-text, no-claim, no-procedure,
// no-failing-example, bad-component and bad-term), and the form would name it again. List is
// empty, as the files are the whole list of terms on an import.
func formInput(t specgraph.Tree) specform.Input {
	var in specform.Input
	add := func(node string, kind specform.Kind, body string, pos specgraph.Position) {
		if body != "" {
			in.Texts = append(in.Texts, specform.Text{Node: node, Kind: kind, Body: body, Where: where(pos)})
		}
	}
	for _, s := range t.Statements {
		add(s.ID, specform.KindStatement, s.Text, s.Pos)
	}
	for _, c := range t.Checks {
		add(c.ID, specform.KindClaim, c.Claim, c.Pos)
		add(c.ID, specform.KindProcedure, c.ProcedureText, c.Pos)
		add(c.ID, specform.KindFailingExample, c.FailingExample, c.Pos)
	}
	for _, c := range t.Components {
		add(c.ID, specform.KindName, c.Name, c.Pos)
		add(c.ID, specform.KindPurpose, c.Purpose, c.Pos)
	}
	for _, term := range t.Terms {
		if term.Definition != "" {
			in.Proposed = append(in.Proposed, specform.Term{Name: term.Name, Definition: term.Definition, Where: where(term.Pos)})
		}
	}
	return in
}

// where is a text's Where: the file and the line of its block.
func where(pos specgraph.Position) string {
	return pos.File + ":" + strconv.Itoa(pos.Line)
}

// formProblems is each finding of specform.Check over the texts of t as a problem of the tree: at
// the file and the line of its block, with the finding's node ("-" for none), its rule, and a
// detail that begins with the rule, as Validate writes one. The node of a definition is the name
// of its term, as Dispatch's refusal names it, and the node of every other text is its block's id.
func formProblems(c specform.Context, t specgraph.Tree) []specgraph.Problem {
	findings := specform.Check(c, formInput(t))
	problems := make([]specgraph.Problem, 0, len(findings))
	for _, f := range findings {
		// Where is where(pos), so the line follows its last colon, which a file name may also hold.
		at := strings.LastIndexByte(f.Where, ':')
		line, _ := strconv.Atoi(f.Where[at+1:])
		node := f.Node
		if node == "" {
			node = "-"
		}
		problems = append(problems, specgraph.Problem{
			Pos: specgraph.Position{File: f.Where[:at], Line: line}, Node: node, Rule: string(f.Rule), Detail: string(f.Rule) + ": " + f.Detail,
		})
	}
	return problems
}
