package plan

import (
	"reflect"
	"slices"
	"testing"
)

// ownedPlan is the plan the tests of who changes a task hold: f names a sub-owner, f1 and f2 are
// its parts, and h needs the whole of f. The parts come before their task, and f2 before f1, so
// an answer in the order of the ids is not the document's order.
func ownedPlan() Plan {
	implementer := []Step{{Role: "implementer"}}
	return Plan{ArtifactID: "plan-md", Version: 1, Tasks: []Task{
		{ID: "f2", PartOf: "f", Steps: implementer, Text: "The second part."},
		{ID: "f1", PartOf: "f", Steps: implementer, Text: "The first part."},
		{ID: "f", Owner: "architect", Text: "The whole task, in two parts."},
		{ID: "h", Needs: []string{"f"}, Steps: implementer, Text: "The work after it."},
	}}
}

// edited is p with each task id names changed by edit, each id gone names taken away, and each
// task of added put at the end.
func edited(p Plan, edit map[string]func(*Task), gone []string, added ...Task) Plan {
	next := Plan{ArtifactID: p.ArtifactID, Version: p.Version + 1}
	for _, task := range p.Tasks {
		if slices.Contains(gone, task.ID) {
			continue
		}
		if change, ok := edit[task.ID]; ok {
			change(&task)
		}
		next.Tasks = append(next.Tasks, task)
	}
	next.Tasks = append(next.Tasks, added...)
	return next
}

// A part of a task is written by that task's sub-owner, and a task of the top level by the owner
// of the issue (WriterOf).
func TestTheWriterOfEachTask(t *testing.T) {
	p := ownedPlan()
	for task, want := range map[string]string{"f": "", "h": "", "f1": "f", "f2": "f", "no-such-task": ""} {
		if got := p.WriterOf(task); got != want {
			t.Errorf("WriterOf(%s) = %q, want %q", task, got, want)
		}
	}
}

// Each task that a version changes is its writer's to change, in the plan held and in the
// version: the owner of the issue changes the tasks of the top level, and the sub-owner of a task
// changes its parts. A version that changes another writer's task is refused, one reason for each
// such task in the order of the ids. The reason names the task's writer, so a part of a part is
// named as its own task's, and the owner's reason says what the owner can do instead.
// A first version is compared with an empty plan, so an owner's first version that writes a part
// is refused as a later one is. A task that the version removes takes its parts with it, so the
// owner removes a task with its parts and is refused nothing for the parts.
func TestEachTaskIsItsWritersToChange(t *testing.T) {
	partsReason := func(id, task string) string {
		return "task " + id + " is a part of task " + task + ", and only the sub-owner of task " + task + " changes the parts of " + task + "."
	}
	ownersReason := func(id, task string) string {
		return partsReason(id, task) + " Leave the parts of " + task + " to its sub-owner, which writes them, or write this work as tasks of your own, with needs, outside task " + task + "."
	}
	subOwnersReason := func(id, task string) string {
		return "task " + id + " is not a part of task " + task + ", and the sub-owner of task " + task + " changes only the parts of " + task + "."
	}
	held := ownedPlan()
	implementer := []Step{{Role: "implementer"}}
	twoLevels := edited(held, map[string]func(*Task){"f1": func(task *Task) { task.Owner, task.Steps = "architect", nil }}, nil,
		Task{ID: "f1a", PartOf: "f1", Steps: implementer, Text: "A part of the first part."})
	for _, tc := range []struct {
		name  string
		held  *Plan
		next  Plan
		giver Giver
		want  []Refusal
	}{
		{name: "the sub-owner changes a task outside its task", giver: "f",
			next: edited(held, map[string]func(*Task){"h": func(task *Task) { task.Text = "The work after it, and its test." }}, nil),
			want: []Refusal{{Task: "h", Reason: subOwnersReason("h", "f")}}},
		{name: "the owner removes a part", giver: "",
			next: edited(held, nil, []string{"f1"}),
			want: []Refusal{{Task: "f1", Reason: ownersReason("f1", "f")}}},
		{name: "the owner adds a part", giver: "",
			next: edited(held, nil, nil, Task{ID: "f3", PartOf: "f", Steps: implementer, Text: "A third part."}),
			want: []Refusal{{Task: "f3", Reason: ownersReason("f3", "f")}}},
		{name: "the sub-owner adds a part and changes another", giver: "f",
			next: edited(held, map[string]func(*Task){"f2": func(task *Task) { task.Text = "The second part, smaller." }}, nil,
				Task{ID: "f3", PartOf: "f", Steps: implementer, Text: "A third part."})},
		{name: "the owner removes a task with its parts", giver: "",
			next: edited(held, nil, []string{"f", "f1", "f2"})},
		{name: "the sub-owner moves a part to another task", giver: "f",
			next: edited(held, map[string]func(*Task){"f2": func(task *Task) { task.PartOf = "g" }}, nil),
			want: []Refusal{{Task: "f2", Reason: partsReason("f2", "g")}}},
		{name: "the owner moves a part to the top level", giver: "",
			next: edited(held, map[string]func(*Task){"f2": func(task *Task) { task.PartOf = "" }}, nil),
			want: []Refusal{{Task: "f2", Reason: ownersReason("f2", "f")}}},
		{name: "the owner's first version writes a part", giver: "", held: &Plan{},
			next: Plan{ArtifactID: "plan-md", Version: 1, Tasks: []Task{
				{ID: "f", Owner: "architect", Text: "The whole task."},
				{ID: "f1", PartOf: "f", Steps: implementer, Text: "A part of it."},
			}},
			want: []Refusal{{Task: "f1", Reason: ownersReason("f1", "f")}}},
		{name: "the owner removes a task with its parts and theirs", giver: "", held: &twoLevels,
			next: edited(twoLevels, nil, []string{"f", "f1", "f2", "f1a"})},
		{name: "the sub-owner of a part removes its own part", giver: "f1", held: &twoLevels,
			next: edited(twoLevels, nil, []string{"f1a"})},
		{name: "the sub-owner of a task changes a part of its part", giver: "f", held: &twoLevels,
			next: edited(twoLevels, map[string]func(*Task){"f1a": func(task *Task) { task.Text = "Smaller." }}, nil),
			want: []Refusal{{Task: "f1a", Reason: partsReason("f1a", "f1")}}},
		{name: "the sub-owner of a part changes its sibling", giver: "f1", held: &twoLevels,
			next: edited(twoLevels, map[string]func(*Task){"f2": func(task *Task) { task.Text = "Smaller." }}, nil),
			want: []Refusal{{Task: "f2", Reason: partsReason("f2", "f")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := held
			if tc.held != nil {
				before = *tc.held
			}
			if got := OwnershipRefusals(before, tc.next, ChangedTasks(before, tc.next), tc.giver); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("OwnershipRefusals(giver %q) = %#v, want %#v", tc.giver, got, tc.want)
			}
		})
	}
}
