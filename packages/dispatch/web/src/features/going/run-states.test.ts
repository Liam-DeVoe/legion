import { expect, test } from "bun:test";

import { STARTED_RUN, withRunFrame } from "./run-frames";
import {
  endWords,
  NO_MODEL_WORDS,
  type RunEvent,
  type RunRequest,
  type RunState,
  reasonCode,
  runNext,
} from "./run-states";

// The state table of one run of the command bar (plan.md t15): each state - idle, working,
// waiting for a plan, done, stopped at a bound, stopped by the person, failed - under each event -
// a step, propose_plan, approve, Stop, Esc in the empty box, a bound, Continue, the bar closing -
// and the start and the ends that reach the states the table names. Each row is a test: the next
// state, and the request the event sends where it sends one.

type Row = readonly [RunState, RunEvent, RunState, RunRequest?];

const rows: readonly Row[] = [
  // Before Enter there is no run: nothing but the start does anything.
  ["idle", "start", "working", "start"],
  ["idle", "step", "idle"],
  ["idle", "propose_plan", "idle"],
  ["idle", "approve", "idle"],
  ["idle", "stop", "idle"],
  ["idle", "esc", "idle"],
  ["idle", "bound", "idle"],
  ["idle", "continue", "idle"],
  ["idle", "bar_closed", "idle"],
  // A run that works shows each step, waits on its plan, and stops at once on Stop or Esc.
  ["working", "step", "working"],
  ["working", "propose_plan", "waiting"],
  ["working", "approve", "working"],
  ["working", "stop", "stopped_person", "stop"],
  ["working", "esc", "stopped_person", "stop"],
  ["working", "bound", "stopped_bound"],
  ["working", "continue", "working"],
  ["working", "bar_closed", "working"],
  ["working", "end_done", "done"],
  ["working", "end_failed", "failed"],
  // A run whose plan waits: approval runs it, Stop stops it, and Esc only closes the bar.
  ["waiting", "step", "working"],
  ["waiting", "propose_plan", "waiting"],
  ["waiting", "approve", "working", "approve"],
  ["waiting", "stop", "stopped_person", "stop"],
  ["waiting", "esc", "waiting"],
  ["waiting", "bound", "stopped_bound"],
  ["waiting", "continue", "waiting"],
  ["waiting", "bar_closed", "waiting"],
  ["waiting", "end_stopped", "stopped_person"],
  // A run that is done stays done: a follow-up is another run.
  ["done", "step", "done"],
  ["done", "propose_plan", "done"],
  ["done", "approve", "done"],
  ["done", "stop", "done"],
  ["done", "esc", "done"],
  ["done", "bound", "done"],
  ["done", "continue", "done"],
  ["done", "bar_closed", "done"],
  // A bound kept what the run did and offers Continue, which starts a follow-up.
  ["stopped_bound", "step", "stopped_bound"],
  ["stopped_bound", "propose_plan", "stopped_bound"],
  ["stopped_bound", "approve", "stopped_bound"],
  ["stopped_bound", "stop", "stopped_bound"],
  ["stopped_bound", "esc", "stopped_bound"],
  ["stopped_bound", "bound", "stopped_bound"],
  ["stopped_bound", "continue", "working", "continue"],
  ["stopped_bound", "bar_closed", "stopped_bound"],
  // The person's Stop is final.
  ["stopped_person", "step", "stopped_person"],
  ["stopped_person", "propose_plan", "stopped_person"],
  ["stopped_person", "approve", "stopped_person"],
  ["stopped_person", "stop", "stopped_person"],
  ["stopped_person", "esc", "stopped_person"],
  ["stopped_person", "bound", "stopped_person"],
  ["stopped_person", "continue", "stopped_person"],
  ["stopped_person", "bar_closed", "stopped_person"],
  // So is a failure.
  ["failed", "step", "failed"],
  ["failed", "propose_plan", "failed"],
  ["failed", "approve", "failed"],
  ["failed", "stop", "failed"],
  ["failed", "esc", "failed"],
  ["failed", "bound", "failed"],
  ["failed", "continue", "failed"],
  ["failed", "bar_closed", "failed"],
];

for (const [state, event, next, send] of rows) {
  test(`a run ${state}, on ${event}, is ${next}${send === undefined ? " and sends nothing" : ` and sends ${send}`}`, () => {
    expect(runNext(state, event)).toEqual(
      send === undefined ? { state: next } : { send, state: next }
    );
  });
}

// The frames of a run's stream are the table's events: a reply that calls tools shows each call as
// a step that runs until its own frame says how it ended, a plan frame that waits is propose_plan,
// and an end frame that offers Continue is a bound.

test("a reply's tool calls show as running steps until each call's frame ends it", () => {
  const replied = withRunFrame(STARTED_RUN, "reply", {
    labels: ["Reading CORE-1"],
    step: 1,
    text: "Reading the issue.",
    tools: ["call_tool"],
  });
  expect(replied.steps).toEqual([
    {
      count: 0,
      header: "Reading the issue.",
      id: 0,
      items: [],
      kind: "",
      label: "Reading CORE-1",
      refused: false,
      running: true,
      slip: false,
      step: undefined,
      tool: "call_tool",
    },
  ]);
  const ended = withRunFrame(replied, "tool", {
    error: false,
    items: ["CORE-1"],
    kind: "read",
    label: "Read CORE-1",
    note: "call_tool dispatch_read: CORE-1",
    step: 2,
    tool: "call_tool",
  });
  expect(ended.steps).toEqual([
    {
      count: 0,
      header: "Reading the issue.",
      id: 0,
      items: ["CORE-1"],
      kind: "read",
      label: "Read CORE-1",
      refused: false,
      running: false,
      slip: false,
      step: 2,
      tool: "call_tool",
    },
  ]);
  // The prose beside the calls is the run's working words, no part of its answer.
  expect(ended.replies).toEqual([]);
  expect(ended.working).toBe("Reading the issue.");
  expect(ended.state).toBe("working");
});

test("a plan frame that waits is propose_plan, and an end that offers Continue is a bound", () => {
  const planned = withRunFrame(STARTED_RUN, "plan", { plan: 1, state: "waiting" });
  expect(planned.state).toBe("waiting");
  expect(planned.plan).toEqual({ number: 1, state: "waiting" });
  const bounded = withRunFrame(STARTED_RUN, "end", {
    offers: ["continue"],
    reason: "CALL_BUDGET: the run made its 6 model calls",
    state: "stopped",
  });
  expect(bounded.state).toBe("stopped_bound");
  const stopped = withRunFrame(STARTED_RUN, "end", {
    offers: [],
    reason: "STOPPED: its person stopped it",
    state: "stopped",
  });
  expect(stopped.state).toBe("stopped_person");
});

// How each end reason the run frames carry reads for the person (design v2, state 9): one
// sentence and one action, never the reason's code or its own words, which are a developer's.
// The reasons are the agent's (`going/agent/run.go`, `levels.go`, `history.go`) and the server's
// (`api/going_grant.go`, `going_run_stream.go`, `going_agent_supervision.go`).
test("a bound reads as a pause at the run's level with what it did so far, and offers Continue", () => {
  expect(
    endWords(
      "stopped_bound",
      "TIME_BUDGET: this run worked the 5m0s a standard run may; Continue starts a follow-up with a new budget",
      "standard",
      "read 14 items"
    )
  ).toEqual({
    action: "continue",
    noModel: false,
    text: "Stopped at Standard's time limit · read 14 items so far",
  });
  // A time stop whose model calls the agent's pacer held for more than half the time says how
  // long the model was busy (`busyWords`, `going/agent/levels.go`).
  expect(
    endWords(
      "stopped_bound",
      "TIME_BUDGET: this run worked the 1m30s a fast run may, and the model was busy for 52s of it; Continue starts a follow-up with a new budget",
      "fast",
      "read 3 items"
    )?.text
  ).toBe("Stopped at Fast's time limit · the model was busy for 52s of it · read 3 items so far");
  expect(
    endWords(
      "stopped_bound",
      "TIME_BUDGET: this run worked the 1m30s a fast run may, and the model was busy for 1m5s of it; Continue starts a follow-up with a new budget",
      "fast",
      ""
    )?.text
  ).toBe("Stopped at Fast's time limit · the model was busy for 1m5s of it");
  expect(endWords("stopped_bound", "CALL_BUDGET: …", "fast", "")?.text).toBe(
    "Stopped at Fast's step limit"
  );
  expect(endWords("stopped_bound", "OUTPUT_TOKEN_BUDGET: …", "deep", "searched twice")?.text).toBe(
    "Stopped at Deep's reply length limit · searched twice so far"
  );
  expect(endWords("stopped_bound", "", "standard", "")?.text).toBe(
    "Stopped at a limit of Standard"
  );
});

test("a Stop keeps what it did, and never repeats the reason's code", () => {
  const stopped = endWords("stopped_person", "STOPPED: its person stopped it", "standard", "");
  expect(stopped).toEqual({
    action: undefined,
    noModel: false,
    text: "Stopped · kept what it did",
  });
  expect(stopped?.text).not.toContain("STOPPED");
  expect(
    endWords("stopped_person", "SESSION_ENDED: the person this run acts for signed out", "fast", "")
      ?.text
  ).toBe("Stopped because you signed out · kept what it did");
  expect(endWords("stopped_person", "SIGN_IN_EXPIRED: …", "fast", "")?.text).toBe(
    "Stopped because your sign-in expired · kept what it did"
  );
});

test("a failure is one sentence and one action, with no code", () => {
  const cases: readonly (readonly [string, string, string])[] = [
    ["MODEL_FAILED: azure: 502 Bad Gateway", "Dispatch could not reach the model.", "retry"],
    [
      "MODEL_CALL_TIME: a model call of this run took past 2m0s",
      "The model took too long to answer.",
      "retry",
    ],
    ["MODEL_REFUSED: the chat model refused to answer", "The model refused to answer.", "retry"],
    ["MODEL_CONTENT_FILTER: …", "The model's content filter refused this request.", "retry"],
    ["MODEL_REPLY_TOO_LARGE: …", "The model's reply was too long to read.", "retry"],
    [
      "HISTORY_TOO_LARGE: the last 8 model turns …",
      "This conversation grew too long for the model to read.",
      "new",
    ],
    [
      "AGENT_KILLED: the agent process died 3 times …",
      "Dispatch's agent stopped while it worked on this run.",
      "retry",
    ],
    ["AGENT_FAILED: save step 3: deadline", "Dispatch's agent failed on this run.", "retry"],
    [
      'UNKNOWN_TOOL: step 4 of this run names the tool "x"',
      "Dispatch's agent failed on this run.",
      "retry",
    ],
  ];
  for (const [reason, text, action] of cases) {
    const words = endWords("failed", reason, "standard", "read 2 items");
    expect(words).toEqual({ action: action as "retry" | "new", noModel: false, text });
    expect(words?.text).not.toMatch(/[A-Z]{2,}_/u);
  }
});

test("a run that failed because the Dispatch has no answering model says the one sentence and offers nothing", () => {
  expect(
    endWords(
      "failed",
      "NO_MODEL: this server names no chat model (DISPATCH_ANSWERER), so its agent cannot work the run",
      "standard",
      ""
    )
  ).toEqual({ action: undefined, noModel: true, text: NO_MODEL_WORDS });
  expect(NO_MODEL_WORDS).toBe("Answers are off on this Dispatch: it has no answering model.");
});

test("a run that has not stopped or failed has no end words, and a reason's code is its first word", () => {
  for (const state of ["idle", "working", "waiting", "done"] as const) {
    expect(endWords(state, "PLAN_REJECTED: the person rejected the plan", "standard", "")).toBe(
      undefined
    );
  }
  expect(reasonCode("TIME_BUDGET: this run worked")).toBe("TIME_BUDGET");
  expect(reasonCode("NO_MODEL")).toBe("NO_MODEL");
  expect(reasonCode("the run failed: why")).toBe("");
  expect(reasonCode("")).toBe("");
});
