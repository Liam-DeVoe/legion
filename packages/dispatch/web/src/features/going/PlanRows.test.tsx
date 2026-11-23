import { afterEach, beforeEach, expect, spyOn, test } from "bun:test";
import { act, cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useState } from "react";

import { keyedAttempt } from "../../__tests__/keyed-attempt";
import { renderWithQueries } from "../../__tests__/query-provider";
import { answerRoute } from "../../__tests__/send-answers";
import { api, type WriteBounds } from "../../api/client";
import type { GoingPlanAnswer, GoingPlanRow, GoingRun, GoingUndoAnswer } from "../../api/types";
import { clockWords } from "../search/blocks/block-cells";
import { resetGoingStore, setPlace, startRun } from "./going-store";
import { PlannedChanges } from "./PlannedChanges";
import { PlanCard, usePlanCardKeys } from "./PlanRows";
import { BoxRow } from "./RunControls";
import { useOpenPlan } from "./usePlan";

// The plan card of a plan that waits (design v3, state 6; spec R4, R5, R7): its title made from
// its rows and its approval mode linked to its setting; each kind of row in the issue page's words
// - a change of fields, a new issue, a message, a row that takes something out, a row whose item
// changed - the page's own priority menu and label picker to edit a value; why a row waits in
// Auto-review; Approve, which counts the ticked rows that wait, and Discard; and the card's keys.

function row(number: number, extra: Partial<GoingPlanRow>): GoingPlanRow {
  return {
    arguments: {},
    changed: false,
    claim: null,
    class: "field",
    fields: [],
    issue: "",
    item: "",
    needs: [],
    now: null,
    result: null,
    row: number,
    state: "waiting",
    tool: "dispatch_issue_update",
    waits: [],
    ...extra,
  };
}

/** A change of DEMO-10's labels and priority: one label added, and a priority set. */
const labelsAndPriority = row(0, {
  arguments: { issue: "DEMO-10", labels: ["board", "accessibility"], priority: 2 },
  fields: ["labels", "priority"],
  issue: "DEMO-10",
  item: "DEMO-10",
  now: { labels: ["board"], priority: null },
});

function planOf(rows: GoingPlanRow[], extra: Partial<GoingPlanAnswer> = {}): GoingPlanAnswer {
  return {
    mode: "ask",
    plan: 1,
    rows,
    run: "run-1",
    state: "waiting",
    summary: "",
    version: "v1",
    ...extra,
  };
}

const created: GoingRun = {
  context: null,
  continues: null,
  conversation: "run-1",
  created_at: new Date().toISOString(),
  finished_at: null,
  id: "run-1",
  issues: [],
  level: "standard",
  mode: "ask",
  state: "queued",
  stop_reason: null,
  used: { calls: 0, tokens: 0, working_ms: 0 },
  words: "tidy DEMO-10",
};

/** Starts the conversation's run, whose stream carries its plan frame and then stays open while
 *  the plan waits, and shows the conversation over the page. The plan is read as `plan`. */
async function openPlan(plan: GoingPlanAnswer): Promise<() => void> {
  const start = spyOn(api, "createGoingRun").mockResolvedValue(created);
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((_id, onFrame) => {
    onFrame("plan", { plan: 1, state: "waiting" });
    return Promise.withResolvers<void>().promise;
  });
  const read = spyOn(api, "goingPlan").mockImplementation(async () => plan);
  await startRun("tidy DEMO-10", { path: "/projects/DEMO/issues" }, "alice");
  setPlace("over");
  return () => {
    start.mockRestore();
    stream.mockRestore();
    read.mockRestore();
  };
}

function Card(): ReactNode {
  return <PlanCard answering={useOpenPlan()} />;
}

/** The bar's box, as far as the card's keys go: ↑ in it, empty, moves the focus into the card. */
function Box(): ReactNode {
  const keys = usePlanCardKeys();
  const [words, setWords] = useState("");
  return (
    <input
      aria-label="Say what to change"
      onChange={(event) => setWords(event.target.value)}
      onKeyDown={(event) => {
        keys(event);
      }}
      value={words}
    />
  );
}

/** The card's row numbered `number`. */
function drawnRow(number: number): HTMLElement {
  const found = within(screen.getByTestId("going-plan-card-rows"))
    .getAllByTestId("going-plan-row")
    .find((drawn) => drawn.getAttribute("data-row") === String(number));
  if (found === undefined) throw new Error(`the card draws no row ${number}`);
  return found;
}

/** The line of `field` in `drawn`. */
function fieldOf(drawn: HTMLElement, field: string): HTMLElement {
  const found = within(drawn)
    .getAllByTestId("going-plan-field")
    .find((line) => line.getAttribute("data-field") === field);
  if (found === undefined) throw new Error(`the row draws no ${field}`);
  return found;
}

/** The words `element` shows: its text without the options of a menu, which show only once the
 *  menu is opened, with its spaces run together. */
function shownText(element: HTMLElement): string {
  const copy = element.cloneNode(true) as HTMLElement;
  for (const menu of copy.querySelectorAll("select")) menu.remove();
  return (copy.textContent ?? "").replace(/\s+/gu, " ").trim();
}

// Each change row's item is an issue chip, which reads its issue's summary: the one read every
// reference to the issue shares.
let summaries: { mockRestore: () => void } | undefined;
beforeEach(() => {
  summaries = spyOn(api, "getIssueSummary").mockImplementation(async (key: string) => ({
    documents: [],
    key,
    project: "DEMO",
    status: "in_progress",
    title: `The issue ${key}`,
  }));
});

afterEach(() => {
  cleanup();
  resetGoingStore();
  summaries?.mockRestore();
});

test("a change row names its item and each field in the page's words, the values as the page shows them", async () => {
  const restore = await openPlan(planOf([labelsAndPriority]));
  try {
    renderWithQueries(<Card />);
    const card = await screen.findByTestId("going-plan");
    expect(within(card).getByTestId("going-plan-title")).toHaveTextContent("Change DEMO-10");
    const mode = within(card).getByTestId("going-plan-mode");
    expect(mode).toHaveTextContent("Ask");
    expect(mode).toHaveAttribute("href", "/settings#command-bar-heading");

    const drawn = drawnRow(0);
    // The item is the issue's chip, as an answer names it: its key, its status and its title.
    const chip = within(within(drawn).getByTestId("going-plan-item")).getByTestId("item-chip");
    expect(chip).toHaveTextContent("DEMO-10");
    await waitFor(() => expect(chip).toHaveTextContent("DEMO-10In progressThe issue DEMO-10"));
    const priority = fieldOf(drawn, "priority");
    expect(priority).toHaveTextContent(/^Priority: No priority → P2/u);
    expect(within(priority).getByTestId("going-plan-now")).toHaveTextContent("No priority");
    const labels = fieldOf(drawn, "labels");
    expect(labels).toHaveTextContent(/^Labels: board →/u);
    // The label added is the one marked in green; the label kept is the page's plain chip.
    const added = labels.querySelector('[data-mark="added"]');
    expect(added?.textContent).toBe("added accessibility");
    expect(labels.querySelector('[data-mark="removed"]')).toBeNull();
    // No raw field name and no JSON.
    expect(drawn.textContent ?? "").not.toMatch(/[[\]{}"]|priority none|labels \[/u);
    expect(drawn).not.toHaveAttribute("data-removes");
  } finally {
    restore();
  }
});

test("the priority menu and the label picker edit a value, the row says Edited, and Approve carries the values", async () => {
  const user = userEvent.setup();
  const restore = await openPlan(planOf([labelsAndPriority]));
  const listIssues = spyOn(api, "listIssues").mockResolvedValue([]);
  const approve = answerRoute<
    (run: string, body: unknown, bounds: WriteBounds) => Promise<GoingPlanAnswer>
  >("POST", "/api/v1/going/runs/*/plan/approve");
  approve.mockImplementation(async () => planOf([labelsAndPriority], { state: "running" }));
  try {
    renderWithQueries(<Card />);
    await screen.findByTestId("going-plan-card-rows");
    expect(within(drawnRow(0)).queryByTestId("going-plan-edited")).toBeNull();

    await user.selectOptions(screen.getByRole("combobox", { name: "Priority of DEMO-10" }), "1");
    const priority = fieldOf(drawnRow(0), "priority");
    expect(within(priority).getByTestId("going-plan-edited")).toHaveTextContent("Edited");
    expect(priority).toHaveTextContent(/^Priority: No priority → P1/u);

    // A press on the new labels opens the issue page's label picker over them.
    await user.click(within(fieldOf(drawnRow(0), "labels")).getByTestId("going-plan-next"));
    const search = await screen.findByRole("combobox", { name: "Search or create label" });
    await user.type(search, "urgent{Enter}");
    await user.keyboard("{Escape}");
    const labels = await waitFor(() => {
      const line = fieldOf(drawnRow(0), "labels");
      expect(within(line).getByTestId("going-plan-edited")).toBeInTheDocument();
      return line;
    });
    expect(
      [...labels.querySelectorAll('[data-mark="added"]')].map((chip) => chip.textContent)
    ).toEqual(["added accessibility", "added urgent"]);

    await user.click(screen.getByTestId("going-plan-approve"));
    await waitFor(() =>
      expect(approve).toHaveBeenCalledWith(
        "run-1",
        {
          plan: 1,
          rows: [
            {
              row: 0,
              tick: true,
              values: { labels: ["board", "accessibility", "urgent"], priority: 1 },
            },
          ],
          version: "v1",
        },
        keyedAttempt
      )
    );
  } finally {
    listIssues.mockRestore();
    restore();
  }
});

test("a row that adds a label shows the labels now with the one it adds, and takes nothing out", async () => {
  const restore = await openPlan(
    planOf([
      row(0, {
        arguments: { issue: "DEMO-10", labels_add: ["accessibility"], priority: 2 },
        fields: ["labels", "priority"],
        issue: "DEMO-10",
        item: "DEMO-10",
        now: { labels: ["board"], priority: null },
      }),
    ])
  );
  try {
    renderWithQueries(<Card />);
    await screen.findByTestId("going-plan-card-rows");
    const labels = fieldOf(drawnRow(0), "labels");
    expect(labels).toHaveTextContent(/^Labels: board → board\s*added accessibility/u);
    expect(within(labels).getByTestId("going-plan-now")).toHaveTextContent(/^board$/u);
    expect(
      [...labels.querySelectorAll('[data-mark="added"]')].map((chip) => chip.textContent)
    ).toEqual(["added accessibility"]);
    expect(labels.querySelector('[data-mark="removed"]')).toBeNull();
    expect(drawnRow(0)).not.toHaveAttribute("data-removes");
    expect(within(drawnRow(0)).queryByTestId("going-plan-removes")).toBeNull();
  } finally {
    restore();
  }
});

test("a row that closes or removes something is marked in red, and a removed label is struck out", async () => {
  const restore = await openPlan(
    planOf([
      row(0, {
        arguments: { issue: "DEMO-1", labels: ["board"] },
        fields: ["labels"],
        issue: "DEMO-1",
        item: "DEMO-1",
        now: { labels: ["board", "a11y"] },
      }),
      row(1, {
        arguments: { issue: "DEMO-2", reason: "Done.", status: "done" },
        fields: ["status"],
        issue: "DEMO-2",
        item: "DEMO-2",
        now: { status: "in_progress" },
      }),
      row(2, {
        arguments: { issue: "DEMO-3", release: true },
        class: "claim",
        fields: ["claim"],
        issue: "DEMO-3",
        item: "DEMO-3",
        now: { id: "alice", kind: "user" },
        tool: "dispatch_claim",
      }),
      row(3, {
        arguments: { issue: "DEMO-4", status: "in_progress" },
        fields: ["status"],
        issue: "DEMO-4",
        item: "DEMO-4",
        now: { status: "todo" },
      }),
    ])
  );
  try {
    renderWithQueries(<Card />);
    await screen.findByTestId("going-plan-card-rows");
    expect(drawnRow(0)).toHaveAttribute("data-removes", "true");
    // The words name each value the row takes out, as the struck-out chip shows it.
    expect(within(drawnRow(0)).getByTestId("going-plan-removes")).toHaveTextContent(
      /^Removes a11y$/u
    );
    expect(drawnRow(0).querySelector('[data-mark="removed"]')?.textContent).toBe("removed a11y");
    expect(drawnRow(1)).toHaveAttribute("data-removes", "true");
    expect(within(drawnRow(1)).getByTestId("going-plan-removes")).toHaveTextContent(/^Closes$/u);
    // The new status is the issue page's own status menu, set to the status the row writes.
    expect(shownText(fieldOf(drawnRow(1), "status"))).toBe("Status: In progress →");
    expect(screen.getByRole("combobox", { name: "Status of DEMO-2" })).toHaveValue("done");
    expect(drawnRow(2)).toHaveAttribute("data-removes", "true");
    expect(fieldOf(drawnRow(2), "claim")).toHaveTextContent(/^Claimed by: alice → Nobody$/u);
    expect(drawnRow(3)).not.toHaveAttribute("data-removes");
    expect(screen.getByTestId("going-plan-title")).toHaveTextContent(
      "Change 3 issues and close DEMO-2"
    );
  } finally {
    restore();
  }
});

test("a new issue row shows New, its title, its fields on one line and its spec in sentences, and a later row names it with (new)", async () => {
  const spec =
    "# Split\n\nThe dome splits in two. Each half turns alone. The motor stays. Nothing else moves.";
  const parentOf = (number: number, key: string, parent: string) =>
    row(number, {
      arguments: { issue: key, parent },
      fields: ["parent"],
      issue: key,
      item: key,
      needs: parent.startsWith("$") ? [Number(parent.slice(1))] : [],
      now: { parent: "DEMO-1" },
    });
  const restore = await openPlan(
    planOf([
      row(0, {
        arguments: {
          labels: ["board"],
          parent: "DEMO-12",
          priority: 2,
          project: "DEMO",
          spec,
          title: "North half",
        },
        class: "create_text",
        tool: "dispatch_issue",
      }),
      row(1, {
        arguments: { project: "DEMO", title: "South half" },
        class: "create_plain",
        tool: "dispatch_issue",
      }),
      parentOf(2, "DEMO-3", "$0"),
      parentOf(3, "DEMO-4", "$1"),
      parentOf(4, "DEMO-5", "DEMO-12"),
    ])
  );
  try {
    renderWithQueries(<Card />);
    await screen.findByTestId("going-plan-card-rows");
    expect(screen.getByTestId("going-plan-title")).toHaveTextContent("File 2 issues and move 3");
    const made = drawnRow(0);
    expect(made).toHaveAttribute("data-kind", "new_issue");
    expect(made).toHaveTextContent(/^New/u);
    // A new issue has no key yet: it keeps its New chip, and no issue's chip.
    expect(within(made).queryByTestId("item-chip")).toBeNull();
    expect(within(fieldOf(made, "title")).getByTestId("going-plan-next")).toHaveTextContent(
      "North half"
    );
    expect(shownText(within(made).getByTestId("going-plan-new-issue-fields"))).toBe(
      "DEMO · under DEMO-12 · P2 · board"
    );
    expect(within(made).getByTestId("going-plan-spec-sentences")).toHaveTextContent(
      "The dome splits in two. Each half turns alone. The motor stays."
    );
    const whole = within(made).getByTestId("going-plan-spec");
    expect(whole).not.toHaveAttribute("open");
    expect(whole).toHaveTextContent("Nothing else moves.");
    expect(within(fieldOf(drawnRow(2), "parent")).getByTestId("going-plan-next")).toHaveTextContent(
      "North half (new)"
    );
  } finally {
    restore();
  }
});

test("message rows say whom they reach and how, show the whole text, and say they cannot be undone", async () => {
  const restore = await openPlan(
    planOf([
      row(0, {
        arguments: {
          body: "The rota is in the spec now.",
          delivery: "btw",
          session: "019a1c01-0d7b-7a2a-8e93-4a5b6c7d8e01",
        },
        class: "message",
        issue: "DEMO-15",
        tool: "dispatch_send_to_agent",
        waits: ["message"],
      }),
      row(1, {
        arguments: {
          body: "The deploy is paused until 14:00.",
          delivery: "aside",
          session_ids: ["s-1", "s-2", "s-3", "s-4"],
        },
        class: "broadcast",
        tool: "dispatch_broadcast",
        waits: ["broadcast"],
      }),
      row(2, {
        arguments: { body: "Please review the split.", issue: "DEMO-1" },
        class: "message",
        issue: "DEMO-1",
        item: "DEMO-1",
        tool: "dispatch_message",
        waits: ["message"],
      }),
    ])
  );
  try {
    renderWithQueries(<Card />);
    await screen.findByTestId("going-plan-card-rows");
    expect(screen.getByTestId("going-plan-title")).toHaveTextContent("Send 3 messages");
    const toSession = drawnRow(0);
    // The issue a message is on is that issue's chip, as an answer names it.
    const toSessionLine = within(toSession).getByTestId("going-plan-message");
    const onIssue = within(toSessionLine).getByTestId("item-chip");
    await waitFor(() => expect(onIssue).toHaveTextContent("DEMO-15In progressThe issue DEMO-15"));
    expect(toSessionLine).toHaveTextContent(
      /^Message to .+ on DEMO-15In progressThe issue DEMO-15 · as a BTW$/u
    );
    expect(within(toSession).getByTestId("going-plan-quote")).toHaveTextContent(
      "The rota is in the spec now."
    );
    const broadcast = drawnRow(1);
    expect(within(broadcast).getByTestId("going-plan-message")).toHaveTextContent(
      "Message to 4 sessions · as an aside"
    );
    expect(
      within(within(broadcast).getByTestId("going-plan-sessions")).getAllByRole("listitem")
    ).toHaveLength(4);
    expect(within(drawnRow(2)).getByTestId("going-plan-message")).toHaveTextContent(
      "Message on DEMO-1"
    );
    for (const number of [0, 1, 2]) {
      expect(within(drawnRow(number)).getByTestId("going-plan-final")).toHaveTextContent(
        "Cannot be undone once sent"
      );
      // In Ask every row waits, and no row says why.
      expect(within(drawnRow(number)).queryByTestId("going-plan-row-state")).toBeNull();
    }
  } finally {
    restore();
  }
});

test("in Auto-review a row that passed review says it ran, and a row that waits says why in words", async () => {
  const restore = await openPlan(
    planOf(
      [
        row(0, {
          arguments: { issue: "DEMO-1", priority: 1 },
          fields: ["priority"],
          issue: "DEMO-1",
          item: "DEMO-1",
          now: { priority: 3 },
          state: "done",
        }),
        row(1, {
          arguments: { issue: "DEMO-2", status: "in_progress" },
          fields: ["status"],
          issue: "DEMO-2",
          item: "DEMO-2",
          now: { status: "todo" },
          waits: ["claimed_by_session"],
        }),
        row(2, {
          arguments: { body: "Looks right.", issue: "DEMO-3" },
          class: "text",
          issue: "DEMO-3",
          item: "DEMO-3",
          tool: "dispatch_comment",
        }),
      ],
      { mode: "auto_review" }
    )
  );
  try {
    renderWithQueries(<Card />);
    await screen.findByTestId("going-plan-card-rows");
    expect(screen.getByTestId("going-plan-mode")).toHaveTextContent("Auto-review");
    expect(within(drawnRow(0)).getByTestId("going-plan-row-state")).toHaveTextContent(
      "Ran · passed review"
    );
    expect(fieldOf(drawnRow(0), "priority")).toHaveTextContent(/^Priority: P3 → P1$/u);
    expect(within(drawnRow(1)).getByTestId("going-plan-row-state")).toHaveTextContent(
      "Waits for you: an agent session holds this issue's claim"
    );
    expect(within(drawnRow(2)).getByTestId("going-plan-row-state")).toHaveTextContent(
      "Waits for you: it changes text, which Auto-review never runs"
    );
    expect(within(drawnRow(2)).getByTestId("going-plan-quote")).toHaveTextContent("Looks right.");
    // Approve counts the rows that wait, not the row that ran.
    expect(screen.getByTestId("going-plan-approve")).toHaveTextContent("Approve 2 changes");
  } finally {
    restore();
  }
});

test("unticking a row unticks the row that needs it, which says why, Approve counts what is left, and Discard rejects", async () => {
  const user = userEvent.setup();
  const restore = await openPlan(
    planOf([
      row(0, {
        arguments: { parent: "DEMO-1", project: "DEMO", title: "North half" },
        class: "create_plain",
        tool: "dispatch_issue",
      }),
      row(1, {
        arguments: { issue: "DEMO-3", parent: "$0" },
        fields: ["parent"],
        issue: "DEMO-3",
        item: "DEMO-3",
        needs: [0],
        now: { parent: "DEMO-1" },
      }),
      { ...labelsAndPriority, row: 2 },
    ])
  );
  const reject = answerRoute<
    (run: string, body: unknown, bounds: WriteBounds) => Promise<GoingPlanAnswer>
  >("POST", "/api/v1/going/runs/*/plan/reject");
  reject.mockImplementation(async () => planOf([], { state: "rejected" }));
  try {
    renderWithQueries(<Card />);
    await screen.findByTestId("going-plan-card-rows");
    const approve = screen.getByTestId("going-plan-approve");
    expect(approve).toHaveTextContent("Approve 3 changes");
    expect(within(drawnRow(1)).queryByTestId("going-plan-needs")).toBeNull();

    await user.click(screen.getByRole("checkbox", { name: "File North half" }));
    expect(drawnRow(1)).toHaveAttribute("data-ticked", "false");
    expect(within(drawnRow(1)).getByTestId("going-plan-needs")).toHaveTextContent(
      "Needs the change above"
    );
    expect(within(drawnRow(0)).queryByTestId("going-plan-needs")).toBeNull();
    expect(approve).toHaveTextContent("Approve 1 change");

    expect(screen.getByTestId("going-plan-reject")).toHaveTextContent("Discard");
    await user.click(screen.getByTestId("going-plan-reject"));
    await waitFor(() => expect(reject).toHaveBeenCalledWith("run-1", { plan: 1 }, keyedAttempt));
  } finally {
    restore();
  }
});

test("a row whose item changed since the plan showed is amber, says its value now, and waits to be ticked again", async () => {
  const user = userEvent.setup();
  const restore = await openPlan(
    planOf([
      row(0, {
        arguments: { issue: "DEMO-3", title: "Raise the dome" },
        changed: true,
        fields: ["title"],
        issue: "DEMO-3",
        item: "DEMO-3",
        now: { title: "Bob's dome" },
      }),
      row(1, { ...labelsAndPriority, needs: [0], row: 1 }),
    ])
  );
  try {
    renderWithQueries(<Card />);
    await screen.findByTestId("going-plan-card-rows");
    const changed = drawnRow(0);
    expect(changed).toHaveAttribute("data-changed", "true");
    expect(within(changed).getByTestId("going-plan-changed")).toHaveTextContent(
      "Changed since the plan: now Bob's dome"
    );
    expect(fieldOf(changed, "title")).toHaveTextContent(/^Title: Bob's dome → Raise the dome/u);
    // It waits to be ticked again, and so does the row that needs it.
    await waitFor(() => expect(drawnRow(0)).toHaveAttribute("data-ticked", "false"));
    expect(drawnRow(1)).toHaveAttribute("data-ticked", "false");
    expect(screen.getByTestId("going-plan-approve")).toHaveTextContent("Approve 0 changes");
    expect(screen.getByTestId("going-plan-approve")).toBeDisabled();

    await user.click(within(drawnRow(1)).getByRole("checkbox"));
    expect(drawnRow(0)).toHaveAttribute("data-ticked", "true");
    expect(drawnRow(1)).toHaveAttribute("data-ticked", "true");
    expect(screen.getByTestId("going-plan-approve")).toHaveTextContent("Approve 2 changes");
  } finally {
    restore();
  }
});

test("the card notes the changes the page shows, and Approve counts them", async () => {
  const restore = await openPlan(
    planOf([
      row(0, {
        arguments: { issue: "DEMO-1", title: "North" },
        fields: ["title"],
        issue: "DEMO-1",
        item: "DEMO-1",
        now: { title: "Dome" },
      }),
      row(1, {
        arguments: { issue: "DEMO-3", title: "South" },
        fields: ["title"],
        issue: "DEMO-3",
        item: "DEMO-3",
        now: { title: "Dome" },
      }),
      row(2, {
        arguments: { body: "Split.", issue: "DEMO-1" },
        class: "message",
        issue: "DEMO-1",
        item: "DEMO-1",
        tool: "dispatch_message",
      }),
    ])
  );
  try {
    renderWithQueries(
      <>
        <PlannedChanges
          surface={{ issues: ["DEMO-1", "DEMO-3"], kind: "issues", project: "DEMO" }}
        />
        <Card />
      </>
    );
    expect(await screen.findByTestId("going-plan-on-page-note")).toHaveTextContent(
      "2 changes show on this page"
    );
    const card = screen.getByTestId("going-plan");
    expect(within(card).getByTestId("going-plan-approve")).toHaveTextContent("Approve 3 changes");
    // The page's rows have the card's look, in the Planned outline.
    const page = screen.getByTestId("going-plan-on-page");
    expect(within(page).getAllByTestId("going-plan-row")).toHaveLength(2);
    expect(
      fieldOf(within(page).getAllByTestId("going-plan-row")[0] as HTMLElement, "title")
    ).toHaveTextContent(/^Title: Dome → North/u);
  } finally {
    restore();
  }
});

test("↑ in the empty box moves into the card, where ↑ ↓ move, Space ticks, E edits, and Esc or a letter go back to the box", async () => {
  const user = userEvent.setup();
  const restore = await openPlan(
    planOf([
      row(0, {
        arguments: { issue: "DEMO-1", title: "North" },
        fields: ["title"],
        issue: "DEMO-1",
        item: "DEMO-1",
        now: { title: "Dome" },
      }),
      { ...labelsAndPriority, row: 1 },
      row(2, {
        arguments: { body: "Split.", issue: "DEMO-1" },
        class: "message",
        issue: "DEMO-1",
        item: "DEMO-1",
        tool: "dispatch_message",
      }),
    ])
  );
  try {
    renderWithQueries(
      <>
        <Card />
        <Box />
      </>
    );
    await screen.findByTestId("going-plan-card-rows");
    const box = screen.getByRole("textbox", { name: "Say what to change" });
    await user.click(box);
    await user.keyboard("{ArrowUp}");
    // The row nearest the box takes the focus.
    expect(drawnRow(2)).toHaveFocus();
    await user.keyboard("{ArrowUp}{ArrowUp}{ArrowUp}");
    expect(drawnRow(0)).toHaveFocus();
    await user.keyboard("{ArrowDown}");
    expect(drawnRow(1)).toHaveFocus();
    await user.keyboard(" ");
    expect(drawnRow(1)).toHaveAttribute("data-ticked", "false");
    await user.keyboard(" ");
    expect(drawnRow(1)).toHaveAttribute("data-ticked", "true");
    await user.keyboard("{Escape}");
    expect(box).toHaveFocus();

    // E opens the editor of the selected row's value: the title's box, with the focus in it.
    await user.keyboard("{ArrowUp}{ArrowUp}{ArrowUp}");
    expect(drawnRow(0)).toHaveFocus();
    await user.keyboard("e");
    expect(screen.getByRole("textbox", { name: "New title" })).toHaveFocus();
    await user.keyboard("{Escape}");

    // A letter puts the focus back in the box, the letter typed into it.
    await user.click(box);
    await user.keyboard("{ArrowUp}");
    expect(drawnRow(2)).toHaveFocus();
    await user.keyboard("k");
    expect(box).toHaveFocus();
    expect(box).toHaveValue("k");
    // With words in the box, ↑ is the box's own.
    await user.keyboard("{ArrowUp}");
    expect(box).toHaveFocus();
  } finally {
    restore();
  }
});

// Design v3, state 6, "Editing a value": a status and an assignee are edited with the issue
// page's own controls, the status menu and the assignee picker, and Approve carries the values.
test("the status menu and the assignee picker edit a row's values, and Approve carries them", async () => {
  const user = userEvent.setup();
  const listUsers = spyOn(api, "listUsers").mockResolvedValue([
    { login: "alice" },
    { login: "bob" },
  ]);
  const handOver = row(0, {
    arguments: { assignee: "alice", issue: "DEMO-5", status: "in_progress" },
    fields: ["assignee", "status"],
    issue: "DEMO-5",
    item: "DEMO-5",
    now: { assignee: null, status: "todo" },
  });
  const restore = await openPlan(planOf([handOver]));
  const approve = answerRoute<
    (run: string, body: unknown, bounds: WriteBounds) => Promise<GoingPlanAnswer>
  >("POST", "/api/v1/going/runs/*/plan/approve");
  approve.mockImplementation(async () => planOf([handOver], { state: "running" }));
  try {
    renderWithQueries(<Card />);
    await screen.findByTestId("going-plan-card-rows");
    const status = screen.getByRole("combobox", { name: "Status of DEMO-5" });
    expect(status).toHaveValue("in_progress");
    await user.selectOptions(status, "testing");
    expect(
      within(fieldOf(drawnRow(0), "status")).getByTestId("going-plan-edited")
    ).toBeInTheDocument();
    const assignee = screen.getByRole("combobox", { name: "Assignee of DEMO-5" });
    expect(assignee).toHaveValue("alice");
    // The picker reads who has signed in once the person reaches for it, as on the issue page.
    await user.hover(assignee);
    await waitFor(() => expect(within(assignee).getAllByRole("option")).toHaveLength(3));
    await user.selectOptions(assignee, "bob");
    expect(
      within(fieldOf(drawnRow(0), "assignee")).getByTestId("going-plan-edited")
    ).toBeInTheDocument();
    await user.click(screen.getByTestId("going-plan-approve"));
    await waitFor(() =>
      expect(approve).toHaveBeenCalledWith(
        "run-1",
        {
          plan: 1,
          rows: [{ row: 0, tick: true, values: { assignee: "bob", status: "testing" } }],
          version: "v1",
        },
        keyedAttempt
      )
    );
  } finally {
    listUsers.mockRestore();
    restore();
  }
});

/** A plan of three rows standing in `states`: a priority, a parent that needs the priority's
 *  row, and a message; the first failed with its reason where it failed, and the message queued
 *  where it ran. */
function sentRows(states: readonly GoingPlanRow["state"][]): GoingPlanRow[] {
  return [
    row(0, {
      arguments: { issue: "DEMO-1", priority: 1 },
      fields: ["priority"],
      issue: "DEMO-1",
      item: "DEMO-1",
      now: { priority: 3 },
      result:
        states[0] === "failed"
          ? { code: "FORBIDDEN", delivery: null, message: "you have no access", status: 403 }
          : null,
      state: states[0],
    }),
    row(1, {
      arguments: { issue: "DEMO-2", parent: "DEMO-1" },
      fields: ["parent"],
      issue: "DEMO-2",
      item: "DEMO-2",
      needs: [0],
      now: { parent: null },
      state: states[1],
    }),
    row(2, {
      arguments: { body: "Please look.", issue: "DEMO-1" },
      class: "message",
      issue: "DEMO-1",
      item: "DEMO-1",
      result:
        states[2] === "done" ? { code: "", delivery: "queued", message: "", status: 201 } : null,
      state: states[2],
      tool: "dispatch_message",
    }),
  ];
}

/** Each mark the card's rows draw. */
function marks(): readonly (string | null)[] {
  return screen.getAllByTestId("going-plan-row-mark").map((mark) => mark.getAttribute("data-mark"));
}

// Design v2, state 7: after approval each row ticks in place - a spinner while it runs, a ring
// while it waits its turn - and the title says which change runs.
test("while an approved plan runs, its title says Applying 1 of 3 and its rows tick in place", async () => {
  const restore = await openPlan(
    planOf(sentRows(["running", "approved", "approved"]), { state: "running" })
  );
  try {
    renderWithQueries(<Card />);
    await screen.findByTestId("going-plan-card-rows");
    expect(screen.getByTestId("going-plan-title")).toHaveTextContent("Applying 1 of 3…");
    expect(marks()).toEqual(["running", "turn", "turn"]);
    expect(screen.queryByTestId("going-plan-approve")).toBeNull();
    expect(screen.queryByTestId("going-plan-mode")).toBeNull();
    expect(screen.queryByTestId("going-undo-button")).toBeNull();
  } finally {
    restore();
  }
});

// Design v3, state 7: "Applied 3 changes · 16:42", the time the last row ran, which the run's end
// comes after while it writes its answer.
test("once its rows ran, the title says when, while the run still works", async () => {
  let answer = planOf([{ ...labelsAndPriority, state: "running" }], { state: "running" });
  let send: (name: string, data: unknown) => void = () => {};
  const start = spyOn(api, "createGoingRun").mockResolvedValue(created);
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((_id, onFrame) => {
    send = onFrame;
    onFrame("plan", { plan: 1, state: "running" });
    return Promise.withResolvers<void>().promise;
  });
  const read = spyOn(api, "goingPlan").mockImplementation(async () => answer);
  try {
    await startRun("tidy DEMO-10", { path: "/projects/DEMO/issues" }, "alice");
    setPlace("over");
    renderWithQueries(<Card />);
    const title = await screen.findByTestId("going-plan-title");
    await waitFor(() => expect(title).toHaveTextContent("Applying 1 of 1…"));

    answer = planOf([{ ...labelsAndPriority, state: "done" }], { state: "done" });
    const before = Date.now();
    act(() => send("plan", { plan: 1, state: "done" }));
    await waitFor(() => expect(title).toHaveTextContent(/^Applied 1 change · /u));
    const after = Date.now();
    expect([before, after].map((at) => `Applied 1 change · ${clockWords(at)}`)).toContain(
      title.textContent ?? ""
    );
  } finally {
    start.mockRestore();
    stream.mockRestore();
    read.mockRestore();
  }
});

// Design v2, state 7 (R4, R7): a failed row says why with "Say what to do instead", which puts
// the cursor in the box; the rows that needed it say they did not run; a message row says what
// became of it.
test("once it ran, a failed row says why and Say what to do instead puts the cursor in the box", async () => {
  const user = userEvent.setup();
  const restore = await openPlan(
    planOf(sentRows(["failed", "skipped", "done"]), { state: "done" })
  );
  try {
    renderWithQueries(<CardAndBox />);
    await screen.findByTestId("going-plan-card-rows");
    expect(screen.getByTestId("going-plan-title")).toHaveTextContent("Applied 1 of 3 changes");
    expect(marks()).toEqual(["failed", "not_run", "done"]);
    expect(within(drawnRow(0)).getByTestId("going-plan-row-state")).toHaveTextContent(
      "you have no access Say what to do instead"
    );
    expect(within(drawnRow(1)).getByTestId("going-plan-row-state")).toHaveTextContent(
      "Not run: it needs the change above"
    );
    expect(within(drawnRow(2)).getByTestId("going-plan-row-state")).toHaveTextContent(
      "Queued: Dispatch delivers it when the session can take it"
    );
    await user.click(within(drawnRow(0)).getByTestId("going-plan-fix"));
    expect(screen.getByRole("textbox", { name: "Ask the command bar" })).toHaveFocus();
  } finally {
    restore();
  }
});

// Design v2, state 7 (R6): the card keeps one Undo once a row ran; after it, the title says what
// was reverted, a value someone else changed since stays and its row names it as the page shows
// it, and a sent message's row reads "Sent, not undone".
test("Undo on the card reverts the run, names a value someone else changed, and a message sent, not undone", async () => {
  const user = userEvent.setup();
  const ran = planOf(
    [
      row(0, {
        arguments: { issue: "DEMO-1", priority: 1 },
        fields: ["priority"],
        issue: "DEMO-1",
        item: "DEMO-1",
        now: { priority: 3 },
        state: "done",
      }),
      row(1, {
        arguments: { issue: "DEMO-3", title: "Raise the dome" },
        fields: ["title"],
        issue: "DEMO-3",
        item: "DEMO-3",
        now: { title: "Dome" },
        state: "done",
      }),
      row(2, {
        arguments: { body: "Done.", issue: "DEMO-1" },
        class: "message",
        issue: "DEMO-1",
        item: "DEMO-1",
        result: { code: "", delivery: "sent", message: "", status: 201 },
        state: "done",
        tool: "dispatch_message",
      }),
    ],
    { state: "done" }
  );
  const restore = await openPlan(ran);
  const undo = answerRoute<
    (run: string, body: undefined, bounds: WriteBounds) => Promise<GoingUndoAnswer>
  >("POST", "/api/v1/going/runs/*/undo");
  undo.mockImplementation(async () => ({
    lines: [
      {
        fields: [],
        item: "DEMO-1",
        outcome: { code: "", message: "sent, not undone", ok: true, status: 200 },
        plan: 1,
        row: 2,
        tool: "dispatch_message",
        undo: "not_undone",
      },
      {
        fields: [
          {
            by: { id: "bob", kind: "user" },
            field: "title",
            message: `holds "Bob's dome" now, which user bob wrote after this run, so it is kept`,
            now: "Bob's dome",
            ok: false,
          },
        ],
        item: "DEMO-3",
        outcome: { code: "GOING_UNDO_CHANGED", message: "title: holds …", ok: false, status: 409 },
        plan: 1,
        row: 1,
        tool: "dispatch_issue_update",
        undo: "write_back",
      },
      {
        fields: [{ field: "priority", message: "written back", ok: true }],
        item: "DEMO-1",
        outcome: { code: "", message: "written back", ok: true, status: 200 },
        plan: 1,
        row: 0,
        tool: "dispatch_issue_update",
        undo: "write_back",
      },
    ],
  }));
  try {
    renderWithQueries(<Card />);
    await screen.findByTestId("going-plan-card-rows");
    expect(screen.getByTestId("going-plan-title")).toHaveTextContent("Applied 3 changes");
    expect(within(drawnRow(2)).getByTestId("going-plan-row-state")).toHaveTextContent("Sent");
    await user.click(screen.getByTestId("going-undo-button"));
    await waitFor(() => expect(undo).toHaveBeenCalledWith("run-1", undefined, keyedAttempt));
    await waitFor(() =>
      expect(screen.getByTestId("going-plan-title")).toHaveTextContent(
        "Undone · 1 of 2 changes reverted"
      )
    );
    expect(within(drawnRow(0)).getByTestId("going-undo-line")).toHaveTextContent("Reverted");
    expect(within(drawnRow(1)).getByTestId("going-undo-line").textContent).toBe(
      "Title stays Bob's dome: bob changed it after this run"
    );
    expect(within(drawnRow(2)).getByTestId("going-undo-line")).toHaveTextContent(
      "Sent, not undone"
    );
    expect(screen.queryByTestId("going-undo-button")).toBeNull();
    expect(screen.getByTestId("going-plan").textContent ?? "").not.toMatch(/[{}"]|GOING_/u);
  } finally {
    restore();
  }
});

/** The plan card above the conversation's box row, whose box "Say what to do instead" puts the
 *  cursor in. */
function CardAndBox(): ReactNode {
  return (
    <>
      <Card />
      <BoxRow
        box={<input aria-label="Ask the command bar" type="text" />}
        chip={undefined}
        hint={undefined}
        onStop={() => {}}
      />
    </>
  );
}
