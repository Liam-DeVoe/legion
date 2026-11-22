package plan

import (
	"fmt"
	"regexp"
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

// recordWord is the form of a role's name and of a role's type in Dispatch's step record, which
// takes no other, and recordForm is the end of each refusal of a word outside it.
var recordWord = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

const recordForm = "is not of the form [a-z][a-z0-9-]* with at most 32 characters: Dispatch's step record takes no other."

// Validate is what Legion cannot carry out in any release: a plan with no task; each role the plan
// defines whose name or type Dispatch's step record cannot hold, or whose name the deployment or
// Legion holds already (LegionNames); each role whose model the deployment does not list, at the
// first step of each task that names it, or for the whole plan where no step names it; each agent
// (AgentOf) whose steps name roles of two models or two harnesses, once, and each role the key
// owner does at a step of a task whose model is not the owner's, at its first such step; and each
// step that names a role neither d nor the plan defines, refused once for each role at the first
// step of each task that names it. A type no list holds, and a role with no type or no model, are
// taken. It refuses no plan for a step the plan lacks, and it holds a role's prompt to no bound of
// its own.
func Validate(p Plan, d Deployment) []Refusal {
	if len(p.Tasks) == 0 {
		return []Refusal{{Reason: noTask}}
	}
	roles := RolesOf(p, d)
	refusals := append(roleReasons(p, d), modelReasons(p, d, roles)...)
	refusals = append(refusals, agentReasons(p, d, roles)...)
	for _, task := range p.Tasks {
		refused := map[string]bool{}
		for i, step := range task.Steps {
			if _, ok := roles[step.Role]; ok || refused[step.Role] {
				continue
			}
			refused[step.Role] = true
			refusals = append(refusals, Refusal{Task: task.ID, Reason: fmt.Sprintf(
				"step %d of task %s names the role %s, which neither the deployment nor the plan defines.", i+1, task.ID, step.Role)})
		}
	}
	return refusals
}

// roleReasons is Validate's causes in the roles p defines, in the document's order and, for each
// role, in this order: a name outside recordWord, a name of a role of d or of LegionNames, and a
// type that is set and outside recordWord.
func roleReasons(p Plan, d Deployment) []Refusal {
	var refusals []Refusal
	for _, role := range p.Roles {
		if !recordWord.MatchString(role.Name) {
			refusals = append(refusals, Refusal{Reason: fmt.Sprintf("the plan defines the role %s, whose name %s", role.Name, recordForm)})
		}
		if _, ok := d.Roles[role.Name]; ok || slices.Contains(legionNames, role.Name) {
			refusals = append(refusals, Refusal{Reason: fmt.Sprintf(
				"the plan defines the role %s, which the deployment or Legion defines already; a role of a plan takes a name of its own.", role.Name)})
		}
		if role.Type != "" && !recordWord.MatchString(string(role.Type)) {
			refusals = append(refusals, Refusal{Reason: fmt.Sprintf("the plan defines the role %s with the type %s, which %s", role.Name, role.Type, recordForm)})
		}
	}
	return refusals
}

// modelReasons is Validate's causes in the models of roles, each role a step names as RolesOf
// answers it: each role whose model is set and is none of d's models, at the first step of each
// task that names it, in task and step order; then each role p defines that no step names, whose
// model is set and is none of d's models, in the document's order.
func modelReasons(p Plan, d Deployment, roles map[string]RoleDef) []Refusal {
	listed := "it lists none."
	if len(d.Models) > 0 {
		listed = "it lists " + strings.Join(d.Models, ", ") + "."
	}
	var refusals []Refusal
	named := map[string]bool{}
	for _, task := range p.Tasks {
		refused := map[string]bool{}
		for i, step := range task.Steps {
			named[step.Role] = true
			role, ok := roles[step.Role]
			if !ok || role.Model == "" || slices.Contains(d.Models, role.Model) || refused[step.Role] {
				continue
			}
			refused[step.Role] = true
			refusals = append(refusals, Refusal{Task: task.ID, Reason: fmt.Sprintf(
				"step %d of task %s names the role %s, whose model %s the deployment does not list; %s", i+1, task.ID, step.Role, role.Model, listed)})
		}
	}
	for _, role := range p.Roles {
		if named[role.Name] || role.Model == "" || slices.Contains(d.Models, role.Model) {
			continue
		}
		refusals = append(refusals, Refusal{Reason: fmt.Sprintf(
			"the plan defines the role %s with the model %s, which the deployment does not list; %s", role.Name, role.Model, listed)})
	}
	return refusals
}

// agentReasons is Validate's causes in the agents of p's steps (AgentOf), each step's role as roles
// answers it, in task and step order: for each agent of a key but owner, the first step whose role
// runs on another model or another harness than the role of the agent's first step, once for each
// agent; and for the key owner, in each task, the first step of each role whose model is not the
// owner's (d.Owner). Two roles of no model run on one model, the deployment's default. The owner
// does its step in its own session, so its harness is not compared. A step whose role nobody
// defines is passed over: Validate refuses it on its own.
func agentReasons(p Plan, d Deployment, roles map[string]RoleDef) []Refusal {
	type firstStep struct {
		task, role string
		place      int
		def        RoleDef
	}
	first := map[Agent]firstStep{}
	refused := map[Agent]bool{}
	var refusals []Refusal
	for _, task := range p.Tasks {
		ownersRefused := map[string]bool{}
		for i, step := range task.Steps {
			role, ok := roles[step.Role]
			if !ok {
				continue
			}
			agent := AgentOf(task.ID, i+1, step)
			if agent.Owner() {
				if role.Model != d.Owner.Model && !ownersRefused[step.Role] {
					ownersRefused[step.Role] = true
					refusals = append(refusals, Refusal{Task: task.ID, Reason: fmt.Sprintf(
						"step %d of task %s names %s, whose model %s is not the owner's (%s); the owner runs on its own model.",
						i+1, task.ID, step, modelName(role.Model), modelName(d.Owner.Model))})
				}
				continue
			}
			was, seen := first[agent]
			if !seen {
				first[agent] = firstStep{task: task.ID, role: step.Role, place: i + 1, def: role}
				continue
			}
			if refused[agent] || (role.Model == was.def.Model && role.Backend == was.def.Backend) {
				continue
			}
			refused[agent] = true
			runOn := fmt.Sprintf("two models (%s, %s); one agent runs on one model.", modelName(was.def.Model), modelName(role.Model))
			if role.Model == was.def.Model {
				runOn = fmt.Sprintf("two harnesses (%s, %s); one agent runs on one harness.", was.def.Backend, role.Backend)
			}
			refusals = append(refusals, Refusal{Task: task.ID, Reason: fmt.Sprintf(
				"the key %s names one agent for %s at step %d of task %s and %s at step %d of task %s, whose roles run on %s",
				agent.Key, was.role, was.place, was.task, step.Role, i+1, task.ID, runOn)})
		}
	}
	return refusals
}

// modelName is a model as a reason writes it: "" is the default model.
func modelName(model string) string {
	if model == "" {
		return "the default model"
	}
	return model
}

// Supported is what this release does not carry out yet: more than one task, and, for each task,
// `needs`, `owner`, `part_of` and `merges`. A role block is taken: a step can name a role p
// defines. A key on any step is taken: it names the agent of its steps (AgentOf), and the key
// owner names the owner of the task. A role can stand at two steps, of one agent or of two, and a
// second agent of one role gets a name of its own. Its steps stand in any order, a role of the
// deployment or of p at any place, and a task needs no step of any role: acceptance with no key is
// a fresh agent of the role acceptance, a step that reads its task's pull request where none exists
// reads what its task text names, and the agent of a task's last step of the type implementation
// does its retro, the owner that of a task with none (RetroAgent). A task that names no steps runs
// the project's default steps, which the configuration loader holds to these same checks.
func Supported(p Plan) []Refusal {
	var refusals []Refusal
	if len(p.Tasks) > 1 {
		ids := make([]string, len(p.Tasks))
		for i, task := range p.Tasks {
			ids[i] = task.ID
		}
		refusals = append(refusals, Refusal{Reason: fmt.Sprintf(
			"the plan has %d tasks (%s), and this release runs one task for each issue.", len(p.Tasks), strings.Join(ids, ", ")) + notYet})
	}
	for _, task := range p.Tasks {
		for _, reason := range taskReasons(task) {
			refusals = append(refusals, Refusal{Task: task.ID, Reason: reason + notYet})
		}
	}
	return refusals
}

// taskReasons is Supported's causes in one task: its attributes.
func taskReasons(t Task) []string {
	var reasons []string
	if len(t.Needs) > 0 {
		reasons = append(reasons, fmt.Sprintf("task %s needs %s.", t.ID, strings.Join(t.Needs, ", ")))
	}
	if t.Owner != "" {
		reasons = append(reasons, fmt.Sprintf("task %s names owner.", t.ID))
	}
	if t.PartOf != "" {
		reasons = append(reasons, fmt.Sprintf("task %s names part_of.", t.ID))
	}
	if t.Merges {
		reasons = append(reasons, fmt.Sprintf("task %s names merges.", t.ID))
	}
	return reasons
}
