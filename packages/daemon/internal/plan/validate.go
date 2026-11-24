package plan

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// The two checks a plan passes before Legion takes it. Both run on every plan, and each gives one
// reason for each cause it finds, so a plan with two causes gets two reasons, and the owner reads
// every cause at once rather than one for each try. Each reason names its task where it has one.

// noTask is the reason a plan with no task block gets. No later release takes such a plan, so it
// is Validate's and does not say "yet".
const noTask = "the plan has no task block, so it gives Legion no work."

// notYet ends each reason of Supported: what a later release carries out.
const notYet = " This release of Legion does not carry it out yet."

// Validate is what Legion cannot carry out in any release: a plan with no task; each task block
// whose `parent` names an id of the plan's document that no typed block of the version holds
// (Task.MissingParent), in the document's order; each element that names `steps`, `owner` or
// `merges` (Element.Names), in the document's order, since Legion does not carry out an element;
// for each task, each `needs` word that names no task of the plan, in the block's order
// (Task.Unresolved, which FromBlocks fills), then each `needs` word that names an element
// (Task.ElementNeeds), then each `needs` word that is a `dispatch://` reference not of the plan's
// issue, in the block's order (Task.OutsideIssue), one into another issue naming that issue and one
// that starts with no issue key as naming none, and each step that names a role neither d nor the
// plan's own role blocks define, refused once for each role at the first step that names it; then
// each cycle of needs (cycles), named by its first task in id order with the chain from that task;
// then the causes of the tasks with a sub-owner and their parts (partRefusals). It refuses no plan
// for a step the plan lacks.
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
		if task.MissingParent != "" {
			refusals = append(refusals, Refusal{Task: task.ID, Reason: fmt.Sprintf("task block %s names %s as its parent, which no typed block of version %d of the plan holds. "+
				"Name a typed block of this version in parent, or leave parent out.", task.ID, task.MissingParent, p.Version)})
		}
	}
	elements := make(map[string]Element, len(p.Elements))
	for _, element := range p.Elements {
		elements[element.ID] = element
		if len(element.Names) == 0 {
			continue
		}
		under := fmt.Sprintf("%s, which names no sub-owner", element.Under)
		if element.Under != element.Of {
			under = fmt.Sprintf("%s, an element of %s", element.Under, element.Of)
		}
		refusals = append(refusals, Refusal{Task: element.ID, Reason: fmt.Sprintf("task block %s is an element of task %s: it stands under %s. "+
			"Legion does not carry out an element, so an element names no steps, owner or merges, and %s names %s.",
			element.ID, element.Of, under, element.ID, wordList(element.Names))})
	}
	for _, task := range p.Tasks {
		for _, word := range task.Unresolved {
			refusals = append(refusals, Refusal{Task: task.ID, Reason: fmt.Sprintf("task %s needs %s, which names no task of this plan.", task.ID, word)})
		}
		for _, word := range task.ElementNeeds {
			refusals = append(refusals, Refusal{Task: task.ID, Reason: fmt.Sprintf("task %s needs %s, which is an element of task %s. "+
				"Legion does not carry out an element, so a task cannot need one.", task.ID, word, elements[neededID(word)].Of)})
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
	return append(refusals, partRefusals(p.Tasks)...)
}

// partRefusals is Validate's causes of the tasks with a sub-owner and their parts, in task id
// order, a task's own causes together, one for each cause:
//   - a task that names both steps and a sub-owner, whose work is its parts;
//   - for each of its needs, in its block's order, a need of a part of a task it stands outside
//     (R14): it needs the whole task, and no part of it. The task it stands outside is the
//     outermost task above the part that does not hold it, so a part may need its siblings and
//     the parts of a task above it, and a task of the top level the whole task alone;
//   - for each of its needs, a need that is done only after the task itself starts, so the task
//     never starts and the tasks above it are never done (neverStarted);
//   - a task whose sub-owner's agent takes the name (SubOwnerAgent) of the agent of a task before
//     it in id order: one claim cannot be two sub-owners.
//
// A part may need a task of the top level that does not wait for its own task, which Legion
// carries out.
func partRefusals(tasks []Task) []Refusal {
	w := newWaits(tasks)
	refused := w.neverStarted()
	ids := slices.Sorted(maps.Keys(w.task))
	named := map[claim.Agent]string{}
	var refusals []Refusal
	for _, id := range ids {
		task := w.task[id]
		if task.Owner != "" && task.NamesSteps() {
			refusals = append(refusals, Refusal{Task: id, Reason: fmt.Sprintf(
				"task %s names both steps and the sub-owner %s, and a task with a sub-owner has no steps: its work is its parts.", id, task.Owner)})
		}
		for _, need := range task.Needs {
			if outside, ok := w.outsideOf(id, need); ok {
				refusals = append(refusals, Refusal{Task: id, Reason: fmt.Sprintf(
					"task %s needs %s, a part of task %s: a task outside %s needs the whole task %s, and no part of it.", id, need, outside, outside, outside)})
			}
			if reason, ok := refused[[2]string{id, need}]; ok {
				refusals = append(refusals, Refusal{Task: id, Reason: reason})
			}
		}
		if task.Owner == "" {
			continue
		}
		agent := SubOwnerAgent(id)
		if first, taken := named[agent]; taken {
			refusals = append(refusals, Refusal{Task: id, Reason: fmt.Sprintf(
				"task %s names a sub-owner, and its agent takes the name %s, which the agent of the sub-owner of task %s takes too: give one of the two tasks another id.",
				id, agent, first)})
			continue
		}
		named[agent] = id
	}
	return refusals
}

// waits is the tasks of a plan as the rule of a task's start and end reads them: a task starts once
// each task it needs is done and, for a part, once its task has started (it stands at its parts),
// and a task is done only after it started and each of its parts is done. A need is a task id
// (FromBlocks); one that names no task of the plan is Validate's to refuse, and waits on nothing.
type waits struct {
	tasks []Task
	task  map[string]Task
	parts map[string][]string
}

// newWaits is the waits of tasks, each task's parts in id order.
func newWaits(tasks []Task) waits {
	w := waits{tasks: tasks, task: make(map[string]Task, len(tasks)), parts: map[string][]string{}}
	for _, task := range tasks {
		w.task[task.ID] = task
		if task.PartOf != "" {
			w.parts[task.PartOf] = append(w.parts[task.PartOf], task.ID)
		}
	}
	for _, parts := range w.parts {
		slices.Sort(parts)
	}
	return w
}

// above is each task that id is a part of, the nearest first. Places refuses parents in a cycle, and
// the walk stops at a task it has passed, so it ends for any plan.
func (w waits) above(id string) []string {
	var tasks []string
	seen := map[string]bool{id: true}
	for at := w.task[id].PartOf; at != "" && !seen[at]; at = w.task[at].PartOf {
		seen[at] = true
		tasks = append(tasks, at)
	}
	return tasks
}

// holds says whether task holds id: id is task, or a part of it, directly or through its tasks.
func (w waits) holds(task, id string) bool {
	return id == task || slices.Contains(w.above(id), task)
}

// outsideOf is the task that id stands outside where its need need is a part of it (R14): the
// outermost task above need that does not hold id. ok is false where need is no part, or where each
// task above need holds id: a part's sibling, and a part of a task above it.
func (w waits) outsideOf(id, need string) (string, bool) {
	outside := ""
	for _, above := range w.above(need) {
		if w.holds(above, id) {
			break
		}
		outside = above
	}
	return outside, outside != ""
}

// waitNode is one moment of a task the rule of its start and end reads: its start, or its end.
type waitNode struct {
	task  string
	start bool
}

// waitEdge is one need of a plan: the task, and the task it needs.
type waitEdge = [2]string

// next is each moment node waits for, with the words that say why, as a reason writes them, and
// none for an end's wait for its own start, which a reason does not need to say. cut are the needs
// left out, each refused by another cause. The end of a task waits for its start and for the end
// of each of its parts, in id order; the start of a task waits for the end of each task it needs,
// in its block's order, and the start of a part for the start of its task.
func (w waits) next(node waitNode, cut map[waitEdge]bool) ([]waitNode, []string) {
	var nodes []waitNode
	var words []string
	if !node.start {
		nodes, words = append(nodes, waitNode{node.task, true}), append(words, "")
		for _, part := range w.parts[node.task] {
			nodes = append(nodes, waitNode{part, false})
			words = append(words, fmt.Sprintf("%s is done only after its part %s", node.task, part))
		}
		return nodes, words
	}
	for _, need := range w.task[node.task].Needs {
		if _, known := w.task[need]; known && !cut[waitEdge{node.task, need}] {
			nodes = append(nodes, waitNode{need, false})
			words = append(words, fmt.Sprintf("%s needs %s", node.task, need))
		}
	}
	if of := w.task[node.task].PartOf; of != "" {
		nodes = append(nodes, waitNode{of, true})
		words = append(words, fmt.Sprintf("%s, a part of %s, starts only once %s has started", node.task, of, of))
	}
	return nodes, words
}

// waitPath is the fewest waits from from to to, each as the words that say it, the words of an
// end's wait for its own start left out, and ok false where to is not reached.
func (w waits) waitPath(from, to waitNode, cut map[waitEdge]bool) ([]string, bool) {
	type step struct {
		from  waitNode
		words string
	}
	reached := map[waitNode]step{from: {}}
	for queue := []waitNode{from}; len(queue) > 0; queue = queue[1:] {
		at := queue[0]
		if at == to {
			var words []string
			for at != from {
				if s := reached[at]; s.words != "" {
					words = append(words, s.words)
				}
				at = reached[at].from
			}
			slices.Reverse(words)
			return words, true
		}
		nodes, words := w.next(at, cut)
		for i, node := range nodes {
			if _, seen := reached[node]; !seen {
				reached[node] = step{from: at, words: words[i]}
				queue = append(queue, node)
			}
		}
	}
	return nil, false
}

// neverStarted is the reason of each need of a task that is done only after that task itself
// starts, keyed by the task and the need, so the task never starts and the tasks above it are never
// done. A need that a cycle of needs alone closes is left to cycles, and a need of a part of a task
// its task stands outside to R14 (outsideOf). The need that causes the wait is named, not each
// need the wait then holds back, in two passes over the needs, each in task id order and each
// task's needs in its block's order:
//  1. A part's need of a task it is a part of, directly or through its tasks; a part's need of a
//     task outside a task above it whose end waits for that task's end; and a task's need of a part
//     of its own, which starts only once the task has started.
//  2. Each other need still done only after its task starts, with the needs the first pass named
//     left out, so a plan this function answers nothing for holds no task that never starts.
//
// A need of a task it is a part of takes the reason of S3: the task is done only after each of its
// parts. Each other reason names the need and the waits from its end to the task's start.
func (w waits) neverStarted() map[waitEdge]string {
	cut := map[waitEdge]bool{}
	for _, cycle := range cycles(w.tasks) {
		for i, id := range cycle {
			cut[waitEdge{id, cycle[(i+1)%len(cycle)]}] = true
		}
	}
	ids := slices.Sorted(maps.Keys(w.task))
	for _, id := range ids {
		for _, need := range w.task[id].Needs {
			if _, ok := w.outsideOf(id, need); ok {
				cut[waitEdge{id, need}] = true
			}
		}
	}
	reasons := map[waitEdge]string{}
	refuse := func(id, need string) {
		edge := waitEdge{id, need}
		if slices.Contains(w.above(id), need) {
			reasons[edge] = fmt.Sprintf("task %s needs %s, which it is a part of, and %s is done only after each of its parts.", id, need, need)
		} else {
			words, _ := w.waitPath(waitNode{need, false}, waitNode{id, true}, cut)
			reasons[edge] = fmt.Sprintf("task %s needs %s, which is done only after %s starts: %s.", id, need, id, wordList(words))
		}
		cut[edge] = true
	}
	causes := func(id, need string) bool {
		if w.holds(id, need) {
			return need != id
		}
		for _, above := range w.above(id) {
			if need == above {
				return true
			}
			if _, ends := w.waitPath(waitNode{need, false}, waitNode{above, false}, cut); ends && !w.holds(above, need) {
				return true
			}
		}
		return false
	}
	for pass := 1; pass <= 2; pass++ {
		for _, id := range ids {
			for _, need := range w.task[id].Needs {
				if _, known := w.task[need]; !known || cut[waitEdge{id, need}] {
					continue
				}
				if _, never := w.waitPath(waitNode{need, false}, waitNode{id, true}, cut); never && (pass == 2 || causes(id, need)) {
					refuse(id, need)
				}
			}
		}
	}
	return reasons
}

// wordList is words joined as a sentence lists them: "a", "a and b", "a, b and c".
func wordList(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
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

// Supported is what this release does not carry out yet: a role block, and, for each task,
// `owner`, a task that is a part of another (Places), the key owner on a role but acceptance,
// acceptance with a key but owner or with none, a role named at two steps, no implementer, each
// step of a role that reads the implementer's pull request with no implementer step before it,
// and each step after the
// implementer's whose role d gives the type implementation, since Legion's retro is done by the
// implementer. A plan of several tasks, and a task's `needs`, are taken: each task runs once the
// tasks it needs are done. So is a plan in which the work of several tasks merges, each by itself
// (Merging). Any other key is taken: it names the agent of its steps (AgentOf). Its steps stand in
// any other order: the planner at any place, the tester, the reviewer and the acceptance in any
// order after the implementer, and a role of the deployment at any place. A task that names no
// steps runs the project's default steps, which the configuration loader holds to these same
// checks, so none of its step causes apply.
func Supported(p Plan, d Deployment) []Refusal {
	var refusals []Refusal
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
		reasons = append(reasons, fmt.Sprintf("task %s is a part of task %s.", t.ID, t.PartOf))
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
