package record

import (
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// Each agent of a version gets one name, the last part of its claim token: the steps of one key
// share it, a step with no key gets the role it is on, and the owner gets none, since its claim is
// the tree's architect. A later version keeps the name a key already has, and an agent beside it
// that would take the same name gets none in this release.
func TestNameAgentsGivesOneNameToEachAgentAndKeepsAKeysName(t *testing.T) {
	steps := []PlanStep{
		{Task: "t", Position: 1, Role: "implementer", AgentKey: "a"},
		{Task: "t", Position: 2, Role: "tester", AgentKey: "a"},
		{Task: "t", Position: 3, Role: "reviewer"},
		{Task: "t", Position: 4, Role: "acceptance", AgentKey: "owner"},
	}
	got := NameAgents(nil, steps)
	want := []claim.Agent{"implementer", "implementer", "reviewer", ""}
	for i := range want {
		if got[i].Agent != want[i] {
			t.Errorf("step %d: name %q, want %q", i+1, got[i].Agent, want[i])
		}
	}
	if steps[0].Agent != "" {
		t.Errorf("NameAgents named the steps it was given (%q), want a copy named", steps[0].Agent)
	}
	later := []PlanStep{{Task: "u", Position: 1, Role: "tester", AgentKey: "a"}, {Task: "u", Position: 2, Role: "implementer"}}
	again := NameAgents(got, later)
	if again[0].Agent != "implementer" {
		t.Errorf("key a in a later version: name %q, want implementer, the name it had", again[0].Agent)
	}
	if again[1].Agent != "" {
		t.Errorf("a fresh implementer beside key a: name %q, want none in this release", again[1].Agent)
	}
}

// A key keeps its name when a later version gives the role it was named for to a fresh step that
// comes before it: every key an earlier row names is named before any new name is given, so the
// fresh step cannot take it, and a step with no key keeps the name its place had.
func TestAKeyKeepsItsNameWhenALaterVersionGivesItsRoleToAFreshStep(t *testing.T) {
	v1 := NameAgents(nil, []PlanStep{
		{Task: "t", Position: 1, Role: "implementer", AgentKey: "a"},
		{Task: "t", Position: 2, Role: "tester", AgentKey: "a"},
		{Task: "t", Position: 3, Role: "reviewer"},
	})
	v2 := NameAgents(v1, []PlanStep{
		{Task: "t", Position: 1, Role: "implementer"},
		{Task: "t", Position: 2, Role: "tester", AgentKey: "a"},
		{Task: "t", Position: 3, Role: "reviewer"},
	})
	if v2[1].Agent != "implementer" {
		t.Errorf("key a in version 2: name %q, want implementer", v2[1].Agent)
	}
	if v2[0].Agent != "" {
		t.Errorf("the fresh step 1 of version 2: name %q, want none in this release", v2[0].Agent)
	}
	if v2[2].Agent != "reviewer" {
		t.Errorf("step 3 of version 2: name %q, want reviewer", v2[2].Agent)
	}
}

// A row that a release before the column wrote has no name, and reads as its role, which named its
// agent then. A step of the key owner has no name whatever it holds: its agent is the tree's
// architect, whose token names the tree, not the issue.
func TestARowWrittenBeforeTheColumnReadsAsItsRole(t *testing.T) {
	if got := (PlanStep{Role: "reviewer"}).AgentName(); got != "reviewer" {
		t.Errorf("AgentName = %q, want reviewer", got)
	}
	if got := (PlanStep{Role: "acceptance", AgentKey: "owner"}).AgentName(); got != "" {
		t.Errorf("AgentName = %q, want none: the owner is the tree's architect", got)
	}
	if got := (PlanStep{Role: "tester", AgentKey: "a", Agent: "implementer"}).AgentName(); got != "implementer" {
		t.Errorf("AgentName = %q, want the name the row holds", got)
	}
}

// ofGeneration is steps as rows of generation gen of an issue's plan.
func ofGeneration(gen uint64, steps []PlanStep) []PlanStep {
	rows := make([]PlanStep, len(steps))
	for i, step := range steps {
		step.Generation = gen
		rows[i] = step
	}
	return rows
}

// agentNames is the name of each step of named, in order.
func agentNames(named []PlanStep) []claim.Agent {
	got := make([]claim.Agent, len(named))
	for i, step := range named {
		got[i] = step.Agent
	}
	return got
}

// A key that a version no longer names frees its name, and another key of a later generation takes
// it. When the first key comes back beside it, both would hold one name from the rows of the two
// generations, which the generation's copy keeps: the key named first in the version keeps it, and
// the other gets none, so the version is refused rather than run as one agent in one session.
func TestTwoKeysThatInheritOneNameAreNotBothNamed(t *testing.T) {
	first := ofGeneration(1, NameAgents(nil, []PlanStep{
		{Task: "t", Position: 1, Role: "implementer", AgentKey: "a"},
		{Task: "t", Position: 2, Role: "tester"},
		{Task: "t", Position: 3, Role: "reviewer"},
	}))
	second := ofGeneration(2, NameAgents(first, []PlanStep{
		{Task: "t", Position: 1, Role: "implementer", AgentKey: "b"},
		{Task: "t", Position: 2, Role: "tester"},
		{Task: "t", Position: 3, Role: "reviewer"},
	}))
	if second[0].Agent != "implementer" {
		t.Fatalf("key b, with key a dropped: name %q, want implementer, the name key a freed", second[0].Agent)
	}
	earlier := append(slices.Clone(first), second...)
	got := NameAgents(earlier, []PlanStep{
		{Task: "t", Position: 1, Role: "implementer", AgentKey: "b"},
		{Task: "t", Position: 2, Role: "tester", AgentKey: "a"},
		{Task: "t", Position: 3, Role: "reviewer"},
	})
	if want := []claim.Agent{"implementer", "", "reviewer"}; !slices.Equal(agentNames(got), want) {
		t.Fatalf("the names = %q, want %q: key b keeps implementer, and key a, whose generation-1 name key b holds, gets none", agentNames(got), want)
	}
	unnamed := UnnamedAgents(earlier, got)
	if len(unnamed) != 1 || unnamed[0].Step.Position != 2 || unnamed[0].Name != "implementer" {
		t.Fatalf("UnnamedAgents = %+v, want step 2's agent, key a, which would take implementer, its generation-1 name", unnamed)
	}
}

// A key's name is its newest: rows of an earlier generation that named it otherwise do not move it
// within a generation. The project's default steps name key a for its first role, implementer, with
// no earlier rows (defaultRows); the owner's version of that generation keeps that name, and not the
// name tester that key a had in generation 1.
func TestAKeysNameIsItsNewestAcrossGenerations(t *testing.T) {
	first := ofGeneration(1, NameAgents(nil, []PlanStep{
		{Task: "t", Position: 1, Role: "tester", AgentKey: "a"},
		{Task: "t", Position: 2, Role: "implementer"},
	}))
	defaults := ofGeneration(2, NameAgents(nil, []PlanStep{
		{Task: "t", Position: 1, Role: "implementer", AgentKey: "a"},
		{Task: "t", Position: 2, Role: "tester", AgentKey: "a"},
		{Task: "t", Position: 3, Role: "reviewer"},
	}))
	earlier := append(slices.Clone(first), defaults...)
	got := NameAgents(earlier, []PlanStep{
		{Task: "t", Position: 1, Role: "implementer", AgentKey: "a"},
		{Task: "t", Position: 2, Role: "tester", AgentKey: "a"},
		{Task: "t", Position: 3, Role: "reviewer"},
	})
	if want := []claim.Agent{"implementer", "implementer", "reviewer"}; !slices.Equal(agentNames(got), want) {
		t.Fatalf("the names = %q, want %q: key a keeps the name its generation's default steps gave it", agentNames(got), want)
	}
}

// A key the version drops frees its name for an agent of the version with no key, which takes it in
// pass 4 with the name's claim and session.
func TestADroppedKeysNameGoesToAnotherAgent(t *testing.T) {
	first := ofGeneration(1, NameAgents(nil, []PlanStep{
		{Task: "t", Position: 1, Role: "implementer", AgentKey: "a"},
		{Task: "t", Position: 2, Role: "tester", AgentKey: "a"},
		{Task: "t", Position: 3, Role: "reviewer"},
	}))
	got := NameAgents(first, []PlanStep{
		{Task: "t", Position: 1, Role: "implementer"},
		{Task: "t", Position: 2, Role: "tester"},
		{Task: "t", Position: 3, Role: "reviewer"},
	})
	if want := []claim.Agent{"implementer", "tester", "reviewer"}; !slices.Equal(agentNames(got), want) {
		t.Fatalf("the names = %q, want %q", agentNames(got), want)
	}
}

// Rows of the key owner in earlier name no agent: the owner's step of a later version still has no
// name, and no other key or step takes a name from the owner's rows.
func TestTheOwnersEarlierRowsNameNoAgent(t *testing.T) {
	earlier := ofGeneration(1, NameAgents(nil, []PlanStep{
		{Task: "t", Position: 1, Role: "implementer"},
		{Task: "t", Position: 2, Role: "acceptance", AgentKey: "owner"},
	}))
	earlier[1].Agent = "acceptance"
	got := NameAgents(earlier, []PlanStep{
		{Task: "t", Position: 1, Role: "implementer", AgentKey: "a"},
		{Task: "t", Position: 2, Role: "acceptance", AgentKey: "owner"},
	})
	if want := []claim.Agent{"implementer", ""}; !slices.Equal(agentNames(got), want) {
		t.Fatalf("the names = %q, want %q: the owner has no name whatever its earlier rows hold", agentNames(got), want)
	}
}

// UnnamedAgents names each agent NameAgents left with no name once, at its first step, with the
// name it would take: a key's newest earlier name, or else its role's. A named agent and the owner
// are not named, nor is a later step of an unnamed key.
func TestUnnamedAgentsNamesEachAgentWithNoNameAndTheNameItWouldTake(t *testing.T) {
	earlier := []PlanStep{
		{Generation: 1, Task: "t", Position: 1, Role: "implementer", AgentKey: "a", Agent: "tester"},
		{Generation: 2, Task: "t", Position: 1, Role: "implementer", AgentKey: "a", Agent: "critic"},
	}
	named := []PlanStep{
		{Task: "t", Position: 1, Role: "reviewer", AgentKey: "b", Agent: "reviewer"},
		{Task: "t", Position: 2, Role: "implementer", AgentKey: "a"},
		{Task: "t", Position: 3, Role: "tester", AgentKey: "a"},
		{Task: "t", Position: 4, Role: "reviewer"},
		{Task: "t", Position: 5, Role: "acceptance", AgentKey: "owner"},
	}
	got := UnnamedAgents(earlier, named)
	want := []UnnamedAgent{{Step: named[1], Name: "critic"}, {Step: named[3], Name: "reviewer"}}
	if !slices.Equal(got, want) {
		t.Fatalf("UnnamedAgents = %+v, want %+v", got, want)
	}
	if none := UnnamedAgents(nil, NameAgents(nil, named[:3])); len(none) != 0 {
		t.Fatalf("UnnamedAgents of a version every agent of which has a name = %+v, want none", none)
	}
}
