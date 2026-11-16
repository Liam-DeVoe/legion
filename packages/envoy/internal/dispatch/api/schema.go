package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc/schemanodes"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// blockSchemaRead is what GET /api/v1/schema/blocks answers: the block schema this process
// installed, member for member as pmdoc.SchemaJSON renders it - its version, the Proof schema's
// nodes and marks, and its typed blocks - and beside those members the types of the store that
// declare no block type and the lens catalogue, both read from the store on the request. A reader
// of the block schema reads the members it always read, and the version is the installed block
// schema's: no store type and no lens kind moves it, and none closes an editor's socket.
type blockSchemaRead struct {
	// Version is the installed block schema's version, which clients present when they admit a
	// document.
	Version int `json:"version"`
	// Nodes, Marks and Types are the installed block schema's own members, as it renders them.
	Nodes installedSchemaElements `json:"nodes,omitempty"`
	Marks installedSchemaElements `json:"marks,omitempty"`
	Types installedSchemaElements `json:"types"`
	// StoreTypes is every type of the store that declares no block type, by name, each with the
	// attributes its attribute nodes hold.
	StoreTypes []storeTypeRead `json:"store_types"`
	// LensKinds is the lens catalogue, by id: each kind with its words and the constraint that
	// holds its primary source.
	LensKinds []lensKindRead `json:"lens_kinds"`
}

// installedSchemaElements is one member of the installed block schema - its nodes, its marks or
// its typed blocks - held as the bytes pmdoc.SchemaJSON rendered, so nothing between the install
// and the answer reshapes it.
type installedSchemaElements json.RawMessage

// MarshalJSON writes the member's bytes where they stand.
func (e installedSchemaElements) MarshalJSON() ([]byte, error) {
	return json.RawMessage(e).MarshalJSON()
}

// UnmarshalJSON keeps the bytes as they came.
func (e *installedSchemaElements) UnmarshalJSON(data []byte) error {
	return (*json.RawMessage)(e).UnmarshalJSON(data)
}

// JSONSchemaBytes is the schema the OpenAPI emitter writes for each member: a list of the block
// schema's elements, each the object packages/contracts/schemas/block-schema.schema.json declares.
func (installedSchemaElements) JSONSchemaBytes() ([]byte, error) {
	return json.Marshal(map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "object", "additionalProperties": true},
		"description": "elements of the installed block schema, each as packages/contracts/schemas/block-schema.schema.json " +
			"declares it: a node or a mark of the Proof schema, or a typed block",
	})
}

// InlineJSONSchema marks the schema to be written where each member stands rather than declared
// under a name of its own: the names a client imports for these elements are @legion/contracts'
// block-schema types.
func (installedSchemaElements) InlineJSONSchema() {}

// storeTypeRead is one type of the store that declares no block type (schemanodes.StoreTypes).
type storeTypeRead struct {
	Name       string               `json:"name"`
	Attributes []storeAttributeRead `json:"attributes"`
}

// storeAttributeRead is one attribute a store type holds: the name a value of it is stored under
// and the kind of value it holds.
type storeAttributeRead struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// lensKindRead is one kind of the lens catalogue (refs.LensCatalogue): its id, its address, the
// words a person reads for it, and the constraint each of its constrained_by tuples reaches, which
// for a kind of the catalogue is the one that holds its primary source as standard and that
// source's reference.
type lensKindRead struct {
	ID      string               `json:"id"`
	Ref     string               `json:"ref"`
	Words   string               `json:"words"`
	Sources []lensKindSourceRead `json:"sources"`
}

// lensKindSourceRead is one constraint a lens kind is held to.
type lensKindSourceRead struct {
	ID        string `json:"id"`
	Ref       string `json:"ref"`
	Standard  string `json:"standard"`
	Reference string `json:"reference"`
}

// getBlockSchema answers the installed block schema and, read from the store on this request, the
// store's types that declare no block type and the lens catalogue. The catalogue is read through
// the predicate with no credential, as the route takes none: a lens kind and its source are
// project-visible and in no project, so every caller reads them. Nothing here is cached, so a type
// or a kind a migration or `envoy-dispatch lens-kind-add` writes is answered by the next request of
// every process.
func (s *server) getBlockSchema(w http.ResponseWriter, r *http.Request) {
	var answer blockSchemaRead
	if err := json.Unmarshal(pmdoc.SchemaJSON(), &answer); err != nil {
		s.writeHandlerError(w, fmt.Errorf("read the installed block schema: %w", err))
		return
	}
	types, err := schemanodes.StoreTypes(r.Context(), s.deps.Store.Pool)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	answer.StoreTypes = make([]storeTypeRead, len(types))
	for index, typ := range types {
		read := storeTypeRead{Name: typ.Name, Attributes: make([]storeAttributeRead, len(typ.Attributes))}
		for at, attribute := range typ.Attributes {
			read.Attributes[at] = storeAttributeRead{Name: attribute.Name, Kind: string(attribute.Kind)}
		}
		answer.StoreTypes[index] = read
	}
	catalogue, err := refs.LensCatalogue(r.Context(), s.deps.Store.Pool, model.Actor{})
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	answer.LensKinds = make([]lensKindRead, len(catalogue))
	for index, kind := range catalogue {
		read := lensKindRead{
			ID: kind.ID, Ref: "dispatch://" + graphstore.KindLensKind + "/" + kind.ID, Words: kind.Words,
			Sources: make([]lensKindSourceRead, len(kind.Sources)),
		}
		for at, source := range kind.Sources {
			read.Sources[at] = lensKindSourceRead{
				ID: source.ID, Ref: "dispatch://" + graphstore.KindConstraint + "/" + source.ID,
				Standard: source.Standard, Reference: source.Reference,
			}
		}
		answer.LensKinds[index] = read
	}
	WriteJSON(w, http.StatusOK, answer)
}
