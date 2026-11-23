import type { StepRecord } from "@legion/contracts";
import type { CommandContext, DispatchToolResult } from "./context";

/** A field the record does not hold prints `-`, so every line keeps its columns. */
function orDash(value: string | null): string {
  return value === null || value === "" ? "-" : value;
}

/** What a run's line ends with after its result: ` at <start>..<end>`, the commits its task's head
 *  was at when the run started and when it ended, where the record holds either, each cut to its
 *  first ten characters, or `-` for one the record does not hold yet; then, for a fix, ` fix of
 *  <check>`, the CI check it fixes, which is the rest of the line. A record that holds neither
 *  field, as an older Dispatch answers one, ends at its result. */
function commitsAndFix(record: StepRecord): string[] {
  const words: string[] = [];
  if (record.start_commit || record.end_commit) {
    const start = record.start_commit?.slice(0, 10) || "-";
    words.push("at", `${start}..${record.end_commit?.slice(0, 10) || "-"}`);
  }
  if (record.fix_check) words.push("fix of", record.fix_check);
  return words;
}

/** One run of a step: where it stands in its task, who ran it on which model, how it ended, and
 *  then its commits and the check a fix fixes. A run that has not ended holds no end and no
 *  result, and prints `running` for both. */
function stepLine(record: StepRecord): string {
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
    ...(record.ended_at === null
      ? ["running", "running"]
      : [record.ended_at, orDash(record.result)]),
    ...commitsAndFix(record),
  ].join(" ");
}

/** `dispatch steps`: each run of each step of an issue, task by task, as Dispatch lists them. */
export async function stepsCommand(context: CommandContext): Promise<DispatchToolResult> {
  const { client, issue } = context;
  const read = await client.listSteps(issue());
  // The generated shape lets either list be null; Dispatch writes `[]`, and null reads the same.
  const tasks = (read.tasks ?? []).map((task) => ({ task: task.task, steps: task.steps ?? [] }));
  const records = tasks.flatMap((task) => task.steps);
  return {
    text: [
      `${read.issue}: ${records.length} steps in ${tasks.length} tasks`,
      ...records.map(stepLine),
    ].join("\n"),
    details: { issue: read.issue, tasks },
  };
}
