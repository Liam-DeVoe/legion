// Package plan is the plan the owner of an issue gives Legion: its tasks, the steps of each task,
// the roles those steps name, and the two checks a plan passes before Legion takes it — Validate,
// what Legion cannot carry out in any release (R14), and Supported, what this release does not
// carry out yet (C6). It reads the blocks of one version of the plan document (FromBlocks) and
// compares two versions (ChangedTasks); it holds no store and calls nothing.
//
// It imports internal/phase alone, so both the configuration loader, which reads a project's
// default steps with this grammar and these checks, and the workflow, which walks the line they
// make (Line), can import it.
package plan

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/phase"
)

// Type is the type of work a role does. Legion records it on each step and reads it for the
// returns of R7, the commit of R11 and the retro of R13, and for nothing else (R3).
type Type string

// The types Legion's list starts with (R3); a deployment adds its own in a later release.
const (
	TypeDesign         Type = "design"
	TypePlanning       Type = "planning"
	TypeImplementation Type = "implementation"
	TypeTesting        Type = "testing"
	TypeReview         Type = "review"
)

// Step is one step of a task: the role whose agent does it, and the key of that agent, which is
// empty for a fresh agent (C3, R6).
type Step struct{ Role, Key string }

// String is the step as a plan writes it: `role`, or `role:key`.
func (s Step) String() string {
	if s.Key == "" {
		return s.Role
	}
	return s.Role + ":" + s.Key
}

// Agent is who does one step: the owner of the task (Key "owner"), the agent of a key (Key set,
// Task "" and Place 0), or, for a step with no key, an agent of that step alone (Key "", Task and
// Place set). Two steps have one agent exactly when their Agent values are equal, so the steps of
// one key are one agent in any task, and a return to a step with no key finds the agent it had.
type Agent struct {
	Key   string
	Task  string
	Place int
}

// AgentOf is the agent of step place (from 1) of task, whose step is s.
func AgentOf(task string, place int, s Step) Agent {
	if s.Key != "" {
		return Agent{Key: s.Key}
	}
	return Agent{Task: task, Place: place}
}

// Owner says whether a is the owner of its task.
func (a Agent) Owner() bool { return a.Key == "owner" }

// DefaultTask is the id of the one task of an issue whose owner gives no plan, which runs the
// project's default steps (R5).
const DefaultTask = "default"

// Task is one `plan_task` block of a plan, as FromBlocks reads it.
type Task struct {
	// ID is the block's id, which every record of the task's steps names.
	ID string
	// Needs are the `needs` values, each kept whole as the block writes it: a reference to a
	// block or a word. This release only refuses them (Supported), so it decides nothing of how
	// one names a task.
	Needs []string
	// PartOf is `part_of` and Owner is `owner`, each as the block writes it, "" when unset.
	PartOf, Owner string
	// Merges is `merges`: true unless the block leaves the attribute out or writes `false`.
	Merges bool
	// HasSteps says whether the block names a step at all: a block with no `steps`, or with
	// `steps` of no word, has none, and runs the project's default steps (StepsOf, R5).
	HasSteps bool
	// Steps are the steps the block names, in order; nil when HasSteps is false.
	Steps []Step
	// Text is the block's text, which says what each step must do (R2) and which a later version
	// changes a task by (ChangedTasks).
	Text string
}

// RoleDef is a role a step can name: its name, its type, the phase of today's line it runs as in
// this release, where each step is a phase, and the model and the harness its agent starts on.
type RoleDef struct {
	Name  string
	Type  Type
	Phase phase.Phase
	// Model is the model its agent starts with: "" for the deployment's default model.
	Model string
	// Backend is the harness its agent runs on, in the words of runtime.Backend: "omp" or
	// "claude".
	Backend string
}

// PlanRole is one `plan_role` block: the role's name (the block's id), its type, its model (""
// names none, and the role's agent starts on the default model), and its prompt (the block's
// text).
type PlanRole struct {
	Name   string
	Type   Type
	Model  string
	Prompt string
}

// planRoleBackend is the harness of every role a plan defines: a role of a plan runs on Oh My Pi
// (runtime.BackendOmp's word), and no setting changes it.
const planRoleBackend = "omp"

// Plan is one version of an owner's plan document as Legion reads it.
type Plan struct {
	ArtifactID string
	Version    int
	// Tasks are the document's `plan_task` blocks, in its order.
	Tasks []Task
	// Roles are the roles the plan defines, one for each `plan_role` block, in the document's
	// order.
	Roles []PlanRole
}

// Deployment is what the deployment gives a plan: the roles a step can name, by name, the models a
// role may name, and the owner's own role. In this release its callers give BuiltinRoles alone
// (B-18).
type Deployment struct {
	Roles map[string]RoleDef
	// Models are the models the deployment lists.
	Models []string
	// Owner is the role of the owner of the plan's tasks: its name, its model and its harness.
	Owner RoleDef
}

// RolesOf is the roles the steps of p can name: each role of d, then each role p defines that d
// does not, as a RoleDef of its name, its type and its model on Oh My Pi. It is the one place that
// answers which role a step names, and of which type, model and harness. The answer is a fresh
// map, so a caller that changes it leaves d's roles whole.
func RolesOf(p Plan, d Deployment) map[string]RoleDef {
	roles := make(map[string]RoleDef, len(d.Roles)+len(p.Roles))
	for name, role := range d.Roles {
		roles[name] = role
	}
	for _, role := range p.Roles {
		if _, ok := roles[role.Name]; ok {
			continue
		}
		roles[role.Name] = RoleDef{Name: role.Name, Type: role.Type, Model: role.Model, Backend: planRoleBackend}
	}
	return roles
}

// Refusal is one reason Legion does not take a plan: the task it names, "" for a cause of the
// whole plan, and the reason as the owner reads it.
type Refusal struct{ Task, Reason string }

// BuiltinRoles is Legion's own roles of a step, each with its type and the phase it runs as: the
// planner, the implementer, the tester, the reviewer and acceptance, the owner's reading of the
// finished work. The architect, the merger and the controller are no roles of a step. The answer
// is a fresh map, so a caller that changes it leaves the next caller's answer whole.
func BuiltinRoles() map[string]RoleDef {
	roles := make(map[string]RoleDef, len(builtinRoles))
	for _, role := range builtinRoles {
		roles[role.Name] = role
	}
	return roles
}

// builtinRoles is BuiltinRoles in the order of today's line, which is the order Supported holds a
// task's steps to in this release.
var builtinRoles = []RoleDef{
	{Name: "planner", Type: TypePlanning, Phase: phase.Planning},
	{Name: "implementer", Type: TypeImplementation, Phase: phase.Implementing},
	{Name: "tester", Type: TypeTesting, Phase: phase.Testing},
	{Name: "reviewer", Type: TypeReview, Phase: phase.Reviewing},
	{Name: "acceptance", Type: TypeReview, Phase: phase.Accepting},
}

// nameWord is the grammar of a role and of a key (C3).
var nameWord = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ParseSteps reads the words of a `steps` attribute (C3): each word is `role` or `role:key`, a
// role and a key each matching [a-z][a-z0-9-]*, with white space between two words. Text of no
// word is no step. The key `owner` is reserved for the owner of the task: the grammar takes it,
// and Supported says which keys this release carries out.
func ParseSteps(text string) ([]Step, error) {
	words := strings.Fields(text)
	steps := make([]Step, 0, len(words))
	for _, word := range words {
		role, key, keyed := strings.Cut(word, ":")
		if !nameWord.MatchString(role) || keyed && !nameWord.MatchString(key) {
			return nil, fmt.Errorf("the step %q is neither a role nor a role:key, where each matches [a-z][a-z0-9-]*", word)
		}
		steps = append(steps, Step{Role: role, Key: key})
	}
	return steps, nil
}

// StepsOf is the steps task t runs: its own when its block names any, the project's defaults
// when it names neither steps nor a sub-owner (R5), and none for the task of a sub-owner, whose
// work is its parts. The answer is t's or defaults' own slice, which the caller does not change.
func StepsOf(t Task, defaults []Step) []Step {
	if t.HasSteps {
		return t.Steps
	}
	if t.Owner == "" {
		return defaults
	}
	return nil
}

// Line is the phases a task walks in this release, where each step runs as the phase of its
// role: the steps' phases in their order, then Legion's own retro and merge, which no plan names
// and no plan leaves out (R13). A role d does not define has no phase and stands in the line as
// "", which no row of the workflow moves into, so the caller walks only a plan Validate took.
func Line(steps []Step, d Deployment) []phase.Phase {
	line := make([]phase.Phase, 0, len(steps)+2)
	for _, step := range steps {
		line = append(line, d.Roles[step.Role].Phase)
	}
	return append(line, phase.Retro, phase.Merging)
}
