import type { IssueSteps, StepRecord } from "../../api/types";

/** Where a run stands: running until the daemon writes its end, then the result it ended with.
 *  `other` is a result this build has no word for, which only a newer Dispatch can send; the row
 *  keeps it in `result`, in the server's own word. */
export type StepState = "running" | "passed" | "failed" | "stopped" | "other";

/** One run of one step, as the Steps tab draws it: the fields of its row, and nothing the row
 *  does not show (the task is the table's, `StepTaskRows`). `label` names the step: its place in
 *  the task for a step of the plan, and `Retro` or `Merge` for the two runs that no plan names
 *  (step 0). An empty `agent`, `model` or `type` is a field the record does not hold, as is a
 *  null `started` or `ended`; `attempt` is the server's count of this step's runs in its task. */
export interface StepRow {
  id: string;
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
  /** Where a failed or stopped run sent its task back to, when it sent it back, as its Result
   *  cell names it: `step <n>`, or `the retro` for place 0 (`returnPlace`). */
  returnedTo: string | null;
}

/** One task's runs, in the order the server read them. */
export interface StepTaskRows {
  task: string;
  rows: StepRow[];
}

/** The label of each kind no plan names, keyed by the contract's kinds, so a kind the contract
 *  adds fails to compile here until it has one. */
const KIND_LABELS: Readonly<Record<Exclude<StepRecord["kind"], "step">, string>> = {
  merge: "Merge",
  retro: "Retro",
};

/** The place a move back sent the task to, in words. Place 0 is the retro's: of the two runs no
 *  plan names, a move back can reach only the retro, since the merge is the task's last run
 *  (Dispatch reads `returned_to` 0 as a return to the retro). It is no step, so it never reads
 *  `step 0`. */
function returnPlace(place: number | null): string | null {
  if (place === null) return null;
  return place === 0 ? "the retro" : `step ${place}`;
}

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
    // A kind only a newer Dispatch sends, which this build's contract does not name, keeps its
    // own word rather than a step number it lacks.
    label: record.kind === "step" ? String(record.step) : (KIND_LABELS[record.kind] ?? record.kind),
    model: record.model,
    result: record.result,
    returnedTo: returnPlace(record.returned_to),
    role: record.role,
    started: record.started_at,
    state: stepState(record.result),
    type: record.type,
  };
}

/** The issue's tasks in the server's order (the order of each task's first step), each with its
 *  runs in the order of the read: by generation, then start, a run with no start first. The
 *  generated shape lets either list be null, which reads as empty. */
export function stepTasks(steps: IssueSteps): StepTaskRows[] {
  return (steps.tasks ?? []).map((task) => ({
    rows: (task.steps ?? []).map(stepRow),
    task: task.task,
  }));
}
