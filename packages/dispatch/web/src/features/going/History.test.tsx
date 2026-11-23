import { afterEach, expect, spyOn, test } from "bun:test";
import { act, cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";

import { keyedAttempt } from "../../__tests__/keyed-attempt";
import { renderWithQueries } from "../../__tests__/query-provider";
import { answerRoute } from "../../__tests__/send-answers";
import { api, type WriteBounds } from "../../api/client";
import type { GoingConversation, GoingRun, GoingUndoAnswer } from "../../api/types";
import { ConversationView } from "./ConversationView";
import { goingState, openConversation, resetGoingStore } from "./going-store";
import { History } from "./History";
import { useOpenPlan } from "./usePlan";

// History as the person reads it (spec R6, R11; design v3, state 11): the conversations of the
// last 7 days grouped Today, Yesterday and This week, each row its title, its state in words and
// its time; a box that filters them as the person types; the keys that move through them and open
// one; the Undo of a row whose newest run changed something, as a filing from the line on top
// does; and a press that shows a conversation in the bar.

const now = Date.now();
/** The reader's midnight today: a turn an hour before it was yesterday, one 3 days before it
 *  this week, whatever the time of day the test runs at. */
const midnight = new Date(new Date(now).setHours(0, 0, 0, 0)).getTime();

function run(id: string, words: string, at: number, extra: Partial<GoingRun> = {}): GoingRun {
  const iso = new Date(at).toISOString();
  return {
    context: null,
    continues: null,
    conversation: "c-1",
    created_at: iso,
    finished_at: iso,
    id,
    issues: [],
    level: "standard",
    line: null,
    mode: "ask",
    state: "done",
    stop_reason: null,
    used: { calls: 2, tokens: 10, working_ms: 1000 },
    words,
    ...extra,
  };
}

/** Three conversations, the newest first: today's split, whose second run changed two issues;
 *  yesterday's question, whose plan still waits; and this week's filing from the line on top,
 *  which filed CORE-46. */
const conversations: GoingConversation[] = [
  {
    id: "c-3",
    newest_at: new Date(now).toISOString(),
    runs: [
      run("r-4", "now move CORE-3 under the new one", now, {
        continues: "r-3",
        conversation: "c-3",
        issues: ["CORE-1", "CORE-3"],
      }),
      run("r-3", "split CORE-1 in two", now, { conversation: "c-3" }),
    ],
  },
  {
    id: "c-2",
    newest_at: new Date(midnight - 3_600_000).toISOString(),
    runs: [
      run("r-2", "close the stale duplicates", midnight - 3_600_000, {
        conversation: "c-2",
        finished_at: null,
        state: "waiting",
      }),
    ],
  },
  {
    id: "c-1",
    newest_at: new Date(midnight - 3 * 86_400_000).toISOString(),
    runs: [
      run("r-1", "file the telescope's mirror as a new issue", midnight - 3 * 86_400_000, {
        issues: ["CORE-46"],
        level: "fast",
      }),
    ],
  },
];

function listConversations(rows: GoingConversation[] = conversations) {
  return spyOn(api, "myGoingRuns").mockResolvedValue({
    limit: 50,
    offset: 0,
    rows,
    total: rows.length,
  });
}

function renderHistory(onOpen: () => void = () => {}): void {
  renderWithQueries(<History onOpen={onOpen} />);
}

/** The conversation as the bar draws it beside History. */
function Conversation(): ReactNode {
  return <ConversationView answering={useOpenPlan()} onContinue={() => {}} onRetry={() => {}} />;
}

/** The titles of the rows History lists, in its order. */
function titles(): (string | null)[] {
  return screen.queryAllByTestId("going-history-title").map((title) => title.textContent);
}

afterEach(() => {
  cleanup();
  resetGoingStore();
});

test("History groups the conversations Today, Yesterday and This week, each row its title, state and time, and Undo only where its newest run changed something", async () => {
  const read = listConversations();
  try {
    renderHistory();
    const groups = await screen.findAllByTestId("going-history-group");
    expect(groups.map((group) => group.getAttribute("data-group"))).toEqual([
      "today",
      "yesterday",
      "week",
    ]);
    expect(groups.map((group) => within(group).getByRole("heading").textContent)).toEqual([
      "Today",
      "Yesterday",
      "This week",
    ]);
    const rows = screen.getAllByTestId("going-history-conversation");
    expect(
      rows.map((row) => [
        row.getAttribute("data-conversation"),
        within(row).getByTestId("going-history-title").textContent,
        within(row).getByTestId("going-history-state").textContent,
        row.querySelector("time")?.getAttribute("datetime"),
      ])
    ).toEqual([
      ["c-3", "split CORE-1 in two", "Done", conversations[0]?.newest_at],
      ["c-2", "close the stale duplicates", "Needs you", conversations[1]?.newest_at],
      ["c-1", "file the telescope's mirror as a new issue", "Done", conversations[2]?.newest_at],
    ]);
    // The split's newest run touched CORE-1 and CORE-3, and the filing filed CORE-46: each row
    // holds its run's Undo. The plan that waits changed nothing yet.
    const undos = screen.getAllByTestId("going-undo");
    expect(undos.map((undo) => undo.getAttribute("data-run"))).toEqual(["r-4", "r-1"]);
  } finally {
    read.mockRestore();
  }
});

test("Undo in History undoes that run and names a value another person changed, and a message sent, not undone", async () => {
  const user = userEvent.setup();
  const read = listConversations();
  const undo = answerRoute<
    (run: string, body: undefined, bounds: WriteBounds) => Promise<GoingUndoAnswer>
  >("POST", "/api/v1/going/runs/*/undo");
  undo.mockImplementation(async () => ({
    lines: [
      {
        fields: [{ field: "parent", message: "bob set it to CORE-9 since", ok: false }],
        item: "CORE-3",
        outcome: { code: "", message: "", ok: true, status: 200 },
        plan: 1,
        row: 2,
        tool: "dispatch_issue_update",
        undo: "write_back",
      },
      {
        fields: [],
        item: "CORE-1",
        outcome: { code: "", message: "", ok: true, status: 200 },
        plan: 1,
        row: 1,
        tool: "dispatch_message",
        undo: "not_undone",
      },
    ],
  }));
  try {
    renderHistory();
    const [first] = await screen.findAllByTestId("going-undo-button");
    await user.click(first as HTMLElement);

    await waitFor(() => expect(undo).toHaveBeenCalledWith("r-4", undefined, keyedAttempt));
    const lines = await screen.findAllByTestId("going-undo-line");
    // Each line names its change and its field as the plan card named them, never by a tool.
    expect(lines.map((line) => line.textContent)).toEqual([
      "Update CORE-3: Parent kept: bob set it to CORE-9 since",
      "Send a message to CORE-1: Sent, not undone",
    ]);
  } finally {
    read.mockRestore();
  }
});

test("typing filters History by every question of a conversation, and the arrows and Enter open one", async () => {
  const user = userEvent.setup();
  const read = listConversations();
  // Each opened run's frames are read again from its stream; the stream answers nothing here.
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation(
    () => new Promise<void>(() => {})
  );
  let opened = 0;
  try {
    renderHistory(() => {
      opened += 1;
    });
    await screen.findAllByTestId("going-history-conversation");
    // The box takes the focus as History opens.
    const filter = screen.getByRole("searchbox", { name: "Filter History" });
    expect(filter).toHaveFocus();

    // A later question of a conversation finds it too.
    await user.keyboard("move core-3");
    expect(titles()).toEqual(["split CORE-1 in two"]);
    await user.clear(filter);
    await user.keyboard("comet");
    expect(titles()).toEqual([]);
    expect(screen.getByText("No conversation matches “comet”.")).toBeInTheDocument();
    await user.clear(filter);
    expect(titles()).toHaveLength(3);

    // Down moves the focus through the rows, and Up from the first goes back to the box.
    const rows = screen.getAllByTestId("going-history-open");
    await user.keyboard("{ArrowDown}");
    expect(rows[0]).toHaveFocus();
    await user.keyboard("{ArrowDown}");
    expect(rows[1]).toHaveFocus();
    await user.keyboard("{ArrowUp}{ArrowUp}");
    expect(filter).toHaveFocus();

    // Typing on a row goes on in the box.
    await user.keyboard("{ArrowDown}{ArrowDown}{ArrowDown}");
    expect(rows[2]).toHaveFocus();
    await user.keyboard("tel");
    expect(filter).toHaveFocus();
    expect(filter).toHaveValue("tel");
    expect(titles()).toEqual(["file the telescope's mirror as a new issue"]);

    // Enter in the box opens the first row; Enter on a row opens that row.
    await user.keyboard("{Enter}");
    expect(opened).toBe(1);
    expect(goingState().conversation).toEqual(["r-1"]);
    await user.clear(filter);
    await user.keyboard("{ArrowDown}{ArrowDown}{Enter}");
    expect(opened).toBe(2);
    expect(goingState().conversation).toEqual(["r-2"]);
    expect(goingState().place).toBe("over");
  } finally {
    read.mockRestore();
    stream.mockRestore();
  }
});

test("a press on a row shows the conversation in the bar, its runs in the order they were asked", async () => {
  const user = userEvent.setup();
  const read = listConversations();
  // Each opened run's frames are read again from its stream; the stream answers nothing here.
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation(
    () => new Promise<void>(() => {})
  );
  let opened = 0;
  try {
    renderHistory(() => {
      opened += 1;
    });
    const [first] = await screen.findAllByTestId("going-history-open");
    await user.click(first as HTMLElement);

    expect(opened).toBe(1);
    expect(goingState().conversation).toEqual(["r-3", "r-4"]);
    expect(goingState().place).toBe("over");
    expect(stream.mock.calls.map(([id]) => id)).toEqual(["r-3", "r-4"]);
  } finally {
    read.mockRestore();
    stream.mockRestore();
  }
});

test("a row says how its newest run stands as this page follows it", async () => {
  const working = run("r-9", "count the moons", now, { conversation: "c-9", state: "working" });
  const listed: GoingConversation = {
    id: "c-9",
    newest_at: working.created_at,
    runs: [working],
  };
  const read = listConversations([listed]);
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation(async (_id, onFrame) => {
    onFrame("reply", { labels: [], text: "Many.", tools: [] });
    onFrame("end", { offers: [], reason: "", state: "done" });
  });
  try {
    renderHistory();
    const state = await screen.findByTestId("going-history-state");
    expect(state).toHaveTextContent("Working");
    // The page reads the run's stream to its end: the list still says it works, and the row says
    // it is done.
    await act(async () => openConversation(listed));
    await waitFor(() =>
      expect(screen.getByTestId("going-history-state")).toHaveTextContent("Done")
    );
  } finally {
    read.mockRestore();
    stream.mockRestore();
  }
});

// A run's frames are read from its stream's first frame. Opening its conversation again, from
// History or from the Inbox, shows each step once: a run read to its end is not read again, and
// one read again starts its frames afresh.
test("opening a conversation again shows each step of its runs once", async () => {
  const user = userEvent.setup();
  const read = listConversations();
  // Each run's stream carries a reply that calls a tool, the call's own frame, and the end.
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation(async (id, onFrame) => {
    onFrame("reply", { labels: [`Reading the notes of ${id}`], text: "", tools: ["call_tool"] });
    onFrame("tool", {
      kind: "read",
      label: `Read the notes of ${id}`,
      note: `read for ${id}`,
      step: 2,
      tool: "call_tool",
    });
    onFrame("end", { offers: [], reason: "", state: "done" });
  });
  try {
    renderWithQueries(
      <>
        <History onOpen={() => {}} />
        <Conversation />
      </>
    );
    const [first] = await screen.findAllByTestId("going-history-open");
    await user.click(first as HTMLElement);
    const steps = (run: string) =>
      screen
        .getAllByTestId("going-run")
        .filter((shown) => shown.getAttribute("data-run") === run)
        .flatMap((shown) => within(shown).queryAllByTestId("going-step"))
        .map((step) => step.textContent);
    await waitFor(() => expect(steps("r-3")).toEqual(["Read the notes of r-3"]));

    await user.click(first as HTMLElement);

    expect(steps("r-3")).toEqual(["Read the notes of r-3"]);
    expect(steps("r-4")).toEqual(["Read the notes of r-4"]);
    expect(stream.mock.calls.map(([id]) => id)).toEqual(["r-3", "r-4"]);
  } finally {
    read.mockRestore();
    stream.mockRestore();
  }
});

// A run its level's bound stopped offers Continue; a run its person or its grant stopped - the
// session ended, the token was revoked - offers none. History says "Stopped" of each, and holds
// which it was before the run's frames are read.
test("History names a stop a bound's only where its reason is one of the level's budgets", async () => {
  const stopped = (id: string, reason: string): GoingConversation => {
    const ran = run(id, `run ${id}`, now, {
      conversation: `c-${id}`,
      state: "stopped",
      stop_reason: reason,
    });
    return { id: `c-${id}`, newest_at: ran.created_at, runs: [ran] };
  };
  const read = listConversations([
    stopped("calls", "CALL_BUDGET: this run made the 6 model calls a Fast run may"),
    stopped("tokens", "OUTPUT_TOKEN_BUDGET: a model call wrote the tokens it may"),
    stopped("person", "STOPPED: its person stopped it"),
    stopped("session", "SESSION_ENDED: the session that asked ended"),
    stopped("token", "TOKEN_REVOKED: the grant's token was revoked"),
  ]);
  try {
    renderHistory();
    const rows = await screen.findAllByTestId("going-history-conversation");
    expect(
      rows.map((row) => [
        row.getAttribute("data-conversation"),
        row.getAttribute("data-state"),
        within(row).getByTestId("going-history-state").textContent,
      ])
    ).toEqual([
      ["c-calls", "stopped_bound", "Stopped"],
      ["c-tokens", "stopped_bound", "Stopped"],
      ["c-person", "stopped_person", "Stopped"],
      ["c-session", "stopped_person", "Stopped"],
      ["c-token", "stopped_person", "Stopped"],
    ]);
  } finally {
    read.mockRestore();
  }
});
