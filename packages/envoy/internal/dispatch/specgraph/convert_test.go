package specgraph

// This file is the converter of the spec fixtures from the block files (*.md) to the YAML files
// (*.yaml). It lives only at the commit where the old reader (Parse, Print) and the new codec
// (printNode, parseFiles) both exist; the next commit runs it with -write and deletes it. A branch
// that still holds *.md fixtures runs it from a worktree of that commit:
//
//	go test -count=1 -v -run TestConvertFixtures ./internal/dispatch/specgraph/ \
//	  -args -convert -write -dir <absolute path of a fixture directory>   # one -dir for each
//
// Without -write it converts into a temporary directory only. Before it writes anything, it holds
// the conversion to four parts: each node is the same node (every field, the position aside), each
// digest is the same, the new reader finds exactly the expected problems, and, for
// testdata/blocks, the printed file equals testdata/nodes/all-kinds.yaml byte for byte.

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var (
	convertFlag = flag.Bool("convert", false, "convert the *.md spec fixtures to YAML spec files")
	writeFlag   = flag.Bool("write", false, "write the converted files and delete the *.md files; without it, convert into a temporary directory")
	dirFlags    convertDirs
)

func init() {
	flag.Var(&dirFlags, "dir", "an absolute path of a directory to convert, with every directory and approved.json under it; give it once for each")
}

// convertDirs is the value of -dir: each directory, in the order given.
type convertDirs []string

func (d *convertDirs) String() string { return strings.Join(*d, " ") }
func (d *convertDirs) Set(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%q is not an absolute path", dir)
	}
	*d = append(*d, dir)
	return nil
}

// schemaFaults is each fixture tree of testdata/trees whose planted fault the schema now finds:
// the JSON pointer of the value it refuses and a word of the validator's message. The new reader
// finds exactly that one problem in the converted tree, and no problem in any other directory.
var schemaFaults = map[string]struct{ at, says string }{
	"check-serves-two-statements":       {"/serves", "does not match pattern"},
	"check-subject-not-allowed":         {"/subject", "value must be one of"},
	"check-without-failing-example":     {"node", "missing property 'failing_example'"},
	"component-no-purpose":              {"node", "missing property 'purpose'"},
	"interface-joins-one":               {"/joins", "minItems"},
	"program-check-without-job":         {"/test", "missing property 'job'"},
	"program-check-without-test-part":   {"node", "missing property 'test'"},
	"query-check-with-test-part":        {"/test", "the node takes no such key"},
	"statement-without-kind":            {"node", "missing property 'kind'"},
	"structure-statement-without-about": {"node", "missing property 'about'"},
}

// conversion is the converted form of one directory or of one approved.json: each file to write
// with its content, each file to delete, and one line for each node that moved.
type conversion struct {
	dir     string
	write   map[string][]byte // absolute path -> content
	remove  []string
	moves   []string
	blocks  bool // the directory is testdata/blocks, which -write never writes
	summary string
}

func TestConvertFixtures(t *testing.T) {
	if !*convertFlag {
		t.Skip("the converter runs only with -args -convert")
	}
	roots := []string(dirFlags)
	if len(roots) == 0 {
		for _, name := range []string{"blocks", "trees", "changes", "compare", "scenarios"} {
			root, err := filepath.Abs(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			roots = append(roots, root)
		}
	}
	blocksDir, err := filepath.Abs("testdata/blocks")
	if err != nil {
		t.Fatal(err)
	}
	var conversions []conversion
	for _, root := range roots {
		dirs, approved, err := fixturesUnder(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, dir := range dirs {
			c, err := convertDir(dir, dir == blocksDir)
			if err != nil {
				t.Errorf("%s: %v", dir, err)
				continue
			}
			conversions = append(conversions, c)
		}
		for _, file := range approved {
			c, err := convertApproved(file)
			if err != nil {
				t.Errorf("%s: %v", file, err)
				continue
			}
			conversions = append(conversions, c)
		}
	}
	if t.Failed() {
		t.Fatal("the conversion has a fault, so it writes nothing")
	}
	if len(conversions) == 0 {
		t.Fatalf("no *.md file and no approved.json under %q: the converter read nothing", roots)
	}
	for _, c := range conversions {
		if !*writeFlag || c.blocks {
			dry := t.TempDir()
			for path, content := range c.write {
				if err := os.WriteFile(filepath.Join(dry, filepath.Base(path)), content, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("converted (dry) %s%s", c.dir, c.summary)
			continue
		}
		for _, path := range slices.Sorted(maps.Keys(c.write)) {
			if err := os.WriteFile(path, c.write[path], 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, path := range c.remove {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		for _, move := range c.moves {
			t.Log(move)
		}
		t.Logf("converted %s%s", c.dir, c.summary)
	}
}

// fixturesUnder is each directory under root (root included) that holds a *.md file directly,
// and each approved.json under root.
func fixturesUnder(root string) (dirs, approved []string, err error) {
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case entry.Type().IsRegular() && entry.Name() == "approved.json":
			approved = append(approved, path)
		case entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".md"):
			if dir := filepath.Dir(path); !slices.Contains(dirs, dir) {
				dirs = append(dirs, dir)
			}
		}
		return nil
	})
	return dirs, approved, err
}

// convertDir converts the *.md files directly in dir: x.md becomes x.yaml, the documents of its
// nodes in the order of their blocks.
func convertDir(dir string, blocks bool) (conversion, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return conversion{}, err
	}
	old := map[string][]byte{}
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".md") {
			if old[entry.Name()], err = os.ReadFile(filepath.Join(dir, entry.Name())); err != nil {
				return conversion{}, err
			}
		}
	}
	oldTree, problems := Parse(old)
	if len(problems) != 0 {
		return conversion{}, fmt.Errorf("the block reader finds problems, so the converter cannot vouch for the nodes: %+v", problems)
	}
	c := conversion{dir: dir, write: map[string][]byte{}, blocks: blocks}
	printed := map[string][]byte{}
	nodes := nodesOf(oldTree)
	slices.SortStableFunc(nodes, func(a, b anyNode) int {
		if a.pos.File != b.pos.File {
			return strings.Compare(a.pos.File, b.pos.File)
		}
		return a.pos.Line - b.pos.Line
	})
	for _, n := range nodes {
		name := strings.TrimSuffix(n.pos.File, ".md") + ".yaml"
		if _, exists := old[name]; exists {
			return conversion{}, fmt.Errorf("%s exists beside %s", name, n.pos.File)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return conversion{}, fmt.Errorf("%s exists", name)
		}
		document, err := printNode(n.node)
		if err != nil {
			return conversion{}, fmt.Errorf("%s:%d: %s: %w", n.pos.File, n.pos.Line, n.id, err)
		}
		line := bytes.Count(printed[name], []byte("\n")) + 1
		printed[name] = append(printed[name], document...)
		c.moves = append(c.moves, fmt.Sprintf("converted %s:%d -> %s:%d %s", filepath.Join(dir, n.pos.File), n.pos.Line, name, line, n.id))
	}
	for name := range old {
		if _, held := printed[strings.TrimSuffix(name, ".md")+".yaml"]; !held {
			return conversion{}, fmt.Errorf("%s holds no node", name)
		}
		c.remove = append(c.remove, filepath.Join(dir, name))
	}
	slices.Sort(c.remove)
	for name, content := range printed {
		c.write[filepath.Join(dir, name)] = content
	}

	newTree, problems := parseFiles(printed)
	if differences := compareTrees(oldTree, newTree); len(differences) != 0 {
		return conversion{}, fmt.Errorf("part 1, the converted nodes differ: %s", strings.Join(differences, "; "))
	}
	if differences := compareDigests(oldTree, newTree); len(differences) != 0 {
		return conversion{}, fmt.Errorf("part 2, the digests differ: %s", strings.Join(differences, "; "))
	}
	if fault, planted := schemaFaults[filepath.Base(dir)]; planted {
		if len(problems) != 1 || problems[0].Rule != "schema" || !strings.HasPrefix(problems[0].Detail, "schema: "+fault.at+": ") ||
			!strings.Contains(problems[0].Detail, fault.says) {
			return conversion{}, fmt.Errorf("part 3, the new reader finds %+v, want one schema problem at %s that says %q", problems, fault.at, fault.says)
		}
		c.summary = fmt.Sprintf(": the new reader finds the planted fault %s:%d: %s: %s", problems[0].Pos.File, problems[0].Pos.Line, problems[0].Node, problems[0].Detail)
	} else if len(problems) != 0 {
		return conversion{}, fmt.Errorf("part 3, the new reader finds problems %+v, want none", problems)
	}
	if blocks {
		want, err := os.ReadFile("testdata/nodes/all-kinds.yaml")
		if err != nil {
			return conversion{}, err
		}
		if got := printed["all-kinds.yaml"]; !bytes.Equal(got, want) {
			return conversion{}, fmt.Errorf("the printed all-kinds.yaml is not testdata/nodes/all-kinds.yaml:\n%s", got)
		}
		c.summary += ": the printed all-kinds.yaml equals testdata/nodes/all-kinds.yaml byte for byte"
	}
	return c, nil
}

// approvedFile is approved.json as the converter reads it, every member in its place so the
// written file differs only in the key and the value of each version's text.
type approvedFile struct {
	Nodes []approvedFileNode `json:"nodes"`
}

type approvedFileNode struct {
	ID            string                `json:"id"`
	Removed       bool                  `json:"removed"`
	PartlyChecked bool                  `json:"partly_checked"`
	Versions      []approvedFileVersion `json:"versions"`
}

type approvedFileVersion struct {
	Version  int             `json:"version"`
	At       json.RawMessage `json:"at"`
	Block    string          `json:"block,omitempty"`
	Document string          `json:"document,omitempty"`
}

// convertApproved converts one approved.json: each version's block becomes the document that
// printNode writes for its node, after the check of parts 1 and 2 over that version.
func convertApproved(path string) (conversion, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return conversion{}, err
	}
	var file approvedFile
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return conversion{}, err
	}
	if again, err := encodeApproved(file); err != nil || !bytes.Equal(again, content) {
		return conversion{}, errors.New("the file is not in the form the converter writes (two-space indent, a final newline), so a write would change more than the texts")
	}
	for i, n := range file.Nodes {
		for j, v := range n.Versions {
			if v.Block == "" || v.Document != "" {
				return conversion{}, fmt.Errorf("node %q version %d holds no block, or holds a document already", n.ID, v.Version)
			}
			oldTree, problems := Parse(map[string][]byte{"block": []byte(v.Block)})
			if len(problems) != 0 {
				return conversion{}, fmt.Errorf("node %q version %d: the block reader finds %+v", n.ID, v.Version, problems)
			}
			nodes := nodesOf(oldTree)
			if len(nodes) != 1 {
				return conversion{}, fmt.Errorf("node %q version %d: the block holds %d nodes", n.ID, v.Version, len(nodes))
			}
			document, err := printNode(nodes[0].node)
			if err != nil {
				return conversion{}, fmt.Errorf("node %q version %d: %w", n.ID, v.Version, err)
			}
			newTree, problems := parseFiles(map[string][]byte{"document": []byte(document)})
			if len(problems) != 0 {
				return conversion{}, fmt.Errorf("node %q version %d: the new reader finds %+v", n.ID, v.Version, problems)
			}
			if differences := compareTrees(oldTree, newTree); len(differences) != 0 {
				return conversion{}, fmt.Errorf("node %q version %d: part 1: %s", n.ID, v.Version, strings.Join(differences, "; "))
			}
			if differences := compareDigests(oldTree, newTree); len(differences) != 0 {
				return conversion{}, fmt.Errorf("node %q version %d: part 2: %s", n.ID, v.Version, strings.Join(differences, "; "))
			}
			file.Nodes[i].Versions[j] = approvedFileVersion{Version: v.Version, At: v.At, Document: document}
		}
	}
	written, err := encodeApproved(file)
	if err != nil {
		return conversion{}, err
	}
	return conversion{dir: path, write: map[string][]byte{path: written}}, nil
}

// encodeApproved writes file as approved.json is written: a two-space indent, no escape of & < >,
// and a final newline.
func encodeApproved(file approvedFile) ([]byte, error) {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(file); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// compareTrees is each difference between the old tree and the converted tree: a list of another
// length, or a field of a node that differs, its position aside, as `<kind> "<id>": <Field>`.
func compareTrees(old, converted Tree) []string {
	var differences []string
	compare := func(kind string, a, b any) {
		av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
		if av.Len() != bv.Len() {
			differences = append(differences, fmt.Sprintf("%d %s nodes, want %d", bv.Len(), kind, av.Len()))
			return
		}
		for i := range av.Len() {
			x, y := reflect.New(av.Index(i).Type()).Elem(), reflect.New(bv.Index(i).Type()).Elem()
			x.Set(av.Index(i))
			y.Set(bv.Index(i))
			x.FieldByName("Pos").Set(reflect.Zero(x.FieldByName("Pos").Type()))
			y.FieldByName("Pos").Set(reflect.Zero(y.FieldByName("Pos").Type()))
			for f := range x.NumField() {
				if !reflect.DeepEqual(x.Field(f).Interface(), y.Field(f).Interface()) {
					differences = append(differences, fmt.Sprintf("%s %q: %s", kind, x.FieldByName("ID").String(), x.Type().Field(f).Name))
				}
			}
		}
	}
	compare("requirement", old.Statements, converted.Statements)
	compare("check", old.Checks, converted.Checks)
	compare("component", old.Components, converted.Components)
	compare("term", old.Terms, converted.Terms)
	return differences
}

// compareDigests is each node whose digest differs between the old tree and the converted tree,
// which compareTrees has found to hold the same nodes in the same order.
func compareDigests(old, converted Tree) []string {
	var differences []string
	a, b := nodesOf(old), nodesOf(converted)
	if len(a) != len(b) {
		return []string{fmt.Sprintf("%d nodes, want %d", len(b), len(a))}
	}
	for i := range a {
		if a[i].node.Digest() != b[i].node.Digest() {
			differences = append(differences, fmt.Sprintf("%q", a[i].id))
		}
	}
	return differences
}

// The comparator of the conversion names the node and the field of a difference: two trees whose
// nodes stand in other files, as each converted node does, and differ in one word of one
// statement's text.
func TestConvertComparatorCatchesADifference(t *testing.T) {
	t.Parallel()
	old := Tree{
		Statements: []Statement{
			{ID: "a-claim-survives-a-restart", Text: "Legion keeps each claim.", Kind: KindBehavior, Pos: Position{File: "spec.md", Line: 1}},
			{ID: "legion-keeps-each-claim", Text: "Legion keeps each claim in Dispatch.", Kind: KindBehavior, Pos: Position{File: "spec.md", Line: 5}},
		},
		Terms: []Term{{ID: "work-unit", Name: "work unit", Definition: "A work unit is work.", Pos: Position{File: "spec.md", Line: 9}}},
	}
	converted := Tree{
		Statements: []Statement{
			{ID: "a-claim-survives-a-restart", Text: "Legion keeps each stored claim.", Kind: KindBehavior, Pos: Position{File: "spec.yaml", Line: 1}},
			{ID: "legion-keeps-each-claim", Text: "Legion keeps each claim in Dispatch.", Kind: KindBehavior, Pos: Position{File: "spec.yaml", Line: 6}},
		},
		Terms: []Term{{ID: "work-unit", Name: "work unit", Definition: "A work unit is work.", Pos: Position{File: "spec.yaml", Line: 11}}},
	}
	if got, want := compareTrees(old, converted), []string{`requirement "a-claim-survives-a-restart": Text`}; !slices.Equal(got, want) {
		t.Errorf("compareTrees = %q, want %q", got, want)
	}
	if got := compareTrees(old, old); len(got) != 0 {
		t.Errorf("compareTrees of a tree and itself = %q, want none", got)
	}
}
