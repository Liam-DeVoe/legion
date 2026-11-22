package record

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/plan"
)

// baseTask is a task of the plans the tests of a task's base read: its id, the ids it needs, the
// branch its first start gave it ("" for the issue's own) and the role of each of its steps.
type baseTask struct {
	id     string
	needs  []string
	branch string
	roles  []string
}

// basePlan is a plan of planIssue at generation 1 holding tasks, each running, each step typed as
// Legion's own roles type it (plan.BuiltinRoles).
func basePlan(tasks ...baseTask) StoredPlan {
	roles := plan.BuiltinRoles()
	stored := StoredPlan{Version: PlanVersion{Issue: planIssue, Generation: 1, Version: 1}, Steps: map[string][]PlanStep{}}
	for _, task := range tasks {
		stored.Tasks = append(stored.Tasks, PlanTask{Issue: planIssue, Generation: 1, Task: task.id, Needs: task.needs, Branch: task.branch, State: TaskRunning})
		for i, role := range task.roles {
			stored.Steps[task.id] = append(stored.Steps[task.id], PlanStep{Issue: planIssue, Generation: 1, Task: task.id, Position: i + 1, Role: role,
				Type: roles[role].Type})
		}
	}
	return stored
}

// The base of each task's pull request. A and B need nothing, C needs B, D needs A and C, and E,
// `tester` alone, needs D and r; r, `reviewer` alone, needs D. So E is the one task no task needs,
// the task that merges, and r is a task with a need and no step of the type implementation that does
// not merge. A, B and E are based on the default branch: two tasks that need none and the task that
// merges. C is based on its one need's branch, B's. D, which needs two tasks, is based on its join
// branch, its own branch and claim.JoinBranchSuffix, where the work of both is joined. r is based on
// the default branch: its pull request shows the joined work of its needs, which its review reads.
// The one task of a plan of one is based on the default branch.
//
// The fault it catches is D's pull request against A's branch, which shows C's work as D's (its
// control: BaseOf answering the first need's branch).
func TestTheBaseOfATask(t *testing.T) {
	issue := Issue{Key: planIssue, DefaultBranch: "main"}
	stored := basePlan(
		baseTask{"a", nil, "", []string{"implementer", "reviewer"}},
		baseTask{"b", nil, "legion/LEGION-208.b", []string{"implementer", "reviewer"}},
		baseTask{"c", []string{"b"}, "legion/LEGION-208.c", []string{"implementer", "reviewer"}},
		baseTask{"d", []string{"a", "c"}, "legion/LEGION-208.d", []string{"implementer", "reviewer"}},
		baseTask{"e", []string{"d", "r"}, "legion/LEGION-208.e", []string{"tester"}},
		baseTask{"r", []string{"d"}, "legion/LEGION-208.r", []string{"reviewer"}})
	for _, tc := range []struct {
		task, base string
		join       bool
	}{
		{"a", "main", false},
		{"b", "main", false},
		{"c", "legion/LEGION-208.b", false},
		{"d", "legion/LEGION-208.d.join", true},
		{"e", "main", false},
		{"r", "main", false},
	} {
		if base, join := stored.BaseOf(issue, tc.task); base != tc.base || join != tc.join {
			t.Errorf("BaseOf(%s) = %s, %t, want %s, %t", tc.task, base, join, tc.base, tc.join)
		}
	}

	one := basePlan(baseTask{"a", nil, "", []string{"implementer", "reviewer"}})
	if base, join := one.BaseOf(issue, "a"); base != "main" || join {
		t.Errorf("BaseOf(a) of a plan of one task = %s, %t, want main, false", base, join)
	}
}
