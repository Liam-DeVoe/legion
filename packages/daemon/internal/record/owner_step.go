package record

import (
	"fmt"

	"github.com/sjawhar/legion/daemon/internal/plan"
)

// OwnerStepNotRunning is the sentence of OWNER_STEP_NOT_RUNNING, the refusal of the owner's report
// of step (from 1, and 0 for the retro) of task on issue while the task, in state, stands at place
// on steps, its plan's rows (no rows where stored is false, an issue with no plan rows). It names
// where the task stands and who works that place, so the owner reads which report, if any, is its
// own now; where the task stands at the step named and the owner does it, what keeps that step from
// running is the task's state or the issue's phase (a hold), and it names those. The route
// complete_step and the workflow's check of the owner's report both write it (api's completeStep,
// workflow's ownerStepRefusal), so the two say one sentence.
func OwnerStepNotRunning(issue Issue, task string, stored bool, state TaskState, step int, place TaskPlace, steps []PlanStep) string {
	named := TaskPlace{Kind: PlaceStep, Position: step}
	if step == 0 {
		named = TaskPlace{Kind: PlaceRetro}
	}
	key := issue.Key
	reported := fmt.Sprintf("%s of task %s of %s", PlaceWords(named), task, key)
	if !stored {
		return fmt.Sprintf("%s is not a step of yours that runs now: %s runs with no plan rows, so no step of it is the owner's. This report changed nothing.", reported, key)
	}
	role, agent := placeWorker(task, place, steps)
	switch {
	case role == "" || agent == "":
		return fmt.Sprintf("%s is not a step of yours that runs now: %s stands at %s, which no agent works. This report changed nothing.", reported, key, PlaceWords(place))
	case place == named && agent == "the owner":
		return fmt.Sprintf("%s is not a step of yours that runs now: %s stands in %s, and its task %s is %s at %s, so that step does not run. This report changed nothing.",
			reported, key, issue.Phase, task, state, PlaceWords(place))
	}
	return fmt.Sprintf("%s is not a step of yours that runs now: %s stands at %s (%s), whose agent is %s. This report changed nothing.",
		reported, key, PlaceWords(place), role, agent)
}

// SignOffNotTheOwners is the sentence of SIGNOFF_NOT_THE_OWNERS, the refusal of a sign-off of
// issue in accepting where the acceptance its task stands at, place of task on steps, is not the
// owner's own step (Issue.OwnersPlace): a step `acceptance` with no key, or with another key, whose
// agent ends it by its own verdict. The workflow's signOff writes it.
func SignOffNotTheOwners(issue Issue, task string, place TaskPlace, steps []PlanStep) string {
	role, agent := placeWorker(task, place, steps)
	return fmt.Sprintf("%s (%s) of task %s of %s is not a step of yours: its agent is %s, which ends it with its own verdict, and a sign-off takes only an acceptance whose agent is the owner. This sign-off changed nothing.",
		PlaceWords(place), role, task, issue.Key, agent)
}

// OwnersPlaceWords is the owner's place of task, place on steps, as the architect's catch-up names
// it: `step <n> (<role>) of task <task>`, or `the retro of task <task>`.
func OwnersPlaceWords(task string, place TaskPlace, steps []PlanStep) string {
	if role, _ := placeWorker(task, place, steps); place.Kind == PlaceStep && role != "" {
		return fmt.Sprintf("%s (%s) of task %s", PlaceWords(place), role, task)
	}
	return fmt.Sprintf("%s of task %s", PlaceWords(place), task)
}

// placeWorker is the role and the agent of place on task's steps as a sentence names them: the
// step's own role at a step and the retro agent's role at the retro (RetroAgentOf), with the
// agent's name (PlanStep.AgentName), or `the owner` where the owner does it; "" for both at the
// merge, at no act and at a place the task does not hold, which no agent of the plan works.
func placeWorker(task string, place TaskPlace, steps []PlanStep) (role, agent string) {
	switch place.Kind {
	case PlaceStep:
		if place.Position < 1 || place.Position > len(steps) {
			return "", ""
		}
		step := steps[place.Position-1]
		if plan.AgentOf(task, place.Position, plan.Step{Role: step.Role, Key: step.AgentKey}).Owner() {
			return step.Role, "the owner"
		}
		return step.Role, string(step.AgentName())
	case PlaceRetro:
		retro, role := RetroAgentOf(task, steps)
		if retro.Owner() {
			return role, "the owner"
		}
		for i := len(steps) - 1; i >= 0; i-- {
			if plan.AgentOf(task, i+1, plan.Step{Role: steps[i].Role, Key: steps[i].AgentKey}) == retro {
				return role, string(steps[i].AgentName())
			}
		}
	}
	return "", ""
}

// PlaceWords is place as a sentence names it: step <n>, the retro, the merge, or no step for a
// task at no act.
func PlaceWords(place TaskPlace) string {
	switch place.Kind {
	case PlaceStep:
		return fmt.Sprintf("step %d", place.Position)
	case PlaceRetro:
		return "the retro"
	case PlaceMerge:
		return "the merge"
	}
	return "no step"
}
