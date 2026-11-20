import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";

import { keyedAttempt } from "../../__tests__/keyed-attempt";
import { answerRoute } from "../../__tests__/send-answers";
import { api, type WriteBounds } from "../../api/client";
import type { GoingPlanAnswer, GoingPlanRow, GoingRun } from "../../api/types";
import { resetGoingStore, setPlace, startRun } from "./going-store";
import { PlannedChanges } from "./PlannedChanges";
import { PlanCard } from "./PlanRows";
import { useOpenPlan } from "./usePlan";

// Where a planned change is drawn and how the person answers it (spec R4, plan.md settlement 7):
// a page draws the rows of the open plan that change the items it shows, the plan card draws the
// rest, a plan whose conversation is not open draws on no page, and the answer carries each
// row's tick, where unticking a row unticks every row that needs it, and each edited value.

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
    waits: ["ask"],
    ...extra,
  };
}

/** A split of CORE-1: a new issue under CORE-1, a move of CORE-3 under it that needs it, and a
 *  message to the agent of CORE-1, which no list draws. */
const plan: GoingPlanAnswer = {
  mode: "ask",
  plan: 1,
  rows: [
    row(0, {
      arguments: { parent: "CORE-1", project: "CORE", title: "The second half" },
      class: "create_plain",
      tool: "dispatch_issue",
    }),
    row(1, {
      arguments: { issue: "CORE-3", parent: "CORE-2" },
      fields: ["parent"],
      issue: "CORE-3",
      item: "CORE-3",
      needs: [0],
      now: { parent: "CORE-1" },
    }),
    row(2, {
      arguments: { body: "Please review the split.", issue: "CORE-1" },
      class: "message",
      issue: "CORE-1",
      item: "CORE-1",
      tool: "dispatch_message",
    }),
  ],
  run: "run-1",
  state: "waiting",
  summary: "Split CORE-1 in two.",
};

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
  words: "split CORE-1 in two",
};

function Card(): ReactNode {
  return <PlanCard answering={useOpenPlan()} />;
}

function renderSurfaces(ui: ReactNode): void {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>);
}

/** Starts the conversation's run, whose stream carries its plan frame and then stays open while
 *  the plan waits, and shows the conversation over the page unless `place` keeps it closed. */
async function openPlan(place: "over" | "closed" = "over"): Promise<() => void> {
  const start = spyOn(api, "createGoingRun").mockResolvedValue(created);
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((_id, onFrame) => {
    onFrame("plan", { plan: 1, state: "waiting" });
    return Promise.withResolvers<void>().promise;
  });
  const read = spyOn(api, "goingPlan").mockResolvedValue(plan);
  await startRun("split CORE-1 in two", { path: "/projects/CORE/issues" }, "alice");
  setPlace(place);
  return () => {
    start.mockRestore();
    stream.mockRestore();
    read.mockRestore();
  };
}

function drawnRows(within_: HTMLElement): (string | null)[] {
  return within(within_)
    .getAllByTestId("going-plan-row")
    .map((drawn) => drawn.getAttribute("data-row"));
}

afterEach(() => {
  cleanup();
  resetGoingStore();
});

test("a plan with rows on the page and in the card shows each in its place, once", async () => {
  const restore = await openPlan();
  try {
    renderSurfaces(
      <>
        <PlannedChanges
          surface={{ issues: ["CORE-1", "CORE-3"], kind: "issues", project: "CORE" }}
        />
        <Card />
      </>
    );
    const page = await screen.findByTestId("going-plan-on-page");
    await waitFor(() =>
      expect(drawnRows(screen.getByTestId("going-plan-card-rows"))).toEqual(["2"])
    );
    expect(drawnRows(page)).toEqual(["0", "1"]);
  } finally {
    restore();
  }
});

test("a plan waiting in the Inbox draws on no page until its conversation is opened", async () => {
  // The run's plan waits, and its conversation is on no part of the screen: the bar was closed.
  const restore = await openPlan("closed");
  try {
    renderSurfaces(
      <PlannedChanges surface={{ issues: ["CORE-1", "CORE-3"], kind: "issues", project: "CORE" }} />
    );
    const pause = Promise.withResolvers<void>();
    setTimeout(pause.resolve, 50);
    await pause.promise;
    expect(screen.queryByTestId("going-plan-on-page")).toBeNull();
    // Opened again, its plan draws on the page.
    act(() => setPlace("over"));
    expect(await screen.findByTestId("going-plan-on-page")).toBeInTheDocument();
  } finally {
    restore();
  }
});

test("unticking a row unticks the row that needs it, and the approval carries each tick and each edited value", async () => {
  const user = userEvent.setup();
  const restore = await openPlan();
  const approve = answerRoute<
    (run: string, body: unknown, bounds: WriteBounds) => Promise<GoingPlanAnswer>
  >("POST", "/api/v1/going/runs/*/plan/approve");
  approve.mockImplementation(async () => ({ ...plan, state: "running" }));
  try {
    renderSurfaces(<Card />);
    const card = await screen.findByTestId("going-plan-card-rows");
    await user.click(within(card).getByRole("checkbox", { name: "Run issue" }));
    const ticks = within(card)
      .getAllByTestId("going-plan-row")
      .map((drawn) => drawn.getAttribute("data-ticked"));
    expect(ticks).toEqual(["false", "false", "true"]);
    // Ticking the move again ticks the new issue it needs.
    await user.click(within(card).getByRole("checkbox", { name: "Run issue update CORE-3" }));
    await user.click(within(card).getByRole("button", { name: "Edit title" }));
    const title = within(card).getByRole("textbox", { name: "New title" });
    await user.clear(title);
    await user.type(title, "The other half{Enter}");
    await user.click(screen.getByTestId("going-plan-approve"));

    await waitFor(() =>
      expect(approve).toHaveBeenCalledWith(
        "run-1",
        {
          plan: 1,
          rows: [
            { row: 0, tick: true, values: { title: "The other half" } },
            { row: 1, tick: true },
            { row: 2, tick: true },
          ],
        },
        keyedAttempt
      )
    );
  } finally {
    restore();
  }
});
