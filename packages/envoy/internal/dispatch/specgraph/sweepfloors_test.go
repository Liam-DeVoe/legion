package specgraph

import (
	"maps"
	"slices"
	"testing"
	"testing/fstest"
)

// sweepFloors is the count of inputs that each sweep read of each base (sweepBases) when the table
// was written, by the sweep: the tree sweep (TestNoInputThatSaysNothingMovesAStateTowardTrue), the
// members added (TestAddingAMemberMovesNoStateTowardTrue), the texts inside a block and the
// attributes left out (TestNoNameOfNothingInsideABlockMovesAStateTowardTrue), and the refusals
// added (TestAddingARefusalOfCIMovesAStateTowardTrueOnlyByTakingOutItsCommitsResults). A sweep that
// reads fewer inputs of a base fails by name (atFloor), and so does a base with no floor or a floor
// of no base (floorsCover), so that a fault that drops inputs, of a sweep's meaning or of its
// writers, cannot pass by reading less. A base or a scenario that adds inputs raises no failure;
// one that takes inputs out lowers its floor here, on purpose.
var sweepFloors = map[string]map[string]int{
	"tree": {
		"carry": 979,
		"carry with a failure of c0, c1's parent, and a later pass of c2":            1234,
		"carry with a later failing example planted by no step":                      1038,
		"carry with a later failing example planted on c1 that missed its fault":     1042,
		"carry with a later failing example planted on c9, no commit of the history": 1042,
		"carry with c2's failure at 10:30 and a later pass of m2":                    1131,
		"carry with c2's failure between c1's pass and c1's failing example":         1117,
		"carry with its failing example planted by no step":                          966,
		"earlier-text":           434,
		"one-commit-two-answers": 792,
		"production-and-graph":   482,
		"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 490,
		"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              490,
		"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                490,
		"production-and-graph with CI's refusal at c1 of its query of the graph":                                          490,
		"production-and-graph with its program check of production as a program check of the graph":                       482,
		"production-and-graph with its program check of production as an agent of production":                             482,
		"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  486,
		"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               486,
		"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": 486,
		"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           486,
		"refusal":    987,
		"silent-leg": 667,
		"silent-leg with its matrix dropped at m2": 651,
		"wait-cycle": 744,
		"wait-cycle with a second, passing check of a-message-survives-a-restart": 907,
		"wait-cycle with the outcomes of its two first runs swapped":              744,
	},
	"members": {
		"carry": 125,
		"carry with a failure of c0, c1's parent, and a later pass of c2":            226,
		"carry with a later failing example planted by no step":                      158,
		"carry with a later failing example planted on c1 that missed its fault":     149,
		"carry with a later failing example planted on c9, no commit of the history": 159,
		"carry with c2's failure at 10:30 and a later pass of m2":                    212,
		"carry with c2's failure between c1's pass and c1's failing example":         212,
		"carry with its failing example planted by no step":                          129,
		"earlier-text":           40,
		"one-commit-two-answers": 171,
		"production-and-graph":   133,
		"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 133,
		"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              133,
		"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                133,
		"production-and-graph with CI's refusal at c1 of its query of the graph":                                          133,
		"production-and-graph with its program check of production as a program check of the graph":                       133,
		"production-and-graph with its program check of production as an agent of production":                             133,
		"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  130,
		"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               130,
		"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": 130,
		"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           130,
		"refusal":    125,
		"silent-leg": 171,
		"silent-leg with its matrix dropped at m2": 171,
		"wait-cycle": 110,
		"wait-cycle with a second, passing check of a-message-survives-a-restart": 144,
		"wait-cycle with the outcomes of its two first runs swapped":              110,
	},
	"texts": {
		"carry": 575,
		"carry with a failure of c0, c1's parent, and a later pass of c2":            575,
		"carry with a later failing example planted by no step":                      575,
		"carry with a later failing example planted on c1 that missed its fault":     575,
		"carry with a later failing example planted on c9, no commit of the history": 575,
		"carry with c2's failure at 10:30 and a later pass of m2":                    575,
		"carry with c2's failure between c1's pass and c1's failing example":         575,
		"carry with its failing example planted by no step":                          575,
		"earlier-text":           300,
		"one-commit-two-answers": 510,
		"production-and-graph":   206,
		"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 206,
		"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              206,
		"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                206,
		"production-and-graph with CI's refusal at c1 of its query of the graph":                                          206,
		"production-and-graph with its program check of production as a program check of the graph":                       206,
		"production-and-graph with its program check of production as an agent of production":                             182,
		"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  206,
		"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               206,
		"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": 206,
		"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           206,
		"refusal":    575,
		"silent-leg": 345,
		"silent-leg with its matrix dropped at m2": 345,
		"wait-cycle": 350,
		"wait-cycle with a second, passing check of a-message-survives-a-restart": 438,
		"wait-cycle with the outcomes of its two first runs swapped":              350,
	},
	"attributes": {
		"carry": 26,
		"carry with a failure of c0, c1's parent, and a later pass of c2":            26,
		"carry with a later failing example planted by no step":                      26,
		"carry with a later failing example planted on c1 that missed its fault":     26,
		"carry with a later failing example planted on c9, no commit of the history": 26,
		"carry with c2's failure at 10:30 and a later pass of m2":                    26,
		"carry with c2's failure between c1's pass and c1's failing example":         26,
		"carry with its failing example planted by no step":                          26,
		"earlier-text":           22,
		"one-commit-two-answers": 68,
		"production-and-graph":   24,
		"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 24,
		"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              24,
		"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                24,
		"production-and-graph with CI's refusal at c1 of its query of the graph":                                          24,
		"production-and-graph with its program check of production as a program check of the graph":                       24,
		"production-and-graph with its program check of production as an agent of production":                             18,
		"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  24,
		"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               24,
		"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": 24,
		"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           24,
		"refusal":    26,
		"silent-leg": 26,
		"silent-leg with its matrix dropped at m2": 26,
		"wait-cycle": 46,
		"wait-cycle with a second, passing check of a-message-survives-a-restart": 58,
		"wait-cycle with the outcomes of its two first runs swapped":              46,
	},
	"refusals": {
		"carry": 14,
		"carry with a failure of c0, c1's parent, and a later pass of c2":            18,
		"carry with a later failing example planted by no step":                      16,
		"carry with a later failing example planted on c1 that missed its fault":     16,
		"carry with a later failing example planted on c9, no commit of the history": 18,
		"carry with c2's failure at 10:30 and a later pass of m2":                    16,
		"carry with c2's failure between c1's pass and c1's failing example":         16,
		"carry with its failing example planted by no step":                          12,
		"earlier-text":           5,
		"one-commit-two-answers": 20,
		"production-and-graph":   4,
		"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 3,
		"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              3,
		"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                3,
		"production-and-graph with CI's refusal at c1 of its query of the graph":                                          3,
		"production-and-graph with its program check of production as a program check of the graph":                       4,
		"production-and-graph with its program check of production as an agent of production":                             4,
		"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  4,
		"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               4,
		"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": 4,
		"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           4,
		"refusal":    13,
		"silent-leg": 10,
		"silent-leg with its matrix dropped at m2": 10,
		"wait-cycle": 18,
		"wait-cycle with a second, passing check of a-message-survives-a-restart": 24,
		"wait-cycle with the outcomes of its two first runs swapped":              18,
	},
}

// markReadingsFloor is the count of readings of a member left out that
// TestAMemberLeftOutOfAFileIsReadByItsMark read when the floor was written.
const markReadingsFloor = 82

// atFloor fails the test unless the sweep read at least its floor of inputs of the base name
// (sweepFloors).
func atFloor(t *testing.T, sweep, name string, n int) {
	t.Helper()
	if floor, held := sweepFloors[sweep][name]; n < floor || !held {
		t.Errorf("the %s sweep read %d inputs of %s, and its floor is %d (held %t; sweepFloors)", sweep, n, name, floor, held)
	}
}

// floorsCover fails the test unless the sweep has a floor of inputs for each base of bases and for
// no other (sweepFloors).
func floorsCover(t *testing.T, sweep string, bases map[string]fstest.MapFS) {
	t.Helper()
	if got, want := slices.Sorted(maps.Keys(sweepFloors[sweep])), slices.Sorted(maps.Keys(bases)); !slices.Equal(got, want) {
		t.Errorf("the %s sweep has floors of inputs for the bases %q, and the sweep reads the bases %q (sweepFloors)", sweep, got, want)
	}
}
