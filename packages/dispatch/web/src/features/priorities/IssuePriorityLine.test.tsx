import { expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

import { keyedAttempt } from "../../__tests__/keyed-attempt";
import { listOf, textBoxes } from "../../__tests__/list-picks";
import { answerIssuePatches } from "../../__tests__/send-answers";
import { ApiError, api } from "../../api/client";
import type { Issue, Priorities, PriorityRow } from "../../api/types";
import { IssuePriorityLine } from "./IssuePriorityLine";

/**
 * The issue header's `Advances` item (INFRA-2947, R16 and R3): what it draws for a priority of
 * the issue's own and for one it inherits, what it draws for an issue that advances none, and
 * the two writes the control makes.
 */

function priority(id: string, title: string): PriorityRow {
  return {
    description: "",
    id,
    made_at: "2026-11-01T00:00:00Z",
    made_by: { id: "alice", kind: "user" },
    owner: "INFRA controller",
    rank: id,
    state: "live",
    title,
    work: {
      by_status: {},
      claimed: 0,
      closed_last_7_days: 0,
      newest_event_at: null,
      open_asks: 0,
    },
  };
}

/** The read's answer, in rank order: the line's order ("P2") is a row's place in this list. */
const body: Priorities = {
  priorities: [
    priority("evals-now", "Evals collecting data now"),
    priority("rocq-lean", "Rocq and Lean speed work restarted"),
    priority("oss-verify", "OSS Verify: curl, TLS and Rustls"),
  ],
  priority_work_parked: { by_priority: {}, count: 0, issues: [] },
  work_under_no_priority: { by_project: {}, count: 0, issues: [] },
};

/** The revision of the issue's own set the line's read answered, which each press carries. */
const REVISION = 41;

// Queries are scoped to this render's container: bun runs every test file in one document, and
// a sibling file's un-unmounted render would otherwise leak its links into a `screen`-wide query.
function renderLine(advances: Issue["advances"], closed = false) {
  const listPriorities = spyOn(api, "listPriorities").mockResolvedValue(body);
  const patchIssue = answerIssuePatches();
  patchIssue.mockResolvedValue({} as never);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidations = spyOn(queryClient, "invalidateQueries");
  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={queryClient}>
        <IssuePriorityLine
          disabled={closed}
          issue={{ advances, advances_revision: REVISION, key: "CORE-9" }}
        />
      </QueryClientProvider>
    </MemoryRouter>
  );
  return {
    ...view,
    invalidations,
    listPriorities,
    patchIssue,
    q: within(view.container),
    unmountAll: () => {
      view.unmount();
      listPriorities.mockRestore();
      patchIssue.mockRestore();
    },
  };
}

/** The listbox of the list `trigger` opened. The list is portalled to the body, outside this
 *  render's container, so it is found through the panel its trigger names (`aria-controls`). */
function openedList(trigger: HTMLElement): HTMLElement {
  const panel = document.getElementById(trigger.getAttribute("aria-controls") ?? "");
  if (panel === null) throw new Error("the trigger names no open list");
  return within(panel).getByRole("listbox");
}

test("the line names the priority an issue advances, by its order and title, and links to its page", async () => {
  const view = renderLine([{ id: "rocq-lean", inherited: false }]);
  try {
    const link = await view.q.findByRole("link", {
      name: "P2 Rocq and Lean speed work restarted",
    });
    expect(link).toHaveAttribute("href", "/priorities/rocq-lean");
    expect(view.q.getByTestId("issue-advances")).toHaveTextContent("Advances");
    // The issue's own relation is the issue's to retract, so the entry carries the remove.
    view.q.getByRole("button", {
      name: "Remove P2 Rocq and Lean speed work restarted from CORE-9",
    });
    expect(view.q.queryByText("inherited")).toBeNull();
  } finally {
    view.unmountAll();
  }
});

test("a priority an issue inherits says so on the same line, and is not the child's to remove", async () => {
  const view = renderLine([{ id: "oss-verify", inherited: true }]);
  try {
    const link = await view.q.findByRole("link", { name: "P3 OSS Verify: curl, TLS and Rustls" });
    expect(link).toHaveAttribute("href", "/priorities/oss-verify");
    view.q.getByText("inherited");
    expect(
      view.q.queryByRole("button", {
        name: "Remove P3 OSS Verify: curl, TLS and Rustls from CORE-9",
      })
    ).toBeNull();
  } finally {
    view.unmountAll();
  }
});

test("an issue that advances none carries no line and no empty label, and reads no priority", async () => {
  const view = renderLine([]);
  try {
    expect(view.q.getByTestId("issue-advances")).not.toHaveTextContent("Advances");
    expect(view.q.queryByRole("link")).toBeNull();
    // Only the way to place the issue under one, which a person reaches from a fault row.
    view.q.getByRole("button", { name: "Add a priority to CORE-9" });
    // The list of priorities waits for that control: an issue advancing none reads nothing.
    await waitFor(() => expect(view.listPriorities).not.toHaveBeenCalled());
  } finally {
    view.unmountAll();
  }
});

test("the control adds a priority, writing the issue's own set as advances", async () => {
  const user = userEvent.setup();
  const view = renderLine([{ id: "evals-now", inherited: false }]);
  try {
    await user.click(await view.q.findByRole("button", { name: "Add a priority to CORE-9" }));
    const picker = await view.q.findByRole("button", { name: "Priority to advance" });
    await waitFor(() => expect(view.listPriorities).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(picker).toHaveTextContent("Choose a priority"));
    await user.click(picker);
    const list = openedList(picker);
    // A priority the issue already advances is no choice; the rest are offered in rank order.
    expect(
      within(list)
        .getAllByRole("option")
        .map((option) => option.textContent)
    ).toEqual(["P2 Rocq and Lean speed work restarted", "P3 OSS Verify: curl, TLS and Rustls"]);
    await user.click(
      within(list).getByRole("option", { name: "P2 Rocq and Lean speed work restarted" })
    );
    await user.click(view.q.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(view.patchIssue).toHaveBeenCalledTimes(1));
    // The whole own set, with the revision of the read it was drawn from.
    expect(view.patchIssue).toHaveBeenCalledWith(
      "CORE-9",
      { advances: ["evals-now", "rocq-lean"], advances_revision: REVISION },
      keyedAttempt
    );
  } finally {
    view.unmountAll();
  }
});

test("a remove writes the issue's own set without that priority, and leaves an inherited one alone", async () => {
  const user = userEvent.setup();
  const view = renderLine([
    { id: "evals-now", inherited: false },
    { id: "rocq-lean", inherited: false },
    { id: "oss-verify", inherited: true },
  ]);
  try {
    await user.click(
      await view.q.findByRole("button", { name: "Remove P1 Evals collecting data now from CORE-9" })
    );
    await waitFor(() => expect(view.patchIssue).toHaveBeenCalledTimes(1));
    expect(view.patchIssue).toHaveBeenCalledWith(
      "CORE-9",
      { advances: ["rocq-lean"], advances_revision: REVISION },
      keyedAttempt
    );
  } finally {
    view.unmountAll();
  }
});

// The race the revision closes: this tab read {evals-now}; another writer replaced evals-now with
// oss-verify in one whole-set write; the tab removes evals-now and sends []. Sent bare, the server
// would read that as one act and retract oss-verify, which this reader never saw. Sent with the
// revision this tab read, it is refused: the reader is told why, and the issue is read again so
// the next press carries the revision it holds now.
test("a remove carries the revision it was read at, and a stale one's refusal is shown", async () => {
  const user = userEvent.setup();
  const view = renderLine([{ id: "evals-now", inherited: false }]);
  const refusal =
    "advances revision 41 is not the revision this issue holds, 57: read the issue again and write the set you mean against 57";
  view.patchIssue.mockRejectedValue(
    new ApiError(409, { code: "ADVANCES_REVISION", error: refusal })
  );
  try {
    await user.click(
      await view.q.findByRole("button", { name: "Remove P1 Evals collecting data now from CORE-9" })
    );
    await waitFor(() => expect(view.patchIssue).toHaveBeenCalledTimes(1));
    expect(view.patchIssue).toHaveBeenCalledWith(
      "CORE-9",
      { advances: [], advances_revision: REVISION },
      keyedAttempt
    );
    expect((await view.q.findByRole("alert")).textContent).toBe(refusal);
    expect(view.invalidations).toHaveBeenCalledWith({ queryKey: ["issue"] });
  } finally {
    view.unmountAll();
  }
});

test("a closed issue reads its priorities and writes none of them", async () => {
  const view = renderLine([{ id: "rocq-lean", inherited: false }], true);
  try {
    await view.q.findByRole("link", { name: "P2 Rocq and Lean speed work restarted" });
    expect(
      view.q.getByRole("button", {
        name: "Remove P2 Rocq and Lean speed work restarted from CORE-9",
      })
    ).toBeDisabled();
    expect(view.q.getByRole("button", { name: "Add a priority to CORE-9" })).toBeDisabled();
  } finally {
    view.unmountAll();
  }
});

// INFRA-3135, R3: "Every list of options a reader picks from holds a text box that narrows the
// options." The issue header's Priority to advance is such a list: its options are every
// priority the issue does not advance yet, which grow with the priorities page. Opened, it holds
// a text box of its own, a word typed into it leaves only the priorities that match, and a pick
// stages that priority, with the focus back on the control and nothing written until Add.
test("the Priority to advance list holds a text box that narrows the priorities, and a pick stages the one Add writes", async () => {
  const user = userEvent.setup();
  const view = renderLine([]);
  const page = within(document.body);
  try {
    await user.click(view.q.getByRole("button", { name: "Add a priority to CORE-9" }));
    const before = new Set(textBoxes());
    await user.click(await view.q.findByRole("button", { name: "Priority to advance" }));
    await waitFor(() =>
      expect(page.queryAllByRole("option", { name: /^P3 OSS Verify\b/ }).length).toBeGreaterThan(0)
    );
    const own = textBoxes().filter((box) => !before.has(box));
    expect(own, "the Priority to advance list holds no text box of its own").toHaveLength(1);
    const list = listOf(own[0] as HTMLElement);
    await user.type(own[0] as HTMLElement, "oss");
    await waitFor(() =>
      expect(
        within(list).queryAllByRole("option", { name: /^P1 Evals\b/ }),
        `"oss" leaves P1 in the Priority to advance list`
      ).toHaveLength(0)
    );
    expect(
      within(list).queryAllByRole("option", { name: /^P2 Rocq\b/ }),
      `"oss" leaves P2 in the Priority to advance list`
    ).toHaveLength(0);
    await user.click(within(list).getByRole("option", { name: /^P3 OSS Verify\b/ }));
    const picker = view.q.getByRole("button", { name: "Priority to advance" });
    expect(picker).toHaveFocus();
    expect(picker).toHaveAccessibleDescription("P3 OSS Verify: curl, TLS and Rustls");
    expect(view.patchIssue).not.toHaveBeenCalled();

    await user.click(view.q.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(view.patchIssue).toHaveBeenCalledTimes(1));
    expect(view.patchIssue).toHaveBeenCalledWith(
      "CORE-9",
      { advances: ["oss-verify"], advances_revision: REVISION },
      keyedAttempt
    );
  } finally {
    view.unmountAll();
  }
});
