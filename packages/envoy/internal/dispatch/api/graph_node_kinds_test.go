package api

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// TestTheGraphNodeKindEnumIsTheRegistrysKinds: the OpenAPI document closes `GraphNode.kind` over
// every node kind the read registry holds (refs.VisibilityKinds), so the TypeScript type
// `NODE_LABELS` is keyed by names each of them, and a child that registers a kind later adds its
// member or fails here. `state` and `visibility` are closed over the values the store takes. Both
// the document the emitter writes now and the one committed beside the clients are read: the
// first is what the model's tags say, the second what every generated client was written from.
// The shapes that embed a node (GraphNodeRead, the node of a references read) carry the same.
func TestTheGraphNodeKindEnumIsTheRegistrysKinds(t *testing.T) {
	want := map[string][]string{
		"kind":       refs.VisibilityKinds(),
		"state":      graphstore.NodeStates(),
		"visibility": {graphstore.VisibilityProject, graphstore.VisibilityAuthor},
	}
	rendered, _ := readOpenAPI(t)
	for name, document := range map[string][]byte{"the emitted document": rendered, "the committed document": committedOpenAPI(t)} {
		var read struct {
			Components struct {
				Schemas map[string]struct {
					Properties map[string]struct {
						Enum []string `json:"enum"`
					} `json:"properties"`
					Required []string `json:"required"`
				} `json:"schemas"`
			} `json:"components"`
		}
		if err := json.Unmarshal(document, &read); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		for _, schema := range []string{"GraphNode", "GraphNodeRead"} {
			declared, held := read.Components.Schemas[schema]
			if !held {
				t.Fatalf("%s declares no schema %s", name, schema)
			}
			for field, values := range want {
				got := slices.Sorted(slices.Values(declared.Properties[field].Enum))
				if !slices.Equal(got, slices.Sorted(slices.Values(values))) {
					t.Errorf("%s declares %s.%s as one of %v; the registry and the store hold %v", name, schema, field, got, values)
				}
				if !slices.Contains(declared.Required, field) {
					t.Errorf("%s does not require %s.%s, which every node answers", name, schema, field)
				}
			}
		}
	}
}
