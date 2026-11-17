import type { IssueSteps, StepRecord } from "../../api/types";

/** Where a run stands: running until the daemon writes its end, then the result it ended with.
 *  `other` is a result this build has no word for, which only a newer Dispatch can send; the row
 *  keeps it in `result`, in the server's own word. */
export type StepState = "running" | "passed" | "failed" | "stopped" | "other";

/** One run of one step, as the Steps tab draws it. `label` names the step: its place in the task
 *  for a step of the plan, and `Retro` or `Merge` for the two runs that no plan names (step 0).
 *  An empty `agent`, `model` or `type` is a field the record does not hold, as is a null `started`
 *  or `ended`; `attempt` is the server's count of this step's runs in its task. */
export interface StepRow {
  id: string;
  task: string;
  step: number;
  label: string;
  role: string;
  type: string;
  agent: string;
  model: string;
  attempt: number;
  started: string | null;
  ended: string | null;
  state: StepState;
  result: string;
  /** The step a failed or stopped run sent its task back to, when it sent it back. */
  returnedTo: number | null;
}

/** Where a step of the plan stands, as the server answers it from the runs of the plan's own run of
 *  the issue: `not started`, `running`, or the result its newest run ended with. `other` is a word
 *  this build does not know, which only a newer Dispatch can send. */
export type PlannedStepState = "not started" | StepState;

/** One step the plan gives a task: `step` is its place in the task, from 1, and `name` the plan's
 *  word for it, a role or `role:key`. `state` is where the server says it stands, and `result` the
 *  server's own word for it, which the tab shows for a state of `other`. */
export interface PlannedStep {
  step: number;
  name: string;
  state: PlannedStepState;
  result: string;
}

/** One planned step as the server answers it. */
type AnsweredStep = NonNullable<NonNullable<IssueSteps["tasks"]>[number]["planned"]>[number];

/** One task: the tasks of the plan it needs, null where its plan node recorded none or the plan
 *  does not hold it, and [] where it needs none; the steps the plan gives it, empty for a task the
 *  plan does not hold; and its runs in the order the server read them. */
export interface StepTaskRows {
  task: string;
  needs: string[] | null;
  planned: PlannedStep[];
  rows: StepRow[];
}

const KIND_LABELS: Readonly<Record<string, string>> = { merge: "Merge", retro: "Retro" };

function stepState(result: string): StepState {
  switch (result) {
    case "":
      return "running";
    case "passed":
    case "failed":
    case "stopped":
      return result;
    default:
      return "other";
  }
}

function stepRow(record: StepRecord): StepRow {
  return {
    agent: record.agent,
    attempt: record.attempt,
    ended: record.ended_at,
    id: record.id,
    // A kind this build does not know keeps its own word rather than a step number it lacks.
    label: record.kind === "step" ? String(record.step) : (KIND_LABELS[record.kind] ?? record.kind),
    model: record.model,
    result: record.result,
    returnedTo: record.returned_to,
    role: record.role,
    started: record.started_at,
    state: stepState(record.result),
    step: record.step,
    task: record.task,
    type: record.type,
  };
}

function plannedState(state: string): PlannedStepState {
  switch (state) {
    case "not started":
    case "running":
    case "passed":
    case "failed":
    case "stopped":
      return state;
    default:
      return "other";
  }
}

/** A planned step as the server answers it, its state kept as answered: the client works out no
 *  state from the runs it lists, which hold every run of the issue. */
function plannedStep(answered: AnsweredStep): PlannedStep {
  return {
    name: answered.name,
    result: answered.state,
    state: plannedState(answered.state),
    step: answered.step,
  };
}

/** The issue's tasks in the server's order - the tasks of its newest plan first, in the plan's
 *  order, then each other task with a run, by its first run - each with its needs, its planned
 *  steps and its runs in the order of the read: by generation, then start, a run with no start
 *  first. The generated shape lets either list be null, which reads as empty; a server older than
 *  the plan's tasks sends no `needs` or `planned`, which read as needs not recorded and no step. */
export function stepTasks(steps: IssueSteps): StepTaskRows[] {
  return (steps.tasks ?? []).map((task) => ({
    needs: task.needs ?? null,
    planned: (task.planned ?? []).map(plannedStep),
    rows: (task.steps ?? []).map(stepRow),
    task: task.task,
  }));
}
