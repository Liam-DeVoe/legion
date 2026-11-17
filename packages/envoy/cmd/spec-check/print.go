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
	"reflect"
	"slices"
	"strings"
	"time"

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
// specgraph.Layout.Place with the bound of one file, notes.MaxFileSize. It holds the lock of dir
// for the whole run (lockSpecDir), and waits at most printLockWait for another run that holds it.
// It writes nothing, and answers 2, with one line for each refusal on stderr: when the approved
// spec does not hold an id or removed it; when another run holds the lock after the wait; when
// dir holds a file whose documents the reader cannot say for certain, Place refuses a node, or
// the run would lose or change a node that it does not name, or would write a name that is no
// spec file of dir, or when the judgment of tree finds a problem in the files that the run would
// leave (planPrint); and for a file it cannot read or arguments it does not take. When it cannot
// write a file, it answers 2 with the files as they were, as far as the file system allows
// (writeFiles). Else it writes each file that changes, prints "wrote <id> to <dir>/<file>:<line>"
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
	return printApproved(stdout, stderr, path, a, named, *dirFlag, printBounds{limit: notes.MaxFileSize, wait: printLockWait})
}

// printLockWait is how long print waits for another run of print that holds the lock of its
// directory, before it refuses: a run takes a few seconds at most.
const printLockWait = 10 * time.Second

// printBounds are the bounds of one run of print: limit, the bound of one file, and wait, how long
// it waits for the lock of its directory. print passes notes.MaxFileSize and printLockWait; the
// tests of print pass smaller ones, so each case of the rule fits in a small directory and a test
// of the lock does not wait ten seconds.
type printBounds struct {
	limit int64
	wait  time.Duration
}

// printApproved is print once its arguments are read: it writes the current approved document of
// each node named, from the approved spec a that it read from path, into the spec files of dirArg,
// within the bounds b, and answers print's exit status.
func printApproved(stdout, stderr io.Writer, path string, a specgraph.Approved, named []string, dirArg string, b printBounds) int {
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
	dir, ok := absolute(stderr, dirArg)
	if !ok {
		return 2
	}
	lock, err := lockSpecDir(dir, b.wait)
	if err != nil {
		say(stderr, "%s: %s", dir, err)
		return 2
	}
	wrote := false
	defer func() { lock.release(wrote) }()
	set, err := readSet(stderr, dir)
	if err != nil {
		return cannotJudge(stderr, dir, err)
	}
	after, lines, refusals := planPrint(dir, set, a, named, b.limit)
	if len(refusals) > 0 {
		for _, line := range refusals {
			say(stderr, "%s", line)
		}
		return 2
	}
	if err := writeFiles(osFileSystem, dir, set.Files, after); err != nil {
		say(stderr, "%s: cannot be written: %s", dir, err)
		return 2
	}
	wrote = true
	for _, line := range lines {
		say(stdout, "%s", line)
	}
	return 0
}

// planPrint is one run of print over the spec directory dir, which set read: it places each node
// of the run in its order (printRun) with Layout.Place and limit, then judges the result. after is
// the files that the run leaves (nil when it refuses the run before the judgment); wrote is the
// line of each node it placed; refusals is each line that refuses the run, empty for a run that
// print writes. It refuses, in this order: a directory that holds a file whose documents
// ReadLayout cannot say for certain (Layout.Uncounted), each by name; a node that Place refuses;
// then, together, each name of after that is no spec file of dir (a name that IsFile refuses, or
// the name of an entry of another kind that set rejected), each node that the files held before
// the run and that after does not hold as it was, apart from the nodes of the run, each node of
// the run that after does not hold as the run writes it (keepsEachNode), and each problem that the
// judgment of tree (judgeFiles) finds in after.
func planPrint(dir string, set notes.Set, a specgraph.Approved, named []string, limit int64) (after map[string][]byte, wrote, refusals []string) {
	current, _ := a.Current() // ReadApproved refused every document that would be a problem here
	run := printRun(current, named)
	layout, _ := specgraph.ReadLayout(set.Files) // the judgment below names each problem of the files
	for _, name := range slices.Sorted(maps.Keys(layout.Uncounted)) {
		refusals = append(refusals, fmt.Sprintf("print would write into %s, and the reader cannot say which documents %s holds: %s. Correct that file, then run print again",
			dir, filepath.Join(dir, name), layout.Uncounted[name]))
	}
	if len(refusals) > 0 {
		return nil, nil, refusals
	}
	before := heldNodes(layout)
	for _, node := range run {
		if _, err := layout.Place(node, limit); err != nil {
			return nil, nil, []string{placeRefusal(dir, err)}
		}
	}
	after = layout.Bytes()
	rejected := map[string]notes.Kind{}
	for _, entry := range set.Rejected {
		rejected[entry.Path] = entry.Kind
	}
	for _, name := range slices.Sorted(maps.Keys(after)) {
		path := filepath.Join(dir, name)
		switch kind, held := rejected[path]; {
		case !specgraph.IsFile(name, notes.KindFile):
			refusals = append(refusals, fmt.Sprintf("print would write %s, which is no spec file of %s: a spec file is a *.yaml file directly in the directory", path, dir))
		case held:
			refusals = append(refusals, fmt.Sprintf("print would write %s, where %s holds a %s, not a spec file: print replaces a spec file only", path, dir, kind))
		}
	}
	j := judgeFiles(dir, after, specgraph.Options{})
	for _, err := range j.oversize {
		refusals = append(refusals, "print would leave "+err.Error())
	}
	if len(j.oversize) == 0 {
		refusals = append(refusals, keepsEachNode(dir, before, run, j.tree)...)
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

// heldNodes is each node of each document of l, by the reader of yaml.v3: the nodes that the files
// hold before a run of print.
func heldNodes(l specgraph.Layout) []any {
	var nodes []any
	for _, name := range slices.Sorted(maps.Keys(l.Files)) {
		for _, d := range l.Files[name] {
			if d.Node != nil {
				nodes = append(nodes, d.Node)
			}
		}
	}
	return nodes
}

// keepsEachNode is one line for each id whose nodes in after, the tree of the files that a run of
// print would leave, are not the nodes that the files held before the run (before) with each node
// of run in place of the nodes of its id: a node of the run that after does not hold as the run
// writes it, a node that the files held and the run does not name that after does not hold as it
// was, and a node that neither held. Nodes are the same when they are the same but for their Pos.
func keepsEachNode(dir string, before, run []any, after specgraph.Tree) []string {
	want := map[string][]any{}
	for _, node := range before {
		want[nodeID(node)] = append(want[nodeID(node)], node)
	}
	written := map[string]bool{}
	for _, node := range run {
		want[nodeID(node)] = []any{node}
		written[nodeID(node)] = true
	}
	got := map[string][]any{}
	for _, node := range treeNodes(after) {
		got[nodeID(node)] = append(got[nodeID(node)], node)
	}
	ids := map[string]bool{}
	for id := range want {
		ids[id] = true
	}
	for id := range got {
		ids[id] = true
	}
	same := func(a, b any) bool { return reflect.DeepEqual(withoutPos(a), withoutPos(b)) }
	var lines []string
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		if slices.EqualFunc(want[id], got[id], same) {
			continue
		}
		switch {
		case written[id]:
			lines = append(lines, fmt.Sprintf("print would not leave node %q as its approved document", id))
		case len(want[id]) > 0:
			pos := nodePos(want[id][0])
			lines = append(lines, fmt.Sprintf("print would lose or change node %q of %s:%d, which the run does not name", id, filepath.Join(dir, pos.File), pos.Line))
		default:
			pos := nodePos(got[id][0])
			lines = append(lines, fmt.Sprintf("print would leave node %q at %s:%d, which neither the files nor the run held", id, filepath.Join(dir, pos.File), pos.Line))
		}
	}
	return lines
}

// treeNodes is each node of tree: its statements, checks, components and terms.
func treeNodes(tree specgraph.Tree) []any {
	nodes := make([]any, 0, len(tree.Statements)+len(tree.Checks)+len(tree.Components)+len(tree.Terms))
	for _, n := range tree.Statements {
		nodes = append(nodes, n)
	}
	for _, n := range tree.Checks {
		nodes = append(nodes, n)
	}
	for _, n := range tree.Components {
		nodes = append(nodes, n)
	}
	for _, n := range tree.Terms {
		nodes = append(nodes, n)
	}
	return nodes
}

// nodePos is the position of node, a Statement, Check, Component or Term.
func nodePos(node any) specgraph.Position {
	switch n := node.(type) {
	case specgraph.Statement:
		return n.Pos
	case specgraph.Check:
		return n.Pos
	case specgraph.Component:
		return n.Pos
	case specgraph.Term:
		return n.Pos
	}
	return specgraph.Position{}
}

// withoutPos is node with no position, so two nodes compare by their data alone.
func withoutPos(node any) any {
	switch n := node.(type) {
	case specgraph.Statement:
		n.Pos = specgraph.Position{}
		return n
	case specgraph.Check:
		n.Pos = specgraph.Position{}
		return n
	case specgraph.Component:
		n.Pos = specgraph.Position{}
		return n
	case specgraph.Term:
		n.Pos = specgraph.Position{}
		return n
	}
	return node
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

// fileSystem is each step of writeFiles that can fail on its own: print passes osFileSystem, and
// a test passes one with a step that fails.
type fileSystem struct {
	createTemp func(dir, pattern string) (*os.File, error)
	rename     func(oldpath, newpath string) error
}

// osFileSystem is the file system of the process.
var osFileSystem = fileSystem{createTemp: os.CreateTemp, rename: os.Rename}

// writeFiles writes each file of after that differs from files, the files that dir holds, so that
// a run changes all of them or none, as far as the file system allows. First it writes, syncs and
// closes a temporary file of dir with the new text of each file, and one with the old text of each
// file that it replaces; then it renames each new text over its file, in name order. When a rename
// fails, it first puts back each file that it already renamed: the old text over a file that dir
// held, and no file for a new one. It removes each temporary file that it does not rename. Its
// error is the step that failed, and names each file that it could not put back.
func writeFiles(fsys fileSystem, dir string, files, after map[string][]byte) error {
	var changed []string
	for _, name := range slices.Sorted(maps.Keys(after)) {
		if old, held := files[name]; !held || !bytes.Equal(old, after[name]) {
			changed = append(changed, name)
		}
	}
	var temps []string
	defer func() {
		for _, tmp := range temps {
			_ = os.Remove(tmp) // a temporary file that a rename took is gone already
		}
	}()
	write := func(content []byte) (string, error) {
		tmp, err := fsys.createTemp(dir, ".spec-check-print-*")
		if err != nil {
			return "", err
		}
		temps = append(temps, tmp.Name())
		_, err = tmp.Write(content)
		if err == nil {
			err = tmp.Sync()
		}
		if closeErr := tmp.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Chmod(tmp.Name(), 0o644)
		}
		return tmp.Name(), err
	}
	fresh, old := map[string]string{}, map[string]string{}
	for _, name := range changed {
		tmp, err := write(after[name])
		if err != nil {
			return err
		}
		fresh[name] = tmp
		if content, held := files[name]; held {
			if old[name], err = write(content); err != nil {
				return err
			}
		}
	}
	for i, name := range changed {
		err := fsys.rename(fresh[name], filepath.Join(dir, name))
		if err == nil {
			continue
		}
		var kept []string
		for _, done := range changed[:i] {
			path := filepath.Join(dir, done)
			putBack := os.Remove
			if tmp, held := old[done]; held {
				putBack = func(path string) error { return fsys.rename(tmp, path) }
			}
			if putBack(path) != nil {
				kept = append(kept, path)
			}
		}
		if len(kept) > 0 {
			return fmt.Errorf("%w; print changed %s and could not put back the text it held", err, strings.Join(kept, ", "))
		}
		return err
	}
	return nil
}
