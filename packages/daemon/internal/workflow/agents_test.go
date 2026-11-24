package workflow

import (
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// agentsOfTask is the name of the agent of each stored step of task, in place order.
func agentsOfTask(stored *record.StoredPlan, task string) []claim.Agent {
	var names []claim.Agent
	for _, step := range stored.Steps[task] {
		names = append(names, step.Agent)
	}
	return names
}

// A name a key held is that key's alone: a later version in which a step with no key, or another
// key, would take it would run that agent in the name's claim and resume the key's session. Such a
// version is refused as a plan, 409 PLAN_REFUSED with R14's ending, naming the key that holds the
// name, before anything is written; the fact stays unspent, and version 1's rows, with their names,
// stand.
func TestALaterVersionThatWouldGiveAKeysNameToAnotherAgentIsRefusedAsAPlan(t *testing.T) {
	for _, tc := range []struct {
		name      string
		first     []string
		firstWant []claim.Agent
		later     []string
		place     int
		takes     string
		holder    string
	}{
		{name: "a step with no key", first: []string{"implementer:a", "tester:a", "reviewer"}, firstWant: []claim.Agent{"implementer", "implementer", "reviewer"},
			later: []string{"implementer", "tester:a", "reviewer"}, place: 1, takes: "implementer", holder: "a"},
		{name: "the key dropped and its step given to no key", first: []string{"implementer", "tester:k", "reviewer:k"}, firstWant: []claim.Agent{"implementer", "tester", "tester"},
			later: []string{"implementer", "tester", "reviewer"}, place: 2, takes: "tester", holder: "k"},
		{name: "the key dropped and its step given to another key", first: []string{"implementer", "tester:k", "reviewer:k"}, firstWant: []claim.Agent{"implementer", "tester", "tester"},
			later: []string{"implementer", "tester:m", "reviewer"}, place: 2, takes: "tester", holder: "k"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			engine := returnEngine()
			apply := applyFacts(t, pool, engine)
			root := stepRoot(t, pool, phase.Admitted)
			key, gen := root.Key, root.Generation
			mustTake(t, "version 1", apply(planID(key, gen, 1), ownersPlan(t, key, gen, 1, givenTask(t, "Build the widget.", tc.first...))))
			move(t, pool, engine, key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
			if result := apply("stop", ownersStop(t, root, "a")); result.Refusal != nil {
				t.Fatalf("the owner's stop was refused: %+v", result.Refusal)
			}
			before := planStandingOf(t, pool, engine, key, gen)
			if got := agentsOfTask(before.plan, "a"); !slices.Equal(got, tc.firstWant) {
				t.Fatalf("the agents of version 1 = %q, want %q", got, tc.firstWant)
			}

			id := planID(key, gen, 2)
			reason := intake.AgentUnnamed("a", tc.place, tc.takes, tc.holder)
			if !strings.Contains(reason.Reason, "key "+tc.holder) || !strings.Contains(reason.Reason, " "+tc.takes+",") {
				t.Fatalf("the refusal's reason %q names neither key %s nor the name %s", reason.Reason, tc.holder, tc.takes)
			}
			want := &intake.Refusal{Status: 409, Code: "PLAN_REFUSED", Message: "Legion refused this plan and recorded nothing: " + reason.Reason,
				Reasons: []plan.Refusal{reason}}
			assertPlanRefusal(t, pool, engine, key, gen, id,
				apply(id, ownersPlan(t, key, gen, 2, givenTask(t, "Build the widget.", tc.later...))), want, before)
		})
	}
}

// A version that gives a task again names its keys from the rows read before it replaces the
// task's steps, so a key keeps the name it had, even where its first step is now of another role.
func TestARegivenTaskKeepsTheNamesOfItsKeys(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	apply := applyFacts(t, pool, engine)
	root := stepRoot(t, pool, phase.Admitted)
	key, gen := root.Key, root.Generation
	mustTake(t, "version 1", apply(planID(key, gen, 1), ownersPlan(t, key, gen, 1, givenTask(t, "Build the widget.", "implementer", "tester:a", "reviewer"))))
	if got := agentsOfTask(planStandingOf(t, pool, engine, key, gen).plan, "a"); !slices.Equal(got, []claim.Agent{"implementer", "tester", "reviewer"}) {
		t.Fatalf("the agents of version 1 = %q, want implementer, key a named tester, and reviewer", got)
	}
	mustTake(t, "version 2", apply(planID(key, gen, 2), ownersPlan(t, key, gen, 2, givenTask(t, "Build the widget, and its manual.", "implementer:a", "tester:a", "reviewer"))))
	if got := agentsOfTask(planStandingOf(t, pool, engine, key, gen).plan, "a"); !slices.Equal(got, []claim.Agent{"tester", "tester", "reviewer"}) {
		t.Fatalf("the agents of version 2 = %q, want key a still named tester at steps 1 and 2, and reviewer", got)
	}
}

// The project's default steps are named from their own steps alone when the first step of a run
// with no owner's plan writes them: each agent has a name, the steps of a key share it.
func TestTheDefaultStepsAreNamedWhenTheyAreWritten(t *testing.T) {
	engine := returnEngine()
	engine.cfg.DefaultSteps = []plan.Step{{Role: "implementer"}, {Role: "tester", Key: "r"}, {Role: "reviewer", Key: "r"}}
	_, _, steps, handed := engine.defaultRows(record.Issue{Key: "LEGION-208", Generation: 1}, nil, nil)
	if len(handed) != 0 {
		t.Fatalf("the default steps with no earlier rows handed on %+v, want no name", handed)
	}
	var names []claim.Agent
	for _, step := range steps {
		names = append(names, step.Agent)
	}
	if want := []claim.Agent{"implementer", "tester", "tester"}; !slices.Equal(names, want) {
		t.Fatalf("the default steps are named %q, want %q", names, want)
	}
}

// The project's default steps of a new generation are named from the issue's earlier rows, so a
// name a key held stays that key's, but they cannot be refused as an owner's version is. Generation
// 1 ran `implementer tester:k reviewer:k`, key k named tester, whose claim records key k's session;
// generation 2 runs the default steps, whose tester step takes the name tester all the same. So the
// step is written with that claim's session to leave (record.PlanStep.LeaveSession), every start of
// the tester's step carries it (record.SuperviseRequest.LeaveSession), and the issue and the
// controller are told that the agent starts in a session of its own, which was key k's. No agent but
// key k resumes key k's session. The implementer's step, whose name no key held, carries none.
func TestTheDefaultStepsOfANewGenerationLeaveTheSessionOfAKeysName(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	apply := applyFacts(t, pool, engine)
	root := stepRoot(t, pool, phase.Admitted)
	key := root.Key
	mustTake(t, "version 1", apply(planID(key, 1, 1), ownersPlan(t, key, 1, 1, givenTask(t, "Build the widget.", "implementer", "tester:k", "reviewer:k"))))
	keyed := claim.Token(token(t, key, claim.RoleTester))
	if _, err := pool.Exec(t.Context(), `insert into claims (token, project, tree, issue, role, generation, session, session_file,
		state, launch_failures, prompt_failures, prompt_retires, uncertain_streak, suspended_by)
		values ($1, 'legion', $2, $2, 'reviewer', 3, 'ses-key-k', '/sessions/ses-key-k.jsonl', 'suspended', 0, 0, 0, 0, 'daemon')`,
		string(keyed), key); err != nil {
		t.Fatalf("seed key k's claim: %v", err)
	}
	next := root
	next.Generation = 2
	seedIssue(t, pool, next)

	move(t, pool, engine, key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	move(t, pool, engine, key, TriggerImplementationReady, record.TaskPlace{}, record.PhaseRow{})
	stored := planStandingOf(t, pool, engine, key, 2).plan
	steps := stored.Steps[defaultTask]
	if len(steps) < 2 || steps[1].Role != "tester" || steps[1].Agent != "tester" || steps[1].LeaveSession != "ses-key-k" || steps[0].LeaveSession != "" {
		t.Fatalf("generation 2's default steps are %+v, want the tester's step named tester leaving key k's session ses-key-k, and the implementer's leaving none", steps)
	}
	starts := superviseRowsOf(t, pool, key, "start")
	var tester, implementer []record.SuperviseRequest
	for _, start := range starts {
		switch start.Role {
		case claim.RoleTester:
			tester = append(tester, start)
		case claim.RoleImplementer:
			implementer = append(implementer, start)
		}
	}
	if len(tester) != 1 || tester[0].LeaveSession != "ses-key-k" {
		t.Fatalf("the tester's starts are %+v, want one carrying the session to leave, ses-key-k", tester)
	}
	for _, start := range implementer {
		if start.LeaveSession != "" {
			t.Fatalf("an implementer's start carries the session to leave %q, want none", start.LeaveSession)
		}
	}
	told := strings.Join(messageBodies(t, pool), "\n")
	if !strings.Contains(told, "key k") || !strings.Contains(told, `"ses-key-k"`) {
		t.Fatalf("the issue was told %q, want the tester's step's new session named, with key k and its session ses-key-k", told)
	}
}
