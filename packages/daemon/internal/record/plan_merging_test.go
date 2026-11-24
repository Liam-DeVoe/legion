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

// Whether a task's work merged is a fact its row holds: the task is done at its merge. So a plan
// of b, done at its merge, and f, waiting and needing b, says b's work merged, though f's need takes
// b out of the tasks that will merge, and the tasks that have a merge, done or to come, are b and f.
// A task done at its last step did not merge, and neither did a task running at its merge or one
// left there, nor a task the plan does not hold.
//
// The fault it catches is "its work merged" read from the rule of the plan in force, which a version
// that needs the merged task changes (its control: a task merged when it is in Merging and done,
// under which b's work did not merge).
func TestAMergedTaskStaysMergedWhenAVersionNeedsIt(t *testing.T) {
	merge := TaskPlace{Kind: PlaceMerge}
	stored := StoredPlan{Tasks: []PlanTask{
		{Task: "b", State: TaskDone, Place: merge},
		{Task: "f", State: TaskWaiting, Needs: []string{"b"}},
	}}
	if !stored.WorkMerged("b") {
		t.Fatalf("WorkMerged(b), done at its merge = false, want true")
	}
	if got := stored.Merging(); slices.Contains(got, "b") {
		t.Fatalf("Merging = %q, want no b: f needs it", got)
	}
	if got, want := stored.TasksThatMerge(), []string{"b", "f"}; !slices.Equal(got, want) {
		t.Fatalf("TasksThatMerge = %q, want %q: b, whose work merged, and f, which will merge", got, want)
	}
	for _, tc := range []struct {
		name string
		row  PlanTask
	}{
		{"done at its last step", PlanTask{Task: "a", State: TaskDone, Place: TaskPlace{Kind: PlaceStep, Position: 2}}},
		{"running at its merge", PlanTask{Task: "a", State: TaskRunning, Place: merge}},
		{"left at its merge", PlanTask{Task: "a", State: TaskLeft, Place: merge}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if (StoredPlan{Tasks: []PlanTask{tc.row}}).WorkMerged("a") {
				t.Fatalf("WorkMerged(a), %s = true, want false", tc.name)
			}
		})
	}
	if stored.WorkMerged("z") {
		t.Fatalf("WorkMerged(z), a task the plan does not hold = true, want false")
	}
}

// A task that its issue's leave wrote left at its merge still has a merge: a person parked the issue
// while the merge waited for the issues beneath it or stood in the merge queue, and the merge may
// land, or have landed, before the issue is set done. So the tasks that have a merge hold it, beside
// each task that will merge and each whose work merged, and a task left anywhere else is none of
// them. Of a (left at its merge), b (left at its retro, its block saying merges), c (done at its
// merge) and d (left at its first step, needing c), the tasks that have a merge are a and c.
//
// The fault it catches is a parked issue's merge that no later leave at done checks in production
// (its control: the tasks that have a merge without the left task at its merge, which gives [c]).
func TestATaskLeftAtItsMergeStillHasAMerge(t *testing.T) {
	stored := StoredPlan{Tasks: []PlanTask{
		{Task: "a", State: TaskLeft, Place: TaskPlace{Kind: PlaceMerge}},
		{Task: "b", State: TaskLeft, Merges: true, Place: TaskPlace{Kind: PlaceRetro}},
		{Task: "c", State: TaskDone, Place: TaskPlace{Kind: PlaceMerge}},
		{Task: "d", State: TaskLeft, Needs: []string{"c"}, Place: TaskPlace{Kind: PlaceStep, Position: 1}},
	}}
	if got, want := stored.TasksThatMerge(), []string{"a", "c"}; !slices.Equal(got, want) {
		t.Fatalf("TasksThatMerge = %q, want %q: a, left at its merge, and c, whose work merged", got, want)
	}
	if got, ok := (&StoredPlan{Tasks: stored.Tasks[:1]}).OnlyTaskThatMerges(); got != "a" || !ok {
		t.Fatalf("OnlyTaskThatMerges of a plan of a alone, left at its merge = %q, %v, want a", got, ok)
	}
}
