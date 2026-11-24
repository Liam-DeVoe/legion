package record

import (
	"slices"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/plan"
)

// NameAgents is steps, copied, with the name of each step's agent (PlanStep.Agent) given: the one
// rule of a name, which reads no store of its own. earlier is every plan_steps row of the issue, of
// every generation (PlanStepsOf), read before the version's steps are written, since the write
// deletes the steps of each task it gives. It names in four passes, in this order, so that a key
// the version names never changes its name and no two agents of the version share one. A key the
// version no longer names frees its name: an agent of the version can take it, and with it that
// name's claim and session, as a new task's agent with no key takes the name of a stopped task's
// agent of its role.
//
//  1. A step of the key owner has no name: its agent is the tree's architect, whose token names the
//     tree and no name on the issue.
//  2. Each other key that an earlier row names takes the name of the newest such row
//     (earlierKeyNames), unless an agent named before it in this pass took that name: a key that
//     dropped out of a version freed its name for another, and then came back in a later
//     generation, which the generation's copy of its rows carries. Such a key keeps no name. Every
//     such key is named before any other pass gives a name.
//  3. Each step with no key takes the name of the latest earlier row of its task, place and role
//     with no key, unless pass 2 or an earlier step of this pass gave that name.
//  4. Each agent still with no name, in the order of its first step, takes the role of that first
//     step (RoleName), where no agent of steps has that name.
//
// An agent left with no name would share its name with another agent: this release runs one
// agent of each role for an issue, and the caller refuses such a version before it writes anything
// (UnnamedAgents).
//
// The steps of one key are one agent (PlanStep.PlanAgent), so they share one name in every task.
func NameAgents(earlier, steps []PlanStep) []PlanStep {
	named := slices.Clone(steps)
	keyNames := earlierKeyNames(earlier)
	names := map[plan.Agent]claim.Agent{}
	taken := map[claim.Agent]bool{}
	give := func(agent plan.Agent, name claim.Agent) {
		names[agent] = name
		if name != "" {
			taken[name] = true
		}
	}
	for _, step := range named {
		agent := step.PlanAgent()
		if _, done := names[agent]; done {
			continue
		}
		if agent.Owner() {
			give(agent, "")
			continue
		}
		name, ok := keyNames[agent.Key]
		switch {
		case agent.Key == "" || !ok:
		case taken[name]:
			give(agent, "")
		default:
			give(agent, name)
		}
	}
	for _, step := range named {
		agent := step.PlanAgent()
		if agent.Key != "" {
			continue
		}
		if name, ok := earlierPlaceName(earlier, step); ok && !taken[name] {
			give(agent, name)
		}
	}
	for _, step := range named {
		agent := step.PlanAgent()
		if _, ok := names[agent]; ok {
			continue
		}
		name := RoleName(step.Role)
		if taken[name] {
			give(agent, "")
			continue
		}
		give(agent, name)
	}
	for i := range named {
		named[i].Agent = names[named[i].PlanAgent()]
	}
	return named
}

// RoleName is the name an agent takes from the role of its first step (NameAgents' pass 4): the
// role itself. A step's role is a role of the deployment's or one of Legion's (plan.Supported
// refuses a plan's own role block), and config reads a role of the deployment's in the agent
// grammar already (its roleWord, at most maxRoleWord bytes), so a role needs no cut to be a name.
func RoleName(role string) claim.Agent { return claim.Agent(role) }

// UnnamedAgent is an agent of a version that NameAgents gave no name: the agent's first step, and
// the name it would have taken, which another agent of the version holds.
type UnnamedAgent struct {
	Step PlanStep
	Name claim.Agent
}

// UnnamedAgents is each agent of named, steps NameAgents gave from earlier, but the owner, that has
// no name, with the name it would have taken: a key's newest earlier name (earlierKeyNames), and
// otherwise its role's (RoleName). This release runs one agent of each role for an issue, so a
// version with one is refused before any write.
func UnnamedAgents(earlier, named []PlanStep) []UnnamedAgent {
	keyNames := earlierKeyNames(earlier)
	var unnamed []UnnamedAgent
	seen := map[plan.Agent]bool{}
	for _, step := range named {
		agent := step.PlanAgent()
		if seen[agent] || agent.Owner() {
			continue
		}
		seen[agent] = true
		if step.Agent != "" {
			continue
		}
		name, ok := keyNames[agent.Key]
		if agent.Key == "" || !ok {
			name = RoleName(step.Role)
		}
		unnamed = append(unnamed, UnnamedAgent{Step: step, Name: name})
	}
	return unnamed
}

// earlierKeyNames is the name each key but the owner's held in earlier, read newest first: the name
// of the key's row of the latest generation that names it. earlier is ordered by generation, as
// PlanStepsOf reads it, and the steps of one key in one generation share one name, so a key named
// again since an earlier generation keeps the name it holds now and not its oldest.
func earlierKeyNames(earlier []PlanStep) map[string]claim.Agent {
	names := map[string]claim.Agent{}
	for i := len(earlier) - 1; i >= 0; i-- {
		row := earlier[i]
		// AgentName is "" for a step of the key owner, as for a row that holds no name.
		name := row.AgentName()
		if row.AgentKey == "" || name == "" {
			continue
		}
		if _, ok := names[row.AgentKey]; !ok {
			names[row.AgentKey] = name
		}
	}
	return names
}

// earlierPlaceName is the name of the latest row of earlier at step's task and place, on step's
// role and with no key: the agent that place had, which a step with no key keeps (pass 3).
func earlierPlaceName(earlier []PlanStep, step PlanStep) (claim.Agent, bool) {
	for i := len(earlier) - 1; i >= 0; i-- {
		row := earlier[i]
		if row.AgentKey == "" && row.Task == step.Task && row.Position == step.Position && row.Role == step.Role {
			return row.AgentName(), true
		}
	}
	return "", false
}
