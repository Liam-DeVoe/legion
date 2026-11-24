import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
// biome-ignore lint/style/noRestrictedImports: the drag of a row onto another below is a drag and a drop, which user-event has no form for
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { api } from "../../api/client";
import type { IssueSummary, Priorities, Priority, PriorityRow } from "../../api/types";
import { PrioritiesPage } from "./PrioritiesPage";

function priority(id: string, overrides: Partial<PriorityRow> = {}): PriorityRow {
  return {
    id,
    title: `Priority ${id}`,
    description: "",
    owner: "INFRA controller",
    rank: id,
    state: "live",
    made_at: "2026-11-01T00:00:00Z",
    made_by: { kind: "user", id: "alice" },
    work: {
      by_status: null,
      claimed: 0,
      closed_last_7_days: 0,
      newest_event_at: null,
      open_asks: 0,
    },
    ...overrides,
  };
}

function body(overrides: Partial<Priorities> = {}): Priorities {
  return {
    priorities: [],
    work_under_no_priority: { by_project: null, count: 0, issues: null },
    priority_work_in_backlog_or_icebox: { by_priority: null, count: 0, issues: null },
    ...overrides,
  };
}

/** The deployment's issues as the listing route answers them, standing in whatever status and
 *  advancing whatever priorities `advances` and `overrides` give. */
function issue(
  key: string,
  advances: string[] = [],
  overrides: Partial<IssueSummary> = {}
): IssueSummary {
  return {
    key,
    advances: advances.map((id) => ({ id, inherited: false })),
    advances_revision: 0,
    title: `Work ${key}`,
    status: "in_progress",
    priority: null,
    rank: key,
    labels: [],
    parent: null,
    blocked_by: [],
    assignee: null,
    claim: null,
    components: { mode: "inherit", ids: [], unknown: [], reason: null, inherited_from: null },
    route: null,
    repo: null,
    resolved_repo: null,
    route_status: null,
    route_holder: null,
    route_status_reason: null,
    last_seq: 2,
    open_asks: 0,
    progress: { tasks: null, children: null },
    primary_artifact_id: "spec",
    closed_at: null,
    created_by: { kind: "user", id: "alice" },
    created_at: "2026-11-01T00:00:00Z",
    updated_at: "2026-11-01T00:00:00Z",
    ...overrides,
  };
}

const saved = (id: string): Priority => ({
  id,
  title: `Priority ${id}`,
  description: "",
  owner: "",
  rank: "n",
  state: "live",
  made_at: "2026-11-01T00:00:00Z",
  made_by: { kind: "user", id: "alice" },
});

const restores: (() => void)[] = [];
afterEach(() => {
  for (const restore of restores.splice(0)) restore();
  localStorage.clear();
});

/** The listing route as the server answers it: `advances` keeps the issues advancing any of the
 *  ids it names, `none` those advancing none and `any` those advancing one; `status` keeps the
 *  issues standing in one of its statuses; `open` leaves the closed out; and the move into
 *  backlog or icebox is carried only when `moved` asks for it. */
function answerListing(issues: readonly IssueSummary[]) {
  return async (options: Parameters<typeof api.listIssues>[0] = {}) =>
    issues
      .filter((row) => {
        const ids = (row.advances ?? []).map((relation) => relation.id);
        return (
          (options.advances === undefined ||
            options.advances.some((asked) =>
              asked === "none"
                ? ids.length === 0
                : asked === "any"
                  ? ids.length > 0
                  : ids.includes(asked)
            )) &&
          (options.status === undefined || options.status.includes(row.status)) &&
          (!options.open || row.status !== "done")
        );
      })
      .map(({ moved_at, moved_by, ...row }) =>
        options.moved ? { ...row, moved_at, moved_by } : row
      );
}

/** The page as a signed-in person sees it at `at`, with the reads it makes answered from
 *  `answer`; a reader who is not a person, and so may not write a priority, where `writer` is
 *  false. `remount` loads the page again in the same browser, as a reader's next visit does. */
function page(answer: Priorities, issues: IssueSummary[] = [], writer = true, at = "/priorities") {
  const priorities = spyOn(api, "listPriorities").mockResolvedValue(answer);
  const listing = spyOn(api, "listIssues").mockImplementation(answerListing(issues));
  const state = spyOn(api, "getMyState").mockResolvedValue({});
  const agents = spyOn(api, "listAgents").mockResolvedValue([]);
  // `useIsHuman`: the contract types only a person's answer, so the other principal is cast.
  const whoami = writer
    ? { event_head: "0", kind: "user" as const, login: "alice" }
    : ({ event_head: "0", kind: "token", login: "ci" } as never);
  const viewer = spyOn(api, "whoAmI").mockResolvedValue(whoami);
  const create = spyOn(api, "createPriority").mockResolvedValue(saved("new"));
  const patch = spyOn(api, "patchPriority").mockImplementation(async (id) => saved(id));
  // The unit DOM lays nothing out: give every table's box the height a window would.
  const descriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientHeight");
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get() {
      return this.dataset.testid?.endsWith("-scroll") ? 600 : (descriptor?.get?.call(this) ?? 0);
    },
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(["whoami"], whoami);
  const tree = (
    <MemoryRouter initialEntries={[at]}>
      <QueryClientProvider client={client}>
        <PrioritiesPage />
      </QueryClientProvider>
    </MemoryRouter>
  );
  let view = render(tree);
  restores.push(() => {
    view.unmount();
    client.clear();
    for (const spy of [priorities, listing, state, agents, viewer, create, patch])
      spy.mockRestore();
    if (descriptor) Object.defineProperty(HTMLElement.prototype, "clientHeight", descriptor);
    else Reflect.deleteProperty(HTMLElement.prototype, "clientHeight");
  });
  const remount = () => {
    view.unmount();
    view = render(tree);
  };
  return { create, listing, patch, remount };
}

const FIFTEEN = body({
  priorities: [
    priority("legion-first", {
      title: "Legion works reliably first",
      description: "The first line.\nThe rest of it, which the row shows when it is open.",
      owner: "INFRA controller",
      work: {
        by_status: { in_progress: 5, testing: 1, needs_review: 1, retro: 0, todo: 9, triage: 3 },
        claimed: 2,
        closed_last_7_days: 22,
        newest_event_at: "2026-11-11T10:00:00Z",
        open_asks: 1,
      },
    }),
    priority("linkage-closure", {
      title: "Linkage closure eval",
      owner: "BET controller",
      work: {
        by_status: { in_progress: 1, backlog: 2, icebox: 1 },
        claimed: 0,
        closed_last_7_days: 2,
        newest_event_at: null,
        open_asks: 0,
      },
    }),
    priority("oss-verify", { title: "OSS Verify", owner: "OSSV controller" }),
  ],
});

test("the page draws the priorities table above both fault lists", async () => {
  page(
    body({
      ...FIFTEEN,
      work_under_no_priority: { by_project: { CORE: 1 }, count: 1, issues: null },
      priority_work_in_backlog_or_icebox: {
        by_priority: { "linkage-closure": 1 },
        count: 1,
        issues: null,
      },
    }),
    [issue("CORE-1"), issue("CORE-9", ["linkage-closure"], { status: "backlog" })]
  );
  const priorities = await screen.findByRole("table", { name: "Priorities table" });
  const faults = await screen.findByRole("table", {
    name: "Issues in flight under no priority table",
  });
  const backlog = await screen.findByRole("table", {
    name: "Priority issues in backlog or icebox table",
  });
  const headings = screen.getAllByRole("heading").map((heading) => heading.textContent);
  expect(headings).toEqual([
    "Priorities",
    "In flight under no priority",
    "Priority work in backlog or icebox",
  ]);
  // The priorities come first, which is what the page's own name says it is about.
  expect(priorities.compareDocumentPosition(faults) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
    Node.DOCUMENT_POSITION_FOLLOWING
  );
  expect(faults.compareDocumentPosition(backlog) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
    Node.DOCUMENT_POSITION_FOLLOWING
  );
  // Each fault stands under its rule in plain words, with its count and where it falls.
  expect(screen.getByText(/advances no priority, its own or one it inherits/)).toBeDefined();
  expect(screen.getByText(/stands in the backlog or the icebox/)).toBeDefined();
  expect(screen.getByText(/1 issue · CORE 1/)).toBeDefined();
  expect(screen.getByText(/1 issue · Linkage closure eval 1/)).toBeDefined();
  // Each list holds its own fault's rows and no other's.
  expect(await within(faults).findByRole("row", { name: "CORE-1 Work CORE-1" })).toBeDefined();
  expect(within(faults).queryByRole("row", { name: "CORE-9 Work CORE-9" })).toBeNull();
  expect(await within(backlog).findByRole("row", { name: "CORE-9 Work CORE-9" })).toBeDefined();
  expect(within(backlog).queryByRole("row", { name: "CORE-1 Work CORE-1" })).toBeNull();
});

test("each fault table asks the issue listing for its statuses, and the backlog-or-icebox one for the move", async () => {
  const user = userEvent.setup();
  const { listing } = page(FIFTEEN, [
    issue("CORE-1"),
    issue("OPS-12", [], { status: "needs_review" }),
    issue("CORE-9", ["linkage-closure"], {
      status: "backlog",
      moved_at: "2026-11-09T12:00:00Z",
      moved_by: { kind: "user", id: "bob" },
    }),
    issue("OPS-4", ["legion-first", "oss-verify"], { status: "icebox" }),
  ]);
  const faults = await screen.findByRole("table", {
    name: "Issues in flight under no priority table",
  });
  const backlog = await screen.findByRole("table", {
    name: "Priority issues in backlog or icebox table",
  });
  // The narrowing is the server's: the no-priority list names the statuses work is done in, and
  // the list in backlog or icebox those two statuses with the move into them. Neither reads a
  // closed issue.
  expect(listing).toHaveBeenCalledWith({
    advances: ["none"],
    status: ["in_progress", "testing", "needs_review", "retro"],
    open: true,
  });
  expect(listing).toHaveBeenCalledWith({
    advances: ["any"],
    status: ["backlog", "icebox"],
    moved: true,
    open: true,
  });
  // A fault read names its statuses every time it is made: no read of every closed issue goes
  // out beside it.
  const faultReads = listing.mock.calls.filter(([options]) =>
    ["none", "any"].includes(options?.advances?.[0] ?? "")
  );
  expect(faultReads.length).toBeGreaterThan(0);
  expect(faultReads.every(([options]) => options?.status !== undefined)).toBe(true);

  const cellsOf = async (table: HTMLElement, name: string) => {
    const headers = within(table)
      .getAllByRole("columnheader")
      .map((header) => header.textContent ?? "");
    const row = await within(table).findByRole("row", { name });
    const cells = within(row).getAllByRole("cell");
    return (header: string) => {
      const at = headers.findIndex((text) => text.startsWith(header));
      if (at === -1) throw new Error(`no ${header} column`);
      const cell = cells[at];
      if (cell === undefined) throw new Error(`no ${header} cell`);
      return cell;
    };
  };

  // The no-priority list names each row's project and when it last changed, which is how long it
  // has stood as it is (R6), and shows none of the columns of the list in backlog or icebox.
  const inFlight = await cellsOf(faults, "OPS-12 Work OPS-12");
  expect(inFlight("Project").textContent).toBe("OPS");
  expect(within(inFlight("Updated")).getByRole("time").getAttribute("datetime")).toBe(
    "2026-11-01T00:00:00Z"
  );
  const faultHeaders = within(faults)
    .getAllByRole("columnheader")
    .map((header) => header.textContent ?? "");
  expect(faultHeaders.some((text) => text.startsWith("Advances"))).toBe(false);
  expect(faultHeaders.some((text) => text.startsWith("Moved there"))).toBe(false);
  expect(faultHeaders.some((text) => text.startsWith("Reason"))).toBe(false);

  // The list in backlog or icebox names the project, each priority advanced by its title, linking
  // to it, and when the work was moved there and by whom. A row answered with no move draws an
  // empty Moved there cell: absent is absent, and that cell does not say a record is missing.
  const moved = await cellsOf(backlog, "CORE-9 Work CORE-9");
  expect(moved("Project").textContent).toBe("CORE");
  expect(within(moved("Updated")).getByRole("time").getAttribute("datetime")).toBe(
    "2026-11-01T00:00:00Z"
  );
  expect(
    within(moved("Advances"))
      .getByRole("link", { name: "Linkage closure eval" })
      .getAttribute("href")
  ).toBe("/priorities/linkage-closure");
  expect(within(moved("Moved there")).getByRole("time").getAttribute("datetime")).toBe(
    "2026-11-09T12:00:00Z"
  );
  expect(moved("Moved there").textContent).toContain("bob");
  const unrecorded = await cellsOf(backlog, "OPS-4 Work OPS-4");
  expect(
    within(unrecorded("Advances"))
      .getAllByRole("link")
      .map((link) => link.textContent)
  ).toEqual(["Legion works reliably first", "OSS Verify"]);
  expect(unrecorded("Moved there").textContent).toBe("");
  expect(within(unrecorded("Moved there")).queryByRole("time")).toBeNull();
  // Why it was moved there: nothing records that yet, so every row says so, naming the work that
  // records one as a link to it (R6).
  for (const reason of [moved("Reason"), unrecorded("Reason")]) {
    expect(reason.textContent).toBe("Not recorded until INFRA-2934");
    expect(within(reason).getByRole("link", { name: "INFRA-2934" }).getAttribute("href")).toBe(
      "/issues/INFRA-2934"
    );
  }

  // Nor does the list under no priority offer the columns of the list in backlog or icebox among
  // those a reader may show.
  await user.click(
    within(screen.getByRole("region", { name: "In flight under no priority" })).getByRole(
      "button",
      { name: "Columns" }
    )
  );
  const menu = screen.getByRole("dialog", { name: "Columns" });
  expect(within(menu).getByRole("checkbox", { name: "Project" })).toBeChecked();
  for (const name of ["Advances", "Moved there", "Reason"])
    expect(within(menu).queryByRole("checkbox", { name })).toBeNull();
});

test("with no priority the page says so, and offers a writer the way to add the first", async () => {
  const user = userEvent.setup();
  const { create } = page(body({ empty: "No priority has been written yet." }));
  expect(await screen.findByText("No priority has been written yet.")).toBeDefined();
  expect(screen.queryByRole("table", { name: "Priorities table" })).toBeNull();
  await user.click(screen.getByRole("button", { name: "New priority" }));
  await user.type(screen.getByRole("textbox", { name: "Title of the new priority" }), "First");
  await user.click(screen.getByRole("button", { name: "Add priority" }));
  await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
  expect(create.mock.calls[0]?.[0]).toEqual({ title: "First" });
});

test("with no priority a reader who may not write one is shown only the sentence", async () => {
  page(body({ empty: "No priority has been written yet." }), [], false);
  expect(await screen.findByText("No priority has been written yet.")).toBeDefined();
  expect(screen.queryByRole("button", { name: "New priority" })).toBeNull();
});

test("a row counts the work in flight, waiting, in backlog or icebox and closed in the last 7 days", async () => {
  page(FIFTEEN);
  const row = await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  const cells = within(row)
    .getAllByRole("cell")
    .map((cell) => cell.textContent);
  // In flight is in_progress, testing, needs_review and retro; waiting is todo and triage; the
  // third count is backlog and icebox; the fourth is what the read says closed in 7 days.
  expect(cells.slice(3, 7)).toEqual(["7", "12", "0", "22"]);
  // The newest event is read as a time, whose words follow the clock.
  expect(within(row).getByRole("time").getAttribute("datetime")).toBe("2026-11-11T10:00:00Z");
  const second = screen.getByRole("row", { name: "P2 Linkage closure eval" });
  expect(
    within(second)
      .getAllByRole("cell")
      .map((cell) => cell.textContent)
      .slice(3)
  ).toEqual(["1", "0", "3", "2", "Never"]);
});

test("the first line of a description shows on a closed row and the whole of it when it opens", async () => {
  const user = userEvent.setup();
  page(FIFTEEN, [issue("CORE-1", ["legion-first"]), issue("CORE-2")]);
  await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  expect(screen.getByText("The first line.")).toBeDefined();
  await user.click(screen.getByRole("button", { name: "Open P1 Legion works reliably first" }));
  // Open, the row reads the whole description rendered, and a click on its text opens its source.
  const rendered = await screen.findByRole("group", { name: "Description of P1" });
  expect(await within(rendered).findByText(/The rest of it/)).toBeDefined();
  await user.click(within(rendered).getByText(/The rest of it/));
  const whole = await screen.findByRole("textbox", { name: "Description of P1" });
  expect((whole as HTMLTextAreaElement).value).toBe(
    "The first line.\nThe rest of it, which the row shows when it is open."
  );
});

test("opening a row lists the work advancing that priority, over every project", async () => {
  const user = userEvent.setup();
  const { listing } = page(FIFTEEN, [issue("CORE-1", ["legion-first"]), issue("OPS-3")]);
  await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  await user.click(screen.getByRole("button", { name: "Open P1 Legion works reliably first" }));
  const work = await screen.findByRole("region", {
    name: "Work advancing Legion works reliably first",
  });
  expect(await within(work).findByRole("row", { name: "CORE-1 Work CORE-1" })).toBeDefined();
  expect(within(work).queryByRole("row", { name: "OPS-3 Work OPS-3" })).toBeNull();
  // The narrowing is the server's, and it names no project: a priority belongs to none.
  expect(listing).toHaveBeenCalledWith({ advances: ["legion-first"], open: true });
});

test("a person who may write adds a priority", async () => {
  const user = userEvent.setup();
  const { create } = page(FIFTEEN);
  await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  await user.click(screen.getByRole("button", { name: "New priority" }));
  await user.type(
    screen.getByRole("textbox", { name: "Title of the new priority" }),
    "Spend ruthlessly"
  );
  await user.click(screen.getByRole("button", { name: "Add priority" }));
  await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
  expect(create.mock.calls[0]?.[0]).toEqual({ title: "Spend ruthlessly" });
});

test("a title is renamed where it is read, with no edit mode and no Save button", async () => {
  const user = userEvent.setup();
  const { patch } = page(FIFTEEN);
  const field = await screen.findByRole("textbox", { name: "Title of P2" });
  await user.clear(field);
  await user.type(field, "Linkage closure, restarted");
  await user.tab();
  await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
  expect(patch.mock.calls[0]?.slice(0, 2)).toEqual([
    "linkage-closure",
    { title: "Linkage closure, restarted" },
  ]);
  expect(screen.queryByRole("button", { name: /^Save/ })).toBeNull();
});

test("a description is written on the open row: the empty block is the prompt that opens it", async () => {
  const user = userEvent.setup();
  const { patch } = page(FIFTEEN);
  await screen.findByRole("row", { name: "P3 OSS Verify" });
  await user.click(screen.getByRole("button", { name: "Open P3 OSS Verify" }));
  const rendered = await screen.findByRole("group", { name: "Description of P3" });
  const prompt = within(rendered).getByText(/No description is written\./);
  expect(prompt.textContent).toBe("No description is written. Click here to write one.");
  await user.click(prompt);
  const field = await screen.findByRole("textbox", { name: "Description of P3" });
  await user.type(field, "What it is.");
  await user.tab();
  await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
  expect(patch.mock.calls[0]?.slice(0, 2)).toEqual(["oss-verify", { description: "What it is." }]);
  // Left, the description reads rendered again, with what was written.
  const back = await screen.findByRole("group", { name: "Description of P3" });
  expect(await within(back).findByText("What it is.")).toBeDefined();
});

test("dragging a row onto another writes the place it landed", async () => {
  const { patch } = page(FIFTEEN);
  const third = await screen.findByRole("row", { name: "P3 OSS Verify" });
  const first = screen.getByRole("row", { name: "P1 Legion works reliably first" });
  // user-event has no form for a drag: it drives pointer events, and the order is moved by the
  // HTML drag-and-drop events the handle listens for.
  fireEvent.dragStart(
    within(third).getByRole("button", {
      name: "Move P3 OSS Verify: drag it, or press the up and down arrows",
    })
  );
  // user-event has no form for a drag.
  fireEvent.dragOver(first);
  // user-event has no form for a drop.
  fireEvent.drop(first);
  await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
  // Dragged up, it lands above the row it was dropped on.
  expect(patch.mock.calls[0]?.slice(0, 2)).toEqual([
    "oss-verify",
    { rank: { before: "legion-first" } },
  ]);
});

test("the keyboard moves a priority one place, as the drag does", async () => {
  const user = userEvent.setup();
  const { patch } = page(FIFTEEN);
  const first = await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  within(first)
    .getByRole("button", {
      name: "Move P1 Legion works reliably first: drag it, or press the up and down arrows",
    })
    .focus();
  await user.keyboard("{ArrowDown}");
  await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
  expect(patch.mock.calls[0]?.slice(0, 2)).toEqual([
    "legion-first",
    { rank: { after: "linkage-closure" } },
  ]);
});

test("the table's own view is in the address under its prefix", async () => {
  const user = userEvent.setup();
  page(FIFTEEN);
  await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  await user.type(screen.getByRole("searchbox", { name: "Search priorities" }), "linkage");
  await waitFor(() =>
    expect(screen.queryByRole("row", { name: "P1 Legion works reliably first" })).toBeNull()
  );
  expect(screen.getByRole("row", { name: "P2 Linkage closure eval" })).toBeDefined();
});

test("the page carries no estimate, no forecast, no percentage and no written status", async () => {
  page(FIFTEEN);
  await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  const text = document.body.textContent ?? "";
  expect(/\d+%/.test(text)).toBe(false);
  expect(/estimate|forecast|on track|at risk/i.test(text)).toBe(false);
});

test("the priorities table counts its rows as priorities, as the agreed page reads", async () => {
  page(FIFTEEN);
  await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  // dispatch://INFRA-2947/artifact/priorities-round10-png reads "15 of 15 priorities" under the
  // table: a priority is no issue, and a reader counting the rows is told what they count.
  expect(screen.getByText("3 of 3 priorities")).toBeDefined();
  expect(screen.queryByText("3 of 3 issues")).toBeNull();
});

test("a fault row tells no reader that an issue nobody claims is claimed", async () => {
  page(FIFTEEN, [
    issue("CORE-1", [], { assignee: "alice" }),
    issue("CORE-2", [], {
      assignee: "alice",
      claim: { actor: { kind: "user", id: "bob" }, at: "2026-11-10T00:00:00Z" },
    }),
  ]);
  const faults = await screen.findByRole("table", {
    name: "Issues in flight under no priority table",
  });
  await within(faults).findByRole("row", { name: "CORE-1 Work CORE-1" });
  const headers = within(faults)
    .getAllByRole("columnheader")
    .map((header) => header.textContent ?? "");
  const claimedBy = headers.findIndex((header) => header.includes("Claimed by"));
  expect(claimedBy).not.toBe(-1);
  const cellUnder = (key: string) =>
    within(within(faults).getByRole("row", { name: `${key} Work ${key}` })).getAllByRole("cell")[
      claimedBy
    ]?.textContent ?? "";
  // The Issues table reads "Claimed by" as the claim alone (its ClaimChip). Under that header
  // an unclaimed issue names nobody, whoever it is assigned to, and a claimed one its claimant.
  expect(cellUnder("CORE-1")).not.toContain("alice");
  expect(cellUnder("CORE-2")).toContain("bob");
});

test("a grouped table counts each group in the plural its rows are called by", async () => {
  const user = userEvent.setup();
  page(
    body({
      priorities: [
        priority("legion-first", { title: "Legion works reliably first" }),
        priority("oss-verify", { title: "OSS Verify" }),
      ],
      work_under_no_priority: { by_project: { CORE: 2 }, count: 2, issues: null },
    }),
    [issue("CORE-1"), issue("CORE-2")]
  );
  await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  const groupBy = (region: string) =>
    within(screen.getByRole("region", { name: region })).getByRole("combobox", {
      name: /^Group by/,
    });
  // No rule makes a plural from a singular: adding "s" to "priority" wrote "2 prioritys", so each
  // table names both words, as its pager does. The priorities table's heading is the one group
  // row every table draws (`DataTableGroupRow`).
  await user.selectOptions(groupBy("Priorities"), "owner");
  expect(
    await screen.findByRole("button", { name: "Collapse INFRA controller, 2 priorities" })
  ).toBeDefined();
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  await user.selectOptions(groupBy("In flight under no priority"), "status");
  expect(
    await screen.findByRole("button", { name: "Collapse In progress, 2 issues" })
  ).toBeDefined();
});

// The stored layouts' move on the page's first render (the plan's state table L8a), one row per
// test. A layout the reader kept under a name the page no longer uses moves to the name it uses
// now on the first render, before the first write, and a second render moves nothing more.

/** The browser keys the page's layouts are stored under (`tableStorageKey`): the list in backlog
 *  or icebox, the key that list kept its layout under before, and the priorities table. */
const BACKLOG_KEY = "dispatch.project-table.priorities-backlog";
const FORMER_BACKLOG_KEY = "dispatch.project-table.priorities-parked";
const PRIORITIES_KEY = "dispatch.project-table.priorities";

/** A layout of the list in backlog or icebox as a reader stores one: the Status column's width,
 *  Status before Key, and one column hidden. */
const backlogLayout = (width: number, hidden: string) =>
  JSON.stringify({
    sizing: { status: width },
    order: ["status", "key"],
    visibility: { [hidden]: false },
  });

const storedAt = (key: string) => JSON.parse(localStorage.getItem(key) ?? "null");

/** Both tables whose layouts move are drawn, so each one's mount effect has written. */
async function bothTablesDrawn() {
  await screen.findByRole("table", { name: "Priorities table" });
  await screen.findByRole("table", { name: "Priority issues in backlog or icebox table" });
}

test("a layout under the list's former key moves to its key, and the former key goes", async () => {
  localStorage.setItem(FORMER_BACKLOG_KEY, backlogLayout(222, "assignee"));
  const { remount } = page(FIFTEEN);
  await bothTablesDrawn();
  await waitFor(() => expect(localStorage.getItem(FORMER_BACKLOG_KEY)).toBeNull());
  const moved = storedAt(BACKLOG_KEY);
  expect(moved.sizing).toEqual({ status: 222 });
  expect(moved.order).toEqual(["status", "key"]);
  expect(moved.visibility.assignee).toBe(false);
  // A second render finds nothing under the former key and moves nothing.
  const first = localStorage.getItem(BACKLOG_KEY);
  remount();
  await bothTablesDrawn();
  expect(localStorage.getItem(FORMER_BACKLOG_KEY)).toBeNull();
  expect(localStorage.getItem(BACKLOG_KEY)).toBe(first);
});

test("a layout already under the list's key stays, and the former key goes", async () => {
  localStorage.setItem(FORMER_BACKLOG_KEY, backlogLayout(222, "assignee"));
  localStorage.setItem(BACKLOG_KEY, backlogLayout(333, "labels"));
  const { remount } = page(FIFTEEN);
  await bothTablesDrawn();
  await waitFor(() => expect(localStorage.getItem(FORMER_BACKLOG_KEY)).toBeNull());
  const kept = storedAt(BACKLOG_KEY);
  expect(kept.sizing).toEqual({ status: 333 });
  expect(kept.visibility.labels).toBe(false);
  expect(kept.visibility.assignee).toBeUndefined();
  const first = localStorage.getItem(BACKLOG_KEY);
  remount();
  await bothTablesDrawn();
  expect(localStorage.getItem(FORMER_BACKLOG_KEY)).toBeNull();
  expect(localStorage.getItem(BACKLOG_KEY)).toBe(first);
});

test("with no layout under either key the page writes only what each table's mount writes", async () => {
  const { remount } = page(FIFTEEN);
  await bothTablesDrawn();
  const tableKeys = () =>
    Object.keys(localStorage)
      .filter((key) => key.startsWith("dispatch.project-table."))
      .sort();
  await waitFor(() =>
    expect(tableKeys()).toEqual([
      PRIORITIES_KEY,
      BACKLOG_KEY,
      "dispatch.project-table.priorities-no-priority",
    ])
  );
  const written = storedAt(BACKLOG_KEY);
  expect(written.sizing).toEqual({});
  expect(written.order).toEqual([]);
  const first = localStorage.getItem(BACKLOG_KEY);
  remount();
  await bothTablesDrawn();
  expect(tableKeys()).toEqual([
    PRIORITIES_KEY,
    BACKLOG_KEY,
    "dispatch.project-table.priorities-no-priority",
  ]);
  expect(localStorage.getItem(BACKLOG_KEY)).toBe(first);
});

test("the priorities table's stored width, place and visibility of its backlog column move to its id now", async () => {
  localStorage.setItem(
    PRIORITIES_KEY,
    JSON.stringify({
      sizing: { owner: 230, parked: 240 },
      order: ["owner", "parked", "title"],
      visibility: { parked: false },
    })
  );
  const moved = JSON.stringify({
    sizing: { owner: 230, backlog: 240 },
    order: ["owner", "backlog", "title"],
    visibility: { backlog: false },
  });
  const { remount } = page(FIFTEEN);
  await bothTablesDrawn();
  await waitFor(() => expect(localStorage.getItem(PRIORITIES_KEY)).toBe(moved));
  // The stored visibility is the reader's: the column stays hidden under its id now.
  const table = screen.getByRole("table", { name: "Priorities table" });
  expect(within(table).queryByRole("columnheader", { name: /^In backlog or icebox/ })).toBeNull();
  remount();
  await bothTablesDrawn();
  expect(localStorage.getItem(PRIORITIES_KEY)).toBe(moved);
});

test("a priorities layout that names the backlog column by its id now is left as it is", async () => {
  const layout = JSON.stringify({
    sizing: { backlog: 250 },
    order: ["owner", "backlog"],
    visibility: { backlog: false },
  });
  localStorage.setItem(PRIORITIES_KEY, layout);
  const { remount } = page(FIFTEEN);
  await bothTablesDrawn();
  expect(localStorage.getItem(PRIORITIES_KEY)).toBe(layout);
  remount();
  await bothTablesDrawn();
  expect(localStorage.getItem(PRIORITIES_KEY)).toBe(layout);
});

test("an older link sorted by the backlog column under its former id sorts by it", async () => {
  page(FIFTEEN, [], true, "/priorities?p.sort=parked");
  const table = await screen.findByRole("table", { name: "Priorities table" });
  const linkage = await within(table).findByRole("row", { name: "P2 Linkage closure eval" });
  expect(
    within(table)
      .getByRole("columnheader", { name: /^In backlog or icebox/ })
      .getAttribute("aria-sort")
  ).toBe("ascending");
  // Linkage closure eval holds the three issues in backlog or icebox and the two others none, so
  // it comes after both, where the order alone puts it between them.
  for (const name of ["P1 Legion works reliably first", "P3 OSS Verify"]) {
    const other = within(table).getByRole("row", { name });
    expect(other.compareDocumentPosition(linkage) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING
    );
  }
});

test("an older link filtered by the backlog column under its former id filters by it", async () => {
  page(FIFTEEN, [], true, "/priorities?p.f.parked=3");
  expect(await screen.findByRole("row", { name: "P2 Linkage closure eval" })).toBeDefined();
  expect(screen.queryByRole("row", { name: "P1 Legion works reliably first" })).toBeNull();
  expect(screen.queryByRole("row", { name: "P3 OSS Verify" })).toBeNull();
});
