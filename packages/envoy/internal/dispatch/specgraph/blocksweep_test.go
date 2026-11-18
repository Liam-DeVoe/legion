package specgraph

import (
	"encoding/json/v2"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// A text inside a block that names nothing at one place: the id or a value of an attribute of the
// block's opening line, or a line of its body after the marker of a field of a check (blockTexts),
// in a block of approved.json or of a spec file of the base, written as " " or "", as an id that
// no node of the base holds, and as the id of a check of the base, a kind of node that no relation
// names (relationKinds); and each attribute of the opening line of each block of approved.json
// left out, in approved.json alone and in the spec files that hold the same line too
// (leftOutAttributes). No such input may move a state toward true: the reader refuses a relation
// of an approved block that names no node of the approved spec of its kind (relationFault), a
// check's serves left out among them, and Validate refuses one in the files by the same kinds. One
// right: a parent left out (parentLeftOut).
func TestNoNameOfNothingInsideABlockMovesAStateTowardTrue(t *testing.T) {
	t.Parallel()
	bases := sweepBases(t)
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := bases[name]
			s, err := ReadScenario(base)
			if err != nil {
				t.Fatalf("the base: %v", err)
			}
			current, _ := s.Approved.Current()
			if len(current.Checks) == 0 {
				t.Fatal("the base holds no check")
			}
			names := append(slices.Clone(namesOfNothing), "no-such-node", current.Checks[0].ID)
			var inputs []sweepInput
			var tree any
			if err := json.Unmarshal(base["approved.json"].Data, &tree); err != nil {
				t.Fatalf("approved.json: %v", err)
			}
			for n, node := range tree.(map[string]any)["nodes"].([]any) {
				for v, version := range node.(map[string]any)["versions"].([]any) {
					place := []string{"nodes", strconv.Itoa(n), "versions", strconv.Itoa(v), "block"}
					block := version.(map[string]any)["block"].(string)
					for _, at := range blockTexts(block) {
						for _, form := range names {
							if block[at[0]:at[1]] != form {
								fsys := maps.Clone(base)
								fsys["approved.json"] = &fstest.MapFile{Data: encodeTree(t, treeWith(tree, place, block[:at[0]]+form+block[at[1]:]))}
								inputs = append(inputs, sweepInput{what: fmt.Sprintf("approved.json %s: %q as %q", treePointer(place), block[at[0]:at[1]], form), fsys: fsys})
							}
						}
					}
				}
			}
			for _, file := range slices.Sorted(maps.Keys(base)) {
				if !strings.HasPrefix(file, "files/") || path.Ext(file) != ".md" {
					continue
				}
				text := string(base[file].Data)
				for _, at := range blockTexts(text) {
					for _, form := range names {
						if text[at[0]:at[1]] != form {
							fsys := maps.Clone(base)
							fsys[file] = &fstest.MapFile{Data: []byte(text[:at[0]] + form + text[at[1]:])}
							inputs = append(inputs, sweepInput{what: fmt.Sprintf("%s at byte %d: %q as %q", file, at[0], text[at[0]:at[1]], form), fsys: fsys})
						}
					}
				}
			}
			whole := baseStates(t, base)
			n := sweepAgainst(t, whole, inputs)
			t.Logf("%d texts inside a block: %d refused, %d read as the base, %d read another state", n.inputs, n.refused, n.same, n.moved)
			n = sweepAgainst(t, whole, leftOutAttributes(t, base, tree))
			t.Logf("%d attributes left out: %d refused, %d read as the base, %d read another state, %d of them toward true by right: %q",
				n.inputs, n.refused, n.same, n.moved, n.byRight, n.rights)
		})
	}
}

// parentLeftOut is the right of an approved block that leaves out its parent: a statement or a
// component at the top of the tree has none (R1: "Each statement has one parent, except at the top
// level"; R15: "a parent component except at the top"), and no approved sentence refuses a change
// that moves one there, so the statement above it loses it, which can leave that one met.
const parentLeftOut = "a statement or a component whose approved block leaves out its parent is at the top of the tree (R1, R15)"

// blockAttributeText is one attribute of a block's opening line, with the space before it.
var blockAttributeText = regexp.MustCompile(` ([a-z_]+)="[^"]*"`)

// leftOutAttributes is, for each attribute of the opening line of each version of each node of
// tree, the approved.json of base, base with the attribute left out of that block (A), and the same
// with the attribute left out of each spec file of base that holds the same opening line too (B),
// where one does. An input that leaves out a parent has the right parentLeftOut.
func leftOutAttributes(t *testing.T, base fstest.MapFS, tree any) []sweepInput {
	t.Helper()
	var inputs []sweepInput
	for n, node := range tree.(map[string]any)["nodes"].([]any) {
		for v, version := range node.(map[string]any)["versions"].([]any) {
			place := []string{"nodes", strconv.Itoa(n), "versions", strconv.Itoa(v), "block"}
			block := version.(map[string]any)["block"].(string)
			opening, body, _ := strings.Cut(block, "\n")
			for _, at := range blockAttributeText.FindAllStringSubmatchIndex(opening, -1) {
				without := opening[:at[0]] + opening[at[1]:]
				right := ""
				if opening[at[2]:at[3]] == "parent" {
					right = parentLeftOut
				}
				what := fmt.Sprintf("approved.json %s: %q left out", treePointer(place), opening[at[0]+1:at[1]])
				fsys := maps.Clone(base)
				fsys["approved.json"] = &fstest.MapFile{Data: encodeTree(t, treeWith(tree, place, without+"\n"+body))}
				inputs = append(inputs, sweepInput{what: what, fsys: fsys, right: right})
				inFiles := maps.Clone(fsys)
				held := false
				for _, file := range slices.Sorted(maps.Keys(base)) {
					if text := string(base[file].Data); strings.HasPrefix(file, "files/") && strings.Contains(text, opening+"\n") {
						inFiles[file] = &fstest.MapFile{Data: []byte(strings.ReplaceAll(text, opening+"\n", without+"\n"))}
						held = true
					}
				}
				if held {
					inputs = append(inputs, sweepInput{what: what + " and from the spec files", fsys: inFiles, right: right})
				}
			}
		}
	}
	return inputs
}

// blockOpening is the opening line of a block, with what its braces hold, and blockAttribute the
// id and each value of an attribute in it.
var (
	blockOpening   = regexp.MustCompile(`^:::[a-z]+\{(.*)\}$`)
	blockAttribute = regexp.MustCompile(`#([^ }]*)|[a-z_]+="([^"]*)"`)
)

// blockTexts is the place of each text inside each block of text, as the offsets of its first and
// its last byte and one: the id and each value of an attribute of the block's opening line, and
// each line of its body, after the marker of a field of a check and its space where it has one.
func blockTexts(text string) [][2]int {
	var places [][2]int
	inBlock, at := false, 0
	for _, line := range strings.SplitAfter(text, "\n") {
		body := strings.TrimSuffix(line, "\n")
		switch open := blockOpening.FindStringSubmatchIndex(body); {
		case !inBlock && open != nil:
			inBlock = true
			for _, m := range blockAttribute.FindAllStringSubmatchIndex(body[open[2]:open[3]], -1) {
				for g := 2; g < len(m); g += 2 {
					if m[g] >= 0 {
						places = append(places, [2]int{at + open[2] + m[g], at + open[2] + m[g+1]})
					}
				}
			}
		case inBlock && body == ":::":
			inBlock = false
		case inBlock && body != "":
			start := 0
			for _, marker := range checkFields {
				if strings.HasPrefix(body, marker+" ") {
					start = len(marker) + 1
				}
			}
			places = append(places, [2]int{at + start, at + len(body)})
		}
		at += len(line)
	}
	return places
}
