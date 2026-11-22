package plan

import "testing"

// The retro of a task is done by the agent of its last step of the type implementation, as the
// role of that step; where the task has no such step, or that step's key is owner, by the owner,
// on the implementer's row. The rule reads each step's type, never its role's name: a coder of the
// type implementation does its task's retro, and a task whose only step is of the type testing has
// the owner's.
func TestTheRetroIsTheLastImplementationStepsAgentElseTheOwners(t *testing.T) {
	owner := Agent{Key: "owner"}
	for _, tc := range []struct {
		steps     string
		types     []Type
		wantAgent func(steps []Step) Agent
		wantRole  string
	}{
		{"implementer tester", []Type{TypeImplementation, TypeTesting}, func(s []Step) Agent { return AgentOf("a", 1, s[0]) }, "implementer"},
		{"implementer:x reviewer implementer:y", []Type{TypeImplementation, TypeReview, TypeImplementation}, func(s []Step) Agent { return AgentOf("a", 3, s[2]) }, "implementer"},
		{"tester", []Type{TypeTesting}, func([]Step) Agent { return owner }, "implementer"},
		{"implementer:owner reviewer", []Type{TypeImplementation, TypeReview}, func([]Step) Agent { return owner }, "implementer"},
		{"implementer reviewer implementer:owner", []Type{TypeImplementation, TypeReview, TypeImplementation}, func([]Step) Agent { return owner }, "implementer"},
		{"coder checker", []Type{TypeImplementation, TypeTesting}, func(s []Step) Agent { return AgentOf("a", 1, s[0]) }, "coder"},
		{"checker", []Type{TypeTesting}, func([]Step) Agent { return owner }, "implementer"},
	} {
		steps, err := ParseSteps(tc.steps)
		if err != nil {
			t.Fatalf("ParseSteps(%s): %v", tc.steps, err)
		}
		agent, role := RetroAgent("a", steps, tc.types)
		if want := tc.wantAgent(steps); agent != want || role != tc.wantRole {
			t.Errorf("RetroAgent(%s, %v) = (%+v, %s), want (%+v, %s)", tc.steps, tc.types, agent, role, want, tc.wantRole)
		}
	}
}
