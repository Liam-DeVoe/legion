package plan

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// yet ends each refusal of something a later release carries out.
const yet = " This release of Legion does not carry it out yet."

// What no release carries out, as this release checks it: a step that names a role neither the
// deployment nor the plan defines is refused, naming the step, its task and the role. Validate
// refuses no plan for a step that it lacks, so each plan of Legion's own roles passes it whatever
// steps it leaves out; a task with no implementer step, and a tester, reviewer or acceptance step
// with no implementer before it, are what this release does not carry out yet, and Supported
// refuses them. The control is in the second half: a validator that also required a reviewer
// step would refuse `implementer tester`, and one that required a tester would refuse
// `implementer reviewer`.
func TestValidateRefusesARoleThatNobodyDefines(t *testing.T) {
	d := Deployment{Roles: BuiltinRoles()}
	r := Validate(oneTask("a", "implementer prober"), d)
	want := Refusal{Task: "a", Reason: "step 2 of task a names the role prober, which neither the deployment nor the plan defines."}
	if len(r) != 1 || r[0] != want {
		t.Fatalf("got %+v, want %+v", r, want)
	}
	for _, taken := range []Plan{oneTask("a", "implementer tester"), oneTask("a", "implementer reviewer"), oneTask("a", "implementer")} {
		if r := Validate(taken, d); len(r) != 0 {
			t.Fatalf("%v refused: %+v", taken, r)
		}
	}
	// architect, merger and controller are no roles of a step: the step roles are Legion's five.
	for _, role := range []string{"architect", "merger", "controller"} {
		r := Validate(oneTask("a", "implementer "+role), d)
		want := Refusal{Task: "a", Reason: "step 2 of task a names the role " + role + ", which neither the deployment nor the plan defines."}
		if len(r) != 1 || r[0] != want {
			t.Fatalf("Validate(implementer %s) = %+v, want %+v", role, r, want)
		}
	}
	// A role named at two steps is one cause, refused once at its first step.
	if r := Validate(oneTask("a", "prober implementer prober"), d); !reflect.DeepEqual(r, []Refusal{
		{Task: "a", Reason: "step 1 of task a names the role prober, which neither the deployment nor the plan defines."},
	}) {
		t.Fatalf("Validate(prober implementer prober) = %+v", r)
	}
	// A role the plan's own role block defines is defined (Supported refuses the role block).
	defined := oneTask("a", "implementer prober")
	defined.RoleBlocks = []string{"prober"}
	if r := Validate(defined, d); len(r) != 0 {
		t.Fatalf("Validate(a role the plan defines) = %+v, want none", r)
	}
}

// A plan with no task block gives Legion no work, and Validate refuses it with the one reason no
// later release lifts, so the route answers it as each other plan Legion does not take. The
// control: a plan of one block with no `steps` is taken.
func TestValidateRefusesAPlanWithNoTask(t *testing.T) {
	d := Deployment{Roles: BuiltinRoles()}
	empty, err := FromBlocks("plan-md", 1, []Block{{ID: "intro", Type: "paragraph", Text: "No task here."}})
	if err != nil {
		t.Fatalf("FromBlocks(no task block): %v", err)
	}
	if len(empty.Tasks) != 0 {
		t.Fatalf("FromBlocks(no task block) tasks = %+v, want none", empty.Tasks)
	}
	want := []Refusal{{Task: "", Reason: "the plan has no task block, so it gives Legion no work."}}
	if r := Validate(empty, d); !reflect.DeepEqual(r, want) {
		t.Fatalf("Validate(no task) = %+v, want %+v", r, want)
	}
	if r := Supported(empty); len(r) != 0 {
		t.Fatalf("Supported(no task) = %+v, want none: the cause is Validate's alone", r)
	}
	taken, err := FromBlocks("plan-md", 1, []Block{{ID: "a", Type: "plan_task", Text: "Do the change."}})
	if err != nil {
		t.Fatalf("FromBlocks(one block): %v", err)
	}
	if r := append(Supported(taken), Validate(taken, d)...); len(r) != 0 {
		t.Fatalf("a plan of one block with no steps refused: %+v", r)
	}
}

// Each cause this release does not carry out yet (Supported), with its exact text and its task.
// Each cause gets one reason, so a plan with two causes gets two. The taken plans are the subsets
// of today's line that keep the implementer, with the owner's acceptance.
func TestSupportedRefusesWhatThisReleaseDoesNotCarryOut(t *testing.T) {
	implementer, _ := ParseSteps("implementer")
	withTask := func(task Task) Plan { return Plan{Tasks: []Task{task}} }
	for _, tc := range []struct {
		name string
		plan Plan
		want []Refusal
	}{
		{name: "more than one task", plan: Plan{Tasks: []Task{
			{ID: "a", Steps: implementer}, {ID: "b", Steps: implementer}, {ID: "c"},
		}}, want: []Refusal{
			{Task: "", Reason: "the plan has 3 tasks (a, b, c), and this release runs one task for each issue." + yet},
			{Task: "", Reason: "the plan has 3 tasks that no task needs (a, b, c), and the work of each would merge by itself." + yet},
		}},
		{name: "needs", plan: planOf(t, 1, taskNeeding("a", "b", "dispatch://ACME-2/artifact/plan-md#b-c"), taskNeeding("b"), taskNeeding("c")),
			want: []Refusal{
				{Task: "", Reason: "the plan has 3 tasks (a, b, c), and this release runs one task for each issue." + yet},
				{Task: "a", Reason: "task a needs b, c." + yet},
			}},
		{name: "a step that reads a pull request before the implementer", plan: oneTask("a", "reviewer implementer"),
			want: []Refusal{{Task: "a", Reason: "step 1 of task a names reviewer, and no step before it names the role implementer, whose pull request reviewer reads." + yet}}},
		{name: "a task with no implementer", plan: oneTask("a", "tester reviewer"), want: []Refusal{
			{Task: "a", Reason: "task a has no step implementer." + yet},
			{Task: "a", Reason: "step 1 of task a names tester, and no step before it names the role implementer, whose pull request tester reads." + yet},
			{Task: "a", Reason: "step 2 of task a names reviewer, and no step before it names the role implementer, whose pull request reviewer reads." + yet},
		}},
		{name: "a role two times in a task", plan: oneTask("a", "implementer reviewer reviewer"),
			want: []Refusal{{Task: "a", Reason: "task a names the role reviewer at steps 2 and 3." + yet}}},
		{name: "acceptance with no key", plan: oneTask("a", "implementer acceptance"),
			want: []Refusal{{Task: "a", Reason: "step 2 of task a names acceptance with no key, which asks for a fresh agent of the role acceptance." + yet}}},
		{name: "a key", plan: oneTask("a", "implementer:cd reviewer"),
			want: []Refusal{{Task: "a", Reason: "step 1 of task a names the key cd." + yet}}},
		{name: "the key owner on a role but acceptance", plan: oneTask("a", "implementer:owner"),
			want: []Refusal{{Task: "a", Reason: "step 1 of task a names the key owner." + yet}}},
		{name: "a key on acceptance but owner", plan: oneTask("a", "implementer acceptance:r"),
			want: []Refusal{{Task: "a", Reason: "step 2 of task a names the key r." + yet}}},
		{name: "a role block", plan: func() Plan { p := oneTask("a", "implementer"); p.RoleBlocks = []string{"prober"}; return p }(),
			want: []Refusal{{Task: "", Reason: "the plan defines the role prober." + yet}}},
		{name: "merges", plan: withTask(Task{ID: "a", Merges: true, Steps: implementer}),
			want: []Refusal{{Task: "a", Reason: "task a names merges." + yet}}},
		{name: "owner", plan: withTask(Task{ID: "f", Owner: "architect"}),
			want: []Refusal{{Task: "f", Reason: "task f names owner." + yet}}},
		{name: "part_of", plan: withTask(Task{ID: "f1", PartOf: "f", Steps: implementer}),
			want: []Refusal{{Task: "f1", Reason: "task f1 names part_of." + yet}}},
		{name: "two causes in one task", plan: oneTask("a", "reviewer implementer reviewer"), want: []Refusal{
			{Task: "a", Reason: "task a names the role reviewer at steps 1 and 3." + yet},
			{Task: "a", Reason: "step 1 of task a names reviewer, and no step before it names the role implementer, whose pull request reviewer reads." + yet},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Supported(tc.plan); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Supported =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}

	for _, steps := range []string{"implementer reviewer", "planner implementer tester reviewer acceptance:owner", "implementer acceptance:owner", "implementer"} {
		if r := Supported(oneTask("a", steps)); len(r) != 0 {
			t.Fatalf("Supported(%s) = %+v, want none", steps, r)
		}
	}

	// Both checks run, so a plan with causes of each kind gets the reasons of both, as one refusal.
	twoTasks := oneTask("a", "implementer prober")
	twoTasks.Tasks = append(twoTasks.Tasks, Task{ID: "b", Steps: implementer})
	got := Refusals(twoTasks, Deployment{Roles: BuiltinRoles()})
	want := []Refusal{
		{Task: "", Reason: "the plan has 2 tasks (a, b), and this release runs one task for each issue." + yet},
		{Task: "", Reason: "the plan has 2 tasks that no task needs (a, b), and the work of each would merge by itself." + yet},
		{Task: "a", Reason: "step 2 of task a names the role prober, which neither the deployment nor the plan defines."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("two tasks and prober =\n%+v\nwant\n%+v", got, want)
	}
}

// A task names its steps in any order. The planner stands at any place, and the tester, the
// reviewer and the owner's acceptance each come after the implementer, in any order among them. A
// role named at two steps stays refused in this release. The twin: today's line, the steps an
// issue with no plan rows runs, is taken.
func TestSupportedTakesStepsInAnyOrder(t *testing.T) {
	for _, steps := range []string{"implementer reviewer tester", "implementer tester reviewer", "implementer acceptance:owner reviewer",
		"implementer reviewer acceptance:owner tester", "implementer planner", "planner implementer reviewer", "implementer reviewer planner tester",
		"planner implementer tester reviewer acceptance:owner"} {
		p := oneTask("a", steps)
		if r := append(Supported(p), Validate(p, Deployment{Roles: BuiltinRoles()})...); len(r) != 0 {
			t.Fatalf("%s refused: %+v", steps, r)
		}
	}
	want := Refusal{Task: "a", Reason: "task a names the role tester at steps 2 and 4. This release of Legion does not carry it out yet."}
	if r := Supported(oneTask("a", "implementer tester reviewer tester")); len(r) != 1 || r[0] != want {
		t.Fatalf("got %+v", r)
	}
}

// The tester, the reviewer and the owner's acceptance read the pull request the implementer opens
// (hasOpenOrMergedPR, the one guard of a move on), so this release refuses a step of one of those
// roles with no implementer step before it, by its role and with its word as the plan writes it.
// The planner reads no pull request, so it stands anywhere.
func TestAStepThatReadsAPullRequestNeedsAnImplementerBeforeIt(t *testing.T) {
	for _, tc := range []struct{ steps, reason string }{
		{"tester implementer", "step 1 of task a names tester, and no step before it names the role implementer, whose pull request tester reads."},
		{"reviewer implementer", "step 1 of task a names reviewer, and no step before it names the role implementer, whose pull request reviewer reads."},
		{"acceptance:owner implementer", "step 1 of task a names acceptance:owner, and no step before it names the role implementer, whose pull request acceptance:owner reads."},
		{"planner reviewer implementer", "step 2 of task a names reviewer, and no step before it names the role implementer, whose pull request reviewer reads."},
	} {
		want := []Refusal{{Task: "a", Reason: tc.reason + yet}}
		if r := Supported(oneTask("a", tc.steps)); !reflect.DeepEqual(r, want) {
			t.Fatalf("Supported(%s) =\n%+v\nwant\n%+v", tc.steps, r, want)
		}
	}
	if r := Supported(oneTask("a", "implementer planner")); len(r) != 0 {
		t.Fatalf("Supported(implementer planner) = %+v, want none", r)
	}
	// The plan `tester` alone has two causes, in the order Supported builds them, and both stand.
	want := []Refusal{
		{Task: "a", Reason: "task a has no step implementer." + yet},
		{Task: "a", Reason: "step 1 of task a names tester, and no step before it names the role implementer, whose pull request tester reads." + yet},
	}
	if r := Supported(oneTask("a", "tester")); !reflect.DeepEqual(r, want) {
		t.Fatalf("Supported(tester) =\n%+v\nwant\n%+v", r, want)
	}
}

// numbered is count words, prefix then a number of five digits from 1, so no word holds another.
func numbered(prefix string, count int) []string {
	words := make([]string, count)
	for i := range words {
		words[i] = fmt.Sprintf("%s%05d", prefix, i+1)
	}
	return words
}

// One refusal of a plan names the first MostReasons causes in the order the checks find them and
// counts the rest in one more reason, of the whole plan, so its size does not grow with the plan.
// Each check that finds a cause for each item of the plan is held to it: a `needs` word that names
// no task, a role nobody defines, a step's key, and a step that reads a pull request with no
// implementer before it (which comes after the role named at two steps); and Supported's and
// Validate's causes share the one bound. A plan of MostReasons causes or fewer gets every cause
// and no count. Each case holds the word or the step each named reason names, in order, and the
// count; none holds a sentence. The fault is a refusal that names every cause; the control is
// reasons.add with its bound taken out, under which each case past the bound names every cause.
func TestARefusalNamesItsFirstReasonsAndCountsTheRest(t *testing.T) {
	d := Deployment{Roles: BuiltinRoles()}
	steps := func(words ...[]string) Plan { return oneTask("a", strings.Join(slices.Concat(words...), " ")) }
	keyed := func(role string, keys []string) []string {
		words := make([]string, len(keys))
		for i, key := range keys {
			words[i] = role + ":" + key
		}
		return words
	}
	testers := func(count int) []string { return slices.Repeat([]string{"tester"}, count) }
	// stepAt names the steps from 1 to count, as a reason of a step names it.
	stepAt := func(count int) []string {
		words := make([]string, count)
		for i := range words {
			words[i] = fmt.Sprintf("step %d of", i+1)
		}
		return words
	}
	implementer := []string{"implementer"}
	const past = MostReasons + 7
	for _, tc := range []struct {
		name  string
		plan  Plan
		named []string // what each named reason names, in order
		left  int      // how many causes the last reason counts
	}{
		{name: "needs words that name no task, past the bound", plan: planOf(t, 1, taskNeeding("a", numbered("x", past)...)),
			named: numbered("x", MostReasons), left: 7},
		{name: "needs words that name no task, at the bound", plan: planOf(t, 1, taskNeeding("a", numbered("x", MostReasons)...)),
			named: numbered("x", MostReasons)},
		{name: "roles nobody defines, past the bound", plan: steps(implementer, numbered("r", past)), named: numbered("r", MostReasons), left: 7},
		{name: "roles nobody defines, at the bound", plan: steps(implementer, numbered("r", MostReasons)), named: numbered("r", MostReasons)},
		// Each step reviewer:kNNNNN names a key, and then the role reviewer is named at two steps.
		{name: "keys, past the bound", plan: steps(implementer, keyed("reviewer", numbered("k", past))), named: numbered("k", MostReasons), left: 8},
		{name: "keys, at the bound", plan: steps(implementer, keyed("reviewer", numbered("k", MostReasons-1))),
			named: append(numbered("k", MostReasons-1), "reviewer at steps 2 and 3")},
		// The role tester is named at two steps, and then each tester step has no implementer before it.
		{name: "steps with no implementer before them, past the bound", plan: steps(testers(past), implementer),
			named: append([]string{"tester at steps 1 and 2"}, stepAt(MostReasons-1)...), left: 8},
		{name: "steps with no implementer before them, at the bound", plan: steps(testers(MostReasons-1), implementer),
			named: append([]string{"tester at steps 1 and 2"}, stepAt(MostReasons-1)...)},
		{name: "Supported's causes, then Validate's, share the bound", plan: steps(implementer, keyed("reviewer", numbered("k", 12)), numbered("r", 12)),
			named: slices.Concat(numbered("k", 12), []string{"reviewer at steps 2 and 3"}, numbered("r", 7)), left: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Refusals(tc.plan, d)
			named := got
			if tc.left > 0 && len(got) > 0 {
				named = got[:len(got)-1]
				count := got[len(got)-1]
				if count.Task != "" || !strings.Contains(count.Reason, fmt.Sprintf(" %d ", tc.left)) {
					t.Errorf("the last reason is %+v, want one of the whole plan that counts %d more", count, tc.left)
				}
			}
			if len(named) != len(tc.named) {
				t.Fatalf("the refusal names %d causes, want %d, then a count of %d: %+v", len(named), len(tc.named), tc.left, got)
			}
			for i, item := range tc.named {
				if named[i].Task != "a" || !strings.Contains(named[i].Reason, item) {
					t.Errorf("reason %d is %+v, want one of task a naming %q", i+1, named[i], item)
				}
			}
		})
	}
}
