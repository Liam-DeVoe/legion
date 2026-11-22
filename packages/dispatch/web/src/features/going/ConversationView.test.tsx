import { afterEach, expect, spyOn, test } from "bun:test";
import { cleanup, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";

import { renderWithQueries } from "../../__tests__/query-provider";
import { api } from "../../api/client";
import type { GoingRun } from "../../api/types";
import { dangerText } from "../../theme/classes";
import { ConversationView } from "./ConversationView";
import { openConversation, resetGoingStore } from "./going-store";
import { useOpenPlan } from "./usePlan";

// The steps of a run as the bar draws them (design v3, state 3): one line for each call, in the
// present tense with "…" while it runs and in the past tense once it ended, or one sentence that
// says why it failed; no line for a call with no words, or for a change of an approved plan,
// which its row's tick shows; the model's prose beside its calls while they run, never as the
// answer; and under the answer, the line that says results were withheld from the model.

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
