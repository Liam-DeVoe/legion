package refs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// committedOpenAPIPath is the OpenAPI document committed beside the clients, from this package's
// directory: five directories up is the repository root. The test reads that file, never the
// document the emitter renders now, so it proves the contract every generated client was written
// from rather than the emitter's input.
var committedOpenAPIPath = filepath.Join("..", "..", "..", "..", "..", "packages", "contracts", "schemas", "dispatch-openapi.json")

// TestTheNodeKindEnumHoldsExactlyTheKindsTheGraphResolves is the enum clause of check
// c-visibility (spec requirement r-visibility of dispatch://INFRA-1098): the enum of
// GraphNode.kind in the committed OpenAPI document holds exactly nodeKinds, the keys of
// nodeRegistry that carry a loader. A kind the registry holds with no loader, such as supertag or
// attribute, is in no read's answer, so a reader never meets it and the enum does not name it. A
// kind a child adds to the registry with a loader is a member that child adds to the enum in its
// own forward merge.
//
// It prints both lists, and fails on each kind one holds and the other lacks, naming the kind and
// the list it stands in. A document that declares kind a plain string, with no enum, fails too,
// saying so: dispatch://INFRA-1950 closes the set, and a head without that commit has no enum to
// hold. The test lives in this package because the registry is unexported.
func TestTheNodeKindEnumHoldsExactlyTheKindsTheGraphResolves(t *testing.T) {
	document, err := os.ReadFile(committedOpenAPIPath)
	if err != nil {
		t.Fatalf("read the committed OpenAPI document: %v", err)
	}
	var read struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(document, &read); err != nil {
		t.Fatalf("decode %s: %v", committedOpenAPIPath, err)
	}
	node, held := read.Components.Schemas["GraphNode"]
	if !held {
		t.Fatalf("%s declares no schema GraphNode", committedOpenAPIPath)
	}
	declared, held := node.Properties["kind"]
	if !held {
		t.Fatalf("%s declares GraphNode with no property kind", committedOpenAPIPath)
	}
	var kind struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(declared, &kind); err != nil {
		t.Fatalf("decode GraphNode.kind of %s: %v", committedOpenAPIPath, err)
	}
	if kind.Enum == nil {
		t.Fatalf("%s declares GraphNode.kind as %s, a plain string with no enum: dispatch://INFRA-1950 closes it over the "+
			"node kinds the graph resolves, and this head does not hold that commit", committedOpenAPIPath, declared)
	}

	enum := slices.Sorted(slices.Values(kind.Enum))
	resolved := slices.Sorted(slices.Values(nodeKinds))
	t.Logf("the enum of GraphNode.kind in %s holds %d kinds: %v", committedOpenAPIPath, len(enum), enum)
	t.Logf("refs.nodeKinds, the keys of refs.nodeRegistry that carry a loader, holds %d kinds: %v", len(resolved), resolved)
	// Each list is counted before the two are compared: an empty one on either side is a
	// comparison of nothing, which would read no difference.
	if len(enum) == 0 {
		t.Fatalf("the enum of GraphNode.kind in %s holds no kind, so the comparison would read zero differences over nothing", committedOpenAPIPath)
	}
	if len(resolved) == 0 {
		t.Fatal("refs.nodeKinds holds no kind, so the comparison would read zero differences over nothing")
	}
	for index := 1; index < len(enum); index++ {
		if enum[index] == enum[index-1] {
			t.Errorf("the enum of GraphNode.kind names %s twice", enum[index])
		}
	}

	differences := 0
	for _, name := range resolved {
		if !slices.Contains(enum, name) {
			differences++
			t.Errorf("refs.nodeKinds holds %s, which a read of the graph answers, and the enum of GraphNode.kind lacks it", name)
		}
	}
	for _, name := range slices.Compact(slices.Clone(enum)) {
		if slices.Contains(resolved, name) {
			continue
		}
		differences++
		if _, registered := nodeRegistry[name]; registered {
			t.Errorf("the enum of GraphNode.kind holds %s, which refs.nodeRegistry holds with no loader, so no read of the graph answers it", name)
		} else {
			t.Errorf("the enum of GraphNode.kind holds %s, which refs.nodeRegistry does not hold", name)
		}
	}
	t.Logf("compared %d kinds of the enum with %d kinds of refs.nodeKinds: %d differences", len(enum), len(resolved), differences)
}
