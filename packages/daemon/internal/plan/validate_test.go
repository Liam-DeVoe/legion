package plan

import (
	"reflect"
	"testing"
)

// yet ends each refusal of something a later release carries out.
const yet = " This release of Legion does not carry it out yet."

// builtin is the deployment of Legion's own roles alone.
var builtin = Deployment{Roles: BuiltinRoles()}

// What no release carries out, as this release checks it: a step that names a role neither the
// deployment nor the plan defines is refused, naming the step, its task and the role. Legion
// refuses no plan for a step that it lacks, so each plan of Legion's own roles is taken whatever
// steps it leaves out. The control is in the second half: a validator that also required a
// reviewer step would refuse `implementer tester`, and one that required a tester would refuse
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
// Each cause gets one reason, so a plan with two causes gets two. The taken plans are subsets of
// today's line, with the owner's acceptance, a fresh agent's, and a key on any role.
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
		}}, want: []Refusal{{Task: "", Reason: "the plan has 3 tasks (a, b, c), and this release runs one task for each issue." + yet}}},
		{name: "needs", plan: withTask(Task{ID: "a", Needs: []string{"b", "dispatch://ACME-2/artifact/plan-md#b-c"}, Steps: implementer}),
			want: []Refusal{{Task: "a", Reason: "task a needs b, dispatch://ACME-2/artifact/plan-md#b-c." + yet}}},
		{name: "a role two times in a task", plan: oneTask("a", "implementer reviewer reviewer"),
			want: []Refusal{{Task: "a", Reason: "task a names the role reviewer at steps 2 and 3." + yet}}},
		{name: "a role block", plan: func() Plan { p := oneTask("a", "implementer"); p.RoleBlocks = []string{"prober"}; return p }(),
			want: []Refusal{{Task: "", Reason: "the plan defines the role prober." + yet}}},
		{name: "merges", plan: withTask(Task{ID: "a", Merges: true, Steps: implementer}),
			want: []Refusal{{Task: "a", Reason: "task a names merges." + yet}}},
		{name: "owner", plan: withTask(Task{ID: "f", Owner: "architect"}),
			want: []Refusal{{Task: "f", Reason: "task f names owner." + yet}}},
		{name: "part_of", plan: withTask(Task{ID: "f1", PartOf: "f", Steps: implementer}),
			want: []Refusal{{Task: "f1", Reason: "task f1 names part_of." + yet}}},
		{name: "two causes in one task", plan: withTask(Task{ID: "a", Merges: true, Steps: []Step{{Role: "reviewer"}, {Role: "implementer"}, {Role: "reviewer"}}}), want: []Refusal{
			{Task: "a", Reason: "task a names merges." + yet},
			{Task: "a", Reason: "task a names the role reviewer at steps 1 and 3." + yet},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Supported(tc.plan); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Supported =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}

	for _, steps := range []string{"implementer reviewer", "planner implementer tester reviewer acceptance:owner", "implementer acceptance:owner", "implementer", "implementer:cd reviewer",
		"implementer acceptance", "tester reviewer", "reviewer implementer"} {
		if r := Supported(oneTask("a", steps)); len(r) != 0 {
			t.Fatalf("Supported(%s) = %+v, want none", steps, r)
		}
	}

	// Both checks run, so a plan with a cause of each gets two reasons, one from each.
	twoTasks := oneTask("a", "implementer prober")
	twoTasks.Tasks = append(twoTasks.Tasks, Task{ID: "b", Steps: implementer})
	got := append(Supported(twoTasks), Validate(twoTasks, builtin)...)
	want := []Refusal{
		{Task: "", Reason: "the plan has 2 tasks (a, b), and this release runs one task for each issue." + yet},
		{Task: "a", Reason: "step 2 of task a names the role prober, which neither the deployment nor the plan defines."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("two tasks and prober =\n%+v\nwant\n%+v", got, want)
	}
}

// A step can name its agent by a key on any role, and the steps of one key are one agent, so each
// plan below is taken: the agent r does the accepting step alone, one agent r does both steps,
// the owner does a step of the implementer, and one agent a does two roles.
func TestAKeyOnAnyStepIsTaken(t *testing.T) {
	for _, steps := range []string{"implementer acceptance:r", "implementer:r acceptance:r", "implementer:owner reviewer", "implementer:a tester:a reviewer"} {
		p := oneTask("t", steps)
		if got := append(Supported(p), Validate(p, builtin)...); len(got) != 0 {
			t.Errorf("%s refused: %+v, want it taken", steps, got)
		}
	}
}

// A task with no step implementer is taken, whatever steps it names: the owner does its retro
// (RetroAgent), and a step that reads its task's pull request reads what its task text names
// where none exists. So is acceptance with no key, a fresh agent of the role acceptance, and a
// step of the tester or the reviewer before the implementer.
func TestAPlanWithNoImplementerIsTaken(t *testing.T) {
	for _, steps := range []string{"tester", "reviewer", "acceptance:owner", "acceptance", "planner tester", "implementer:owner reviewer",
		"tester:owner", "implementer acceptance", "reviewer implementer"} {
		p := oneTask("a", steps)
		if got := Supported(p); got != nil {
			t.Errorf("Supported(%s) = %+v, want nil", steps, got)
		}
		if got := Validate(p, builtin); got != nil {
			t.Errorf("Validate(%s) = %+v, want nil", steps, got)
		}
	}
}

// A task names its steps in any order: the planner, the tester, the reviewer and the owner's
// acceptance each stand at any place. A role named at two steps stays refused in this release. The
// twin: today's line, the steps an issue with no plan rows runs, is taken.
func TestSupportedTakesStepsInAnyOrder(t *testing.T) {
	for _, steps := range []string{"implementer reviewer tester", "implementer tester reviewer", "implementer acceptance:owner reviewer",
		"implementer reviewer acceptance:owner tester", "implementer planner", "planner implementer reviewer", "implementer reviewer planner tester",
		"planner implementer tester reviewer acceptance:owner"} {
		p := oneTask("a", steps)
		if r := append(Supported(p), Validate(p, builtin)...); len(r) != 0 {
			t.Fatalf("%s refused: %+v", steps, r)
		}
	}
	want := Refusal{Task: "a", Reason: "task a names the role tester at steps 2 and 4. This release of Legion does not carry it out yet."}
	if r := Supported(oneTask("a", "implementer tester reviewer tester")); len(r) != 1 || r[0] != want {
		t.Fatalf("got %+v", r)
	}
}

// withRole is Legion's own roles and one role of the deployment, name, of type typ.
func withRole(name string, typ Type) Deployment {
	roles := BuiltinRoles()
	roles[name] = RoleDef{Name: name, Type: typ}
	return Deployment{Roles: roles}
}

// A step of any role stands at any place: a step that reads its task's pull request before any
// step of the implementer reads what its task text names where none exists, and a role of the
// deployment of the type implementation after the implementer does its task's retro
// (RetroAgent). Each plan below is taken, under a deployment whose coder does implementation work
// and whose critic does review work.
func TestAStepOfAnyRoleStandsAnywhere(t *testing.T) {
	d := withRole("coder", TypeImplementation)
	d.Roles["critic"] = RoleDef{Name: "critic", Type: TypeReview}
	for _, steps := range []string{"tester implementer", "reviewer implementer", "acceptance:owner implementer", "planner reviewer implementer",
		"implementer coder", "coder implementer", "critic implementer", "reviewer coder"} {
		p := oneTask("a", steps)
		if r := append(Supported(p), Validate(p, d)...); len(r) != 0 {
			t.Errorf("%s refused: %+v, want it taken", steps, r)
		}
	}
}

// A role runs at most one step of a task: a role of the deployment named at two steps is refused
// as Legion's own roles are, so neither an owner's plan (api's register_plan) nor a project's
// default steps (config's checkDefaultSteps), the two writers of a task's steps, can name one. The
// daemon reads the role of the step a task stands at as the step (record.Issue.HoldsQueued, the
// phases row of a role), which holds only while this does. The control: the same deployment's
// plan that names the role once, beside another role of the deployment, is taken.
func TestARoleOfTheDeploymentAtTwoStepsIsRefused(t *testing.T) {
	d := withRole("checker", TypeTesting)
	d.Roles["linter"] = RoleDef{Name: "linter", Type: TypeTesting}
	want := []Refusal{{Task: "a", Reason: "task a names the role checker at steps 2 and 4." + yet}}
	if r := Supported(oneTask("a", "implementer checker linter checker")); !reflect.DeepEqual(r, want) {
		t.Fatalf("Supported(implementer checker linter checker) =\n%+v\nwant\n%+v", r, want)
	}
	p := oneTask("a", "implementer checker linter")
	if r := append(Supported(p), Validate(p, d)...); len(r) != 0 {
		t.Fatalf("Supported(implementer checker linter) = %+v, want none", r)
	}
}
