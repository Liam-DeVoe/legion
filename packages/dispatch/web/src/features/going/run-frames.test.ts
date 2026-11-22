import { expect, test } from "bun:test";

import { type RunFrames, STARTED_RUN, withRunFrame } from "./run-frames";

// A run's steps as its frames say them: each call's words while it runs, from the reply that made
// it, then from its own frame, with the kind of step it is and the items it named; the prose
// beside calls is the words of the run while they run, never part of its answer; and the answer's
// reply says how many tool results were withheld from the model.

/** The run once each frame of `frames` arrived, in order. */
function framed(frames: readonly (readonly [string, unknown])[]): RunFrames {
  return frames.reduce((run, [name, data]) => withRunFrame(run, name, data), STARTED_RUN);
}

test("each call runs with its reply's present words, and ends with its frame's past words, kind and items", () => {
  const replied = framed([
    [
      "reply",
      {
        labels: ["Reading DEMO-15", "Reading the messages of DEMO-7"],
        step: 1,
        text: "Checking the issue.",
        tools: ["read", "call_tool"],
      },
    ],
  ]);
  expect(replied.steps.map((step) => [step.label, step.running, step.kind])).toEqual([
    ["Reading DEMO-15", true, ""],
    ["Reading the messages of DEMO-7", true, ""],
  ]);
  const ended = withRunFrame(replied, "tool", {
    error: true,
    items: ["DEMO-7"],
    kind: "read",
    label: "Could not read the messages of DEMO-7: you have no access",
    note: "call_tool {}: refused",
    step: 3,
    tool: "call_tool",
  });
  expect(ended.steps[1]).toEqual({
    id: 1,
    items: ["DEMO-7"],
    kind: "read",
    label: "Could not read the messages of DEMO-7: you have no access",
    refused: true,
    running: false,
    step: 3,
    tool: "call_tool",
  });
  expect(ended.steps[0]?.running).toBe(true);
});

test("the prose beside calls is the run's working words, and the answer is the reply that called none", () => {
  const run = framed([
    [
      "reply",
      { labels: ["Reading DEMO-1"], text: "Checking the children of DEMO-1.", tools: ["read"] },
    ],
    ["tool", { kind: "read", label: "Read DEMO-1", step: 2, tool: "read" }],
    ["reply", { labels: [], text: "DEMO-1 has two children.", tools: [], withheld: 2 }],
  ]);
  expect(run.replies).toEqual(["DEMO-1 has two children."]);
  expect(run.working).toBe("");
  expect(run.withheld).toBe(2);
  const working = framed([
    [
      "reply",
      { labels: ["Reading DEMO-1"], text: "Checking the children of DEMO-1.", tools: ["read"] },
    ],
  ]);
  expect(working.replies).toEqual([]);
  expect(working.working).toBe("Checking the children of DEMO-1.");
  expect(working.withheld).toBe(0);
});

test("a run recorded before calls had words has steps with no words", () => {
  const run = framed([
    ["reply", { text: "", tools: ["call_tool"] }],
    ["tool", { note: "call_tool dispatch_read: CORE-1", step: 2, tool: "call_tool" }],
    ["end", { offers: [], reason: "", state: "done" }],
  ]);
  expect(run.steps.map((step) => [step.label, step.kind, step.items])).toEqual([["", "", []]]);
});

test("a row an approved plan's runner made, which no reply named, is a write step of its own", () => {
  const run = framed([
    [
      "tool",
      {
        items: ["DEMO-7"],
        kind: "write",
        label: "Commented on DEMO-7",
        step: 6,
        tool: "dispatch_comment",
      },
    ],
  ]);
  expect(run.steps).toEqual([
    {
      id: 0,
      items: ["DEMO-7"],
      kind: "write",
      label: "Commented on DEMO-7",
      refused: false,
      running: false,
      step: 6,
      tool: "dispatch_comment",
    },
  ]);
});
