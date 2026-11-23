import { afterEach, beforeEach, expect, spyOn, test } from "bun:test";
// biome-ignore lint/style/noRestrictedImports: the conversation's own scroll event below, which user-event has no form for
import { act, cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";

import { onFakeClock, tick } from "../../__tests__/fake-clock";
import { renderWithQueries } from "../../__tests__/query-provider";
import { api } from "../../api/client";
import type { GoingPlanAnswer, GoingPlanRow, GoingRun } from "../../api/types";
import { dangerText } from "../../theme/classes";
import { clockWords } from "../search/blocks/block-cells";
import { ConversationView } from "./ConversationView";
import { goingState, openConversation, resetGoingStore, startRun } from "./going-store";
import { useOpenPlan } from "./usePlan";

// The steps of a run as the bar draws them (design v2, state 3): one line for each call, in the
// present tense with "…" while it runs and in the past tense once it ended, or one sentence that
// says why it failed, whatever order its call, tool and reply frames came in, and no line left
// running once the run ended; no line for a call with no words, or for a change of an approved
// plan, which its row's tick shows; the model's prose beside its calls as their header, never as
// the answer; the run's timer on the newest line; and "Reconnecting…" while its stream is dropped.
// The answer (state 4): the steps folded above it into "Worked for…", each issue it names as a
// chip, the line that says results were withheld from the model, and under it "Done", "Waits for
// your answer", or, for a run of the line Do that ended done with no plan, "Nothing changed: no
// plan was proposed.", with Copy and Retry. Around them (states 2 and 3): the question as a
// bubble, "Thinking…" with its seconds until the first step or answer, from the moment of Enter,
// and the words sent while the run works, waiting at the foot with × to drop them.

function record(
  state: GoingRun["state"],
  workingMs = 1000,
  extra: Partial<GoingRun> = {}
): GoingRun {
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
    line: null,
    mode: "ask",
    state,
    stop_reason: null,
    used: { calls: 1, tokens: 10, working_ms: workingMs },
    words: "where is DEMO-15",
    ...extra,
  };
}

/** The bar's conversation of one run, whose stream carries `frames` and stays open. With
 *  `dialogKeys`, it is drawn in a dialog as the bar draws it, and each key that reaches the
 *  dialog is put in `dialogKeys`. */
async function showRun(
  state: GoingRun["state"],
  frames: readonly (readonly [string, unknown])[],
  {
    dialogKeys,
    onContinue = () => {},
    onRetry = () => {},
    run = {},
    workingMs,
  }: {
    dialogKeys?: string[];
    onContinue?: (run: string, level?: GoingRun["level"]) => void;
    onRetry?: (words: string) => void;
    run?: Partial<GoingRun>;
    workingMs?: number;
  } = {}
): Promise<() => void> {
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((_id, onFrame) => {
    for (const [name, data] of frames) onFrame(name, data);
    return new Promise<void>(() => {});
  });
  openConversation({
    id: "c-1",
    newest_at: new Date().toISOString(),
    runs: [record(state, workingMs, run)],
  });
  const conversation = <Conversation onContinue={onContinue} onRetry={onRetry} />;
  renderWithQueries(
    dialogKeys === undefined ? (
      conversation
    ) : (
      <div
        aria-label="Search"
        className="relative"
        onKeyDown={(event) => dialogKeys.push(event.key)}
        role="dialog"
      >
        {conversation}
      </div>
    )
  );
  await screen.findByTestId("going-run");
  return () => stream.mockRestore();
}

function Conversation({
  onContinue = () => {},
  onRetry = () => {},
}: {
  readonly onContinue?: (run: string, level?: GoingRun["level"]) => void;
  readonly onRetry?: (words: string) => void;
}): ReactNode {
  return <ConversationView answering={useOpenPlan()} onContinue={onContinue} onRetry={onRetry} />;
}

test("a run that failed because the Dispatch has no answering model says so in one sentence, with no Failed and no code", async () => {
  const restore = await showRun("failed", [
    [
      "end",
      {
        offers: [],
        reason:
          "NO_MODEL: this server names no chat model (DISPATCH_ANSWERER), so its agent cannot work the run",
        state: "failed",
      },
    ],
  ]);
  try {
    expect((await screen.findByTestId("going-cannot-answer")).textContent).toBe(
      "Answers are off on this Dispatch: it has no answering model."
    );
    const run = screen.getByTestId("going-run");
    expect(run.textContent ?? "").not.toMatch(/Failed|NO_MODEL|DISPATCH_ANSWERER/u);
    expect(screen.queryByTestId("going-run-state")).toBeNull();
    expect(screen.queryByTestId("going-run-action")).toBeNull();
  } finally {
    restore();
  }
});

test("a failure reads one sentence and Try again, which runs the same words again", async () => {
  const user = userEvent.setup();
  const retried: string[] = [];
  const restore = await showRun(
    "failed",
    [["end", { offers: [], reason: "MODEL_FAILED: azure: 502 Bad Gateway", state: "failed" }]],
    { onRetry: (words) => retried.push(words) }
  );
  try {
    const line = await screen.findByTestId("going-run-state");
    expect(line.textContent).toBe("Dispatch could not reach the model. Try again");
    expect(line.textContent).not.toMatch(/MODEL_FAILED|502/u);
    await user.click(within(line).getByRole("button", { name: "Try again" }));
    expect(retried).toEqual(["where is DEMO-15"]);
  } finally {
    restore();
  }
});

test("a bound is a grey card with what the run did so far, Continue and Continue in Deep", async () => {
  const user = userEvent.setup();
  const continued: (readonly [string, GoingRun["level"] | undefined])[] = [];
  const restore = await showRun(
    "stopped",
    [
      ["tool", { items: ["DEMO-15"], kind: "read", label: "Read DEMO-15", step: 2, tool: "read" }],
      [
        "end",
        {
          offers: ["continue"],
          reason: "TIME_BUDGET: this run worked the 5m0s a standard run may",
          state: "stopped",
        },
      ],
    ],
    { onContinue: (run, level) => continued.push([run, level]) }
  );
  try {
    const bound = await screen.findByTestId("going-run-bound");
    expect(within(bound).getByTestId("going-run-state").textContent).toBe(
      "Stopped at Standard's time limit · read 1 item so far"
    );
    await user.click(within(bound).getByTestId("going-run-continue"));
    await user.click(within(bound).getByTestId("going-run-continue-deep"));
    expect(continued).toEqual([
      ["r-1", undefined],
      ["r-1", "deep"],
    ]);
  } finally {
    restore();
  }
});

// Design v2, state 5: a block is a card with a caption that says what it counts and when, and a
// table past 8 rows opens whole, over the whole bar that draws the conversation, with a way back.
test("a table's caption says what it counts and when, and Show all opens it over the whole bar until Esc or Back", async () => {
  const user = userEvent.setup();
  const finished = "2026-11-22T16:40:00Z";
  const dialogKeys: string[] = [];
  const restore = await showRun(
    "done",
    [
      [
        "block",
        {
          kind: "table",
          rows: Array.from({ length: 12 }, (_, index) => ({
            issues: index + 1,
            status: index % 2 === 0 ? "in_progress" : "todo",
          })),
          spec: { columns: ["status", "issues"], params: {}, source: "run", table: "chain" },
          title: "DEMO issues by status",
        },
      ],
      ["reply", { labels: [], text: "Here they are.", tools: [] }],
      ["end", { offers: [], reason: "", state: "done" }],
    ],
    { dialogKeys, run: { finished_at: finished } }
  );
  try {
    // The block was read before the run finished, and was read again here after it: the caption
    // says the time the run finished.
    expect((await screen.findByTestId("going-block-caption")).textContent).toBe(
      `DEMO issues by status · as of ${clockWords(Date.parse(finished))}`
    );
    const table = screen.getByTestId("going-block-table");
    expect(within(table).getAllByRole("row")).toHaveLength(9);
    expect(within(table).getAllByRole("cell")[0]).toHaveTextContent("In progress");
    await user.click(screen.getByTestId("going-block-show-all"));
    const whole = screen.getByTestId("going-block-whole");
    // It covers the whole bar: it is drawn in the bar's dialog itself, over the conversation and
    // the box, and not in the conversation's own scrolled area.
    expect(whole.parentElement).toBe(screen.getByRole("dialog", { name: "Search" }));
    expect(whole.className).toContain("absolute inset-0");
    expect(within(whole).getAllByRole("row")).toHaveLength(13);
    expect(within(whole).getByTestId("going-block-whole-close")).toHaveFocus();
    // Esc closes the table first, and the bar never hears it.
    await user.keyboard("{Escape}");
    expect(screen.queryByTestId("going-block-whole")).toBeNull();
    expect(dialogKeys).not.toContain("Escape");

    await user.click(screen.getByTestId("going-block-show-all"));
    await user.click(screen.getByTestId("going-block-whole-close"));
    expect(screen.queryByTestId("going-block-whole")).toBeNull();
  } finally {
    restore();
  }
});

// Design v2, state 8: the view follows new output until the person scrolls up; then "↓ New reply"
// shows at the next output and takes them back to the foot.
test("the conversation follows new output until the person scrolls up, and New reply brings it back", async () => {
  const user = userEvent.setup();
  let send: ((name: string, data: unknown) => void) | undefined;
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((_id, onFrame) => {
    send = onFrame;
    return new Promise<void>(() => {});
  });
  try {
    openConversation({ id: "c-1", newest_at: new Date().toISOString(), runs: [record("working")] });
    renderWithQueries(<Conversation />);
    const view = screen.getByTestId("going-conversation-view");
    Object.defineProperty(view, "scrollHeight", { configurable: true, value: 1000 });
    Object.defineProperty(view, "clientHeight", { configurable: true, value: 200 });
    act(() => send?.("call", { kind: "read", label: "Reading DEMO-15", step: 2, tool: "read" }));
    // At the foot it follows: the new line scrolled the view down, and no pill shows.
    expect(view.scrollTop).toBe(1000);
    expect(screen.queryByTestId("going-new-reply")).toBeNull();

    view.scrollTop = 100;
    fireEvent.scroll(view);
    act(() =>
      send?.("tool", {
        items: ["DEMO-15"],
        kind: "read",
        label: "Read DEMO-15",
        step: 2,
        tool: "read",
      })
    );
    expect(view.scrollTop).toBe(100);
    const pill = screen.getByTestId("going-new-reply");
    expect(pill).toHaveTextContent("↓ New reply");
    await user.click(pill);
    expect(view.scrollTop).toBe(1000);
    expect(screen.queryByTestId("going-new-reply")).toBeNull();
  } finally {
    stream.mockRestore();
  }
});

// Design v2, state 8: an old turn keeps its folded line, and its applied plan folds to one line,
// "Applied 2 changes · Undo", which opens the card.
test("an old turn's applied plan folds to one line with Undo, which opens the card", async () => {
  const user = userEvent.setup();
  const applied: GoingPlanAnswer = {
    mode: "ask",
    plan: 1,
    rows: [0, 1].map(
      (number): GoingPlanRow => ({
        arguments: { issue: `DEMO-${number + 1}`, priority: 1 },
        changed: false,
        claim: null,
        class: "field",
        fields: ["priority"],
        issue: `DEMO-${number + 1}`,
        item: `DEMO-${number + 1}`,
        needs: [],
        now: { priority: 3 },
        result: { code: "", delivery: null, message: "", status: 200 },
        row: number,
        state: "done",
        tool: "dispatch_issue_update",
        waits: [],
      })
    ),
    run: "r-1",
    state: "done",
    summary: "",
    version: "v1",
  };
  const read = spyOn(api, "goingPlan").mockImplementation(async () => applied);
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((id, onFrame) => {
    if (id === "r-1") {
      onFrame("plan", { plan: 1, state: "waiting" });
      onFrame("plan", { plan: 1, state: "done" });
      onFrame("end", { offers: [], reason: "", state: "done" });
    } else {
      onFrame("reply", { labels: [], text: "Both are P1 now.", tools: [] });
      onFrame("end", { offers: [], reason: "", state: "done" });
    }
    return new Promise<void>(() => {});
  });
  try {
    openConversation({
      id: "c-1",
      newest_at: new Date().toISOString(),
      runs: [
        record("done", 1000, { continues: "r-1", id: "r-2", words: "and now?" }),
        record("done", 1000, { words: "raise DEMO-1 and DEMO-2" }),
      ],
    });
    renderWithQueries(<Conversation />);
    const [old] = await screen.findAllByTestId("going-run");
    const folded = await within(old as HTMLElement).findByTestId("going-plan");
    expect(folded).toHaveAttribute("data-folded", "true");
    expect(within(folded).getByTestId("going-plan-title")).toHaveTextContent("Applied 2 changes");
    expect(within(folded).getByTestId("going-undo-button")).toHaveTextContent("Undo");
    expect(within(folded).queryByTestId("going-plan-row")).toBeNull();
    await user.click(within(folded).getByTestId("going-plan-title"));
    const card = within(old as HTMLElement).getByTestId("going-plan");
    expect(within(card).getAllByTestId("going-plan-row")).toHaveLength(2);
    expect(
      within(card)
        .getAllByTestId("going-plan-row-mark")
        .map((mark) => mark.getAttribute("data-mark"))
    ).toEqual(["done", "done"]);
  } finally {
    read.mockRestore();
    stream.mockRestore();
  }
});

// Design v3, rule 3 and state 7: once the plan showed, the run's steps stay folded into one line
// while the rows of its approved plan run, which the card shows as its rows' ticks: no step line
// and no timer opens above the card as the run works again.
test("while an approved plan's rows run, the run's steps stay folded above the card, with no timer", async () => {
  let send: ((name: string, data: unknown) => void) | undefined;
  const change: GoingPlanRow = {
    arguments: { issue: "DEMO-10", priority: 2 },
    changed: false,
    claim: null,
    class: "field",
    fields: ["priority"],
    issue: "DEMO-10",
    item: "DEMO-10",
    needs: [],
    now: { priority: null },
    result: null,
    row: 0,
    state: "waiting",
    tool: "dispatch_issue_update",
    waits: [],
  };
  const planOf = (state: GoingPlanAnswer["state"], rowState: GoingPlanRow["state"]) => ({
    mode: "ask" as const,
    plan: 1,
    rows: [{ ...change, state: rowState }],
    run: "r-1",
    state,
    summary: "",
    version: "v1",
  });
  let answer: GoingPlanAnswer = planOf("waiting", "waiting");
  const read = spyOn(api, "goingPlan").mockImplementation(async () => answer);
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((_id, onFrame) => {
    send = onFrame;
    onFrame("tool", {
      items: ["DEMO-10"],
      kind: "read",
      label: "Read DEMO-10",
      step: 1,
      tool: "read",
    });
    onFrame("plan", { plan: 1, state: "waiting" });
    return new Promise<void>(() => {});
  });
  try {
    openConversation({
      id: "c-1",
      newest_at: new Date().toISOString(),
      runs: [record("waiting", 4000, { words: "raise DEMO-10" })],
    });
    renderWithQueries(<Conversation />);
    const title = await screen.findByTestId("going-plan-title");
    expect(screen.getByTestId("going-steps-fold")).toHaveTextContent("read 1 item");

    // Approved, the run works again and its rows run: the card ticks them, and the steps stay
    // folded, with no line of their own and no timer.
    answer = planOf("running", "running");
    act(() => send?.("plan", { plan: 1, state: "running" }));
    act(() =>
      send?.("call", {
        kind: "write",
        label: "Changing DEMO-10…",
        step: 2,
        tool: "dispatch_issue_update",
      })
    );
    await waitFor(() => expect(title).toHaveTextContent("Applying 1 of 1…"));
    expect(screen.getByTestId("going-run")).toHaveAttribute("data-state", "working");
    expect(screen.getByTestId("going-steps-fold")).toHaveTextContent("read 1 item");
    expect(screen.queryAllByTestId("going-step")).toHaveLength(0);
    expect(screen.queryByTestId("going-step-timer")).toBeNull();

    // Its rows ran, and the run works on to write its answer: still folded.
    answer = planOf("done", "done");
    act(() => send?.("plan", { plan: 1, state: "done" }));
    await waitFor(() => expect(title).toHaveTextContent(/^Applied 1 change/u));
    expect(screen.getByTestId("going-run")).toHaveAttribute("data-state", "working");
    expect(screen.getByTestId("going-steps-fold")).toBeInTheDocument();
    expect(screen.queryAllByTestId("going-step")).toHaveLength(0);
  } finally {
    read.mockRestore();
    stream.mockRestore();
  }
});

// Each change row of a plan names its issue as a chip, and each issue an answer names is one: a
// chip reads its issue's summary, the one read every reference to the issue shares.
let summaries: { mockRestore: () => void } | undefined;
beforeEach(() => {
  summaries = spyOn(api, "getIssueSummary").mockImplementation(async (key: string) => ({
    documents: [],
    key,
    project: "DEMO",
    status: "todo",
    title: `The issue ${key}`,
  }));
});

afterEach(() => {
  cleanup();
  resetGoingStore();
  summaries?.mockRestore();
});

/** The words of each step line the bar draws, without the run's timer. */
function stepWords(): readonly (string | null)[] {
  return screen
    .queryAllByTestId("going-step")
    .map((step) => within(step).getByTestId("going-step-label").textContent);
}

/** The step lines the bar draws, each with whether it runs. */
function stepLines(): readonly (readonly [string | null, string | null])[] {
  return screen
    .getAllByTestId("going-step")
    .map((step) => [
      within(step).getByTestId("going-step-label").textContent,
      step.getAttribute("data-running"),
    ]);
}

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
      expect(stepWords()).toEqual(["Read DEMO-15", "Reading the messages of DEMO-7…"])
    );
    const steps = screen.getAllByTestId("going-step");
    expect(steps.map((step) => step.getAttribute("data-running"))).toEqual(["false", "true"]);
    expect(steps.every((step) => !step.hasAttribute("title"))).toBe(true);
    expect(screen.getByTestId("going-step-words").textContent).toBe(
      "Checking the issue and its messages."
    );
    // The key the ended call read is a plain link, and the newest line carries the run's timer.
    expect(within(steps[0] as HTMLElement).getByRole("link", { name: "DEMO-15" })).toHaveAttribute(
      "href",
      "/issues/DEMO-15"
    );
    expect(within(steps[1] as HTMLElement).getByTestId("going-step-timer")).toBeInTheDocument();
    // The steps and the timer say the run works.
    expect(screen.queryByTestId("going-run-state")).toBeNull();
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
    await waitFor(() =>
      expect(stepWords()).toEqual(["Could not read DEMO-12: you have no access"])
    );
    expect(screen.getByTestId("going-step").className).toContain(dangerText);
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
    expect(screen.queryByTestId("going-steps-fold")).toBeNull();
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
  const user = userEvent.setup();
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
    // The answer came, so the steps fold above it; the fold opens them.
    await user.click(screen.getByTestId("going-steps-fold"));
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
    // The prose stays as the header of the call it heads; the stopped run has no timer.
    expect(screen.getByTestId("going-step-words").textContent).toBe("Checking.");
    expect(screen.queryByTestId("going-step-timer")).toBeNull();
  } finally {
    restore();
  }
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
      // The step's line carries the run's timer on from the question; it says the run works.
      expect(screen.getByTestId("going-step-timer")).toHaveTextContent("2 s");
      expect(screen.queryByTestId("going-run-state")).toBeNull();
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

/** A search of DEMO that answered three hits, then a read of DEMO-1, then the answer. */
const SEARCHED_AND_READ: readonly (readonly [string, unknown])[] = [
  ["call", { kind: "search", label: "Searching DEMO", step: 2, text: "", tool: "search" }],
  [
    "tool",
    {
      count: 3,
      items: ["DEMO-1", "DEMO-2", "DEMO-3"],
      kind: "search",
      label: "Searched DEMO · 3 hits",
      step: 2,
      tool: "search",
    },
  ],
  ["call", { kind: "read", label: "Reading DEMO-1", step: 4, text: "", tool: "read" }],
  ["tool", { items: ["DEMO-1"], kind: "read", label: "Read DEMO-1", step: 4, tool: "read" }],
  ["reply", { labels: [], step: 5, text: "DEMO-1 is the oldest.", tools: [] }],
  ["end", { offers: [], reason: "", state: "done" }],
];

test("once the answer came, the steps fold into one line above it, Worked for and what they did, which a click opens", async () => {
  const user = userEvent.setup();
  const restore = await showRun("done", SEARCHED_AND_READ, { workingMs: 108_000 });
  try {
    const fold = await screen.findByTestId("going-steps-fold");
    expect(fold).toHaveAccessibleName("Worked for 1 min 48 s · read 1 item, searched once");
    expect(fold.compareDocumentPosition(screen.getByTestId("going-run-answer"))).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING
    );
    expect(stepWords()).toEqual([]);
    await user.click(fold);
    expect(stepWords()).toEqual(["Searched DEMO · 3 hits", "Read DEMO-1"]);
  } finally {
    restore();
  }
});

test("Worked for counts from the question to the frame that ended the run, for a run started here", async () => {
  await onFakeClock(async () => {
    let send: ((name: string, data: unknown) => void) | undefined;
    const start = spyOn(api, "createGoingRun").mockResolvedValue(record("working"));
    const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((_id, onFrame) => {
      send = onFrame;
      return new Promise<void>(() => {});
    });
    try {
      renderWithQueries(<Conversation />);
      await act(() => startRun("where is DEMO-15", { path: "/" }, "alice"));
      await tick(3000);
      act(() => {
        for (const [name, data] of SEARCHED_AND_READ) send?.(name, data);
      });
      expect(screen.getByTestId("going-steps-fold")).toHaveAccessibleName(
        "Worked for 3 s · read 1 item, searched once"
      );
    } finally {
      start.mockRestore();
      stream.mockRestore();
    }
  });
});

test("under the answer, Done in grey, with Copy of the answer's text and Retry of the same words", async () => {
  const user = userEvent.setup();
  const retried: string[] = [];
  const writeText = spyOn({ writeText: async (_text: string) => undefined }, "writeText");
  const clipboard = navigator.clipboard;
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  const restore = await showRun("done", SEARCHED_AND_READ, {
    onRetry: (words) => retried.push(words),
  });
  try {
    const foot = await screen.findByTestId("going-run-foot");
    expect(within(foot).getByTestId("going-run-state").textContent).toBe("Done");
    await user.click(within(foot).getByRole("button", { name: "Copy the answer" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("DEMO-1 is the oldest."));
    await user.click(within(foot).getByRole("button", { name: "Retry" }));
    expect(retried).toEqual(["where is DEMO-15"]);
  } finally {
    restore();
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: clipboard });
  }
});

test("an answer whose frame says it waits for the person ends with Waits for your answer, not Done", async () => {
  const restore = await showRun("done", [
    [
      "reply",
      {
        labels: [],
        text: "Which changes hand it over: a label or a status",
        tools: [],
        waits_for_person: true,
      },
    ],
    ["end", { offers: [], reason: "", state: "done" }],
  ]);
  try {
    expect((await screen.findByTestId("going-run-state")).textContent).toBe(
      "Waits for your answer"
    );
  } finally {
    restore();
  }
});

const NOTHING_CHANGED = "Nothing changed: no plan was proposed.";

/** An answer, and the end of a run done with it. */
const ANSWERED_DONE: readonly (readonly [string, unknown])[] = [
  ["reply", { labels: [], text: "DEMO-15 is marked as started.", tools: [] }],
  ["end", { offers: [], reason: "", state: "done" }],
];

/** What the line under the run's answer says. */
async function footWords(): Promise<string | null> {
  const foot = await screen.findByTestId("going-run-foot");
  return within(foot).getByTestId("going-run-state").textContent;
}

test("a run of the line Do that ended done with no plan says nothing changed in place of Done, read from its stored record", async () => {
  const restore = await showRun("done", ANSWERED_DONE, { run: { line: "do" } });
  try {
    expect(await footWords()).toBe(NOTHING_CHANGED);
    expect(screen.getByTestId("going-run-answer")).toHaveTextContent(
      "DEMO-15 is marked as started."
    );
    expect(within(screen.getByTestId("going-run")).queryByText("Done")).toBeNull();
  } finally {
    restore();
  }
});

test("a run started here from the line Do sends the line, and says nothing changed once its frames end it done with no plan", async () => {
  let send: ((name: string, data: unknown) => void) | undefined;
  const start = spyOn(api, "createGoingRun").mockResolvedValue(
    record("working", 1000, { line: "do", words: "mark DEMO-15 started" })
  );
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((_id, onFrame) => {
    send = onFrame;
    return new Promise<void>(() => {});
  });
  try {
    renderWithQueries(<Conversation />);
    await act(() => startRun("mark DEMO-15 started", { path: "/" }, "alice", { line: "do" }));
    expect(start.mock.calls[0]?.[0]).toMatchObject({ line: "do", words: "mark DEMO-15 started" });
    act(() => send?.("reply", { labels: [], text: "DEMO-15 is marked as started.", tools: [] }));
    expect(screen.queryByText(NOTHING_CHANGED)).toBeNull();
    act(() => send?.("end", { offers: [], reason: "", state: "done" }));
    expect(await footWords()).toBe(NOTHING_CHANGED);
  } finally {
    start.mockRestore();
    stream.mockRestore();
  }
});

test("a run of the line Ask that ended done with no plan ends Done, as before", async () => {
  const restore = await showRun("done", ANSWERED_DONE, { run: { line: "ask" } });
  try {
    expect(await footWords()).toBe("Done");
  } finally {
    restore();
  }
});

test("a run started from no line that ended done with no plan ends Done", async () => {
  const restore = await showRun("done", ANSWERED_DONE);
  try {
    expect(await footWords()).toBe("Done");
  } finally {
    restore();
  }
});

test("a run of the line Do whose answer waits for the person ends Waits for your answer, not that nothing changed", async () => {
  const restore = await showRun(
    "done",
    [
      [
        "reply",
        {
          labels: [],
          text: "Which changes hand it over: a label or a status",
          tools: [],
          waits_for_person: true,
        },
      ],
      ["end", { offers: [], reason: "", state: "done" }],
    ],
    { run: { line: "do" } }
  );
  try {
    expect(await footWords()).toBe("Waits for your answer");
  } finally {
    restore();
  }
});

test("a run of the line Do that proposed a plan ends Done, not that nothing changed", async () => {
  const read = spyOn(api, "goingPlan").mockImplementation(async () => ({
    mode: "ask",
    plan: 1,
    rows: [],
    run: "r-1",
    state: "rejected",
    summary: "Mark DEMO-15 started.",
    version: "v1",
  }));
  const restore = await showRun(
    "done",
    [
      ["plan", { plan: 1, state: "waiting" }],
      ["plan", { plan: 1, state: "rejected" }],
      ...ANSWERED_DONE,
    ],
    { run: { line: "do" } }
  );
  try {
    expect(await footWords()).toBe("Done");
    expect(screen.queryByText(NOTHING_CHANGED)).toBeNull();
  } finally {
    restore();
    read.mockRestore();
  }
});

test("each issue the answer links is a chip: its key, a status dot and its title", async () => {
  const summary = spyOn(api, "getIssueSummary").mockResolvedValue({
    documents: [],
    key: "DEMO-1",
    project: "DEMO",
    status: "in_progress",
    title: "Map the lunar craters",
  });
  const restore = await showRun("done", [
    ["reply", { labels: [], text: "The oldest is [DEMO-1](dispatch://DEMO-1).", tools: [] }],
    ["end", { offers: [], reason: "", state: "done" }],
  ]);
  try {
    const answer = await screen.findByTestId("going-run-answer");
    const chip = await within(answer).findByTestId("item-chip");
    await waitFor(() => expect(chip).toHaveTextContent("DEMO-1In progressMap the lunar craters"));
    expect(within(chip).getByTestId("item-chip-status")).toHaveAttribute("title", "In progress");
    expect(within(answer).getByRole("link")).toHaveAttribute("href", "/issues/DEMO-1");
  } finally {
    restore();
    summary.mockRestore();
  }
});

test("a run whose stream dropped reads Reconnecting… until the stream comes back, and its steps carry on", async () => {
  let send: ((name: string, data: unknown) => void) | undefined;
  let dropped: ((dropped: boolean) => void) | undefined;
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation(
    (_id, onFrame, _signal, onDropped) => {
      send = onFrame;
      dropped = onDropped;
      return new Promise<void>(() => {});
    }
  );
  try {
    openConversation({ id: "c-1", newest_at: new Date().toISOString(), runs: [record("working")] });
    renderWithQueries(<Conversation />);
    act(() => send?.("call", { kind: "read", label: "Reading DEMO-15", step: 2, tool: "read" }));
    expect(screen.queryByTestId("going-run-reconnecting")).toBeNull();

    act(() => dropped?.(true));
    expect(screen.getByTestId("going-run-reconnecting").textContent).toBe("Reconnecting…");
    expect(stepWords()).toEqual(["Reading DEMO-15…"]);

    act(() =>
      send?.("tool", {
        items: ["DEMO-15"],
        kind: "read",
        label: "Read DEMO-15",
        step: 2,
        tool: "read",
      })
    );
    expect(screen.queryByTestId("going-run-reconnecting")).toBeNull();
    expect(stepWords()).toEqual(["Read DEMO-15"]);
  } finally {
    stream.mockRestore();
  }
});
