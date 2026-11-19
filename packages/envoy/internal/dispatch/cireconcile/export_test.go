package cireconcile

import (
	"context"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// mappingRepairsOf is api.MappingRepairs, the repairs cmd/dispatch gives its Reconciler
// (Reconciler.MappingRepairs), which a test of this package gives the Reconciler it reads a WARN
// of: package api imports this one, so no file of this package can import it, and the external
// test package hands it over (UseMappingRepairs, mapping_route_test.go).
var mappingRepairsOf func(*store.Pool) func(ctx context.Context, row string) []any

// UseMappingRepairs is how the external test package sets mappingRepairsOf, before any test runs.
func UseMappingRepairs(of func(*store.Pool) func(ctx context.Context, row string) []any) {
	mappingRepairsOf = of
}
