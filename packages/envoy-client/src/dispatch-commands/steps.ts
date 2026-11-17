import type { StepRecord } from "@legion/contracts";
import type { CommandContext, DispatchToolResult } from "./context";

/** A field the record does not hold prints `-`, so every line keeps its columns. */
function orDash(value: string | null): string {
  return value === null || value === "" ? "-" : value;
}

/** One run of a step: where it stands in its task, who ran it on which model, and how it ended.
 *  A run that has not ended holds no end and no result, and prints `running` for both. */
function stepLine(record: StepRecord): string {
  const ended = record.ended_at !== null;
  return [
    record.task,
    record.step,
    record.kind,
    record.role,
    orDash(record.type),
    "attempt",
    record.attempt,
    orDash(record.agent),
    orDash(record.model),
    orDash(record.started_at),
    ended ? record.ended_at : "running",
    ended ? orDash(record.result) : "running",
  ].join(" ");
}

/** A task of the plan: the tasks it needs - `nothing` for none, and `are not recorded` where its
 *  plan node recorded none - then each step the plan gives it, with where its newest run stands -
 *  the last run of kind `step` at its place, `running` until it ends and then its result - or
 *  `not started` before any run of it. */
function taskLine(
  task: string,
  needs: readonly string[] | null,
  planned: readonly string[],
  steps: readonly StepRecord[]
): string {
  const newest = new Map(
    steps.filter((record) => record.kind === "step").map((record) => [record.step, record])
  );
  const states = planned.map((name, index) => {
    const run = newest.get(index + 1);
    if (run === undefined) return `${index + 1} ${name} not started`;
    return `${index + 1} ${name} ${run.ended_at === null ? "running" : orDash(run.result)}`;
  });
  const needed =
    needs === null ? "are not recorded" : needs.length === 0 ? "nothing" : needs.join(", ");
  return `task ${task} needs ${needed}: ${states.join(", ")}`;
}

/** `dispatch steps`: each task of an issue as Dispatch lists them, the tasks of its plan first,
 *  and each run of each step, task by task. A task of the plan prints the line of its needs and
 *  its planned steps (taskLine) before its runs; any other task prints its runs alone. */
export async function stepsCommand(context: CommandContext): Promise<DispatchToolResult> {
  const { client, issue } = context;
  const read = await client.listSteps(issue());
  // The generated shape lets either list be null; Dispatch writes `[]`, and null reads the same.
  // A Dispatch older than the plan's tasks sends no `needs` or `planned`: no task line prints.
  const tasks = (read.tasks ?? []).map((task) => ({ ...task, steps: task.steps ?? [] }));
  const records = tasks.flatMap((task) => task.steps);
  const lines = tasks.flatMap((task) => {
    const runs = task.steps.map(stepLine);
    const planned = task.planned ?? [];
    if (planned.length === 0) return runs;
    return [taskLine(task.task, task.needs ?? null, planned, task.steps), ...runs];
  });
  return {
    text: [`${read.issue}: ${records.length} steps in ${tasks.length} tasks`, ...lines].join("\n"),
    details: { issue: read.issue, tasks },
  };
}
