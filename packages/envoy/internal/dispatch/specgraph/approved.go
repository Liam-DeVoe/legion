package specgraph

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// ApprovedVersion is one text that the approved spec held for a node (R2: "Dispatch keeps each
// text that the approved spec held"): its version number, the time of the approval that wrote it,
// and Block, the node's block as Print writes it, which a change copies into the files.
type ApprovedVersion struct {
	Version int       `json:"version"`
	At      time.Time `json:"at"`
	Block   string    `json:"block"`
}

// ApprovedNode is one statement, check, component, interface or term of the approved spec, with
// each text that it held, oldest first. The newest version is its current text, unless the
// approved spec removed the node: a removed node has no current text, and its id is never used
// for another text (R1). PartlyChecked marks a statement whose checks decide only part of it, as
// the reviewer of its level judged (R10).
type ApprovedNode struct {
	ID            string            `json:"id"`
	Removed       bool              `json:"removed"`
	PartlyChecked bool              `json:"partly_checked"`
	Versions      []ApprovedVersion `json:"versions"`
}

// Approved is the approved spec (R2): each node that it holds or held, by id.
type Approved struct {
	Nodes map[string]ApprovedNode
}

// ReadApproved reads an approved spec in the form of approved.json:
//
//	{"nodes":[{"id","removed","partly_checked","versions":[{"version","at","block"}]}]}
//
// It refuses what decodeJSON refuses (a byte that is not UTF-8, a field it does not know or that is
// written in other letters, a field that one object gives two times, text after the object), a
// node with no id, an id on two nodes, a node with no version, version numbers that do not go up
// from 1, a version with no time or older than the one before it, and a block that is not its
// node's one block as Print writes it. The error names the node and the version.
func ReadApproved(r io.Reader) (Approved, error) {
	var file struct {
		Nodes []ApprovedNode `json:"nodes"`
	}
	if err := decodeJSONFrom(r, &file); err != nil {
		return Approved{}, fmt.Errorf("specgraph: approved spec: %w", err)
	}
	a := Approved{Nodes: make(map[string]ApprovedNode, len(file.Nodes))}
	for _, n := range file.Nodes {
		if n.ID == "" {
			return Approved{}, errors.New("specgraph: approved spec: a node has no id")
		}
		if _, twice := a.Nodes[n.ID]; twice {
			return Approved{}, fmt.Errorf("specgraph: approved spec: node %q is in the approved spec two times", n.ID)
		}
		if err := n.check(); err != nil {
			return Approved{}, err
		}
		a.Nodes[n.ID] = n
	}
	return a, nil
}

// check is the first fault of n that ReadApproved refuses, or nil.
func (n ApprovedNode) check() error {
	if len(n.Versions) == 0 {
		return fmt.Errorf("specgraph: approved node %q: it has no version", n.ID)
	}
	for i, v := range n.Versions {
		fault := ""
		switch {
		case v.Version < 1 || i > 0 && v.Version <= n.Versions[i-1].Version:
			fault = "the versions are 1 and more, oldest first"
		case v.At.IsZero():
			fault = "it has no time"
		case i > 0 && v.At.Before(n.Versions[i-1].At):
			fault = fmt.Sprintf("it is older than version %d", n.Versions[i-1].Version)
		default:
			if _, problems := versionText(n.ID, v); len(problems) != 0 {
				fault = fmt.Sprintf("%s (line %d of its block)", problems[0].Detail, problems[0].Pos.Line)
			}
		}
		if fault != "" {
			return fmt.Errorf("specgraph: approved node %q version %d: %s", n.ID, v.Version, fault)
		}
	}
	return nil
}

// Current is the tree of the current text of each node that the approved spec did not remove.
// Each node is at the position "approved <id> version <n>", line 1. A caller that builds an
// Approved itself, as the pilot does from the store, can give a block that ReadApproved would
// refuse: Current leaves that node out and gives each problem of its block.
func (a Approved) Current() (Tree, []Problem) {
	var tree Tree
	var problems []Problem
	for id, n := range a.Nodes {
		if n.Removed || len(n.Versions) == 0 {
			continue
		}
		text, refused := versionText(id, n.Versions[len(n.Versions)-1])
		problems = append(problems, refused...)
		tree.Statements = append(tree.Statements, text.Statements...)
		tree.Checks = append(tree.Checks, text.Checks...)
		tree.Components = append(tree.Components, text.Components...)
		tree.Terms = append(tree.Terms, text.Terms...)
	}
	slices.SortFunc(tree.Statements, func(x, y Statement) int { return cmp.Compare(x.ID, y.ID) })
	slices.SortFunc(tree.Checks, func(x, y Check) int { return cmp.Compare(x.ID, y.ID) })
	slices.SortFunc(tree.Components, func(x, y Component) int { return cmp.Compare(x.ID, y.ID) })
	slices.SortFunc(tree.Terms, func(x, y Term) int { return cmp.Compare(x.ID, y.ID) })
	SortProblems(problems)
	return tree, problems
}

// Since is the time of the approval that wrote the current text of the node id, and the zero time
// when the approved spec does not hold id or removed it.
func (a Approved) Since(id string) time.Time {
	n, held := a.Nodes[id]
	if !held || n.Removed || len(n.Versions) == 0 {
		return time.Time{}
	}
	return n.Versions[len(n.Versions)-1].At
}

// versionText is the tree of the one node that version v of the approved node id holds, at the
// position "approved <id> version <n>". problems is each reason it holds none: a line that Parse
// refuses, or bad-approved-block for a block of no node or of two, of a node with another id, or
// not as Print writes its node.
func versionText(id string, v ApprovedVersion) (Tree, []Problem) {
	name := fmt.Sprintf("approved %s version %d", id, v.Version)
	tree, problems := Parse(map[string][]byte{name: []byte(v.Block)})
	if len(problems) != 0 {
		return Tree{}, problems
	}
	refuse := func(detail string) (Tree, []Problem) {
		return Tree{}, []Problem{idProblem(Position{File: name, Line: 1}, id, "bad-approved-block: "+detail)}
	}
	nodes := nodesOf(tree)
	switch {
	case len(nodes) != 1:
		return refuse(fmt.Sprintf("the block holds %d nodes, not one", len(nodes)))
	case nodes[0].id != id:
		return refuse(fmt.Sprintf("the block holds node %q, not %q", nodes[0].id, id))
	}
	if printed, err := nodes[0].node.Print(); err != nil || printed != v.Block {
		return refuse("the block is not as Print writes it")
	}
	return tree, nil
}

// idProblem is the problem detail of the node id at pos: its rule is the word before the
// detail's first colon, and a node with no id is "-".
func idProblem(pos Position, id, detail string) Problem {
	if id == "" {
		id = "-"
	}
	rule, _, _ := strings.Cut(detail, ":")
	return Problem{Pos: pos, Node: id, Rule: rule, Detail: detail}
}

// anyNode is one node of a tree, of any of the four kinds: its id, its position, and the node.
type anyNode struct {
	id   string
	pos  Position
	node interface {
		Print() (string, error)
		Digest() string
	}
}

// nodesOf is every node of tree: its statements, checks, components, then terms, each in the
// tree's order.
func nodesOf(tree Tree) []anyNode {
	all := make([]anyNode, 0, len(tree.Statements)+len(tree.Checks)+len(tree.Components)+len(tree.Terms))
	for _, s := range tree.Statements {
		all = append(all, anyNode{s.ID, s.Pos, s})
	}
	for _, c := range tree.Checks {
		all = append(all, anyNode{c.ID, c.Pos, c})
	}
	for _, c := range tree.Components {
		all = append(all, anyNode{c.ID, c.Pos, c})
	}
	for _, t := range tree.Terms {
		all = append(all, anyNode{t.ID, t.Pos, t})
	}
	return all
}

// SortProblems sorts problems by file, line, node, rule and detail: the order of every list of
// problems that this package gives, so a caller that joins two lists sorts them the same way.
func SortProblems(problems []Problem) {
	slices.SortFunc(problems, func(a, b Problem) int {
		return cmp.Or(cmp.Compare(a.Pos.File, b.Pos.File), cmp.Compare(a.Pos.Line, b.Pos.Line), cmp.Compare(a.Node, b.Node),
			cmp.Compare(a.Rule, b.Rule), cmp.Compare(a.Detail, b.Detail))
	})
}
