package plan

import (
	"fmt"
	"slices"
	"strings"
)

// The two checks a plan passes before Legion takes it. Both run on every plan, and each gives one
// reason for each cause it finds, so a plan with two causes gets two reasons, and the owner reads
// every cause at once rather than one for each try. Each reason names its task where it has one.
// One refusal names at most MostReasons of them and counts the rest (reasons).

// MostReasons is the most reasons one refusal of a plan names; one more reason counts the rest
// (reasons). The owner fixes what the refusal names and gives the plan again, and the next refusal
// names what is left. Twenty is one screen of sentences of 50 to 170 bytes, a few kilobytes in an
// answer that writes each reason twice, which a terminal and an agent's tool result both hold, and
// enough causes to show a slip the plan makes at each of its items, such as a reference to the
// blocks of another document in each need or a key on each step.
const MostReasons = 20

// noTask is the reason a plan with no task block gets. No later release takes such a plan, so it
// is Validate's and does not say "yet".
const noTask = "the plan has no task block, so it gives Legion no work."

// notYet ends each reason of Supported: what a later release carries out.
const notYet = " This release of Legion does not carry it out yet."

// reasons is the reasons of one refusal of a plan, as Supported and Validate find them: the first
// MostReasons in the order found, then a count of the rest. Every cause of either check is added
// here, so a plan whose blocks give a cause for each of their items (each `needs` word that names
// no task, each role nobody defines, each step's key, each step that reads a pull request with no
// implementer before it) gets a refusal of MostReasons reasons and the count, however many items
// it holds. One reason for each such word made a 409 answer of about 30 MB from a 1 MiB attribute.
type reasons struct {
	named []Refusal
	left  int
}

// add is one cause of task, "" for a cause of the whole plan: named while fewer than MostReasons
// are, counted past them, so reason is called only for a reason the refusal names.
func (r *reasons) add(task string, reason func() string) {
	if len(r.named) == MostReasons {
		r.left++
		return
	}
	r.named = append(r.named, Refusal{Task: task, Reason: reason()})
}

// yet is add of a cause a later release carries out (Supported's): its reason ends notYet.
func (r *reasons) yet(task string, reason func() string) {
	r.add(task, func() string { return reason() + notYet })
}

// refusals is the reasons named, then, past MostReasons, one reason of the whole plan that counts
// the rest, so the answer says how many it leaves out.
func (r *reasons) refusals() []Refusal {
	if r.left == 0 {
		return r.named
	}
	return append(r.named, Refusal{Reason: fmt.Sprintf(
		"Legion found %d more reasons, which this answer leaves out: it names the first %d.", r.left, MostReasons)})
}

// Refusals is every reason Legion does not take p, as one refusal names them (reasons): Supported's,
// what this release does not carry out yet, then Validate's, what no release can. Both checks run,
// so a plan with a cause of each kind gets both, and the two share the one bound.
func Refusals(p Plan, d Deployment) []Refusal {
	var found reasons
	supported(p, &found)
	validate(p, d, &found)
	return found.refusals()
}

// Validate is what Legion cannot carry out in any release: a plan with no task; for each task,
// each `needs` word that names no task of the plan, in the block's order (Task.Unresolved, which
// FromBlocks fills), and each step that names a role neither d nor the plan's own role blocks
// define, refused once for each role at the first step that names it; then one cycle of each set
// of tasks that need each other (cycles), named by its first task in id order with the chain from
// that task. It refuses no plan for a step the plan lacks. The other refusals of a plan no release
// carries out need an attribute or a block that Supported refuses in this release, so the release
// that lifts that refusal adds them here. It is one refusal's reasons (reasons).
func Validate(p Plan, d Deployment) []Refusal {
	var found reasons
	validate(p, d, &found)
	return found.refusals()
}

// validate adds Validate's causes to found.
func validate(p Plan, d Deployment, found *reasons) {
	if len(p.Tasks) == 0 {
		found.add("", func() string { return noTask })
		return
	}
	defined := make(map[string]bool, len(p.RoleBlocks))
	for _, role := range p.RoleBlocks {
		defined[role] = true
	}
	for _, task := range p.Tasks {
		for _, word := range task.Unresolved {
			found.add(task.ID, func() string {
				return fmt.Sprintf("task %s needs %s, which names no task of this plan.", task.ID, word)
			})
		}
		refused := map[string]bool{}
		for i, step := range task.Steps {
			if _, ok := d.Roles[step.Role]; ok || defined[step.Role] || refused[step.Role] {
				continue
			}
			refused[step.Role] = true
			found.add(task.ID, func() string {
				return fmt.Sprintf("step %d of task %s names the role %s, which neither the deployment nor the plan defines.", i+1, task.ID, step.Role)
			})
		}
	}
	sets, _ := cycles(p.Tasks)
	for _, cycle := range sets {
		found.add(cycle[0], func() string {
			chain := make([]string, len(cycle))
			for i, id := range cycle {
				chain[i] = id + " needs " + cycle[(i+1)%len(cycle)]
			}
			return fmt.Sprintf("task %s needs itself: %s.", cycle[0], strings.Join(chain, ", "))
		})
	}
}

// cycles is one cycle of needs for each set of tasks that need each other: each strongly
// connected component of the tasks' needs that holds a cycle, which is one of more than one task
// or one task that needs itself (components, one pass over the tasks and their needs). The cycle
// is the shortest from the set's first task in id order back to that task, found breadth first
// inside the set (cycleFrom), as Dispatch's spec graph names its loops (findLoops in
// packages/envoy/internal/dispatch/specgraph). So the check takes time linear in the tasks and
// their needs, and a set gives one reason however many cycles run through it: a search from
// every need, which named the shortest cycle through each one, was quadratic on a ring of tasks,
// and Dispatch takes an owner's plan of 10,000 of them. Each cycle starts at its set's first task,
// and the cycles are in the order of those tasks' ids. A need is a task id (FromBlocks); one that
// names no task of tasks closes no cycle. followed is how many needs cycleFrom follows over all
// the sets, at most the needs of tasks, since a task joins at most one search.
func cycles(tasks []Task) (found [][]string, followed int) {
	at := make(map[string]int, len(tasks))
	for i, task := range tasks {
		at[task.ID] = i
	}
	needs := make([][]int, len(tasks))
	for i, task := range tasks {
		for _, id := range task.Needs {
			if needed, ok := at[id]; ok {
				needs[i] = append(needs[i], needed)
			}
		}
	}
	component, count := components(needs)
	// Each component's first task in id order, and whether it holds a cycle.
	first := make([]int, count)
	members := make([]int, count)
	itself := make([]bool, count)
	for i, task := range tasks {
		c := component[i]
		if members[c] == 0 || task.ID < tasks[first[c]].ID {
			first[c] = i
		}
		members[c]++
		itself[c] = itself[c] || slices.Contains(needs[i], i)
	}
	parent := make([]int, len(tasks))
	for i := range parent {
		parent[i] = -1
	}
	for c := range count {
		if members[c] == 1 && !itself[c] {
			continue
		}
		path, more := cycleFrom(needs, component, parent, first[c])
		followed += more
		cycle := make([]string, len(path))
		for i, task := range path {
			cycle[i] = tasks[task].ID
		}
		found = append(found, cycle)
	}
	slices.SortFunc(found, slices.Compare)
	return found, followed
}

// components numbers the strongly connected components of the graph in which task i needs each
// task of needs[i], by Tarjan's algorithm kept on explicit stacks, so a chain of 10,000 needs
// costs no call stack: component[i] is task i's component, of count in all.
func components(needs [][]int) (component []int, count int) {
	const unvisited = -1
	// order is the order each task was first reached in, and low the earliest order reached from
	// it through the tasks still on the stack.
	order := make([]int, len(needs))
	low := make([]int, len(needs))
	onStack := make([]bool, len(needs))
	component = make([]int, len(needs))
	for i := range order {
		order[i] = unvisited
	}
	var stack []int
	// visiting is the path of tasks being visited, each with the index of its next need.
	type visit struct{ task, next int }
	var visiting []visit
	reached := 0
	enter := func(task int) {
		order[task], low[task] = reached, reached
		reached++
		stack = append(stack, task)
		onStack[task] = true
		visiting = append(visiting, visit{task: task})
	}
	for root := range needs {
		if order[root] != unvisited {
			continue
		}
		enter(root)
		for len(visiting) > 0 {
			top := &visiting[len(visiting)-1]
			if top.next < len(needs[top.task]) {
				needed := needs[top.task][top.next]
				top.next++
				switch {
				case order[needed] == unvisited:
					enter(needed)
				case onStack[needed]:
					low[top.task] = min(low[top.task], order[needed])
				}
				continue
			}
			task := top.task
			visiting = visiting[:len(visiting)-1]
			if len(visiting) > 0 {
				caller := visiting[len(visiting)-1].task
				low[caller] = min(low[caller], low[task])
			}
			if low[task] != order[task] {
				continue
			}
			for {
				member := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[member] = false
				component[member] = count
				if member == task {
					break
				}
			}
			count++
		}
	}
	return component, count
}

// cycleFrom is the shortest cycle from task back to task inside its component: the tasks from
// task to the last before it again, each needing the next, found breadth first with each task's
// needs in the order its block writes them. parent holds -1 for each task of the component;
// cycleFrom marks the tasks it reaches there, and no later call reads those marks, since each
// component is searched once. followed is how many needs it follows.
func cycleFrom(needs [][]int, component, parent []int, task int) (path []int, followed int) {
	parent[task] = task
	queue := []int{task}
	for head := 0; head < len(queue); head++ {
		at := queue[head]
		for _, needed := range needs[at] {
			followed++
			if needed == task {
				path = []int{at}
				for at != task {
					at = parent[at]
					path = append(path, at)
				}
				slices.Reverse(path)
				return path, followed
			}
			if component[needed] == component[task] && parent[needed] < 0 {
				parent[needed] = at
				queue = append(queue, needed)
			}
		}
	}
	// A component of more than one task leads from each of its tasks back to it, and one of one
	// task holds a cycle only when that task needs itself: the search finds that need in its first
	// round, among the task's own needs, whichever place the block writes it at.
	panic(fmt.Sprintf("plan: task %d leads back to itself by no need inside its component", task))
}

// Supported is what this release does not carry out yet: more than one task, more than one
// task that no task needs (Sinks, over the task ids FromBlocks reads), whose work would each
// merge by itself where this release merges an issue's work as one change, a role block, and,
// for each task, `needs`, `owner`, `part_of` and `merges`, each key but the owner's on
// acceptance, acceptance with no key, a role named at two steps, no implementer, and each step
// of a role that reads the implementer's pull request with no implementer step before it. Its
// steps stand in any other order: the planner at any place, and the tester, the reviewer and the
// acceptance in any order after the implementer. A task that names no steps runs the project's
// default steps, which the configuration loader holds to these same checks, so none of its step
// causes apply. It is one refusal's reasons (reasons).
func Supported(p Plan) []Refusal {
	var found reasons
	supported(p, &found)
	return found.refusals()
}

// supported adds Supported's causes to found.
func supported(p Plan, found *reasons) {
	if len(p.Tasks) > 1 {
		found.yet("", func() string {
			ids := make([]string, len(p.Tasks))
			for i, task := range p.Tasks {
				ids[i] = task.ID
			}
			return fmt.Sprintf("the plan has %d tasks (%s), and this release runs one task for each issue.", len(p.Tasks), strings.Join(ids, ", "))
		})
	}
	if sinks := Sinks(p.Tasks); len(sinks) > 1 {
		found.yet("", func() string {
			return fmt.Sprintf("the plan has %d tasks that no task needs (%s), and the work of each would merge by itself.", len(sinks), strings.Join(sinks, ", "))
		})
	}
	for _, role := range p.RoleBlocks {
		found.yet("", func() string { return "the plan defines the role " + role + "." })
	}
	for _, task := range p.Tasks {
		taskReasons(task, found)
	}
}

// taskReasons adds Supported's causes in one task to found: its attributes first, then its steps
// (stepReasons).
func taskReasons(t Task, found *reasons) {
	if len(t.Needs) > 0 {
		found.yet(t.ID, func() string { return fmt.Sprintf("task %s needs %s.", t.ID, strings.Join(t.Needs, ", ")) })
	}
	if t.Owner != "" {
		found.yet(t.ID, func() string { return fmt.Sprintf("task %s names owner.", t.ID) })
	}
	if t.PartOf != "" {
		found.yet(t.ID, func() string { return fmt.Sprintf("task %s names part_of.", t.ID) })
	}
	if t.Merges {
		found.yet(t.ID, func() string { return fmt.Sprintf("task %s names merges.", t.ID) })
	}
	if t.NamesSteps() {
		stepReasons(t, found)
	}
}

// readsPullRequest is each role whose step reads the pull request the implementer's step opens:
// the tester and the reviewer judge its head, and the owner's acceptance reads the evidence of it.
// The implementer's move on waits for that pull request (the workflow's guard hasOpenOrMergedPR),
// so such a step with no implementer step before it has no pull request to read, and this release
// refuses it; a later release that gives such a step another pull request to read takes it. The
// role decides, never the step's type.
var readsPullRequest = map[string]bool{"tester": true, "reviewer": true, "acceptance": true}

// stepReasons adds Supported's causes in the steps of t to found, in this order: each step's key,
// in step order; each role named at two steps, at its first two; no implementer; and each step of
// a role that reads a pull request with no implementer step before it, in step order, named by
// its word as the plan writes it.
func stepReasons(t Task, found *reasons) {
	firstAt := map[string]int{}
	var roles []string // each role once, in the order of its first step
	secondAt := map[string]int{}
	hasImplementer := false
	for i, step := range t.Steps {
		at := i + 1
		switch {
		case step.Role == "acceptance" && step.Key == "":
			found.yet(t.ID, func() string {
				return fmt.Sprintf("step %d of task %s names acceptance with no key, which asks for a fresh agent of the role acceptance.", at, t.ID)
			})
		case step.Key != "" && (step.Role != "acceptance" || step.Key != "owner"):
			found.yet(t.ID, func() string { return fmt.Sprintf("step %d of task %s names the key %s.", at, t.ID, step.Key) })
		}
		if _, seen := firstAt[step.Role]; !seen {
			firstAt[step.Role] = at
			roles = append(roles, step.Role)
		} else if _, seen := secondAt[step.Role]; !seen {
			secondAt[step.Role] = at
		}
		hasImplementer = hasImplementer || step.Role == "implementer"
	}
	for _, role := range roles {
		if second, ok := secondAt[role]; ok {
			found.yet(t.ID, func() string {
				return fmt.Sprintf("task %s names the role %s at steps %d and %d.", t.ID, role, firstAt[role], second)
			})
		}
	}
	if !hasImplementer {
		found.yet(t.ID, func() string { return fmt.Sprintf("task %s has no step implementer.", t.ID) })
	}
	for i, step := range t.Steps {
		if step.Role == "implementer" {
			break
		}
		if readsPullRequest[step.Role] {
			found.yet(t.ID, func() string {
				return fmt.Sprintf("step %d of task %s names %s, and no step before it names the role implementer, whose pull request %s reads.",
					i+1, t.ID, step, step)
			})
		}
	}
}
