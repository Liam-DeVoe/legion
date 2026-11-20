package specgraph

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// scenarioFS is each file of the scenario at dir, by its path in the scenario.
func scenarioFS(t *testing.T, dir string) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		fsys[filepath.ToSlash(rel)] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return fsys
}

// editTree changes the JSON file name of fsys by change, which takes its tree and gives the new one,
// and reads the file back by its name, with each other file unchanged (filesRead); the value that
// change writes is the caller's to read back.
func editTree(t *testing.T, fsys fstest.MapFS, name string, change func(any) any) {
	t.Helper()
	var tree any
	if err := json.Unmarshal(fsys[name].Data, &tree); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	before := maps.Clone(fsys)
	data := encodeTree(t, change(tree))
	fsys[name] = &fstest.MapFile{Data: data}
	filesRead(t, before, fsys, map[string][]byte{name: data}, nil)
}

// filesRead fails the test unless fsys, which a writer of a sweep made from a copy of base, holds
// what the sweep means it to hold, which the caller gives apart from the writer's call: each file of
// written with its bytes, or no such file where they are nil, which takes the file out; each JSON
// file of changed as the tree of the base's file with its one change (treeChange.on), as the reader
// decodes it, compared whole; each other file of base with the base's bytes; and no other file. So
// a writer that writes under another file name, writes another value, writes it at another place
// or under another key, or changes another place or another file too, fails by name.
func filesRead(t *testing.T, base, fsys fstest.MapFS, written map[string][]byte, changed map[string]treeChange) {
	t.Helper()
	meant := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(written)) {
		meant[name] = true
		file, held := fsys[name]
		switch data := written[name]; {
		case data == nil && held:
			t.Fatalf("the sweep took %s out, and the scenario holds it", name)
		case data != nil && !held:
			t.Fatalf("the sweep wrote %s, and the scenario holds no file of that name", name)
		case data != nil && !bytes.Equal(file.Data, data):
			t.Fatalf("the sweep wrote %d bytes to %s, and the scenario holds %d other bytes there", len(data), name, len(file.Data))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(changed)) {
		meant[name] = true
		change := changed[name]
		file, held := fsys[name]
		if !held {
			t.Fatalf("the sweep changed %s at %q, and the scenario holds no file of that name", name, treePointer(change.place))
		}
		var got any
		if err := json.Unmarshal(file.Data, &got); err != nil {
			t.Fatalf("the sweep changed %s at %q, and the file is no JSON: %v", name, treePointer(change.place), err)
		}
		want := change.on(t, fileTree(t, base, name))
		if at, differ := treeDiff(got, want, nil); differ {
			x, xHeld := treeAt(got, at)
			w, wHeld := treeAt(want, at)
			t.Fatalf("the sweep changed %s at %q, and the file differs from that change at %q: it holds %s there (held %t), where it should hold %s (held %t)",
				name, treePointer(change.place), treePointer(at), encodeTree(t, x), xHeld, encodeTree(t, w), wHeld)
		}
	}
	names := slices.Sorted(maps.Keys(meant))
	for _, name := range slices.Sorted(maps.Keys(fsys)) {
		if meant[name] {
			continue
		}
		if was, held := base[name]; !held || !bytes.Equal(fsys[name].Data, was.Data) {
			t.Fatalf("the sweep wrote %q, and the scenario holds %s, which the base does not hold as it is (held %t)", names, name, held)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(base)) {
		if _, held := fsys[name]; !meant[name] && !held {
			t.Fatalf("the sweep wrote %q, and the scenario no longer holds %s", names, name)
		}
	}
}

// treeChange is one change of a JSON tree as a sweep means it, which the sweep gives apart from the
// writer that makes it, for the read back of the write (filesRead): at place, the value value, null
// for nil; or, with out, the member or element taken out; or, with renamed, the member under the
// name that value holds.
type treeChange struct {
	place        []string
	value        any
	out, renamed bool
}

// on is the JSON tree v, which the caller decoded for it alone, with the change c made in place, its
// value as the reader decodes it (a number as a float64). It shares no code of the package with the
// writers of the sweeps (treeWith, treeChanged and treeRenamed, which copy the tree on the path that
// they change), so that a writer that also writes at another place fails the read back.
func (c treeChange) on(t *testing.T, v any) any {
	t.Helper()
	if !c.renamed {
		var x any
		data, err := json.Marshal(c.value)
		if err == nil {
			err = json.Unmarshal(data, &x)
		}
		if err != nil {
			t.Fatal(err)
		}
		c.value = x
	}
	if len(c.place) == 0 && !c.out && !c.renamed {
		return c.value
	}
	if len(c.place) > 0 {
		parent, _ := treeAt(v, c.place[:len(c.place)-1])
		last := c.place[len(c.place)-1]
		switch p := parent.(type) {
		case map[string]any:
			x, held := p[last]
			switch {
			case !c.out && !c.renamed:
				p[last] = c.value
				return v
			case held:
				delete(p, last)
				if c.renamed {
					p[c.value.(string)] = x
				}
				return v
			}
		case []any:
			if i, err := strconv.Atoi(last); err == nil && i >= 0 && i < len(p) && !c.renamed {
				if !c.out {
					p[i] = c.value
					return v
				}
				return treeChange{place: c.place[:len(c.place)-1], value: slices.Delete(p, i, i+1)}.on(t, v)
			}
		}
	}
	t.Fatalf("the sweep means a change at %q, where the base holds no member or element", treePointer(c.place))
	return nil
}

// treeDiff is the first place, by the order of the names, where the JSON trees a and b differ (a
// member that one holds and the other does not, a list of another length, a value of another type
// or another value), and whether they differ.
func treeDiff(a, b any, at []string) ([]string, bool) {
	switch x := a.(type) {
	case map[string]any:
		y, object := b.(map[string]any)
		if !object {
			return at, true
		}
		names := slices.Collect(maps.Keys(x))
		for name := range y {
			if _, held := x[name]; !held {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		for _, name := range names {
			xv, inA := x[name]
			yv, inB := y[name]
			place := append(slices.Clone(at), name)
			if !inA || !inB {
				return place, true
			}
			if p, differ := treeDiff(xv, yv, place); differ {
				return p, true
			}
		}
		return nil, false
	case []any:
		y, list := b.([]any)
		if !list || len(x) != len(y) {
			return at, true
		}
		for i := range x {
			if p, differ := treeDiff(x[i], y[i], append(slices.Clone(at), strconv.Itoa(i))); differ {
				return p, true
			}
		}
		return nil, false
	}
	return at, !reflect.DeepEqual(a, b)
}

// changedFile is a copy of base with the JSON file name written as the tree written, which a writer
// of a sweep made, read back against want, the change that the sweep means, which the caller gives
// apart from the writer's call (filesRead).
func changedFile(t *testing.T, base fstest.MapFS, name string, written any, want treeChange) fstest.MapFS {
	t.Helper()
	fsys := maps.Clone(base)
	fsys[name] = &fstest.MapFile{Data: encodeTree(t, written)}
	filesRead(t, base, fsys, nil, map[string]treeChange{name: want})
	return fsys
}

// fileTree is the JSON tree of the file name of fsys, decoded anew for its caller.
func fileTree(t *testing.T, fsys fstest.MapFS, name string) any {
	t.Helper()
	var tree any
	if err := json.Unmarshal(fsys[name].Data, &tree); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return tree
}

// placeMatches is whether the place at is the place pattern, whose "*" stands for any one name.
func placeMatches(pattern, at []string) bool {
	if len(pattern) != len(at) {
		return false
	}
	for i, name := range pattern {
		if name != "*" && name != at[i] {
			return false
		}
	}
	return true
}

// treeChanged is a copy of the JSON tree v with the value at place null, or, with leftOut, taken
// out of its object or its list.
func treeChanged(v any, place []string, leftOut bool) any {
	if len(place) == 0 {
		return nil
	}
	switch v := v.(type) {
	case map[string]any:
		c := maps.Clone(v)
		if len(place) == 1 && leftOut {
			delete(c, place[0])
		} else {
			c[place[0]] = treeChanged(v[place[0]], place[1:], leftOut)
		}
		return c
	case []any:
		i, _ := strconv.Atoi(place[0])
		if len(place) == 1 && leftOut {
			return slices.Delete(slices.Clone(v), i, i+1)
		}
		c := slices.Clone(v)
		c[i] = treeChanged(v[i], place[1:], leftOut)
		return c
	}
	panic(fmt.Sprintf("no value at %q", place))
}

// treeWith is a copy of the JSON tree v with x at place.
func treeWith(v any, place []string, x any) any {
	if len(place) == 0 {
		return x
	}
	switch v := v.(type) {
	case map[string]any:
		c := maps.Clone(v)
		c[place[0]] = treeWith(v[place[0]], place[1:], x)
		return c
	case []any:
		i, _ := strconv.Atoi(place[0])
		c := slices.Clone(v)
		c[i] = treeWith(v[i], place[1:], x)
		return c
	}
	panic(fmt.Sprintf("no value at %q", place))
}

// treeAt is the value of the JSON tree v at place, and whether v holds one there.
func treeAt(v any, place []string) (any, bool) {
	for _, name := range place {
		switch o := v.(type) {
		case map[string]any:
			x, held := o[name]
			if !held {
				return nil, false
			}
			v = x
		case []any:
			i, err := strconv.Atoi(name)
			if err != nil || i < 0 || i >= len(o) {
				return nil, false
			}
			v = o[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// treeRenamed is a copy of the JSON tree v with the member at place, the last name of place, under
// the name renamed in its object.
func treeRenamed(v any, place []string, renamed string) any {
	object := v
	for _, name := range place[:len(place)-1] {
		switch o := object.(type) {
		case map[string]any:
			object = o[name]
		case []any:
			i, _ := strconv.Atoi(name)
			object = o[i]
		}
	}
	c := maps.Clone(object.(map[string]any))
	c[renamed] = c[place[len(place)-1]]
	delete(c, place[len(place)-1])
	return treeWith(v, place[:len(place)-1], c)
}

// treePointer is the JSON pointer of place.
func treePointer(place []string) string {
	var b strings.Builder
	for _, name := range place {
		b.WriteString("/" + pointerToken.Replace(name))
	}
	return b.String()
}

// encodeTree is the JSON text of the tree v.
func encodeTree(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
