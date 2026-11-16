package specgraph

import (
	"slices"
	"testing"
)

// v-work 10: "Two units each wait on a statement that the other unit serves. Dispatch refuses the
// second wait." R29: "a wait on a statement counts as a wait on each unit that serves that
// statement".
func TestTwoUnitsThatWaitOnEachOthersStatementsCloseACycle(t *testing.T) {
	t.Parallel()
	expectCycle(t, []Unit{
		{ID: "u-a", Serves: []string{"s-a"}, WaitsOn: []string{"s-b"}},
		{ID: "u-b", Serves: []string{"s-b"}, WaitsOn: []string{"s-a"}},
	}, []string{"u-a", "s-b", "u-b", "s-a", "u-a"})
}

func TestAUnitThatWaitsOnItsOwnStatementIsACycle(t *testing.T) {
	t.Parallel()
	expectCycle(t, []Unit{{ID: "u-a", Serves: []string{"s-a"}, WaitsOn: []string{"s-a"}}}, []string{"u-a", "s-a", "u-a"})
}

// A unit that needs another unit waits on it directly, so its step names no statement.
func TestNeedsAndWaitsJoin(t *testing.T) {
	t.Parallel()
	expectCycle(t, []Unit{
		{ID: "u-a", Serves: []string{"s-a"}, Needs: []string{"u-b"}},
		{ID: "u-b", WaitsOn: []string{"s-a"}},
	}, []string{"u-a", "u-b", "s-a", "u-a"})
}

// A chain of waits ends at a unit that waits on a statement that no unit serves.
func TestNoCycle(t *testing.T) {
	t.Parallel()
	expectCycle(t, []Unit{
		{ID: "u-a", Serves: []string{"s-a"}, WaitsOn: []string{"s-b"}},
		{ID: "u-b", Serves: []string{"s-b"}, WaitsOn: []string{"s-c"}, Needs: []string{"u-c"}},
		{ID: "u-c", Serves: []string{"s-d"}},
	}, nil)
}

// Of several cycles, WaitCycle gives the shortest one from the smallest unit that is on a cycle,
// whatever the order of the units: u-b's two units are on no cycle, and u-c is on a cycle of three
// units and on one of two.
func TestTheShortestCycleFromTheSmallestUnit(t *testing.T) {
	t.Parallel()
	units := []Unit{
		{ID: "u-z", Serves: []string{"s-z"}, WaitsOn: []string{"s-y"}},
		{ID: "u-y", Serves: []string{"s-y"}, WaitsOn: []string{"s-z"}},
		{ID: "u-e", Serves: []string{"s-e"}, WaitsOn: []string{"s-c"}},
		{ID: "u-d", Serves: []string{"s-d"}, WaitsOn: []string{"s-e"}},
		{ID: "u-c", Serves: []string{"s-c"}, WaitsOn: []string{"s-d", "s-e"}},
		{ID: "u-b", WaitsOn: []string{"s-c"}},
		{ID: "u-a", Needs: []string{"u-b"}},
	}
	want := []string{"u-c", "s-e", "u-e", "s-c", "u-c"}
	expectCycle(t, units, want)
	slices.Reverse(units)
	expectCycle(t, units, want)
}

// expectCycle fails the test unless WaitCycle(units) is want.
func expectCycle(t *testing.T, units []Unit, want []string) {
	t.Helper()
	if got := WaitCycle(units); !slices.Equal(got, want) || (got == nil) != (want == nil) {
		t.Errorf("WaitCycle = %q, want %q", got, want)
	}
}
