package plan

import (
	"reflect"
	"strings"
	"testing"
)

// needWord is a need written as Dispatch writes a reference to the block id of the plan document.
func needWord(id string) string { return "dispatch://ACME-1/artifact/plan-md#b-" + id }

// taskNeeding is a `plan_task` block id, with the steps `implementer reviewer`, whose `needs`
// writes the words needs, and no `needs` when there are none.
func taskNeeding(id string, needs ...string) Block {
	attributes := map[string]string{"steps": "implementer reviewer"}
	if len(needs) > 0 {
		attributes["needs"] = strings.Join(needs, " ")
	}
	return Block{ID: id, Type: TaskBlockType, Attributes: attributes, Text: "What task " + id + " does."}
}

// exampleAToE is the spec's example plan, each need written as a reference to a block: c needs b,
// d needs a and c, and e needs d.
func exampleAToE(t *testing.T) Plan {
	t.Helper()
	return planOf(t, 1, taskNeeding("a"), taskNeeding("b"), taskNeeding("c", needWord("b")),
		taskNeeding("d", needWord("a"), needWord("c")), taskNeeding("e", needWord("d")))
}

// ResolveNeeds gives each task's Needs as the ids of the tasks they name, in the order the block
// writes them: a reference's block id after its last `#b-`, decoded as Dispatch encodes one (`%2E`
// is a `.`) with a pin after it left out, and a word with no `#b-` as itself. A task named twice is
// needed once. Each word that names no task stays in Unresolved, and the plan given is left as it
// was. The fault is a plan recorded with the words of its needs, which no reader of a task id can
// read; the control is a ResolveNeeds that keeps each word whole, under which d needs the words.
func TestResolveNeedsGivesTheIdsOfTheTasksNeeded(t *testing.T) {
	given := planOf(t, 1, taskNeeding("a.b"), taskNeeding("c"),
		taskNeeding("d", needWord("c")+"@v2", needWord("a%2Eb"), "x", "c"),
		taskNeeding("e", "d", needWord("d")+"@vx"))
	words := append([]string(nil), given.Tasks[2].Needs...)

	resolved := ResolveNeeds(given)
	if got := resolved.Tasks[2]; !reflect.DeepEqual(got.Needs, []string{"c", "a.b"}) || !reflect.DeepEqual(got.Unresolved, []string{"x"}) {
		t.Fatalf("d needs %q and leaves %q unresolved, want [c a.b] and [x]", got.Needs, got.Unresolved)
	}
	// A pin that is no `@v<N>` makes the reference name no block, as Dispatch reads it.
	if got := resolved.Tasks[3]; !reflect.DeepEqual(got.Needs, []string{"d"}) || !reflect.DeepEqual(got.Unresolved, []string{needWord("d") + "@vx"}) {
		t.Fatalf("e needs %q and leaves %q unresolved, want [d] and the reference with the bad pin", got.Needs, got.Unresolved)
	}
	if got := resolved.Tasks[0]; got.Needs != nil || got.Unresolved != nil {
		t.Fatalf("a, which needs nothing, needs %q and leaves %q unresolved", got.Needs, got.Unresolved)
	}
	if !reflect.DeepEqual(given.Tasks[2].Needs, words) || given.Tasks[2].Unresolved != nil {
		t.Fatalf("ResolveNeeds changed the plan given: d needs %q, unresolved %q", given.Tasks[2].Needs, given.Tasks[2].Unresolved)
	}
}

// R14: a need that names no task of the plan waits for a task that never runs, so Validate refuses
// it for good, once for each such word in the order the block writes them, naming the word as the
// block writes it. The fault is such a plan taken; the control is a ResolveNeeds that keeps each
// word whole, under which the reference to b-x is refused by its id and the spec's example A to E
// answers four refusals of a need of no task.
func TestANeedThatNamesNoTaskIsRefused(t *testing.T) {
	d := Deployment{Roles: BuiltinRoles()}
	for _, tc := range []struct {
		name string
		plan Plan
		want []Refusal
	}{
		{name: "a word", plan: planOf(t, 1, taskNeeding("a"), taskNeeding("d", "x")),
			want: []Refusal{{Task: "d", Reason: "task d needs x, which names no task of this plan."}}},
		{name: "a reference to the block of no task", plan: planOf(t, 1, taskNeeding("a"), taskNeeding("d", needWord("x"))),
			want: []Refusal{{Task: "d", Reason: "task d needs dispatch://ACME-1/artifact/plan-md#b-x, which names no task of this plan."}}},
		{name: "each word in the block's order", plan: planOf(t, 1, taskNeeding("a"), taskNeeding("d", "y", needWord("a"), "x")),
			want: []Refusal{
				{Task: "d", Reason: "task d needs y, which names no task of this plan."},
				{Task: "d", Reason: "task d needs x, which names no task of this plan."},
			}},
		{name: "the spec's example A to E", plan: exampleAToE(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Validate(ResolveNeeds(tc.plan), d); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Validate =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

// R14: a task that needs itself through other tasks never starts, so Validate refuses each cycle
// of needs for good, named by its first task in id order with the chain from that task. Each need
// that lies on a cycle is in a chain of the answer, the shortest cycle through it, so the owner
// reads every need that closes a cycle at once; a need that closes no cycle is in none. The fault
// is a cycle that walks for ever or is taken. The control is Validate without the check of
// cycles, under which the first case answers no refusal.
func TestATaskThatNeedsItselfIsRefused(t *testing.T) {
	d := Deployment{Roles: BuiltinRoles()}
	itself := func(task, chain string) Refusal {
		return Refusal{Task: task, Reason: "task " + task + " needs itself: " + chain + "."}
	}
	for _, tc := range []struct {
		name string
		plan Plan
		want []Refusal
	}{
		{name: "a cycle of three tasks", plan: planOf(t, 1, taskNeeding("a", needWord("b")), taskNeeding("b", needWord("c")), taskNeeding("c", needWord("a"))),
			want: []Refusal{itself("a", "a needs b, b needs c, c needs a")}},
		{name: "a task that names itself", plan: planOf(t, 1, taskNeeding("a", "a")),
			want: []Refusal{itself("a", "a needs a")}},
		{name: "two cycles", plan: planOf(t, 1, taskNeeding("a", "b"), taskNeeding("b", "a"), taskNeeding("c", "d"), taskNeeding("d", "c")),
			want: []Refusal{itself("a", "a needs b, b needs a"), itself("c", "c needs d, d needs c")}},
		{name: "a cycle named by its first task in id order, whatever the document's order", plan: planOf(t, 1,
			taskNeeding("c", needWord("a")), taskNeeding("b", needWord("c")), taskNeeding("a", needWord("b"))),
			want: []Refusal{itself("a", "a needs b, b needs c, c needs a")}},
		{name: "two cycles through one task", plan: planOf(t, 1, taskNeeding("a", "c", "b"), taskNeeding("b", "a"), taskNeeding("c", "d"), taskNeeding("d", "a")),
			want: []Refusal{itself("a", "a needs b, b needs a"), itself("a", "a needs c, c needs d, d needs a")}},
		{name: "two cycles that share a task", plan: planOf(t, 1, taskNeeding("a", "b"), taskNeeding("b", "a", "c"), taskNeeding("c", "b")),
			want: []Refusal{itself("a", "a needs b, b needs a"), itself("b", "b needs c, c needs b")}},
		{name: "a need outside the cycle", plan: planOf(t, 1, taskNeeding("a", "b"), taskNeeding("b", "a"), taskNeeding("e", "a")),
			want: []Refusal{itself("a", "a needs b, b needs a")}},
		{name: "the spec's example A to E", plan: exampleAToE(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Validate(ResolveNeeds(tc.plan), d); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Validate =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}
