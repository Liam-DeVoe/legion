package record

import (
	"slices"
	"testing"
)

// Which tasks of a stored plan merge is the one rule of the plan (plan.Merging), read over each
// task that is not left, as its row gives it. A left task, whose issue left the workflow before it
// was done, neither merges nor needs: a need that only it names keeps no task from merging. So in
// the plan of a (done), b (left, needing a) and c (waiting), a and c merge; with b waiting, b and
// c merge, and a, which b needs, does not.
//
// The fault it catches is a left task's need keeping the task it needs from merging (its control:
// the rule read over every row, under which the first plan gives [b c]).
func TestTheMergeRuleReadsEachTaskThatIsNotLeft(t *testing.T) {
	for _, tc := range []struct {
		name string
		b    TaskState
		want []string
	}{
		{"b left", TaskLeft, []string{"a", "c"}},
		{"b waiting", TaskWaiting, []string{"b", "c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := StoredPlan{Tasks: []PlanTask{
				{Task: "a", State: TaskDone},
				{Task: "b", State: tc.b, Needs: []string{"a"}},
				{Task: "c", State: TaskWaiting},
			}}
			if got := stored.Merging(); !slices.Equal(got, tc.want) {
				t.Fatalf("Merging = %q, want %q", got, tc.want)
			}
		})
	}
}
