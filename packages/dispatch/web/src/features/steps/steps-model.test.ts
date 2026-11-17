import { expect, test } from "bun:test";

import type { StepRecord } from "../../api/types";
import { stepRecord as record } from "./step-record-fixture";
import { stepTasks } from "./steps-model";

// What the tab draws, without a DOM: the server's tasks in the server's order, each task's runs
// in the order the read listed them, a retro and a merge named by their kind, and a run that has
// not ended read as running.
test("stepTasks keeps the server's order and names each run's step, state and times", () => {
  const tasks = stepTasks({
    issue: "CORE-1",
    tasks: [
      {
        steps: [
          record({ id: "implement-1" }),
          record({
            agent: "legion-leg-leg-1-reviewer",
            ended_at: "2026-11-16T10:30:00Z",
            id: "review-1",
            model: "openai/gpt-6.1-sol",
            result: "failed",
            returned_to: 2,
            role: "reviewer",
            started_at: "2026-11-16T10:00:00Z",
            step: 4,
            type: "review",
          }),
          record({
            attempt: 2,
            ended_at: null,
            id: "implement-2",
            model: "",
            result: "",
            started_at: "2026-11-16T10:31:00Z",
          }),
          record({ id: "retro-1", kind: "retro", step: 0, type: "" }),
          // A merge GitHub already made: no merger started, so no agent, no model and no start.
          record({
            agent: "",
            id: "merge-1",
            kind: "merge",
            model: "",
            role: "merger",
            started_at: null,
            step: 0,
            type: "",
          }),
        ],
        task: "default",
      },
      { steps: [record({ id: "other-task-1", task: "docs" })], task: "docs" },
    ],
  });

  expect(tasks.map((task) => [task.task, task.rows.map((row) => row.id)])).toEqual([
    ["default", ["implement-1", "review-1", "implement-2", "retro-1", "merge-1"]],
    ["docs", ["other-task-1"]],
  ]);
  const [first] = tasks;
  expect(first?.rows.map((row) => row.label)).toEqual(["2", "4", "2", "Retro", "Merge"]);
  expect(first?.rows.map((row) => row.state)).toEqual([
    "passed",
    "failed",
    "running",
    "passed",
    "passed",
  ]);
  expect(first?.rows[1]).toEqual({
    agent: "legion-leg-leg-1-reviewer",
    attempt: 1,
    ended: "2026-11-16T10:30:00Z",
    id: "review-1",
    label: "4",
    model: "openai/gpt-6.1-sol",
    result: "failed",
    returnedTo: "step 2",
    role: "reviewer",
    started: "2026-11-16T10:00:00Z",
    state: "failed",
    type: "review",
  });
  expect(first?.rows[2]).toMatchObject({ attempt: 2, ended: null, state: "running" });
  expect(first?.rows[4]).toMatchObject({ agent: "", model: "", started: null, type: "" });
});

// The generated shape lets both lists be null, and an issue Legion never ran has no task at all.
test("stepTasks reads a null list as empty", () => {
  expect(stepTasks({ issue: "CORE-1", tasks: null })).toEqual([]);
  expect(stepTasks({ issue: "CORE-1", tasks: [] })).toEqual([]);
  expect(stepTasks({ issue: "CORE-1", tasks: [{ steps: null, task: "default" }] })).toEqual([
    { rows: [], task: "default" },
  ]);
});

// A move back names where it sent the task. Place 0 is the retro's, which a merger's move back
// reaches and no plan names, so it reads as the retro and never as `step 0`; and a return to place
// 0 is a return, not the nothing a run that returned nowhere names.
test("stepTasks names where a run returned its task, the retro for place 0", () => {
  const [task] = stepTasks({
    issue: "CORE-1",
    tasks: [
      {
        steps: [
          record({
            id: "review-1",
            result: "stopped",
            returned_to: 2,
            role: "reviewer",
            step: 4,
            type: "review",
          }),
          record({
            id: "merge-1",
            kind: "merge",
            result: "failed",
            returned_to: 0,
            role: "merger",
            step: 0,
            type: "",
          }),
          record({ id: "implement-1" }),
        ],
        task: "default",
      },
    ],
  });

  expect(task?.rows.map((row) => [row.id, row.returnedTo])).toEqual([
    ["review-1", "step 2"],
    ["merge-1", "the retro"],
    ["implement-1", null],
  ]);
});

// A result or a kind this build has no word for comes from a newer Dispatch: the run is kept, in
// its place, with the server's own word, rather than dropped or read as one of the known states.
test("stepTasks keeps a run whose result or kind it does not know, in the server's words", () => {
  const [task] = stepTasks({
    issue: "CORE-1",
    tasks: [
      {
        steps: [
          record({ id: "skipped", result: "skipped" }),
          record({ id: "pause", kind: "pause" as StepRecord["kind"], step: 0 }),
        ],
        task: "default",
      },
    ],
  });

  expect(task?.rows.map((row) => [row.id, row.label, row.state, row.result])).toEqual([
    ["skipped", "2", "other", "skipped"],
    ["pause", "pause", "passed", "passed"],
  ]);
});
