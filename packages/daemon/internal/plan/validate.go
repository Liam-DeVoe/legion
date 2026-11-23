package plan

import (
	"fmt"
	"slices"
	"strings"
)

// The two checks a plan passes before Legion takes it. Both run on every plan, and each gives one
// reason for each cause it finds, so a plan with two causes gets two reasons, and the owner reads
// every cause at once rather than one for each try. Each reason names its task where it has one.

// noTask is the reason a plan with no task block gets. No later release takes such a plan, so it
// is Validate's and does not say "yet".
const noTask = "the plan has no task block, so it gives Legion no work."

// notYet ends each reason of Supported: what a later release carries out.
const notYet = " This release of Legion does not carry it out yet."

// Validate is what Legion cannot carry out in any release: a plan with no task; for each task,
// each `needs` word that names no task of the plan, in the block's order (Task.Unresolved, which
// FromBlocks fills), then each `needs` word that is a `dispatch://` reference not of the plan's
// issue, in the block's order (Task.OutsideIssue), one into another issue naming that issue and one
// that starts with no issue key as naming none, and each step that names a role neither d nor the
// plan's own role blocks define, refused once for each role at the first step that names it; then
// each cycle of needs (cycles), named by its first task in id order with the chain from that task.
// It refuses no plan for a step the plan lacks. The other refusals of a plan no release carries out
// need an attribute or a block that Supported refuses in this release, so the release that lifts
// that refusal adds them here.
func Validate(p Plan, d Deployment) []Refusal {
	if len(p.Tasks) == 0 {
		return []Refusal{{Reason: noTask}}
	}
	defined := make(map[string]bool, len(p.RoleBlocks))
	for _, role := range p.RoleBlocks {
		defined[role] = true
	}
	var refusals []Refusal
	for _, task := range p.Tasks {
		for _, word := range task.Unresolved {
			refusals = append(refusals, Refusal{Task: task.ID, Reason: fmt.Sprintf("task %s needs %s, which names no task of this plan.", task.ID, word)})
		}
		for _, word := range task.OutsideIssue {
			var reason string
			if other, _ := issueOf(word); other != "" {
				reason = fmt.Sprintf("task %s needs %s, which names another issue, %s: "+
					"a task can need only a task of this plan, and an issue waits for another issue by a blocked_by link between the two.", task.ID, word, other)
			} else {
				reason = fmt.Sprintf("task %s needs %s, which names no issue: "+
					"a task can need only a task of this plan, and a reference to one starts with the plan's issue key.", task.ID, word)
			}
			refusals = append(refusals, Refusal{Task: task.ID, Reason: reason})
		}
		refused := map[string]bool{}
		for i, step := range task.Steps {
			if _, ok := d.Roles[step.Role]; ok || defined[step.Role] || refused[step.Role] {
				continue
			}
			refused[step.Role] = true
			refusals = append(refusals, Refusal{Task: task.ID, Reason: fmt.Sprintf(
				"step %d of task %s names the role %s, which neither the deployment nor the plan defines.", i+1, task.ID, step.Role)})
		}
	}
	for _, cycle := range cycles(p.Tasks) {
		chain := make([]string, len(cycle))
		for i, id := range cycle {
			chain[i] = id + " needs " + cycle[(i+1)%len(cycle)]
		}
		refusals = append(refusals, Refusal{Task: cycle[0], Reason: fmt.Sprintf("task %s needs itself: %s.", cycle[0], strings.Join(chain, ", "))})
	}
	return refusals
}

// cycles is the cycles of the tasks' needs that a reason names: for each need that lies on a
// cycle, the shortest cycle through it, once however many of its needs it is the shortest
// through. So each need that closes a cycle is in one of them, and the answer holds at most one
// cycle for each need. Each cycle is its tasks from its first in id order, and the cycles are in
// the order of their tasks' ids, compared one by one. A need is a task id (FromBlocks); one that
// names no task of tasks closes no cycle.
func cycles(tasks []Task) [][]string {
	needs := make(map[string][]string, len(tasks))
	for _, task := range tasks {
		needs[task.ID] = task.Needs
	}
	var found [][]string
	for _, task := range tasks {
		for _, needed := range task.Needs {
			back := shortestPath(needs, needed, task.ID)
			if back == nil {
				continue
			}
			cycle := append([]string{task.ID}, back[:len(back)-1]...)
			first := 0
			for i, id := range cycle {
				if id < cycle[first] {
					first = i
				}
			}
			cycle = append(slices.Clone(cycle[first:]), cycle[:first]...)
			if !slices.ContainsFunc(found, func(other []string) bool { return slices.Equal(other, cycle) }) {
				found = append(found, cycle)
			}
		}
	}
	slices.SortFunc(found, slices.Compare)
	return found
}

// shortestPath is the fewest tasks from from to to through needs, both ends included, each step
// from a task to one it needs, taken in the order its block writes them; from alone when it is
// to, and nil when no path leads there.
func shortestPath(needs map[string][]string, from, to string) []string {
	parent := map[string]string{from: from}
	for queue := []string{from}; len(queue) > 0; queue = queue[1:] {
		at := queue[0]
		if at == to {
			path := []string{at}
			for at != from {
				at = parent[at]
				path = append(path, at)
			}
			slices.Reverse(path)
			return path
		}
		for _, next := range needs[at] {
			if _, reached := parent[next]; !reached {
				parent[next] = at
				queue = append(queue, next)
			}
		}
	}
	return nil
}

// Supported is what this release does not carry out yet: more than one task that no task needs
// (Sinks, over the task ids FromBlocks reads), whose work would each merge by itself where this
// release merges an issue's work as one change, a role block, and, for each task, `owner`,
// `part_of` and `merges`, the key owner on a role but acceptance, acceptance with a key but owner
// or with none, a role named at two steps, no implementer, each step of a role that reads the
// implementer's pull request with no implementer step before it, and each step after the
// implementer's whose role d gives the type implementation, since Legion's retro is done by the
// implementer. A plan of several tasks, and a task's `needs`, are taken: each task runs once the
// tasks it needs are done. Any other key is taken: it names the agent of its steps (AgentOf). Its
// steps stand in any other order: the planner at any place, the tester, the reviewer and the
// acceptance in any order after the implementer, and a role of the deployment at any place. A
// task that names no steps runs the project's default steps, which the configuration loader holds
// to these same checks, so none of its step causes apply.
func Supported(p Plan, d Deployment) []Refusal {
	var refusals []Refusal
	if sinks := Sinks(p.Tasks); len(sinks) > 1 {
		refusals = append(refusals, Refusal{Reason: fmt.Sprintf(
			"the plan has %d tasks that no task needs (%s), and the work of each would merge by itself.", len(sinks), strings.Join(sinks, ", ")) + notYet})
	}
	for _, role := range p.RoleBlocks {
		refusals = append(refusals, Refusal{Reason: "the plan defines the role " + role + "." + notYet})
	}
	for _, task := range p.Tasks {
		for _, reason := range taskReasons(task, d) {
			refusals = append(refusals, Refusal{Task: task.ID, Reason: reason + notYet})
		}
	}
	return refusals
}

// taskReasons is Supported's causes in one task: its attributes first, then its steps (stepReasons).
func taskReasons(t Task, d Deployment) []string {
	var reasons []string
	if t.Owner != "" {
		reasons = append(reasons, fmt.Sprintf("task %s names owner.", t.ID))
	}
	if t.PartOf != "" {
		reasons = append(reasons, fmt.Sprintf("task %s names part_of.", t.ID))
	}
	if t.Merges {
		reasons = append(reasons, fmt.Sprintf("task %s names merges.", t.ID))
	}
	if t.NamesSteps() {
		reasons = append(reasons, stepReasons(t, d)...)
	}
	return reasons
}

// readsPullRequest is each role whose step reads the pull request the implementer's step opens:
// the tester and the reviewer judge its head, and the owner's acceptance reads the evidence of it.
// The implementer's move on waits for that pull request (the workflow's guard hasOpenOrMergedPR),
// so such a step with no implementer step before it has no pull request to read, and this release
// refuses it; a later release that gives such a step another pull request to read takes it. A
// role of the deployment reports pass or fail and reads no pull request, so it stands anywhere.
// The role decides, never the step's type.
var readsPullRequest = map[string]bool{"tester": true, "reviewer": true, "acceptance": true}

// stepReasons is Supported's causes in the steps of t, in this order: each step that names the
// key owner on a role but acceptance, or acceptance with a key but owner or with none, in step
// order; each role named at two steps, at its first two; no implementer; each step of a role that
// reads a pull request with no implementer step before it, in step order, named by its word as the
// plan writes it; and each step after the implementer's whose role d gives the type
// implementation, in step order. The owner does the accepting step in this release, so a key on
// it but owner would name an agent Legion does not run it in.
func stepReasons(t Task, d Deployment) []string {
	var keys, twice []string
	firstAt := map[string]int{}
	var roles []string // each role once, in the order of its first step
	secondAt := map[string]int{}
	implementerAt := 0 // the place of the first step of the implementer, 0 before it
	var unopened, retro []string
	for i, step := range t.Steps {
		at := i + 1
		switch {
		case step.Role == "acceptance" && step.Key == "":
			keys = append(keys, fmt.Sprintf("step %d of task %s names acceptance with no key, which asks for a fresh agent of the role acceptance.", at, t.ID))
		// The key owner and the role acceptance go together: either one without the other is refused.
		case step.Key != "" && (step.Key == "owner") != (step.Role == "acceptance"):
			keys = append(keys, fmt.Sprintf("step %d of task %s names the key %s.", at, t.ID, step.Key))
		}
		if _, seen := firstAt[step.Role]; !seen {
			firstAt[step.Role] = at
			roles = append(roles, step.Role)
		} else if _, seen := secondAt[step.Role]; !seen {
			secondAt[step.Role] = at
		}
		if readsPullRequest[step.Role] && implementerAt == 0 {
			unopened = append(unopened, fmt.Sprintf("step %d of task %s names %s, and no step before it names the role implementer, whose pull request %s reads; a role of this deployment that reports pass or fail needs none.",
				at, t.ID, step, step))
		}
		if implementerAt > 0 && step.Role != "implementer" && d.Roles[step.Role].Type == TypeImplementation {
			retro = append(retro, fmt.Sprintf("task %s names %s at step %d, a step of the type implementation after the step implementer at step %d, and Legion's retro is done by the implementer in this release.",
				t.ID, step.Role, at, implementerAt))
		}
		if implementerAt == 0 && step.Role == "implementer" {
			implementerAt = at
		}
	}
	for _, role := range roles {
		if second, ok := secondAt[role]; ok {
			twice = append(twice, fmt.Sprintf("task %s names the role %s at steps %d and %d.", t.ID, role, firstAt[role], second))
		}
	}
	reasons := append(keys, twice...)
	if implementerAt == 0 {
		reasons = append(reasons, fmt.Sprintf("task %s has no step implementer.", t.ID))
	}
	reasons = append(reasons, unopened...)
	return append(reasons, retro...)
}
