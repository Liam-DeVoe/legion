import { afterEach, beforeEach, expect, setSystemTime, spyOn, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useState } from "react";
import { MemoryRouter } from "react-router-dom";

import { ApiError, api } from "../../api/client";
import { issueStepsQuery } from "../../api/queries";
import { DispatchQueryClient, queryRetry, queryRetryDelay } from "../../api/query-client";
import type { IssueDetails, IssueSteps } from "../../api/types";
import { stepRecord } from "../steps/step-record-fixture";
import { stepTasks } from "../steps/steps-model";
import { DetailsSteps } from "./DetailsSteps";
import { DetailsHistory } from "./details-more";
import { type MarginDrawer, MarginSlotProvider } from "./margin-slot";
import { ANSWER_LINES, CONTROL_LABELS } from "./page-words";

const KEY = "CORE-1";

// The runs are dated at hours of one day, built in the machine's own zone, and the section draws
// each as the instant it is, with its year only where it is not this one: each case runs at one
// fixed local midday of that day, so none reads the wall clock. `setSystemTime` holds `Date`
// alone; the timers the cases wait on (React Query's retries, `waitFor`'s) run on the real clock.
const NOON = new Date(2026, 10, 19, 12, 0, 0, 0);
beforeEach(() => setSystemTime(NOON));
afterEach(() => setSystemTime());

/** The instant `hour`:`minute` of the cases' day, in the machine's own zone. */
function at(hour: number, minute = 0): string {
  return new Date(2026, 10, 19, hour, minute).toISOString();
}

/** What the section draws of the instant `iso`, as the Steps tab draws it: the date and the time
 *  of day in the reader's locale. */
function drawnAt(iso: string): string {
  const date = new Date(iso);
  const day = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short" });
  const time = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" });
  return `${day.format(date)} ${time.format(date)}`;
}

/** Two tasks in the server's order, `docs` before `default`, which no sort would give: the
 *  passed run of the task `docs`; and in `default`, an implementation that passed, a review that
 *  failed and sent the task back to step 2, the implementation's second run, still running with
 *  no model yet, and a merge GitHub made, which names no type, agent, model or start. */
const TWO_TASKS: IssueSteps = {
  issue: KEY,
  tasks: [
    {
      steps: [
        stepRecord({
          ended_at: at(8, 30),
          id: "docs-1",
          started_at: at(8),
          step: 1,
          task: "docs",
          type: "documentation",
        }),
      ],
      task: "docs",
    },
    {
      steps: [
        stepRecord({ ended_at: at(9, 40), id: "implement-1", started_at: at(9) }),
        stepRecord({
          agent: "legion-core-core-1-reviewer",
          ended_at: at(10, 30),
          id: "review-1",
          model: "openai/gpt-6.1-sol",
          result: "failed",
          returned_to: 2,
          role: "reviewer",
          started_at: at(10),
          step: 4,
          type: "review",
        }),
        stepRecord({
          attempt: 2,
          ended_at: null,
          id: "implement-2",
          model: "",
          result: "",
          started_at: at(10, 31),
        }),
        stepRecord({
          agent: "",
          ended_at: at(11),
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
  ],
};

const NO_STEPS: IssueSteps = { issue: KEY, tasks: [] };

/** The client of the page, with the app's own retry policy (`main.tsx`): a failed read is asked
 *  again twice, 500 ms apart. */
function appClient(): DispatchQueryClient {
  return new DispatchQueryClient({
    defaultOptions: { queries: { retry: queryRetry, retryDelay: queryRetryDelay } },
  });
}

/** The section alone, reading the issue's steps through `read`, with Details open or shut. */
function renderSteps(read: () => Promise<IssueSteps>, open = true) {
  const listSteps = spyOn(api, "listSteps").mockImplementation(read);
  const client = appClient();
  const draw = (isOpen: boolean) => (
    <QueryClientProvider client={client}>
      <DetailsSteps issueKey={KEY} open={isOpen} />
    </QueryClientProvider>
  );
  const view = render(draw(open));
  return {
    client,
    listSteps,
    open: () => view.rerender(draw(true)),
    restore: () => {
      view.unmount();
      client.clear();
      listSteps.mockRestore();
    },
  };
}

/** The section's heading. */
function heading(): HTMLElement {
  return within(screen.getByRole("region", { name: CONTROL_LABELS.steps })).getByRole("heading", {
    level: 2,
  });
}

/** Waits until the heading counts `runs`, as it does once a read has answered. Its words are
 *  read as text: happy-dom lays out no inline box, so the name the accessibility tree computes
 *  spaces the count off from its brackets where a browser does not. */
async function headedWith(runs: number): Promise<void> {
  await waitFor(() => expect(heading().textContent).toBe(`${CONTROL_LABELS.steps} (${runs})`));
}

/** One run, by its record's id. */
function run(id: string): HTMLElement {
  const found = document.querySelector<HTMLElement>(`[data-step-run="${id}"]`);
  if (found === null) throw new Error(`the section draws no run ${id}`);
  return found;
}

/** Each labelled value of a run, by its field, as the reader reads it. */
function fieldsOf(id: string): Record<string, string> {
  return Object.fromEntries(
    [...run(id).querySelectorAll("[data-step-field]")].map((field) => [
      field.getAttribute("data-step-field"),
      field.querySelector("dd")?.textContent ?? "",
    ])
  );
}

// Details lists every run of the issue's steps, headed by how many it lists: one group per task in
// the order `stepTasks` gives, each run in the read's order, each field a labelled value of a list
// rather than a cell of a table. A review that failed names the step it sent the task back to, a
// run that has not ended reads as running, and every field the record does not hold reads None.
test("the steps read as a list per task, headed by the runs' count, each run's fields labelled", async () => {
  const steps = renderSteps(async () => TWO_TASKS);
  try {
    await headedWith(5);
    expect(screen.queryByRole("table"), "the runs are drawn as a table").toBeNull();
    expect(
      within(screen.getByRole("region", { name: CONTROL_LABELS.steps }))
        .getAllByRole("heading", { level: 3 })
        .map((group) => group.textContent)
    ).toEqual(stepTasks(TWO_TASKS).map(({ task }) => `${CONTROL_LABELS.steps_task} ${task}`));
    expect(
      [...document.querySelectorAll("[data-step-run]")].map((drawn) =>
        drawn.getAttribute("data-step-run")
      )
    ).toEqual(["docs-1", "implement-1", "review-1", "implement-2", "merge-1"]);

    const fields = CONTROL_LABELS.step_fields;
    expect(
      [...run("review-1").querySelectorAll("dt")].map((label) => label.textContent),
      "the failed review's fields are not labelled in the page's words"
    ).toEqual([
      fields.step,
      fields.role,
      fields.type,
      fields.agent,
      fields.model,
      fields.attempt,
      fields.started,
      fields.ended,
      fields.result,
      fields.returned_to,
    ]);
    expect(fieldsOf("review-1")).toEqual({
      agent: "legion-core-core-1-reviewer",
      attempt: "1",
      ended: drawnAt(at(10, 30)),
      model: "openai/gpt-6.1-sol",
      result: CONTROL_LABELS.step_results.failed,
      returned_to: "step 2",
      role: "reviewer",
      started: drawnAt(at(10)),
      step: "4",
      type: "review",
    });
    // Each time is the record's own instant, in a `<time>` that names it.
    expect(
      [...run("review-1").querySelectorAll("time")].map((time) => time.getAttribute("datetime"))
    ).toEqual([at(10), at(10, 30)]);

    // The second run of the implementation: the server's count, running, and no model or end.
    expect(fieldsOf("implement-2")).toMatchObject({
      attempt: "2",
      ended: CONTROL_LABELS.none,
      model: CONTROL_LABELS.none,
      result: CONTROL_LABELS.step_results.running,
    });
    // The merge GitHub made: no type, agent, model or start, each read the one way.
    expect(fieldsOf("merge-1")).toMatchObject({
      agent: CONTROL_LABELS.none,
      model: CONTROL_LABELS.none,
      result: CONTROL_LABELS.step_results.passed,
      started: CONTROL_LABELS.none,
      step: "Merge",
      type: CONTROL_LABELS.none,
    });
    // Only a run that sent its task back names where it sent it.
    for (const id of ["docs-1", "implement-1", "implement-2", "merge-1"]) {
      expect(fieldsOf(id), `${id} names a step it went back to`).not.toHaveProperty("returned_to");
    }
    expect(steps.listSteps).toHaveBeenCalledTimes(1);
  } finally {
    steps.restore();
  }
});

test("an issue with no runs reads None under its heading", async () => {
  const steps = renderSteps(async () => NO_STEPS);
  try {
    await headedWith(0);
    const section = screen.getByRole("region", { name: CONTROL_LABELS.steps });
    expect(within(section).getByText(CONTROL_LABELS.none, { exact: true })).toBeDefined();
    expect(section.querySelectorAll("[data-step-run]")).toHaveLength(0);
  } finally {
    steps.restore();
  }
});

// A read Dispatch refused is asked again twice, as every read of Details is, and then says so
// with its Retry, under the heading that has no count yet. Retry reads again and draws the runs;
// the focus it held goes to the heading once the line goes, never to the page. A later read that
// fails stands above the runs the read before it drew.
test("a refused read says so with a Retry that reads again, and a later failure stands above the runs", async () => {
  let refusals = 3;
  const steps = renderSteps(async () => {
    if (refusals > 0) {
      refusals -= 1;
      throw new ApiError(500, { code: "INTERNAL", error: "refused" });
    }
    return TWO_TASKS;
  });
  try {
    const failure = await screen.findByRole("alert");
    expect(failure.textContent).toBe(ANSWER_LINES.steps_unreadable);
    expect(steps.listSteps, "the refused read was not asked again twice").toHaveBeenCalledTimes(3);
    expect(heading().textContent, "the heading counts runs no read has answered").toBe(
      CONTROL_LABELS.steps
    );
    expect(screen.queryByText(ANSWER_LINES.loading_steps)).toBeNull();

    const retry = screen.getByRole("button", { name: CONTROL_LABELS.retry });
    act(() => retry.focus());
    await userEvent.keyboard("{Enter}");
    await headedWith(5);
    expect(steps.listSteps).toHaveBeenCalledTimes(4);
    expect(screen.queryByRole("alert")).toBeNull();
    expect(document.activeElement, "the Retry that went left the focus on the page").toBe(
      heading()
    );

    // A later read that fails, and its two retries, leave the runs drawn under the failure.
    refusals = 3;
    await act(() => steps.client.invalidateQueries({ queryKey: issueStepsQuery(KEY).queryKey }));
    const later = await screen.findByRole("alert", {}, { timeout: 10_000 });
    expect(later.textContent).toBe(ANSWER_LINES.steps_unreadable);
    expect(steps.listSteps).toHaveBeenCalledTimes(7);
    expect(heading().textContent).toBe(`${CONTROL_LABELS.steps} (5)`);
    expect(
      later.compareDocumentPosition(run("docs-1")) & Node.DOCUMENT_POSITION_FOLLOWING,
      "the failure is not drawn above the runs"
    ).toBeTruthy();
  } finally {
    steps.restore();
  }
});

// Shut, Details reads no steps; opened, the section reads them once.
test("the section reads nothing while Details is shut", async () => {
  const steps = renderSteps(async () => TWO_TASKS, false);
  try {
    expect(heading().textContent).toBe(CONTROL_LABELS.steps);
    expect(screen.getByText(ANSWER_LINES.loading_steps)).toBeDefined();
    await act(async () => {});
    expect(steps.listSteps, "the steps were read with Details shut").not.toHaveBeenCalled();

    steps.open();
    await headedWith(5);
    expect(steps.listSteps).toHaveBeenCalledTimes(1);
  } finally {
    steps.restore();
  }
});

const issue = {
  artifacts: [],
  key: KEY,
  status: "in_progress",
  title: "Review the spec",
} as unknown as IssueDetails;

/** Details' history and what follows it, as the page draws them inside the drawer, which starts
 *  shut; `hold.open` opens and shuts it. */
function HistoryInDrawer({ hold }: { hold: { open?: (open: boolean) => void } }): ReactNode {
  const [open, setOpen] = useState(false);
  hold.open = setOpen;
  const drawer: MarginDrawer = { open, setOpen, setPage: () => undefined };
  return (
    <MarginSlotProvider drawer={drawer}>
      <DetailsHistory issue={issue} state={undefined} />
    </MarginSlotProvider>
  );
}

// The section stands under the history in Details: a load that leaves Details shut draws neither
// and reads no steps, and an open of Details draws it under the history and reads them.
test("Details draws the steps under the history only once it opens, and reads them only then", async () => {
  const listSteps = spyOn(api, "listSteps").mockResolvedValue(TWO_TASKS);
  const getIssueEvents = spyOn(api, "getIssueEvents").mockResolvedValue([]);
  const client = appClient();
  // The outline's own reads, which the history draws from, held as the outline left them.
  client.setQueryData(["asks", KEY], []);
  client.setQueryData(["comments", KEY], []);
  client.setQueryData(["issue-messages", KEY], []);
  client.setDefaultOptions({
    queries: { retry: queryRetry, retryDelay: queryRetryDelay, staleTime: 30_000 },
  });
  const hold: { open?: (open: boolean) => void } = {};
  const view = render(
    <MemoryRouter initialEntries={[`/issues/${KEY}`]}>
      <QueryClientProvider client={client}>
        <HistoryInDrawer hold={hold} />
      </QueryClientProvider>
    </MemoryRouter>
  );
  try {
    await act(async () => {});
    expect(screen.queryByTestId("issue-steps")).toBeNull();
    expect(
      listSteps,
      "the steps were read on a load that left Details shut"
    ).not.toHaveBeenCalled();

    act(() => hold.open?.(true));
    await headedWith(5);
    expect(listSteps).toHaveBeenCalledTimes(1);
    const history = screen.getByTestId("issue-history");
    expect(
      history.compareDocumentPosition(screen.getByTestId("issue-steps")) &
        Node.DOCUMENT_POSITION_FOLLOWING,
      "the steps are not drawn under the history"
    ).toBeTruthy();
  } finally {
    view.unmount();
    client.clear();
    listSteps.mockRestore();
    getIssueEvents.mockRestore();
  }
});
