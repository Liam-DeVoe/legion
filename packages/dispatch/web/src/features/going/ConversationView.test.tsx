import { afterEach, expect, spyOn, test } from "bun:test";
import { act, cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";

import { onFakeClock, tick } from "../../__tests__/fake-clock";
import { renderWithQueries } from "../../__tests__/query-provider";
import { api } from "../../api/client";
import type { GoingRun } from "../../api/types";
import { dangerText } from "../../theme/classes";
import { ConversationView, elapsedWords } from "./ConversationView";
import { goingState, openConversation, resetGoingStore, startRun } from "./going-store";
import { useOpenPlan } from "./usePlan";

// The steps of a run as the bar draws them (design v3, state 3): one line for each call, in the
// present tense with "…" while it runs and in the past tense once it ended, or one sentence that
// says why it failed, whatever order its call, tool and reply frames came in, and no line left
// running once the run ended; no line for a call with no words, or for a change of an approved
// plan, which its row's tick shows; the model's prose beside its calls while they run, never as
// the answer; and under the answer, the line that says results were withheld from the model.
// Around them (design v2, states 2 and 3): the question as a bubble, "Thinking…" with its seconds
// until the first step or answer, from the moment of Enter, and the words sent while the run works,
// waiting at the foot with × to drop them.

function record(state: GoingRun["state"]): GoingRun {
  const at = new Date().toISOString();
  return {
    context: null,
    continues: null,
    conversation: "c-1",
    created_at: at,
    finished_at: state === "working" ? null : at,
    id: "r-1",
    issues: [],
    level: "standard",
    mode: "ask",
    state,
    stop_reason: null,
    used: { calls: 1, tokens: 10, working_ms: 1000 },
    words: "where is DEMO-15",
  };
}

/** The bar's conversation of one run, whose stream carries `frames` and stays open. */
async function showRun(
  state: GoingRun["state"],
  frames: readonly (readonly [string, unknown])[]
): Promise<() => void> {
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((_id, onFrame) => {
    for (const [name, data] of frames) onFrame(name, data);
    return new Promise<void>(() => {});
  });
  openConversation({ id: "c-1", newest_at: new Date().toISOString(), runs: [record(state)] });
  renderWithQueries(<Conversation />);
  await screen.findByTestId("going-run");
  return () => stream.mockRestore();
}

function Conversation(): ReactNode {
  return <ConversationView answering={useOpenPlan()} onContinue={() => {}} />;
}

afterEach(() => {
  cleanup();
  resetGoingStore();
});

test("a running call shows its present words with …, an ended one its past words, and neither a title", async () => {
  const restore = await showRun("working", [
    [
      "reply",
      {
        labels: ["Reading DEMO-15", "Reading the messages of DEMO-7", ""],
        text: "Checking the issue and its messages.",
        tools: ["read", "call_tool", "made_up_tool"],
      },
    ],
    ["tool", { items: ["DEMO-15"], kind: "read", label: "Read DEMO-15", step: 2, tool: "read" }],
    // A row an approved plan's runner made, which the plan's card shows as its tick.
    [
      "tool",
      {
        items: ["DEMO-7"],
        kind: "write",
        label: "Commented on DEMO-7",
        step: 5,
        tool: "dispatch_comment",
      },
    ],
  ]);
  try {
    await waitFor(() =>
      expect(screen.getAllByTestId("going-step").map((step) => step.textContent)).toEqual([
        "Read DEMO-15",
        "Reading the messages of DEMO-7…",
      ])
    );
    const steps = screen.getAllByTestId("going-step");
    expect(steps.map((step) => step.getAttribute("data-running"))).toEqual(["false", "true"]);
    expect(steps.every((step) => !step.hasAttribute("title"))).toBe(true);
    expect(screen.getByTestId("going-step-words").textContent).toBe(
      "Checking the issue and its messages."
    );
    expect(screen.queryByTestId("going-run-answer")).toBeNull();
  } finally {
    restore();
  }
});

test("a refused call reads its sentence in the danger colour", async () => {
  const restore = await showRun("working", [
    ["reply", { labels: ["Reading DEMO-12"], text: "", tools: ["read"] }],
    [
      "tool",
      {
        error: true,
        items: ["DEMO-12"],
        kind: "read",
        label: "Could not read DEMO-12: you have no access",
        step: 2,
        tool: "read",
      },
    ],
  ]);
  try {
    const step = await screen.findByText("Could not read DEMO-12: you have no access");
    expect(step.getAttribute("data-testid")).toBe("going-step");
    expect(step.className).toContain(dangerText);
  } finally {
    restore();
  }
});

test("a run recorded before calls had words shows no step line", async () => {
  const restore = await showRun("done", [
    ["reply", { text: "", tools: ["call_tool"] }],
    ["tool", { note: "call_tool dispatch_read: CORE-1", step: 2, tool: "call_tool" }],
    ["reply", { text: "CORE-1 is done.", tools: [] }],
    ["end", { offers: [], reason: "", state: "done" }],
  ]);
  try {
    await screen.findByTestId("going-run-answer");
    expect(screen.queryByTestId("going-steps")).toBeNull();
    expect(screen.queryByText(/call_tool/)).toBeNull();
  } finally {
    restore();
  }
});

test("the answer is the reply that called no tool, and withheld results read as one line under it", async () => {
  const restore = await showRun("done", [
    [
      "reply",
      { labels: ["Reading DEMO-1"], text: "Checking the children of DEMO-1.", tools: ["read"] },
    ],
    ["tool", { items: ["DEMO-1"], kind: "read", label: "Read DEMO-1", step: 2, tool: "read" }],
    ["reply", { labels: [], text: "DEMO-1 has two children.", tools: [], withheld: 2 }],
    ["end", { offers: [], reason: "", state: "done" }],
  ]);
  try {
    // The answer's markdown renders after its frame arrives.
    await waitFor(() =>
      expect(screen.getByTestId("going-run-answer").textContent).toBe("DEMO-1 has two children.")
    );
    expect(screen.queryByTestId("going-step-words")).toBeNull();
    expect(screen.getByTestId("going-run-withheld").textContent).toBe(
      "The model's content filter held back 2 results, so this answer may miss them."
    );
  } finally {
    restore();
  }
});

test("one withheld result reads in the singular", async () => {
  const restore = await showRun("done", [
    ["reply", { labels: [], text: "No session is named the architect.", tools: [], withheld: 1 }],
    ["end", { offers: [], reason: "", state: "done" }],
  ]);
  try {
    expect((await screen.findByTestId("going-run-withheld")).textContent).toBe(
      "The model's content filter held back one result, so this answer may miss it."
    );
  } finally {
    restore();
  }
});

/** The step lines the bar draws, each with whether it runs. */
function stepLines(): readonly (readonly [string | null, string | null])[] {
  return screen
    .getAllByTestId("going-step")
    .map((step) => [step.textContent, step.getAttribute("data-running")]);
}

test("a call frame draws its call running before the call ends, and its tool frame ends that line", async () => {
  const restore = await showRun("working", [
    [
      "call",
      { kind: "read", label: "Reading DEMO-15", step: 2, text: "Checking DEMO-15.", tool: "read" },
    ],
    ["call", { kind: "read", label: "Reading the messages of DEMO-7", step: 3, tool: "call_tool" }],
    ["tool", { items: ["DEMO-15"], kind: "read", label: "Read DEMO-15", step: 2, tool: "read" }],
  ]);
  try {
    await waitFor(() =>
      expect(stepLines()).toEqual([
        ["Read DEMO-15", "false"],
        ["Reading the messages of DEMO-7…", "true"],
      ])
    );
    expect(screen.getByTestId("going-step-words").textContent).toBe("Checking DEMO-15.");
  } finally {
    restore();
  }
});

test("a tool frame after its call frame, or before its reply frame, draws one line per call", async () => {
  const restore = await showRun("done", [
    // The first call, as a run sends it now: its call frame, its tool frame, then its reply.
    ["call", { kind: "read", label: "Reading DEMO-15", step: 2, text: "", tool: "read" }],
    ["tool", { items: ["DEMO-15"], kind: "read", label: "Read DEMO-15", step: 2, tool: "read" }],
    ["reply", { labels: ["Reading DEMO-15"], step: 1, text: "", tools: ["read"] }],
    // The second, as a run recorded before call frames: its tool frame, then its reply.
    [
      "tool",
      {
        items: ["DEMO-7"],
        kind: "read",
        label: "Read the comments of DEMO-7",
        step: 4,
        tool: "read",
      },
    ],
    ["reply", { labels: ["Reading the comments of DEMO-7"], step: 3, text: "", tools: ["read"] }],
    ["reply", { labels: [], step: 5, text: "DEMO-15 waits on DEMO-7.", tools: [] }],
    ["end", { offers: [], reason: "", state: "done" }],
  ]);
  try {
    await waitFor(() =>
      expect(screen.getByTestId("going-run-answer").textContent).toBe("DEMO-15 waits on DEMO-7.")
    );
    expect(stepLines()).toEqual([
      ["Read DEMO-15", "false"],
      ["Read the comments of DEMO-7", "false"],
    ]);
  } finally {
    restore();
  }
});

test("no running line stays once the run ended: a call its end cut draws no line", async () => {
  const restore = await showRun("stopped", [
    ["call", { kind: "read", label: "Reading DEMO-15", step: 2, text: "Checking.", tool: "read" }],
    ["tool", { items: ["DEMO-15"], kind: "read", label: "Read DEMO-15", step: 2, tool: "read" }],
    ["call", { kind: "read", label: "Reading the messages of DEMO-7", step: 3, tool: "call_tool" }],
    ["end", { offers: [], reason: "STOPPED: its person stopped it", state: "stopped" }],
  ]);
  try {
    await waitFor(() =>
      expect(screen.getByTestId("going-run-state").textContent).toContain("Stopped")
    );
    expect(stepLines()).toEqual([["Read DEMO-15", "false"]]);
    expect(screen.queryByTestId("going-step-words")).toBeNull();
  } finally {
    restore();
  }
});

test("the Thinking line counts whole seconds from the moment the person asked", () => {
  expect(elapsedWords(10_000, 10_000)).toBe("0 s");
  expect(elapsedWords(10_000, 10_999)).toBe("0 s");
  expect(elapsedWords(10_000, 12_000)).toBe("2 s");
  expect(elapsedWords(10_000, 9_000)).toBe("0 s");
});

test("a run that has sent nothing yet shows its question and Thinking with its seconds, until its first step", async () => {
  await onFakeClock(async () => {
    let send: ((name: string, data: unknown) => void) | undefined;
    const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((_id, onFrame) => {
      send = onFrame;
      return new Promise<void>(() => {});
    });
    try {
      openConversation({
        id: "c-1",
        newest_at: new Date().toISOString(),
        runs: [record("working")],
      });
      renderWithQueries(<Conversation />);
      expect(screen.getByTestId("going-run-words")).toHaveTextContent("where is DEMO-15");
      expect(screen.getByTestId("going-thinking")).toHaveTextContent("Thinking… 0 s");
      // "Working…" would say the same thing twice.
      expect(screen.queryByTestId("going-run-state")).toBeNull();
      await tick(1000);
      expect(screen.getByTestId("going-thinking")).toHaveTextContent("Thinking… 1 s");
      await tick(1000);
      expect(screen.getByTestId("going-thinking")).toHaveTextContent("Thinking… 2 s");

      act(() => send?.("call", { kind: "read", label: "Reading DEMO-15", step: 2, tool: "read" }));
      expect(screen.queryByTestId("going-thinking")).toBeNull();
      expect(stepLines()).toEqual([["Reading DEMO-15…", "true"]]);
      expect(screen.getByTestId("going-run-state")).toHaveTextContent("Working…");
    } finally {
      stream.mockRestore();
    }
  });
});

test("the question shows with Thinking from the moment of Enter, before Dispatch answers the start", async () => {
  const { promise: created, resolve: create } = Promise.withResolvers<GoingRun>();
  const start = spyOn(api, "createGoingRun").mockReturnValue(created);
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation(
    () => new Promise<void>(() => {})
  );
  try {
    renderWithQueries(<Conversation />);
    let started: Promise<boolean> | undefined;
    act(() => {
      started = startRun("where is DEMO-15", { path: "/" }, "alice");
    });
    const asked = screen.getByTestId("going-run-starting");
    expect(within(asked).getByTestId("going-run-words")).toHaveTextContent("where is DEMO-15");
    expect(within(asked).getByTestId("going-thinking")).toHaveTextContent("Thinking… 0 s");

    await act(async () => {
      create(record("working"));
      await started;
    });
    expect(screen.queryByTestId("going-run-starting")).toBeNull();
    const run = screen.getByTestId("going-run");
    expect(within(run).getByTestId("going-run-words")).toHaveTextContent("where is DEMO-15");
    expect(within(run).getByTestId("going-thinking")).toBeInTheDocument();
  } finally {
    start.mockRestore();
    stream.mockRestore();
  }
});

test("words sent while the run works wait at the foot, sending when it ends, and × drops them", async () => {
  const user = userEvent.setup();
  const start = spyOn(api, "createGoingRun");
  const restore = await showRun("working", []);
  try {
    await act(() => startRun("and its comments", { path: "/" }, "alice"));
    expect(start).not.toHaveBeenCalled();
    const queued = screen.getByTestId("going-queued");
    expect(within(queued).getByTestId("going-queued-words")).toHaveTextContent("and its comments");
    expect(queued).toHaveTextContent("Sends when this run ends");

    await user.click(within(queued).getByRole("button", { name: "Drop these words" }));
    expect(screen.queryByTestId("going-queued")).toBeNull();
    expect(goingState().queued).toBeUndefined();
  } finally {
    start.mockRestore();
    restore();
  }
});
