package main

import (
	"bytes"
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/architecture/notes"
	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
)

// ids is the value of a flag that can be given more than once, each value in the order given.
type ids []string

func (v *ids) String() string      { return strings.Join(*v, " ") }
func (v *ids) Set(id string) error { *v = append(*v, id); return nil }

// runPrint is "spec-check print --approved <approved.json> --id <id> [--id <id>...] --dir <dir>":
// it writes the current approved document of each node named, and of each current check that
// serves a statement named, into the spec files of dir, each node at its place by
// specgraph.Layout.Place with the bound of one file, notes.MaxFileSize. It writes nothing, and
// answers 2, when Place refuses a node or when the judgment of tree finds a problem in the files
// that the run would leave (planPrint), with one line for each refusal on stderr; when the approved
// spec does not hold an id or removed it; and for a file it cannot read or write or arguments it
// does not take. Else it writes each file that changes, prints "wrote <id> to <dir>/<file>:<line>"
// for each node of the run, and answers 0.
//
// The documents do not pass through runnerlog.Say: they go into the files byte for byte (R2: "A
// change copies that output into the files, and no agent types it"). Each is its node's document
// as the approved spec holds it, which ReadApproved has checked is the document Print writes.
func runPrint(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("print", flag.ContinueOnError)
	approvedFlag := flags.String("approved", "", "the approved spec, in the form of approved.json")
	var named ids
	flags.Var(&named, "id", "a node of the approved spec to print; give it once for each node")
	dirFlag := flags.String("dir", "", "the directory of spec files that print writes the nodes into")
	if code, ok := parse(flags, args, stderr, "approved", "id", "dir"); !ok {
		return code
	}
	path, a, ok := readApproved(stderr, *approvedFlag)
	if !ok {
		return 2
	}
	refused := false
	for _, id := range named {
		switch n, held := a.Nodes[id]; {
		case !held:
			say(stderr, "%s: the approved spec holds no node %q", path, id)
			refused = true
		case n.Removed:
			say(stderr, "%s: the approved spec removed node %q, so it has no current text", path, id)
			refused = true
		}
	}
	if refused {
		return 2
	}
	dir, ok := absolute(stderr, *dirFlag)
	if !ok {
		return 2
	}
	files, err := readFiles(stderr, dir)
	switch {
	case errors.Is(err, notes.ErrNoDirectory):
		// Git keeps no empty directory, so print writes the first spec file of a repository into a
		// directory that it makes.
		files = map[string][]byte{}
	case err != nil:
		return cannotJudge(stderr, dir, err)
	}

	after, wrote, refusals := planPrint(dir, files, a, named, notes.MaxFileSize)
	if len(refusals) > 0 {
		for _, line := range refusals {
			say(stderr, "%s", line)
		}
		return 2
	}
	if err := writeFiles(dir, files, after); err != nil {
		say(stderr, "%s: cannot be written: %s", dir, err)
		return 2
	}
	for _, line := range wrote {
		say(stdout, "%s", line)
	}
	return 0
}

// planPrint is one run of print over the files of dir: it places each node of the run in its order
// (printRun) with Layout.Place and limit, then judges the result with judgeFiles. after is the
// files that the run leaves (nil when Place refused a node); wrote is the line of each node it
// placed; refusals is each line that refuses the run, empty for a run that print writes.
func planPrint(dir string, files map[string][]byte, a specgraph.Approved, ids []string, limit int64) (after map[string][]byte, wrote, refusals []string) {
	current, _ := a.Current() // ReadApproved refused every document that would be a problem here
	run := printRun(current, ids)
	layout, _ := specgraph.ReadLayout(files) // the judgment below names each problem of the files
	for _, node := range run {
		if _, err := layout.Place(node, limit); err != nil {
			return nil, nil, []string{placeRefusal(dir, err)}
		}
	}
	after = layout.Bytes()
	j := judgeFiles(dir, after, specgraph.Options{})
	for _, err := range j.oversize {
		refusals = append(refusals, "print would leave "+err.Error())
	}
	for _, p := range j.problems {
		refusals = append(refusals, fmt.Sprintf("print would leave %s:%d: %s: %s", filepath.Join(dir, p.Pos.File), p.Pos.Line, p.Node, p.Detail))
	}
	if len(refusals) > 0 {
		return after, nil, refusals
	}
	at := map[string]specgraph.Position{}
	for _, s := range j.tree.Statements {
		at[s.ID] = s.Pos
	}
	for _, c := range j.tree.Checks {
		at[c.ID] = c.Pos
	}
	for _, c := range j.tree.Components {
		at[c.ID] = c.Pos
	}
	for _, t := range j.tree.Terms {
		at[t.ID] = t.Pos
	}
	for _, node := range run {
		id := nodeID(node)
		wrote = append(wrote, fmt.Sprintf("wrote %s to %s:%d", id, filepath.Join(dir, at[id].File), at[id].Line))
	}
	return after, wrote, nil
}

// placeRefusal is the line that refuses a run of print for the refusal err of Place.
func placeRefusal(dir string, err error) string {
	var refused *specgraph.PlaceError
	if !errors.As(err, &refused) {
		return "print would write a node that no document holds: " + err.Error()
	}
	switch refused.Err {
	case specgraph.ErrNoStatement:
		return fmt.Sprintf("print would write check %q, whose statement %q %s does not hold: check-serves: a check serves exactly one statement of the files", refused.Node, refused.Needs, dir)
	case specgraph.ErrNoParent:
		return fmt.Sprintf("print would write statement %q, whose parent %q %s does not hold: no-parent: the files hold no statement %q", refused.Node, refused.Needs, dir, refused.Needs)
	case specgraph.ErrHeldTwice:
		return fmt.Sprintf("print would write node %q, which %s and %s both hold", refused.Node, filepath.Join(dir, refused.Places[0]), filepath.Join(dir, refused.Places[1]))
	default:
		return fmt.Sprintf("print would write %s %q, whose block is %d bytes, more than one file holds (limit %d)", refused.Kind, refused.Node, refused.Size, refused.Limit)
	}
}

// printRun is the nodes of one run of print, from the current approved tree: each node named and,
// for a named statement, each current check that serves it, each once. Their order: the components
// and interfaces, by their depth in the tree of components, then by id; then the terms, by id; then
// the statements, by their depth in the tree of statements, then by id, each followed by its checks
// of the run, by id, also when the run does not hold the statement. So each parent comes before
// its children and each statement before its checks.
func printRun(current specgraph.Tree, named []string) []any {
	type entry struct {
		group, depth int
		key, sub     string
		node         any
	}
	depth := func(id string, parent func(string) (string, bool)) int {
		n := 0
		seen := map[string]bool{}
		for next, ok := parent(id); ok && next != "" && !seen[next]; next, ok = parent(next) {
			seen[next] = true
			n++
		}
		return n
	}
	statementParent := func(id string) (string, bool) {
		s, ok := current.StatementByID(id)
		return s.Parent, ok
	}
	componentParent := func(id string) (string, bool) {
		c, ok := current.ComponentByID(id)
		return c.Parent, ok
	}
	var run []entry
	added := map[string]bool{}
	add := func(id string) {
		if added[id] {
			return
		}
		added[id] = true
		if s, ok := current.StatementByID(id); ok {
			run = append(run, entry{2, depth(id, statementParent), id, "", s})
		} else if c, ok := current.CheckByID(id); ok {
			run = append(run, entry{2, depth(c.Serves, statementParent), c.Serves, id, c})
		} else if c, ok := current.ComponentByID(id); ok {
			run = append(run, entry{0, depth(id, componentParent), id, "", c})
		} else if i := slices.IndexFunc(current.Terms, func(t specgraph.Term) bool { return t.ID == id }); i >= 0 {
			run = append(run, entry{1, 0, id, "", current.Terms[i]})
		}
	}
	for _, id := range named {
		add(id)
		if _, statement := current.StatementByID(id); statement {
			for _, c := range current.ChecksOf(id) {
				add(c.ID)
			}
		}
	}
	slices.SortFunc(run, func(a, b entry) int {
		return cmp.Or(cmp.Compare(a.group, b.group), cmp.Compare(a.depth, b.depth), cmp.Compare(a.key, b.key), cmp.Compare(a.sub, b.sub))
	})
	nodes := make([]any, len(run))
	for i, e := range run {
		nodes[i] = e.node
	}
	return nodes
}

// nodeID is the id of a node of a run.
func nodeID(node any) string {
	switch n := node.(type) {
	case specgraph.Statement:
		return n.ID
	case specgraph.Check:
		return n.ID
	case specgraph.Component:
		return n.ID
	case specgraph.Term:
		return n.ID
	}
	return ""
}

// writeFiles writes each file of after that differs from files, through a temporary file of dir
// and a rename, so a reader never sees half a file; it makes dir when it does not exist.
func writeFiles(dir string, files, after map[string][]byte) error {
	for _, name := range slices.Sorted(maps.Keys(after)) {
		if old, held := files[name]; held && bytes.Equal(old, after[name]) {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(dir, ".spec-check-print-*")
		if err != nil {
			return err
		}
		_, err = tmp.Write(after[name])
		if closeErr := tmp.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Chmod(tmp.Name(), 0o644)
		}
		if err == nil {
			err = os.Rename(tmp.Name(), filepath.Join(dir, name))
		}
		if err != nil {
			_ = os.Remove(tmp.Name())
			return err
		}
	}
	return nil
}
