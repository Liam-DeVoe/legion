package refs

import (
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
)

// Every kind graphstore's pointerRules holds - each pointer graphstore.PointerKinds lists and each
// placement graphstore.PlacementKinds lists - is one the store arm of the graph read reads into a
// block (keyedStoreArm): among its pointers (storeArmPointers), or named as left out of them
// (storeArmPlacements), so a kind added to the table is read by the arm or left out by name, and
// never dropped from the read unsaid.
//
// The negative control is a placement rule of a new kind added to pointerRules alone, which this
// test names.
func TestTheStoreArmReadsEveryKindThePointerRulesHold(t *testing.T) {
	for _, kind := range slices.Concat(graphstore.PointerKinds(), graphstore.PlacementKinds()) {
		if !slices.Contains(storeArmPointers, kind) && !slices.Contains(storeArmPlacements, kind) {
			t.Errorf("pointerRules holds %s, which keyedStoreArm neither reads among its pointers (storeArmPointers) nor names as left out of them (storeArmPlacements)", kind)
		}
	}
}
