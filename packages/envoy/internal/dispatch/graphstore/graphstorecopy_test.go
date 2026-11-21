package graphstore

import (
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore/graphstorecopy"
)

// TestGraphstorecopyBaseAttributesAreTheStoresOwn holds graphstorecopy's copy of the base
// attributes to the set IsBaseAttribute reads (baseAttributes), element for element, so a base
// attribute added here or taken out is added to or taken out of the list the tests outside this
// package read.
func TestGraphstorecopyBaseAttributesAreTheStoresOwn(t *testing.T) {
	if copied := graphstorecopy.BaseAttributes(); !slices.Equal(copied, baseAttributes[:]) {
		t.Errorf("graphstorecopy.BaseAttributes is %q and the base attributes IsBaseAttribute reads are %q: change the copy with the set", copied, baseAttributes)
	}
}
