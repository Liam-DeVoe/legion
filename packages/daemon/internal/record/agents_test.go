package record

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// Each agent of a version gets one name, the last part of its claim token: the steps of one key
// share it, a step with no key gets the role it is on, and the owner gets none, since its claim is
// the tree's architect. A later version keeps the name a key already has, and an agent beside it
// that would take the same name gets a name of its own, `implementer-2`.
func TestNameAgentsGivesOneNameToEachAgentAndKeepsAKeysName(t *testing.T) {
	steps := []PlanStep{
		{Task: "t", Position: 1, Role: "implementer", AgentKey: "a"},
		{Task: "t", Position: 2, Role: "tester", AgentKey: "a"},
		{Task: "t", Position: 3, Role: "reviewer"},
		{Task: "t", Position: 4, Role: "acceptance", AgentKey: "owner"},
	}
	got := NameAgents(nil, nil, steps)
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
	again := NameAgents(got, nil, later)
	if again[0].Agent != "implementer" {
		t.Errorf("key a in a later version: name %q, want implementer, the name it had", again[0].Agent)
	}
	if again[1].Agent != "implementer-2" {
		t.Errorf("a fresh implementer beside key a: name %q, want implementer-2, a name of its own", again[1].Agent)
	}
}

// A key keeps its name when a later version gives the role it was named for to a fresh step that
// comes before it: every key an earlier row names is named before any new name is given, so the
// fresh step cannot take it, and a step with no key keeps the name its place had.
func TestAKeyKeepsItsNameWhenALaterVersionGivesItsRoleToAFreshStep(t *testing.T) {
	v1 := NameAgents(nil, nil, []PlanStep{
		{Task: "t", Position: 1, Role: "implementer", AgentKey: "a"},
		{Task: "t", Position: 2, Role: "tester", AgentKey: "a"},
		{Task: "t", Position: 3, Role: "reviewer"},
	})
	v2 := NameAgents(v1, nil, []PlanStep{
		{Task: "t", Position: 1, Role: "implementer"},
		{Task: "t", Position: 2, Role: "tester", AgentKey: "a"},
		{Task: "t", Position: 3, Role: "reviewer"},
	})
	if v2[1].Agent != "implementer" {
		t.Errorf("key a in version 2: name %q, want implementer", v2[1].Agent)
	}
	if v2[0].Agent != "implementer-2" {
		t.Errorf("the fresh step 1 of version 2: name %q, want implementer-2, a name of its own", v2[0].Agent)
	}
	if v2[2].Agent != "reviewer" {
		t.Errorf("step 3 of version 2: name %q, want reviewer", v2[2].Agent)
	}
}

// A second agent of one role gets a name of its own: the role and the first number from 2 that no
// agent of the version and no earlier row of the issue holds, so two agents never share a name,
// and so a session. The role is cut so that the name has at most 32 bytes, the agent grammar's
// bound. The fault: step 3 of `implementer reviewer implementer` shares step 1's name, or gets
// none.
func TestASecondAgentOfOneRoleGetsANameOfItsOwn(t *testing.T) {
	steps := []PlanStep{
		{Task: "t", Position: 1, Role: "implementer"},
		{Task: "t", Position: 2, Role: "reviewer"},
		{Task: "t", Position: 3, Role: "implementer"},
	}
	if got := NameAgents(nil, nil, steps); got[0].Agent != "implementer" || got[2].Agent != "implementer-2" {
		t.Errorf("implementer reviewer implementer: names %q and %q, want implementer and implementer-2", got[0].Agent, got[2].Agent)
	}
	earlier := []PlanStep{{Task: "old", Position: 1, Role: "tester", AgentKey: "b", Agent: "implementer-2"}}
	if got := NameAgents(earlier, nil, steps); got[2].Agent != "implementer-3" {
		t.Errorf("step 3 beside an earlier row named implementer-2: name %q, want implementer-3", got[2].Agent)
	}
	long := strings.Repeat("r", 32)
	twice := NameAgents(nil, nil, []PlanStep{
		{Task: "t", Position: 1, Role: "implementer"},
		{Task: "t", Position: 2, Role: long},
		{Task: "t", Position: 3, Role: long},
	})
	if want := claim.Agent(strings.Repeat("r", 30) + "-2"); twice[2].Agent != want || len(twice[2].Agent) != 32 {
		t.Errorf("a second agent of a role of 32 bytes: name %q, want %q, 32 bytes", twice[2].Agent, want)
	}
	for _, step := range twice {
		if _, err := claim.AgentToken("legion", "LEGION-208", step.Agent); err != nil {
			t.Errorf("the name %q makes no claim token: %v", step.Agent, err)
		}
	}
}

// Rows written before a name a key held was kept for it can show two keys that held one name
// across generations: generation 1 names key a implementer, and in generation 2, where no step
// names key a, key b took implementer by pass 4. A later version that
// names both keys gives the name to one key alone, the one whose row held it last, key b, whose
// agent ran on that name's claim last; key a gets a name of its own, implementer-2. So one name is
// one claim and one session for one agent. The fault: pass 2 gave each key the name its first row
// had, both implementer, two agents on one claim and one session.
func TestTwoKeysThatHeldOneNameInTurnGetTwoNames(t *testing.T) {
	earlier := []PlanStep{
		{Generation: 1, Task: "t", Position: 1, Role: "implementer", AgentKey: "a", Agent: "implementer"},
		{Generation: 1, Task: "t", Position: 2, Role: "reviewer", Agent: "reviewer"},
		{Generation: 2, Task: "u", Position: 1, Role: "implementer", AgentKey: "b", Agent: "implementer"},
		{Generation: 2, Task: "u", Position: 2, Role: "reviewer", Agent: "reviewer"},
	}
	got := NameAgents(earlier, nil, []PlanStep{
		{Generation: 3, Task: "v", Position: 1, Role: "implementer", AgentKey: "a"},
		{Generation: 3, Task: "v", Position: 2, Role: "reviewer"},
		{Generation: 3, Task: "v", Position: 3, Role: "implementer", AgentKey: "b"},
	})
	if got[0].Agent == got[2].Agent {
		t.Fatalf("keys a and b both named %q, want two names", got[0].Agent)
	}
	if got[2].Agent != "implementer" || got[0].Agent != "implementer-2" {
		t.Errorf("key a named %q and key b %q, want key b implementer, the key that held it last, and key a implementer-2", got[0].Agent, got[2].Agent)
	}
	if again := NameAgents(append(earlier, got...), nil, []PlanStep{
		{Generation: 3, Task: "w", Position: 1, Role: "implementer", AgentKey: "a"},
		{Generation: 3, Task: "w", Position: 2, Role: "implementer", AgentKey: "b"},
	}); again[0].Agent != got[0].Agent || again[1].Agent != got[2].Agent {
		t.Errorf("a later version names key a %q and key b %q, want each the name it had, %q and %q", again[0].Agent, again[1].Agent, got[0].Agent, got[2].Agent)
	}
}

// A name an earlier row of a key held is that key's: no agent of a later version takes it but that
// key, so the claim of the name, and the session it holds, wait for the key across a version that
// leaves the key out. v1 `implementer:k reviewer` names key k implementer; v2 `implementer
// reviewer` gives step 1, which has no key, implementer-2, a name of its own, at the cost of a slot
// of its own; v3 `implementer:k reviewer` gives key k implementer again. Another key is held to the
// same rule, and so is a step with no key whose place had the name before the key took it. A name
// only steps with no key held stays free: a new task's step with no key takes the name of a
// stopped task's step of its role. The fault: the step with no key of v2 took implementer, so key
// k's return in v3 found that name's claim holding another agent's session, and started fresh.
func TestANameAKeyHeldIsKeptForThatKey(t *testing.T) {
	v1 := NameAgents(nil, nil, []PlanStep{
		{Generation: 1, Task: "a", Position: 1, Role: "implementer", AgentKey: "k"},
		{Generation: 1, Task: "a", Position: 2, Role: "reviewer"},
	})
	if v1[0].Agent != "implementer" {
		t.Fatalf("key k in version 1: name %q, want implementer", v1[0].Agent)
	}
	v2 := NameAgents(v1, nil, []PlanStep{
		{Generation: 1, Task: "a", Position: 1, Role: "implementer"},
		{Generation: 1, Task: "a", Position: 2, Role: "reviewer"},
	})
	if v2[0].Agent != "implementer-2" || v2[1].Agent != "reviewer" {
		t.Fatalf("version 2, which leaves key k out: names %q and %q, want implementer-2, a name of its own beside key k's, and reviewer",
			v2[0].Agent, v2[1].Agent)
	}
	// The version written replaces the steps of the task it gives, so the rows version 3 is named
	// from are version 2's.
	v3 := NameAgents(v2, nil, []PlanStep{
		{Generation: 1, Task: "a", Position: 1, Role: "implementer", AgentKey: "k"},
		{Generation: 1, Task: "a", Position: 2, Role: "reviewer"},
	})
	if v3[0].Agent != "implementer" {
		t.Errorf("key k back in version 3: name %q, want implementer, the name whose claim holds its session", v3[0].Agent)
	}
	if other := NameAgents(v1, nil, []PlanStep{
		{Generation: 1, Task: "a", Position: 1, Role: "implementer", AgentKey: "j"},
		{Generation: 1, Task: "a", Position: 2, Role: "reviewer"},
	}); other[0].Agent != "implementer-2" {
		t.Errorf("key j in the place of key k: name %q, want implementer-2, since key k held implementer", other[0].Agent)
	}
	// Generation 1 ran task a with a step with no key named implementer, and generation 2 gave
	// that place to key k, which took the name. A later version of generation 2 that takes the key
	// off again finds the place's name of generation 1, which key k holds now.
	keyed := []PlanStep{
		{Generation: 1, Task: "a", Position: 1, Role: "implementer", Agent: "implementer"},
		{Generation: 1, Task: "a", Position: 2, Role: "reviewer", Agent: "reviewer"},
		{Generation: 2, Task: "a", Position: 1, Role: "implementer", AgentKey: "k", Agent: "implementer"},
		{Generation: 2, Task: "a", Position: 2, Role: "reviewer", Agent: "reviewer"},
	}
	if place := NameAgents(keyed, nil, []PlanStep{
		{Generation: 2, Task: "a", Position: 1, Role: "implementer"},
		{Generation: 2, Task: "a", Position: 2, Role: "reviewer"},
	}); place[0].Agent != "implementer-2" {
		t.Errorf("a step with no key at a place whose name key k took: name %q, want implementer-2", place[0].Agent)
	}
	if free := NameAgents(v2, nil, []PlanStep{
		{Generation: 1, Task: "p", Position: 1, Role: "reviewer"},
	}); free[0].Agent != "reviewer" {
		t.Errorf("control: a new task's reviewer, where only a step with no key held reviewer: name %q, want reviewer", free[0].Agent)
	}
}

// A name whose claim holds the session of a key's agent is that key's, after the versions that
// left the key out have replaced every row that named it: version 2, `implementer reviewer`,
// replaced key k's rows, so version 3, `planner implementer reviewer`, is named from rows that name
// no key, and the claim implementer, whose session is key k's (KeySessionsOf), keeps implementer
// for key k. Version 3's implementer takes implementer-3, since version 2's row holds
// implementer-2, and version 4, `implementer:k reviewer`, gives key k implementer, the name whose
// claim holds its session; another key is given a name of its own. A claim whose session is a step's
// agent with no key, or the key owner's, or that records none, keeps no name. The fault: the rows
// alone named version 3's implementer implementer, the claim of key k's session.
func TestANameWhoseClaimHoldsAKeysSessionIsKeptForThatKey(t *testing.T) {
	token := func(name claim.Agent) claim.Token {
		got, err := claim.AgentToken("legion", "LEGION-208", name)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	sessions := KeySessionsOf([]IssueClaim{
		{Token: token("implementer"), Role: "implementer", SessionAgent: "key:k"},
		{Token: token("implementer-2"), Role: "implementer", SessionAgent: "step:1:a"},
		{Token: token("reviewer"), Role: "reviewer"},
		{Token: token("tester"), Role: "tester", SessionAgent: "key:owner"},
	})
	if want := (KeySessions{"implementer": "k"}); !maps.Equal(sessions, want) {
		t.Fatalf("the key sessions of the claims = %v, want %v", sessions, want)
	}
	v2 := []PlanStep{
		{Generation: 1, Task: "a", Position: 1, Role: "implementer", Agent: "implementer-2"},
		{Generation: 1, Task: "a", Position: 2, Role: "reviewer", Agent: "reviewer"},
	}
	v3 := NameAgents(v2, sessions, []PlanStep{
		{Generation: 1, Task: "a", Position: 1, Role: "planner"},
		{Generation: 1, Task: "a", Position: 2, Role: "implementer"},
		{Generation: 1, Task: "a", Position: 3, Role: "reviewer"},
	})
	if got := []claim.Agent{v3[0].Agent, v3[1].Agent, v3[2].Agent}; !slices.Equal(got, []claim.Agent{"planner", "implementer-3", "reviewer"}) {
		t.Fatalf("version 3, beside the claim implementer of key k's session: names %q, want planner, implementer-3 and reviewer", got)
	}
	if v4 := NameAgents(v3, sessions, []PlanStep{
		{Generation: 1, Task: "a", Position: 1, Role: "implementer", AgentKey: "k"},
		{Generation: 1, Task: "a", Position: 2, Role: "reviewer"},
	}); v4[0].Agent != "implementer" {
		t.Errorf("key k in version 4: name %q, want implementer, the name whose claim holds its session", v4[0].Agent)
	}
	if other := NameAgents(v3, sessions, []PlanStep{
		{Generation: 1, Task: "a", Position: 1, Role: "implementer", AgentKey: "j"},
	}); other[0].Agent == "implementer" {
		t.Errorf("key j beside the claim implementer of key k's session: name %q, want a name of its own", other[0].Agent)
	}
}
